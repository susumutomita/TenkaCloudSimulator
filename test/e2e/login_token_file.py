"""User-selected bounded token-file + strict real auth; dummy files only.

Optional LOGIN_TEST_BUNDLE overlays ONLY first-party login JS/CSS for focused
pre-integration execution. This is explicitly not a final embedded-console run.
No clipboard permissions are granted in TEST contexts, no real Mac/user keys.
"""
import hashlib,json,os,secrets,shutil,sys,tempfile,time
from pathlib import Path
from urllib.parse import parse_qs,urlsplit
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,sync_playwright,expect,REPORTS
from reflow import assert_reflow

bundle=Path(os.environ['LOGIN_TEST_BUNDLE']) if os.environ.get('LOGIN_TEST_BUNDLE') else None
if bundle:assert bundle.is_file()and bundle.with_suffix('.css').is_file(),'Selected private bundle missing; no fake fallback'
with tempfile.TemporaryDirectory(prefix='login-file-dummy-') as folder,running(authenticated=True) as (endpoint,token),sync_playwright() as p:
 files=Path(folder);browser=p.chromium.launch(executable_path=shutil.which('chromium'),args=['--no-sandbox','--renderer-process-limit=1']);contexts=[];errors=[];outside=[];login_posts=[]
 report={'browserVersion':browser.version,'realUserSelectedDummyFiles':True,'privateLoginAssetOverlay':bool(bundle),'TESTcontextPermissionsOverridden':False,'MacDiaVerified':False,'tokenValuesReported':False}
 def fixture(name,data):
  path=files/name;path.write_bytes(data);return path
 valid=fixture('simulator-token',(token+'\n').encode());valid_crlf=fixture('token.txt',(token+'\r\n').encode())
 def fresh():
  context=browser.new_context(viewport={'width':1440,'height':1000});contexts.append(context);page=context.new_page();page.set_default_timeout(5000);page.on('pageerror',lambda e:errors.append(type(e).__name__))
  def route(r):
   if not r.request.url.startswith(endpoint+'/'):outside.append('external-origin');r.abort();return
   if bundle and urlsplit(r.request.frame.url).path=='/_simulator/login':
    for suffix,mime in [('app.js','application/javascript'),('app.css','text/css')]:
     if r.request.url==endpoint+'/_simulator/console/'+suffix:r.fulfill(status=200,content_type=mime,body=(bundle if suffix=='app.js'else bundle.with_suffix('.css')).read_bytes());return
   r.continue_()
  context.route('**/*',route)
  def requested(req):
   assert token not in req.url,'Credential leaked into request URL (redacted)'
   if req.method=='POST'and req.url==endpoint+'/_simulator/login':
    fields=parse_qs(req.post_data or '');assert set(fields)=={'token'};assert req.headers.get('content-type','').startswith('application/x-www-form-urlencoded');login_posts.append(True)
   else:assert token not in (req.post_data or ''),'Credential sent outside sign-in (redacted)'
  page.on('request',requested)
  page.on('filechooser',lambda event:page.evaluate('window.__fileKeyboard.choosers++'))
  page.add_init_script("""window.__fileKeyboard={events:[],choosers:0,submits:0};for(const kind of ['keydown','click'])document.addEventListener(kind,e=>{if(e.target instanceof HTMLInputElement&&e.target.type==='file'&&window.__fileKeyboard.events.length<32)window.__fileKeyboard.events.push({kind,trusted:e.isTrusted,enter:e.key==='Enter',space:e.key===' ',focused:document.activeElement===e.target,type:e.target.type,defaultPrevented:e.defaultPrevented,userActivation:navigator.userActivation.isActive})},true);document.addEventListener('keydown',e=>{if(e.target instanceof HTMLInputElement&&e.target.type==='file'&&window.__fileKeyboard.events.length<32)window.__fileKeyboard.events.push({kind:'keydown-bubble',trusted:e.isTrusted,enter:e.key==='Enter',space:e.key===' ',focused:document.activeElement===e.target,type:e.target.type,defaultPrevented:e.defaultPrevented,userActivation:navigator.userActivation.isActive})});document.addEventListener('submit',()=>window.__fileKeyboard.submits++,true);""")
  page.add_init_script("""(()=>{const read=File.prototype.arrayBuffer;window.__fileReads=0;window.__pendingFiles=[];File.prototype.arrayBuffer=function(){window.__fileReads++;const promise=read.call(this);if(this.name.startsWith('late-'))return promise.then(bytes=>new Promise((resolve,reject)=>window.__pendingFiles.push({resolve:()=>resolve(bytes),reject:()=>reject(Error('DO_NOT_REFLECT_FILE_ERROR'))})));return promise}})();""")
  page.goto(endpoint+'/_simulator/login');expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();return page
 def field(page):return page.locator('input[type=password]')
 def check_value(page,value):
  for _ in range(200):
   if field(page).input_value()==value:return
   time.sleep(.01)
  raise AssertionError('Masked token mismatch; values redacted')
 def choose(page,path):
  with page.expect_file_chooser() as pending:page.locator('button').filter(has_text='Choose token file').click()
  pending.value.set_files(str(path)if path is not None else [])
 def keyboard_diagnostic(page,stage,failure):
  try:
   state=page.evaluate('({focusType:document.activeElement?.type||null,focusTag:document.activeElement?.tagName||null,documentFocused:document.hasFocus(),keyboard:window.__fileKeyboard})')
  except Exception:state={'pageUnavailable':True}
  # No token, input value, file name/path, arbitrary key or request URL is read.
  (REPORTS/'login-token-file-keyboard-failure.json').write_text(json.dumps({'browserVersion':browser.version,'stage':stage,'failureType':type(failure).__name__,'state':state},indent=2)+'\n')
 def choose_keyboard(page,path,key='Enter'):
  try:
   before=page.evaluate('({choosers:window.__fileKeyboard.choosers,submits:window.__fileKeyboard.submits})')
   field(page).focus();page.keyboard.press('Tab');assert page.evaluate('document.activeElement.type==="file"'),'File chooser missing from keyboard order'
   with page.expect_file_chooser(timeout=5000) as pending:page.keyboard.press(key)
   pending.value.set_files(str(path))
   page.evaluate('()=>new Promise(resolve=>setTimeout(resolve,0))')
   after=page.evaluate('window.__fileKeyboard')
   assert after['choosers']==before['choosers']+1,'One keyboard activation must open exactly one chooser'
   assert after['submits']==before['submits'],'File activation must not submit the login form'
  except Exception as failure:
   keyboard_diagnostic(page,key,failure)
   raise AssertionError('Keyboard file chooser failed; safe focus/event diagnostics saved') from None
 def loaded(page):expect(page.get_by_role('status').filter(has_text='Token loaded. Choose Open sandbox to verify it.')).to_be_visible()
 def released(page,kind='resolve'):
  page.evaluate('(kind)=>window.__pendingFiles.shift()[kind]()',kind)
 def submit(page,want):
  with page.expect_response(lambda r:r.request.method=='POST'and r.url==endpoint+'/_simulator/login')as post:page.get_by_role('button',name='Open sandbox',exact=True).click()
  assert post.value.status==want,'Unexpected strict server authentication status'
 page=fresh();page.locator('input[type=file]').evaluate("input=>input.addEventListener('keydown',e=>{if(e.key==='Enter')e.preventDefault()},{once:true})");before=len(login_posts);choose_keyboard(page,valid);loaded(page);check_value(page,token);assert len(login_posts)==before;report['ArtificialCanceledNativeEnterStillOpensOneChooserWithoutSubmit']=True
 page=fresh();before=len(login_posts);choose_keyboard(page,valid,key='Space');loaded(page);check_value(page,token);assert len(login_posts)==before;report['NativeSpaceFileActivationUnchanged']=True
 page=fresh();field(page).focus();page.keyboard.press('Tab');page.locator('input[type=file]').evaluate("input=>input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}))");page.evaluate('()=>new Promise(resolve=>setTimeout(resolve,0))');assert page.evaluate('window.__fileKeyboard.choosers===0&&window.__fileKeyboard.submits===0');report['UntrustedSyntheticEnterCannotOpenChooser']=True
 page=fresh();before=len(login_posts);choose_keyboard(page,valid);loaded(page);check_value(page,token);assert len(login_posts)==before;assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==401;expect(field(page)).to_have_attribute('type','password');assert page.evaluate('document.activeElement!==document.querySelector("input[type=password]")');report['ImportDoesNotAuthenticateUploadOrFocusPassword']=True
 page.get_by_role('button',name='日本語',exact=True).click();expect(page.get_by_role('heading',name='ローカルサンドボックスにサインイン',exact=True)).to_be_visible();check_value(page,token);page.reload();expect(page.get_by_role('heading',name='ローカルサンドボックスにサインイン',exact=True)).to_be_visible();check_value(page,'');page.get_by_role('button',name='English',exact=True).click();choose(page,valid_crlf);loaded(page);check_value(page,token);report['EnglishJapanesePreferenceAndNoCredentialPersistence']=True
 assert page.evaluate('(value)=>!JSON.stringify(localStorage).includes(value)&&!JSON.stringify(sessionStorage).includes(value)',token)
 submit(page,303);expect(page.get_by_role('button',name='Account menu',exact=True)).to_be_visible();assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==200;page.get_by_role('button',name='Account menu',exact=True).click();page.get_by_role('menuitem',name='Sign out',exact=True).click();expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();assert page.request.get(endpoint+'/_simulator/v1/capabilities').status==401;choose(page,valid);loaded(page);submit(page,303);report['RealChooserLoginLogoutRelogin']=True
 bad=[('empty.txt',b''),('short.txt',b'x'*31),('overlength.txt',b'x'*257),('oversize.txt',b'x'*513),('bom.txt',b'\xef\xbb\xbf'+token.encode()),('multiline.txt',(token+'\n\n').encode()),('leading.txt',(' '+token).encode()),('trailing.txt',(token+' ').encode()),('wrong.html',token.encode()),('spoofed.txt',('<b>'+token+'</b>').encode()),('binary.bin',b'\xff'*64)]
 for name,data in bad:
  page=fresh();before=len(login_posts);choose(page,fixture(name,data));expect(page.get_by_text('Token loaded. Choose Open sandbox to verify it.',exact=True)).to_have_count(0);check_value(page,'');assert len(login_posts)==before and page.request.get(endpoint+'/_simulator/v1/capabilities').status==401
 report['WrongMIMEBOMMultilineWhitespaceOversizeBinaryRejected']=True
 for length in (32,256):
  page=fresh();choose(page,fixture('bound.txt',b'x'*length));loaded(page);submit(page,401)
 report['ServerAlphabetLengthBoundsDoNotGrantAuthority']=True
 page=fresh();choose(page,fixture('other.txt',secrets.token_hex(32).encode()));loaded(page);submit(page,401);expect(page.get_by_text('Invalid or expired access token',exact=True)).to_be_visible();report['WrongWellFormedTokenRemains401']=True
 # Delay the genuine POST; do not replace authentication or grant permissions.
 page=fresh();choose(page,valid);loaded(page);held=[];before=len(login_posts)
 page.route(endpoint+'/_simulator/login',lambda route:held.append(route))
 try:
  page.get_by_role('button',name='Open sandbox',exact=True).click()
  for _ in range(200):
   if held:break
   page.wait_for_timeout(10)
  assert len(held)==1 and len(login_posts)==before+1
  expect(page.locator('input[type=file]')).to_be_disabled()
  page.locator('input[type=file]').evaluate('input=>input.click()')
  page.keyboard.press('Enter')
  assert page.evaluate('window.__fileKeyboard.choosers')==1,'Disabled file control opened another chooser'
  assert len(login_posts)==before+1 and page.request.get(endpoint+'/_simulator/v1/capabilities').status==401
 finally:
  for route in held:route.continue_()
 expect(page.get_by_role('button',name='Account menu',exact=True)).to_be_visible();report['BusyRealSubmissionBlocksAdditionalChooserAndEarlyAuthentication']=True
 page=fresh();choose(page,valid);loaded(page);choose(page,None);check_value(page,token);report['EmptySelectionPreservesInput']=True
 late=fixture('late-old.txt',secrets.token_hex(32).encode())
 page=fresh();choose(page,late);page.wait_for_function('window.__pendingFiles.length===1');expect(page.get_by_role('button',name='Open sandbox',exact=True)).to_be_disabled();choose(page,valid);loaded(page);released(page);check_value(page,token);report['NewFileWinsLateOldRead']=True
 page=fresh();choose(page,late);page.wait_for_function('window.__pendingFiles.length===1');field(page).focus();page.keyboard.type('newer');released(page,'reject');check_value(page,'newer');expect(page.get_by_text('Could not read that token file. Choose it again.',exact=True)).to_have_count(0);report['NewTypingIgnoresLateFileError']=True
 # Clipboard seeding is confined to a separate SETUP context. TEST stays default.
 seed=browser.new_context(permissions=['clipboard-read','clipboard-write']);contexts.append(seed);setup=seed.new_page();setup.goto(endpoint+'/_simulator/login');setup.evaluate('(v)=>navigator.clipboard.writeText(v)',token)
 page=fresh();choose(page,late);page.wait_for_function('window.__pendingFiles.length===1');field(page).focus();page.keyboard.press('Control+V');check_value(page,token);released(page,'reject');check_value(page,token);expect(page.get_by_text('Could not read that token file. Choose it again.',exact=True)).to_have_count(0);report['ActualNativePasteInvalidatesFileRead']=True
 page=fresh();choose(page,late);page.wait_for_function('window.__pendingFiles.length===1');page.get_by_role('button',name='日本語',exact=True).click();released(page,'reject');expect(page.get_by_text('トークンファイルを読み取れませんでした。選び直してください。',exact=True)).to_be_visible();page.get_by_role('button',name='English',exact=True).click();expect(page.get_by_text('Could not read that token file. Choose it again.',exact=True)).to_be_visible();report['CurrentFileFailureVisibleAndLanguageDoesNotRemount']=True
 page=fresh();choose(page,late);page.wait_for_function('window.__pendingFiles.length===1');page.goto(endpoint+'/_simulator/login');check_value(page,'');report['NavigationDoesNotKeepSelectedCredential']=True
 page=fresh();axe=Path(os.environ['AXE_SCRIPT']);assert hashlib.sha256(axe.read_bytes()).hexdigest()==json.loads((axe.parent/'provenance.json').read_text())['bundleSha256']
 for width in (1440,390):
  page.set_viewport_size({'width':width,'height':1000});assert_reflow(page);page.evaluate(axe.read_text());violations=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")['violations'];assert not violations,'Login file UI axe violations';page.screenshot(path=str(REPORTS/f'login-token-file-{width}.png'),full_page=True,animations='disabled')
 assert not errors and not outside;report.update(pageErrors=0,externalRequests=0,PC390KeyboardFileChooserAndAxe=True,cleanup='dummy files and owned browser/CLI/DB removed')
 for context in contexts:context.close()
 browser.close()
(REPORTS/'login-token-file.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
