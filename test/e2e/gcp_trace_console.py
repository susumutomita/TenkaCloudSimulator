"""Opt-in real read-only Cloud Trace Console with official discovery-client state."""
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import parse_qs, urlsplit

from playwright.sync_api import expect, sync_playwright

from gcp_dns_sdk import LocalTeam, started
from gcp_trace_sdk import api, new_reader
from reflow import assert_reflow
from sakura_cli import frame


def run():
    assert importlib.metadata.version('google-api-python-client') == '2.199.0'
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    digest = os.environ['SIMULATOR_BINARY_SHA256']
    assert re.fullmatch('[a-f0-9]{64}', digest)
    with binary.open('rb') as stream:
        assert hashlib.file_digest(stream, 'sha256').hexdigest() == digest
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix='gcp-trace-console-') as directory:
        for name in ('owner', 'peer'):
            (Path(directory) / name).mkdir()
        team = LocalTeam(Path(directory) / 'owner', binary)
        peer = LocalTeam(Path(directory) / 'peer', binary)
        with started(peer), started(team), sync_playwright() as playwright:
            project = team.control('GET', '/_simulator/v1/capabilities')['gcpTrace']['project']
            assert team.port != peer.port and team.token != peer.token
            now = datetime.now(timezone.utc)
            stamp = (now - timedelta(minutes=2)).strftime('%Y-%m-%dT%H:%M:%S') + '.123456789Z'
            end = (now - timedelta(minutes=1)).strftime('%Y-%m-%dT%H:%M:%S') + '.987654321Z'
            spans = [
                {'spanId': '18446744073709551615', 'name': 'request <local>',
                 'startTime': stamp, 'endTime': end, 'kind': 'RPC_SERVER',
                 'labels': {'<label>': '<script>local only</script>'}},
                {'spanId': '9007199254740993', 'parentSpanId': '18446744073709551615',
                 'name': 'child', 'startTime': stamp, 'endTime': end},
                {'spanId': '18446744073709551615', 'name': 'second observation',
                 'startTime': stamp, 'endTime': end},
            ]
            ids = [f'{index:032x}' for index in range(1, 27)]
            owner = api(team, team.token, project)
            other = api(peer, peer.token, project)
            records = [{'projectId': project, 'traceId': identifier,
                        'spans': spans if index == 0 else [spans[0]]}
                       for index, identifier in enumerate(ids)]
            owner.patchTraces(projectId=project, body={'traces': records}).execute(num_retries=0)
            other.patchTraces(projectId=project, body={'traces': [dict(records[0], spans=[dict(spans[0], name='peer only')])]}).execute(num_retries=0)
            participant = new_reader(team)
            bounds = {'startTime': (now - timedelta(hours=1)).isoformat(),
                      'endTime': now.isoformat(), 'view': 'COMPLETE', 'pageSize': 100}

            def saved(client):
                return client.traces().list(projectId=project, **bounds).execute(num_retries=0)

            before, peer_before = saved(owner), saved(other)
            assert len(before['traces']) == 26 and before['traces'][0]['spans'] == spans
            snapshots = frame(team.database), frame(peer.database)
            collection = f'/_simulator/providers/gcp/cloudtrace/v1/projects/{project}/traces'
            outside, errors, wire = [], [], []
            with playwright.chromium.launch(executable_path=str(executable), chromium_sandbox=True) as browser:
                def context_for(token):
                    context = browser.new_context(service_workers='block', viewport={'width': 1440, 'height': 1000})
                    context.set_default_timeout(20_000)

                    def guard(route):
                        target = urlsplit(route.request.url)
                        if target.scheme == 'http' and target.netloc == f'127.0.0.1:{team.port}':
                            route.continue_()
                        else:
                            outside.append(True)
                            route.abort()

                    context.route('**/*', guard)
                    assert context.request.post(team.endpoint + '/_simulator/login', form={'token': token},
                                                max_redirects=0).status == 303
                    return context

                with context_for(team.token) as context:
                    page = context.new_page()
                    page.on('pageerror', lambda error: errors.append(type(error).__name__))

                    def observe(request):
                        target = urlsplit(request.url)
                        if not target.path.startswith('/_simulator/providers/'):
                            return
                        assert request.method == 'GET' and request.post_data is None
                        # Choosing Google Cloud opens its existing Storage landing
                        # before the user selects Trace from Services.
                        if target.path == '/_simulator/providers/gcp/storage/v1/b':
                            assert parse_qs(target.query) == {'project': [project], 'maxResults': ['100']}
                            return
                        assert target.path in (collection, collection + '/' + ids[0])
                        assert request.redirected_from is None and request.header_value('cookie')
                        for header in ('authorization', 'x-simulator-token', 'x-simulator-access-role', 'x-simulator-service'):
                            assert not request.header_value(header)
                        if target.path == collection:
                            query = parse_qs(target.query)
                            assert set(query) in ({'startTime', 'endTime', 'view', 'pageSize'},
                                                  {'startTime', 'endTime', 'view', 'pageSize', 'pageToken'})
                            assert query['view'] == ['MINIMAL'] and query['pageSize'] == ['25']
                        wire.append((target.path, parse_qs(target.query)))

                    page.on('request', observe)
                    page.goto(team.endpoint + '/_simulator/console/')
                    page.get_by_role('button', name='Google Cloud', exact=True).click()
                    page.get_by_role('button', name='Services', exact=True).click()
                    page.get_by_role('menuitem', name='Trace', exact=True).click()
                    expect(page.get_by_role('heading', name='Trace explorer', exact=True)).to_be_visible()
                    table = page.get_by_role('table')
                    expect(table.get_by_role('button', name=ids[0], exact=True)).to_be_visible()
                    expect(table.get_by_role('button', name=re.compile('^[0-9a-f]{32}$'))).to_have_count(25)
                    page.get_by_role('button', name='Next page', exact=True).click()
                    expect(table.get_by_role('button', name=ids[-1], exact=True)).to_be_visible()
                    expect(table.get_by_role('button', name=re.compile('^[0-9a-f]{32}$'))).to_have_count(1)
                    page.get_by_role('button', name='Previous page', exact=True).click()
                    table.get_by_role('button', name=ids[0], exact=True).click()
                    expect(page.get_by_role('heading', name='Trace details', exact=True)).to_be_visible()
                    rows = table.get_by_role('row')
                    expect(rows).to_have_count(4)
                    for index, span in enumerate(spans, 1):
                        row = rows.nth(index)
                        expect(row.get_by_role('cell').nth(0)).to_have_text(str(index))
                        expect(row.get_by_role('cell').nth(1)).to_have_text(span['spanId'])
                        expect(row.get_by_role('cell').nth(2)).to_have_text(span.get('parentSpanId', '—'))
                        expect(row.get_by_role('cell').nth(3)).to_have_text(span['name'])
                        expect(row.get_by_role('cell').nth(4)).to_have_text(span.get('kind', '—'))
                        expect(row.get_by_role('cell').nth(5)).to_have_text(stamp)
                        expect(row.get_by_role('cell').nth(6)).to_have_text(end)
                        assert json.loads(row.get_by_role('cell').nth(7).inner_text()) == span.get('labels', {})
                    expect(rows.nth(1)).to_contain_text('<script>local only</script>')
                    assert page.locator('table script').count() == 0
                    page.get_by_role('button', name='Refresh trace', exact=True).click()
                    expect(rows).to_have_count(4)
                    page.set_viewport_size({'width': 390, 'height': 1000})
                    assert_reflow(page)
                    page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                    expect(page.get_by_role('heading', name='トレースの詳細', exact=True)).to_be_visible()
                    expect(rows.nth(1)).to_contain_text('18446744073709551615')
                    assert_reflow(page)
                    page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                    page.get_by_role('button', name='Back to traces', exact=True).click()
                    expect(table.get_by_role('button', name=ids[0], exact=True)).to_be_visible()
                    start_input = page.get_by_label('Start time (RFC 3339)', exact=True)
                    end_input = page.get_by_label('End time (RFC 3339)', exact=True)
                    valid_start, valid_end = start_input.input_value(), end_input.input_value()
                    start_input.fill(valid_end)
                    end_input.fill(valid_start)
                    with page.expect_response(lambda response: urlsplit(response.url).path == collection) as invalid:
                        page.get_by_role('button', name='Search traces', exact=True).click()
                    assert invalid.value.status == 400
                    expect(page.get_by_text('Could not load trace data', exact=True)).to_be_visible()
                    expect(page.get_by_text('400: endTime precedes startTime.', exact=True)).to_be_visible()
                    start_input.fill(valid_start)
                    end_input.fill(valid_end)
                    page.get_by_role('button', name='Search traces', exact=True).click()
                    expect(table.get_by_role('button', name=ids[0], exact=True)).to_be_visible()
                    expect(page.get_by_text('Could not load trace data', exact=True)).to_have_count(0)
                    assert any('pageToken' in query for _, query in wire)
                assert saved(owner) == before and saved(other) == peer_before
                assert (frame(team.database), frame(peer.database)) == snapshots
                with context_for(participant) as restricted:
                    caps = restricted.request.get(team.endpoint + '/_simulator/v1/capabilities').json()
                    assert 'gcpTrace' not in caps
                    page = restricted.new_page()
                    page.goto(team.endpoint + '/_simulator/console/#/gcp-trace')
                    expect(page.get_by_role('heading', name='Trace explorer', exact=True)).to_have_count(0)
                    expect(page.get_by_text(ids[0], exact=True)).to_have_count(0)
                team.stop()
                team.start()
                assert saved(api(team, team.token, project)) == before and saved(other) == peer_before
                with context_for(team.token) as restarted:
                    page = restarted.new_page()
                    page.goto(team.endpoint + '/_simulator/console/#/gcp-trace')
                    page.get_by_role('button', name=ids[0], exact=True).click()
                    expect(page.get_by_role('heading', name='Trace details', exact=True)).to_be_visible()
                    expect(page.get_by_role('table').get_by_role('row')).to_have_count(4)
                assert not outside and not errors and wire
    assert not Path(directory).exists()
    print(json.dumps({'ok': True, 'binarySha256': digest, 'sdkSeededTraces': 26,
                      'duplicateSpanObservations': 3, 'paging': True, 'nativeErrorRecovery': True,
                      'japaneseNarrow': True, 'peerUnchanged': True, 'restart': True,
                      'participantHidden': True, 'cleanup': True, 'externalCloud': False}))


if __name__ == '__main__':
    run()
