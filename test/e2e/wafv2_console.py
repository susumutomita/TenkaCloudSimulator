"""Native WAF Cloudscape round-trip; requires root-integrated WAF route/backend.

This verifies actual SDK state, optimistic concurrency, desktop/mobile reflow and
browser containment. Real API Gateway Count/Block→Lambda is a separate mandatory
integration gate; this helper alone does not complete the WAF service.
"""
import json,os,re,shutil,sys
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,boto3,Config,sync_playwright,expect,REPORTS,assert_reflow

def client(endpoint,token):
 c=boto3.client('wafv2',endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
 c.meta.events.register('before-sign.wafv2',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token))
 return c

def choose(page,label,value):
 page.get_by_label(label,exact=True).click()
 page.get_by_role('option',name=value,exact=True).click()

with running(authenticated=True) as (endpoint,token),sync_playwright() as p:
 waf=client(endpoint,token)
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
 context=browser.new_context(viewport={'width':1440,'height':1000},bypass_csp=bool(os.environ.get('AXE_SCRIPT_PATH')));page=context.new_page();errors=[];outside=[]
 page.on('pageerror',lambda error:errors.append(str(error)))
 def route(r):
  if r.request.url.startswith(endpoint+'/'):r.continue_()
  else:outside.append(r.request.url);r.abort()
 page.route('**/*',route);page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click()
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='AWS WAF',exact=True).click()
 page.get_by_role('button',name='Create web ACL',exact=True).click();page.get_by_label('Name',exact=True).fill('dummy-console-waf');page.get_by_label('Description - optional',exact=True).fill('Local dummy data')
 assert_reflow(page);page.screenshot(path=str(REPORTS/'wafv2-create-desktop.png'),full_page=True)
 page.set_viewport_size({'width':390,'height':844});assert_reflow(page);page.screenshot(path=str(REPORTS/'wafv2-create-mobile.png'),full_page=True);page.set_viewport_size({'width':1440,'height':1000})
 page.get_by_role('button',name='Create web ACL',exact=True).click();expect(page.get_by_role('heading',name='dummy-console-waf',exact=True)).to_be_visible()
 summary=waf.list_web_acls(Scope='REGIONAL')['WebACLs'];assert len(summary)==1;identifier=summary[0]['Id']
 def get():return waf.get_web_acl(Name='dummy-console-waf',Id=identifier,Scope='REGIONAL')
 # Exact original statement semantics. boto3 uses bytes while native wire JSON uses base64.
 statement={'AndStatement':{'Statements':[{'ByteMatchStatement':{'SearchString':'L3dwLWpzb24vYmF0Y2gvdjE=','FieldToMatch':{'UriPath':{}},'PositionalConstraint':'ENDS_WITH','TextTransformations':[{'Priority':0,'Type':'NONE'}]}},{'SqliMatchStatement':{'FieldToMatch':{'Body':{'OversizeHandling':'CONTINUE'}},'TextTransformations':[{'Priority':0,'Type':'URL_DECODE'}]}}]}}
 page.get_by_role('button',name='Add rule',exact=True).click();page.get_by_label('Rule name',exact=True).fill('batch-sqli');page.get_by_label('Statement JSON',exact=True).fill(json.dumps(statement));page.get_by_role('button',name='Save',exact=True).click()
 original=get()['WebACL'];rule=original['Rules'][0];assert rule['Action']=={'Count':{}} and rule['Statement']['AndStatement']['Statements'][0]['ByteMatchStatement']['SearchString']==b'/wp-json/batch/v1'
 page.get_by_role('radio',name='Select batch-sqli',exact=True).check();page.get_by_role('button',name='Edit rule',exact=True).click();choose(page,'Action','Block')
 # A second SDK session changes the same object while the browser holds its old token.
 current=get();acl=current['WebACL'];waf.update_web_acl(Name=acl['Name'],Id=acl['Id'],Scope='REGIONAL',LockToken=current['LockToken'],DefaultAction=acl['DefaultAction'],Rules=acl['Rules'],VisibilityConfig=acl['VisibilityConfig'],Description='Changed in another session')
 page.get_by_role('button',name='Save',exact=True).click();expect(page.get_by_text('Web ACL changed in another session',exact=True)).to_be_visible();assert get()['WebACL']['Rules'][0]['Action']=={'Count':{}}
 page.get_by_role('button',name='Reload latest web ACL',exact=True).click();page.get_by_role('radio',name='Select batch-sqli',exact=True).check();page.get_by_role('button',name='Edit rule',exact=True).click();choose(page,'Action','Block')
 # Keyboard activates the actual save button; no evaluate() state mutation.
 page.get_by_role('button',name='Save',exact=True).focus();page.keyboard.press('Enter');expect(page.get_by_text('Rule saved.',exact=True)).to_be_visible()
 saved=get()['WebACL'];assert saved['Rules'][0]['Action']=={'Block':{}};assert saved['Rules'][0]['Statement']==rule['Statement'];assert saved['VisibilityConfig']==original['VisibilityConfig'];assert saved['Description']=='Changed in another session'
 assert_reflow(page);page.screenshot(path=str(REPORTS/'wafv2-rules-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});assert_reflow(page);page.screenshot(path=str(REPORTS/'wafv2-rules-mobile.png'),full_page=True);page.set_viewport_size({'width':1440,'height':1000})
 axe=os.environ.get('AXE_SCRIPT_PATH')
 if axe:
  page.add_script_tag(path=axe)
  for width in (1440,390):
   page.set_viewport_size({'width':width,'height':1000});assert_reflow(page);page.screenshot(path=str(REPORTS/('wafv2-audit-'+str(width)+'.png')),full_page=True,animations='disabled');audit=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})");assert not audit['violations'],audit['violations']
 page.get_by_role('button',name='Delete web ACL',exact=True).click();page.get_by_label('Enter web ACL name to confirm',exact=True).fill('dummy-console-waf');page.get_by_role('button',name='Delete web ACL',exact=True).click();expect(page.get_by_text('Web ACL deleted.',exact=True)).to_be_visible();assert waf.list_web_acls(Scope='REGIONAL')['WebACLs']==[]
 assert errors==[],errors;assert outside==[],outside;browser.close()
 print(json.dumps({'nativeCloudscapeWAFRoundTrip':True,'countToBlockPreservesStatementAndVisibility':True,'staleLockRejectedAndReloaded':True,'keyboardSave':True,'desktopMobileReflow':True,'axe':bool(axe),'externalRequests':0,'browserErrors':0,'teardown':'DELETED','authenticatedAWSScreenObserved':False,'actualHTTPCountBlockVerifiedHere':False}))
