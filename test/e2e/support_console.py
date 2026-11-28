"""Exercise the packaged, signed-in AWS Support console on an owned numeric loopback.

Set SIMULATOR_BINARY to the built CLI. Add --sdk to use the repository's pinned
stock boto3 for independent case creation/readback. AXE_SCRIPT selects a local
axe-core distribution. The two disposable team databases are removed at exit.
"""
import argparse
import base64
from contextlib import closing, ExitStack
from http.client import HTTPConnection
from importlib.metadata import version
import json
import os
from pathlib import Path
import shutil
import tempfile
import time
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright
from reflow import assert_reflow
from sts_session_lifecycle import LocalSimulator

TARGET = 'AWSSupport_20130415.'
REGION = 'us-east-1'
NAME = 'Reading <practice> file.bin'
DATA = bytes([0, 255, 127, 10])
START = 'First line\n<practice note>'
TOPIC = 'Fixture case <local>'
CREATED = 'Console case'
MESSAGE = 'Second <practice> message'


def wire(team, method, path, body=None, operation=''):
    headers = {'X-Simulator-Token': team.token, 'Content-Type': 'application/json'}
    if operation:
        headers.update({'X-Amz-Target': TARGET + operation, 'X-Simulator-Region': REGION,
                        'X-Simulator-Service': 'support', 'Content-Type': 'application/x-amz-json-1.1'})
    with closing(HTTPConnection('127.0.0.1', team.port, timeout=8)) as conn:
        conn.request(method, path, None if body is None else json.dumps(body), headers)
        res = conn.getresponse()
        payload = res.read()
        assert 200 <= res.status < 300, (operation or path, res.status)
        return json.loads(payload) if payload else None


def native(team, use_sdk, stack, receipt):
    if not use_sdk:
        def local(operation, **params):
            if operation == 'AddAttachmentsToSet':
                params['attachments'] = [dict(item, data=base64.b64encode(item['data']).decode('ascii'))
                                         for item in params['attachments']]
            result = wire(team, 'POST', '/', params, operation)
            if operation == 'DescribeAttachment':
                result['attachment']['data'] = base64.b64decode(result['attachment']['data'], validate=True)
            return result
        return local
    import boto3
    from botocore.config import Config
    assert version('botocore') == '1.41.6'
    client = boto3.client('support', endpoint_url=team.endpoint, region_name=REGION,
                          aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='local-fixture-only',
                          config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=3, read_timeout=5))
    stack.callback(client.close)
    client.meta.events.register('before-sign.support',
        lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', team.token))

    def bound(request, **kwargs):
        assert request.url == team.endpoint + '/' and request.method == 'POST'
        assert b'/us-east-1/support/aws4_request' in request.headers['Authorization']
        receipt['sdkSends'] += 1
    client.meta.events.register('before-send.support', bound)
    methods = {operation: getattr(client, method) for method, operation in client.meta.method_to_api_mapping.items()}

    def call(operation, **params):
        result = methods[operation](**params)
        assert result['ResponseMetadata']['HTTPStatusCode'] == 200
        receipt['sdkResponses'] += 1
        return result
    return call


def exercise(binary, use_sdk, axe):
    receipt = {'stockSDK':use_sdk, 'sdkSends':0, 'sdkResponses':0, 'browser':[], 'audits':[]}
    outside, browser_errors = [], []
    with tempfile.TemporaryDirectory(prefix='support-console-') as temp, ExitStack() as stack:
        root = Path(temp)
        team, peer = (LocalSimulator(root / name, binary) for name in ('A', 'B'))
        for instance in (team, peer):
            stack.callback(instance.close)
            instance.start()
        call = native(team, use_sdk, stack, receipt)
        peer_call = native(peer, use_sdk, stack, receipt)
        staged = call('AddAttachmentsToSet', attachments=[{'fileName':NAME, 'data':DATA}])
        first = call('CreateCase', subject=TOPIC, communicationBody=START, attachmentSetId=staged['attachmentSetId'])['caseId']
        def history(case_id):
            return call('DescribeCommunications', caseId=case_id)['communications']
        def selected(case_id):
            rows = call('DescribeCases', caseIdList=[case_id], includeResolvedCases=True)['cases']
            assert len(rows) == 1 and rows[0]['caseId'] == case_id
            return rows[0]

        pw = stack.enter_context(sync_playwright())
        browser = stack.enter_context(pw.chromium.launch(chromium_sandbox=True,
            executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')))

        def open_page(token, lang='en', width=1365, instance=team):
            ctx = browser.new_context(viewport={'width':width, 'height':900}, reduced_motion='reduce',
                                      service_workers='block', accept_downloads=True)
            def bound(route):
                url = urlsplit(route.request.url)
                if url.scheme == 'http' and url.netloc == f'127.0.0.1:{instance.port}':
                    route.continue_()
                else:
                    outside.append(url.scheme)
                    route.abort()
            ctx.route('**/*', bound)
            assert ctx.request.post(instance.endpoint + '/_simulator/login', form={'token':token}, max_redirects=0).status == 303
            ctx.add_init_script("localStorage.setItem('tenkacloud-simulator-locale'," + json.dumps(lang) + ")")
            page = ctx.new_page()
            page.on('pageerror', lambda err: browser_errors.append(str(err)))
            def record(res):
                headers = res.request.headers
                target = headers.get('x-amz-target', '')
                if target.startswith(TARGET):
                    assert res.request.method == 'POST' and res.url == instance.endpoint + '/'
                    assert headers['x-simulator-region'] == REGION and headers['x-simulator-service'] == 'support'
                    assert headers['content-type'] == 'application/x-amz-json-1.1' and 'x-simulator-token' not in headers
                    receipt['browser'].append([target[len(TARGET):], res.status])
            page.on('response', record)
            page.goto(instance.endpoint + '/_simulator/console/#/home')
            page.get_by_role('link', name='AWS Support', exact=True).click()
            return ctx, page

        def click(page, name):
            page.get_by_role('button', name=name, exact=True).click()

        def audit(page, view):
            assert_reflow(page)
            if axe:
                page.evaluate(axe.read_text())
                violations = page.evaluate("""async () => (await axe.run(document, {runOnly:{type:'tag',
                    values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})).violations.map(v=>[v.id,v.nodes.map(n=>n.target)])""")
                assert not violations, (view, violations)
                receipt['audits'].append([view, page.viewport_size['width']])

        ctx, page = open_page(team.token)
        click(page, TOPIC)
        expect(page.get_by_text(first, exact=True)).to_be_visible()
        expect(page.get_by_text(START, exact=True)).to_be_visible()
        expect(page.get_by_text(NAME, exact=True)).to_be_visible()
        assert ['DescribeAttachment', 200] not in receipt['browser']
        with page.expect_download() as downloaded:
            click(page, 'Download ' + NAME)
        assert downloaded.value.path().read_bytes() == DATA
        assert downloaded.value.suggested_filename == 'Reading _practice_ file.bin'
        assert page.locator('practice').count() == 0
        audit(page, 'native-created-detail-en')
        click(page, 'Back to cases')
        click(page, 'Create case')
        page.get_by_label('Subject', exact=True).fill(CREATED)
        page.get_by_label('Message', exact=True).fill('Opened from console.')
        audit(page, 'create-en')
        click(page, 'Create case')
        expect(page.get_by_text('Case created.', exact=True)).to_be_visible()
        created = next(r['caseId'] for r in call('DescribeCases')['cases'] if r['subject'] == CREATED)
        assert selected(created)['displayId'] != created and len(history(created)) == 1

        # A live case change and download must enforce the participant's native policy.
        policy = {'Version':'2012-10-17','Statement':[{'Effect':'Allow','Resource':'*',
                  'Action':['support:DescribeCases','support:DescribeCommunications']}]}
        wire(team, 'PUT', '/_simulator/v1/iam/principals/support-reader/policy', policy)
        participant = wire(team, 'POST', '/_simulator/v1/access', {'Principal':'support-reader'})['AccessToken']
        limited, reader = open_page(participant)
        click(reader, TOPIC)
        click(reader, 'Download ' + NAME)
        expect(reader.get_by_text('403 AccessDeniedException', exact=False)).to_be_visible()
        reader.get_by_label('Message', exact=True).fill('Denied message')
        click(reader, 'Send message')
        expect(reader.get_by_text('403 AccessDeniedException', exact=False)).to_be_visible()
        assert [r['body'] for r in history(first)] == [START]
        limited.close()

        page.get_by_label('Message', exact=True).fill(MESSAGE)
        page.locator('input[type=file]').set_input_files({'name':NAME, 'mimeType':'application/octet-stream', 'buffer':DATA})
        click(page, 'Send message')
        expect(page.get_by_text('Message added.', exact=True)).to_be_visible()
        stored = history(created)
        assert [r['body'] for r in stored] == [MESSAGE, 'Opened from console.']
        assert stored[0]['submittedBy'] and len(stored[0]['attachmentSet']) == 1
        metadata = stored[0]['attachmentSet'][0]
        assert set(metadata) == {'attachmentId','fileName'} and metadata['fileName'] == NAME
        assert call('DescribeAttachment', attachmentId=metadata['attachmentId'])['attachment']['data'] == DATA

        narrow, japanese = open_page(team.token, 'ja', 390)
        click(japanese, CREATED)
        expect(japanese.get_by_role('heading', name='サポートケースの詳細')).to_be_visible()
        expect(japanese.get_by_text(created, exact=True)).to_be_visible()
        audit(japanese, 'detail-ja')
        narrow.close()

        # The provider commits once. Lose only its acknowledgement, then fetch this exact case.
        lost = []
        def lose_message(route):
            if route.request.headers.get('x-amz-target') != TARGET + 'AddCommunicationToCase':
                route.fallback()
                return
            res = route.fetch(max_redirects=0)
            assert res.status == 200
            res.dispose()
            lost.append(route.request.post_data_json['caseId'])
            route.abort('failed')
        page.route(team.endpoint + '/', lose_message)
        page.get_by_label('Message', exact=True).fill('Unacknowledged message')
        click(page, 'Send message')
        expect(page.get_by_role('button', name='Review this case')).to_be_visible()
        expect(page.get_by_text('Message added.', exact=True)).to_have_count(0)
        page.unroute(team.endpoint + '/', lose_message)
        click(page, 'Review this case')
        expect(page.get_by_text('Unacknowledged message', exact=True)).to_be_visible()
        assert lost == [created] and [r['body'] for r in history(created)].count('Unacknowledged message') == 1
        click(page, 'Resolve case')
        click(page, 'Confirm resolution')
        expect(page.get_by_text('Case resolved.', exact=True)).to_be_visible()
        assert selected(created)['status'] == 'resolved'
        expect(page.get_by_role('button', name='Send message')).to_have_count(0)
        click(page, 'Back to cases')
        expect(page.get_by_role('button', name=CREATED, exact=True)).to_have_count(0)
        page.get_by_role('checkbox', name='Include resolved cases').check()
        click(page, CREATED)
        expect(page.get_by_text('Resolved', exact=True)).to_be_visible()

        # Pause a real read for one case. Moving back and selecting another case aborts its old scope.
        click(page, 'Back to cases')
        held = []
        def hold(route):
            if route.request.post_data_json.get('caseId') == created and route.request.headers.get('x-amz-target') == TARGET+'DescribeCommunications':
                held.append(route)
            else:
                route.fallback()
        page.route(team.endpoint + '/', hold)
        click(page, CREATED)
        deadline = time.monotonic() + 8
        while not held and time.monotonic() < deadline:
            page.wait_for_timeout(20)
        assert len(held) == 1
        click(page, 'Back to cases')
        click(page, TOPIC)
        expect(page.get_by_text(first, exact=True)).to_be_visible()
        held[0].continue_()
        page.unroute(team.endpoint + '/', hold)
        expect(page.get_by_text(created, exact=True)).to_have_count(0)
        # Lose the ack for a real create with a duplicate subject. Its unknown ID cannot be inferred from the list.
        click(page, 'Back to cases')
        click(page, 'Create case')
        page.get_by_label('Subject', exact=True).fill(TOPIC)
        page.get_by_label('Message', exact=True).fill('Distinct body with the same subject')
        missing_ids = []
        def lose_create(route):
            if route.request.headers.get('x-amz-target') != TARGET + 'CreateCase':
                route.fallback()
                return
            response = route.fetch(max_redirects=0)
            assert response.status == 200
            missing_ids.append(response.json()['caseId'])
            response.dispose()
            route.abort('failed')
        page.route(team.endpoint + '/', lose_create)
        click(page, 'Create case')
        expect(page.get_by_role('button', name='Review the case list')).to_be_visible()
        expect(page.get_by_text('Case created.', exact=True)).to_have_count(0)
        page.unroute(team.endpoint + '/', lose_create)
        click(page, 'Review the case list')
        expect(page.get_by_text('The last creation result is unknown.', exact=False)).to_be_visible()
        expect(page.get_by_role('button', name=TOPIC, exact=True)).to_have_count(2)
        assert len(missing_ids) == 1 and missing_ids[0] != first and len(history(missing_ids[0])) == 1
        call('ResolveCase', caseId=missing_ids[0])

        # Cross both real ten-row page boundaries, including identical message content.
        for n in range(10):
            call('CreateCase', subject=f'Page practice {n}', communicationBody='List inventory')
            call('AddCommunicationToCase', caseId=first, communicationBody='Repeated message')
        click(page, 'Refresh cases')
        expect(page.get_by_role('button', name='Load older cases')).to_be_visible()
        expect(page.get_by_role('button', name=TOPIC, exact=True)).to_have_count(0)
        click(page, 'Load older cases')
        first_number = selected(first)['displayId']
        first_row = page.get_by_role('row').filter(has=page.get_by_role('cell', name=first_number, exact=True))
        first_row.get_by_role('button', name=TOPIC, exact=True).click()
        expect(page.get_by_text('Repeated message', exact=True)).to_have_count(10)
        expect(page.get_by_text(START, exact=True)).to_have_count(0)
        click(page, 'Load older messages')
        expect(page.get_by_text(START, exact=True)).to_be_visible()
        assert peer_call('DescribeCases', includeResolvedCases=True)['cases'] == []
        ctx.close()
        team.close()
        team.start()
        assert selected(created)['status'] == 'resolved' and len(history(created)) == 3
        assert call('DescribeAttachment', attachmentId=metadata['attachmentId'])['attachment']['data'] == DATA
        call('ResolveCase', caseId=first)
    assert not root.exists() and not outside and not browser_errors, (outside, browser_errors)
    for op in ('AddAttachmentsToSet', 'CreateCase', 'DescribeCases', 'DescribeCommunications', 'AddCommunicationToCase',
               'ResolveCase', 'DescribeAttachment'):
        assert [op, 200] in receipt['browser'], op
    assert ['DescribeAttachment', 403] in receipt['browser'] and ['AddCommunicationToCase', 403] in receipt['browser']
    assert receipt['sdkSends'] == receipt['sdkResponses'] and (receipt['sdkSends'] > 0) == use_sdk
    receipt.update({'caseReadAfterRestart':True, 'exactDownloadBytes':len(DATA), 'lostMessageWrites':len(lost), 'lostCreateWrites':len(missing_ids), 'pagedCasesAndMessages':True, 'cleanup':True})
    return receipt


if __name__ == '__main__':
    args = argparse.ArgumentParser()
    args.add_argument('--sdk', action='store_true')
    opts = args.parse_args()
    output = exercise(Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve(strict=True),
                      opts.sdk, Path(os.environ['AXE_SCRIPT']) if os.environ.get('AXE_SCRIPT') else None)
    print(json.dumps(output, ensure_ascii=False))
