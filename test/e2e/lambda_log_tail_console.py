"""Explicit native Lambda log tails in the console; loopback dummy fixtures only."""
import io
import json
import os
import shutil
import time
import urllib.request
import zipfile
from pathlib import Path

from check import Config, REPORTS, assert_reflow, boto3, expect, running, sync_playwright


def control(endpoint, token, path, body):
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(),
        method='PUT' if path.endswith('/policy') else 'POST',
        headers={'Content-Type':'application/json', 'X-Simulator-Token':token})
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)


with running(authenticated=True) as (endpoint, operator), sync_playwright() as playwright:
    client = boto3.client('lambda', endpoint_url=endpoint, region_name='us-east-1',
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy', config=Config(retries={'max_attempts':0}))
    client.meta.events.register('before-sign.lambda', lambda request, **kwargs:
        request.headers.add_header('X-Simulator-Token', operator))
    control(endpoint, operator, '/_simulator/v1/iam/principals/tail-executor/policy',
        {'Version':'2012-10-17','Statement':[{'Effect':'Deny','Action':'*','Resource':'*'}]})
    archive=io.BytesIO()
    with zipfile.ZipFile(archive,'w') as zipped:
        zipped.writestr('index.py', '''def handler(event, context):
    print("dummy-tail-日本語-<script>not executable</script>", flush=True)
    if event.get("fail"):
        raise ValueError("dummy failure")
    return {"ok": True}
''')
    created=client.create_function(FunctionName='console-tail', Runtime='python3.12', Handler='index.handler',
        Role='arn:aws:iam::000000000000:role/tail-executor', Timeout=3, Code={'ZipFile':archive.getvalue()})
    browser=playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
    context=browser.new_context(viewport={'width':1440,'height':1000})
    assert context.request.post(endpoint+'/_simulator/login',form={'token':operator},max_redirects=0).status==303
    page=context.new_page();errors=[];outside=[];invocations=[]
    page.on('pageerror',lambda error:errors.append(str(error)))
    def guard(route):
        if route.request.url.startswith(endpoint+'/'):route.continue_()
        else:outside.append(route.request.url);route.abort()
    context.route('**/*',guard)
    page.on('request',lambda request:invocations.append(request) if request.url.endswith('/invocations') else None)
    page.goto(endpoint+'/_simulator/console/#/lambda')
    page.get_by_role('link',name='console-tail',exact=True).click()
    logs=page.get_by_label('Log output (last 4 KiB)',exact=True)
    include=page.get_by_role('checkbox',name='Include log tail for this test',exact=True)
    expect(include).not_to_be_checked()
    page.get_by_role('button',name='Test',exact=True).click()
    expect(page.get_by_text('Execution succeeded',exact=True)).to_be_visible(timeout=20000)
    assert 'x-amz-log-type' not in invocations[-1].headers
    expect(logs).to_have_count(0)
    assert 'dummy-tail-' not in page.content()
    include.check();page.get_by_role('button',name='Test',exact=True).click()
    expect(logs).to_have_value('dummy-tail-日本語-<script>not executable</script>\n\n',timeout=20000)
    assert invocations[-1].headers.get('x-amz-log-type')=='Tail'
    for width in (1440,390):
        page.set_viewport_size({'width':width,'height':1000});assert_reflow(page)
        page.evaluate(Path(os.environ['AXE_SCRIPT']).read_text())
        audit=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
        assert not audit['violations'],[item['id'] for item in audit['violations']]
        page.screenshot(path=str(REPORTS/f'lambda-log-tail-{width}.png'),full_page=True)
    before_locale=len(invocations)
    page.get_by_role('combobox',name='Language',exact=True).select_option('ja')
    expect(page.get_by_role('checkbox',name='このテストのログ末尾を取得',exact=True)).to_be_checked()
    expect(page.get_by_label('ログ出力（末尾 4 KiB）',exact=True)).to_have_value('dummy-tail-日本語-<script>not executable</script>\n\n')
    for width in (1440,390):
        page.set_viewport_size({'width':width,'height':1000});assert_reflow(page)
        audit=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
        assert not audit['violations'],[item['id'] for item in audit['violations']]
        page.screenshot(path=str(REPORTS/f'lambda-log-tail-ja-{width}.png'),full_page=True)
    assert len(invocations)==before_locale,'Language change must not repeat invocation'
    page.get_by_role('combobox',name='言語',exact=True).select_option('en')
    page.set_viewport_size({'width':1440,'height':1000})
    include.uncheck();expect(logs).to_have_count(0)
    include.check();page.get_by_label('Event JSON',exact=True).fill('{"fail":true}')
    page.get_by_role('button',name='Test',exact=True).click()
    expect(page.get_by_text('Execution failed',exact=True)).to_be_visible(timeout=20000)
    expect(logs).to_have_value('dummy-tail-日本語-<script>not executable</script>\n\n')
    # Invocation-only readers can use the name entry without reading configuration.
    control(endpoint,operator,'/_simulator/v1/iam/principals/tail-reader/policy',{'Version':'2012-10-17','Statement':[
        {'Effect':'Allow','Action':'lambda:InvokeFunction','Resource':created['FunctionArn']}]})
    access=control(endpoint,operator,'/_simulator/v1/access',{'Principal':'tail-reader'})['AccessToken']
    reader=browser.new_context(viewport={'width':1440,'height':1000})
    assert reader.request.post(endpoint+'/_simulator/login',form={'token':access},max_redirects=0).status==303
    reader.route('**/*',guard)
    rp=reader.new_page();rp.on('pageerror',lambda error:errors.append(str(error)))
    rp.goto(endpoint+'/_simulator/console/#/lambda')
    target=rp.get_by_label('Function name or ARN',exact=True);target.fill('console-tail')
    ri=rp.get_by_role('checkbox',name='Include log tail for this test',exact=True)
    ri.check();rp.get_by_role('button',name='Test',exact=True).click()
    rlogs=rp.get_by_label('Log output (last 4 KiB)',exact=True)
    expect(rlogs).to_have_value('dummy-tail-日本語-<script>not executable</script>\n\n',timeout=20000)
    held=[]
    rp.route('**/console-tail/invocations',lambda route:held.append(route))
    rp.get_by_role('button',name='Test',exact=True).click()
    expect(rlogs).to_have_count(0)
    deadline=time.monotonic()+5
    while not held and time.monotonic()<deadline:rp.wait_for_timeout(10)
    assert held,'Delayed invocation did not start'
    with rp.expect_event('requestfailed',predicate=lambda request:request==held[0].request):
        target.fill('different-function')
    expect(ri).not_to_be_checked();expect(rlogs).to_have_count(0)
    for route in held:
        try:route.continue_()
        except Exception:assert route.request.failure is not None
    rp.unroute('**/console-tail/invocations')
    target.fill('console-tail');ri.check()
    control(endpoint,operator,'/_simulator/v1/iam/principals/tail-reader/policy',{'Version':'2012-10-17','Statement':[{'Effect':'Deny','Action':'*','Resource':'*'}]})
    rp.get_by_role('button',name='Test',exact=True).click()
    expect(rp.get_by_text('Could not complete the operation',exact=True)).to_be_visible()
    expect(rlogs).to_have_count(0)
    reader.close()
    # Service navigation destroys the old output and explicit log choice.
    page.get_by_role('button',name='Services',exact=True).click()
    page.get_by_role('menuitem',name='Systems Manager',exact=True).click()
    assert 'dummy-tail-' not in page.content()
    page.go_back();page.get_by_role('link',name='console-tail',exact=True).click()
    expect(page.get_by_role('checkbox',name='Include log tail for this test',exact=True)).not_to_be_checked()
    expect(page.get_by_label('Log output (last 4 KiB)',exact=True)).to_have_count(0)
    assert not errors and not outside,(errors,outside)
    browser.close()
print('Lambda explicit log-tail console acceptance passed')
