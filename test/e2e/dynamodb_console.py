"""Prepared real DynamoDB console/SDK acceptance; no mocked API or cloud calls."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import time
import traceback
import urllib.error
import urllib.request
from urllib.parse import urlsplit
from playwright.sync_api import expect, sync_playwright
from reflow import assert_reflow
from sqs import Team, NoRedirect
from dynamodb import client, control, COUNTS, SEEN

REPORTS=Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve()
OPS={'CreateTable','ListTables','DescribeTable','DeleteTable','PutItem','GetItem','DeleteItem'}
PRIVATE=[]


def wire(value):
    if isinstance(value,bytes):return base64.b64encode(value).decode()
    if isinstance(value,dict):return {k:wire(v) for k,v in value.items()}
    if isinstance(value,list):return [wire(v) for v in value]
    return value


def run():
    try:_run()
    except Exception as failure:
        text=str(failure)
        for secret in PRIVATE:
            if secret:text=text.replace(secret,'[redacted]')
        locations=' > '.join(Path(frame.filename).name+':'+str(frame.lineno) for frame in traceback.extract_tb(failure.__traceback__))
        raise RuntimeError(type(failure).__name__+': '+text+' ['+locations+']') from None
    finally:PRIVATE.clear()


def _run():
    REPORTS.mkdir(parents=True,exist_ok=True)
    axe_path=Path(os.environ['AXE_SCRIPT']);axe=axe_path.read_text()
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest()==json.loads((axe_path.parent/'provenance.json').read_text())['bundleSha256']
    audits,flows,errors,outside=[],[],[],[]
    with tempfile.TemporaryDirectory(prefix='ddb-console-') as directory:
        a,b=Team(Path(directory)/'a'),Team(Path(directory)/'b');a.directory.mkdir();b.directory.mkdir()
        try:
            a.start();b.start();PRIVATE.extend([a.token,b.token]);own,peer=client(a),client(b)
            # Host token authentication precedes the provider JSON protocol.
            foreign=urllib.request.Request(b.endpoint+'/',data=b'{"Limit":1}',headers={'X-Simulator-Token':a.token,'Content-Type':'application/x-amz-json-1.0','X-Amz-Target':'DynamoDB_20120810.ListTables'},method='POST')
            try:urllib.request.build_opener(NoRedirect()).open(foreign,timeout=5)
            except urllib.error.HTTPError as failure:assert failure.code==401
            else:raise AssertionError('Foreign team token accepted')
            reader_policy={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['dynamodb:ListTables','dynamodb:DescribeTable','dynamodb:GetItem'],'Resource':'*'}]}
            control(a,'/_simulator/v1/iam/principals/ddb-console-reader/policy',reader_policy,method='PUT')
            participant=control(a,'/_simulator/v1/access',{'Principal':'ddb-console-reader'})['AccessToken'];PRIVATE.append(participant)
            with sync_playwright() as playwright:
                browser=playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
                contexts=[]
                try:
                    for width,kind,sort in [(1440,'S',False),(390,'S',True),(1440,'N',True),(390,'B',True)]:
                        pk='constructor' if kind=='N' else '__proto__' if kind=='B' else 'id'
                        name='console-ddb-'+kind.lower()+'-'+str(width);value={'S':'learner-日本語','N':'2.0','B':'AQI='}[kind]
                        key={pk:{kind:base64.b64decode(value) if kind=='B' else value},**({'sort':{'S':'entry'}} if sort else {})}
                        raw={**wire(key),'payload':{'S':'dummy JSON日本語 GetItem'},'flags':{'M':{'valid':{'BOOL':True},'nothing':{'NULL':True}}}}
                        schema={'TableName':name,'KeySchema':[{'AttributeName':pk,'KeyType':'HASH'}]+([{'AttributeName':'sort','KeyType':'RANGE'}] if sort else []),'AttributeDefinitions':[{'AttributeName':pk,'AttributeType':kind}]+([{'AttributeName':'sort','AttributeType':'S'}] if sort else []),'BillingMode':'PAY_PER_REQUEST'}
                        peer.create_table(**schema);peer.put_item(TableName=name,Item={**key,'payload':{'S':'peer must survive'}})
                        context=browser.new_context(viewport={'width':width,'height':1000});contexts.append(context);page=context.new_page();page.set_default_timeout(10000)
                        # Observe real AbortSignals without replacing responses,
                        # touching DOM state, or changing fetch's returned promise.
                        page.add_init_script('''(() => {const original=window.fetch.bind(window),ids=new WeakMap();let serial=0;window.__ddbSignalObservations=[];window.fetch=(url,options={})=>{const target=new Headers(options.headers||{}).get('X-Amz-Target')||'';if(target.startsWith('DynamoDB_20120810.')&&options.signal){if(!ids.has(options.signal))ids.set(options.signal,++serial);const item={operation:target.slice('DynamoDB_20120810.'.length),signal:ids.get(options.signal),aborted:options.signal.aborted};window.__ddbSignalObservations.push(item);options.signal.addEventListener('abort',()=>{item.aborted=true},{once:true})}return original(url,options)}})();''')

                        calls,responses,failed=[],[],[];intercept=[None];phase=['startup'];request_records={}
                        def operation(request):return request.headers.get('x-amz-target','').removeprefix('DynamoDB_20120810.')
                        def guard(route):
                            target=urlsplit(route.request.url)
                            if target.scheme!='http' or target.netloc!=urlsplit(a.endpoint).netloc:outside.append(True);route.abort();return
                            if intercept[0] is not None and intercept[0](route):return
                            route.continue_()
                        context.route('**/*',guard);page.on('pageerror',lambda _:errors.append(True))
                        def begun(request):
                            if operation(request) in OPS:
                                calls.append(operation(request));request_records[request]={'operation':operation(request),'phase':phase[0],'status':None,'failure':None}
                        def answered(response):
                            if operation(response.request) in OPS:
                                responses.append((operation(response.request),response.status));request_records[response.request]['status']=response.status
                        def unsuccessful(request):
                            if operation(request) in OPS:
                                failed.append(operation(request));request_records[request]['failure']=request.failure;request_records[request]['failurePhase']=phase[0]
                        page.on('request',begun);page.on('response',answered);page.on('requestfailed',unsuccessful)
                        def login(target,token):
                            target.goto(a.endpoint+'/_simulator/console/');target.get_by_label('Access token',exact=True).fill(token);target.get_by_role('button',name='Open sandbox',exact=True).click()
                            target.wait_for_url(re.compile(r'/_simulator/console/(?:#.*)?$'));expect(target.get_by_role('button',name='Services',exact=True)).to_be_enabled()
                            target.get_by_role('button',name='Services',exact=True).click();target.get_by_role('menuitem',name='DynamoDB',exact=True).click()
                        def audit(label,target=None):
                            observed=target or page
                            assert_reflow(observed)
                            for token in PRIVATE:
                                assert token not in observed.content() and token not in observed.url
                                assert not observed.evaluate('(v)=>Object.values(localStorage).some(x=>x.includes(v))||Object.values(sessionStorage).some(x=>x.includes(v))',token)
                            observed.evaluate(axe);result=observed.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})");assert not result['violations'],'DynamoDB axe violation'
                            observed.screenshot(path=str(REPORTS/(label+'-'+kind+'-'+str(width)+'.png')),full_page=False,animations='disabled');audits.append({'state':label,'width':width,'key':kind,'violations':0})
                        def native(button,op,scope=None,status=200,enter=None):
                            with page.expect_response(lambda response:operation(response.request)==op) as result:
                                if enter is not None:enter.press('Enter')
                                else:(scope or page).get_by_role('button',name=button,exact=True).click()
                            assert result.value.status==status
                            return result.value.json()
                        def locale(target):
                            page.get_by_role('combobox',name='Language' if target=='ja' else '言語',exact=True).select_option(target);expect(page.locator('html')).to_have_attribute('lang',target)
                        def keys():
                            page.get_by_label(pk+' ('+kind+')',exact=True).fill(value)
                            if sort:page.get_by_label('sort (S)',exact=True).fill('entry')
                        sentinel='cancel-sentinel-'+kind.lower()+'-'+str(width)
                        own.create_table(TableName=sentinel,KeySchema=[{'AttributeName':'id','KeyType':'HASH'}],AttributeDefinitions=[{'AttributeName':'id','AttributeType':'S'}],BillingMode='PAY_PER_REQUEST')
                        login(page,a.token);expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled()
                        page.evaluate('()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))')
                        expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled()
                        expect(page.get_by_text('DynamoDB request failed',exact=True)).to_have_count(0)

                        # Language and provider-scoped service search are presentation only.
                        before_search=list(calls);page.get_by_role('button',name='Services',exact=True).click()
                        page.get_by_role('searchbox',name='Search services in AWS',exact=True).fill('dynamo')
                        locale('ja');expect(page.get_by_role('searchbox',name='サービスを検索: AWS',exact=True)).to_have_value('dynamo')
                        page.get_by_role('searchbox',name='サービスを検索: AWS',exact=True).fill('ダイナモDB')
                        expect(page.get_by_role('menuitem',name='DynamoDB',exact=True)).to_be_visible();expect(page.get_by_role('menuitem')).to_have_count(1)
                        page.get_by_role('searchbox',name='サービスを検索: AWS',exact=True).press('Escape');expect(page.get_by_role('button',name='サービス',exact=True)).to_be_focused()
                        locale('en');assert calls==before_search,'Language/search changed native request authority or data';audit('language-service-search')
                        # Cancel while a real list request is pending: no stuck loading or write.
                        held=[]
                        def hold_list(route):
                            if operation(route.request)=='ListTables':held.append(route);return True
                            return False
                        phase[0]='held-list-cancel';intercept[0]=hold_list;page.get_by_role('button',name='Refresh tables',exact=True).click()
                        deadline=time.monotonic()+5
                        while not held:assert time.monotonic()<deadline;page.wait_for_timeout(10)
                        page.get_by_role('button',name='Create table',exact=True).click();page.get_by_role('button',name='Cancel',exact=True).click()
                        with page.expect_response(lambda response:operation(response.request)=='ListTables') as released:held.pop().continue_()
                        assert released.value.status==200;phase[0]='post-cancel';intercept[0]=None;expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled();assert calls.count('CreateTable')==0
                        expect(page.get_by_role('link',name=sentinel,exact=True)).to_have_count(0)
                        native('Refresh tables','ListTables');expect(page.get_by_role('link',name=sentinel,exact=True)).to_be_visible()
                        own.delete_table(TableName=sentinel);native('Refresh tables','ListTables')
                        expect(page.get_by_role('link',name=sentinel,exact=True)).to_have_count(0)
                        expect(page.get_by_text('DynamoDB request failed',exact=True)).to_have_count(0)

                        page.get_by_role('button',name='Create table',exact=True).click();page.get_by_label('Table name',exact=True).fill(name);page.get_by_label('Partition key',exact=True).fill(pk)
                        if kind!='S':page.get_by_label('Partition key type',exact=True).click();page.get_by_role('option',name='Number (N)' if kind=='N' else 'Binary (B)',exact=True).click()
                        if sort:page.get_by_role('checkbox',name='Sort key (optional)',exact=True).check()
                        before=list(calls);locale('ja');expect(page.get_by_label('テーブル名',exact=True)).to_have_value(name);assert calls==before;locale('en')
                        phase[0]='normal-lifecycle';audit('create-table-draft');native('Create table','CreateTable',enter=page.get_by_label('Table name',exact=True));expect(page.get_by_role('heading',name=name,exact=True)).to_be_visible()
                        meta=own.describe_table(TableName=name)['Table'];assert meta['KeySchema']==schema['KeySchema'];assert meta['BillingModeSummary']['BillingMode']=='PAY_PER_REQUEST'
                        page.get_by_role('tab',name='Explore table items',exact=True).click();page.get_by_role('button',name='Create item',exact=True).click();page.get_by_label('Item JSON',exact=True).fill(json.dumps(raw,ensure_ascii=False))
                        before=list(calls);locale('ja');expect(page.get_by_label('項目 JSON',exact=True)).to_have_value(json.dumps(raw,ensure_ascii=False));assert calls==before;locale('en');audit('item-draft')
                        fixture={'ack':False,'abort':0}
                        def acknowledged(response):
                            if operation(response.request)=='PutItem' and response.status==200:fixture['ack']=True
                        def fail_read(route):
                            if operation(route.request)=='DescribeTable' and fixture['ack']:fixture['ack']=False;fixture['abort']+=1;route.abort('failed');return True
                            return False
                        intercept[0]=fail_read;page.on('response',acknowledged);native('Create item','PutItem')
                        expect(page.get_by_role('button',name='Retry read',exact=True)).to_be_visible();expect(page.get_by_label('Item JSON',exact=True)).to_have_count(0)
                        assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw;assert fixture['abort']==1;native('Retry read','DescribeTable')
                        expect(page.get_by_role('button',name='Refresh table',exact=True)).to_be_enabled();assert calls.count('PutItem')==1
                        intercept[0]=None;page.remove_listener('response',acknowledged)
                        page.get_by_role('tab',name='Explore table items',exact=True).click();page.get_by_role('button',name='Create item',exact=True).click();page.get_by_label('Item JSON',exact=True).fill(json.dumps({**raw,'payload':{'S':'must not replace'}}));conditional=native('Create item','PutItem',status=400);assert conditional['__type'].endswith('#ConditionalCheckFailedException')
                        assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw;assert own.describe_table(TableName=name)['Table']['ItemCount']==1;page.get_by_role('button',name='Cancel',exact=True).click()
                        # Raw duplicate Item members must reach the strict native decoder unchanged.
                        page.get_by_role('button',name='Create item',exact=True).click();duplicate=json.dumps(raw)[:-1]+',"payload":{"S":"duplicate must fail"}}';page.get_by_label('Item JSON',exact=True).fill(duplicate);duplicate_error=native('Create item','PutItem',status=400);assert duplicate_error['__type'].endswith('#ValidationException')
                        assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw;page.get_by_role('button',name='Cancel',exact=True).click()
                        keys();native('Get item','GetItem');expect(page.get_by_label('Returned item JSON',exact=True)).not_to_have_value('');assert json.loads(page.get_by_label('Returned item JSON',exact=True).input_value())==wire(own.get_item(TableName=name,Key=key)['Item']);audit('item-returned')
                        # Real region and principal transitions must discard displayed
                        # items and dirty drafts without discarding native stored data.
                        page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='us-west-2',exact=True).click()
                        expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled();expect(page.get_by_role('link',name=name,exact=True)).to_have_count(0);expect(page.get_by_label('Returned item JSON',exact=True)).to_have_count(0)
                        west=client(a,region='us-west-2');assert name not in west.list_tables()['TableNames'];assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw
                        page.get_by_role('button',name='us-west-2',exact=True).click();page.get_by_role('menuitem',name='us-east-1',exact=True).click();expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled()
                        page.get_by_role('link',name=name,exact=True).click();page.get_by_role('tab',name='Explore table items',exact=True).click();expect(page.get_by_label(pk+' ('+kind+')',exact=True)).to_have_value('');keys();native('Get item','GetItem')
                        page.get_by_role('button',name='Create item',exact=True).click();page.get_by_label('Item JSON',exact=True).fill(json.dumps({**raw,'payload':{'S':'must reset after principal change'}}))
                        before_scope_writes=calls.count('PutItem')
                        def identity(principal):
                            page.get_by_role('button',name='Account menu',exact=True).click();page.get_by_role('menuitem',name='Choose identity',exact=True).click()
                            dialog=page.get_by_role('dialog');dialog.get_by_label('Principal name',exact=True).fill(principal);dialog.get_by_role('button',name='Choose identity',exact=True).click()
                            expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled();expect(page.get_by_label('Item JSON',exact=True)).to_have_count(0);expect(page.get_by_label('Returned item JSON',exact=True)).to_have_count(0)
                        identity('ddb-console-reader');page.get_by_role('link',name=name,exact=True).click();page.get_by_role('tab',name='Explore table items',exact=True).click();expect(page.get_by_label(pk+' ('+kind+')',exact=True)).to_have_value('');keys();native('Get item','GetItem');assert json.loads(page.get_by_label('Returned item JSON',exact=True).input_value())==raw
                        identity('simulator-admin');page.get_by_role('link',name=name,exact=True).click();page.get_by_role('tab',name='Explore table items',exact=True).click();expect(page.get_by_label(pk+' ('+kind+')',exact=True)).to_have_value('');keys();native('Get item','GetItem');assert calls.count('PutItem')==before_scope_writes;assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw;audit('scope-restored')
                        page.get_by_label(pk+' ('+kind+')',exact=True).fill('different' if kind=='S' else '3' if kind=='N' else 'Aw==');expect(page.get_by_label('Returned item JSON',exact=True)).to_have_count(0);expect(page.get_by_role('button',name='Delete item',exact=True)).to_have_count(0)
                        keys();native('Get item','GetItem');page.get_by_role('button',name='Delete item',exact=True).click();expect(page.get_by_role('dialog')).to_be_visible();page.keyboard.press('Escape');expect(page.get_by_role('dialog')).to_have_count(0);expect(page.get_by_role('button',name='Delete item',exact=True)).to_be_focused();assert calls.count('DeleteItem')==0
                        page.get_by_role('button',name='Delete item',exact=True).click();page.get_by_label('Type delete to confirm',exact=True).fill('delete');native('Delete','DeleteItem',page.get_by_role('dialog'))
                        assert own.get_item(TableName=name,Key=key).get('Item') is None;own.delete_item(TableName=name,Key=key);assert own.get_item(TableName=name,Key=key).get('Item') is None;assert own.describe_table(TableName=name)['Table']['ItemCount']==0
                        page.get_by_role('tab',name='Explore table items',exact=True).click();keys();native('Get item','GetItem');expect(page.get_by_role('heading',name='Item not found',exact=True)).to_be_visible();audit('item-not-found')
                        # Participant can read but cannot put; SDK confirms no new data.
                        limited=browser.new_context(viewport={'width':width,'height':1000});contexts.append(limited);lp=limited.new_page();lp.set_default_timeout(10000);lp.on('pageerror',lambda _:errors.append(True))
                        def limited_guard(route):
                            target=urlsplit(route.request.url)
                            if target.scheme=='http' and target.netloc==urlsplit(a.endpoint).netloc:route.continue_()
                            else:outside.append(True);route.abort()
                        limited.route('**/*',limited_guard)
                        login(lp,participant);lp.get_by_role('link',name=name,exact=True).click();lp.get_by_role('tab',name='Explore table items',exact=True).click();lp.get_by_role('button',name='Create item',exact=True).click();lp.get_by_label('Item JSON',exact=True).fill(json.dumps(raw))
                        with lp.expect_response(lambda response:response.request.headers.get('x-amz-target')=='DynamoDB_20120810.PutItem') as denied:lp.get_by_role('button',name='Create item',exact=True).click()
                        assert denied.value.status==403;expect(lp.get_by_text('DynamoDB request failed',exact=True)).to_be_visible();audit('reader403',lp);assert own.get_item(TableName=name,Key=key).get('Item') is None;limited.close();contexts.remove(limited)
                        page.get_by_role('button',name='Delete table',exact=True).click();page.get_by_role('dialog').get_by_role('button',name='Cancel',exact=True).click();assert own.describe_table(TableName=name)['Table']['TableId']==meta['TableId']
                        page.get_by_role('button',name='Delete table',exact=True).click();page.get_by_label('Confirm table name',exact=True).fill(name);native('Delete','DeleteTable',page.get_by_role('dialog'),enter=page.get_by_label('Confirm table name',exact=True));expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled()
                        assert name not in own.list_tables()['TableNames'];assert peer.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'peer must survive'}
                        (REPORTS/('transport-'+kind+'-'+str(width)+'.json')).write_text(json.dumps({'calls':calls,'responses':responses,'failed':failed,'pageErrors':len(errors),'outside':len(outside),'requestRecords':list(request_records.values())},indent=2)+'\n')
                        assert set(calls)==OPS
                        observed=page.evaluate('window.__ddbSignalObservations')
                        startup=[r for r in request_records.values() if r['failure'] and r['operation']=='ListTables' and r['phase']=='startup' and r['failurePhase']=='startup' and r['status']==200 and r['failure']=='net::ERR_ABORTED']
                        intentional=[r for r in request_records.values() if r['failure'] and r['operation']=='DescribeTable' and r['status'] is None and r['failure']=='net::ERR_FAILED' and r['phase']=='normal-lifecycle']
                        assert len(startup)<=1 and len(intentional)==1
                        assert len(failed)==len(startup)+len(intentional), 'Unexpected native network failure'
                        if startup:
                            lists=[r for r in observed if r['operation']=='ListTables']
                            assert lists[0]['aborted'] and not lists[-1]['aborted'] and lists[0]['signal']!=lists[-1]['signal'], 'Startup abort did not belong to a replaced view controller'
                        assert len(request_records)==len(calls) and len(responses)+1==len(calls)
                        assert calls.count('CreateTable')==1 and calls.count('DeleteTable')==1 and calls.count('DeleteItem')==1 and calls.count('PutItem')==3
                        assert all(status==200 or op=='PutItem' and status==400 for op,status in responses);assert not errors and not outside
                        flows.append({'width':width,'keyType':kind,'sortKey':sort,'partitionKeyName':pk,'operations':sorted(set(calls)),'postACKReadAbort':1,'readRetryDidNotRepeatPut':True,'conditionalAndDuplicateRefused':True,'cancelNoMutation':True,'reader403NoMutation':True,'keyChangeClearsOldResult':True,'keyboardEnterAndEscapeFocus':True,'regionAndPrincipalReset':True,'localizedServiceSearchNoNativeCalls':True,'expectedOldViewStartupAbort':len(startup),'signalObservations':observed})
                        context.close();contexts.remove(context);peer.delete_table(TableName=name)
                    assert own.list_tables()['TableNames']==[] and peer.list_tables()['TableNames']==[];assert SEEN==OPS;assert COUNTS['calls']>0 and COUNTS['calls']==COUNTS['sends']==COUNTS['responses']
                    report={'actualEmbeddedUI':True,'twoCLIsDBTokens':True,'flows':flows,'SDKCounts':COUNTS,'axeStates':audits,'foreignToken401':True,'peerPreserved':True,'outsideRequests':0,'pageErrors':0,'cleanup':'DELETED','actualAWS':False};(REPORTS/'dynamodb-console.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
                finally:
                    for context in contexts:context.close()
                    browser.close()
        finally:
            try:a.stop()
            finally:b.stop()


if __name__=='__main__':run()
