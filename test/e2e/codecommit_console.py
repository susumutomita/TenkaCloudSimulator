"""Operate on one real Git history through the packaged Console, Scalar and optional boto3."""
import argparse
import base64
from contextlib import ExitStack
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import time
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright
from autoscaling_plans_console import LocalTeam, started, wire
from reflow import assert_reflow

PREFIX = 'CodeCommit_20150413.'
NAME = 'console-git-history'
FILE = 'src/example.bin'
REGION = 'us-east-1'
BINARY_CONTENT = bytes([0, 255, 10])


def driver(team, stock, stack, observations):
    if not stock:
        return lambda op, **kwargs: wire(team, 'POST', '/', kwargs, PREFIX + op)
    import boto3
    from botocore.config import Config
    client = boto3.client('codecommit', endpoint_url=team.endpoint, region_name=REGION,
                          aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='local-fixture-only',
                          config=Config(connect_timeout=2, read_timeout=5, retries={'max_attempts': 0}, proxies={}))
    stack.callback(client.close)
    client.meta.events.register('before-sign.codecommit',
        lambda request, **kw: request.headers.add_header('X-Simulator-Token', team.token))

    def sent(request, **kw):
        assert request.method == 'POST' and request.url == team.endpoint + '/'
        assert b'/us-east-1/codecommit/aws4_request' in request.headers['Authorization']
        observations['signed'] += 1

    def received(http_response, **kw):
        assert http_response.status_code in (200, 400, 403)
        observations['sdkResponses'] += 1

    client.meta.events.register('before-send.codecommit', sent)
    client.meta.events.register('after-call.codecommit', received)

    def execute(op, **kwargs):
        if 'fileContent' in kwargs:
            kwargs['fileContent'] = base64.b64decode(kwargs['fileContent'], validate=True)
        func = next(name for name, action in client.meta.method_to_api_mapping.items() if action == op)
        data = getattr(client, func)(**kwargs)
        data.pop('ResponseMetadata')
        if 'fileContent' in data:
            data['fileContent'] = base64.b64encode(data['fileContent']).decode()
        return data
    return execute


def button(page, label):
    return page.get_by_role('button', name=label, exact=True)


def context(browser, team, token, observations, lang='en', width=1350):
    ctx = browser.new_context(viewport={'width':width, 'height':900}, service_workers='block', reduced_motion='reduce')
    origin = urlsplit(team.endpoint)
    def local(route):
        url = urlsplit(route.request.url)
        assert (url.scheme, url.netloc) == (origin.scheme, origin.netloc)
        route.continue_()
    ctx.route('**/*', local)
    assert ctx.request.post(team.endpoint + '/_simulator/login', form={'token':token}, max_redirects=0).status == 303
    ctx.add_init_script("localStorage.setItem('tenkacloud-simulator-locale'," + json.dumps(lang) + ")")
    page = ctx.new_page()
    page.set_default_timeout(12000)
    page.on('pageerror', lambda e: observations['errors'].append(type(e).__name__))
    def capture(response):
        request = response.request
        target = request.headers.get('x-amz-target', '')
        if target.startswith(PREFIX):
            assert request.url == team.endpoint+'/' and request.method == 'POST' and not request.redirected_from
            assert 'x-simulator-token' not in request.headers
            observations['console'].append([target.removeprefix(PREFIX), response.status])
    page.on('response', capture)
    return ctx, page


def show(page, team, lang='en'):
    page.goto(team.endpoint+'/_simulator/console/#/catalog')
    page.get_by_role('button', name=re.compile('^(Catalog service|カタログサービス)')).click()
    page.get_by_role('option', name=re.compile('^AWS CodeCommit')).click()
    page.get_by_role('link', name=NAME, exact=True).click()
    pane = page.get_by_test_id('codecommit-contents')
    expect(pane.get_by_role('heading',name='Repository contents' if lang=='en' else 'リポジトリの内容',exact=True)).to_be_visible()
    return pane


def audit(page, name, axe, observations):
    assert_reflow(page)
    if axe:
        page.evaluate(axe.read_text())
        errors = page.evaluate("""async () => (await axe.run(document, {runOnly:{type:'tag',
            values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})).violations.map(v=>[v.id,v.nodes.map(n=>n.target)])""")
        assert not errors, (name, errors)
        observations['audits'].append([name,page.viewport_size['width']])


def scalar(ctx, team, action, body):
    with ctx.new_page() as page:
        with page.expect_response(lambda r:r.url==team.endpoint+'/_simulator/docs/catalog/codecommit.json') as loaded:
            page.goto(team.endpoint+'/_simulator/docs/catalog.html?api=codecommit')
        op=loaded.value.json()['paths']['/']['post']
        target=PREFIX+action
        assert target in op['parameters'][0]['schema']['enum']
        sidebar=page.get_by_role('complementary',name='Sidebar for AWS CodeCommit (simulator catalog)',exact=True)
        sidebar.locator('a[href="#codecommit/tag/aws-codecommit/POST/"]').click()
        button(page,'Test Request (post /)').click()
        dialog=page.get_by_role('dialog').filter(has=button(page,'Close Client'))
        request=dialog.get_by_role('region',name='Request: '+op['summary'],exact=True)
        request.get_by_role('tab',name='Headers',exact=True).press('Enter')
        row=request.get_by_role('row',name=re.compile('^Include X-Amz-Target in request'))
        row.get_by_role('button',name=re.compile('^CodeCommit_20150413[.]')).click()
        page.get_by_role('menuitem',name=target,exact=True).click()
        expect(row.get_by_role('checkbox')).to_be_checked()
        request.get_by_role('tab',name='Body',exact=True).press('Enter')
        edit=request.locator('.cm-content[contenteditable=true]:visible')
        edit.click(); edit.press('ControlOrMeta+a'); page.keyboard.insert_text(json.dumps(body))
        assert json.loads(edit.inner_text()) == body
        sent=[]
        page.on('request',lambda r:sent.append(r) if r.method=='POST' else None)
        with page.expect_response(lambda r:r.request.method=='POST') as result:
            dialog.get_by_role('button',name=re.compile('^Send post request to ')).click()
        response=result.value
        assert len(sent)==1 and sent[0]==response.request and response.status==200
        assert sent[0].url==team.endpoint+'/' and sent[0].post_data_json==body and sent[0].redirected_from is None
        headers=sent[0].all_headers()
        assert headers['x-amz-target']==target and headers['cookie'] and not {'authorization','x-simulator-token','x-simulator-principal'}&headers.keys()
        assert headers['content-type'].split(';')[0]=='application/x-amz-json-1.1'
        data=response.json()
        panel=dialog.locator('.response-section-content-body').filter(has=page.get_by_role('link',name='Download Response Body',exact=True))
        with page.expect_download() as download:
            panel.get_by_role('link',name='Download Response Body',exact=True).click()
        assert download.value.failure() is None and json.loads(Path(download.value.path()).read_bytes())==data
        download.value.delete()
        return data


def stale_read(page, pane, team, expected_status, new_branch, new_tip):
    # Hold an actual native result until after the user switches to a different branch.
    held=[]
    def hold(route):
        if route.request.headers.get('x-amz-target')!=PREFIX+'GetFile':
            route.continue_()
            return
        response=route.fetch()
        assert response.status==expected_status
        held.append((route,response))
    page.route(team.endpoint+'/',hold)
    pane.get_by_label('File path',exact=True).fill(FILE)
    button(pane,'Read file').click()
    deadline=time.monotonic()+8
    while not held and time.monotonic()<deadline:
        page.wait_for_timeout(20)
    assert len(held)==1
    pane.get_by_role('button',name=re.compile('^Branch')).click()
    page.get_by_role('option',name=new_branch,exact=True).click()
    expect(pane.get_by_text(new_tip,exact=True)).to_be_visible()
    route,response=held[0]
    route.fulfill(response=response)
    page.unroute(team.endpoint+'/',hold)
    expect(pane.get_by_text(new_tip,exact=True)).to_be_visible()
    expect(button(pane,'Download file')).to_have_count(0)
    expect(pane.get_by_text(re.compile('^403:'))).to_have_count(0)


def exercise(binary, use_sdk, axe):
    observed={'signed':0,'sdkResponses':0,'stockSDK':use_sdk,'console':[],'scalar':[],'audits':[],'errors':[]}
    with tempfile.TemporaryDirectory(prefix='codecommit-console-') as directory, sync_playwright() as pw, ExitStack() as stack:
        root=Path(directory)
        teams=[]
        for label in ('A','B'):
            folder=root/label; folder.mkdir()
            teams.append(LocalTeam(folder,binary))
        team,peer=teams
        browser=stack.enter_context(pw.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),chromium_sandbox=True))
        with started(team),started(peer):
            a,b = [driver(t,use_sdk,stack,observed) for t in teams]
            def api(op,**kwargs): return a(op,repositoryName=NAME,**kwargs)
            def other(op,**kwargs): return b(op,repositoryName=NAME,**kwargs)
            ids=[f('CreateRepository')['repositoryMetadata']['repositoryId'] for f in (api,other)]
            assert ids[0]!=ids[1]
            peer_commit=other('PutFile',branchName='main',filePath=FILE,fileContent='cGVlcg==')['commitId']
            ctx,page=context(browser,team,team.token,observed)
            pane=show(page,team)
            expect(pane.get_by_text('This repository has no branches.',exact=False)).to_be_visible()
            button(pane,'Add or edit file').press('Enter')
            pane.get_by_label('Commit file path',exact=True).fill('/'+FILE)
            pane.get_by_label('Upload file',exact=True).set_input_files({'name':'example.bin','mimeType':'application/octet-stream','buffer':BINARY_CONTENT})
            pane.get_by_role('button',name=re.compile('^File mode')).click()
            page.get_by_role('option',name='EXECUTABLE',exact=True).click()
            pane.get_by_label('New commit message',exact=True).fill('First binary')
            audit(page,'first commit editor',axe,observed)
            button(pane,'Commit file').press('Enter')
            expect(pane.get_by_text('Commit saved and verified.',exact=True)).to_be_visible()
            root_id=api('GetBranch',branchName='main')['branch']['commitId']
            first=api('GetFile',filePath=FILE,commitSpecifier=root_id)
            assert first['fileMode']=='EXECUTABLE' and base64.b64decode(first['fileContent'])==BINARY_CONTENT
            assert api('GetCommit',commitId=root_id)['commit']['parents']==[]
            expect(pane.get_by_text(first['blobId'],exact=True)).to_be_visible()
            with page.expect_download() as saved: button(pane,'Download file').click()
            assert Path(saved.value.path()).read_bytes()==BINARY_CONTENT; saved.value.delete()
            assert scalar(ctx,team,'GetFile',{'repositoryName':NAME,'filePath':FILE,'commitSpecifier':root_id})==first
            observed['scalar'].append('GetFile')
            with_args={'branchName':'main','parentCommitId':root_id,'filePath':FILE,'fileContent':'','fileMode':'NORMAL','commitMessage':'Scalar empty file'}
            second=scalar(ctx,team,'PutFile',dict(repositoryName=NAME,**with_args))['commitId']
            observed['scalar'].append('PutFile')
            assert api('GetFile',filePath=FILE,commitSpecifier=second)['fileContent']==''
            button(pane,'Refresh repository').click()
            expect(pane.get_by_text(second,exact=True)).to_be_visible()
            button(pane,root_id).click()
            expect(pane.get_by_text('First binary',exact=True)).to_be_visible()
            pane.get_by_label('New branch name',exact=True).fill('historical')
            button(pane,'Create branch at this commit').click()
            expect(pane.get_by_text('Branch created and verified.',exact=True)).to_be_visible()
            assert api('GetBranch',branchName='historical')['branch']['commitId']==root_id
            button(pane,'Add or edit file').click()
            pane.get_by_label('Commit file path',exact=True).fill('notes.txt')
            pane.get_by_label('Text contents',exact=True).fill('separate branch\n')
            button(pane,'Commit file').click()
            expect(pane.get_by_text('Commit saved and verified.',exact=True)).to_be_visible()
            fork_tip=api('GetBranch',branchName='historical')['branch']['commitId']
            assert api('GetCommit',commitId=fork_tip)['commit']['parents']==[root_id]
            assert api('GetBranch',branchName='main')['branch']['commitId']==second
            audit(page,'populated history',axe,observed)

            # A newer writer wins while the visible draft still holds its original parent.
            button(pane,'Add or edit file').click()
            pane.get_by_label('Text contents',exact=True).fill('preserved after conflict')
            advanced=api('PutFile',branchName='historical',parentCommitId=fork_tip,filePath='notes.txt',fileContent='eA==')['commitId']
            button(pane,'Commit file').click()
            expect(pane.get_by_text(re.compile('400: .*not the current branch tip'))).to_be_visible()
            expect(pane.get_by_label('Text contents',exact=True)).to_have_value('preserved after conflict')
            assert api('GetBranch',branchName='historical')['branch']['commitId']==advanced
            button(pane,'Cancel editing').click()
            button(pane,'Refresh repository').click()
            expect(pane.get_by_text(advanced,exact=True)).to_be_visible()

            # Drop the receipt only after the real server has committed the request.
            lost=[]
            def drop_ack(route):
                if route.request.headers.get('x-amz-target')!=PREFIX+'PutFile':
                    route.continue_();return
                reply=route.fetch()
                assert reply.status==200
                lost.append(reply.json()['commitId'])
                route.abort('failed')
            page.route(team.endpoint+'/',drop_ack)
            button(pane,'Add or edit file').click()
            pane.get_by_label('Commit file path',exact=True).fill('uncertain.txt')
            pane.get_by_label('Text contents',exact=True).fill('observed after loss')
            button(pane,'Commit file').click()
            expect(pane.get_by_text('The write outcome is unknown.',exact=False)).to_be_visible()
            assert len(lost)==1 and api('GetBranch',branchName='historical')['branch']['commitId']==lost[0]
            expect(button(pane,'Commit file')).to_have_count(0)
            page.unroute(team.endpoint+'/',drop_ack)
            button(pane,'Refresh repository').click()
            expect(pane.get_by_text('Current repository state refreshed.',exact=True)).to_be_visible()
            expect(pane.get_by_text('observed after loss',exact=True)).to_be_visible()
            assert len(lost)==1
            # This transport failure occurs before the native service receives a write.
            missed=[]
            def before_native(route):
                if route.request.headers.get('x-amz-target')==PREFIX+'PutFile':
                    missed.append(True);route.abort('failed')
                else: route.continue_()
            page.route(team.endpoint+'/',before_native)
            button(pane,'Add or edit file').click()
            pane.get_by_label('Commit file path',exact=True).fill('never-created.txt')
            button(pane,'Commit file').click()
            expect(pane.get_by_text('The write outcome is unknown.',exact=False)).to_be_visible()
            page.unroute(team.endpoint+'/',before_native)
            button(pane,'Refresh repository').click()
            expect(pane.get_by_text('Current repository state refreshed.',exact=True)).to_be_visible()
            assert missed==[True] and api('GetBranch',branchName='historical')['branch']['commitId']==lost[0]
            stale_read(page,pane,team,200,'main',second)
            ctx.close()

            for i in range(10): api('CreateBranch',branchName='page-'+str(i),commitId=root_id)
            read_actions=['GetRepository','ListRepositories','GetBranch','ListBranches','GetCommit','GetFile','ListTagsForResource']
            def rights(actions):
                wire(team,'PUT','/_simulator/v1/iam/principals/git-reader/policy',{'Version':'2012-10-17','Statement':[
                    {'Effect':'Allow','Action':['codecommit:'+op for op in actions],'Resource':'*'}]})
            rights(read_actions)
            token=wire(team,'POST','/_simulator/v1/access',{'Principal':'git-reader'})['AccessToken']
            ctx,page=context(browser,team,token,observed)
            pane=show(page,team)
            button(pane,'Add or edit file').click()
            pane.get_by_label('Commit file path',exact=True).fill('denied.txt')
            button(pane,'Commit file').click()
            expect(pane.get_by_text(re.compile('^403:'))).to_be_visible()
            assert api('GetBranch',branchName='main')['branch']['commitId']==second
            audit(page,'write denied',axe,observed)
            button(pane,'Refresh repository').click()
            expect(button(pane,'Load more branches')).to_be_enabled()
            rights([op for op in read_actions if op!='ListBranches'])
            button(pane,'Load more branches').click()
            expect(pane.get_by_text(re.compile('^403:'))).to_be_visible()
            expect(button(pane,'Load more branches')).to_have_count(0)
            expect(pane.get_by_role('button',name=re.compile('^Branch'))).to_have_count(0)
            rights(read_actions)
            button(pane,'Refresh repository').click()
            button(pane,'Load more branches').click()
            pane.get_by_role('button',name=re.compile('^Branch')).click()
            expect(page.get_by_role('option',name='page-9',exact=True)).to_be_visible()
            page.keyboard.press('Escape')
            rights([op for op in read_actions if op!='GetFile'])
            stale_read(page,pane,team,403,'historical',lost[0])
            rights(read_actions)
            ctx.close()
            ctx,ja=context(browser,team,team.token,observed,'ja',390)
            pane=show(ja,team,'ja')
            expect(pane.get_by_text(second,exact=True)).to_be_visible()
            audit(ja,'Japanese repository details',axe,observed)
            ctx.close()
        with started(team),started(peer):
            a,b = [driver(t,use_sdk,stack,observed) for t in teams]
            try:
                assert api('GetRepository')['repositoryMetadata']['repositoryId']==ids[0]
                assert api('GetFile',commitSpecifier=root_id,filePath=FILE)==first
                assert api('GetFile',commitSpecifier=second,filePath=FILE)['fileContent']==''
                assert api('GetBranch',branchName='historical')['branch']['commitId']==lost[0]
                assert other('GetBranch',branchName='main')['branch']['commitId']==peer_commit
                ctx,page=context(browser,team,team.token,observed)
                pane=show(page,team)
                button(pane,'Add or edit file').click()
                pane.get_by_label('Commit file path',exact=True).fill('boundary.bin')
                upload=pane.get_by_label('Upload file',exact=True)
                upload.set_input_files({'name':'oversize','mimeType':'application/octet-stream','buffer':bytes(65537)})
                expect(button(pane,'Commit file')).to_be_disabled()
                maximum=bytes([255])*65536
                upload.set_input_files({'name':'maximum','mimeType':'application/octet-stream','buffer':maximum})
                button(pane,'Commit file').click()
                expect(pane.get_by_text('Commit saved and verified.',exact=True)).to_be_visible()
                boundary=api('GetFile',filePath='boundary.bin',commitSpecifier='main')
                assert boundary['fileSize']==len(maximum) and base64.b64decode(boundary['fileContent'])==maximum
                assert api('GetCommit',commitId=boundary['commitId'])['commit']['parents']==[second]
                button(pane,'Add or edit file').click()
                pane.get_by_label('Commit file path',exact=True).fill('held-draft.txt')
                assert api('DeleteRepository')['repositoryId']==ids[0]
                replacement=api('CreateRepository')['repositoryMetadata']['repositoryId']
                assert replacement!=ids[0]
                ids[0]=replacement
                before=sum(op=='PutFile' for op,status in observed['console'])
                button(pane,'Commit file').click()
                expect(pane.get_by_text('The selected repository has changed.',exact=False)).to_be_visible()
                assert len(api('ListBranches')['branches'])==0
                assert sum(op=='PutFile' for op,status in observed['console'])==before
                expect(button(pane,'Commit file')).to_have_count(0)
                ctx.close()
            finally:
                assert api('DeleteRepository')['repositoryId']==ids[0]
                assert other('DeleteRepository')['repositoryId']==ids[1]
                assert all(c('ListRepositories')['repositories']==[] for c in (a,b))
    assert not root.exists() and not observed['errors']
    assert observed['signed']==observed['sdkResponses'] and (not use_sdk or observed['signed']>25)
    actual={op for op,status in observed['console'] if status==200}
    assert {'GetFile','GetCommit','GetBranch','ListBranches','PutFile','CreateBranch'}<=actual
    return {**observed,'cleanup':True,'reopenedDatabases':2,'lostAcknowledgements':len(lost),
            'staleReadStatuses':[200,403],'replacedRepositoryWriteSends':0,'largestFileBytes':len(maximum),
            'preSendLossCheckedAgainstMissingFile':True}


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--sdk',action='store_true')
    args=parser.parse_args()
    binary=Path(os.environ.get('SIMULATOR_BINARY','dist/tenkacloud-simulator')).resolve(strict=True)
    axe=Path(os.environ['AXE_SCRIPT']) if os.environ.get('AXE_SCRIPT') else None
    print(json.dumps(exercise(binary,args.sdk,axe)))
