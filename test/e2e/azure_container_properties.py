"""Read-only Container Properties against a matching actual embedded binary.

Native HTTP is the oracle, not an official SDK/live Portal compatibility claim.
All fixtures are dummy data in two private local processes/SQLite databases.
No fake response bodies, cloud access, dependency installation or optional skip.
"""
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import tempfile
import time
from playwright.sync_api import sync_playwright, expect
from check import running, REPORTS
from crossprovider_console import AZURE, VERSION, native
from reflow import assert_reflow


def run_container_properties():
    axe = Path(os.environ['AXE_SCRIPT']).read_text()
    container = 'container-same-name'
    path = AZURE+'/'+container+'?restype=container'
    errors, outside, audits, held, cancellation = [], [], [], [], []
    with tempfile.TemporaryDirectory(prefix='azure-container-properties-') as directory:
        owner_db = Path(directory)/'owner.sqlite'
        with running(db=owner_db, authenticated=True) as (endpoint, token), running(db=Path(directory)/'peer.sqlite', authenticated=True) as (peer, peer_token), sync_playwright() as p:
            def wire(method, target=path, body=None, headers=None, expected=200, host=endpoint, credential=token):
                return native(host, credential, method, target, body, {'x-ms-version': VERSION, **(headers or {})}, expected)

            def create(metadata, host=endpoint, credential=token):
                return wire('PUT', headers={'x-ms-meta-'+key: value for key, value in metadata.items()}, expected=201, host=host, credential=credential)[1]

            create({'proof': 'peer-only'}, peer, peer_token)
            peer_blob = AZURE+'/'+container+'/peer-body'
            wire('PUT', peer_blob, b'peer-body-unchanged', {'x-ms-blob-type': 'BlockBlob'}, 201, peer, peer_token)
            native(endpoint, token, 'PUT', '/_simulator/v1/iam/principals/container-reader/policy', {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': '*', 'Resource': '*'}]})
            participant = json.loads(native(endpoint, token, 'POST', '/_simulator/v1/access', {'Principal': 'container-reader'})[0])['AccessToken']
            browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
            contexts = []
            try:
                for width in (1440, 390):
                    create({'proof': 'old-listed'})
                    context = browser.new_context(viewport={'width': width, 'height': 1000})
                    contexts.append(context)
                    page = context.new_page()
                    page.on('pageerror', lambda error: errors.append(True))
                    calls, mode = [], {'value': 'normal'}

                    def guard(route):
                        if route.request.url.startswith(endpoint+'/') or route.request.url.startswith(('blob:', 'data:')):
                            route.continue_()
                        else:
                            outside.append(True); route.abort()
                    context.route('**/*', guard)
                    page.on('request', lambda request: calls.append((request.method, request.url)) if request.url.startswith(endpoint+AZURE) else None)

                    def container_head(route):
                        if route.request.method != 'HEAD':
                            route.continue_(); return
                        if mode['value'] == 'denied':
                            headers = {key: value for key, value in route.request.headers.items() if key.lower() not in ('cookie', 'x-simulator-principal', 'x-simulator-token')}
                            headers['x-simulator-token'] = participant
                            response = route.fetch(headers=headers)
                        else:
                            response = route.fetch()
                        if mode['value'] == 'hold':
                            assert response.status == 200
                            held.append((route, response))
                        else:
                            assert response.status == {'normal': 200, 'denied': 403, 'missing': 404, 'broken': 500}[mode['value']]
                            route.fulfill(response=response)
                    context.route(endpoint+path, container_head)
                    page.goto(endpoint+'/_simulator/login')
                    page.get_by_label('Access token', exact=True).fill(token)
                    with page.expect_response(lambda response: response.request.method == 'POST' and response.url == endpoint+'/_simulator/login') as signed_in:
                        page.get_by_role('button', name='Open sandbox', exact=True).click()
                    assert signed_in.value.status == 303
                    page.wait_for_url(endpoint+'/_simulator/console/')
                    expect(page.get_by_role('button', name='Services', exact=True)).to_be_visible()
                    page.goto(endpoint+'/_simulator/console/#/azure-blob')
                    view = page.locator('.azure-blob-view')
                    expect(view.get_by_role('link', name=container, exact=True)).to_be_visible()
                    menu = view.locator('.azure-container-more')
                    summary = menu.locator('summary')
                    expect(summary).to_have_attribute('aria-disabled', 'true')
                    view.get_by_role('checkbox', name='Select '+container, exact=True).check()
                    expect(summary).to_have_attribute('aria-disabled', 'false')
                    dialog = page.get_by_role('dialog', name='Container properties', exact=True)

                    def open_properties():
                        summary.focus(); summary.press('Enter')
                        item = menu.get_by_role('button', name='Container properties', exact=True)
                        expect(item).to_be_visible(); item.focus(); item.press('Enter')
                        expect(dialog).to_be_visible()

                    def value(label, expected):
                        expect(dialog.locator('dt').filter(has_text=re.compile('^'+re.escape(label)+'$')).locator('xpath=following-sibling::dd[1]')).to_have_text(expected)

                    def current_properties(headers, metadata):
                        value('Name', container); value('ETag', headers['ETag']); value('Last modified', headers['Last-Modified'])
                        for key, wanted in metadata.items():
                            value(key, wanted)
                        for absent in ('Blob type', 'Size (bytes)', 'Content type'):
                            expect(dialog.locator('dt').filter(has_text=re.compile('^'+re.escape(absent)+'$'))).to_have_count(0)
                        expect(dialog.get_by_role('status')).to_have_count(0)

                    def close_properties():
                        dialog.get_by_role('button', name='Close', exact=True).click()
                        expect(dialog).to_have_count(0); expect(summary).to_be_focused()

                    # Recreate only the empty private fixture after listing.
                    # No unsupported metadata-update API is invented.
                    before = len(calls)
                    wire('DELETE', expected=202)
                    metadata = {'proof': 'fresh <&" text '+str(width)}
                    create(metadata)
                    body, actual = wire('HEAD'); assert body == b''
                    open_properties(); current_properties(actual, metadata)
                    assert calls[before:] == [('HEAD', endpoint+path)], 'Properties issued an extra list/body/mutation request'
                    dialog.get_by_role('combobox', name='Language / 言語', exact=True).select_option('ja')
                    dialog = page.get_by_role('dialog', name='コンテナーのプロパティ', exact=True)
                    value('名前', container); value('proof', metadata['proof'])
                    assert calls[before:] == [('HEAD', endpoint+path)]
                    dialog.get_by_role('combobox', name='Language / 言語', exact=True).select_option('en')
                    dialog = page.get_by_role('dialog', name='Container properties', exact=True)
                    assert_reflow(page); page.evaluate(axe)
                    result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                    assert not result['violations'], [(item['id'], len(item['nodes'])) for item in result['violations']]
                    page.screenshot(path=str(REPORTS/f'azure-container-properties-{width}.png'), full_page=True, animations='disabled')
                    audits.append(width)
                    page.keyboard.press('Escape'); expect(dialog).to_have_count(0); expect(summary).to_be_focused()

                    # Maximum field count and empty metadata both use real HEAD.
                    for metadata in ({'k'+str(i): 'x'*200 for i in range(32)}, {}):
                        wire('DELETE', expected=202); create(metadata)
                        _, actual = wire('HEAD')
                        open_properties(); current_properties(actual, metadata)
                        expect(dialog.locator('dt')).to_have_count(3+len(metadata))
                        if not metadata:
                            expect(dialog.get_by_text('No metadata.', exact=True)).to_be_visible()
                        close_properties()

                    # Same-leaf old/new container HEAD ordering, with truthful
                    # abort limits (no claim an old JS callback ran past abort).
                    leaf = view.element_handle(); mode['value'] = 'hold'
                    open_properties(); expect(dialog.get_by_role('status')).to_have_text('Loading container properties…')
                    deadline = time.monotonic()+5
                    while len(held) != 1:
                        assert time.monotonic()<deadline; page.wait_for_timeout(10)
                    old_route, old_response = held[0]
                    close_properties(); wire('DELETE', expected=202); metadata={'proof': 'new-request'}; create(metadata)
                    _, actual = wire('HEAD')
                    assert actual['ETag'] != old_response.headers['etag'], 'Recreated fixture did not get a fresh ETag'
                    open_properties(); deadline=time.monotonic()+5
                    while len(held) != 2:
                        assert time.monotonic()<deadline; page.wait_for_timeout(10)
                    assert view.evaluate('(node, old)=>node===old', leaf)
                    outcome='fulfill-returned'
                    try:
                        old_route.fulfill(response=old_response)
                    except Exception as error:
                        assert any(word in str(error).lower() for word in ('cancel', 'closed', 'invalid interception', 'already handled')), type(error).__name__
                        outcome='already-canceled'
                    page.evaluate('()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))')
                    expect(dialog.get_by_role('status')).to_have_text('Loading container properties…')
                    expect(dialog.locator('dd')).to_have_count(0)
                    expect(dialog.locator('.azure-pane-body')).to_have_attribute('aria-busy', 'true')
                    held[1][0].fulfill(response=held[1][1]); held.clear()
                    current_properties(actual, metadata); close_properties()
                    cancellation.append({'width': width, 'sameLeaf': True, 'oldRelease': outcome, 'noIndependentPostAbortJSProof': True})
                    mode['value']='denied'; open_properties()
                    expect(dialog.get_by_role('alert')).to_contain_text('Access denied.')
                    expect(dialog.locator('dd')).to_have_count(0); close_properties()
                    mode['value']='normal'; open_properties(); current_properties(actual, metadata); close_properties()

                    # Real bounded server500 from corrupt dummy metadata, with
                    # exact repair; no fabricated response or production API edit.
                    with sqlite3.connect(owner_db) as database:
                        environment, saved = database.execute('SELECT environment,metadata FROM azure_blob_containers WHERE account=? AND name=?', ('tenkacloudlocal', container)).fetchone()
                        database.execute('UPDATE azure_blob_containers SET metadata=? WHERE environment=? AND account=? AND name=?', ('{broken', environment, 'tenkacloudlocal', container))
                    try:
                        mode['value']='broken'; open_properties()
                        expect(dialog.get_by_role('alert')).to_contain_text('request did not complete')
                        expect(dialog.locator('dd')).to_have_count(0); close_properties()
                    finally:
                        with sqlite3.connect(owner_db) as database:
                            database.execute('UPDATE azure_blob_containers SET metadata=? WHERE environment=? AND account=? AND name=?', (saved, environment, 'tenkacloudlocal', container))
                    wire('DELETE', expected=202); mode['value']='missing'; open_properties()
                    expect(dialog.get_by_role('alert')).to_contain_text('resource was not found')
                    expect(dialog.locator('dd')).to_have_count(0); close_properties()
                    create({'proof': 'restored'}); mode['value']='normal'
                    open_properties(); _, actual=wire('HEAD'); current_properties(actual, {'proof':'restored'}); close_properties()

                    # Blob versus container with EXACTLY the same display name:
                    # entering blobs still navigates, and Blob Properties retains
                    # all its original required fields instead of container data.
                    blob_path=AZURE+'/'+container+'/'+container
                    wire('PUT', blob_path, b'actual-blob-body', {'x-ms-blob-type':'BlockBlob','Content-Type':'text/plain','x-ms-meta-proof':'blob-only'}, 201)
                    _, actual_blob=wire('HEAD', blob_path)
                    view.get_by_role('link', name=container, exact=True).click()
                    view.get_by_role('button', name=container, exact=True).click()
                    blob_dialog=page.get_by_role('dialog', name='Blob properties', exact=True)
                    for label, field in [('ETag','ETag'),('Blob type','x-ms-blob-type'),('Size (bytes)','Content-Length'),('Content type','Content-Type')]:
                        expect(blob_dialog.locator('dt').filter(has_text=re.compile('^'+re.escape(label)+'$')).locator('xpath=following-sibling::dd[1]')).to_have_text(actual_blob[field])
                    expect(blob_dialog.get_by_text('restored', exact=True)).to_have_count(0)
                    blob_dialog.get_by_role('button', name='Close', exact=True).click()
                    view.get_by_role('link', name='Containers', exact=True).click()
                    expect(view.get_by_role('table', name='Containers', exact=True)).to_be_visible()
                    expect(view.get_by_role('button', name='↻ Refresh', exact=True)).to_be_enabled()
                    view.get_by_role('checkbox', name='Select '+container, exact=True).check()
                    open_properties(); current_properties(actual, {'proof':'restored'}); close_properties()
                    assert wire('GET', peer_blob, host=peer, credential=peer_token)[0] == b'peer-body-unchanged'
                    assert wire('HEAD', host=peer, credential=peer_token)[1]['x-ms-meta-proof'] == 'peer-only'
                    assert not any(method not in ('GET','HEAD') for method,_ in calls), 'Read-only UI issued native mutation'
                    context.close(); contexts.remove(context)
                    wire('DELETE', expected=202)
                wire('DELETE', expected=202, host=peer, credential=peer_token)
                assert not errors and not outside
                REPORTS.joinpath('azure-container-properties.json').write_text(json.dumps({'actualEmbeddedBinary': True,'oracle':'native HTTP, not official Azure SDK','PC390Axe':audits,'nativeMetadataAndSystemHeaders':True,'noInventedBlobFields':True,'readOnlyUI':True,'sameNameBlobAndContainerSeparate':True,'staleListRecreationFresh':True,'metadata32AndEmpty':True,'native403404500AndRepair':True,'sameLeafOrdering':cancellation,'peerStatePreserved':True,'localeNoRefetch':True,'outside':0,'pageerrors':0,'actualCloud':False,'cleanup':'DELETED'},indent=2)+'\n')
            finally:
                for route,_ in held:
                    try:
                        route.abort()
                    except Exception:
                        pass
                for context in contexts:
                    context.close()
                browser.close()


if __name__ == '__main__':
    run_container_properties()
