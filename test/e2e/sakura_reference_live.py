"""Actual served Sakura reference examples over owned native HTTP, not an SDK.

An explicit root checkout and binary are mandatory. Static response examples are
never treated as saved state. No credentials, generated tokens or raw rows print.
"""
import argparse
from contextlib import ExitStack
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import urllib.request


from metrics_openapi import preserved_metrics_reference

def run(root, binary, reports, browser=False):
    sys.path.insert(0, str(root/'test/e2e'))
    from sakura_cli import CLI, BASE, BASIC, create, frame
    reports.mkdir(parents=True, exist_ok=True)
    statuses, executed = [], []
    owned = []
    with tempfile.TemporaryDirectory(prefix='sakura-reference-') as temporary, ExitStack() as cleanup:
        a = CLI(str(binary), Path(temporary)/'a', 'reference-A'); cleanup.callback(a.close); owned.append(a)
        b = CLI(str(binary), Path(temporary)/'b', 'reference-B'); cleanup.callback(b.close); owned.append(b)
        disabled = CLI(str(binary), Path(temporary)/'off', 'reference-disabled', False); cleanup.callback(disabled.close); owned.append(disabled)
        participant = a.call('POST','/_simulator/v1/access',{'Principal':'learner'})['AccessToken']
        denied = a.call('POST','/_simulator/v1/access',{'Principal':'other'})['AccessToken']
        req = urllib.request.Request(a.origin+'/_simulator/docs/openapi.json',headers={'X-Simulator-Token':participant})
        a.calls += 1
        with a.opener.open(req,timeout=5) as response:
            a.responses += 1
            assert response.status==200
            raw=response.read(1048577); assert len(raw)<=1048576
            spec=json.loads(raw)
        assert spec==json.loads((root/'internal/server/apidocs/openapi.json').read_text()), 'served candidate spec differs'
        operations={op['operationId']:(path,method,op) for path,item in spec['paths'].items() for method,op in item.items() if isinstance(op,dict) and 'operationId' in op}
        preserved_metrics_reference(spec)
        assert len(operations)==66
        names={'SakuraListDisks','SakuraCreateDisk','SakuraGetDisk','SakuraUpdateDisk','SakuraDeleteDisk'}
        assert names<=set(operations)
        templates={name:copy.deepcopy(operations[name][2].get('requestBody',{}).get('content',{}).get('application/json',{}).get('examples',{})) for name in names}
        def call(name, disk_id=None, body=None, status=200):
            path,method,op=operations[name]
            path=path.replace('{zone}','is1a')
            if '{diskId}' in path:
                assert disk_id and disk_id.isascii() and disk_id.isdecimal()
                path=path.replace('{diskId}',disk_id)
            result=a.call(method.upper(),path,body,status,token=participant,principal='')
            assert str(status) in op['responses'], 'actual status must be declared by served operation'
            assert isinstance(result, dict) and result.get('is_ok') is True
            # Explicit native result/state assertions below, not a substitute
            # implementation or claim of unavailable full OpenAPI validation.
            executed.append(name);statuses.append(status)
            return result
        peer_id=create(b,'peer-only');peer_frame=frame(b.db)
        seed=create(a,'preexisting-live-state')
        empty_static=operations['SakuraListDisks'][2]['responses']['200']['content']['application/json']['examples']['Empty']['value']
        assert empty_static['Total']==0
        body=templates['SakuraCreateDisk']['StaticConfiguration']['value']
        created=call('SakuraCreateDisk',body=body,status=201)['Disk'];disk_id=created['ID']
        static_id=operations['SakuraCreateDisk'][2]['responses']['201']['content']['application/json']['examples']['StaticResult']['value']['Disk']['ID']
        assert disk_id!=seed and disk_id!=static_id, 'live identity must come from current create, not static sample'
        assert created['Name']==body['Disk']['Name'] and created['Description']==body['Disk']['Description']
        assert 'Availability' not in created
        listed=call('SakuraListDisks');assert listed['Total']==2 and {d['ID'] for d in listed['Disks']}=={seed,disk_id}
        suffix=operations['SakuraListDisks'][2]['x-sakura-raw-json-query']['encodedSuffix']
        paged=a.call('GET',BASE+suffix,token=participant,principal='')
        assert paged['Count']==1 and paged['Total']==2 and len(paged['Disks'])==1
        read=call('SakuraGetDisk',disk_id)['Disk'];assert read==created
        update=templates['SakuraUpdateDisk']['StaticConfiguration']['value']
        changed=call('SakuraUpdateDisk',disk_id,update)['Disk']
        assert changed['ID']==disk_id and changed['Name']==update['Disk']['Name'] and changed['Description']==''
        assert call('SakuraGetDisk',disk_id)['Disk']==changed
        # The second served Create example also exercises actual neutral SDK wire
        # carriers, without importing or claiming the unavailable community SDK.
        neutral=call('SakuraCreateDisk',body=templates['SakuraCreateDisk']['CommunitySDKNeutralDefaults']['value'],status=201)['Disk']['ID']
        assert neutral not in {seed,disk_id}
        before=frame(a.db)
        for credential in ['',b.token]:a.call('GET',BASE,status=401,token=credential,principal='')
        a.call('GET',BASE,status=403,principal='')
        a.call('GET',BASE,status=403,token=denied,principal='')
        a.call('GET',BASE,status=403,token=participant,principal='other')
        a.call('GET',BASE.replace('/is1a/','/tk1a/'),status=404,token=participant,principal='')
        a.call('GET',BASE+'/999999999',status=404,token=participant,principal='')
        a.call('PUT',BASE+'/'+disk_id,{'Disk':{'Description':None}},status=400,token=participant,principal='')
        disabled.call('GET',BASE,status=404)
        assert frame(a.db)==before and frame(b.db)==peer_frame
        scalar={'executed':False}
        if browser:
            from playwright.sync_api import sync_playwright,expect
            import shutil
            with sync_playwright() as p:
                view=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
                try:
                    context=view.new_context(viewport={'width':1440,'height':1000})
                    try:
                        assert context.request.post(a.origin+'/_simulator/login',form={'token':participant},max_redirects=0).status==303
                        page=context.new_page();outside=[];errors=[]
                        page.on('pageerror',lambda error:errors.append(str(error)))
                        def guard(route):
                            if route.request.url.startswith(a.origin+'/'):route.continue_()
                            else:outside.append(True);route.abort()
                        context.route('**/*',guard)
                        page.goto(a.origin+'/_simulator/docs/')
                        page.get_by_role('button',name='Open Group - Sakura Cloud',exact=True).click()
                        page.get_by_role('link',name='Sakura Disk configuration',exact=True).click()
                        expect(page.get_by_text('List Disk configurations',exact=True).first).to_be_visible(timeout=30000)
                        path=operations['SakuraListDisks'][0]
                        page.get_by_role('button',name='Test Request (get '+path+')',exact=True).click()
                        expect(page.get_by_role('button',name=re.compile(r'^Send get request'))).to_be_visible()
                        page.get_by_role('button',name=re.compile(r'^Authentication\s*Required')).click()
                        # Capture only accessible control names to diagnose real
                        # Scalar authentication inputs; never dump token values.
                        labels=page.get_by_role('textbox').evaluate_all('(els)=>els.map(e=>({placeholder:e.placeholder,aria:e.getAttribute("aria-label"),type:e.type}))')
                        (reports/'scalar-control-shapes.json').write_text(json.dumps({'textboxes':labels,'authNodes':page.locator('*').evaluate_all('(els)=>els.filter(e=>(e.textContent||"").startsWith("Authentication")&&(e.textContent||"").length<100).map(e=>({tag:e.tagName,role:e.getAttribute("role"),text:e.textContent,cls:e.className}))'),'buttons':page.get_by_role('button').all_text_contents(),'inputs':page.locator('input').evaluate_all('(els)=>els.map(e=>({placeholder:e.placeholder,aria:e.getAttribute("aria-label"),type:e.type}))')},indent=2)+'\n')
                        username = page.get_by_text('Username:',exact=True).locator('..').locator('[contenteditable="true"]')
                        expect(username).to_have_count(1)
                        username.fill('TCSIMSAKURA')
                        page.get_by_placeholder('********',exact=True).fill('dummy-local-only')
                        with page.expect_response(lambda r:r.request.method=='GET' and r.url==a.origin+BASE) as sent:
                            page.get_by_role('button').filter(has_text='Send Request').last.click()
                        assert sent.value.status==200
                        actual=sent.value.json()
                        assert actual['Total']==3 and {d['ID'] for d in actual['Disks']}=={seed,disk_id,neutral}
                        assert actual==a.call('GET',BASE,token=participant,principal='')
                        assert frame(a.db)==before and frame(b.db)==peer_frame
                        page.screenshot(path=str(reports/'sakura-reference-live-desktop.png'),full_page=False)
                        page.set_viewport_size({'width':390,'height':844})
                        page.screenshot(path=str(reports/'sakura-reference-live-mobile.png'),full_page=False)
                        scalar={'executed':True,'clientOpened':True,'nativeStatus':200,'liveRows':3,
                            'participantCookieActor':True,'publicDummyBasicOnly':True,'sharedNativeState':True,
                            'outside':0,'pageErrors':0,'screens':[1440,390]}
                        assert not outside and not errors
                    finally:context.close()
                finally:view.close()
        call('SakuraDeleteDisk',disk_id);a.call('GET',BASE+'/'+disk_id,status=404,token=participant,principal='')
        call('SakuraDeleteDisk',neutral);a.call('DELETE',BASE+'/'+seed,token=participant,principal='')
        assert a.call('GET',BASE,token=participant,principal='')['Disks']==[]
        assert frame(b.db)==peer_frame;b.call('DELETE',BASE+'/'+peer_id)
        assert b.call('GET',BASE)['Disks']==[]
        assert set(executed)==names
        counts=[{'calls':c.calls,'responses':c.responses} for c in owned];assert all(c['calls']==c['responses'] and c['calls']>0 for c in counts)
        result={'success':True,'actualNativeHTTP':True,'SDK':False,'fullOpenAPISchemaValidation':False,'servedSpecSHA256':hashlib.sha256(raw).hexdigest(),'operationIDs':sorted(names),'executedExampleCalls':len(executed),'statuses':statuses,'liveIDFromCreatedResponse':True,'staticResponsesNotTreatedAsLive':True,'neutralExampleAlsoExecuted':True,'rawJSONQueryExampleExecuted':True,'twoDBOwnerAndTokenFrame':True,'disabled404':True,'cleanup':True,'HTTP':counts,'scalar':scalar,'binarySHA256':hashlib.sha256(binary.read_bytes()).hexdigest()}
        (reports/'sakura-reference-live.json').write_text(json.dumps(result,indent=2)+'\n')
        print('Served Sakura five-operation native HTTP examples PASS; Scalar outcome separately recorded')

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--root',type=Path,required=True);parser.add_argument('--binary',type=Path,required=True);parser.add_argument('--reports',type=Path,required=True);parser.add_argument('--browser',action='store_true');args=parser.parse_args();run(args.root.resolve(),args.binary.resolve(),args.reports.resolve(),args.browser)
