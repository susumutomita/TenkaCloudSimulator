"""Localized SQS, Secrets and Logs views preserve native operations and data.

All writes use the real embedded browser UI; independent official SDK clients
check the same state. Only temporary local processes and dummy values are used.
"""
import json
import os
import re
import shutil
import time
from urllib.parse import urlsplit

from check import Config, REPORTS, boto3, expect, running, sync_playwright
from reflow import assert_reflow


def run():
    with running(authenticated=True) as (endpoint, operator), sync_playwright() as playwright:
        clients = []

        def client(service):
            sdk = boto3.client(
                service, endpoint_url=endpoint, region_name='us-east-1',
                aws_access_key_id='TCSIMSERVICELOCALE', aws_secret_access_key='dummy-local-only',
                config=Config(retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5))
            sdk.meta.events.register('before-sign', lambda request, **_:
                                     request.headers.add_header('X-Simulator-Token', operator))

            def local_only(request, **_):
                destination = urlsplit(request.url)
                assert destination.scheme == 'http' and destination.netloc == urlsplit(endpoint).netloc
                assert destination.path == '/', 'SDK selected an unexpected service endpoint'

            sdk.meta.events.register('before-send', local_only)
            clients.append(sdk)
            return sdk

        sqs, secrets, logs = client('sqs'), client('secretsmanager'), client('logs')
        browser = playwright.chromium.launch(
            executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),
            args=['--no-sandbox'])
        operations, errors, outside = [], [], []
        try:
            page = browser.new_page(viewport={'width': 1440, 'height': 1000})
            page.set_default_timeout(10000)
            page.on('pageerror', lambda _: errors.append(True))
            page.on('request', lambda request: operations.append(request.headers['x-amz-target'])
                    if request.headers.get('x-amz-target') else None)

            def local_browser(route):
                target = urlsplit(route.request.url)
                if target.scheme == 'http' and target.netloc == urlsplit(endpoint).netloc:
                    route.continue_()
                else:
                    outside.append(True)
                    route.abort()

            page.route('**/*', local_browser)
            page.goto(endpoint + '/_simulator/console/')
            page.get_by_label('Access token', exact=True).fill(operator)
            page.get_by_role('button', name='Open sandbox', exact=True).click()

            def locale(selected):
                current = page.locator('html').get_attribute('lang')
                page.get_by_role('combobox', name='言語' if current == 'ja' else 'Language',
                                 exact=True).select_option(selected)
                expect(page.locator('html')).to_have_attribute('lang', selected)

            def service(name):
                page.get_by_role('button', name='サービス', exact=True).click()
                page.get_by_role('menuitem', name=name, exact=True).click()

            def native_click(label, target):
                with page.expect_response(lambda response:
                        response.request.headers.get('x-amz-target') == target) as completed:
                    page.get_by_role('button', name=label, exact=True).click()
                assert completed.value.status == 200, 'Localized control failed its native operation'

            def check_reflow(label):
                for width in (1440, 390):
                    page.set_viewport_size({'width': width, 'height': 1000})
                    assert_reflow(page)
                    page.screenshot(path=str(REPORTS / f'service-locale-{label}-{width}.png'),
                                    full_page=True, animations='disabled')
                page.set_viewport_size({'width': 1440, 'height': 1000})

            def check_discovery_deep_links():
                # Delay only the real discovery request: service views must not
                # mount and issue native calls until that response is accepted.
                for service_id, list_target, heading in (
                        ('sqs', 'AmazonSQS.ListQueues', 'Queues'),
                        ('logs', 'Logs_20140328.DescribeLogGroups', 'Log groups'),
                        ('secretsmanager', 'secretsmanager.ListSecrets', 'Secrets')):
                    probe = page.context.new_page()
                    probe.set_default_timeout(10000)
                    held, native_requests = [], []
                    probe.on('pageerror', lambda _: errors.append(True))
                    probe.on('request', lambda request:
                             native_requests.append(request.headers['x-amz-target'])
                             if request.headers.get('x-amz-target') else None)
                    probe.route('**/*', local_browser)
                    probe.route('**/_simulator/v1/capabilities', lambda route: held.append(route))
                    try:
                        probe.goto(endpoint + '/_simulator/console/#/' + service_id,
                                   wait_until='domcontentloaded')
                        expect(probe.get_by_role('button', name='Services', exact=True)).to_be_disabled()
                        # Advance rendered frames while discovery remains pending,
                        # so an incorrectly mounted view can run its initial effect.
                        probe.evaluate('() => new Promise(resolve => requestAnimationFrame(() => '
                                       'requestAnimationFrame(resolve)))')
                        assert len(held) == 1, 'Deep link did not reach real capability discovery'
                        assert native_requests == [], 'Deep link called a native service before discovery'
                        with probe.expect_response(lambda response:
                                response.request.headers.get('x-amz-target') == list_target) as completed:
                            held.pop().continue_()
                        assert completed.value.status == 200, 'Discovered service failed its native list'
                        expect(probe.get_by_role('heading',
                               name=re.compile(r'^' + re.escape(heading) + r'(?:\s|$)'))).to_be_visible()
                        assert native_requests == [list_target], 'Deep link issued an unexpected native operation'
                    finally:
                        probe.close()

            expect(page.get_by_role('button', name='Services', exact=True)).to_be_enabled()
            check_discovery_deep_links()
            locale('ja')
            service('SQS')
            page.get_by_role('button', name='キューを作成', exact=True).click()
            queue_name = 'locale-standard'
            page.get_by_label('名前', exact=True).fill(queue_name)
            before_switch = list(operations)
            locale('en')
            expect(page.get_by_label('Name', exact=True)).to_have_value(queue_name)
            locale('ja')
            expect(page.get_by_label('名前', exact=True)).to_have_value(queue_name)
            assert operations == before_switch, 'Language switch resubmitted an SQS request'
            native_click('キューを作成', 'AmazonSQS.CreateQueue')
            expect(page.get_by_role('heading', name=queue_name, exact=True)).to_be_visible()
            expect(page.get_by_text('キューを作成しました。', exact=True)).to_be_visible()
            queue_url = sqs.get_queue_url(QueueName=queue_name)['QueueUrl']
            page.get_by_role('button', name='メッセージの送受信', exact=True).click()
            sqs_value = 'Dummy SQS 日本語: SendMessage'
            page.get_by_label('メッセージ本文', exact=True).fill(sqs_value)
            native_click('メッセージを送信', 'AmazonSQS.SendMessage')
            expect(page.get_by_text('メッセージを送信しました。', exact=True)).to_be_visible()
            assert sqs.get_queue_attributes(QueueUrl=queue_url, AttributeNames=['All'])['Attributes']['ApproximateNumberOfMessages'] == '1'
            page.get_by_label('可視性タイムアウト（秒）', exact=True).fill('0')
            native_click('メッセージをポーリング', 'AmazonSQS.ReceiveMessage')
            page.get_by_role('radio', name=re.compile(r'^メッセージを選択 ')).check()
            expect(page.get_by_label('受信したメッセージ本文', exact=True)).to_have_value(sqs_value)
            assert sqs.receive_message(QueueUrl=queue_url, VisibilityTimeout=0)['Messages'][0]['Body'] == sqs_value
            check_reflow('sqs-ja')

            service('Secrets Manager')
            page.get_by_role('button', name='新しいシークレットを保存', exact=True).click()
            page.get_by_role('tab', name='平文', exact=True).click()
            secret_value = 'DUMMY-secret-locale-日本語-CreateSecret'
            page.get_by_label('平文のシークレット値', exact=True).fill(secret_value)
            page.get_by_role('button', name='次へ', exact=True).click()
            secret_name = 'training/locale-secrets'
            page.get_by_label('シークレット名', exact=True).fill(secret_name)
            description = '説明: SecretString は識別子のまま'
            page.get_by_label('説明', exact=True).fill(description)
            page.get_by_role('button', name='次へ', exact=True).click()
            page.get_by_role('button', name='次へ', exact=True).click()
            assert secret_value not in page.locator('body').inner_text(), 'Review exposed a draft value'
            native_click('保存', 'secretsmanager.CreateSecret')
            expect(page.get_by_role('heading', name='シークレットの詳細', exact=True)).to_be_visible()
            saved = secrets.get_secret_value(SecretId=secret_name)
            assert saved['SecretString'] == secret_value
            assert secrets.describe_secret(SecretId=saved['ARN'])['Description'] == description
            expect(page.get_by_text('シークレットをローカルの練習用データベースに保存しました。', exact=True)).to_be_visible()
            before_switch = list(operations)
            locale('en')
            expect(page.get_by_text('Secret stored in the local practice database.', exact=True)).to_be_visible()
            locale('ja')
            expect(page.get_by_text('シークレットをローカルの練習用データベースに保存しました。', exact=True)).to_be_visible()
            assert operations == before_switch, 'Changing a notice language repeated a Secrets operation'
            native_click('シークレット値を取得', 'secretsmanager.GetSecretValue')
            expect(page.get_by_label('取得したシークレット値', exact=True)).to_have_value(secret_value)
            page.get_by_role('button', name='シークレット値を非表示', exact=True).click()
            expect(page.get_by_label('取得したシークレット値', exact=True)).to_have_count(0)
            check_reflow('secrets-hidden-ja')

            service('CloudWatch Logs')
            page.get_by_role('button', name='ロググループを作成', exact=True).click()
            group, stream = 'locale-log-group', 'locale-log-stream'
            page.get_by_label('ロググループ名', exact=True).fill(group)
            native_click('作成', 'Logs_20140328.CreateLogGroup')
            page.get_by_role('link', name=group, exact=True).click()
            page.get_by_role('button', name='ログストリームを作成', exact=True).click()
            page.get_by_label('ログストリーム名', exact=True).fill(stream)
            native_click('作成', 'Logs_20140328.CreateLogStream')
            page.get_by_role('link', name=stream, exact=True).click()
            page.get_by_role('button', name='ログイベントを追加（シミュレーター）', exact=True).click()
            log_value = 'Dummy Logs 日本語: PutLogEvents'
            entries = [{'timestamp': int(time.time() * 1000), 'message': log_value}]
            page.get_by_label('ログイベント JSON', exact=True).fill(json.dumps(entries, ensure_ascii=False))
            native_click('イベントを送信', 'Logs_20140328.PutLogEvents')
            expect(page.get_by_text(log_value, exact=True)).to_be_visible()
            stored = logs.get_log_events(logGroupName=group, logStreamName=stream, startFromHead=True)['events']
            assert len(stored) == 1 and stored[0]['message'] == log_value
            assert stored[0]['timestamp'] == entries[0]['timestamp']
            check_reflow('logs-ja')

            required = {'AmazonSQS.CreateQueue', 'AmazonSQS.SendMessage', 'AmazonSQS.ReceiveMessage',
                        'secretsmanager.CreateSecret', 'secretsmanager.GetSecretValue',
                        'Logs_20140328.CreateLogGroup', 'Logs_20140328.CreateLogStream',
                        'Logs_20140328.PutLogEvents', 'Logs_20140328.GetLogEvents'}
            assert required.issubset(operations), 'Localized flow omitted a required native operation'
            assert not outside and not errors, 'Unexpected browser error or external request'
            sqs.delete_queue(QueueUrl=queue_url)
            secrets.delete_secret(SecretId=saved['ARN'], ForceDeleteWithoutRecovery=True)
            logs.delete_log_group(logGroupName=group)
            assert sqs.list_queues().get('QueueUrls', []) == []
            assert secrets.list_secrets()['SecretList'] == []
            assert logs.describe_log_groups()['logGroups'] == []
        finally:
            browser.close()
            for sdk in clients:
                sdk.close()
    print(json.dumps({'japaneseSQSSecretsLogs': True, 'canonicalNativeOperations': True,
                      'SDKExactUserData': True, 'draftAndNoticeLocaleSwitch': True,
                      'deepLinksWaitForRealCapabilities': True,
                      'desktopAnd390pxReflow': True, 'cleanup': 'DELETED',
                      'externalRequests': 0, 'pageErrors': 0}))


if __name__ == '__main__':
    run()
