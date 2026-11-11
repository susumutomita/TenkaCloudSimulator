"""App Engine console against the real binary, the official SDK and the data plane.

The browser signs in with a real local token and uses only same-origin requests
to the actual App Engine Admin API v1 paths. Version code comes from a ZIP in
the native Cloud Storage provider, and the deployed Python WSGI application is
fetched through its local data-plane URL. The official discovery client sees
the same SQLite state. Desktop and 390 px layouts are checked with axe and
keyboard focus; no Google Cloud console screen is claimed.
"""
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import urllib.request
from urllib.parse import urlsplit
import zipfile

from playwright.sync_api import sync_playwright, expect
from reflow import assert_reflow
from aws_catalog import Team
from gcp_catalog_fixtures import client

REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
PROJECT = 'tenkacloud-local'
BUCKET = 'appengine-ui-src'


def archive(text):
    data = io.BytesIO()
    with zipfile.ZipFile(data, 'w') as z:
        z.writestr('main.py', 'import os\n\ndef app(environ, start_response):\n'
                   '    body = ("' + text + ' " + os.environ["GAE_VERSION"] + " " + os.environ.get("GREETING", "")).encode()\n'
                   '    start_response("200 OK", [("Content-Type", "text/plain; charset=utf-8")])\n'
                   '    return [body]\n')
    return data.getvalue()


def upload(team, name, data):
    request = urllib.request.Request(f'{team.endpoint}/_simulator/providers/gcp/upload/storage/v1/b/{BUCKET}/o?uploadType=media&name={name}',
                                     data=data, method='POST', headers={'X-Simulator-Token': team.token, 'Content-Type': 'application/zip'})
    with urllib.request.urlopen(request, timeout=10) as response:
        assert response.status == 200


def run():
    REPORTS.mkdir(parents=True, exist_ok=True)
    axe_path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((axe_path.parent / 'provenance.json').read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance['bundleSha256']
    outside, errors, methods = [], [], set()
    with tempfile.TemporaryDirectory(prefix='tcsim-appengine-ui-') as directory:
        team = Team(directory)
        team.start()
        try:
            capabilities = team.control('GET', '/_simulator/v1/capabilities')['gcpAppEngine']
            assert capabilities['endpoint'] == '/_simulator/providers/gcp/appengine/' and capabilities['project'] == PROJECT
            team.control('POST', f'/_simulator/providers/gcp/storage/v1/b?project={PROJECT}', {'name': BUCKET})
            upload(team, 'v1.zip', archive('served by'))
            upload(team, 'v2.zip', archive('second release'))
            apps = client(team, {'api': 'appengine.v1'}).apps()
            with sync_playwright() as p:
                browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
                context = browser.new_context(viewport={'width': 1440, 'height': 1000})

                def local_only(route):
                    target = urlsplit(route.request.url)
                    if target.netloc != urlsplit(team.endpoint).netloc:
                        outside.append(target.netloc)
                        route.abort()
                    else:
                        route.continue_()

                context.route('**/*', local_only)
                page = context.new_page()
                page.on('pageerror', lambda error: errors.append(str(error)))

                def observe(request):
                    if urlsplit(request.url).path.startswith('/_simulator/providers/gcp/appengine/'):
                        methods.add(request.method)

                page.on('request', observe)

                def audit(label):
                    for width, name in ((1440, 'desktop'), (390, 'mobile')):
                        page.set_viewport_size({'width': width, 'height': 1000})
                        assert_reflow(page)
                        page.evaluate(axe_path.read_text())
                        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                        assert not result['violations'], [(item['id'], [n['target'] for n in item['nodes']]) for item in result['violations']]
                        page.screenshot(path=str(REPORTS / f'gcp-appengine-console-{label}{name}.png'), animations='disabled', full_page=True)
                    page.set_viewport_size({'width': 1440, 'height': 1000})

                def tab_to(name):
                    # Keyboard only: Tab from the page start reaches the control.
                    page.locator('body').focus()
                    for _ in range(80):
                        page.keyboard.press('Tab')
                        if page.evaluate('document.activeElement && (document.activeElement.getAttribute("aria-label") || document.activeElement.textContent.trim())') == name:
                            return
                    raise AssertionError('keyboard focus never reached ' + name)

                def served(path):
                    response = context.request.get(team.endpoint + path)
                    return response.status, response.text()

                page.goto(team.endpoint + '/_simulator/console/')
                page.get_by_label('Access token', exact=True).fill(team.token)
                page.get_by_role('button', name='Open sandbox', exact=True).click()
                page.get_by_role('button', name='Google Cloud', exact=True).click()
                page.get_by_role('button', name='Services', exact=True).click()
                page.get_by_role('menuitem', name='App Engine', exact=True).click()
                expect(page.get_by_role('heading', level=1, name='App Engine')).to_be_visible()
                expect(page.get_by_role('heading', level=2, name='Create an application')).to_be_visible()
                expect(page.get_by_text(re.compile('cannot be deleted'))).to_be_visible()
                audit('create-')
                page.get_by_label('Location', exact=True).select_option('asia-northeast1')
                page.get_by_role('button', name='Create application', exact=True).click()
                expect(page.get_by_role('status')).to_have_text(f'Application {PROJECT} created.')
                expect(page.get_by_text(f'{PROJECT}.an.r.appspot.com')).to_be_visible()
                assert apps.get(appsId=PROJECT).execute(num_retries=0)['locationId'] == 'asia-northeast1'
                # Deploy v1 from the Cloud Storage ZIP through the form.
                page.get_by_label('Version ID', exact=True).fill('v1')
                page.get_by_label('Cloud Storage ZIP URL', exact=True).fill(f'https://storage.googleapis.com/{BUCKET}/v1.zip')
                page.get_by_label(re.compile('^Environment variables')).fill('GREETING=hello from the console')
                page.get_by_role('button', name='Deploy', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Version default/v1 deployed.')
                versions = page.get_by_role('region', name='Versions of default')
                expect(versions.get_by_role('row', name=re.compile('^v1 python312 SERVING 100%'))).to_be_visible()
                expect(page.get_by_role('region', name='Services').get_by_text('v1 100%')).to_be_visible()
                link = page.get_by_role('link', name='Open v1', exact=True)
                href = link.get_attribute('href')
                assert href == f'/_simulator/providers/gcp/appspot/v1-dot-default-dot-{PROJECT}.appspot.com/' and link.get_attribute('target') == '_blank'
                status, text = served(href)
                assert (status, text) == (200, 'served by v1 hello from the console'), (status, text)
                # A failed deployment keeps the server's reason visible.
                page.get_by_label('Version ID', exact=True).fill('v1')
                page.get_by_label('Cloud Storage ZIP URL', exact=True).fill(f'https://storage.googleapis.com/{BUCKET}/v1.zip')
                page.get_by_role('button', name='Deploy', exact=True).click()
                expect(page.get_by_role('alert')).to_contain_text('already exists')
                page.get_by_label('Version ID', exact=True).fill('')
                # Firewall rules are created and deleted through the same API.
                page.get_by_label(re.compile('^Priority')).fill('100')
                page.get_by_label('Action', exact=True).select_option('DENY')
                page.get_by_label(re.compile('^IP range')).fill('192.0.2.0/24')
                page.get_by_label('Description (optional)', exact=True).fill('documentation range')
                page.get_by_role('button', name='Create rule', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Firewall rule 100 created.')
                rules = apps.firewall().ingressRules().list(appsId=PROJECT).execute(num_retries=0)['ingressRules']
                assert [(r['priority'], r['action'], r['sourceRange']) for r in rules] == [(100, 'DENY', '192.0.2.0/24'), (2147483647, 'ALLOW', '*')], rules
                audit('')
                page.get_by_role('button', name='Delete rule 100', exact=True).click()
                page.get_by_role('button', name='Cancel', exact=True).click()
                page.get_by_role('button', name='Delete rule 100', exact=True).click()
                page.get_by_role('button', name='Confirm delete rule 100', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Firewall rule 100 deleted.')
                assert len(apps.firewall().ingressRules().list(appsId=PROJECT).execute(num_retries=0)['ingressRules']) == 1
                # The default rule changes action but cannot be deleted.
                page.get_by_role('button', name='Deny by default', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Firewall rule 2147483647 updated.')
                assert served(href)[0] == 403
                page.get_by_role('button', name='Allow by default', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Firewall rule 2147483647 updated.')
                # An SDK deployment appears after a refresh of the same state.
                apps.services().versions().create(appsId=PROJECT, servicesId='default', body={'id': 'v2', 'runtime': 'python312',
                    'deployment': {'zip': {'sourceUrl': f'https://storage.googleapis.com/{BUCKET}/v2.zip'}}}).execute(num_retries=0)
                tab_to('Refresh')
                page.keyboard.press('Enter')
                expect(versions.get_by_role('row', name=re.compile('^v2 python312 SERVING 0%'))).to_be_visible()
                page.get_by_role('button', name='Send all traffic to v2', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('All traffic now goes to default/v2.')
                split = apps.services().get(appsId=PROJECT, servicesId='default').execute(num_retries=0)['split']
                assert split['allocations'] == {'v2': 1}, split
                assert served(f'/_simulator/providers/gcp/appspot/{PROJECT}.an.r.appspot.com/') == (200, 'second release v2 ')
                # A version with traffic keeps the server refusal visible.
                page.get_by_role('button', name='Delete v2', exact=True).click()
                page.get_by_role('button', name='Confirm delete v2', exact=True).click()
                expect(page.get_by_role('alert')).to_contain_text('receives traffic')
                page.get_by_role('button', name='Stop v1', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Version default/v1 stopped.')
                assert served(href)[0] == 503
                page.get_by_role('button', name='Delete v1', exact=True).click()
                page.get_by_role('button', name='Confirm delete v1', exact=True).click()
                expect(page.get_by_role('status')).to_have_text('Version default/v1 deleted.')
                assert [v['id'] for v in apps.services().versions().list(appsId=PROJECT, servicesId='default').execute(num_retries=0)['versions']] == ['v2']
                # Disabling the application stops every version from serving.
                tab_to('Disable application')
                page.keyboard.press('Enter')
                expect(page.get_by_role('status')).to_have_text(f'Application {PROJECT} is disabled.')
                assert served(f'/_simulator/providers/gcp/appspot/{PROJECT}.appspot.com/')[0] == 503
                page.get_by_role('button', name='Enable application', exact=True).click()
                expect(page.get_by_role('status')).to_have_text(f'Application {PROJECT} is serving.')
                assert served(f'/_simulator/providers/gcp/appspot/{PROJECT}.appspot.com/')[0] == 200
                # Japanese presentation keeps identifiers and native state.
                page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                expect(page.get_by_role('heading', level=2, name='ファイアウォール ルール', exact=True)).to_be_visible()
                expect(page.get_by_role('link', name='開く: v2', exact=True)).to_be_visible()
                audit('ja-')
                page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                # The served reference documents the same reviewed methods.
                docs = context.new_page()
                docs.goto(team.endpoint + capabilities['reference'])
                expect(docs.get_by_text('App Engine Admin API (simulator)').first).to_be_visible(timeout=20000)
                document = docs.evaluate("fetch('/_simulator/docs/gcp-appengine/appengine.v1.json').then(r=>r.json())")
                assert document['servers'][0]['url'] == '/_simulator/providers/gcp/appengine'
                assert len({op['operationId'] for path in document['paths'].values() for op in path.values()}) == len(capabilities['methods'])
                browser.close()
        finally:
            team.stop()
    assert not outside and not errors, (outside, errors)
    assert {'GET', 'POST', 'PATCH', 'DELETE'} <= methods, methods
    print(json.dumps({'gcpAppEngineConsole': True, 'requests': sorted(methods), 'externalRequests': 0, 'axeWidths': [1440, 390]}))


if __name__ == '__main__':
    run()
