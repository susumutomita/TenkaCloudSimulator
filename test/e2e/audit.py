import sys,json,os
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,sdk
from playwright.sync_api import sync_playwright,expect
root=Path(os.environ.get('SIMULATOR_AUDIT_REPORTS','reports/audit'));root.mkdir(parents=True,exist_ok=True)
axe_path=Path(os.environ['AXE_SCRIPT']);axe=axe_path.read_text();provenance=json.loads((axe_path.parent/'provenance.json').read_text());import hashlib
assert hashlib.sha256(axe_path.read_bytes()).hexdigest()==provenance['bundleSha256']
(root/'tool-provenance.json').write_text(json.dumps(provenance,indent=2)+'\n')
results=[]
with running() as endpoint,sync_playwright() as p:
 import shutil
 executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
 browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);page=browser.new_page(viewport={'width':1440,'height':1000})
 def capture(name):
  page.screenshot(path=str(root/(name+'.png')),full_page=False)
  (root/(name+'-dom.txt')).write_text(page.locator('body').aria_snapshot())
  page.evaluate(axe);result=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
  results.append({'step':name,'violations':result['violations'],'incomplete':result['incomplete'],'passes':len(result['passes'])})
  print(name,[(v['id'],len(v['nodes'])) for v in result['violations']],flush=True)
 page.goto(endpoint+'/_simulator/console/');expect(page.locator('#empty-title')).to_have_text('No buckets yet');capture('01-empty')
 page.locator('#primary').focus();page.keyboard.press('Enter');expect(page.locator('#bucket-name')).to_be_focused();page.keyboard.press('Escape');expect(page.locator('#primary')).to_be_focused();page.keyboard.press('Enter');expect(page.locator('#modal')).to_be_visible();capture('02-create-form')
 name=page.get_by_label('Bucket name',exact=True);name.fill('UPPER_CASE');assert not name.evaluate('(e)=>e.checkValidity()');name.fill('audit-practice');assert name.evaluate('(e)=>e.checkValidity()');page.locator('#submit').click();expect(page.get_by_role('button',name='audit-practice',exact=True)).to_be_visible();page.get_by_role('button',name='audit-practice',exact=True).click();expect(page.locator('#title')).to_have_text('audit-practice');capture('03-bucket-detail')
 page.locator('#primary').click();page.get_by_label('Choose a file').set_input_files({'name':'hello.txt','mimeType':'text/plain','buffer':b'real object'});capture('04-upload-form')
 page.locator('#submit').click();expect(page.get_by_role('button',name='hello.txt',exact=True)).to_be_visible();page.get_by_role('button',name='Object storage',exact=True).click();page.get_by_label('Select audit-practice',exact=True).check();page.locator('#delete').click();page.get_by_label('Type audit-practice to confirm',exact=True).fill('audit-practice');page.locator('#submit').click();expect(page.locator('#modal-error')).to_contain_text('BucketNotEmpty');capture('05-delete-error')
 page.keyboard.press('Escape');expect(page.locator('#modal')).not_to_be_visible();page.set_viewport_size({'width':390,'height':844});capture('06-mobile');assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
 page.set_viewport_size({'width':720,'height':1000});capture('07-reflow');assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
 page.locator('#scope-button').click();example=page.locator('#cli-example').inner_text();assert '\nexport AWS_SECRET_ACCESS_KEY' in example and '\\n' not in example
 page.locator('#scope-policies').click();expect(page.locator('#modal-title')).to_have_text('Identity policy');assert page.locator('#policy-principal').is_visible()
 browser.close()
(root/'axe-results.json').write_text(json.dumps(results,indent=2)+'\n')

assert all(not step['violations'] for step in results),'Accessibility violations found; inspect reports/audit/axe-results.json'
print('Seven screens captured; keyboard focus/escape, mobile reflow, and automated WCAG rules passed. This is not a full accessibility certification.')
