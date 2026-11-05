"""Actual embedded Scalar cloud navigation and live native list responses.

No mocked API responses, provider overlay, external assets or real cloud access.
The Sakura section is documentation-only and must never acquire an operation.
"""
import json
import hashlib
import os
from pathlib import Path
import re
import urllib.error
import urllib.parse
import urllib.request

from playwright.sync_api import sync_playwright, expect
from check import running, REPORTS
from reflow import assert_reflow
from api_cloud_sections_contract import validate_document, GROUPS, GUIDES


def run():
    axe = Path(os.environ['AXE_SCRIPT'])
    assert axe.is_file(), 'Strict axe is mandatory'
    expected_spec = json.loads(Path('internal/server/apidocs/openapi.json').read_text())
    expected_theme = Path('internal/server/apidocs/reference-theme.css').read_bytes()
    validate_document(expected_spec)
    with running(authenticated=True) as (endpoint, token), sync_playwright() as p:
        parsed = urllib.parse.urlsplit(endpoint)
        assert parsed.scheme == 'http' and parsed.hostname == '127.0.0.1' and parsed.port
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        counters = {'calls': 0, 'sends': 0, 'responses': 0}

        def native(method, path, body=None, headers=None, authenticated=True):
            target = endpoint + path
            assert target.startswith(endpoint + '/') and urllib.parse.urlsplit(target).netloc == parsed.netloc
            counters['calls'] += 1
            request = urllib.request.Request(target, data=body, method=method,
                headers={**({'X-Simulator-Token': token} if authenticated else {}), **(headers or {})})
            counters['sends'] += 1
            try:
                response = opener.open(request, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                result = response.status, response.read()
            counters['responses'] += 1
            return result

        assert native('GET', '/_simulator/providers/gcp/storage/v1/b?project=tenkacloud-local', authenticated=False)[0] == 401
        assert native('PUT', '/docs-cloud-s3')[0] == 200
        assert native('POST', '/_simulator/providers/gcp/storage/v1/b?project=tenkacloud-local',
            json.dumps({'name': 'docs-cloud-gcp'}).encode(), {'Content-Type': 'application/json'})[0] == 200
        assert native('PUT', '/_simulator/providers/azure/tenkacloudlocal/docs-cloud-azure?restype=container',
            headers={'x-ms-version': '2023-11-03'})[0] == 201
        code, raw = native('GET', '/_simulator/docs/openapi.json')
        assert code == 200 and json.loads(raw) == expected_spec
        validate_document(json.loads(raw))
        code, theme = native('GET', '/_simulator/docs/reference-theme.css')
        assert code == 200 and theme == expected_theme
        code, index = native('GET', '/_simulator/docs/')
        assert code == 200
        assert index.find(b'./scalar.css') < index.find(b'./reference-theme.css')
        assert b'href="./reference-theme.css"' in index
        browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE', '/usr/bin/chromium'),
                                   args=['--no-sandbox'])
        try:
            context = browser.new_context(viewport={'width': 1440, 'height': 1000})
            try:
                assert context.request.post(endpoint + '/_simulator/login', form={'token': token}, max_redirects=0).status == 303
                page = context.new_page()
                errors, outside, axe_states, theme_responses, computed_palettes = [], [], [], [], []
                page.on('pageerror', lambda error: errors.append(str(error)))
                page.on('request', lambda request: outside.append(request.url)
                        if not request.url.startswith(endpoint + '/') and not request.url.startswith('data:') else None)
                page.on('response', lambda response: theme_responses.append(response.status)
                        if response.url == endpoint + '/_simulator/docs/reference-theme.css' else None)
                page.goto(endpoint + '/_simulator/docs/')
                sidebar = page.get_by_role('complementary', name='Sidebar for TenkaCloud Simulator API', exact=True)
                expect(sidebar.get_by_role('link', name='AWS', exact=True)).to_be_visible(timeout=30000)
                assert theme_responses and all(status == 200 for status in theme_responses)
                expect(page.locator('link[rel="stylesheet"][href="./reference-theme.css"]')).to_have_count(1)

                for width, height in [(1440, 1000), (390, 844)]:
                    page.set_viewport_size({'width': width, 'height': height})
                    if width == 390:
                        menu_opener = page.get_by_role('button', name='Open Menu', exact=True)
                        menu_opener.focus()
                        expect(menu_opener).to_be_focused()
                        menu_opener.press('Enter')
                    for cloud in GROUPS:
                        expect(sidebar.get_by_role('link', name=cloud, exact=True)).to_be_visible()
                        group = sidebar.get_by_role('button', name=re.compile(r'^(Open|Close) Group - '+re.escape(cloud)+r'$'))
                        expect(group).to_be_visible()
                        group.focus()
                        expect(group).to_be_focused()
                        if group.get_attribute('aria-expanded') == 'false':
                            group.press('Enter')
                    for guide in GUIDES:
                        expect(sidebar.get_by_role('link', name=guide, exact=True)).to_be_visible()
                    status_link = sidebar.get_by_role('link', name='Sakura Cloud status', exact=True)
                    expect(status_link).to_be_visible()
                    status_link.click()
                    expect(page.get_by_text('Not implemented.', exact=True)).to_be_visible()
                    status_region = page.get_by_role('region', name='Sakura Cloud status', exact=True)
                    expect(status_region).to_be_visible()
                    assert status_region.get_by_role('button', name=re.compile(r'^Test Request')).count() == 0
                    palette = page.evaluate("""() => {
                      const root = getComputedStyle(document.querySelector('#reference'));
                      const variables = ['--scalar-color-3','--scalar-sidebar-search-color','--scalar-color-green','--scalar-color-red','--scalar-color-orange'];
                      return {light: Object.fromEntries(variables.map(name => [name,root.getPropertyValue(name).trim()])),
                        nestedRed: [...document.querySelectorAll('#reference .request-card.dark-mode')].map(node => getComputedStyle(node).getPropertyValue('--scalar-color-red').trim())};
                    }""")
                    assert palette['light'] == {'--scalar-color-3': '#595959', '--scalar-sidebar-search-color': '#595959',
                        '--scalar-color-green': '#006644', '--scalar-color-red': '#b42318', '--scalar-color-orange': '#9c3b00'}
                    assert palette['nestedRed'] and set(palette['nestedRed']) == {'#ff8a80'}
                    computed_palettes.append({'width': width, **palette})
                    assert_reflow(page)
                    page.evaluate(axe.read_text())
                    result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
                    (REPORTS / f'api-cloud-sections-axe-{width}.json').write_text(json.dumps(result, indent=2) + '\n')
                    assert not result['violations'], [(v['id'], len(v['nodes'])) for v in result['violations']]
                    axe_states.append(width)
                    page.screenshot(path=str(REPORTS / f'api-cloud-sections-{width}.png'))
                    if width == 390:
                        menu_close = page.get_by_role('button', name='Close Menu', exact=True)
                        menu_close.focus()
                        expect(menu_close).to_be_focused()
                        menu_close.press('Enter')
                        expect(sidebar).to_be_hidden()
                        expect(status_region).to_be_in_viewport()
                        assert status_region.get_by_role('button', name=re.compile(r'^Test Request')).count() == 0
                        assert_reflow(page)
                        content_axe = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
                        (REPORTS / 'api-cloud-sections-axe-390-content.json').write_text(json.dumps(content_axe, indent=2) + '\n')
                        assert not content_axe['violations'], [(v['id'], len(v['nodes'])) for v in content_axe['violations']]
                        page.screenshot(path=str(REPORTS / 'api-cloud-sections-390-content.png'))

                page.set_viewport_size({'width': 1440, 'height': 1000})
                cases = [
                    ('s3-basic-object-storage', 'List all buckets', 'get /', '/', 'docs-cloud-s3'),
                    ('google-cloud-storage', 'List Cloud Storage buckets',
                     'get /_simulator/providers/gcp/storage/v1/b', '/_simulator/providers/gcp/storage/v1/b', 'docs-cloud-gcp'),
                    ('azure-blob-storage', 'List Blob Storage containers',
                     'get /_simulator/providers/azure/{StorageAccount}', '/_simulator/providers/azure/tenkacloudlocal', 'docs-cloud-azure'),
                ]
                live = []
                for tag, summary, operation, path, resource in cases:
                    page.goto(endpoint + '/_simulator/docs/#tag/' + tag)
                    expect(page.get_by_text(summary, exact=True).first).to_be_visible()
                    page.get_by_role('button', name='Test Request (' + operation + ')', exact=True).click()
                    with page.expect_response(lambda response: response.request.method == 'GET'
                            and urllib.parse.urlsplit(response.url).path == path) as sent:
                        page.get_by_role('button').filter(has_text='Send Request').last.click()
                    assert sent.value.status == 200 and resource in sent.value.text()
                    live.append({'cloudTag': tag, 'status': 200, 'actualSavedResourceReturned': True})
                    page.keyboard.press('Escape')
                assert not errors and not outside
                assert counters['calls'] == counters['sends'] == counters['responses'] and counters['calls'] > 0
                report = {'cloudSections': GROUPS[:4], 'SakuraImplementedOperations': 0,
                          'documentationOnlyAWSServiceGuides': sorted(GUIDES), 'nativeOperationIDs': 61,
                          'nativePaths': 34, 'staticExamplesAreNotSavedState': True,
                          'liveScalarRequests': live, 'strictAxeWidths': axe_states,
                          'mobileMenuAndVisibleContentAxe': True,
                          'loopbackNativeHTTP': counters, 'externalRequests': 0, 'pageErrors': 0,
                          'originalLocalTheme': {'nativeGET': 200, 'browserLoaded': theme_responses,
                              'sha256': hashlib.sha256(expected_theme).hexdigest(), 'computedPalettes': computed_palettes},
                          'SDKCompatibilityClaimedByThisHelper': False}
            except BaseException:
                if 'page' in locals() and not page.is_closed():
                    (REPORTS / 'api-cloud-sections-failure-dom.txt').write_text(page.locator('body').aria_snapshot())
                    page.screenshot(path=str(REPORTS / 'api-cloud-sections-failure.png'))
                raise
            finally:
                context.close()
        finally:
            browser.close()
        assert native('DELETE', '/docs-cloud-s3')[0] == 204
        assert native('DELETE', '/_simulator/providers/gcp/storage/v1/b/docs-cloud-gcp')[0] == 204
        assert native('DELETE', '/_simulator/providers/azure/tenkacloudlocal/docs-cloud-azure?restype=container',
            headers={'x-ms-version': '2023-11-03'})[0] == 202
        assert counters['calls'] == counters['sends'] == counters['responses']
        (REPORTS / 'api-cloud-sections.json').write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    run()
