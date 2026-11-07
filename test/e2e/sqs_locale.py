"""Actual local SQS language/receipt contract, separate from existing English tests.

Two real CLI/SQLite/token authorities and actual boto3/browser HTTP. No mocked
API, skipped checks, cloud endpoint, VM guest, or credential/receipt output.
"""
from console_locale import select_console_locale
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
from urllib.parse import urlsplit

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import expect, sync_playwright
from reflow import assert_reflow
from sqs import Team

REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
_PRIVATE = []  # Memory only, used to redact an unexpected failure before logging.
OPERATIONS = {'CreateQueue', 'ListQueues', 'GetQueueUrl', 'DeleteQueue', 'SendMessage',
              'ReceiveMessage', 'DeleteMessage', 'ChangeMessageVisibility', 'GetQueueAttributes'}


def run_sqs_locale():
    try:
        _run()
    except Exception as failure:
        message = str(failure)
        for secret in _PRIVATE:
            if secret:
                message = message.replace(secret, '[redacted]')
        raise RuntimeError(type(failure).__name__+': '+message) from None
    finally:
        _PRIVATE.clear()


def _run():
    REPORTS.mkdir(parents=True, exist_ok=True)
    axe_path = Path(os.environ['AXE_SCRIPT'])
    axe = axe_path.read_text()
    provenance = json.loads((axe_path.parent/'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    sdk_records, audits, browser_records, errors, outside, console_output = [], [], [], [], [], []
    with tempfile.TemporaryDirectory(prefix='sqs-locale-') as directory:
        a, b = Team(Path(directory)/'a'), Team(Path(directory)/'b')
        a.directory.mkdir(); b.directory.mkdir()

        def client(team, label, token=None):
            counts = {'calls': 0, 'sends': 0, 'responses': 0}
            operations = set()
            result = boto3.client('sqs', endpoint_url=team.endpoint, region_name='us-east-1',
                                  aws_access_key_id='TCSIMLOCALE', aws_secret_access_key='dummy-local-only',
                                  config=Config(retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5))
            result.meta.events.register('before-sign.sqs', lambda request, **_: request.headers.add_header('X-Simulator-Token', team.token if token is None else token))

            def before_call(model, **_):
                counts['calls'] += 1; operations.add(model.name)

            def before_send(request, **_):
                target = urlsplit(request.url)
                assert target.scheme == 'http' and target.netloc == urlsplit(team.endpoint).netloc
                assert target.hostname == '127.0.0.1', 'SDK left owned numeric-loopback origin'
                counts['sends'] += 1

            def after_call(http_response, **_):
                assert not 300 <= http_response.status_code < 400, 'Unexpected SDK redirect'
                counts['responses'] += 1
            result.meta.events.register('before-call.sqs', before_call)
            result.meta.events.register('before-send.sqs', before_send)
            result.meta.events.register('after-call.sqs', after_call)
            team.clients.append(result); sdk_records.append((label, counts, operations))
            return result

        try:
            a.start(); b.start(); _PRIVATE.extend([a.token, b.token])
            own, peer, foreign = client(a, 'own'), client(b, 'peer'), client(b, 'foreign-token', a.token)
            try:
                foreign.list_queues()
            except ClientError as failure:
                assert failure.response['ResponseMetadata']['HTTPStatusCode'] == 401
            else:
                raise AssertionError('Foreign team accepted another operator credential')
            with sync_playwright() as p:
                browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
                contexts = []
                try:
                    for width in (1440, 390):
                        name = 'LocaleQueue_'+str(width)
                        body = json.dumps({'hello': '日本語', 'opaque': 'Queue created.', 'width': width}, ensure_ascii=False, separators=(',', ':'))
                        peer_body = 'peer-only-'+str(width)
                        peer_url = peer.create_queue(QueueName=name)['QueueUrl']
                        peer.send_message(QueueUrl=peer_url, MessageBody=peer_body)
                        context = browser.new_context(viewport={'width': width, 'height': 1000})
                        contexts.append(context)
                        operations, calls, replies = set(), {}, {}
                        current_receipt, received_id = [None], [None]
                        receipt_checks = [0]
                        send_md5 = [None]
                        page = context.new_page()
                        page.on('pageerror', lambda failure: errors.append(True))
                        page.on('console', lambda message: console_output.append(message.text))

                        def guard(route):
                            target = urlsplit(route.request.url)
                            if target.scheme == 'http' and target.netloc == urlsplit(a.endpoint).netloc:
                                route.continue_()
                            else:
                                outside.append(True); route.abort()
                        context.route('**/*', guard)

                        def request_seen(request):
                            target = request.headers.get('x-amz-target', '')
                            if not target.startswith('AmazonSQS.'):
                                return
                            operation = target.split('.', 1)[1]
                            assert operation in OPERATIONS
                            operations.add(operation); calls[operation] = calls.get(operation, 0)+1
                            payload = request.post_data_json
                            if operation == 'CreateQueue':
                                assert payload['QueueName'] == name
                            elif operation == 'SendMessage':
                                assert payload['MessageBody'] == body
                            elif operation in ('ChangeMessageVisibility', 'DeleteMessage'):
                                assert current_receipt[0] is not None and payload['ReceiptHandle'] == current_receipt[0], 'Language changed or replaced the held native receipt'
                                receipt_checks[0] += 1

                        def response_seen(response):
                            target = response.request.headers.get('x-amz-target', '')
                            if not target.startswith('AmazonSQS.'):
                                return
                            operation = target.split('.', 1)[1]
                            assert response.status == 200, 'Native browser SQS operation failed'
                            replies[operation] = replies.get(operation, 0)+1
                        page.on('request', request_seen); page.on('response', response_seen)

                        def no_secrets():
                            document = page.content()
                            for secret in _PRIVATE:
                                assert secret not in document and secret not in page.url, 'Credential or receipt entered the document or URL'
                                assert all(secret not in line for line in console_output), 'Credential or receipt entered browser console output'
                                assert not page.evaluate('(value)=>Object.values(localStorage).some(x=>x.includes(value))||Object.values(sessionStorage).some(x=>x.includes(value))', secret), 'Credential or receipt entered web storage'

                        def audit(label):
                            no_secrets(); assert_reflow(page)
                            page.evaluate(axe)
                            result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                            assert not result['violations'], [(v['id'], len(v['nodes'])) for v in result['violations']]
                            page.screenshot(path=str(REPORTS/f'sqs-locale-{label}-{width}.png'), full_page=True, animations='disabled')
                            audits.append([label, width])

                        page.goto(a.endpoint+'/_simulator/console/')
                        page.get_by_label('Access token', exact=True).fill(a.token)
                        page.get_by_role('button', name='Open sandbox', exact=True).click()
                        expect(page.get_by_role('button', name='Account menu', exact=True)).to_be_visible()
                        cookies = [c for c in context.cookies(a.endpoint) if c['name'].startswith('tcsim_')]
                        assert len(cookies) == 1 and cookies[0]['value'] != a.token
                        _PRIVATE.append(cookies[0]['value'])

                        def locale(value):
                            current = page.locator('html').get_attribute('lang')
                            before = dict(calls)
                            select_console_locale(page, value)
                            expect(page.locator('html')).to_have_attribute('lang', value)
                            page.wait_for_timeout(100)  # Bounded observation of locale-caused replay.
                            assert calls == before, 'Language switch issued another native SQS request'
                            now = [c for c in context.cookies(a.endpoint) if c['name'].startswith('tcsim_')]
                            assert [(c['name'], c['value']) for c in now] == [(c['name'], c['value']) for c in cookies], 'Language changed browser authority'
                            no_secrets()

                        services = page.get_by_role('button', name='Services', exact=True)
                        services.click()
                        search = page.get_by_role('searchbox', name='Search services in AWS', exact=True)
                        search.fill('SQS'); expect(page.get_by_role('menuitem', name='SQS', exact=True)).to_be_visible()
                        search.fill('キュー')
                        expect(page.get_by_role('menuitem')).to_have_count(1)
                        entry = page.get_by_role('menuitem', name='SQS', exact=True)
                        search.press('ArrowDown'); expect(entry).to_be_focused(); entry.press('Enter')
                        expect(page.get_by_role('heading', level=1, name=re.compile('^Queues'))).to_be_visible()
                        expect(services).to_be_focused()
                        page.get_by_role('button', name='Create queue', exact=True).click()
                        page.get_by_role('textbox', name='Name', exact=True).fill(name)
                        page.get_by_role('textbox', name='Visibility timeout (seconds)', exact=True).fill('300')
                        locale('ja')
                        expect(page.get_by_role('textbox', name='名前', exact=True)).to_have_value(name)
                        expect(page.get_by_role('textbox', name='可視性タイムアウト (秒)', exact=True)).to_have_value('300')
                        assert own.list_queues().get('QueueUrls', []) == [], 'Language change submitted an unfinished queue'
                        audit('create-ja')
                        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'AmazonSQS.CreateQueue'):
                            page.get_by_role('button', name='キューを作成', exact=True).click()
                        expect(page.get_by_role('heading', level=1, name=name, exact=True)).to_be_visible()
                        expect(page.get_by_role('button', name='メッセージの送受信', exact=True)).to_be_enabled()
                        url = own.get_queue_url(QueueName=name)['QueueUrl']
                        attributes = own.get_queue_attributes(QueueUrl=url, AttributeNames=['All'])['Attributes']
                        assert attributes['VisibilityTimeout'] == '300'
                        assert attributes['ApproximateNumberOfMessages'] == '0'
                        expect(page.get_by_text('キューを作成しました。', exact=True)).to_be_visible()
                        locale('en'); expect(page.get_by_text('Queue created.', exact=True)).to_be_visible()
                        page.get_by_role('button', name='Send and receive messages', exact=True).click()
                        page.get_by_role('textbox', name='Message body', exact=True).fill(body)
                        locale('ja')
                        expect(page.get_by_role('textbox', name='メッセージ本文', exact=True)).to_have_value(body)
                        locale('en'); expect(page.get_by_role('textbox', name='Message body', exact=True)).to_have_value(body)
                        audit('message-draft-en')
                        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'AmazonSQS.SendMessage') as sent:
                            page.get_by_role('button', name='Send message', exact=True).click()
                        # json() reads the complete response body without a redundant lifecycle waiter.
                        send_md5[0] = sent.value.json()['MD5OfMessageBody']
                        assert send_md5[0] == hashlib.md5(body.encode()).hexdigest()
                        expect(page.get_by_text('Message sent.', exact=True)).to_be_visible()
                        locale('ja'); expect(page.get_by_text('メッセージを送信しました。', exact=True)).to_be_visible()
                        assert send_md5[0] == hashlib.md5(body.encode()).hexdigest()
                        assert own.get_queue_attributes(QueueUrl=url, AttributeNames=['All'])['Attributes']['ApproximateNumberOfMessages'] == '1'
                        page.get_by_role('textbox', name='可視性タイムアウト (秒)', exact=True).fill('300')
                        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'AmazonSQS.ReceiveMessage') as received:
                            page.get_by_role('button', name='メッセージをポーリング', exact=True).click()
                        # Read the two required bodies at explicit completed-operation
                        # seams. Background list responses may legitimately be aborted
                        # by navigation; their status/call accounting remains strict.
                        messages = received.value.json().get('Messages', [])
                        assert len(messages) == 1 and messages[0]['Body'] == body
                        receipt = messages[0]['ReceiptHandle']
                        assert isinstance(receipt, str) and receipt
                        _PRIVATE.append(receipt)
                        current_receipt[0], received_id[0] = receipt, messages[0]['MessageId']
                        assert current_receipt[0] and received_id[0]
                        select = page.get_by_role('radio', name='メッセージを選択: '+received_id[0], exact=True)
                        select.check()
                        expect(page.get_by_role('textbox', name='受信したメッセージ本文', exact=True)).to_have_value(body)
                        expect(page.get_by_text('1 件のメッセージを受信しました。', exact=True)).to_be_visible()
                        audit('received-ja')
                        locale('en')
                        expect(page.get_by_role('textbox', name='Received message body', exact=True)).to_have_value(body)
                        expect(page.get_by_text('1 message(s) received.', exact=True)).to_be_visible()
                        page.get_by_role('button', name='Change visibility', exact=True).click()
                        modal = page.get_by_role('dialog', name='Change message visibility', exact=True)
                        modal.get_by_role('textbox', name='New visibility timeout (seconds)', exact=True).fill('600')
                        audit('visibility-en')
                        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'AmazonSQS.ChangeMessageVisibility'):
                            modal.get_by_role('button', name='Change visibility', exact=True).click()
                        expect(page.get_by_text('Message visibility changed.', exact=True)).to_be_visible()
                        locale('ja'); expect(page.get_by_text('メッセージの可視性を変更しました。', exact=True)).to_be_visible()
                        page.get_by_role('radio', name='メッセージを選択: '+received_id[0], exact=True).check()
                        page.get_by_role('button', name='メッセージを削除', exact=True).click()
                        modal = page.get_by_role('dialog', name='メッセージを削除', exact=True)
                        audit('message-delete-ja')
                        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'AmazonSQS.DeleteMessage'):
                            modal.get_by_role('button', name='削除', exact=True).click()
                        expect(page.get_by_text('メッセージを削除しました。', exact=True)).to_be_visible()
                        assert receipt_checks[0] == 2, 'Visibility and deletion must both use the same held receive handle'
                        assert own.receive_message(QueueUrl=url, MaxNumberOfMessages=1, WaitTimeSeconds=0).get('Messages', []) == []
                        assert own.get_queue_attributes(QueueUrl=url, AttributeNames=['All'])['Attributes']['ApproximateNumberOfMessagesNotVisible'] == '0'
                        locale('en')
                        page.get_by_role('button', name='Queue details', exact=True).click()
                        expect(page.get_by_role('button', name='Delete', exact=True)).to_be_enabled()
                        page.get_by_role('button', name='Delete', exact=True).click()
                        modal = page.get_by_role('dialog', name='Delete queue', exact=True)
                        modal.get_by_role('textbox', name='Type delete to confirm', exact=True).fill('delete')
                        audit('queue-delete-en')
                        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'AmazonSQS.ListQueues') as refreshed:
                            with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'AmazonSQS.DeleteQueue'):
                                modal.get_by_role('button', name='Delete', exact=True).click()
                        assert refreshed.value.status == 200
                        expect(page.get_by_role('heading', level=1, name=re.compile('^Queues'))).to_be_visible()
                        expect(page.get_by_role('button', name='Refresh queues', exact=True)).to_be_enabled()
                        assert own.list_queues().get('QueueUrls', []) == []
                        assert operations == OPERATIONS, 'Browser did not invoke all nine supported native APIs'
                        assert calls == replies, 'Some native browser calls lacked a real successful response'
                        assert all(calls.get(op) == 1 for op in ('CreateQueue', 'SendMessage', 'ReceiveMessage', 'ChangeMessageVisibility', 'DeleteMessage', 'DeleteQueue')), 'A native mutation was replayed'
                        assert peer.get_queue_attributes(QueueUrl=peer_url, AttributeNames=['All'])['Attributes']['ApproximateNumberOfMessages'] == '1'
                        peer_message = peer.receive_message(QueueUrl=peer_url, VisibilityTimeout=600, MaxNumberOfMessages=1, WaitTimeSeconds=0)['Messages'][0]
                        assert peer_message['Body'] == peer_body
                        _PRIVATE.append(peer_message['ReceiptHandle'])
                        peer.change_message_visibility(QueueUrl=peer_url, ReceiptHandle=peer_message['ReceiptHandle'], VisibilityTimeout=600)
                        peer.delete_message(QueueUrl=peer_url, ReceiptHandle=peer_message['ReceiptHandle'])
                        peer.delete_queue(QueueUrl=peer_url)
                        no_secrets()
                        browser_records.append({'width': width, 'operations': sorted(operations), 'calls': dict(calls), 'responses': dict(replies), 'sameReceiptChecks': receipt_checks[0]})
                        context.close(); contexts.remove(context)
                    assert not errors, 'Browser page errors (details omitted to protect credentials)'
                    assert not outside, 'Browser left the owned local CLI'
                    assert peer.list_queues().get('QueueUrls', []) == []
                    for _, counts, operations in sdk_records:
                        assert counts['calls'] > 0 and counts['calls'] == counts['sends'] == counts['responses'], 'SDK call/send/response counts were not positive and balanced'
                    assert set().union(*(ops for _, _, ops in sdk_records)) == OPERATIONS
                    report = {'actualTwoCLIs': True, 'twoSQLiteDBs': True, 'actualSDK': 'boto3 '+boto3.__version__,
                              'viewports': [1440, 390], 'browserNineNativeOperations': browser_records,
                              'sdkCalls': [{'client': label, **counts, 'operations': sorted(operations)} for label, counts, operations in sdk_records],
                              'queueAndMessageDraftAcrossLocale': True, 'canonicalResourceBodyAndMd5': True,
                              'sameNativeReceiptAfterLocale': True, 'noDuplicateWritesOrCookieAuthorityChange': True,
                              'foreignToken401AndPeerSameNamePreserved': True, 'receiptOrTokenDisclosure': False,
                              'axeStates': audits, 'outsideRequests': 0, 'pageErrors': 0,
                              'existingRegionIdentityReceiptClearHelper': 'UNCHANGED, not executed by this case',
                              'actualCloud': False, 'cleanup': 'DELETED'}
                    (REPORTS/'sqs-locale.json').write_text(json.dumps(report, indent=2)+'\n')
                    print(json.dumps(report))
                finally:
                    for context in contexts:
                        context.close()
                    browser.close()
        finally:
            # Stop both owned CLIs even if the first shutdown assertion fails.
            try:
                a.stop()
            finally:
                b.stop()


if __name__ == '__main__':
    run_sqs_locale()
