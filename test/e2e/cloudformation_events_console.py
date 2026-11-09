"""Real stack Query API/SQLite -> Cloudscape Events; synthetic loopback data only."""
import json
import os
import shutil
import urllib.parse
import urllib.request
from pathlib import Path

from check import Config, REPORTS, assert_reflow, boto3, expect, running, sync_playwright


def control(endpoint, token, path, body, method='POST'):
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(),
        headers={'Content-Type':'application/json', 'X-Simulator-Token':token}, method=method)
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)


with running(authenticated=True) as (endpoint, operator), sync_playwright() as playwright:
    cfn = boto3.client('cloudformation', endpoint_url=endpoint, region_name='us-east-1',
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy', config=Config(retries={'max_attempts':0}))
    cfn.meta.events.register('before-sign.cloudformation', lambda request, **kwargs:
        request.headers.add_header('X-Simulator-Token', operator))
    template = {'Resources':{f'Parameter{index:02}':{'Type':'AWS::SSM::Parameter',
        'Properties':{'Name':f'/console-events/parameter{index:02}','Type':'String','Value':'event-test-secret-value'}} for index in range(51)},
        'Outputs':{'PrivateResult':{'Value':'event-test-private-output'}}}
    stack_id = cfn.create_stack(StackName='console-events-paged', TemplateBody=json.dumps(template))['StackId']
    other_id = cfn.create_stack(StackName='console-events-other', TemplateBody=json.dumps({'Resources':{
        'Other':{'Type':'AWS::SSM::Parameter','Properties':{'Name':'/console-events/other','Type':'String','Value':'other-private'}}}}))['StackId']
    first = cfn.describe_stack_events(StackName=stack_id)
    second = cfn.describe_stack_events(StackName=stack_id, NextToken=first['NextToken'])
    expected_ids = [event['EventId'] for event in first['StackEvents'] + second['StackEvents']]
    assert len(first['StackEvents']) == 100 and len(expected_ids) == 104 and 'NextToken' not in second
    browser = playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
    context = browser.new_context(viewport={'width':1440,'height':1000})
    assert context.request.post(endpoint+'/_simulator/login', form={'token':operator}, max_redirects=0).status == 303
    page = context.new_page(); errors = []; requests = []; outside = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    def guard(route):
        if route.request.url.startswith(endpoint+'/'): route.continue_()
        else: outside.append(route.request.url); route.abort()
    context.route('**/*', guard)
    page.on('request', lambda request: requests.append(urllib.parse.parse_qs(request.post_data or ''))
        if request.method == 'POST' and request.url == endpoint+'/' else None)
    def open_history(name):
        page.get_by_label('Stack name or ID', exact=True).fill(name)
        page.get_by_role('button', name='Open stack history', exact=True).click()
        expect(page.get_by_role('tab', name='Events', exact=True)).to_have_attribute('aria-selected','true')
    def table(): return page.get_by_role('table', name='Stack events', exact=True)
    def event_ids(): return table().locator('tbody tr td:last-child').all_text_contents()
    page.goto(endpoint+'/_simulator/console/#/cfn')
    page.get_by_role('link', name='console-events-paged', exact=True).click()
    expect(table().locator('tbody tr')).to_have_count(100)
    assert event_ids() == expected_ids[:100]
    assert not any('StackName' in request and request.get('Action') == ['DescribeStacks'] for request in requests)
    assert 'event-test-private-output' not in page.content() and 'event-test-secret-value' not in page.content()
    page.get_by_role('button', name='Load older events', exact=True).click()
    expect(table().locator('tbody tr')).to_have_count(104)
    assert event_ids() == expected_ids
    expect(page.get_by_role('button', name='Load older events', exact=True)).to_have_count(0)
    for width in (1440,390):
        page.set_viewport_size({'width':width,'height':1000}); assert_reflow(page)
        page.evaluate(Path(os.environ['AXE_SCRIPT']).read_text())
        audit = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
        assert not audit['violations'], [item['id'] for item in audit['violations']]
        page.screenshot(path=str(REPORTS/f'cloudformation-events-{width}.png'), full_page=True)
    page.set_viewport_size({'width':1440,'height':1000})
    page.get_by_role('button', name='Refresh stack details', exact=True).click()
    expect(table().locator('tbody tr')).to_have_count(100)
    page.get_by_role('tab', name='Outputs', exact=True).click()
    expect(page.get_by_text('event-test-private-output', exact=True)).to_be_visible()
    assert 'event-test-secret-value' not in page.content()
    page.get_by_role('tab', name='Events', exact=True).click()
    expect(table().locator('tbody tr')).to_have_count(100)
    assert 'event-test-private-output' not in page.content()
    # An in-flight old stack response must not replace a newer stack selection.
    held = []
    def hold(route):
        data = urllib.parse.parse_qs(route.request.post_data or '')
        if data.get('Action') == ['DescribeStackEvents'] and data.get('StackName') == [stack_id]: held.append(route)
        else: route.continue_()
    page.route(endpoint+'/', hold)
    page.get_by_role('button', name='Refresh stack details', exact=True).click()
    page.wait_for_timeout(100)
    assert held, 'The delayed refresh did not start'
    with page.expect_event('requestfailed', predicate=lambda request: request == held[0].request):
        open_history(other_id)
    expect(table().locator('tbody tr')).to_have_count(4)
    for route in held:
        try: route.continue_()
        except Exception:
            # AbortController may already have cancelled this intercepted request.
            assert route.request.failure is not None
    page.unroute(endpoint+'/', hold)
    expect(table().get_by_text('Other', exact=True)).to_have_count(2)
    assert not set(event_ids()) & set(expected_ids)
    # Exact-ARN readers can use lookup even when DescribeStacks is denied.
    # Losing Events permission during pagination must also erase old rows.
    control(endpoint, operator, '/_simulator/v1/iam/principals/event-reader/policy',
        {'Version':'2012-10-17','Statement':[{'Effect':'Allow',
        'Action':'cloudformation:DescribeStackEvents','Resource':stack_id}]}, 'PUT')
    def choose_identity(name):
        page.get_by_role('button', name='Account menu', exact=True).click()
        page.get_by_role('menuitem', name='Choose identity', exact=True).click()
        dialog = page.get_by_role('dialog')
        dialog.get_by_label('Principal name', exact=True).fill(name)
        dialog.get_by_role('button', name='Choose identity', exact=True).click()
        expect(page.get_by_role('tab', name='Events', exact=True)).to_have_count(0)
    choose_identity('event-reader')
    open_history(stack_id); expect(table().locator('tbody tr')).to_have_count(100)
    control(endpoint, operator, '/_simulator/v1/iam/principals/event-reader/policy',
        {'Version':'2012-10-17','Statement':[{'Effect':'Deny','Action':'*','Resource':'*'}]}, 'PUT')
    page.get_by_role('button', name='Load older events', exact=True).click()
    expect(page.locator('[data-analytics-alert="error"]').last).to_be_visible()
    expect(table().get_by_text(expected_ids[0], exact=True)).to_have_count(0)
    expect(page.get_by_role('button', name='Load older events', exact=True)).to_have_count(0)
    choose_identity('simulator-admin')
    open_history(other_id); expect(table().locator('tbody tr')).to_have_count(4)
    # Deleted generations remain addressable by exact ID, but no Outputs exist.
    cfn.delete_stack(StackName=other_id)
    page.get_by_role('button', name='Refresh stack details', exact=True).click()
    expect(table().locator('tbody tr')).to_have_count(8)
    expect(table().get_by_text('DELETE_COMPLETE', exact=True)).to_have_count(2)
    page.get_by_role('tab', name='Outputs', exact=True).click()
    expect(page.locator('[data-analytics-alert="error"]')).to_be_visible()
    page.get_by_role('button', name='Close stack details', exact=True).click()
    expect(page.get_by_role('tab', name='Events', exact=True)).to_have_count(0)
    open_history('does-not-exist')
    expect(page.locator('[data-analytics-alert="error"]')).to_be_visible()
    assert not set(event_ids()) & set(expected_ids)
    # Region navigation unmounts and discards the current detail.
    page.get_by_role('button', name='us-east-1', exact=True).click()
    page.get_by_role('menuitem', name='eu-west-1', exact=True).click()
    expect(page.get_by_role('tab', name='Events', exact=True)).to_have_count(0)
    open_history(stack_id)
    expect(page.locator('[data-analytics-alert="error"]')).to_be_visible()
    page.get_by_role('button', name='eu-west-1', exact=True).click()
    page.get_by_role('menuitem', name='us-east-1', exact=True).click()
    open_history(stack_id); expect(table().locator('tbody tr')).to_have_count(100)
    page.goto(endpoint+'/_simulator/console/#/s3')
    expect(page.get_by_role('tab', name='Events', exact=True)).to_have_count(0)
    page.go_back(); expect(page.get_by_role('tab', name='Events', exact=True)).to_have_count(0)
    page.reload(); expect(page.get_by_role('tab', name='Events', exact=True)).to_have_count(0)
    # A browser participant remains denied even with an Allow-all exercise policy.
    control(endpoint, operator, '/_simulator/v1/iam/principals/event-participant/policy',
        {'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'*','Resource':'*'}]}, 'PUT')
    participant = control(endpoint, operator, '/_simulator/v1/access', {'Principal':'event-participant'})['AccessToken']
    limited = browser.new_context()
    assert limited.request.post(endpoint+'/_simulator/login', form={'token':participant}, max_redirects=0).status == 303
    for action in ('DescribeStacks','DescribeStackEvents'):
        response = limited.request.post(endpoint+'/', form={'Action':action,'Version':'2010-05-15','StackName':stack_id},
            headers={'X-Simulator-Region':'us-east-1'})
        assert response.status == 403
        assert 'event-test-private-output' not in response.text() and expected_ids[0] not in response.text()
    cfn.delete_stack(StackName=stack_id)
    assert not errors, errors
    assert not outside, outside
    browser.close()
    print(json.dumps({'nativeEvents':104,'pagination':True,'outputsExplicit':True,'obsoleteRequestCancelled':True,
        'deletedIDHistory':True,'unknownAndWrongRegionErrors':True,'participantDenied':True,
        'exactARNReader':True,'deniedPaginationClearsRows':True,'principalNavigation':True,
        'refreshCloseBackReload':True,'axeWidths':[1440,390],'browserErrors':0,'externalRequests':0}))
