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
        raise RuntimeError(type(failure).__name__+': '+text) from None
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
                        name='console-ddb-'+kind.lower()+'-'+str(width);value={'S':'learner-日本語','N':'2.0','B':'AQI='}[kind]
                        key={'id':{kind:base64.b64decode(value) if kind=='B' else value},**({'sort':{'S':'entry'}} if sort else {})}
                        raw={**wire(key),'payload':{'S':'dummy JSON日本語 GetItem'},'flags':{'M':{'valid':{'BOOL':True},'nothing':{'NULL':True}}}}
                        schema={'TableName':name,'KeySchema':[{'AttributeName':'id','KeyType':'HASH'}]+([{'AttributeName':'sort','KeyType':'RANGE'}] if sort else []),'AttributeDefinitions':[{'AttributeName':'id','AttributeType':kind}]+([{'AttributeName':'sort','AttributeType':'S'}] if sort else []),'BillingMode':'PAY_PER_REQUEST'}
                        peer.create_table(**schema);peer.put_item(TableName=name,Item={**key,'payload':{'S':'peer must survive'}})
                        context=browser.new_context(viewport={'width':width,'height':1000});contexts.append(context);page=context.new_page();page.set_default_timeout(10000)
                        calls,responses,failed=[],[],[];intercept=[None]
                        def operation(request):return request.headers.get('x-amz-target','').removeprefix('DynamoDB_20120810.')
                        def guard(route):
                            target=urlsplit(route.request.url)
                            if target.scheme!='http' or target.netloc!=urlsplit(a.endpoint).netloc:outside.append(True);route.abort();return
                            if intercept[0] is not None and intercept[0](route):return
                            route.continue_()
                        context.route('**/*',guard);page.on('pageerror',lambda _:errors.append(True))
                        page.on('request',lambda request:calls.append(operation(request)) if operation(request) in OPS else None)
                        page.on('response',lambda response:responses.append((operation(response.request),response.status)) if operation(response.request) in OPS else None)
                        page.on('requestfailed',lambda request:failed.append(operation(request)) if operation(request) in OPS else None)
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
                        def native(button,op,scope=None,status=200):
                            with page.expect_response(lambda response:operation(response.request)==op) as result:(scope or page).get_by_role('button',name=button,exact=True).click()
                            assert result.value.status==status
                            return result.value.json()
                        def locale(target):
                            page.get_by_role('combobox',name='Language' if target=='ja' else '言語',exact=True).select_option(target);expect(page.locator('html')).to_have_attribute('lang',target)
                        def keys():
                            page.get_by_label('id ('+kind+')',exact=True).fill(value)
                            if sort:page.get_by_label('sort (S)',exact=True).fill('entry')
                        login(page,a.token);expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled()
                        # Cancel while a real list request is pending: no stuck loading or write.
                        held=[]
                        def hold_list(route):
                            if operation(route.request)=='ListTables':held.append(route);return True
                            return False
                        intercept[0]=hold_list;page.get_by_role('button',name='Refresh tables',exact=True).click()
                        deadline=time.monotonic()+5
                        while not held:assert time.monotonic()<deadline;page.wait_for_timeout(10)
                        page.get_by_role('button',name='Create table',exact=True).click();page.get_by_role('button',name='Cancel',exact=True).click()
                        with page.expect_response(lambda response:operation(response.request)=='ListTables') as released:held.pop().continue_()
                        assert released.value.status==200;intercept[0]=None;expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled();assert calls.count('CreateTable')==0
                        page.get_by_role('button',name='Create table',exact=True).click();page.get_by_label('Table name',exact=True).fill(name)
                        if kind!='S':page.get_by_label('Partition key type',exact=True).click();page.get_by_role('option',name='Number (N)' if kind=='N' else 'Binary (B)',exact=True).click()
                        if sort:page.get_by_role('checkbox',name='Sort key (optional)',exact=True).check()
                        before=list(calls);locale('ja');expect(page.get_by_label('テーブル名',exact=True)).to_have_value(name);assert calls==before;locale('en')
                        audit('create-table-draft');native('Create table','CreateTable');expect(page.get_by_role('heading',name=name,exact=True)).to_be_visible()
                        meta=own.describe_table(TableName=name)['Table'];assert meta['KeySchema']==schema['KeySchema'];assert meta['BillingModeSummary']['BillingMode']=='PAY_PER_REQUEST'
                        page.get_by_role('tab',name='Explore items',exact=True).click();page.get_by_role('button',name='Create item',exact=True).click();page.get_by_label('Item JSON',exact=True).fill(json.dumps(raw,ensure_ascii=False))
                        before=list(calls);locale('ja');expect(page.get_by_label('項目 JSON',exact=True)).to_have_value(json.dumps(raw,ensure_ascii=False));assert calls==before;locale('en');audit('item-draft')
                        fixture={'ack':False,'abort':0}
                        def acknowledged(response):
                            if operation(response.request)=='PutItem' and response.status==200:fixture['ack']=True
                        def fail_read(route):
                            if operation(route.request)=='DescribeTable' and fixture['ack']:fixture['ack']=False;fixture['abort']+=1;route.abort('failed');return True
                            return False
                        intercept[0]=fail_read;page.on('response',acknowledged);native('Write item','PutItem')
                        expect(page.get_by_role('button',name='Retry read',exact=True)).to_be_visible();expect(page.get_by_label('Item JSON',exact=True)).to_have_count(0)
                        assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw;assert fixture['abort']==1;native('Retry read','DescribeTable')
                        expect(page.get_by_role('button',name='Refresh table',exact=True)).to_be_enabled();assert calls.count('PutItem')==1
                        intercept[0]=None;page.remove_listener('response',acknowledged)
                        page.get_by_role('tab',name='Explore items',exact=True).click();page.get_by_role('button',name='Create item',exact=True).click();page.get_by_label('Item JSON',exact=True).fill(json.dumps({**raw,'payload':{'S':'must not replace'}}));conditional=native('Write item','PutItem',status=400);assert conditional['__type'].endswith('#ConditionalCheckFailedException')
                        assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw;assert own.describe_table(TableName=name)['Table']['ItemCount']==1;page.get_by_role('button',name='Cancel',exact=True).click()
                        # Raw duplicate Item members must reach the strict native decoder unchanged.
                        page.get_by_role('button',name='Create item',exact=True).click();duplicate=json.dumps(raw)[:-1]+',"payload":{"S":"duplicate must fail"}}';page.get_by_label('Item JSON',exact=True).fill(duplicate);duplicate_error=native('Write item','PutItem',status=400);assert duplicate_error['__type'].endswith('#ValidationException')
                        assert wire(own.get_item(TableName=name,Key=key)['Item'])==raw;page.get_by_role('button',name='Cancel',exact=True).click()
                        keys();native('Get item','GetItem');expect(page.get_by_label('Returned item JSON',exact=True)).not_to_have_value('');assert json.loads(page.get_by_label('Returned item JSON',exact=True).input_value())==wire(own.get_item(TableName=name,Key=key)['Item']);audit('item-returned')
                        page.get_by_label('id ('+kind+')',exact=True).fill('different' if kind=='S' else '3' if kind=='N' else 'Aw==');expect(page.get_by_label('Returned item JSON',exact=True)).to_have_count(0);expect(page.get_by_role('button',name='Delete item',exact=True)).to_have_count(0)
                        keys();native('Get item','GetItem');page.get_by_role('button',name='Delete item',exact=True).click();page.get_by_role('dialog').get_by_role('button',name='Cancel',exact=True).click();assert calls.count('DeleteItem')==0
                        page.get_by_role('button',name='Delete item',exact=True).click();page.get_by_label('Type delete to confirm',exact=True).fill('delete');native('Delete','DeleteItem',page.get_by_role('dialog'))
                        assert own.get_item(TableName=name,Key=key).get('Item') is None;own.delete_item(TableName=name,Key=key);assert own.get_item(TableName=name,Key=key).get('Item') is None;assert own.describe_table(TableName=name)['Table']['ItemCount']==0
                        page.get_by_role('tab',name='Explore items',exact=True).click();keys();native('Get item','GetItem');expect(page.get_by_role('heading',name='Item not found',exact=True)).to_be_visible();audit('item-not-found')
                        # Participant can read but cannot put; SDK confirms no new data.
                        limited=browser.new_context(viewport={'width':width,'height':1000});contexts.append(limited);lp=limited.new_page();lp.set_default_timeout(10000);lp.on('pageerror',lambda _:errors.append(True))
                        def limited_guard(route):
                            target=urlsplit(route.request.url)
                            if target.scheme=='http' and target.netloc==urlsplit(a.endpoint).netloc:route.continue_()
                            else:outside.append(True);route.abort()
                        limited.route('**/*',limited_guard)
                        login(lp,participant);lp.get_by_role('link',name=name,exact=True).click();lp.get_by_role('tab',name='Explore items',exact=True).click();lp.get_by_role('button',name='Create item',exact=True).click();lp.get_by_label('Item JSON',exact=True).fill(json.dumps(raw))
                        with lp.expect_response(lambda response:response.request.headers.get('x-amz-target')=='DynamoDB_20120810.PutItem') as denied:lp.get_by_role('button',name='Write item',exact=True).click()
                        assert denied.value.status==403;expect(lp.get_by_text('DynamoDB request failed',exact=True)).to_be_visible();audit('reader403',lp);assert own.get_item(TableName=name,Key=key).get('Item') is None;limited.close();contexts.remove(limited)
                        page.get_by_role('button',name='Delete table',exact=True).click();page.get_by_role('dialog').get_by_role('button',name='Cancel',exact=True).click();assert own.describe_table(TableName=name)['Table']['TableId']==meta['TableId']
                        page.get_by_role('button',name='Delete table',exact=True).click();page.get_by_label('Confirm table name',exact=True).fill(name);native('Delete','DeleteTable',page.get_by_role('dialog'));expect(page.get_by_role('button',name='Refresh tables',exact=True)).to_be_enabled()
                        assert name not in own.list_tables()['TableNames'];assert peer.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'peer must survive'}
                        assert set(calls)==OPS;assert failed==['DescribeTable'];assert len(responses)+1==len(calls);assert calls.count('CreateTable')==1 and calls.count('DeleteTable')==1 and calls.count('DeleteItem')==1 and calls.count('PutItem')==3
                        assert all(status==200 or op=='PutItem' and status==400 for op,status in responses);assert not errors and not outside
                        flows.append({'width':width,'keyType':kind,'sortKey':sort,'operations':sorted(set(calls)),'postACKReadAbort':1,'readRetryDidNotRepeatPut':True,'conditionalAndDuplicateRefused':True,'cancelNoMutation':True,'reader403NoMutation':True,'keyChangeClearsOldResult':True})
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
