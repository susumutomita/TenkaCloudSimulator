"""Exercise the three shipped Cloud Trace v1 Scalar operations through Chromium."""
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from gcp_dns_sdk import LocalTeam, started
from gcp_trace_sdk import api, expect_error

REFERENCE = '/_simulator/docs/gcp-trace.html'
TRACE_ID = '18ef054b00874c78a43cedf89eaf0042'
COLLECTION = '/projects/{projectId}/traces'


def send(context, endpoint, native_path, method, template):
    page = context.new_page()
    requests = []
    failures = []
    page.on('pageerror', lambda err: failures.append(str(err)))
    page.on('request', lambda req: requests.append((req.method, urlsplit(req.url).path))
            if urlsplit(req.url).path.startswith('/_simulator/providers/') else None)
    try:
        assert page.goto(endpoint + REFERENCE).status == 200
        link = page.get_by_role('complementary', name='Sidebar for Cloud Trace v1').locator(f'a[href="#tag/traces/{method}{template}"]')
        link.click()
        action = f'Test Request ({method.lower()} {template})'
        page.get_by_role('button', name=action, exact=True).click()
        assert not requests, requests

        def matches(response):
            return (response.request.method == method and
                    response.url.split('?', 1)[0] == endpoint + native_path)

        with page.expect_response(matches) as pending:
            page.get_by_role('button', name=re.compile(f'^Send {method.lower()} request to ')).click()
        response = pending.value
        assert response.status == 200, (response.status, response.text())
        assert response.request.redirected_from is None
        assert response.request.header_value('cookie')
        for header in ('authorization', 'x-simulator-token', 'x-simulator-principal'):
            assert not response.request.header_value(header), header
        body = response.json()
        rendered = page.locator('.response-section-content-body').filter(
            has=page.get_by_role('link', name='Download Response Body', exact=True))
        expect(rendered).to_have_count(1)
        if method == 'PATCH':
            assert body == {}
            request = response.request.post_data_json['traces'][0]
            assert request['traceId'] == TRACE_ID
            assert request['spans'][0]['spanId'] == '9007199254740993'
        else:
            expect(rendered).to_contain_text(TRACE_ID)
            if template.endswith('{traceId}'):
                expect(rendered).to_contain_text('9007199254740993')
                expect(rendered).to_contain_text('local request')
        assert requests == [(method, native_path)], requests
        assert not failures, failures
        return body
    finally:
        page.close()


def run():
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    binary = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix='trace-scalar-') as directory:
        for name in ('owner', 'peer'):
            (Path(directory) / name).mkdir()
        team = LocalTeam(Path(directory) / 'owner', binary)
        peer = LocalTeam(Path(directory) / 'peer', binary)
        with started(peer), started(team), sync_playwright() as playwright:
            browser = playwright.chromium.launch(executable_path=str(executable), chromium_sandbox=True)
            try:
                context = browser.new_context(service_workers='block')
                context.set_default_timeout(20_000)
                outside = []

                def guard(route):
                    target = urlsplit(route.request.url)
                    if target.scheme != 'http' or target.netloc != f'127.0.0.1:{team.port}':
                        outside.append(target.netloc)
                        route.abort()
                    else:
                        route.continue_()

                context.route('**/*', guard)
                login = context.request.post(team.endpoint + '/_simulator/login', form={'token': team.token},
                                             max_redirects=0)
                assert login.status == 303
                capability = team.control('GET', '/_simulator/v1/capabilities')['gcpTrace']
                project = capability['project']
                assert capability['reference'] == REFERENCE
                assert capability['endpoint'] == '/_simulator/providers/gcp/cloudtrace/v1'
                collection = capability['endpoint'] + f'/projects/{project}/traces'
                owner = api(team, team.token, project).traces()
                other = api(peer, peer.token, project).traces()
                target = {'projectId': project, 'traceId': TRACE_ID}
                expect_error(404, owner.get(**target))
                expect_error(404, other.get(**target))
                send(context, team.endpoint, collection, 'PATCH', COLLECTION)
                saved = owner.get(**target).execute(num_retries=0)
                assert len(saved['spans']) == 1 and saved['spans'][0]['name'] == 'local request'
                assert saved['spans'][0]['spanId'] == '9007199254740993'
                observed = send(context, team.endpoint, collection + '/' + TRACE_ID, 'GET', COLLECTION + '/{traceId}')
                assert observed == saved
                listing = send(context, team.endpoint, collection, 'GET', COLLECTION)
                assert listing == {'traces': [target]}
                expect_error(404, other.get(**target))
                assert owner.get(**target).execute(num_retries=0) == saved
                assert not outside, outside
            finally:
                browser.close()
    assert not Path(directory).exists()
    print('Cloud Trace v1: three real Scalar Sends, official SDK readback, peer isolation and cleanup passed')


if __name__ == '__main__':
    run()
