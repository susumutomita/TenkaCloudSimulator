"""KMS console uses actual native HTTP; requires root-integrated KMS navigation/assets."""
import base64,json,os,re,shutil,sys
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,REPORTS,sync_playwright,expect,boto3,Config
from reflow import assert_reflow

axe=os.environ.get('AXE_SCRIPT')
assert axe and Path(axe).is_file(),'Canonical AXE_SCRIPT is required; do not silently skip accessibility checks'
with running(authenticated=True) as (endpoint,operator),sync_playwright() as p:
 kms=boto3.client('kms',endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
 kms.meta.events.register('before-sign.kms',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',operator))
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
 context=browser.new_context(viewport={'width':1440,'height':1000})
 # Trusted test instrumentation via DevTools; application CSP stays enforced.
 context.add_init_script(path=axe)
 page=context.new_page();page.set_default_timeout(10000);errors=[];outside=[]
 page.on('pageerror',lambda e:errors.append(str(e)))
 def route(r):
  if r.request.url.startswith(endpoint+'/'):r.continue_()
  else:outside.append(r.request.url);r.abort()
 context.route('**/*',route)
 page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(operator);page.get_by_role('button',name='Open sandbox',exact=True).click()
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='KMS',exact=True).click()
 page.get_by_role('button',name='Create key',exact=True).first.click();page.get_by_label('Description',exact=True).fill('Cancelled dummy key');page.get_by_role('button',name='Cancel',exact=True).click();expect(page.get_by_role('heading',name='Customer managed keys (0)',exact=True)).to_be_visible();assert kms.list_keys()['Keys']==[]
 page.get_by_role('button',name='Create key',exact=True).first.click();page.get_by_label('Description',exact=True).fill('Dummy browser key');page.screenshot(path=str(REPORTS/'kms-create-1440.png'),full_page=True,animations='disabled');page.get_by_role('button',name='Create key',exact=True).click()
 expect(page.get_by_role('heading',name='General configuration',exact=True)).to_be_visible();page.screenshot(path=str(REPORTS/'kms-key-1440.png'),full_page=True,animations='disabled')
 keys=kms.list_keys()['Keys'];assert len(keys)==1;key_id=keys[0]['KeyId'];assert kms.describe_key(KeyId=key_id)['KeyMetadata']['Description']=='Dummy browser key'
 page.get_by_role('tab',name='Key policy',exact=True).click();page.get_by_role('button',name='View default key policy',exact=True).click();expect(page.get_by_label('Default key policy (read only)',exact=True)).to_have_value(re.compile('Enable IAM User Permissions'));page.screenshot(path=str(REPORTS/'kms-policy-1440.png'),full_page=True,animations='disabled')
 page.get_by_role('tab',name='Local encryption test',exact=True).click();page.get_by_label('Encryption context',exact=True).fill('{"purpose":"demo"}');page.get_by_label('Plaintext',exact=True).fill('Dummy browser text');page.get_by_role('button',name='Encrypt',exact=True).click();expect(page.get_by_label('Ciphertext blob',exact=True)).not_to_have_value('')
 assert kms.decrypt(CiphertextBlob=base64.b64decode(page.get_by_label('Ciphertext blob',exact=True).input_value()),EncryptionContext={'purpose':'demo'})['Plaintext']==b'Dummy browser text'
 page.get_by_role('button',name='Decrypt',exact=True).click();expect(page.get_by_label('Operation result',exact=True)).to_have_value('Dummy browser text')
 page.get_by_label('Encryption context',exact=True).fill('{"purpose":"wrong"}');page.get_by_role('button',name='Decrypt',exact=True).click();expect(page.get_by_text('400: The local ciphertext is invalid for this request.',exact=True)).to_be_visible()
 page.get_by_label('Encryption context',exact=True).fill('{"purpose":"demo"}');page.get_by_role('button',name='Disable',exact=True).click();expect(page.get_by_role('button',name='Encrypt',exact=True)).to_be_disabled();expect(page.get_by_role('button',name='Decrypt',exact=True)).to_be_disabled();assert kms.describe_key(KeyId=key_id)['KeyMetadata']['KeyState']=='Disabled';page.get_by_role('button',name='Enable',exact=True).click();expect(page.get_by_role('button',name='Decrypt',exact=True)).to_be_enabled();assert kms.describe_key(KeyId=key_id)['KeyMetadata']['KeyState']=='Enabled';page.get_by_role('button',name='Decrypt',exact=True).click();expect(page.get_by_label('Operation result',exact=True)).to_have_value('Dummy browser text')
 # Real keyboard activation, focus, mobile reflow and full serious/critical axe set.
 for width,height in [(1440,1000),(390,844)]:
  page.set_viewport_size({'width':width,'height':height});assert_reflow(page);
  if width==390:
   scroll=page.get_by_role('button',name='Scroll key views right',exact=True)
   if scroll.is_enabled():scroll.click()
   expect(page.get_by_role('tab',name='Local encryption test',exact=True)).to_be_in_viewport(ratio=1)
  page.get_by_label('Encryption context',exact=True).fill('{}');page.get_by_label('Encryption context',exact=True).fill('{"purpose":"demo"}');expect(page.get_by_label('Operation result',exact=True)).to_have_count(0);page.get_by_role('button',name='Decrypt',exact=True).focus();page.keyboard.press('Enter');expect(page.get_by_label('Operation result',exact=True)).to_have_value('Dummy browser text');page.screenshot(path=str(REPORTS/f'kms-{width}.png'),full_page=True,animations='disabled')
  audit=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})");assert not [v for v in audit['violations'] if v['impact'] in ['serious','critical']],audit['violations']
 page.get_by_role('button',name='Customer managed keys',exact=True).click();expect(page.get_by_role('heading',name='Customer managed keys (1)',exact=True)).to_be_visible()
 page.screenshot(path=str(REPORTS/'kms-keys-390.png'),full_page=True,animations='disabled');page.get_by_role('button',name='More',exact=True).click();page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('button',name='us-west-2',exact=True).click();expect(page.get_by_text('No customer managed keys',exact=True)).to_be_visible();expect(page.get_by_label('Plaintext',exact=True)).to_have_count(0)
 assert not errors and not outside,(errors,outside)
 browser.close()
 print(json.dumps({'nativeCreatePolicyEncryptDecrypt':True,'cancelCreatesNothing':True,'SDKConfirmsUIState':True,'wrongContextRejected':True,'disabledGuard':True,'keyboard':True,'PC390AxeReflow':True,'regionRemount':True,'outside':0,'pageerrors':0,'cleanup':'temporary process/DB removed; key scheduled deletion unsupported'}))
