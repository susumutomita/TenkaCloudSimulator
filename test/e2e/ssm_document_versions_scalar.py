"""Send the served ListDocumentVersions preset from Scalar to the real local binary."""
import json
import os
import re
import shutil
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from check import running
from ssm_documents import CONTENT, client

JSON11 = 'application/x-amz-json-1.1'
TARGET = 'AmazonSSM.ListDocumentVersions'


def run():
    with running(authenticated=True) as (endpoint, token), sync_playwright() as p:
        sdk = client(endpoint, token)
        sdk.create_document(Name='local-definition', Content=CONTENT)
        created = True
        try:
            new_definition = json.loads(CONTENT)
            new_definition['description'] = 'second fixture definition, never executed'
            sdk.update_document(Name='local-definition', DocumentVersion='1', Content=json.dumps(new_definition))
            executable = os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
            with p.chromium.launch(executable_path=executable, chromium_sandbox=True) as browser:
                context = browser.new_context(locale='en-US', viewport={'width': 1440, 'height': 1000},
                                              service_workers='block')
                assert context.request.post(endpoint + '/_simulator/login', form={'token': token},
                                            max_redirects=0).status == 303
                page = context.new_page()
                sends, outside, errors = [], [], []
                origin = urlsplit(endpoint)

                def guard(route):
                    url = urlsplit(route.request.url)
                    if (url.scheme, url.netloc) == (origin.scheme, origin.netloc):
                        route.continue_()
                    else:
                        outside.append(route.request.url)
                        route.abort()

                def root_post(request):
                    return request.method == 'POST' and request.url == endpoint + '/'

                context.route('**/*', guard)
                page.on('request', lambda request: sends.append(request) if root_post(request) else None)
                page.on('pageerror', lambda error: errors.append(str(error)))
                with page.expect_response(lambda response: response.url == endpoint + '/_simulator/docs/openapi.json') as received:
                    page.goto(endpoint + '/_simulator/docs/')
                operation = received.value.json()['paths']['/']['post']
                preset = operation['requestBody']['content'][JSON11]['examples']['SSMListDocumentVersions']
                assert preset['value'] == {'Name': 'local-definition', 'MaxResults': 1}
                page.get_by_role('button', name='Test Request (post /)', exact=True).click()
                dialog = page.get_by_role('dialog').filter(
                    has=page.get_by_role('button', name='Close Client', exact=True))
                dialog.get_by_role('button', name='Body', exact=True).click()
                media = dialog.get_by_role('button', name=re.compile(
                    r'^(application/x-amz-json-1\.[01]|application/x-www-form-urlencoded|Form URL Encoded)$'))
                expect(media).to_have_count(1)
                if media.inner_text().strip() != JSON11:
                    media.click()
                    page.get_by_role('option', name=JSON11, exact=True).click()
                dialog.get_by_test_id('example-picker').click()
                page.get_by_role('option', name=preset['summary'], exact=True).click()
                dialog.get_by_role('button', name='Headers', exact=True).click()
                target = dialog.get_by_label('X-Amz-Target Value', exact=True)
                target.fill(TARGET)
                target.press('Tab')
                include = dialog.get_by_role('checkbox', name='Include X-Amz-Target in request', exact=True)
                if not include.is_checked():
                    include.check()
                for name in ('X-Simulator-Service', 'X-Simulator-Region'):
                    optional = dialog.get_by_role('checkbox', name=f'Include {name} in request', exact=True)
                    if optional.is_checked():
                        optional.uncheck()
                assert not sends

                def send(number):
                    with page.expect_response(lambda response: root_post(response.request)) as sent:
                        dialog.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
                    response = sent.value
                    assert len(sends) == number and response.request is sends[-1]
                    headers = response.request.all_headers()
                    assert headers['x-amz-target'] == TARGET and headers['cookie']
                    assert headers['content-type'].split(';', 1)[0] == JSON11
                    assert not {'authorization', 'x-simulator-token', 'x-simulator-principal'}.intersection(headers)
                    assert response.request.post_data_json == preset['value']
                    assert response.request.redirected_from is None and response.request.redirected_to is None
                    return response

                result = send(1)
                assert result.status == 200 and result.headers['content-type'] == JSON11
                data = result.json()
                (row,) = data['DocumentVersions']
                assert row['DocumentVersion'] == '1' and row['IsDefaultVersion'] is True
                assert row['Name'] == preset['value']['Name'] and 'Content' not in row
                assert data['NextToken'] and row['CreatedDate'] > 0
                body = dialog.locator('.response-section-content-body').filter(
                    has=page.get_by_role('link', name='Download Response Body', exact=True))
                expect(body).to_have_count(1)
                expect(body).to_contain_text('NextToken')
                expect(body).to_contain_text(preset['value']['Name'])
                sdk.delete_document(Name='local-definition')
                created = False
                missing = send(2)
                assert missing.status == 400 and missing.json()['__type'] == 'InvalidDocument'
                expect(body).to_contain_text('InvalidDocument')
                assert not outside and not errors, (outside, errors)
                context.close()
        finally:
            if created:
                sdk.delete_document(Name='local-definition')


if __name__ == '__main__':
    run()
