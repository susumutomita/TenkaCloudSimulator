"""Japanese and English KMS controls retain native operation IDs and exact data.

Uses the real embedded UI, a separate official SDK client and local cryptography.
No request/response substitution, real cloud credential or external endpoint.
"""
from console_locale import select_console_locale
import base64
import json
import os
import shutil
from urllib.parse import urlsplit

from check import Config, boto3, expect, running, sync_playwright


def run():
    with running(authenticated=True) as (endpoint, operator), sync_playwright() as playwright:
        sdk = boto3.client(
            'kms', endpoint_url=endpoint, region_name='us-east-1',
            aws_access_key_id='TCSIMKMSLOCALE', aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5))
        sdk.meta.events.register('before-sign.kms', lambda request, **_:
                                 request.headers.add_header('X-Simulator-Token', operator))

        def sdk_local_only(request, **_):
            target = urlsplit(request.url)
            assert target.scheme == 'http' and target.netloc == urlsplit(endpoint).netloc

        sdk.meta.events.register('before-send.kms', sdk_local_only)
        key_id = sdk.create_key(Description='Dummy locale round-trip key')['KeyMetadata']['KeyId']
        browser = playwright.chromium.launch(
            executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),
            args=['--no-sandbox'])
        errors, outside, operations = [], [], []
        try:
            page = browser.new_page(viewport={'width': 1440, 'height': 1000})
            page.set_default_timeout(10000)
            page.on('pageerror', lambda _: errors.append(True))

            def local_only(route):
                target = urlsplit(route.request.url)
                if target.scheme == 'http' and target.netloc == urlsplit(endpoint).netloc:
                    route.continue_()
                else:
                    outside.append(True)
                    route.abort()

            page.route('**/*', local_only)
            page.on('request', lambda request: operations.append(request.headers['x-amz-target'])
                    if request.headers.get('x-amz-target', '').startswith('TrentService.') else None)
            page.goto(endpoint + '/_simulator/console/')
            page.get_by_label('Access token', exact=True).fill(operator)
            page.get_by_role('button', name='Open sandbox', exact=True).click()
            select_console_locale(page, 'ja')
            expect(page.locator('html')).to_have_attribute('lang', 'ja')
            page.get_by_role('button', name='サービス', exact=True).click()
            page.get_by_role('menuitem', name='Key Management Service', exact=True).click()
            page.get_by_role('link', name=key_id, exact=True).click()
            page.get_by_role('tab', name='ローカル暗号化テスト', exact=True).click()

            plaintext = 'Dummy locale data: 日本語 / Encrypt / Decrypt'
            context = {'purpose': '日本語テスト', 'Encrypt': 'Decrypt'}
            page.get_by_label('暗号化コンテキスト', exact=True).fill(json.dumps(context, ensure_ascii=False))
            page.get_by_label('平文', exact=True).fill(plaintext)

            def native_operation(button, operation):
                with page.expect_response(lambda response:
                        response.request.headers.get('x-amz-target') == 'TrentService.' + operation) as completed:
                    page.get_by_role('button', name=button, exact=True).click()
                assert completed.value.status == 200, 'Localized KMS control failed its native operation'

            native_operation('暗号化', 'Encrypt')
            expect(page.get_by_label('暗号文 blob', exact=True)).not_to_have_value('')
            ciphertext = page.get_by_label('暗号文 blob', exact=True).input_value()
            assert ciphertext, 'Japanese Encrypt did not create ciphertext'
            decrypted = sdk.decrypt(KeyId=key_id, CiphertextBlob=base64.b64decode(ciphertext),
                                    EncryptionContext=context)['Plaintext']
            assert decrypted == plaintext.encode(), 'SDK did not recover exact localized UI input'
            native_operation('復号', 'Decrypt')
            expect(page.get_by_label('操作結果', exact=True)).to_have_value(plaintext)

            # A locale change retains the native identifiers and user-entered
            # bytes, and does not repeat any request or mint new authority.
            before_locale = list(operations)
            select_console_locale(page, 'en')
            expect(page.locator('html')).to_have_attribute('lang', 'en')
            expect(page.get_by_label('Plaintext', exact=True)).to_have_value(plaintext)
            expect(page.get_by_label('Ciphertext blob', exact=True)).to_have_value(ciphertext)
            assert operations == before_locale, 'Changing language submitted a KMS operation'
            native_operation('Decrypt', 'Decrypt')
            expect(page.get_by_label('Operation result', exact=True)).to_have_value(plaintext)
            assert operations[-3:] == ['TrentService.Encrypt', 'TrentService.Decrypt', 'TrentService.Decrypt']
            assert not errors and not outside, 'Unexpected browser error or external request'
        finally:
            browser.close()
            sdk.close()
    print(json.dumps({'japaneseEncryptDecrypt': True, 'canonicalOperationIDs': True,
                      'officialSDKExactBytes': True, 'localeSwitchKeepsInputs': True,
                      'externalRequests': 0, 'pageErrors': 0}))


if __name__ == '__main__':
    run()
