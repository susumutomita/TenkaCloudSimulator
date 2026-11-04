"""Actual two-CLI Cloud Storage / Blob Storage console acceptance.

Requires the integrated embedded binary, Chromium and canonical AXE_SCRIPT.
Uses native HTTP as an independent UI state oracle, not an official SDK claim.
No guest execution, cloud access, credentials or new dependencies.
"""
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from playwright.sync_api import sync_playwright, expect
from check import running, REPORTS
from reflow import assert_reflow

GCP = '/_simulator/providers/gcp/storage/v1'
GCP_UPLOAD = '/_simulator/providers/gcp/upload/storage/v1'
AZURE = '/_simulator/providers/azure/tenkacloudlocal'
VERSION = '2023-11-03'


def native(endpoint, token, method, path, body=None, headers=None, expected=200):
    values = {'X-Simulator-Token': token, **(headers or {})}
    if body is not None and isinstance(body, dict):
        body = json.dumps(body).encode(); values['Content-Type'] = 'application/json'
    request = urllib.request.Request(endpoint + path, data=body, method=method, headers=values)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            status, data, received = response.status, response.read(), response.headers
    except urllib.error.HTTPError as response:
        status, data, received = response.code, response.read(), response.headers
    assert status == expected, f'Native {method} expected {expected}, got {status}'
    return data, received


def run_crossprovider_console():
    axe = Path(os.environ['AXE_SCRIPT']).read_text()  # Mandatory, missing tool fails.
    audits, errors, outside = [], [], []
    with tempfile.TemporaryDirectory(prefix='crossprovider-browser-') as directory:
        directory = Path(directory)
        with running(db=directory/'team-a.sqlite', authenticated=True) as (a, operator), running(db=directory/'team-b.sqlite', authenticated=True) as (b, other), sync_playwright() as p:
            browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
            contexts = []
            try:
                def page_for(endpoint, width=1440):
                    context = browser.new_context(viewport={'width': width, 'height': 1000}, accept_downloads=True)
                    contexts.append(context)
                    def guard(route):
                        url = route.request.url
                        if url.startswith(endpoint + '/') or url.startswith(('blob:', 'data:')):
                            route.continue_()
                        else:
                            outside.append(True); route.abort()
                    context.route('**/*', guard)
                    page = context.new_page(); page.on('pageerror', lambda error: errors.append(True))
                    return page

                def login(page, endpoint, token):
                    page.goto(endpoint + '/_simulator/console/')
                    expect(page.get_by_role('heading', name='Open your local sandbox', exact=True)).to_be_visible()
                    page.get_by_label('Access token', exact=True).fill(token)
                    page.get_by_role('button', name='Open sandbox', exact=True).click()
                    expect(page.get_by_role('button', name='Account menu', exact=True)).to_be_visible()
                    cookies = [c for c in page.context.cookies(endpoint) if c['name'].startswith('tcsim_')]
                    assert len(cookies) == 1 and cookies[0]['value'] != token, 'Browser must receive a distinct minted session'
                    return cookies[0]

                def audit(page, label, width):
                    assert_reflow(page)
                    page.evaluate(axe)
                    result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                    assert not result['violations'], [(v['id'], len(v['nodes'])) for v in result['violations']]
                    page.screenshot(path=str(REPORTS/f'crossprovider-{label}-{width}.png'), full_page=True, animations='disabled')
                    audits.append([label, width])

                def gcp_media(endpoint, token, bucket, name, expected=200):
                    return native(endpoint, token, 'GET', f'{GCP}/b/{bucket}/o/{urllib.parse.quote(name, safe="")}?alt=media', expected=expected)[0]

                def azure(endpoint, token, method, tail, body=None, expected=200):
                    return native(endpoint, token, method, AZURE+tail, body,
                                  {'x-ms-version': VERSION, **({'x-ms-blob-type': 'BlockBlob', 'Content-Type': 'text/plain'} if method=='PUT' and body is not None else {})}, expected)

                def downloaded(page, button, wanted):
                    with page.expect_download() as pending:
                        button.click()
                    item = pending.value
                    assert item.failure() is None, 'Browser download failed'
                    path = item.path()
                    assert path and Path(path).read_bytes() == wanted, 'Actual download bytes differ from uploaded fixture'

                owner = page_for(a); first_cookie = login(owner, a, operator)
                peer = page_for(b); login(peer, b, other)
                # Services entries are real controls; the actual flows below also exercise bookmark routes.
                owner.get_by_role('button', name='Services', exact=True).click()
                expect(owner.get_by_role('menuitem', name='Google Cloud · Cloud Storage', exact=True)).to_be_visible()
                expect(owner.get_by_role('menuitem', name='Azure · Blob Storage', exact=True)).to_be_visible()
                owner.keyboard.press('Escape')
                for width in (1440, 390):
                    owner.set_viewport_size({'width': width, 'height': 1000})
                    bucket = f'browser-gcp-{width}'
                    container = f'browser-azure-{width}'
                    object_name = 'notes/%2F-日本語.txt'
                    payload = f'Independent browser fixture {width}\n日本語\n'.encode()
                    # Identical resource names in the second process must never share bytes/state.
                    native(b, other, 'POST', GCP+'/b?project=tenkacloud-local', {'name': bucket})
                    native(b, other, 'POST', f'{GCP_UPLOAD}/b/{bucket}/o?'+urllib.parse.urlencode({'uploadType':'media','name':object_name}), b'peer-gcp', {'Content-Type':'text/plain'})
                    azure(b, other, 'PUT', '/'+container+'?restype=container', expected=201)
                    azure(b, other, 'PUT', '/'+container+'/'+urllib.parse.quote(object_name, safe='/'), b'peer-azure', 201)

                    owner.goto(a+'/_simulator/console/#/gcp-storage')
                    view = owner.get_by_role('region', name='Cloud Storage', exact=True)
                    expect(view.get_by_role('heading', name='Buckets', exact=True)).to_be_visible()
                    expect(view.get_by_role('button', name='Create bucket', exact=True)).to_be_enabled()
                    await_cancel = view.get_by_role('button', name='Create bucket', exact=True)
                    await_cancel.focus(); owner.keyboard.press('Enter')
                    expect(view.get_by_label('Bucket name', exact=True)).to_be_focused()
                    audit(owner, 'gcp-create', width)
                    view.get_by_role('button', name='Cancel', exact=True).click()
                    data, _ = native(a, operator, 'GET', GCP+'/b?project=tenkacloud-local')
                    assert not json.loads(data)['items'], 'Cancel must not create a bucket'
                    view.get_by_role('button', name='Create bucket', exact=True).click()
                    view.get_by_label('Bucket name', exact=True).fill(bucket)
                    view.get_by_role('button', name='Create', exact=True).click()
                    expect(view.get_by_role('button', name=bucket, exact=True)).to_be_visible()
                    assert [item['name'] for item in json.loads(native(a,operator,'GET',GCP+'/b?project=tenkacloud-local')[0])['items']] == [bucket]
                    view.get_by_role('button', name=bucket, exact=True).click()
                    expect(view.get_by_role('button', name='Upload files', exact=True)).to_be_enabled()
                    view.get_by_role('button', name='Upload files', exact=True).click()
                    view.get_by_label('Text file', exact=True).set_input_files({'name':'notes.txt','mimeType':'text/plain','buffer':payload})
                    view.get_by_label('Object name', exact=True).fill(object_name)
                    audit(owner, 'gcp-upload', width)
                    view.get_by_role('button', name='Upload', exact=True).click()
                    expect(view.get_by_role('button', name='Download '+object_name, exact=True)).to_be_enabled()
                    assert gcp_media(a,operator,bucket,object_name) == payload
                    assert gcp_media(b,other,bucket,object_name) == b'peer-gcp'
                    audit(owner, 'gcp-objects', width)
                    downloaded(owner, view.get_by_role('button', name='Download '+object_name, exact=True), payload)
                    native(a,operator,'DELETE',GCP+'/b/'+bucket,expected=409)  # Empty-only server contract.
                    view.get_by_role('button', name='Delete object '+object_name, exact=True).click()
                    view.get_by_label('Type the exact object name to confirm', exact=True).fill(object_name)
                    audit(owner, 'gcp-delete', width)
                    view.get_by_role('button', name='Delete', exact=True).click()
                    expect(view.get_by_role('button', name='Download '+object_name, exact=True)).to_have_count(0)
                    gcp_media(a,operator,bucket,object_name,404)
                    view.get_by_role('button', name='Back to buckets', exact=True).click()
                    view.get_by_role('button', name='Delete bucket '+bucket, exact=True).click()
                    view.get_by_label('Type the exact bucket name to confirm', exact=True).fill(bucket)
                    view.get_by_role('button', name='Delete', exact=True).click()
                    expect(view.get_by_role('button', name=bucket, exact=True)).to_have_count(0)
                    assert not json.loads(native(a,operator,'GET',GCP+'/b?project=tenkacloud-local')[0])['items'], 'Deleted bucket remains in actual project list'
                    # Factory cannot infer project for a missing bucket; owner policy conceals it with403.
                    native(a,operator,'GET',GCP+'/b/'+bucket,expected=403)

                    owner.goto(a+'/_simulator/console/#/azure-blob')
                    azure_view = owner.locator('.azure-blob-view')
                    expect(azure_view.get_by_role('button', name='+ Container', exact=True)).to_be_enabled()
                    azure_view.get_by_role('button', name='+ Container', exact=True).click()
                    modal = owner.get_by_role('dialog', name='New container', exact=True)
                    modal.get_by_label('Name', exact=True).fill(container)
                    audit(owner, 'azure-create', width)
                    modal.get_by_role('button', name='Create', exact=True).click()
                    expect(azure_view.get_by_role('link', name=container, exact=True)).to_be_visible()
                    azure(a,operator,'HEAD','/'+container+'?restype=container')
                    azure_view.get_by_role('link', name=container, exact=True).click()
                    expect(azure_view.get_by_role('button', name='Upload', exact=True)).to_be_enabled()
                    azure_view.get_by_role('button', name='Upload', exact=True).click()
                    modal = owner.get_by_role('dialog', name='Upload blob', exact=True)
                    modal.locator('input[type=file]').set_input_files({'name':'notes.txt','mimeType':'text/plain','buffer':payload})
                    modal.get_by_label('Blob name', exact=True).fill(object_name)
                    audit(owner, 'azure-upload', width)
                    modal.get_by_role('button', name='Upload', exact=True).click()
                    expect(azure_view.get_by_text(object_name, exact=True)).to_be_visible()
                    tail = '/'+container+'/'+urllib.parse.quote(object_name,safe='/')
                    assert azure(a,operator,'GET',tail)[0] == payload
                    assert azure(b,other,'GET',tail)[0] == b'peer-azure'
                    azure_view.get_by_role('radio', name='Select '+object_name, exact=True).check()
                    audit(owner, 'azure-blobs', width)
                    downloaded(owner,azure_view.get_by_role('button',name='Download',exact=True),payload)
                    azure_view.get_by_role('button',name='Delete',exact=True).click()
                    modal=owner.get_by_role('dialog',name='Delete blob',exact=True)
                    modal.get_by_label('Type '+object_name+' to confirm deletion',exact=True).fill(object_name)
                    audit(owner,'azure-delete',width)
                    modal.get_by_role('button',name='Delete',exact=True).click()
                    expect(azure_view.get_by_text(object_name,exact=True)).to_have_count(0)
                    azure(a,operator,'GET',tail,expected=404)
                    azure_view.get_by_role('link',name='Containers',exact=True).click()
                    azure_view.get_by_role('radio',name='Select '+container,exact=True).check()
                    azure_view.get_by_role('button',name='Delete',exact=True).click()
                    modal=owner.get_by_role('dialog',name='Delete container',exact=True)
                    modal.get_by_label('Type '+container+' to confirm deletion',exact=True).fill(container)
                    modal.get_by_role('button',name='Delete',exact=True).click()
                    expect(azure_view.get_by_role('link',name=container,exact=True)).to_have_count(0)
                    azure(a,operator,'HEAD','/'+container+'?restype=container',expected=404)
                    # Remove only explicit peer fixtures; assert its bytes survived owner teardown.
                    assert gcp_media(b,other,bucket,object_name) == b'peer-gcp'
                    native(b,other,'DELETE',f'{GCP}/b/{bucket}/o/'+urllib.parse.quote(object_name,safe=''),expected=204)
                    native(b,other,'DELETE',GCP+'/b/'+bucket,expected=204)
                    assert azure(b,other,'GET',tail)[0] == b'peer-azure'
                    azure(b,other,'DELETE','/'+container+'?restype=container',expected=202)
                # AWS identity grants do not make the participant a configured GCP/Azure owner.
                native(a,operator,'PUT','/_simulator/v1/iam/principals/reader/policy',{'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'*','Resource':'*'}]})
                participant=json.loads(native(a,operator,'POST','/_simulator/v1/access',{'Principal':'reader'})[0])['AccessToken']
                limited=page_for(a,390);limited_cookie=login(limited,a,participant)
                for route in ('gcp-storage','azure-blob'):
                    limited.goto(a+'/_simulator/console/#/'+route)
                    if route=='gcp-storage':
                        expect(limited.get_by_role('alert').filter(has_text='403')).to_be_visible()
                    else:
                        expect(limited.get_by_text('Operation failed',exact=True)).to_be_visible()
                        denial = limited.get_by_text('Access denied. This Azure exercise subject does not have permission for this operation.', exact=True).filter(visible=True)
                        expect(denial).to_have_count(1); expect(denial).to_be_visible()
                    audit(limited,'reader-'+route,390)
                before_gcp=native(a,operator,'GET',GCP+'/b?project=tenkacloud-local')[0]
                before_azure=azure(a,operator,'GET','?comp=list')[0]
                native(a,participant,'GET',GCP+'/b?project=tenkacloud-local',expected=403)
                native(a,participant,'POST',GCP+'/b?project=tenkacloud-local',{'name':'reader-must-not-create'},expected=403)
                azure(a,participant,'GET','?comp=list',expected=403)
                azure(a,participant,'PUT','/reader-must-not-create?restype=container',expected=403)
                assert native(a,operator,'GET',GCP+'/b?project=tenkacloud-local')[0]==before_gcp
                assert azure(a,operator,'GET','?comp=list')[0]==before_azure
                native(a,other,'GET',GCP+'/b?project=tenkacloud-local',expected=401)
                azure(a,other,'GET','?comp=list',expected=401)
                # Real Account menu sign-out revokes this browser, while SDK header auth survives.
                trigger=limited.get_by_role('button',name='Account menu',exact=True)
                trigger.focus();limited.keyboard.press('Enter')
                signout=limited.get_by_role('menuitem',name='Sign out',exact=True)
                expect(signout).to_be_visible();signout.focus();limited.keyboard.press('Enter')
                expect(limited.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible()
                wire=p.request.new_context()
                try:
                    old_cookie=limited_cookie['name']+'='+limited_cookie['value']
                    assert wire.get(a+GCP+'/b?project=tenkacloud-local',headers={'Cookie':old_cookie},max_redirects=0).status==401
                    assert wire.get(a+GCP+'/b?project=tenkacloud-local',max_redirects=0).status==401
                    assert wire.get(b+GCP+'/b?project=tenkacloud-local',headers={'Cookie':first_cookie['name']+'='+first_cookie['value']},max_redirects=0).status==401
                finally:wire.dispose()
                native(a,participant,'GET',GCP+'/b?project=tenkacloud-local',expected=403)  # Valid SDK credential, denied data authorization.
                renewed=login(limited,a,participant);assert renewed['value']!=limited_cookie['value']
                # API reference examples must stay static until actual Scalar Send.
                native(a,operator,'PUT','/crossprovider-docs-fixture',expected=200)
                owner.goto(a+'/_simulator/docs/#tag/s3-basic-object-storage/GET/')
                expect(owner.get_by_role('button',name='Test Request (get /)',exact=True)).to_be_visible(timeout=30000)
                trigger=owner.locator('#s3-result-trigger');expect(trigger).to_be_hidden()
                expect(owner.get_by_text('Static examples below are sample documentation, not your current state.',exact=True).first).to_be_visible()
                owner.get_by_role('button',name='Test Request (get /)',exact=True).click()
                with owner.expect_response(lambda r:r.request.method=='GET' and r.url.rstrip('/')==a) as sent:
                    owner.get_by_role('button').filter(has_text='Send Request').last.click()
                assert sent.value.status==200 and 'crossprovider-docs-fixture' in sent.value.text()
                expect(trigger).to_contain_text('1 bucket')
                native(a,operator,'DELETE','/crossprovider-docs-fixture',expected=204)
                assert not errors,'Browser page errors (see evidence counts; no secret-bearing error text emitted)'
                assert not outside,'Unexpected network request outside the selected local CLI'
                report={'actualBinaryTwoCLIs':True,'twoSQLiteDBs':True,'browserSessionsMinted':True,
                        'gcpAndAzureActualUILifecycle':[1440,390],'nativeHTTPStateOracle':True,
                        'downloadExactBytes':True,'peerSameNameStatePreserved':True,'readerNative403FramesUnchanged':True,
                        'readerUI403':True,'crossTeamTokensAndCookie401':True,'logoutReplay401Relogin':True,
                        'scalarStaticThenActualSend':True,'axeStates':audits,'outsideRequests':0,'pageErrors':0,
                        'officialSDKExecuted':False,'actualCloud':False,'cleanup':'DELETED'}
                (REPORTS/'crossprovider-console.json').write_text(json.dumps(report,indent=2)+'\n')
                print(json.dumps(report))
            finally:
                for context in contexts:context.close()
                browser.close()


if __name__=='__main__':
    run_crossprovider_console()
