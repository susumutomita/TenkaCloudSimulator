"""Capture actual Cloudscape flows and check keyboard, validation and WCAG rules."""
import sys,json,os,hashlib,shutil
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,sdk,submit
from playwright.sync_api import sync_playwright,expect
root=Path(os.environ.get('SIMULATOR_AUDIT_REPORTS','reports/audit'));root.mkdir(parents=True,exist_ok=True)
axe_path=Path(os.environ['AXE_SCRIPT']);axe=axe_path.read_text();provenance=json.loads((axe_path.parent/'provenance.json').read_text())
assert hashlib.sha256(axe_path.read_bytes()).hexdigest()==provenance['bundleSha256']
(root/'tool-provenance.json').write_text(json.dumps(provenance,indent=2)+'\n')
results=[]
with running() as endpoint,sync_playwright() as p:
 executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
 browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);page=browser.new_page(viewport={'width':1440,'height':1000})
 def capture(name):
  page.screenshot(path=str(root/(name+'.png')),full_page=False,animations='disabled')
  (root/(name+'-dom.txt')).write_text(page.locator('body').aria_snapshot())
  page.evaluate(axe);result=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
  results.append({'step':name,'violations':result['violations'],'incomplete':result['incomplete'],'passes':len(result['passes'])})
  print(name,[(v['id'],len(v['nodes'])) for v in result['violations']],flush=True)
 page.goto(endpoint+'/_simulator/console/');expect(page.get_by_role('heading',name='No buckets yet',exact=True)).to_be_visible();capture('01-empty')
 primary=page.get_by_role('button',name='Create bucket',exact=True).first;primary.focus();page.keyboard.press('Enter');expect(page.get_by_label('Bucket name',exact=True)).to_be_focused();capture('02-create-form')
 name=page.get_by_label('Bucket name',exact=True);name.fill('UPPER_CASE');submit(page);expect(page.locator('[id^=form-error-]')).to_contain_text('DNS-compatible');assert sdk(endpoint).list_buckets()['Buckets']==[]
 name.fill('audit-practice');submit(page);page.get_by_role('link',name='audit-practice',exact=True).click();expect(page.get_by_role('tab',name='Objects',exact=True)).to_be_visible();capture('03-bucket-detail')
 page.get_by_role('button',name='Upload',exact=True).first.click();page.get_by_label('Choose a file').set_input_files({'name':'hello.txt','mimeType':'text/plain','buffer':b'real object'});capture('04-upload-form');submit(page)
 expect(page.get_by_role('link',name='hello.txt',exact=True)).to_be_visible();page.get_by_role('link',name='General purpose buckets',exact=True).click();page.get_by_role('radio',name='Select audit-practice',exact=True).check();page.get_by_role('button',name='Delete',exact=True).click();page.get_by_label('Type audit-practice to confirm',exact=True).fill('audit-practice');submit(page);expect(page.get_by_role('dialog')).to_contain_text('BucketNotEmpty');capture('05-delete-error')
 page.keyboard.press('Escape');expect(page.get_by_role('dialog')).not_to_be_visible();expect(page.get_by_role('button',name='Delete',exact=True)).to_be_focused()
 page.set_viewport_size({'width':390,'height':844});capture('06-mobile');assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
 page.set_viewport_size({'width':720,'height':1000});capture('07-reflow');assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
 page.get_by_role('button',name='Toggle help panel',exact=True).click();expect(page.get_by_role('heading',name='Simulator scope',exact=True)).to_be_visible()
 page.get_by_role('button',name='Manage identity policy',exact=True).click();expect(page.get_by_role('dialog')).to_be_visible();expect(page.get_by_label('Principal name',exact=True)).to_be_visible();page.keyboard.press('Escape');expect(page.get_by_role('dialog')).not_to_be_visible()
 page.set_viewport_size({'width':1440,'height':1000});response=page.request.put(endpoint+'/_simulator/v1/iam/principals/audit-executor/policy',data={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'ssm:GetParameter','Resource':'*'}]});assert response.ok
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Lambda',exact=True).click();expect(page.get_by_role('heading',name='No functions yet',exact=True)).to_be_visible();capture('08-lambda-empty')
 page.get_by_role('button',name='Create function',exact=True).first.click();capture('09-lambda-create');page.get_by_label('Function name',exact=True).fill('cloudscape-lambda-demonstration-with-a-long-function-name');page.get_by_label('Execution role ARN',exact=True).fill('arn:aws:iam::000000000000:role/audit-executor');page.get_by_role('button',name='Create function',exact=True).last.click();expect(page.get_by_role('heading',name='cloudscape-lambda-demonstration-with-a-long-function-name',exact=True)).to_be_visible();page.get_by_role('button',name='Test',exact=True).click();expect(page.get_by_text('Execution succeeded',exact=True)).to_be_visible(timeout=20000);capture('10-lambda-test')
 page.set_viewport_size({'width':390,'height':844});capture('11-lambda-mobile');assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
 page.set_viewport_size({'width':1440,'height':1000});page.get_by_role('tab',name='Code',exact=True).click();capture('12-lambda-zip');page.get_by_role('tab',name='Configuration',exact=True).click();capture('13-lambda-configuration');page.set_viewport_size({'width':390,'height':844});capture('14-lambda-configuration-mobile');assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
 browser.close()
(root/'axe-results.json').write_text(json.dumps(results,indent=2)+'\n')
assert all(not step['violations'] for step in results),'Accessibility violations found; inspect reports/audit/axe-results.json'
print('Fourteen screens captured; keyboard, validation, mobile reflow and automated WCAG rules passed. Not a full accessibility certification.')
