"""SOURCE acceptance for root-integrated Sakura navigation and embedded assets.

Actual native HTTP + two CLI processes + SQLite, not SDK or real cloud parity.
No fake successful API responses. Requires a root-mounted #/sakura-disks view.
"""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import tempfile
import urllib.parse

from playwright.sync_api import sync_playwright, expect
from check import BINARY, REPORTS
from reflow import assert_reflow
from sakura_cli import CLI, BASE, create, frame


def run():
    axe = os.environ.get('AXE_SCRIPT')
    assert axe and Path(axe).is_file(), 'Canonical AXE_SCRIPT is mandatory'
    audits, stale = [], []
    with tempfile.TemporaryDirectory(prefix='sakura-console-') as directory:
        a = b = None
        browser = None
        contexts = []
        try:
            a = CLI(str(BINARY), Path(directory)/'a', 'console-A')
            b = CLI(str(BINARY), Path(directory)/'b', 'console-B')
            participant = a.call('POST', '/_simulator/v1/access', {'Principal': 'learner'})['AccessToken']
            denied = a.call('POST', '/_simulator/v1/access', {'Principal': 'other'})['AccessToken']
            peer_id = create(b, 'peer-only')
            peer = frame(b.db)
            caps = a.call('GET', '/_simulator/v1/capabilities')
            assert 'sakura' in caps['providers'] and caps['sakura']['endpoint'] == BASE
            assert caps['sakura']['availabilityReported'] is False
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
                errors, outside = [], []
                for width, height in [(1440,1000),(390,844)]:
                    context = browser.new_context(viewport={'width':width,'height':height})
                    contexts.append(context)
                    context.add_init_script(path=axe)
                    page = context.new_page()
                    page.set_default_timeout(10000)
                    page.on('pageerror', lambda error: errors.append(str(error)))
                    def origin(route):
                        if route.request.url.startswith(a.origin+'/'):
                            route.continue_()
                        else:
                            outside.append(route.request.url)
                            route.abort()
                    context.route('**/*', origin)
                    def login(token):
                        page.goto(a.origin+'/_simulator/console/')
                        page.get_by_label('Access token', exact=True).fill(token)
                        page.get_by_role('button', name='Open sandbox', exact=True).click()
                        page.wait_for_url(a.origin+'/_simulator/console/**')
                        page.goto(a.origin+'/_simulator/console/#/sakura-disks')
                    login(participant)
                    shell = page.locator('.sakura-console')
                    view = shell.locator('.sakura-disks')
                    expect(shell).to_be_visible()
                    expect(view.get_by_text('No Disk configurations', exact=True)).to_be_visible()
                    assert not shell.get_by_role('button', name='Servers', exact=True).count()
                    writes = []
                    page.on('request', lambda request: writes.append(request.method) if urllib.parse.urlsplit(request.url).path.startswith(BASE) and request.method in ('POST','PUT','DELETE') else None)
                    # Cancel is type=button; pressing Enter must submit the primary.
                    view.get_by_role('button', name='Add Disk', exact=True).click()
                    view.get_by_label('Name', exact=True).fill('cancelled')
                    view.get_by_role('button', name='Cancel', exact=True).click()
                    assert a.call('GET', BASE)['Disks'] == [] and writes == []
                    view.get_by_role('button', name='Add Disk', exact=True).click()
                    name = 'local-日本語-'+str(width)
                    view.get_by_label('Name', exact=True).fill(name)
                    view.get_by_label('Description', exact=True).fill('draft stays across locale')
                    page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                    expect(view.get_by_label('名前', exact=True)).to_have_value(name)
                    expect(view.get_by_label('説明', exact=True)).to_have_value('draft stays across locale')
                    page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                    expect(view.get_by_label('Name', exact=True)).to_have_value(name)
                    # Real Create commit followed by a real native read failure.
                    # The route forwards the real HTTP201 unchanged; no success
                    # is invented. SQLite schema is altered only in this owned DB.
                    created = []
                    def ack_then_read_fault(route):
                        if route.request.method != 'POST':
                            route.continue_(); return
                        response = route.fetch()
                        assert response.status == 201
                        created.append(response.json()['Disk']['ID'])
                        with sqlite3.connect(a.db) as db:
                            db.execute('ALTER TABLE sakura_disks RENAME COLUMN description TO private_description')
                        route.fulfill(response=response)
                    page.route(a.origin+BASE, ack_then_read_fault)
                    with page.expect_response(lambda response: response.request.method=='GET' and response.url.startswith(a.origin+BASE+'/') and response.status==500) as failed_read:
                        view.get_by_label('Name', exact=True).press('Enter')
                    assert failed_read.value.status==500
                    expect(view.get_by_role('button', name='Retry read', exact=True)).to_be_visible()
                    expect(view.get_by_role('button', name='Create Disk configuration', exact=True)).to_have_count(0)
                    assert len(created) == 1 and writes == ['POST']
                    with sqlite3.connect(a.db) as db:
                        assert db.execute('SELECT name,private_description FROM sakura_disks').fetchone() == (name,'draft stays across locale')
                        db.execute('ALTER TABLE sakura_disks RENAME COLUMN private_description TO description')
                    page.unroute(a.origin+BASE, ack_then_read_fault)
                    view.get_by_role('button', name='Retry read', exact=True).click()
                    # A valid forwarded Create201 supplied this ID. Retry must
                    # read the actual saved detail, never repeat Create.
                    expect(view.get_by_role('heading', name='Disk information', exact=True)).to_be_visible()
                    expect(view.get_by_text(name, exact=True)).to_be_visible()
                    assert writes == ['POST']
                    disk_id = created[0]
                    assert 'Availability' not in a.call('GET',BASE+'/'+disk_id)['Disk']
                    view.get_by_role('button',name='Edit',exact=True).click()
                    view.get_by_label('Name',exact=True).fill('edited-'+str(width))
                    view.get_by_label('Description',exact=True).fill('saved metadata')
                    view.get_by_role('button',name='Save metadata',exact=True).click()
                    expect(view.get_by_text('edited-'+str(width),exact=True)).to_be_visible()
                    assert a.call('GET',BASE+'/'+disk_id)['Disk']['Description']=='saved metadata'
                    assert writes == ['POST','PUT'] and frame(b.db)==peer
                    # Hold a real old GET result across navigation and a fresh
                    # native update; its eventual body must not overwrite the
                    # newer view or loading state. No fake metadata is supplied.
                    page.evaluate("""()=>{window.__sakuraFetch=window.fetch;window.__sakuraOldConsumed=false;window.fetch=async(...args)=>{const r=await window.__sakuraFetch(...args);if(r.headers.get('X-Sakura-Old-Test')==='yes'){const original=r.json.bind(r);r.json=async()=>{try{return await original()}finally{requestAnimationFrame(()=>requestAnimationFrame(()=>window.__sakuraOldConsumed=true))}}}return r}}""")
                    held = []
                    def hold_old(route):
                        if route.request.method=='GET' and not held:
                            held.append((route,route.fetch()))
                        else: route.continue_()
                    page.route(a.origin+BASE+'/'+disk_id,hold_old)
                    view.get_by_role('button',name='Refresh metadata',exact=True).click()
                    page.wait_for_function('()=>document.querySelector(".sakura-disks")?.getAttribute("aria-busy")==="true"')
                    assert len(held)==1
                    view.get_by_role('button',name='Back to Disks',exact=True).click()
                    expect(view.get_by_role('button',name='edited-'+str(width),exact=True)).to_be_visible()
                    a.call('PUT',BASE+'/'+disk_id,{'Disk':{'Name':'newer-'+str(width)}})
                    view.get_by_role('button',name='edited-'+str(width),exact=True).click()
                    expect(view.get_by_text('newer-'+str(width),exact=True)).to_be_visible()
                    route,response=held[0]
                    route.fulfill(response=response,headers={**response.headers,'X-Sakura-Old-Test':'yes'})
                    page.wait_for_function('()=>window.__sakuraOldConsumed===true')
                    expect(view.get_by_text('newer-'+str(width),exact=True)).to_be_visible()
                    expect(view.get_by_text('edited-'+str(width),exact=True)).to_have_count(0)
                    expect(view).to_have_attribute('aria-busy','false')
                    stale.append({'width':width,'actualOldBodyConsumed':True,'newerNativeMetadataRetained':True})
                    page.unroute(a.origin+BASE+'/'+disk_id,hold_old)
                    page.evaluate('window.fetch=window.__sakuraFetch')
                    assert_reflow(page)
                    violations=page.evaluate("""async()=>{const r=await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}});return r.violations.filter(v=>v.impact==='serious'||v.impact==='critical').map(v=>({id:v.id,impact:v.impact}))}""")
                    assert violations==[], violations
                    audits.append({'width':width,'seriousCritical':0})
                    page.screenshot(path=str(REPORTS/('sakura-disks-'+str(width)+'.png')),full_page=True,animations='disabled')
                    view.get_by_role('button',name='Delete',exact=True).click()
                    view.get_by_label(re.compile('^Type Disk ID to delete')).fill(disk_id)
                    view.get_by_role('button',name='Cancel',exact=True).click()
                    assert a.call('GET',BASE+'/'+disk_id)['Disk']['ID']==disk_id
                    view.get_by_role('button',name='Delete',exact=True).click()
                    view.get_by_label(re.compile('^Type Disk ID to delete')).fill(disk_id)
                    view.get_by_role('button',name='Delete configuration',exact=True).click()
                    expect(view.get_by_text('No Disk configurations',exact=True)).to_be_visible()
                    assert a.call('GET',BASE)['Disks']==[] and frame(b.db)==peer
                    # Authenticated different principal must receive native403;
                    # neither raw local marker nor an operator role grants owner.
                    context.clear_cookies()
                    with page.expect_response(lambda response: response.request.method=='GET' and response.url.startswith(a.origin+BASE) and response.status==403) as denied_read:
                        login(denied)
                    assert denied_read.value.status==403
                    expect(view.get_by_role('alert')).to_be_visible()
                    assert a.call('GET',BASE)['Disks']==[] and frame(b.db)==peer
                    context.close();contexts.remove(context)
                assert not errors and not outside
                b.call('DELETE',BASE+'/'+peer_id)
                REPORTS.joinpath('sakura-disks-console.json').write_text(json.dumps({'success':True,'actualNativeHTTP':True,'SDK':False,'desktop390':audits,'localeDraftPreserved':True,'readAfterAckReadonlyRetry':True,'olderRealBodyRejected':stale,'peerFrame':True,'outside':0,'pageerrors':0,'noCloudConsoleAssets':True},indent=2)+'\n')
        finally:
            # Attempt every cleanup even if an earlier resource close fails.
            with ExitStack() as cleanup:
                if a:cleanup.callback(a.close)
                if b:cleanup.callback(b.close)
                if browser:cleanup.callback(browser.close)
                for context in reversed(contexts):cleanup.callback(context.close)


if __name__=='__main__':run()
