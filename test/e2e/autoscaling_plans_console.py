"""Exercise the packaged Scaling Plans Console and independently read native DynamoDB state.

Use --sdk for the same-resource boto3 capacity readback. The browser always sends its
ordinary authenticated requests to the disposable loopback process. AXE_SCRIPT can
point at the pinned axe-core distribution used by the repository's accessibility job.
"""
import argparse
import http.client
import json
import os
from pathlib import Path
import shutil
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from gcp_dns_sdk import LocalTeam, started
from reflow import assert_reflow

TABLE = 'autoscaling-practice'
PLAN = 'practice-plan'
PLANNER = 'AnyScaleScalingPlannerFrontendService.'
DYNAMO = 'DynamoDB_20120810.'
IDENTITY = {'ScalingPlanName': PLAN, 'ScalingPlanVersion': 1}
REGION = 'us-east-1'


def wire(team, method, path, body=None, target='', region=REGION, token=None):
    headers = {'X-Simulator-Token': team.token if token is None else token,
               'Content-Type': 'application/json'}
    if target:
        headers.update({'X-Amz-Target': target, 'X-Simulator-Region': region,
                        'Content-Type': 'application/x-amz-json-1.0' if target.startswith(DYNAMO) else
                                        'application/x-amz-json-1.1'})
    connection = http.client.HTTPConnection('127.0.0.1', team.port, timeout=8)
    try:
        connection.request(method, path, None if body is None else json.dumps(body), headers)
        response = connection.getresponse()
        payload = response.read()
        assert 200 <= response.status < 300, (target or path, response.status)
        return json.loads(payload) if payload else None
    finally:
        connection.close()


def dynamo(team, action, body):
    return wire(team, 'POST', '/', body, DYNAMO + action)


def read_capacity(team, sdk, expected):
    actual = (sdk.describe_table(TableName=TABLE) if sdk else dynamo(team, 'DescribeTable', {'TableName': TABLE}))
    provisioned = actual['Table']['ProvisionedThroughput']
    assert [provisioned['ReadCapacityUnits'], provisioned['WriteCapacityUnits']] == expected


def sdk_client(team):
    import boto3
    from botocore.config import Config
    client = boto3.client('dynamodb', endpoint_url=team.endpoint, region_name=REGION,
                          aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='local-fixture-only',
                          config=Config(connect_timeout=2, read_timeout=5, proxies={}, retries={'max_attempts': 0}))
    client.meta.events.register('before-sign.dynamodb',
        lambda request, **kw: request.headers.add_header('X-Simulator-Token', team.token))

    def local_only(request, **kw):
        assert request.url == team.endpoint + '/'
    client.meta.events.register('before-send.dynamodb', local_only)
    return client


def exercise(binary, use_sdk, axe):
    result = {'capacity': [], 'audits': [], 'browserActions': [], 'stockSDKReadback': use_sdk}
    outside, errors = [], []
    with tempfile.TemporaryDirectory(prefix='scaling-plan-console-') as directory, sync_playwright() as pw:
        root = Path(directory)
        (root / 'A').mkdir()
        (root / 'B').mkdir()
        team = LocalTeam(root / 'A', binary)
        other = LocalTeam(root / 'B', binary)
        chromium = os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
        with started(team), started(other), pw.chromium.launch(executable_path=chromium, chromium_sandbox=True) as browser:
            client = sdk_client(team) if use_sdk else None

            def check(expected):
                read_capacity(team, client, expected)
                result['capacity'].append(expected)

            def page_for(token, lang='en', width=1365):
                ctx = browser.new_context(viewport={'width': width, 'height': 900}, service_workers='block',
                                          reduced_motion='reduce')
                origin = urlsplit(team.endpoint)

                def local(route):
                    address = urlsplit(route.request.url)
                    if (address.scheme, address.netloc) == (origin.scheme, origin.netloc):
                        route.continue_()
                    else:
                        outside.append(address.scheme)
                        route.abort()
                ctx.route('**/*', local)
                assert ctx.request.post(team.endpoint + '/_simulator/login', form={'token': token}, max_redirects=0).status == 303
                ctx.add_init_script("localStorage.setItem('tenkacloud-simulator-locale'," + json.dumps(lang) + ")")
                page = ctx.new_page()
                page.on('pageerror', lambda error: errors.append(str(error)))

                def record(response):
                    target = response.request.headers.get('x-amz-target', '')
                    if not (target.startswith(PLANNER) or target.startswith(DYNAMO)):
                        return
                    assert response.request.method == 'POST' and response.url == team.endpoint + '/'
                    assert response.request.headers['x-simulator-region'] == REGION
                    assert 'x-simulator-token' not in response.request.headers
                    result['browserActions'].append([target, response.status])
                page.on('response', record)
                return ctx, page

            def audit(page, state):
                assert_reflow(page)
                if axe:
                    page.evaluate(axe.read_text())
                    findings = page.evaluate("""async () => (await axe.run(document, {
                        runOnly: {type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}
                    })).violations.map(v => [v.id, v.nodes.map(n => n.target)])""")
                    assert not findings, (state, findings)
                    result['audits'].append([state, page.viewport_size['width']])

            def click(page, name):
                page.get_by_role('button', name=name, exact=True).click()

            def observe(page, expected):
                click(page, 'Read capacity for ' + TABLE)
                heading = page.get_by_role('heading', name='Observed table capacity', exact=True)
                expect(heading).to_be_visible()
                for label, value in zip(('Read capacity units', 'Write capacity units'), expected):
                    term = page.get_by_role('term').filter(has_text=label)
                    expect(term.locator('xpath=following-sibling::dd[1]')).to_have_text(str(value))
                check(expected)

            dynamo(team, 'CreateTable', {'TableName': TABLE, 'AttributeDefinitions':[{'AttributeName':'id', 'AttributeType':'S'}],
                   'KeySchema':[{'AttributeName':'id', 'KeyType':'HASH'}], 'BillingMode':'PROVISIONED',
                   'ProvisionedThroughput':{'ReadCapacityUnits':2,'WriteCapacityUnits':9}})
            check([2, 9])
            ctx, page = page_for(team.token)
            page.goto(team.endpoint + '/_simulator/console/#/home')
            expect(page.get_by_role('heading', name='Welcome to TenkaCloud')).to_be_visible()
            click(page, 'Services')
            page.get_by_role('menuitem', name='AWS Auto Scaling Plans', exact=True).click()
            expect(page.get_by_text('No scaling plans found.', exact=True)).to_be_visible()
            audit(page, 'list')
            click(page, 'Create scaling plan')
            for label, value in [('Plan name', PLAN), ('Source tag key', 'application'), ('Source tag value', 'practice'),
                                 ('DynamoDB table', TABLE), ('Minimum capacity', '5'), ('Maximum capacity', '8')]:
                page.get_by_label(label, exact=True).fill(value)
            audit(page, 'create')
            click(page, 'Create scaling plan')
            expect(page.get_by_role('heading', name=PLAN, exact=True)).to_be_visible()
            row = page.get_by_role('row').filter(has_text=TABLE)
            expect(row).to_contain_text('5 – 8')
            observe(page, [5, 9])
            audit(page, 'observed')
            spec = wire(team, 'GET', '/_simulator/docs/openapi.json')
            content = spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']
            example = content['examples']['AutoScalingDescribeScalingPlanResources']['value']
            assert example == IDENTITY
            resource = wire(team, 'POST', '/', example, PLANNER + 'DescribeScalingPlanResources')['ScalingPlanResources']
            assert len(resource) == 1 and resource[0]['ResourceId'] == 'table/' + TABLE and resource[0]['ScalingStatusCode'] == 'Active'
            assert wire(other, 'POST', '/', {}, PLANNER + 'DescribeScalingPlans')['ScalingPlans'] == []
            assert wire(team, 'POST', '/', {}, PLANNER + 'DescribeScalingPlans', region='us-west-2')['ScalingPlans'] == []
            policy = {'Version':'2012-10-17', 'Statement':[{'Effect':'Allow','Action':'autoscaling-plans:*','Resource':'*'}]}
            wire(team, 'PUT', '/_simulator/v1/iam/principals/planner/policy', policy)
            participant = wire(team, 'POST', '/_simulator/v1/access', {'Principal':'planner'})['AccessToken']
            limited, reader = page_for(participant)
            reader.goto(team.endpoint + '/_simulator/console/#/autoscaling-plans')
            click(reader, PLAN)
            click(reader, 'Read capacity for ' + TABLE)
            expect(reader.get_by_text('403:', exact=False)).to_be_visible()
            expect(reader.get_by_role('heading', name='Observed table capacity')).to_have_count(0)
            click(reader, 'Edit capacity bounds')
            reader.get_by_label('Maximum capacity', exact=True).fill('12')
            click(reader, 'Save changes')
            expect(reader.get_by_text('403:', exact=False)).to_be_visible()
            assert wire(team, 'POST', '/', {'ScalingPlanNames':[PLAN]}, PLANNER + 'DescribeScalingPlans')['ScalingPlans'][0]['ScalingInstructions'][0]['MaxCapacity'] == 8
            check([5, 9])
            limited.close()
            click(page, 'Edit capacity bounds')
            page.get_by_label('Minimum capacity', exact=True).fill('2')
            page.get_by_label('Maximum capacity', exact=True).fill('3')
            click(page, 'Save changes')
            expect(row).to_contain_text('2 – 3')
            observe(page, [3, 9])
            narrow, japanese = page_for(team.token, 'ja', 390)
            japanese.goto(team.endpoint + '/_simulator/console/#/autoscaling-plans')
            click(japanese, PLAN)
            expect(japanese.get_by_role('heading', name='スケーラブルリソース', exact=True)).to_be_visible()
            click(japanese, '容量を取得：' + TABLE)
            expect(japanese.get_by_role('heading', name='取得したテーブル容量')).to_be_visible()
            audit(japanese, 'observed-ja')
            narrow.close()

            # The real provider commits the write. The transport loses its acknowledgement.
            # Subsequent independent reads establish state; the UI must never replay it.
            faults = {'lost': 0, 'refreshFailures': 0}
            def lose_update(route):
                if route.request.headers.get('x-amz-target') != PLANNER + 'UpdateScalingPlan':
                    route.fallback()
                    return
                committed = route.fetch(max_redirects=0)
                assert committed.status == 200
                committed.dispose()
                faults['lost'] += 1
                route.abort('failed')
            page.route(team.endpoint + '/', lose_update)
            click(page, 'Edit capacity bounds')
            page.get_by_label('Minimum capacity', exact=True).fill('3')
            click(page, 'Save changes')
            expect(page.get_by_text('The write outcome is unknown.', exact=False)).to_be_visible()
            expect(page.get_by_role('button', name='Scaling plans', exact=True)).to_be_disabled()
            expect(page.get_by_role('button', name='Edit capacity bounds', exact=True)).to_have_count(0)
            page.unroute(team.endpoint + '/', lose_update)

            def fail_refresh(route):
                if route.request.headers.get('x-amz-target') == PLANNER + 'DescribeScalingPlans':
                    assert route.request.post_data_json['ScalingPlanNames'] == [PLAN]
                    faults['refreshFailures'] += 1
                    route.abort('failed')
                else:
                    route.fallback()
            page.route(team.endpoint + '/', fail_refresh)
            click(page, 'Refresh')
            expect(page.get_by_text('Failed to fetch', exact=False)).to_be_visible()
            expect(page.get_by_role('button', name='Scaling plans', exact=True)).to_be_disabled()
            page.unroute(team.endpoint + '/', fail_refresh)
            click(page, 'Refresh')
            expect(page.get_by_role('row').filter(has_text=TABLE)).to_contain_text('3 – 3')
            expect(page.get_by_role('button', name='Edit capacity bounds', exact=True)).to_be_enabled()
            assert faults == {'lost':1, 'refreshFailures':1}
            result['lostAcknowledgement'] = faults

            # Cancel a genuine in-flight read without replacing the backend or fabricating data.
            held = []
            def hold_capacity(route):
                if route.request.headers.get('x-amz-target') == DYNAMO + 'DescribeTable':
                    held.append(route)
                else:
                    route.fallback()
            page.route(team.endpoint + '/', hold_capacity)
            click(page, 'Read capacity for ' + TABLE)
            expect(page.get_by_role('button', name='Cancel read')).to_be_visible()
            with page.expect_request(lambda r: r.headers.get('x-amz-target') == PLANNER + 'DescribeScalingPlans'):
                click(page, 'Cancel read')
                click(page, 'Refresh')
            expect(page.get_by_role('heading', name='Scalable resources')).to_be_visible()
            assert len(held) == 1
            held[0].continue_()
            page.unroute(team.endpoint + '/', hold_capacity)
            expect(page.get_by_role('heading', name='Observed table capacity')).to_have_count(0)
            ctx.close()
            if client:
                client.close()
            team.stop()
            team.start()
            client = sdk_client(team) if use_sdk else None
            check([3, 9])
            ctx, page = page_for(team.token)
            page.goto(team.endpoint + '/_simulator/console/#/autoscaling-plans')
            click(page, PLAN)
            click(page, 'Delete plan')
            page.get_by_label('Confirm plan name', exact=True).fill('wrong name')
            expect(page.get_by_role('button', name='Delete plan', exact=True)).to_be_disabled()
            page.get_by_label('Confirm plan name', exact=True).fill(PLAN)
            click(page, 'Delete plan')
            expect(page.get_by_text('No scaling plans found.', exact=True)).to_be_visible()
            check([3, 9])
            assert wire(team, 'POST', '/', {}, PLANNER+'DescribeScalingPlans')['ScalingPlans'] == []
            dynamo(team, 'DeleteTable', {'TableName':TABLE})
            assert dynamo(team, 'ListTables', {})['TableNames'] == []
            assert dynamo(other, 'ListTables', {})['TableNames'] == []
            ctx.close()
            if client:
                client.close()
    assert not outside and not errors, (outside, errors)
    actions = result['browserActions']
    for op in ('CreateScalingPlan','DescribeScalingPlans','DescribeScalingPlanResources','UpdateScalingPlan','DeleteScalingPlan'):
        assert [PLANNER + op, 200] in actions
    assert actions.count([PLANNER + 'CreateScalingPlan',200]) == actions.count([PLANNER+'UpdateScalingPlan',200]) == actions.count([PLANNER+'DeleteScalingPlan',200]) == 1
    assert [PLANNER + 'UpdateScalingPlan',403] in actions and [DYNAMO + 'DescribeTable',403] in actions
    assert not Path(directory).exists()
    return result


if __name__ == '__main__':
    args = argparse.ArgumentParser()
    args.add_argument('--sdk', action='store_true')
    options = args.parse_args()
    binary = Path(os.environ.get('SIMULATOR_BINARY','dist/tenkacloud-simulator')).resolve(strict=True)
    script = Path(os.environ['AXE_SCRIPT']) if os.environ.get('AXE_SCRIPT') else None
    print(json.dumps(exercise(binary, options.sdk, script)))
