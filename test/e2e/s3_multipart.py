"""Multipart native SDK, two owned processes, restart, IAM and embedded UI.

All requests target owned loopback instances; no real AWS credentials or calls.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile
from urllib.parse import urlsplit
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import sync_playwright, expect
from sqs import Team, NoRedirect
from reflow import assert_reflow


MULTIPART = {'CreateMultipartUpload', 'UploadPart', 'ListParts',
             'ListMultipartUploads', 'CompleteMultipartUpload', 'AbortMultipartUpload'}


def run():
    reports = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
    reports.mkdir(parents=True, exist_ok=True)
    axe_path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe_path.parent / 'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    counts = {'calls': 0, 'responses': 0}
    succeeded, outside, errors, mutations = set(), [], [], []
    with tempfile.TemporaryDirectory(prefix='multipart-sdk-') as directory:
        a, b = Team(Path(directory) / 'a'), Team(Path(directory) / 'b')
        a.directory.mkdir(); b.directory.mkdir()

        def client(team, region='us-east-1', token=None):
            c = boto3.client('s3', endpoint_url=team.endpoint, region_name=region,
                aws_access_key_id='TCSIMMULTIPART', aws_secret_access_key='dummy',
                config=Config(s3={'addressing_style': 'path'}, retries={'max_attempts': 0},
                    request_checksum_calculation='when_required', response_checksum_validation='when_required'))

            def signing(request, **_):
                target, owned = urlsplit(request.url), urlsplit(team.endpoint)
                assert target.scheme == owned.scheme and target.netloc == owned.netloc
                request.headers.add_header('X-Simulator-Token', team.token if token is None else token)
                counts['calls'] += 1

            def response(http_response, model, **_):
                counts['responses'] += 1
                assert not 300 <= http_response.status_code < 400
                if 200 <= http_response.status_code < 300:
                    succeeded.add(model.name)

            c.meta.events.register('before-sign.s3', signing)
            c.meta.events.register('after-call.s3', response)
            team.clients.append(c)
            return c

        def fail(code, operation, **args):
            try:
                operation(**args)
            except ClientError as error:
                assert error.response['Error']['Code'] == code, error.response['Error']['Code']
            else:
                raise AssertionError('Expected native failure: ' + code)

        try:
            a.start(); b.start()
            sdk, peer = client(a), client(b)
            bucket, key = 'multipart-bucket', 'dir/data & literal.bin'
            sdk.create_bucket(Bucket=bucket); peer.create_bucket(Bucket=bucket)
            peer.put_object(Bucket=bucket, Key=key, Body=b'peer-preserved')
            sdk.put_bucket_versioning(Bucket=bucket, VersioningConfiguration={'Status': 'Enabled'})
            upload = sdk.create_multipart_upload(Bucket=bucket, Key=key, ContentType='text/plain')['UploadId']
            first, last = b'a' * (5 << 20), b'actual-last-part'
            p = sdk.upload_part(Bucket=bucket, Key=key, UploadId=upload, PartNumber=1, Body=first)['ETag']
            fail('NoSuchUpload', peer.list_parts, Bucket=bucket, Key=key, UploadId=upload)
            fail('401', client(b, token=a.token).list_multipart_uploads, Bucket=bucket)
            # Botocore retries S3 region errors after reading x-amz-bucket-region,
            # even with retries=0. Inspect the native rejection without redirects.
            request = urllib.request.Request(a.endpoint + '/' + bucket + '?uploads', headers={
                'X-Simulator-Token': a.token, 'X-Simulator-Region': 'us-west-2'})
            try:
                with urllib.request.build_opener(NoRedirect()).open(request, timeout=2):
                    pass
            except urllib.error.HTTPError as failure:
                with failure:
                    assert failure.code == 400
                    assert failure.headers['x-amz-bucket-region'] == 'us-east-1'
                    error_xml = ET.fromstring(failure.read())
                    assert error_xml.findtext('Code') == 'IllegalLocationConstraintException'
            else:
                raise AssertionError('Wrong-region native multipart request succeeded')
            assert sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload)['Parts'][0]['ETag'] == p
            saved = a.token
            a.stop(); a.start()
            assert a.token == saved
            sdk = client(a)
            assert sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload)['Parts'][0]['ETag'] == p
            q = sdk.upload_part(Bucket=bucket, Key=key, UploadId=upload, PartNumber=3, Body=last)['ETag']
            page = sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload, MaxParts=1)
            assert page['IsTruncated'] and page['NextPartNumberMarker'] == 1
            assert sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload, PartNumberMarker=1)['Parts'][0]['PartNumber'] == 3
            fail('InvalidPartOrder', sdk.complete_multipart_upload, Bucket=bucket, Key=key, UploadId=upload,
                 MultipartUpload={'Parts': [{'PartNumber': 3, 'ETag': q}, {'PartNumber': 1, 'ETag': p}]})
            assert len(sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload)['Parts']) == 2
            policy = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': 's3:GetObject', 'Resource': '*'}]}
            request = urllib.request.Request(a.endpoint + '/_simulator/v1/iam/principals/multipart-reader/policy',
                data=json.dumps(policy).encode(), headers={'X-Simulator-Token': a.token, 'Content-Type': 'application/json'}, method='PUT')
            with urllib.request.build_opener(NoRedirect()).open(request) as response:
                assert response.status == 200
            request = urllib.request.Request(a.endpoint + '/_simulator/v1/access',
                data=json.dumps({'Principal': 'multipart-reader'}).encode(),
                headers={'X-Simulator-Token': a.token, 'Content-Type': 'application/json'}, method='POST')
            with urllib.request.build_opener(NoRedirect()).open(request) as response:
                limited = json.load(response)['AccessToken']
            fail('AccessDenied', client(a, token=limited).abort_multipart_upload, Bucket=bucket, Key=key, UploadId=upload)
            assert len(sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload)['Parts']) == 2
            result = sdk.complete_multipart_upload(Bucket=bucket, Key=key, UploadId=upload,
                MultipartUpload={'Parts': [{'PartNumber': 1, 'ETag': p}, {'PartNumber': 3, 'ETag': q}]})
            expected = '"' + hashlib.md5(hashlib.md5(first).digest() + hashlib.md5(last).digest()).hexdigest() + '-2"'
            assert result['ETag'] == expected and result['VersionId']
            obj = sdk.get_object(Bucket=bucket, Key=key)
            assert obj['Body'].read() == first + last and obj['ContentType'] == 'text/plain'
            assert peer.get_object(Bucket=bucket, Key=key)['Body'].read() == b'peer-preserved'
            resumed = sdk.create_multipart_upload(Bucket=bucket, Key='sdk-resume')['UploadId']
            sdk.upload_part(Bucket=bucket, Key='sdk-resume', UploadId=resumed, PartNumber=4, Body=b'resume-from-sdk')
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(
                    executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
                context = browser.new_context(viewport={'width': 1440, 'height': 1000})
                page = context.new_page()
                page.on('pageerror', lambda error: errors.append(str(error)))

                def local(route):
                    target = urlsplit(route.request.url)
                    if target.netloc != urlsplit(a.endpoint).netloc:
                        outside.append(True); route.abort()
                    else:
                        if route.request.method in ('POST', 'PUT', 'DELETE') and ('uploads' in target.query or 'uploadId=' in target.query):
                            mutations.append(route.request.method)
                        route.continue_()

                context.route('**/*', local)
                page.goto(a.endpoint + '/_simulator/console/')
                page.get_by_label('Access token', exact=True).fill(a.token)
                page.get_by_role('button', name='Open sandbox', exact=True).click()
                page.get_by_role('link', name=bucket, exact=True).click()
                page.get_by_role('tab', name='Management', exact=True).click()
                # Cloudscape places the group label on the selection-column header;
                # its single-selection radio uses only itemSelectionLabel.
                # Confirm selection after the real ListParts response arrives.
                resumed_row = page.get_by_role('radio', name='Select upload sdk-resume', exact=True)
                resumed_row.click()
                expect(resumed_row).to_be_checked()
                expect(page.get_by_text('15 B', exact=True)).to_be_visible()
                page.get_by_label('Part file', exact=True).set_input_files(
                    {'name': 'previous-upload.txt', 'mimeType': 'text/plain', 'buffer': b'previous-upload-draft'})
                page.get_by_label('Part number', exact=True).fill('8')
                page.get_by_label('Confirmation word', exact=True).fill('previous-confirmation')
                page.get_by_label('Object key', exact=True).fill('ui-object')
                before_locale = len(mutations)
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_label('オブジェクトキー', exact=True)).to_have_value('ui-object')
                expect(page.get_by_label('part番号', exact=True)).to_have_value('8')
                expect(page.get_by_label('確認の文字列', exact=True)).to_have_value('previous-confirmation')
                assert page.get_by_label('partファイル', exact=True).evaluate('(input) => input.files[0].name') == 'previous-upload.txt'
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                assert len(mutations) == before_locale
                with page.expect_response(lambda response: response.request.method == 'POST'
                        and urlsplit(response.url).query == 'uploads') as creation:
                    page.get_by_role('button', name='Start upload', exact=True).click()
                assert creation.value.status == 200, creation.value.text()
                # Routing disables Chromium's cache. The console must explicitly
                # omit unsupported object cache metadata, not rely on the browser
                # avoiding an automatically synthesized no-cache header.
                assert creation.value.request.header_value('cache-control') in (None, '')
                expect(page.get_by_text('Multipart upload started.', exact=True)).to_be_visible()
                expect(page.get_by_label('Part number', exact=True)).to_have_value('1')
                expect(page.get_by_label('Confirmation word', exact=True)).to_have_value('')
                assert page.get_by_label('Part file', exact=True).evaluate('(input) => input.files.length') == 0
                page.get_by_role('button', name='Save part', exact=True).click()
                expect(page.get_by_text('Choose a part file.', exact=True)).to_be_visible()
                active = sdk.list_multipart_uploads(Bucket=bucket)['Uploads']
                ui_upload = next(item['UploadId'] for item in active if item['Key'] == 'ui-object')
                assert not sdk.list_parts(Bucket=bucket, Key='ui-object', UploadId=ui_upload).get('Parts')
                page.get_by_label('Part number', exact=True).fill('3')
                page.get_by_label('Part file', exact=True).set_input_files(
                    {'name': 'part.txt', 'mimeType': 'text/plain', 'buffer': b'ui-and-sdk-same-state'})
                page.get_by_role('button', name='Save part', exact=True).click()
                expect(page.get_by_text('Part saved.', exact=True)).to_be_visible()
                actual = sdk.list_parts(Bucket=bucket, Key='ui-object', UploadId=ui_upload)['Parts']
                assert len(actual) == 1 and actual[0]['PartNumber'] == 3 and actual[0]['Size'] == 21
                receipt = page.locator('div.resource-identifier').filter(has_text=ui_upload)
                expect(receipt).to_contain_text(actual[0]['ETag'])
                page.get_by_label('Object key', exact=True).fill('dirty-next & literal')
                page.get_by_label('Part number', exact=True).fill('42')
                page.get_by_label('Confirmation word', exact=True).fill('dirty-confirmation')
                page.get_by_label('Part file', exact=True).set_input_files(
                    {'name': 'dirty-unsaved.txt', 'mimeType': 'text/plain', 'buffer': b'unsaved'})
                before_locale = len(mutations)
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_text('partを保存しました。', exact=True)).to_be_visible()
                expect(page.get_by_label('オブジェクトキー', exact=True)).to_have_value('dirty-next & literal')
                expect(page.get_by_label('part番号', exact=True)).to_have_value('42')
                expect(page.get_by_label('確認の文字列', exact=True)).to_have_value('dirty-confirmation')
                assert page.get_by_label('partファイル', exact=True).evaluate('(input) => input.files[0].name') == 'dirty-unsaved.txt'
                expect(receipt).to_contain_text(actual[0]['ETag'])
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                expect(page.get_by_text('Part saved.', exact=True)).to_be_visible()
                assert len(mutations) == before_locale
                for width in [1440, 390]:
                    page.set_viewport_size({'width': width, 'height': 1000})
                    assert_reflow(page)
                    page.evaluate(axe_path.read_text())
                    audit = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                    assert not audit['violations'], [violation['id'] for violation in audit['violations']]
                    page.screenshot(path=str(reports / f's3-multipart-{width}.png'), animations='disabled')
                # An SDK replacement invalidates the displayed completion preview.
                sdk.upload_part(Bucket=bucket, Key='ui-object', UploadId=ui_upload, PartNumber=3, Body=b'updated-by-sdk')
                before_complete = len(mutations)
                page.get_by_label('Confirmation word', exact=True).fill('complete')
                page.get_by_role('button', name='Complete upload', exact=True).click()
                expect(page.get_by_text('Parts changed. Review the refreshed list and type complete again.', exact=True)).to_be_visible()
                expect(page.get_by_label('Confirmation word', exact=True)).to_have_value('')
                assert len(mutations) == before_complete
                assert sdk.list_parts(Bucket=bucket, Key='ui-object', UploadId=ui_upload)['Parts'][0]['Size'] == 14
                page.get_by_label('Confirmation word', exact=True).fill('complete')
                page.get_by_role('button', name='Complete upload', exact=True).click()
                expect(page.get_by_text('Object completed.', exact=True)).to_be_visible()
                completed = sdk.get_object(Bucket=bucket, Key='ui-object')
                assert completed['Body'].read() == b'updated-by-sdk'
                expect(receipt).to_contain_text(completed['ETag'])
                expect(receipt).to_contain_text(completed['VersionId'])
                before_locale = len(mutations)
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_text('オブジェクトを結合しました。', exact=True)).to_be_visible()
                expect(receipt).to_contain_text(completed['ETag'])
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                assert len(mutations) == before_locale
                page.get_by_label('Object key', exact=True).fill('aborted')
                page.get_by_role('button', name='Start upload', exact=True).click()
                expect(page.get_by_text('Multipart upload started.', exact=True)).to_be_visible()
                page.get_by_label('Part file', exact=True).set_input_files(
                    {'name': 'discarded.txt', 'mimeType': 'text/plain', 'buffer': b'discarded-part'})
                page.get_by_role('button', name='Save part', exact=True).click()
                expect(page.get_by_text('Part saved.', exact=True)).to_be_visible()
                aborted = next(item['UploadId'] for item in sdk.list_multipart_uploads(Bucket=bucket)['Uploads'] if item['Key'] == 'aborted')
                assert sdk.list_parts(Bucket=bucket, Key='aborted', UploadId=aborted)['Parts'][0]['Size'] == 14
                page.get_by_label('Confirmation word', exact=True).fill('abort')
                page.get_by_role('button', name='Abort upload', exact=True).click()
                expect(page.get_by_text('Upload aborted and parts removed.', exact=True)).to_be_visible()
                fail('NoSuchUpload', sdk.list_parts, Bucket=bucket, Key='aborted', UploadId=aborted)
                assert not errors and not outside
                browser.close()
            sdk.abort_multipart_upload(Bucket=bucket, Key='sdk-resume', UploadId=resumed)
            fail('NoSuchUpload', sdk.list_parts, Bucket=bucket, Key='sdk-resume', UploadId=resumed)
            assert not sdk.list_multipart_uploads(Bucket=bucket).get('Uploads')
            for item in sdk.list_object_versions(Bucket=bucket).get('Versions', []):
                sdk.delete_object(Bucket=bucket, Key=item['Key'], VersionId=item['VersionId'])
            sdk.delete_bucket(Bucket=bucket)
            peer.delete_object(Bucket=bucket, Key=key); peer.delete_bucket(Bucket=bucket)
            assert MULTIPART <= succeeded
            assert counts['calls'] == counts['responses'] and counts['calls'] > 20
            print(json.dumps({'multipartSDK': True, 'sixNativeAPIs': sorted(MULTIPART), 'twoProcesses': True,
                'persistentPartRestart': True, 'IAMAndRegionDenials': True, 'multipartETagAndVersion': True,
                'SDKUISharedState': True, 'PC390Axe': True, 'localeDraftAndReceipt': True,
                'fileDraftIsolation': True, 'concurrentPartReview': True, 'cleanup': 'DELETED',
                'calls': counts, 'realAWS': False}))
        finally:
            try:
                a.stop()
            finally:
                b.stop()


if __name__ == '__main__':
    run()
