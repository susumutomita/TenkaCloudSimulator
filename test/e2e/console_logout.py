"""Actual browser sign-out, opaque-session replay and independent team/SDK state.

No real AWS or guest runtime. Tokens stay in memory and are never printed or
included in reports. Login screenshots show only the masked password control.
"""
import json,os,shutil,sys,urllib.request
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,boto3,Config,sync_playwright,expect,REPORTS,assert_reflow

axe=Path(os.environ['AXE_SCRIPT_PATH']).read_text()
def sdk(endpoint,token):
 client=boto3.client('s3',endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(s3={'addressing_style':'path'},retries={'max_attempts':0}))
 client.meta.events.register('before-sign.s3',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return client

def control(endpoint,token,path,body,method='POST'):
 request=urllib.request.Request(endpoint+path,data=json.dumps(body).encode(),headers={'Content-Type':'application/json','X-Simulator-Token':token},method=method)
 with urllib.request.urlopen(request,timeout=10) as response:return json.load(response)

with running(authenticated=True) as (team_a,operator),running(authenticated=True) as (team_b,other_operator),running() as unsafe,sync_playwright() as p:
 s3=sdk(team_a,operator);other_s3=sdk(team_b,other_operator)
 s3.create_bucket(Bucket='logout-team-a-only');other_s3.create_bucket(Bucket='logout-team-b-only')
 control(team_a,operator,'/_simulator/v1/iam/principals/reader/policy',{'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'s3:ListAllMyBuckets','Resource':'*'}]},method='PUT')
 participant=control(team_a,operator,'/_simulator/v1/access',{'Principal':'reader'})['AccessToken'];participant_s3=sdk(team_a,participant)
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox']);wire=p.request.new_context();contexts=[];errors=[];outside=[];audits=[]
 def make_page(endpoint,width=1440):
  context=browser.new_context(viewport={'width':width,'height':1000});contexts.append(context);page=context.new_page();page.on('pageerror',lambda error:errors.append(str(error)))
  def guard(route):
   if route.request.url.startswith(endpoint+'/'):route.continue_()
   else:outside.append(route.request.url);route.abort()
  context.route('**/*',guard);return page
 def login(page,endpoint,token,capture=False):
  page.goto(endpoint+'/_simulator/console/');expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();page.get_by_label('Access token',exact=True).fill(token)
  if capture:
   assert page.get_by_label('Access token',exact=True).get_attribute('type')=='password'
   for width in (1440,390):
    page.set_viewport_size({'width':width,'height':1000});assert_reflow(page);page.screenshot(path=str(REPORTS/('logout-login-'+str(width)+'.png')),full_page=True,animations='disabled')
   page.set_viewport_size({'width':1440,'height':1000})
  page.get_by_role('button',name='Open sandbox',exact=True).click();page.wait_for_url(endpoint+'/_simulator/console/');expect(page.get_by_role('button',name='Account menu',exact=True)).to_be_visible();expect(page.get_by_role('link',name='logout-team-b-only' if endpoint==team_b else 'logout-team-a-only',exact=True)).to_be_visible()
 def cookie(page,endpoint):
  values=[item for item in page.context.cookies(endpoint) if item['name'].startswith('tcsim_')]
  assert len(values)==1,'Expected a single local authority cookie';return values[0]
 def header(value):return value['name']+'='+value['value']
 def protected(endpoint,value=None,extra=None):
  headers={**({'Cookie':header(value)} if value else {}),**(extra or {})};return wire.get(endpoint+'/_simulator/v1/capabilities',headers=headers,max_redirects=0)
 def account(page):
  trigger=page.get_by_role('button',name='Account menu',exact=True);trigger.focus();page.keyboard.press('Enter');expect(page.get_by_role('menuitem',name='Sign out',exact=True)).to_be_visible()
 def signout(page):
  account(page)
  if page.get_by_role('menuitem',name='Choose identity',exact=True).count():page.keyboard.press('ArrowDown')
  expect(page.get_by_role('menuitem',name='Sign out',exact=True)).to_be_focused();page.keyboard.press('Enter')
 def audit(page,name):
  page.evaluate(axe)
  for width in (1440,390):
   page.set_viewport_size({'width':width,'height':1000});assert_reflow(page);account(page);page.screenshot(path=str(REPORTS/(name+'-'+str(width)+'.png')),full_page=True,animations='disabled')
   result=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})");assert not result['violations'],[v['id'] for v in result['violations']];audits.append([name,width]);page.keyboard.press('Escape')
 page=make_page(team_a);second=make_page(team_a);other=make_page(team_b);limited=make_page(team_a,390)
 login(page,team_a,operator,True);login(second,team_a,operator);login(other,team_b,other_operator);login(limited,team_a,participant)
 original=cookie(page,team_a);second_cookie=cookie(second,team_a);participant_cookie=cookie(limited,team_a)
 assert original['value']!=operator and participant_cookie['value']!=participant,'SDK credential was copied into browser cookie'
 assert protected(team_a,original).status==200
 assert protected(team_a,None,{'X-Simulator-Token':original['value']}).status==401
 assert protected(team_a,{'name':original['name'],'value':operator}).status==401
 assert protected(team_b,original).status==401 and protected(team_b,None,{'X-Simulator-Token':operator}).status==401
 assert wire.get(team_a+'/',headers={'Cookie':header(participant_cookie),'X-Simulator-Principal':'simulator-admin'}).status==403
 account(limited);expect(limited.get_by_role('menuitem',name='Choose identity',exact=True)).to_have_count(0);limited.keyboard.press('Escape')
 audit(page,'logout-operator-account');audit(limited,'logout-participant-account')
 # Both a network failure and a server error must leave the session active and
 # show a retryable error instead of navigating as though revocation succeeded.
 for kind in ('network','server'):
  def fail(route):
   if kind=='network':route.abort('failed')
   else:route.fulfill(status=500,body='Local test failure')
  page.route('**/_simulator/logout',fail);signout(page);expect(page.get_by_text('Could not sign out',exact=True)).to_be_visible();assert '/_simulator/console/' in page.url;assert protected(team_a,original).status==200;page.unroute('**/_simulator/logout',fail)
 signout(page);expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();assert page.get_by_label('Access token',exact=True).input_value()==''
 assert protected(team_a).status==401 and protected(team_a,original).status==401
 assert protected(team_a,second_cookie).status==200 and protected(team_b,cookie(other,team_b)).status==200
 assert [item['Name'] for item in s3.list_buckets()['Buckets']]==['logout-team-a-only'];assert participant_s3.list_buckets()['Buckets'][0]['Name']=='logout-team-a-only';assert other_s3.list_buckets()['Buckets'][0]['Name']=='logout-team-b-only'
 page.goto(team_a+'/_simulator/console/');expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();page.get_by_label('Access token',exact=True).fill(operator);page.get_by_role('button',name='Open sandbox',exact=True).click();page.wait_for_url(team_a+'/_simulator/console/');expect(page.get_by_role('button',name='Account menu',exact=True)).to_be_visible();assert cookie(page,team_a)['value']!=original['value']
 signout(limited);expect(limited.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();assert protected(team_a,participant_cookie).status==401;assert participant_s3.list_buckets()['Buckets'][0]['Name']=='logout-team-a-only'
 unguarded=make_page(unsafe);unguarded.goto(unsafe+'/_simulator/console/');expect(unguarded.get_by_role('button',name='Account menu',exact=True)).to_be_visible();unguarded.get_by_role('button',name='Account menu',exact=True).click();expect(unguarded.get_by_role('menuitem',name='Sign out (authentication disabled)',exact=True)).to_have_attribute('aria-disabled','true')
 s3.delete_bucket(Bucket='logout-team-a-only');other_s3.delete_bucket(Bucket='logout-team-b-only');assert not errors,'Browser page errors';assert not outside,'Unexpected external request';wire.dispose();browser.close()
 print(json.dumps({'realAccountMenuLogout':True,'operatorAndParticipant':True,'browserCredentialSeparatedFromSDK':True,'revokedCookieReplay':401,'protectedAfterLogout':401,'relogin':True,'unrelatedBrowserAndSDKPreserved':True,'crossTeamCookieAndSDKRejected':True,'logoutFailureDoesNotNavigate':True,'unsafeModeTruthfullyDisabled':True,'keyboard':True,'axeStates':audits,'externalRequests':0,'browserErrors':0,'teardown':'DELETED'}))
