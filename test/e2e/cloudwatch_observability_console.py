"""One real local AWS Query + JSON Logs workflow through boto3, Cloudscape and Scalar."""
from collections import Counter
from contextlib import ExitStack, contextmanager
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import time
from urllib.error import HTTPError
from urllib.parse import parse_qsl, urlsplit
from urllib.request import Request
from xml.etree import ElementTree

import boto3
from playwright.sync_api import expect, sync_playwright

from botocore.config import Config
from cloudwatch_metrics import BINARY, OPENER, client, control, running
from simpledb_scalar_reference_live import ScalarQueryReference


ALARM = 'practice-queue-depth'
NS = 'Training/Observability'
METRIC = 'QueueDepth'
DIMS = [{'Name': 'Queue', 'Value': 'practice'}]
LOG = '/practice/observability'
STREAM = 'local'
ACTOR = 'observability-reader'
MON = '{http://monitoring.amazonaws.com/doc/2010-08-01/}'
PERIOD = timedelta(minutes=1)
REGION = 'us-east-1'
ARN = 'arn:aws:cloudwatch:us-east-1:000000000000:alarm:'
MESSAGE = 'explicitly ingested local observation'


def utcminute():
    return datetime.now(timezone.utc).replace(second=0, microsecond=0)


def alarm(name=ALARM, metric=METRIC):
    return dict(AlarmName=name, Namespace=NS, MetricName=metric, Dimensions=DIMS, Unit='Count',
                Statistic='Average', Period=60, EvaluationPeriods=1, Threshold=4,
                ComparisonOperator='GreaterThanThreshold')


def stat(sdk, start, **changes):
    query = dict(Namespace=NS, MetricName=METRIC, Dimensions=DIMS, Unit='Count', Period=60,
                 Statistics=['Average', 'SampleCount'], StartTime=start, EndTime=start + PERIOD)
    return sdk.get_metric_statistics(**{**query, **changes})['Datapoints']


def observation(sdk, name=ALARM):
    alarms = sdk.describe_alarms(AlarmNames=[name])['MetricAlarms']
    assert len(alarms) <= 1
    if alarms:
        assert alarms[0]['AlarmName'] == name and alarms[0]['AlarmArn'] == ARN + name
        return alarms[0]


def measure(sdk, value, stamp):
    sdk.put_metric_data(Namespace=NS, MetricData=[
        dict(MetricName=METRIC, Dimensions=DIMS, Unit='Count', Value=value, Timestamp=stamp)])


def policy(team, list_allowed=True, statistics_allowed=True):
    actions = [{'Effect': 'Allow', 'Action': ['cloudwatch:PutMetricData',
                                              'logs:DescribeLogGroups', 'logs:DescribeLogStreams',
                                              'logs:GetLogEvents'], 'Resource': '*'},
               {'Effect': 'Allow', 'Action': ['cloudwatch:PutMetricAlarm', 'cloudwatch:DeleteAlarms',
                                              'cloudwatch:DescribeAlarms'], 'Resource': ARN + ALARM}]
    if statistics_allowed:
        actions.append({'Effect':'Allow', 'Action':'cloudwatch:GetMetricStatistics','Resource':'*'})
    if list_allowed:
        actions.append({'Effect': 'Allow', 'Action': 'cloudwatch:DescribeAlarms', 'Resource': '*'})
    control(team, '/_simulator/v1/iam/principals/' + ACTOR + '/policy', 'PUT',
            {'Version': '2012-10-17', 'Statement': actions})


@contextmanager
def browser_session(browser, team):
    policy(team)
    token = control(team, '/_simulator/v1/access', 'POST', {'Principal': ACTOR})['AccessToken']
    context = browser.new_context(service_workers='block', viewport={'width': 1300, 'height': 900})
    outside, errors = [], []
    def local(route):
        actual = urlsplit(route.request.url)
        if actual.scheme == 'http' and actual.netloc == urlsplit(team.origin).netloc:
            route.continue_()
        else:
            outside.append(route.request.resource_type)
            route.abort()
    try:
        context.route('**/*', local)
        page = context.new_page()
        page.on('pageerror', lambda error: errors.append(type(error).__name__))
        page.goto(team.origin + '/_simulator/console/')
        page.get_by_label('Access token', exact=True).fill(token)
        page.get_by_role('button', name='Open sandbox', exact=True).click()
        expect(page.get_by_role('button', name='Services', exact=True)).to_be_visible()
        yield context, page
        assert not outside and not errors
    finally:
        context.close()


def menu(page, item):
    page.get_by_role('button', name='Services', exact=True).click()
    page.get_by_role('menuitem', name=item, exact=True).click()


def inspect(page, name, state, measured):
    page.get_by_label('Exact alarm name', exact=True).fill(name)
    page.get_by_role('button', name='View alarm', exact=True).click()
    expect(page.get_by_role('heading', name=name, exact=True)).to_be_visible()
    expect(page.get_by_text(state, exact=True)).to_be_visible()
    expect(page.get_by_text(measured, exact=True)).to_be_visible()
    expect(page.get_by_text('Queue = practice', exact=True)).to_be_visible()


def send(ref, requests, action, edits, status):
    start = len(requests)
    fields = ref.examples[action]['value']
    previous = getattr(ref, 'edited_fields', {}).get(action, set())
    replacements = {key: edits.get(key, fields[key]) for key in previous | set(edits)}
    ref.open_example(action, service='monitoring', replacements=replacements)
    if not hasattr(ref, 'edited_fields'):
        ref.edited_fields = {}
    ref.edited_fields[action] = set(edits)
    assert len(requests) == start, 'Draft edits must not issue a request'
    response = ref.send()
    request = response.request
    assert requests[start:] == [request], (action, len(requests[start:]))
    assert response.status == status, (action, response.status, status)
    assert request.url == response.url == ref.endpoint + '/' and request.method == 'POST'
    assert request.redirected_from is None and request.redirected_to is None
    headers = request.all_headers()
    assert headers['cookie'] and headers['x-simulator-service'] == 'monitoring'
    assert headers['content-type'].split(';')[0] == 'application/x-www-form-urlencoded'
    assert {'x-amz-target', 'authorization', 'x-simulator-token', 'x-simulator-principal',
            'x-simulator-access-role'}.isdisjoint(headers)
    pairs = parse_qsl(request.post_data, strict_parsing=True, keep_blank_values=True)
    expected = ref.examples[action]['value'] | edits
    assert len(pairs) == len(expected) and dict(pairs) == expected
    root = ElementTree.fromstring(response.body())
    if status == 403:
        assert root.tag == MON + 'ErrorResponse' and root.findtext(MON + 'Error/' + MON + 'Code') == 'AccessDenied'
        expect(ref.response_panel).to_contain_text('AccessDenied')
    else:
        assert root.tag == MON + action + 'Response'
        assert root.findtext(MON + 'ResponseMetadata/' + MON + 'RequestId')
        expect(ref.response_panel).to_contain_text(action + 'Response')
    ref.close_client()
    return root


def monitoring_reference(context, origin):
    page = context.new_page()
    sends = []
    page.on('request', lambda request: sends.append(request) if request.method == 'POST' else None)
    ref = ScalarQueryReference(page, origin, {})
    for action in ('DescribeAlarms', 'GetMetricStatistics', 'PutMetricAlarm', 'PutMetricData', 'DeleteAlarms'):
        assert ref.examples[action]['value']['Action'] == action
    return ref, sends


def shown_log(page):
    menu(page, 'CloudWatch Logs')
    page.get_by_role('link', name=LOG, exact=True).click()
    page.get_by_role('link', name=STREAM, exact=True).click()
    expect(page.get_by_text(MESSAGE, exact=True)).to_be_visible()


def verify_replaced_read(page, origin, sibling):
    # Hold one actual outgoing read before the socket. Do not replay or synthesize a response.
    pending = []
    def hold_replaced(route):
        request = route.request
        fields = dict(parse_qsl(request.post_data or ''))
        if not pending and fields.get('Action') == 'DescribeAlarms' and fields.get('AlarmNames.member.1') == sibling:
            pending.append(route)
        else:
            route.fallback()
    page.route(origin + '/', hold_replaced)
    try:
        page.get_by_label('Exact alarm name', exact=True).fill(sibling)
        page.get_by_role('button', name='View alarm', exact=True).click()
        until = time.monotonic() + 10
        while not pending:
            assert time.monotonic() < until, 'No original browser request reached the delay'
            page.wait_for_timeout(20)
        page.get_by_label('Exact alarm name', exact=True).fill(ALARM)
        page.get_by_role('button', name='View alarm', exact=True).click()
        expect(page.get_by_text(ARN + ALARM, exact=True)).to_be_visible()
        pending[0].continue_()
        # An ordinary additional native read drains the browser's event loop.
        page.get_by_role('button', name='Refresh alarm', exact=True).click()
        expect(page.get_by_text(ARN + ALARM, exact=True)).to_be_visible()
        expect(page.get_by_role('heading', name=sibling, exact=True)).to_have_count(0)
    finally:
        page.unroute(origin + '/', hold_replaced)


def audit_view(page):
    path = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((path.parent / 'provenance.json').read_text())
    assert hashlib.sha256(path.read_bytes()).hexdigest() == provenance['bundleSha256']
    page.evaluate(path.read_text())
    result = page.evaluate("""async () => {
        const r = await axe.run(document, {runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}});
        return {violations:r.violations.map(v=>v.id), passed:r.passes.length,
                reflow:document.documentElement.scrollWidth <= window.innerWidth};
    }""")
    assert result['passed'] and not result['violations'] and result['reflow'], result


def owned_cleanup(sdk, logs, names):
    existing = {item['AlarmName'] for item in sdk.describe_alarms()['MetricAlarms']}
    assert existing.issubset(names)
    for name in existing:
        sdk.delete_alarms(AlarmNames=[name])
    assert sdk.describe_alarms()['MetricAlarms'] == []
    groups = logs.describe_log_groups(logGroupNamePrefix=LOG)['logGroups']
    if any(g['logGroupName'] == LOG for g in groups):
        logs.delete_log_group(logGroupName=LOG)
    assert not logs.describe_log_groups(logGroupNamePrefix=LOG)['logGroups']


def clients(stack, team, counts, *, region=REGION):
    sdk = client(stack, team, counts, set(), region=region)
    logs = boto3.client('logs', endpoint_url=team.origin, region_name=region,
                        aws_access_key_id='TCSIMLOGS', aws_secret_access_key='dummy-local-only',
                        config=Config(proxies={}, retries={'total_max_attempts':1}, connect_timeout=3, read_timeout=10))
    def bind_token(request, **_):
        request.headers['X-Simulator-Token'] = team.token
    def verify_endpoint(request, **_):
        assert request.method == 'POST' and request.url == team.origin + '/'
    logs.meta.events.register('before-sign', bind_token)
    logs.meta.events.register('before-send', verify_endpoint)
    stack.callback(logs.close)
    return sdk, logs


def populate_logs(logs, message):
    logs.create_log_group(logGroupName=LOG)
    logs.create_log_stream(logGroupName=LOG, logStreamName=STREAM)
    logs.put_log_events(logGroupName=LOG, logStreamName=STREAM,
                        logEvents=[{'timestamp': int(time.time()*1000), 'message': message}])


def messages(logs):
    return [row['message'] for row in logs.get_log_events(logGroupName=LOG, logStreamName=STREAM)['events']]


def exercise(browser, directory):
    counts = Counter()
    pages = ['practice-page-' + str(i).zfill(2) for i in range(20)]
    owned = [ALARM, *pages]
    with running(directory/'peer') as peer, ExitStack() as peer_stack:
        other, peer_logs = clients(peer_stack, peer, counts)
        try:
            other.put_metric_alarm(**alarm())
            peer_start = utcminute() - PERIOD
            measure(other, 75, peer_start)
            peer_point = stat(other, peer_start)
            assert len(peer_point) == 1 and peer_point[0]['Average'] == 75
            peer_config = observation(other)['AlarmConfigurationUpdatedTimestamp']
            populate_logs(peer_logs, 'separate peer event')

            def peer_unchanged():
                assert observation(other)['AlarmConfigurationUpdatedTimestamp'] == peer_config
                assert stat(other, peer_start) == peer_point and messages(peer_logs) == ['separate peer event']

            with running(directory/'owner') as owner, ExitStack() as stack:
                sdk, logs = clients(stack, owner, counts)
                try:
                    assert owner.token != peer.token and owner.db != peer.db
                    foreign = Request(peer.origin+'/', b'Action=DescribeAlarms&Version=2010-08-01', {
                        'Content-Type':'application/x-www-form-urlencoded', 'X-Simulator-Service':'monitoring',
                        'X-Simulator-Token':owner.token})
                    try:
                        with OPENER.open(foreign, timeout=5):
                            raise AssertionError('A team token was accepted by another process')
                    except HTTPError as error:
                        assert error.code == 401
                    peer_unchanged()
                    populate_logs(logs, MESSAGE)
                    for name in pages:
                        sdk.put_metric_alarm(**alarm(name, 'UnrelatedMetric'))
                    with browser_session(browser, owner) as (ctx, console):
                        seen = []
                        console.on('request', lambda r: seen.append(r) if r.method == 'POST' and r.url == owner.origin+'/' else None)
                        shown_log(console)
                        assert messages(logs) == [MESSAGE]
                        ref, sends = monitoring_reference(ctx, owner.origin)
                        send(ref, sends, 'PutMetricAlarm', {}, 200)
                        assert observation(sdk)['StateValue'] == 'INSUFFICIENT_DATA'
                        menu(console, 'CloudWatch')
                        inspect(console, ALARM, 'INSUFFICIENT_DATA', 'No datapoints')
                        saved_start = utcminute() - PERIOD
                        measure(sdk, 1, saved_start)
                        # Keep a real low observation in the next minute too. Scalar adds the high observation.
                        measure(sdk, 1, saved_start + PERIOD)
                        assert stat(sdk, saved_start)[0]['Average'] == 1
                        assert observation(sdk)['StateValue'] == 'OK'
                        inspect(console, ALARM, 'OK', '1 Count')
                        req = send(ref, sends, 'GetMetricStatistics', {'StartTime': saved_start.isoformat(),
                            'EndTime': (saved_start+PERIOD).isoformat()}, 200)
                        assert req.findtext('.//' + MON + 'Average') == '1'
                        send(ref, sends, 'PutMetricData', {'MetricData.member.1.Value':'9'}, 200)
                        limit = time.monotonic()+75
                        while True:
                            current = observation(sdk)
                            if current['StateValue'] == 'ALARM': break
                            assert time.monotonic() < limit, 'No observed breaching completed minute'
                            time.sleep(0.5)
                        start = datetime.fromisoformat(json.loads(current['StateReasonData'])['Start'])
                        points = stat(sdk, start)
                        assert len(points) == 1 and points[0]['Average'] in (5, 9) and points[0]['Unit'] == 'Count'
                        assert not stat(sdk, start, Unit='Seconds')
                        wrong = client(stack, owner, counts, set(), region='us-west-2')
                        assert not stat(wrong, start) and not wrong.describe_alarms(AlarmNames=[ALARM])['MetricAlarms']
                        inspect(console, ALARM, 'ALARM', f"{points[0]['Average']:g} Count")
                        got = send(ref, sends, 'DescribeAlarms', {}, 200)
                        assert got.findtext('.//' + MON + 'AlarmArn') == ARN + ALARM
                        assert got.findtext('.//' + MON + 'StateValue') == 'ALARM'
                        before = observation(sdk, pages[0])
                        send(ref, sends, 'DeleteAlarms', {'AlarmNames.member.1':pages[0]}, 403)
                        after = observation(sdk, pages[0])
                        assert before['AlarmConfigurationUpdatedTimestamp'] == after['AlarmConfigurationUpdatedTimestamp']
                        assert after['StateValue'] == 'INSUFFICIENT_DATA' and after['AlarmArn'] == ARN+pages[0]
                        console.get_by_role('button', name='Refresh alarm list', exact=True).click()
                        expect(console.get_by_role('button', name='Load more alarms', exact=True)).to_be_visible()
                        policy(owner, list_allowed=False)
                        with console.expect_response(lambda r: r.url==owner.origin+'/' and r.request.method=='POST' and r.status==403):
                            console.get_by_role('button', name='Load more alarms', exact=True).click()
                        expect(console.get_by_text(re.compile('AccessDenied'))).to_be_visible()
                        expect(console.get_by_role('table', name='Metric alarms')).to_have_count(0)
                        expect(console.get_by_text('No metric alarms', exact=True)).to_have_count(0)
                        expect(console.get_by_role('button', name='Load more alarms', exact=True)).to_have_count(0)
                        policy(owner)
                        console.get_by_role('button', name='Refresh alarm list', exact=True).click()
                        expect(console.get_by_role('link', name=pages[0], exact=True)).to_be_visible()
                        assert all(dict(parse_qsl(r.post_data))['Action'] in ('DescribeAlarms','GetMetricStatistics')
                                   for r in seen if r.headers.get('x-simulator-service')=='monitoring')
                        # Removing only the independent statistic permission keeps the legitimately read alarm.
                        policy(owner, statistics_allowed=False)
                        console.get_by_label('Exact alarm name', exact=True).fill(ALARM)
                        with console.expect_response(lambda r: r.url==owner.origin+'/' and r.status==403):
                            console.get_by_role('button', name='View alarm', exact=True).click()
                        expect(console.get_by_text(ARN+ALARM, exact=True)).to_be_visible()
                        expect(console.get_by_text(re.compile('AccessDenied'))).to_be_visible()
                        expect(console.get_by_text('Measured statistic', exact=True)).to_have_count(0)
                        expect(console.get_by_text('No datapoints', exact=True)).to_have_count(0)
                        policy(owner)
                        verify_replaced_read(console, owner.origin, pages[0])
                        expect(console.get_by_text('Measured statistic', exact=True)).to_be_visible()
                        audit_view(console)
                        console.set_viewport_size({'width':390,'height':844})
                        console.reload()
                        expect(console.get_by_role('heading', name='Metric alarms', exact=True)).to_be_visible()
                        console.get_by_label('Exact alarm name', exact=True).fill(ALARM)
                        console.get_by_role('button', name='View alarm', exact=True).click()
                        expect(console.get_by_text('Measured statistic', exact=True)).to_be_visible()
                        audit_view(console)
                        assert len(sends) == 5
                        peer_unchanged()
                        config_time = observation(sdk)['AlarmConfigurationUpdatedTimestamp']
                        assert owner.db != peer.db
                except BaseException:
                    owned_cleanup(sdk, logs, owned)
                    raise
            with running(directory/'owner') as restarted, ExitStack() as stack:
                sdk, logs = clients(stack, restarted, counts)
                try:
                    assert stat(sdk, start) == points and messages(logs) == [MESSAGE]
                    fresh = observation(sdk)
                    assert fresh['AlarmConfigurationUpdatedTimestamp'] == config_time
                    with browser_session(browser, restarted) as (ctx, console):
                        menu(console, 'CloudWatch')
                        expect(console.get_by_role('table', name='Metric alarms')).to_be_visible()
                        console.get_by_label('Exact alarm name', exact=True).fill(ALARM)
                        console.get_by_role('button', name='View alarm', exact=True).click()
                        expect(console.get_by_role('heading', name=ALARM, exact=True)).to_be_visible()
                        expect(console.get_by_text(ARN+ALARM, exact=True)).to_be_visible()
                        ref, sends = monitoring_reference(ctx, restarted.origin)
                        send(ref, sends, 'DeleteAlarms', {}, 200)
                        assert len(sends) == 1 and observation(sdk) is None and stat(sdk, start) == points
                        peer_unchanged()
                finally:
                    owned_cleanup(sdk, logs, owned)
        finally:
            owned_cleanup(other, peer_logs, [ALARM])
    assert counts['sends'] == counts['responses'] == counts['calls'] and counts['sends'] > 40
    return {'monitoringSdkCalls':counts['sends'], 'scalarStatus':[200,200,200,200,403,200],
            'observedAlarmStates':['INSUFFICIENT_DATA','OK','ALARM'], 'logsRead':True,
            'revokedContinuation':403, 'deniedStatistic':403, 'replacedRead':True,
            'auditedWidths':[1300,390], 'twoProcesses':True,'restart':True,'nativeCleanup':True}


def run():
    with BINARY.open('rb') as binary:
        assert hashlib.file_digest(binary, 'sha256').hexdigest() == os.environ['SIMULATOR_BINARY_SHA256']
    boto3.Session()
    with tempfile.TemporaryDirectory(prefix='cloudwatch-surface-') as temp, sync_playwright() as p:
        browser = p.chromium.launch(chromium_sandbox=True, executable_path=os.environ.get('CHROMIUM_EXECUTABLE'))
        try:
            print(json.dumps(exercise(browser, Path(temp)), sort_keys=True))
        finally:
            browser.close()


if __name__ == '__main__':
    run()
