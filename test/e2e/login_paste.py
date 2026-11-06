"""Actual local clipboard/login acceptance, never a Mac/Dia reproduction claim.

Uses only host-generated temporary tokens. A native target paste-cancellation
fixture is artificial interference, not evidence of the user's browser cause.
No fill(), clipboard HTML interpretation, raw token reports, or auth bypass.
"""
import hashlib,json,os,secrets,shutil,sys,time
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,sync_playwright,expect,REPORTS
from reflow import assert_reflow

with running(authenticated=True) as (endpoint,token),sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE')or shutil.which('chromium'),args=['--no-sandbox','--renderer-process-limit=2'])
 contexts=[];errors=[];outside=[];report={'realClipboard':True,'MacDiaVerified':False,'actualUserKeysRead':False,'tokenValuesReported':False}
 def fresh():
  context=browser.new_context(viewport={'width':1440,'height':1000},permissions=['clipboard-read','clipboard-write']);contexts.append(context);page=context.new_page();page.set_default_timeout(5000);page.on('pageerror',lambda e:errors.append(type(e).__name__))
  def guard(route):
   if route.request.url.startswith(endpoint+'/'):route.continue_()
   else:outside.append('external origin');route.abort()
  context.add_init_script("""window.__pasteEvents=[];document.addEventListener('paste',e=>window.__pasteEvents.push({trusted:e.isTrusted,plainAvailable:!!e.clipboardData?.getData('text/plain'),types:[...(e.clipboardData?.types||[])]}),true);""")
  context.route('**/*',guard);page.goto(endpoint+'/_simulator/login');expect(page.get_by_label('Access token',exact=True)).to_have_attribute('type','password');return page
 def field(page):return page.get_by_label('Access token',exact=True)
 def paste(page,value,select_all=True):
  page.evaluate('(value)=>navigator.clipboard.writeText(value)',value);field(page).focus()
  if select_all:page.keyboard.press('Control+A')
  before=page.evaluate('window.__pasteEvents.length');page.keyboard.press('Control+V')
  if value:assert page.evaluate('(before)=>window.__pasteEvents.slice(before).some(e=>e.trusted&&e.plainAvailable)',before),'Missing actual trusted plain clipboard event'
 def check_value(page,expected):
  for _ in range(150):
   if field(page).input_value()==expected:return
   time.sleep(0.01)
  raise AssertionError('Clipboard/input value mismatch; values redacted')
 def submit(page,want,keyboard=False):
  with page.expect_response(lambda r:r.request.method=='POST'and r.url==endpoint+'/_simulator/login') as pending:
   if keyboard:field(page).press('Enter')
   else:page.get_by_role('button',name='Open sandbox',exact=True).click()
  assert pending.value.status==want,'Unexpected actual native login status'
 def success(page):
  expect(page.get_by_role('button',name='Account menu',exact=True)).to_be_visible();assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==200
 # First-party DOM-only interoperability fixtures, NOT real-extension proof.
 # Use only this fixture's fresh dummy token, never a user's clipboard/file.
 page=fresh();access=field(page)
 assert access.get_attribute('data-1p-ignore')=='true' and access.get_attribute('data-op-ignore')=='true'
 def native_write(page,value):
  field(page).evaluate('(input,value)=>Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,"value").set.call(input,value)',value)
 native_write(page,token);submit(page,303,True);success(page)
 report['nativeDOMWriteWithoutChangeEventAuthenticates']=True
 page=fresh();paste(page,token);native_write(page,'');posts=[]
 page.on('request',lambda request:posts.append(True)if request.method=='POST'and request.url==endpoint+'/_simulator/login'else None)
 field(page).press('Enter')
 expect(page.get_by_text('Enter your access token. If paste left this blank, copy the token as plain text.',exact=True)).to_be_visible()
 check_value(page,'') # Error rerender must not restore the old controlled value.
 assert not posts and page.request.get(endpoint+'/_simulator/v1/capabilities').status==401
 report['nativeDOMClearDoesNotSubmitStaleToken']=True
 page=fresh();native_write(page,token);field(page).focus();page.get_by_role('button',name='日本語',exact=True).click()
 expect(page.get_by_label('アクセストークン',exact=True)).to_have_value(token)
 page.get_by_role('button',name='English',exact=True).click();check_value(page,token);submit(page,303);success(page)
 report['nativeDOMValueSurvivesBlurAndLocaleRerender']=True
 report['actual1PasswordExtensionVerified']=False
 page=fresh();paste(page,token);check_value(page,token);page.keyboard.press('Control+Z');check_value(page,'');report['normalNativeUndo']=True
 # Partial selection replacement must preserve the unaffected prefix.
 before=token[:32]+'x'*32;page.keyboard.type(before);page.keyboard.press('End')
 for _ in range(32):page.keyboard.press('Shift+ArrowLeft')
 paste(page,token[32:],False);check_value(page,token);page.keyboard.press('Control+Z');check_value(page,before);report['selectedReplacementUndo']=True
 paste(page,token+'\r\n');check_value(page,token);submit(page,303,True);success(page)
 page.get_by_role('button',name='Account menu',exact=True).click();page.get_by_role('menuitem',name='Sign out',exact=True).click();expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==401
 paste(page,token);submit(page,303,True);success(page);report['actualValidKeyboardLoginLogoutRelogin']=True
 # Real plaintext clipboard event; only native default insertion is prevented.
 for stop_bubbling in (False,True):
  page=fresh();field(page).evaluate('(input,stop)=>input.addEventListener("paste",e=>{e.preventDefault();if(stop)e.stopPropagation()},{once:true})',stop_bubbling);paste(page,token);check_value(page,token);submit(page,303,True);success(page)
 report['artificialCanceledNativeAndStoppedBubbleFallback']=True
 # Pause timers only for this race fixture, not server/native clocks or auth.
 page=fresh();page.clock.install();page.clock.pause_at('2030-01-01T00:00:00Z');field(page).evaluate('input=>input.addEventListener("paste",e=>e.preventDefault())');paste(page,token);page.keyboard.type('newer');page.clock.run_for(1);expect(field(page)).to_have_value('newer');report['newerTypingInvalidatesPendingPaste']=True
 page=fresh();page.clock.install();page.clock.pause_at('2030-01-01T00:00:00Z');field(page).evaluate('input=>input.addEventListener("paste",e=>e.preventDefault())');paste(page,secrets.token_hex(32));paste(page,token);page.clock.run_for(1);check_value(page,token);report['newerPasteInvalidatesOlderPaste']=True
 # A later real HTML-only paste must cancel an older pending plain paste.
 page=fresh();page.clock.install();page.clock.pause_at('2030-01-01T00:00:00Z');field(page).evaluate('input=>input.addEventListener("paste",e=>e.preventDefault(),{once:true})');paste(page,token)
 page.evaluate("async value=>navigator.clipboard.write([new ClipboardItem({'text/html':new Blob(['<b>'+value+'</b>'],{type:'text/html'})})])",token);field(page).focus();page.keyboard.press('Control+V');page.clock.run_for(1);check_value(page,'');report['actualHTMLPasteInvalidatesOlderPendingPlainPaste']=True
 # ClipboardEvent without clipboardData is synthetic, explicitly not OS paste.
 page=fresh();page.clock.install();page.clock.pause_at('2030-01-01T00:00:00Z');field(page).evaluate('input=>input.addEventListener("paste",e=>e.preventDefault(),{once:true})');paste(page,token);field(page).evaluate('input=>input.dispatchEvent(new ClipboardEvent("paste",{bubbles:true,cancelable:true}))');page.clock.run_for(1);check_value(page,'');report['syntheticNullClipboardDataInvalidatesPendingSafely']=True
 # Enter is pressed before the paused next-task fallback can execute.
 page=fresh();page.clock.install();page.clock.pause_at('2030-01-01T00:00:00Z');field(page).evaluate('input=>input.addEventListener("paste",e=>e.preventDefault(),{once:true})');paste(page,token);submit(page,303,True);success(page);report['immediateKeyboardSubmitFlushesPendingPaste']=True

 page=fresh();page.clock.install();page.clock.pause_at('2030-01-01T00:00:00Z');field(page).evaluate('input=>input.addEventListener("paste",e=>e.preventDefault())');paste(page,token);page.goto(endpoint+'/_simulator/login');check_value(page,'');report['navigationDiscardsPendingPaste']=True
 for name,value in [('invalid',secrets.token_hex(32)),('spaces',' '+token+' '),('label','Operator token: '+token),('escape','\x1b'+token)]:
  page=fresh();paste(page,value);check_value(page,value);submit(page,401);expect(page.get_by_text('Invalid or expired access token',exact=True)).to_be_visible();assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==401
 report['invalidSpacesLabelsEscapeRemainRejected']=True
 page=fresh();paste(page,'');check_value(page,'');page.get_by_role('button',name='Open sandbox',exact=True).click();expect(page.get_by_text('Enter your access token. If paste left this blank, copy the token as plain text.',exact=True)).to_be_visible();assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==401;report['emptyClipboardNoAuthentication']=True
 page=fresh();page.evaluate("async value=>navigator.clipboard.write([new ClipboardItem({'text/html':new Blob(['<b>'+value+'</b>'],{type:'text/html'})})])",token);field(page).focus();page.keyboard.press('Control+V');check_value(page,'');expect(page.get_by_text('The clipboard has no plain-text token. Copy the token as plain text and paste again.',exact=True)).to_be_visible();assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==401;report['HTMLOnlyNotInterpretedAndExplained']=True
 for failure in ('network',403,429,500):
  page=fresh();paste(page,token)
  def refuse(route):
   if failure=='network':route.abort('failed')
   else:route.fulfill(status=failure,content_type='text/plain',body='DO_NOT_REFLECT_SERVER_BODY')
  page.route(endpoint+'/_simulator/login',refuse);page.get_by_role('button',name='Open sandbox',exact=True).click()
  message='Could not reach this simulator. Check the connection and try again.'if failure=='network'else f'Sign-in failed (HTTP {failure}). Try again or contact your host.'
  expect(page.get_by_text(message,exact=True)).to_be_visible();expect(page.get_by_text('DO_NOT_REFLECT_SERVER_BODY',exact=True)).to_have_count(0);assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==401;page.unroute(endpoint+'/_simulator/login',refuse);submit(page,303);success(page)
 report['SafeHTTPAndNetworkErrorsRecover']=True
 # Retain the actual native303 response until the busy-button/Enter guard is checked.
 page=fresh();paste(page,token);held=[]
 def hold(route):
  actual=route.fetch(max_redirects=0);assert actual.status==303;held.append((route,actual))
 page.route(endpoint+'/_simulator/login',hold);page.get_by_role('button',name='Open sandbox',exact=True).click();expect(page.get_by_role('button',name='Open sandbox',exact=True)).to_be_disabled();page.keyboard.press('Enter');assert len(held)==1;held[0][0].fulfill(response=held[0][1]);success(page);report['BusySubmitHasOneNativeRequest']=True
 page=fresh();axe=Path(os.environ['AXE_SCRIPT']);provenance=json.loads((axe.parent/'provenance.json').read_text());assert hashlib.sha256(axe.read_bytes()).hexdigest()==provenance['bundleSha256'];audits=[]
 for width in (1440,390):
  page.set_viewport_size({'width':width,'height':1000});assert_reflow(page);page.evaluate(axe.read_text());violations=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")['violations'];assert not violations,'Login axe violations';page.screenshot(path=str(REPORTS/f'login-paste-{width}.png'),full_page=True,animations='disabled');audits.append(width)
 report.update(axeWidths=audits,pageErrors=len(errors),externalRequests=len(outside),cleanup='temporary process/DB/browser contexts removed; no real user token used')
 assert not errors and not outside
 for context in contexts:context.close()
 browser.close()
(REPORTS/'login-paste.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
