"""KMS console uses actual native HTTP; requires root-integrated KMS navigation/assets."""
import json,os,re,shutil,sys
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,REPORTS,sync_playwright,expect
from reflow import assert_reflow

axe=os.environ.get('AXE_SCRIPT')
assert axe and Path(axe).is_file(),'Canonical AXE_SCRIPT is required; do not silently skip accessibility checks'
with running(authenticated=True) as (endpoint,operator),sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
 context=browser.new_context(viewport={'width':1440,'height':1000},bypass_csp=True)
 page=context.new_page();errors=[];outside=[]
 page.on('pageerror',lambda e:errors.append(str(e)))
 def route(r):
  if r.request.url.startswith(endpoint+'/'):r.continue_()
  else:outside.append(r.request.url);r.abort()
 context.route('**/*',route)
 page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(operator);page.get_by_role('button',name='Open sandbox',exact=True).click()
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='KMS',exact=True).click()
 page.get_by_role('button',name='Create key',exact=True).first.click();page.get_by_label('Description',exact=True).fill('Dummy browser key');page.get_by_role('button',name='Create key',exact=True).click()
 expect(page.get_by_role('heading',name='General configuration',exact=True)).to_be_visible()
 page.get_by_role('tab',name='Key policy',exact=True).click();page.get_by_role('button',name='View default key policy',exact=True).click();expect(page.get_by_label('Default key policy (read only)',exact=True)).to_have_value(re.compile('Enable IAM User Permissions'))
 page.get_by_role('tab',name='Local encryption test',exact=True).click();page.get_by_label('Encryption context',exact=True).fill('{"purpose":"demo"}');page.get_by_label('Plaintext',exact=True).fill('Dummy browser text');page.get_by_role('button',name='Encrypt',exact=True).click();expect(page.get_by_label('Ciphertext blob',exact=True)).not_to_have_value('')
 page.get_by_role('button',name='Decrypt',exact=True).click();expect(page.get_by_label('Operation result',exact=True)).to_have_value('Dummy browser text')
 page.get_by_label('Encryption context',exact=True).fill('{"purpose":"wrong"}');page.get_by_role('button',name='Decrypt',exact=True).click();expect(page.get_by_role('alert').filter(has_text='ciphertext is invalid')).to_be_visible()
 page.get_by_label('Encryption context',exact=True).fill('{"purpose":"demo"}');page.get_by_role('button',name='Disable',exact=True).click();expect(page.get_by_role('button',name='Encrypt',exact=True)).to_be_disabled();expect(page.get_by_role('button',name='Decrypt',exact=True)).to_be_disabled();page.get_by_role('button',name='Enable',exact=True).click();page.get_by_role('button',name='Decrypt',exact=True).click();expect(page.get_by_label('Operation result',exact=True)).to_have_value('Dummy browser text')
 # Real keyboard activation, focus, mobile reflow and full serious/critical axe set.
 for width,height in [(1440,1000),(390,844)]:
  page.set_viewport_size({'width':width,'height':height});assert_reflow(page);page.get_by_role('button',name='Decrypt',exact=True).focus();page.keyboard.press('Enter');expect(page.get_by_label('Operation result',exact=True)).to_have_value('Dummy browser text');page.screenshot(path=str(REPORTS/f'kms-{width}.png'),full_page=True,animations='disabled')
  page.add_script_tag(path=axe);audit=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})");assert not [v for v in audit['violations'] if v['impact'] in ['serious','critical']],audit['violations']
 page.get_by_role('button',name='Customer managed keys',exact=True).click();expect(page.get_by_role('heading',name='Customer managed keys',exact=True)).to_be_visible()
 page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='us-west-2',exact=True).click();expect(page.get_by_text('No customer managed keys',exact=True)).to_be_visible();expect(page.get_by_label('Plaintext',exact=True)).to_have_count(0)
 assert not errors and not outside,(errors,outside)
 browser.close()
 print(json.dumps({'nativeCreatePolicyEncryptDecrypt':True,'wrongContextRejected':True,'disabledGuard':True,'keyboard':True,'PC390AxeReflow':True,'regionRemount':True,'outside':0,'pageerrors':0,'cleanup':'temporary process/DB removed; key scheduled deletion unsupported'}))
