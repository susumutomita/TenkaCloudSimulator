"""Drive participant practice in real browsers; never log flags or credentials.
Requires Bun + platform dependencies (README) and scripts/requirements-test.txt.
"""
import json,os,select,shutil,signal,subprocess,time
from pathlib import Path
from playwright.sync_api import sync_playwright,expect
bun=os.environ.get('BUN','bun')
process=subprocess.Popen([bun,'test/platform/hello-world.ts'],env={**os.environ,'SIMULATOR_PRACTICE':'1'},stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
try:
 deadline=time.monotonic()+90;ready=None
 while time.monotonic()<deadline:
  if process.poll() is not None:raise RuntimeError('Practice runner failed: '+process.stderr.read())
  if select.select([process.stdout],[],[],.2)[0]:
   line=process.stdout.readline()
   try:message=json.loads(line)
   except ValueError:continue
   if message.get('practiceReady'):ready=message;break
 assert ready,'Practice did not become ready'
 credentials=[json.loads(Path(file).read_text()) for file in ready['teamFiles']]
 with sync_playwright() as p:
  browser=p.chromium.launch(**({'executable_path':os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')} if os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium') else {}),args=['--no-sandbox'])
  flags=[];portals=[];contexts=[];errors=[];outside=[]
  for index,credential in enumerate(credentials):
   context=browser.new_context(viewport={'width':1440,'height':1000});contexts.append(context)
   allowed=[credential['portalUrl']+'/',credential['consoleUrl'].split('/_simulator')[0]+'/']
   def route(r,request,allowed=allowed):
    if any(r.request.url.startswith(origin) for origin in allowed):r.continue_()
    else:outside.append(r.request.url);r.abort()
   context.route('**/*',route)
   page=context.new_page();page.on('pageerror',lambda error:errors.append(str(error)))
   page.goto(credential['consoleUrl']);expect(page.get_by_label('Access token',exact=True)).to_be_visible();page.get_by_label('Access token',exact=True).fill(credential['consoleToken']);page.get_by_role('button',name='Open sandbox',exact=True).click()
   # Login redirects to S3; service selection uses the real top navigation.
   page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Systems Manager',exact=True).click()
   page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='ap-northeast-1',exact=True).click()
   parameter=page.get_by_role('table').get_by_role('link').first;expect(parameter).to_be_visible();parameter.click();expect(page.get_by_role('tab',name='Overview',exact=True)).to_be_visible();flags.append(page.get_by_label('Value',exact=True).input_value())
   page.set_viewport_size({'width':390,'height':844});page.wait_for_function('document.documentElement.scrollWidth <= innerWidth');assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
   portal=context.new_page();portal.on('pageerror',lambda error:errors.append(str(error)));portal.goto(credential['portalUrl']);portal.get_by_label('Team key',exact=True).fill(credential['teamKey']);portal.get_by_role('button',name='練習を開く',exact=True).click();expect(portal.locator('#exercise')).to_be_visible();portals.append(portal)
  assert flags[0]!=flags[1]
  def submit(index,value,expected):
   portal=portals[index];portal.get_by_label('Flag',exact=True).fill(value);portal.get_by_role('button',name='提出する',exact=True).click();expect(portal.locator('#result')).to_have_text(expected)
  submit(1,flags[0],'wrong');submit(0,flags[0],'ok');submit(1,flags[1],'ok')
  # The UI reads the actual host state after each submission.
  expect(portals[0].locator('#score')).to_have_text('100');expect(portals[1].locator('#score')).to_have_text('95')
  assert errors==[],errors;assert outside==[],outside
  browser.close()
 print(json.dumps({'participantBrowserFlow':'import/deploy/open SSM/read/submit/score','teams':2,'scores':[100,95],'externalRequests':0,'pageErrors':0,'mobileOverflow':False}))
finally:
 process.send_signal(signal.SIGINT)
 try:output,error=process.communicate(timeout=15)
 except subprocess.TimeoutExpired:process.kill();process.communicate();raise AssertionError('Practice cleanup timed out')
 if process.returncode!=0:raise AssertionError('Practice runner cleanup failed: '+error)
 assert '"DELETED","DELETED"' in output,'Practice did not confirm teardown'
