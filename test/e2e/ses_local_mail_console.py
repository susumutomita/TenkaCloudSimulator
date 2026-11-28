"""Drive the shipped SES console and reference against one verified mailbox."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright
from reflow import assert_reflow
from ses_local_mail import ABSENT,DOMAIN,FROM,IDENTITIES,MAILBOX,REGION,SEND,SINK,TEMPLATE,direct,parsed,verify

class MailConsole:
    def __init__(self,browser,axe,stack):
        self.browser,self.axe,self.stack=browser,axe,stack
        self.outside=[];self.audits=[];self.scalar=[];self.sent=[]
        self.stored_ids=set()
        self.page=self.ctx=self.team=None

    def session(self,team,token,width=1440,ja=False):
        ctx=self.browser.new_context(viewport={'width':width,'height':950},service_workers='block',accept_downloads=True)
        self.stack.callback(ctx.close)
        if ja: ctx.add_init_script("localStorage.setItem('tenkacloud-simulator-locale','ja')")
        authority=urlsplit(team.endpoint)
        def guard(route):
            target=urlsplit(route.request.url)
            if (target.scheme,target.netloc)==(authority.scheme,authority.netloc):route.continue_()
            else:self.outside.append(target.scheme);route.abort()
        ctx.route('**/*',guard)
        assert ctx.request.post(team.endpoint+'/_simulator/login',form={'token':token},max_redirects=0).status==303
        return ctx

    def open(self,ctx,team,ja=False):
        p=ctx.new_page();p.set_default_timeout(8000)
        p.goto(team.endpoint+'/_simulator/console/#/catalog')
        p.get_by_role('button',name=re.compile('^'+('カタログサービス' if ja else 'Catalog service'))).click()
        p.get_by_role('option',name=re.compile('Amazon Simple Email Service')).click()
        p.get_by_role('button',name='ローカルメール' if ja else 'Local email',exact=True).click()
        return p

    def actual(self,page,team,method,path,button,status):
        url=team.endpoint+path
        with page.expect_response(lambda r:r.url==url and r.request.method==method) as s:
            button.focus();page.keyboard.press('Enter')
        res=s.value
        assert res.status==status and res.request.redirected_from is None and res.request.header_value('cookie'), (method,path,status,res.status)
        assert not res.request.header_value('x-simulator-token')
        self.sent.append((method,path,status))
        return res

    def audit(self,p,label):
        p.evaluate(self.axe)
        errors=p.evaluate("""async()=>(await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})).violations.map(v=>[v.id,v.nodes.map(n=>n.target)])""")
        assert not errors,(label,errors)
        assert_reflow(p)
        self.audits.append(label)

    def create_domain(self,team):
        self.team=team;self.ctx=self.session(team,team.token);p=self.page=self.open(self.ctx,team)
        p.get_by_label('Local .test domain',exact=True).fill(DOMAIN)
        data=self.actual(p,team,'POST',IDENTITIES,p.get_by_role('button',name='Create domain identity',exact=True),200).json()
        expect(p.get_by_text(re.compile(r'owned\.test.*PENDING'))).to_be_visible()
        for token in data['DkimAttributes']['Tokens']:
            expect(p.get_by_text(token+'._domainkey.'+DOMAIN+'.',exact=True)).to_be_visible()
        return data

    def receipt(self,p,team,ident):
        data=self.actual(p,team,'GET',SINK,p.get_by_role('button',name='Refresh receipts',exact=True),200).json()
        assert ident in {r['messageId'] for r in data['messages']}
        row=self.actual(p,team,'GET',SINK+'/'+ident,p.get_by_role('button',name=ident,exact=True),200).json()
        expect(p.get_by_text(ident,exact=True).last).to_be_visible()
        return row

    def reference_send(self,ctx,team):
        p=ctx.new_page();p.set_default_timeout(10000)
        try:
            p.goto(team.endpoint+'/_simulator/docs/catalog.html?api=sesv2')
            p.get_by_text('SendEmail',exact=True).first.click()
            p.get_by_role('button',name='Test Request (post '+SEND+')',exact=True).click()
            submit=p.get_by_role('button',name=re.compile('^Send post request to '))
            with p.expect_response(lambda r:r.request.method=='POST' and r.url==team.endpoint+SEND) as obs:submit.click()
            res=obs.value; req=res.request
            assert res.status==200 and req.redirected_from is None and req.header_value('cookie'), (res.status, req.header_value('x-simulator-service'), sorted((req.post_data_json or {}).keys()), res.header_value('x-amzn-errortype'), res.text()[:140] if res.status!=200 else 'missing cookie')
            assert req.header_value('x-simulator-service')=='ses' and not req.header_value('x-simulator-principal')
            assert not req.header_value('x-simulator-token') and not req.header_value('authorization')
            body=req.post_data_json
            assert body['FromEmailAddress']==FROM and body['Destination']=={'ToAddresses':[MAILBOX]}
            ident=res.json()['MessageId']
            pane=p.locator('.response-section-content-body').filter(has=p.get_by_role('link',name='Download Response Body',exact=True))
            expect(pane).to_have_count(1)
            expect(pane).to_contain_text(ident)
            parsed(team,ident,body['Content']['Simple']['Subject']['Data'],body['Content']['Simple']['Body']['Text']['Data'])
            self.scalar.append('SendEmail')
            return ident
        finally:p.close()

    def shared(self,owner,peer,access,client,ids):
        p=self.page
        got=self.actual(p,owner,'GET',IDENTITIES+'/'+DOMAIN,p.get_by_role('button',name='Check DNS proof',exact=True),200).json()
        assert got['VerificationStatus']=='SUCCESS'
        expect(p.get_by_text(re.compile(r'owned\.test.*SUCCESS'))).to_be_visible()
        p.get_by_label('From address',exact=True).fill(FROM)
        p.get_by_label('Subject',exact=True).fill('Console test')
        p.get_by_label('Plain text',exact=True).fill('literal {{console}}')
        p.get_by_label('HTML body',exact=True).fill('<i>Console</i>')
        send=p.get_by_role('button',name='Send to local sink',exact=True)
        def sent():
            ident=self.actual(p,owner,'POST',SEND,send,200).json()['MessageId']
            expect(p.get_by_role('status').filter(has_text=ident)).to_be_visible()
            assert ident not in ids;ids.add(ident)
            return ident
        simple=sent();assert self.receipt(p,owner,simple)['outcome']=='delivered'
        with p.expect_download() as download:p.get_by_role('button',name='Download MIME',exact=True).click()
        assert download.value.suggested_filename==simple+'.eml'
        assert Path(download.value.path()).read_bytes()==parsed(owner,simple,'Console test','literal {{console}}','<i>Console</i>')
        self.audit(p,'en-1440-delivered')
        shortcode=self.reference_send(self.ctx,owner);ids.add(shortcode)
        assert self.receipt(p,owner,shortcode)['outcome']=='delivered'
        p.get_by_role('button',name=re.compile('^Message content')).click()
        p.get_by_role('option',name='Saved template',exact=True).click()
        p.get_by_label('Template name',exact=True).fill(TEMPLATE)
        p.get_by_label('Template data (JSON)',exact=True).fill('{"who":"ブラウザ"}')
        saved=sent();parsed(owner,saved,'Hello ブラウザ','Saved ブラウザ')
        p.get_by_label('To address',exact=True).fill(ABSENT)
        rejected=sent(); assert self.receipt(p,owner,rejected)['reason']=='mailbox-not-found'
        assert p.get_by_role('button',name='Download MIME',exact=True).count()==0
        p.get_by_label('To address',exact=True).fill(MAILBOX)
        previous=set(ids); lost=[]
        def drop(route):
            if route.request.method!='POST':return route.continue_()
            result=route.fetch(max_redirects=0)
            assert result.status==200
            lost.append(result.json()['MessageId'])
            route.abort('failed')
        p.route(owner.endpoint+SEND,drop)
        send.click()
        expect(p.get_by_text('The write acknowledgement is unknown.',exact=True)).to_be_visible()
        expect(send).to_be_disabled()
        assert len(lost)==1 and lost[0] not in previous
        assert p.get_by_text(lost[0],exact=True).count()==0
        self.audit(p,'en-1440-unknown')
        p.unroute(owner.endpoint+SEND,drop)
        p.get_by_role('button',name='Read current state',exact=True).click()
        expect(send).to_be_enabled()
        ids.add(lost[0])
        assert {r['messageId'] for r in json.loads(direct(owner,'GET',SINK))['messages']}==ids
        assert p.get_by_role('status').filter(has_text=lost[0]).count()==0
        assert len(lost)==1
        foreign=self.open(self.session(peer,peer.token),peer)
        self.actual(foreign,peer,'GET',SINK,foreign.get_by_role('button',name='Refresh receipts',exact=True),200)
        expect(foreign.get_by_text('No saved submissions.',exact=True)).to_be_visible()
        limited=self.open(self.session(owner,access),owner)
        self.actual(limited,owner,'GET',SINK,limited.get_by_role('button',name='Refresh receipts',exact=True),403)
        expect(limited.get_by_role('alert').filter(has_text='403').first).to_be_visible()
        assert limited.get_by_text(simple,exact=True).count()==0
        mobile=self.open(self.session(owner,owner.token,390,True),owner,True)
        self.actual(mobile,owner,'GET',SINK,mobile.get_by_role('button',name='受信記録を更新',exact=True),200)
        expect(mobile.get_by_role('button',name=simple,exact=True)).to_be_visible()
        self.audit(mobile,'ja-390-receipts')
        # Hold a genuine authorized DNS read until the operator changes the target.
        held=[]
        def hold(route):
            actual=route.fetch(max_redirects=0)
            assert actual.status==200 and actual.json()['VerificationStatus']=='SUCCESS'
            held.append((route,actual))
        exact=owner.endpoint+IDENTITIES+'/'+DOMAIN
        p.route(exact,hold)
        p.get_by_role('button',name='Check DNS proof',exact=True).click()
        p.wait_for_function("!![...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='Cancel read')")
        for _ in range(100):
            if held:break
            p.wait_for_timeout(20)
        assert len(held)==1
        p.get_by_label('Local .test domain',exact=True).fill('new-target.test')
        held[0][0].fulfill(response=held[0][1]);p.unroute(exact,hold)
        expect(p.get_by_text(re.compile(r'owned\.test.*SUCCESS'))).to_have_count(0)
        p.get_by_label('Local .test domain',exact=True).fill(DOMAIN)
        self.receipt(p,owner,rejected)
        p.get_by_role('button',name='Remove receipt',exact=True).click()
        self.actual(p,owner,'DELETE',SINK+'/'+rejected,p.get_by_role('button',name='Confirm removal',exact=True),204)
        direct(owner,'GET',SINK+'/'+rejected,expected=404)
        ids.remove(rejected)
        assert not self.outside and self.scalar==['SendEmail']
        return dict(staleReadIgnored=True,consolePurgeConfirmed=True,scalar=self.scalar,audits=self.audits,browserCalls=self.sent,lostAcknowledgements=len(lost),unknownAutoSends=0)

    def revoked(self,owner):
        # The CLI intentionally retires browser sessions across restart.
        self.ctx=self.session(owner,owner.token);p=self.page=self.open(self.ctx,owner)
        p.get_by_label('Local .test domain',exact=True).fill(DOMAIN)
        p.get_by_label('From address',exact=True).fill(FROM)
        p.get_by_label('Subject',exact=True).fill('Revoked sender')
        p.get_by_label('Plain text',exact=True).fill('Must not reach the local sink')
        self.actual(p,owner,'GET',IDENTITIES+'/'+DOMAIN,p.get_by_role('button',name='Check DNS proof',exact=True),200)
        expect(p.get_by_text(re.compile(r'owned\.test.*PENDING'))).to_be_visible()
        self.actual(p,owner,'POST',SEND,p.get_by_role('button',name='Send to local sink',exact=True),400)
        expect(p.get_by_role('alert').filter(has_text='400').first).to_be_visible()
        assert not self.outside

def main():
    from contextlib import ExitStack
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary',required=True,type=Path);parser.add_argument('--binary-sha256',required=True)
    parser.add_argument('--axe',required=True,type=Path);parser.add_argument('--direct-local',action='store_true')
    args=parser.parse_args()
    with args.binary.open('rb') as f:assert hashlib.file_digest(f,'sha256').hexdigest()==args.binary_sha256
    provenance=json.loads((args.axe.parent/'provenance.json').read_text())
    assert provenance['version']=='4.11.0' and hashlib.sha256(args.axe.read_bytes()).hexdigest()==provenance['bundleSha256']
    with sync_playwright() as playwright:
        browser=playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE'),chromium_sandbox=True,args=['--no-proxy-server'])
        try:
            with ExitStack() as contexts:
                result=verify(args.binary.resolve(),not args.direct_local,MailConsole(browser,args.axe.read_text(),contexts))
        finally: browser.close()
    print(json.dumps(result))

if __name__=='__main__':main()
