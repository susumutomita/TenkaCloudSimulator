"""Native create/edit/deploy/invoke with bundled CodeMirror; no external editor service."""
import os
import shutil
import json
import urllib.request
from check import REPORTS, expect, running, sync_playwright, assert_reflow
def control(endpoint, token, path, body):
    req=urllib.request.Request(endpoint+path,data=json.dumps(body).encode(),method='PUT',headers={'Content-Type':'application/json','X-Simulator-Token':token})
    with urllib.request.urlopen(req,timeout=10) as response:return json.load(response)

# This entry point deliberately needs the real browser/server environment. Unit
# harness results cannot stand in for this contract.
with running(authenticated=True) as (endpoint, operator), sync_playwright() as pw:
    control(endpoint, operator, '/_simulator/v1/iam/principals/editor-executor/policy',
            {'Version':'2012-10-17','Statement':[{'Effect':'Deny','Action':'*','Resource':'*'}]})
    browser=pw.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
    context=browser.new_context(viewport={'width':1440,'height':1000})
    assert context.request.post(endpoint+'/_simulator/login',form={'token':operator},max_redirects=0).status==303
    page=context.new_page();errors=[];outside=[]
    page.on('pageerror',lambda error:errors.append(str(error)))
    page.add_init_script("window.cspViolations=[];document.addEventListener('securitypolicyviolation',e=>window.cspViolations.push({directive:e.violatedDirective,blocked:e.blockedURI,source:e.sourceFile,line:e.lineNumber,column:e.columnNumber}));")
    def guard(route):
        if route.request.url.startswith(endpoint+'/'):route.continue_()
        else:outside.append(route.request.url);route.abort()
    context.route('**/*',guard)
    def assert_csp(stage):
        violations=page.evaluate('window.cspViolations')
        assert not violations,{'stage':stage,'violations':violations,'styles':page.evaluate("Array.from(document.querySelectorAll('style')).map(s=>({nonceMatches:!!s.nonce&&s.nonce===document.querySelector('meta[name=editor-style-nonce]')?.content,textStart:s.textContent.slice(0,100)}))")}
    page.goto(endpoint+'/_simulator/console/#/lambda')
    assert_csp('console loaded')
    page.get_by_role('button',name='Create function',exact=True).first.click()
    page.get_by_role('textbox',name='Function name',exact=True).fill('inline-demo')
    page.get_by_role('textbox',name='Execution role ARN',exact=True).fill('arn:aws:iam::000000000000:role/editor-executor')
    def edit(source):
        editor=page.locator('.lambda-python-editor .cm-content').first
        editor.click();editor.press('ControlOrMeta+A');page.keyboard.insert_text(source)
    assert_csp('create editor mounted')
    edit('def lambda_handler(event, context):\n    return {"answer": 41}\n')
    page.get_by_role('button',name='Create function',exact=True).click()
    expect(page.get_by_text('Deployed code',exact=True)).to_be_visible()
    edit('def lambda_handler(event, context):\n    print("editor-output")\n    return {"answer": 42}\n')
    page.get_by_role('tab',name='Test',exact=True).click()
    expect(page.get_by_role('button',name='Test',exact=True)).to_be_disabled()
    page.get_by_role('tab',name='Code',exact=True).click()
    page.get_by_role('button',name='Deploy code',exact=True).click()
    expect(page.get_by_text('Deployed code',exact=True)).to_be_visible()
    assert not page.evaluate('window.cspViolations'),page.evaluate('window.cspViolations')
    assert_csp('deployed before screenshots')
    for width in (1440,390):
        page.set_viewport_size({'width':width,'height':1000});assert_reflow(page)
        page.screenshot(path=str(REPORTS/f'lambda-inline-{width}.png'),full_page=True,caret='initial',animations='allow')
    assert_csp('screenshots complete')
    page.get_by_role('tab',name='Test',exact=True).click()
    page.get_by_role('checkbox',name='Include log tail for this test',exact=True).check()
    page.get_by_role('button',name='Test',exact=True).click()
    expect(page.get_by_label('Response',exact=True)).to_have_value('{\n  \"answer\": 42\n}')
    expect(page.get_by_label('Log output (last 4 KiB)',exact=True)).to_have_value('editor-output\n\n')
    assert not page.evaluate('window.cspViolations'),page.evaluate('window.cspViolations')
    page.reload()
    page.get_by_role('link',name='inline-demo',exact=True).click()
    page.get_by_role('tab',name='Code',exact=True).click()
    expect(page.get_by_role('button',name='Deploy code',exact=True)).to_have_count(0)
    expect(page.get_by_text('Source download is not supported yet.',exact=False)).to_be_visible()
    assert not errors,errors
    assert not outside,outside
    assert not page.evaluate('window.cspViolations'),page.evaluate('window.cspViolations')
    browser.close()
