"""Exercise the actual Systems Manager Documents comparison against local saved versions."""
import json
import os
import shutil
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from check import ClientError, running
from ssm_documents import client


def run():
    name = 'comparison-fixture'
    markers = ('first saved definition', 'second saved definition')

    def definition(description):
        return json.dumps({
            'schemaVersion': '2.2', 'description': description,
            'mainSteps': [{'action': 'aws:runShellScript', 'name': 'fixture',
                           'inputs': {'runCommand': ['echo stored-only']}}],
        })

    with running(authenticated=True) as (endpoint, token), sync_playwright() as p:
        sdk = client(endpoint, token)
        sdk.create_document(Name=name, Content=definition(markers[0]))
        created = True
        try:
            sdk.update_document(Name=name, DocumentVersion='1', Content=definition(markers[1]))
            executable = os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
            with p.chromium.launch(executable_path=executable, chromium_sandbox=True) as browser:
                context = browser.new_context(locale='en-US', viewport={'width': 1440, 'height': 1000},
                                              service_workers='block')
                assert context.request.post(endpoint + '/_simulator/login', form={'token': token},
                                            max_redirects=0).status == 303
                page = context.new_page()
                actions, outside, errors = [], [], []
                origin = urlsplit(endpoint)

                def guard(route):
                    url = urlsplit(route.request.url)
                    if (url.scheme, url.netloc) == (origin.scheme, origin.netloc):
                        route.continue_()
                    else:
                        outside.append(route.request.url)
                        route.abort()

                def remember(request):
                    target = request.headers.get('x-amz-target')
                    if target in ('AmazonSSM.ListDocuments', 'AmazonSSM.ListDocumentVersions', 'AmazonSSM.GetDocument'):
                        assert not request.headers.get('x-simulator-token')
                        actions.append((target, request.post_data_json))

                def version_read(response):
                    return response.request.headers.get('x-amz-target') == 'AmazonSSM.ListDocumentVersions'

                context.route('**/*', guard)
                page.on('request', remember)
                page.on('pageerror', lambda error: errors.append(str(error)))
                page.goto(endpoint + '/_simulator/console/#/parameters')
                page.get_by_role('link', name='Documents', exact=True).click()
                page.get_by_role('button', name=name, exact=True).click()
                expect(page.get_by_role('tab', name='Content', exact=True)).to_be_visible()
                with page.expect_response(version_read) as response:
                    page.get_by_role('button', name='Compare versions', exact=True).click()
                assert response.value.status == 200
                versions = response.value.json()['DocumentVersions']
                assert [(item['DocumentVersion'], item['IsDefaultVersion']) for item in versions] == [('1', True), ('2', False)]
                expect(page.get_by_role('button', name='Compare selected versions', exact=True)).to_be_enabled()
                assert ('AmazonSSM.ListDocumentVersions', {'Name': name, 'MaxResults': 50}) in actions
                assert not any(target == 'AmazonSSM.GetDocument' for target, _ in actions)
                page.get_by_role('button', name='Compare selected versions', exact=True).click()
                for marker in markers:
                    expect(page.locator('pre.ssm-definition-content').filter(has_text=marker)).to_be_visible()
                assert sorted(body['DocumentVersion'] for target, body in actions
                              if target == 'AmazonSSM.GetDocument') == ['1', '2']
                sdk.delete_document(Name=name)
                created = False
                with page.expect_response(version_read) as response:
                    page.get_by_role('button', name='Compare versions', exact=True).click()
                assert response.value.status == 400 and response.value.json()['__type'] == 'InvalidDocument'
                expect(page.locator('pre.ssm-definition-content')).to_have_count(0)
                page.get_by_role('button', name='Back to documents', exact=True).click()
                expect(page.get_by_text('No owned documents in this Region.', exact=True)).to_be_visible()
                assert not outside and not errors, (outside, errors)
                context.close()
            try:
                sdk.list_document_versions(Name=name)
            except ClientError as error:
                assert error.response['Error']['Code'] == 'InvalidDocument'
            else:
                raise AssertionError('Deleted document still has a visible version history')
        finally:
            if created:
                sdk.delete_document(Name=name)


if __name__ == '__main__':
    run()
