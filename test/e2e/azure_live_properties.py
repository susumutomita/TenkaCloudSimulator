"""Actual embedded Azure Properties -> native HEAD acceptance (no SDK claim).

Requires a freshly embedded binary containing azure-blob.jsx, Chromium and axe.
No overlay, fake response values or cloud access. Delayed/error routes below
forward actual native responses; only their delivery timing/auth context varies.
Source-only until the integration owner schedules real execution.
"""
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import time
import urllib.parse
from playwright.sync_api import sync_playwright, expect
from check import running, REPORTS
from crossprovider_console import AZURE, VERSION, native
from reflow import assert_reflow


def run_azure_live_properties():
    axe = Path(os.environ['AXE_SCRIPT']).read_text()
    errors, outside, audits, held = [], [], [], []
    container, key = 'live-properties', 'folder/percent%25-日本語.txt'
    tail = '/' + container + '/' + urllib.parse.quote(key, safe='/')
    path = AZURE + tail
    with tempfile.TemporaryDirectory(prefix='azure-live-properties-') as directory:
        with running(db=Path(directory)/'owner.sqlite', authenticated=True) as (endpoint, token), running(db=Path(directory)/'peer.sqlite', authenticated=True) as (peer, other), sync_playwright() as p:
            def wire(method, target, body=None, headers=None, expected=200, host=endpoint, credential=token):
                return native(host, credential, method, target, body, {'x-ms-version': VERSION, **(headers or {})}, expected)

            def put(body, label, host=endpoint, credential=token):
                return wire('PUT', path, body, {'x-ms-blob-type': 'BlockBlob', 'Content-Type': 'text/plain; charset=utf-8', 'x-ms-meta-proof': label}, 201, host, credential)[1]

            wire('PUT', AZURE+'/'+container+'?restype=container', expected=201)
            wire('PUT', AZURE+'/'+container+'?restype=container', expected=201, host=peer, credential=other)
            put(b'original-list-body', 'listed-old')
            put(b'peer-only-body', 'peer-only', peer, other)
            native(endpoint, token, 'PUT', '/_simulator/v1/iam/principals/properties-reader/policy', {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': '*', 'Resource': '*'}]})
            reader = json.loads(native(endpoint, token, 'POST', '/_simulator/v1/access', {'Principal': 'properties-reader'})[0])['AccessToken']
            browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
            contexts = []
            try:
                for width in (1440, 390):
                    context = browser.new_context(viewport={'width': width, 'height': 1000})
                    contexts.append(context)
                    heads, body_reads, mode = [], [], {'value': 'normal'}
                    url = endpoint + path

                    def guard(route):
                        if route.request.url.startswith(endpoint+'/') or route.request.url.startswith(('blob:', 'data:')):
                            route.continue_()
                        else:
                            outside.append(True); route.abort()
                    context.route('**/*', guard)

                    def properties_route(route):
                        if route.request.method != 'HEAD':
                            if route.request.method == 'GET':
                                body_reads.append(True)
                            route.continue_(); return
                        heads.append(True)
                        headers = None
                        if mode['value'] == 'denied':
                            # Real participant credentials with no configured Azure
                            # ownership: actual server403, not a fabricated error.
                            headers = {k: v for k, v in route.request.headers.items() if k.lower() not in ('cookie', 'x-simulator-token', 'x-simulator-principal')}
                            headers['x-simulator-token'] = reader
                        response = route.fetch(headers=headers) if headers is not None else route.fetch()
                        if mode['value'] == 'hold':
                            assert response.status == 200
                            held.append((route, response))
                        else:
                            assert response.status == (403 if mode['value'] == 'denied' else 200 if mode['value'] == 'normal' else 404)
                            route.fulfill(response=response)
                    context.route(url, properties_route)
                    page = context.new_page()
                    page.on('pageerror', lambda error: errors.append(True))
                    page.goto(endpoint+'/_simulator/console/#/azure-blob')
                    page.get_by_label('Access token', exact=True).fill(token)
                    page.get_by_role('button', name='Open sandbox', exact=True).click()
                    expect(page.get_by_role('button', name='Account menu', exact=True)).to_be_visible()
                    page.goto(endpoint+'/_simulator/console/#/azure-blob')
                    view = page.locator('.azure-blob-view')
                    expect(view.get_by_role('link', name=container, exact=True)).to_be_visible()
                    view.get_by_role('link', name=container, exact=True).click()
                    expect(view.get_by_role('button', name=key, exact=True)).to_be_visible()
                    # List row is deliberately stale before opening the pane.
                    before = len(heads)
                    put(('new-body-'+str(width)).encode(), 'fresh-'+str(width))
                    actual = wire('HEAD', path)[1]
                    view.get_by_role('button', name=key, exact=True).click()
                    dialog = page.get_by_role('dialog', name='Blob properties', exact=True)

                    def property_value(label, wanted):
                        expect(dialog.locator('dt').filter(has_text=re.compile('^'+re.escape(label)+'$')).locator('xpath=following-sibling::dd[1]')).to_have_text(wanted)

                    for label, name in [('ETag', 'ETag'), ('Size (bytes)', 'Content-Length'), ('Content type', 'Content-Type'), ('Last modified', 'Last-Modified'), ('Blob type', 'x-ms-blob-type')]:
                        assert actual.get(name) is not None
                        property_value(label, actual[name])
                    property_value('Name', key)
                    property_value('proof', actual['x-ms-meta-proof'])
                    assert len(heads) == before+1 and not body_reads
                    # Translation must not remount, refetch or rewrite raw metadata.
                    dialog.get_by_role('combobox', name='Language / 言語', exact=True).select_option('ja')
                    dialog = page.get_by_role('dialog', name='BLOB のプロパティ', exact=True)
                    property_value('コンテンツの種類', actual['Content-Type'])
                    property_value('proof', actual['x-ms-meta-proof'])
                    assert len(heads) == before+1
                    dialog.get_by_role('combobox', name='Language / 言語', exact=True).select_option('en')
                    dialog = page.get_by_role('dialog', name='Blob properties', exact=True)
                    assert_reflow(page); page.evaluate(axe)
                    result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                    assert not result['violations'], [(v['id'], len(v['nodes'])) for v in result['violations']]
                    page.screenshot(path=str(REPORTS/f'azure-live-properties-{width}.png'), full_page=True, animations='disabled')
                    audits.append(width)
                    dialog.get_by_role('button', name='Close', exact=True).click()

                    # Loading cannot reuse any previous success. A genuine old
                    # HEAD response delivered after Close/unmount is ignored.
                    mode['value'] = 'hold'
                    view.get_by_role('button', name=key, exact=True).click()
                    expect(dialog.get_by_role('status')).to_have_text('Loading blob properties…')
                    expect(dialog.locator('dd')).to_have_count(0)
                    deadline = time.monotonic()+5
                    while not held:
                        assert time.monotonic()<deadline, 'Actual HEAD did not reach the delivery barrier'
                        page.wait_for_timeout(10)
                    dialog.get_by_role('button', name='Close', exact=True).click()
                    # Same-document provider change destroys the original leaf.
                    page.get_by_role('navigation', name='Cloud provider', exact=True).get_by_role('button', name='Google Cloud', exact=True).click()
                    route, response = held.pop()
                    try:
                        route.fulfill(response=response)
                    except Exception as error:
                        # Chromium may already cancel the route on abort. Other
                        # delivery errors are not silently accepted.
                        assert any(word in str(error).lower() for word in ('cancel', 'closed', 'invalid interception', 'already handled')), type(error).__name__
                    page.get_by_role('navigation', name='Cloud provider', exact=True).get_by_role('button', name='Azure', exact=True).click()
                    expect(view.get_by_role('link', name=container, exact=True)).to_be_visible()
                    expect(page.get_by_role('dialog')).to_have_count(0)
                    view.get_by_role('link', name=container, exact=True).click()
                    expect(view.get_by_role('button', name=key, exact=True)).to_be_visible()
                    mode['value'] = 'denied'
                    view.get_by_role('button', name=key, exact=True).click()
                    expect(dialog.get_by_role('alert')).to_contain_text('Access denied.')
                    expect(dialog.locator('dd')).to_have_count(0)
                    expect(dialog.get_by_role('status')).to_have_count(0)
                    dialog.get_by_role('button', name='Close', exact=True).click()
                    # Repaired credentials retry loads the live record; the
                    # failed read did not alter owner or peer data.
                    mode['value'] = 'normal'
                    view.get_by_role('button', name=key, exact=True).click()
                    property_value('ETag', actual['ETag'])
                    dialog.get_by_role('button', name='Close', exact=True).click()
                    # A second actual old response must also be discarded when
                    # the host exercise identity changes (the parent's real key).
                    mode['value'] = 'hold'
                    view.get_by_role('button', name=key, exact=True).click()
                    expect(dialog.get_by_role('status')).to_have_text('Loading blob properties…')
                    deadline = time.monotonic()+5
                    while not held:
                        assert time.monotonic()<deadline, 'Second native HEAD did not reach barrier'
                        page.wait_for_timeout(10)
                    dialog.get_by_role('button', name='Close', exact=True).click()

                    def choose_identity(identity):
                        page.locator('.cloud-session summary[aria-label="Account menu"]').click()
                        page.get_by_role('button', name='Choose identity', exact=True).click()
                        chooser = page.get_by_role('dialog', name='Choose identity', exact=True)
                        chooser.get_by_label('Principal name', exact=True).fill(identity)
                        chooser.get_by_role('button', name='Choose identity', exact=True).click()

                    choose_identity('properties-reader')
                    expect(view.get_by_text('Access denied. This Azure exercise subject does not have permission for this operation.', exact=True)).to_be_visible()
                    route, response = held.pop()
                    try:
                        route.fulfill(response=response)
                    except Exception as error:
                        assert any(word in str(error).lower() for word in ('cancel', 'closed', 'invalid interception', 'already handled')), type(error).__name__
                    expect(page.get_by_role('dialog')).to_have_count(0)
                    expect(view.get_by_text(actual['x-ms-meta-proof'], exact=True)).to_have_count(0)
                    expect(view.get_by_text(key, exact=True)).to_have_count(0)
                    choose_identity('simulator-admin')
                    expect(view.get_by_role('link', name=container, exact=True)).to_be_visible()
                    view.get_by_role('link', name=container, exact=True).click()
                    expect(view.get_by_role('button', name=key, exact=True)).to_be_visible()
                    wire('DELETE', path, expected=202)
                    mode['value'] = 'missing'
                    view.get_by_role('button', name=key, exact=True).click()
                    expect(dialog.get_by_role('alert')).to_contain_text('resource was not found')
                    expect(dialog.locator('dd')).to_have_count(0)
                    dialog.get_by_role('button', name='Close', exact=True).click()
                    put(b'repaired-current-body', 'repair')
                    mode['value'] = 'normal'
                    view.get_by_role('button', name=key, exact=True).click()
                    property_value('proof', 'repair')
                    dialog.get_by_role('button', name='Close', exact=True).click()
                    assert wire('GET', path, host=peer, credential=other)[0] == b'peer-only-body'
                    assert not body_reads, 'Properties must never fetch blob content'
                    context.close(); contexts.remove(context)
                assert not errors and not outside
                wire('DELETE', AZURE+'/'+container+'?restype=container', expected=202)
                wire('DELETE', AZURE+'/'+container+'?restype=container', expected=202, host=peer, credential=other)
                REPORTS.joinpath('azure-live-properties.json').write_text(json.dumps({'actualEmbeddedBinary': True, 'nativeLiveHEAD': True, 'bodyFetches': 0, 'staleListReplacement': True, 'localeKeepsLiveRecord': True, 'cancelUnmountLateReplyGuard': True, 'principalChangeLateReplyGuard': True, 'actual403And404NoCachedFallback': True, 'repairedRetry': True, 'peerPreserved': True, 'axeWidths': audits, 'pageErrors': len(errors), 'externalRequests': len(outside)}, indent=2)+'\n')
            finally:
                for route, _ in held:
                    try:
                        route.abort()
                    except Exception:
                        pass  # Teardown only; no acceptance assertion depends on it.
                for context in contexts:
                    context.close()
                browser.close()


if __name__ == '__main__':
    run_azure_live_properties()
