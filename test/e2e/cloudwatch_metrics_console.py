"""Unregistered source-only Metrics console acceptance, awaiting production seams.

No capability/presentation override, SDK success or AWS parity claim. One explicitly
labelled transport-fault subcase fetches real native XML then fulfills that request
with exactly one synthetic extra point; unmodified/recovery proof is separate.
The actual root Gateway and authenticated console must implement the proposed
monitoring selector and CloudWatch Metrics catalog entry before this can run.
"""
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
import time
from contextlib import ExitStack
from pathlib import Path
import re
import shutil
from tempfile import TemporaryDirectory
from urllib.parse import urlencode, urlsplit, parse_qs
from urllib.request import Request, build_opener, ProxyHandler
from urllib.error import HTTPError
from xml.etree import ElementTree as ET

from check import BINARY, REPORTS, assert_reflow, expect, running, sync_playwright
from sakura_cli import NoRedirect, frame

XML = '{http://monitoring.amazonaws.com/doc/2010-08-01/}'

UNIT_DIAGNOSTIC_LABELS = ('All native units', 'Seconds', 'Microseconds', 'Milliseconds', 'Bytes', 'Kilobytes', 'Megabytes', 'Gigabytes', 'Terabytes', 'Bits', 'Kilobits', 'Megabits', 'Gigabits', 'Terabits', 'Percent', 'Count', 'Bytes/Second', 'Kilobytes/Second', 'Megabytes/Second', 'Gigabytes/Second', 'Terabytes/Second', 'Bits/Second', 'Kilobits/Second', 'Megabits/Second', 'Gigabits/Second', 'Terabits/Second', 'Count/Second', 'None')
UNIT_DIAGNOSTICS = []
PAGINATION_DIAGNOSTICS = []


class Native:
    """Real root HTTP only. No signing/auth bypass and no response substitution."""
    def __init__(self, origin, token):
        self.origin, self.token = origin, token
        parsed = urlsplit(origin)
        assert parsed.scheme == 'http' and parsed.hostname == '127.0.0.1' and parsed.port
        assert not parsed.username and not parsed.password and not parsed.query and not parsed.fragment
        self.opener = build_opener(ProxyHandler({}), NoRedirect())
        self.calls = self.responses = 0
        self.operations = {}

    def send(self, path, payload, status=200, principal='simulator-admin', region='us-east-1', credential=None, policy=False):
        assert path.startswith('/') and not path.startswith('//')
        credential = self.token if credential is None else credential
        target = self.origin + path
        assert urlsplit(target).netloc == urlsplit(self.origin).netloc
        body = json.dumps(payload).encode() if policy else urlencode(payload).encode()
        request = Request(target, data=body, method='PUT' if policy else 'POST', headers={
            'Content-Type':'application/json' if policy else 'application/x-www-form-urlencoded',
            'X-Simulator-Token':credential, 'X-Simulator-Principal':principal,
            'X-Simulator-Service':'monitoring', 'X-Simulator-Region':region})
        self.calls += 1
        operation='HostPolicySetup' if policy else payload['Action']
        self.operations[operation]=self.operations.get(operation,0)+1
        try:
            response = self.opener.open(request, timeout=5)
        except HTTPError as error:
            response = error
        with response:
            self.responses += 1
            assert response.status == status, 'native status mismatch; body redacted'
            raw = response.read(2097153)
            assert len(raw) <= 2097152 and (not credential or credential.encode() not in raw)
            assert not response.headers.get('Location'), 'redirect refused'
            return raw

    def put(self, namespace, metric, values, stamp, unit='Count', dimension='selected', region='us-east-1'):
        form = {'Action':'PutMetricData','Version':'2010-08-01','Namespace':namespace}
        for i, value in enumerate(values, 1):
            prefix = 'MetricData.member.%s.' % i
            form.update({prefix+'MetricName':metric,prefix+'Value':str(value),prefix+'Timestamp':stamp,
                         prefix+'Unit':unit,prefix+'Dimensions.member.1.Name':'Fixture',prefix+'Dimensions.member.1.Value':dimension})
        assert ET.fromstring(self.send('/',form,region=region)).tag == XML+'PutMetricDataResponse'


def choose_identity(page, name):
    header = page.locator('.aws-console-top-navigation')
    trigger = header.get_by_role('button', name='Account menu', exact=True)
    expect(trigger).to_have_count(1)
    trigger.click()
    page.get_by_role('menuitem',name='Choose identity',exact=True).click()
    page.get_by_label('Principal name',exact=True).fill(name)
    page.get_by_role('dialog').get_by_role('button',name='Choose identity',exact=True).click()


def choose_region(page, previous, next_region):
    page.set_viewport_size({'width':1440,'height':1000})
    page.locator('.aws-console-top-navigation').get_by_role('button',name=previous,exact=True).click()
    page.get_by_role('menuitem',name=next_region,exact=True).click()


def select_metric(page, namespace, metric, dimension='selected'):
    page.get_by_role('tab',name='Browse',exact=True).click()
    page.get_by_label('Namespace (exact, optional)',exact=True).fill(namespace)
    page.get_by_label('Metric name (exact, optional)',exact=True).fill(metric)
    page.get_by_role('button',name='Browse metrics',exact=True).click()
    table=page.get_by_role('table',name='Native metrics in current page',exact=True)
    row=table.get_by_role('row').filter(has_text='Fixture='+dimension)
    expect(row).to_have_count(1)
    row.get_by_role('radio').check()
    page.get_by_role('button',name='Graph selected metric',exact=True).click()
    expect(page.get_by_label('Start time (ISO8601 with UTC/offset)',exact=True)).to_be_focused()



def record_unit_select_diagnostic(page, requested, stage, screenshot=False):
    # Read only the known Unit-filter control and its linked popup. Role-name
    # queries serialize only closed known labels/counts, never arbitrary names.
    if len(UNIT_DIAGNOSTICS)>=8:
        return
    record={'stage':stage,'requestedKnownLabel':requested if requested in UNIT_DIAGNOSTIC_LABELS else 'OTHER',
            'diagnosticStatus':'UNAVAILABLE'}
    UNIT_DIAGNOSTICS.append(record)
    def boolean_enum(value):
        return value if value in ('true','false') else 'missing' if value is None else 'other'
    try:
        trigger=page.get_by_label('Unit filter',exact=True)
        record['triggerCount']=min(trigger.count(),4)
        if record['triggerCount']!=1:
            return
        role=trigger.evaluate("element => element.getAttribute('role') || (element.tagName==='BUTTON'?'button':'other')")
        record['triggerRole']=role if role in ('button','combobox','listbox') else 'other'
        record['triggerExpanded']=boolean_enum(trigger.get_attribute('aria-expanded'))
        # Follow only this known control's actual popup relationship. The raw
        # generated ID is kept in memory, validated for a quoted attribute
        # selector, and never placed in a receipt, screenshot name or error.
        control_id=trigger.get_attribute('aria-controls')
        record['triggerHasPopupLink']=bool(control_id)
        record['popupLinkIDWellFormed']=bool(control_id and re.fullmatch(r'[A-Za-z0-9_:.-]{1,200}',control_id))
        record['triggerControlsKnownPopup']=False
        if record['popupLinkIDWellFormed']:
            popup=page.locator('[id="'+control_id+'"]')
            record['linkedPopupCount']=min(popup.count(),4)
        else:
            record['linkedPopupCount']=0
        if record['linkedPopupCount']==1:
            record['triggerControlsKnownPopup']=popup.get_attribute('id')==control_id
            role=popup.get_attribute('role')
            record['linkedPopupRole']=role if role in ('listbox','menu','dialog','presentation','group') else 'missing' if role is None else 'other'
            record['unitOptionCount']=min(popup.get_by_role('option').count(),64)
            matches=[]
            for label in UNIT_DIAGNOSTIC_LABELS:
                variants=(('exact',label),('selected-prefix','Selected '+label),('selected-prefix-lower','selected '+label),
                          ('selected-suffix',label+' Selected'),('selected-suffix-lower',label+' selected'),
                          ('selected-comma',label+', selected'))
                for category,name in variants:
                    option=popup.get_by_role('option',name=name,exact=True)
                    count=option.count()
                    if count:
                        matches.append({'knownLabel':label,'nameVariant':category,'count':min(count,4),
                                        'ariaSelected':boolean_enum(option.get_attribute('aria-selected')) if count==1 else 'ambiguous'})
            record['closedKnownNameMatches']=matches[:64]
        record['diagnosticStatus']='CAPTURED'
    except BaseException as error:
        record['captureFailureType']=safe_failure_type(error)
    if screenshot:
        try:
            path=REPORTS/'metrics-unit-select-diagnostic.png'
            page.screenshot(path=str(path),full_page=True,timeout=2000)
            record['dummyScreenshot']=path.name
        except BaseException as error:
            record['screenshotFailureType']=safe_failure_type(error)


def read_values(page, start, end, unit=None):
    page.get_by_label('Start time (ISO8601 with UTC/offset)',exact=True).fill(start)
    page.get_by_label('End time (exclusive, ISO8601)',exact=True).fill(end)
    page.get_by_label('Period (seconds, multiple of 60)',exact=True).fill('60')
    if unit is not None:
        # Lifetime fixtures use only the closed English native-unit labels.
        # Japanese rendering/audits below do not imply translated-name selection.
        assert unit in UNIT_DIAGNOSTIC_LABELS, 'Unknown static Unit selection fixture'
        trigger=page.get_by_label('Unit filter',exact=True)
        trigger.click()
        try:
            control_id=trigger.get_attribute('aria-controls')
            assert control_id and re.fullmatch(r'[A-Za-z0-9_:.-]{1,200}',control_id), 'Missing owned Unit popup link'
            popup=page.locator('[id="'+control_id+'"]')
            expect(popup).to_have_count(1)
            expect(popup).to_have_attribute('role','listbox')
            bare=popup.get_by_role('option',name=unit,exact=True)
            selected=popup.get_by_role('option',name='Selected '+unit,exact=True)
            option=bare.or_(selected)
            expect(option).to_have_count(1)
            # The pinned native control prefixes the selected accessible name.
            # Exact union uniqueness forbids silently choosing another option.
            expect(option).to_have_attribute('aria-selected','true' if selected.count()==1 else 'false')
            option.click()
            expect(trigger).to_have_attribute('aria-expanded','false')
        except BaseException:
            record_unit_select_diagnostic(page,unit,'failed-exact-owned-unit-selection',screenshot=True)
            raise
    page.get_by_role('button',name='Read statistics',exact=True).click()
    return page.get_by_role('table',name='Native statistic values',exact=True)


# Timing-only instrument: consume the actual native XML body and independently
# parse it before holding response.text completion. Preserve Response identity,
# exact bytes and CSP. Abort may retire authority after the body is consumed;
# explicit MessageChannel+two-RAF fence observes processing after release.
XML_GATE = """(() => {
 const originalFetch=window.fetch.bind(window), gate={armed:false,held:false,completed:0,release:null};
 window.__metricsXMLGate={arm(){gate.armed=true;},held(){return gate.held;},completed(){return gate.completed;},release(){gate.release?.();}};
 window.fetch=async(...args)=>{
  const response=await originalFetch(...args), options=args[1]||{};
  const url=new URL(typeof args[0]==='string'?args[0]:args[0].url,location.href);
  const form=new URLSearchParams(typeof options.body==='string'?options.body:'');
  if(gate.armed && response.ok && url.origin===location.origin && url.pathname==='/' && form.get('Action')==='GetMetricStatistics' && form.get('Unit')==='Count'){
   gate.armed=false;const originalText=response.text.bind(response);
   response.text=async()=>{const data=await originalText(), doc=new DOMParser().parseFromString(data,'application/xml');
    if(doc.documentElement.localName!=='GetMetricStatisticsResponse'||doc.querySelector('parsererror'))throw Error('Invalid actual XML in test probe');
    gate.held=true;await new Promise(resolve=>{gate.release=resolve;});gate.held=false;gate.release=null;
    const channel=new MessageChannel();channel.port1.onmessage=()=>{channel.port1.close();channel.port2.close();requestAnimationFrame(()=>requestAnimationFrame(()=>gate.completed++));};channel.port2.postMessage(null);return data;
   };
  }return response;
 };
})();"""


def exercise_lifetimes(page, context, browser, native, peer, directory, namespace, metric, start, end, stamp, axe, cleanup, failures):
    # All setup mutations are explicit native calls before the read-only frame.
    native.put(namespace, metric, (11,13), start, unit='Bytes')
    page_namespace = 'Simulator/PaginationAcceptance'
    page_form = {'Action':'PutMetricData','Version':'2010-08-01','Namespace':page_namespace}
    expected_names = {'Page%03d'%i for i in range(501)}
    for i, name in enumerate(sorted(expected_names), 1):
        prefix='MetricData.member.%s.'%i
        page_form.update({prefix+'MetricName':name,prefix+'Value':'0',prefix+'Timestamp':start,prefix+'Unit':'Count'})
    assert ET.fromstring(native.send('/',page_form)).tag == XML+'PutMetricDataResponse'
    policy={'Version':'2012-10-17','Statement':[
        {'Effect':'Allow','Action':['cloudwatch:ListMetrics'],'Resource':'*'},
        {'Effect':'Deny','Action':['cloudwatch:GetMetricStatistics'],'Resource':'*'}]}
    native.send('/_simulator/v1/iam/principals/metrics-console-reader/policy',policy,policy=True)
    before = (frame(Path(directory)/'a.db'),frame(Path(directory)/'b.db'))
    list_form={'Action':'ListMetrics','Version':'2010-08-01'}
    native.send('/',list_form,status=401,credential=peer.token)
    peer.send('/',list_form,status=401,credential=native.token)
    assert before == (frame(Path(directory)/'a.db'),frame(Path(directory)/'b.db')), 'cross-token refusal mutated state'
    stamp_text=stamp.isoformat().replace('+00:00','Z')
    def literal_row(table, unit, expected):
        rows=table.get_by_role('row')
        expect(rows).to_have_count(2)
        expect(rows.nth(1).get_by_role('cell')).to_have_text([stamp_text,unit,*expected])
    def release_and_fence():
        previous=page.evaluate('() => window.__metricsXMLGate.completed()')
        page.evaluate('() => window.__metricsXMLGate.release()')
        page.wait_for_function('(n) => window.__metricsXMLGate.completed() === n',arg=previous+1,timeout=5000)
    def arm_and_hold():
        page.evaluate('() => window.__metricsXMLGate.arm()')
        page.get_by_role('button',name='Read statistics',exact=True).click()
        page.wait_for_function('() => window.__metricsXMLGate.held()',timeout=5000)
    page.set_viewport_size({'width':1440,'height':1000})
    values=read_values(page,start,end,'All native units')
    expect(values.get_by_role('row')).to_have_count(3)
    expect(values.get_by_role('row').nth(1).get_by_role('cell')).to_have_text([stamp_text,'Bytes','12','24','11','13','2'])
    expect(values.get_by_role('row').nth(2).get_by_role('cell')).to_have_text([stamp_text,'Count','3','6','2','4','2'])
    expect(values).not_to_contain_text('999')
    # Exact Unit=None has no recorded sample; do not invent a zero statistic.
    read_values(page,start,end,'None')
    expect_empty_statistics_table(values)
    read_values(page,start,end,'Count')
    literal_row(values,'Count',['3','6','2','4','2'])
    # Same component, different query: old Count completion must not replace Bytes.
    arm_and_hold()
    read_values(page,start,end,'Bytes')
    literal_row(values,'Bytes',['12','24','11','13','2'])
    release_and_fence()
    literal_row(values,'Bytes',['12','24','11','13','2'])
    expect(values.get_by_role('cell',name='Count',exact=True)).to_have_count(0)
    # Real current request abort: visible failure clears rows, real retry repairs.
    aborted=[]
    def abort_list(route):
        form=parse_qs(route.request.post_data or '')
        if form.get('Action')==['ListMetrics'] and not aborted:
            aborted.append(True);route.abort('failed')
        else:
            route.continue_()
    page.route(native.origin+'/',abort_list)
    try:
        page.get_by_role('button',name='Refresh metrics',exact=True).click()
        expect(page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.',exact=True)).to_be_visible()
        assert len(aborted)==1, 'controlled actual read abort was not observed'
    finally:
        page.unroute(native.origin+'/',abort_list)
    page.get_by_role('tab',name='Browse',exact=True).click()
    expect_empty_metrics_browse_table(page.get_by_role('table',name='Native metrics in current page',exact=True))
    select_metric(page,namespace,metric)
    read_values(page,start,end,'Count')
    literal_row(values,'Count',['3','6','2','4','2'])
    # Region uses a distinct real sample, no expected-empty-only isolation oracle.
    arm_and_hold()
    choose_region(page,'us-east-1','us-west-2')
    expect(page.get_by_role('tab',name='Browse',exact=True)).to_have_attribute('aria-selected','true')
    select_metric(page,namespace,metric)
    read_values(page,start,end,'Count')
    literal_row(values,'Count',['22','44','20','24','2'])
    release_and_fence()
    literal_row(values,'Count',['22','44','20','24','2'])
    # Principal has List permission but actual native statistics denial.
    def actual_metric_response(response, action):
        parsed=urlsplit(response.url)
        return (parsed.scheme+'://'+parsed.netloc==native.origin and parsed.path=='/'
                and response.request.method=='POST'
                and parse_qs(response.request.post_data or '').get('Action')==[action])
    arm_and_hold()
    with page.expect_response(lambda response:actual_metric_response(response,'ListMetrics')) as list_response:
        choose_identity(page,'metrics-console-reader')
    assert list_response.value.status==200, 'selected reader ListMetrics was not actual native200'
    expect(page.get_by_role('tab',name='Browse',exact=True)).to_have_attribute('aria-selected','true')
    select_metric(page,namespace,metric)
    with page.expect_response(lambda response:actual_metric_response(response,'GetMetricStatistics')) as denied_response:
        read_values(page,start,end,'Count')
    assert denied_response.value.status==403, 'selected reader statistics were not actual native403'
    expect(page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.',exact=True)).to_be_visible()
    expect_empty_statistics_table(values)
    release_and_fence()
    expect_empty_statistics_table(values)
    expect(page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.',exact=True)).to_be_visible()
    choose_identity(page,'simulator-admin')
    choose_region(page,'us-west-2','us-east-1')
    # Namespaces do not alias even when the metric and complete dimensions agree.
    select_metric(page,'Simulator/OtherNamespace',metric)
    read_values(page,start,end,'Count')
    literal_row(values,'Count',['777','777','777','777','1'])
    select_metric(page,namespace,metric,'other')
    read_values(page,start,end,'Count')
    literal_row(values,'Count',['999','999','999','999','1'])
    # 501 distinct series, one legal batch. No invented MaxResults/Limit parameter.
    page.get_by_role('tab',name='Browse',exact=True).click()
    page.get_by_label('Namespace (exact, optional)',exact=True).fill(page_namespace)
    page.get_by_label('Metric name (exact, optional)',exact=True).fill('')
    page.get_by_role('button',name='Browse metrics',exact=True).click()
    metrics=page.get_by_role('table',name='Native metrics in current page',exact=True)
    expect(metrics.get_by_role('row')).to_have_count(501)
    first=set(metrics.get_by_role('cell',name=re.compile(r'^Page[0-9]{3}$')).all_text_contents())
    assert len(first)==500 and first < expected_names, 'first page literal metric identities mismatch'
    missing=expected_names-first
    assert len(missing)==1, 'independent fixture must leave exactly one next-page metric'
    missing_name=next(iter(missing))
    next_responses=[]
    def observe_next_page(response):
        try:
            parsed=urlsplit(response.url)
            fields=parse_qs(response.request.post_data or '')
            if (parsed.scheme+'://'+parsed.netloc==native.origin and parsed.path=='/'
                    and response.request.method=='POST' and fields.get('Action')==['ListMetrics']
                    and fields.get('Namespace')==[page_namespace] and fields.get('NextToken')):
                if len(next_responses)<4:
                    next_responses.append(response.status if response.status in (200,400,401,403,404,413,429,500) else 'other')
        except BaseException:
            if len(next_responses)<4:
                next_responses.append('capture-unavailable')
    second=None
    page.on('response',observe_next_page)
    try:
        page.get_by_role('button',name='Next page',exact=True).click()
        # Header+placeholder also has two rows. Await the one independently
        # missing seeded identity, never merely the ambiguous total row count.
        expect(metrics.get_by_role('cell',name=missing_name,exact=True)).to_be_visible(timeout=5000)
        expect(metrics.get_by_role('row')).to_have_count(2)
        second=set(metrics.get_by_role('cell',name=re.compile(r'^Page[0-9]{3}$')).all_text_contents())
        assert len(second)==1 and first.isdisjoint(second) and first|second==expected_names, 'pagination skipped/repeated a metric identity'
    except BaseException:
        record={'phase':'next-page-data-ready','nativeListResponseStatuses':list(next_responses),
                'capturedSecondCount':None if second is None else min(len(second),512),
                'capturedSecondOnlyKnown':None if second is None else second<=expected_names,
                'diagnosticStatus':'UNAVAILABLE'}
        if len(PAGINATION_DIAGNOSTICS)<2:
            PAGINATION_DIAGNOSTICS.append(record)
        try:
            now=metrics.get_by_role('cell',name=re.compile(r'^Page[0-9]{3}$')).all_text_contents()
            record['currentKnownIDCount']=min(sum(value in expected_names for value in now),512)
            record['currentUnexpectedNameCount']=min(sum(value not in expected_names for value in now),512)
            record['expectedMissingCellVisible']=metrics.get_by_role('cell',name=missing_name,exact=True).is_visible()
            record['fixedEmptyPlaceholderVisible']=metrics.get_by_text('No metrics on this page. Publish raw data through the native API first, or change the exact filters.',exact=True).is_visible()
            loading=metrics.get_attribute('aria-busy')
            record['tableAriaBusy']=loading if loading in ('true','false') else 'missing' if loading is None else 'other'
            record['diagnosticStatus']='CAPTURED'
        except BaseException as error:
            record['captureFailureType']=safe_failure_type(error)
        raise
    finally:
        page.remove_listener('response',observe_next_page)
    expect(page.get_by_role('button',name='Next page',exact=True)).to_be_disabled()
    page.get_by_label('Metric name (exact, optional)',exact=True).fill('Page000')
    page.get_by_role('button',name='Browse metrics',exact=True).click()
    zero_row=metrics.get_by_role('row').filter(has=page.get_by_role('cell',name='Page000',exact=True))
    expect(zero_row).to_have_count(1)
    expect(zero_row.get_by_role('cell',name='No dimensions',exact=True)).to_be_visible()
    expect(zero_row.get_by_role('radio')).to_have_accessible_name(page_namespace+': Page000 — No dimensions')
    zero_row.get_by_role('radio').check()
    page.get_by_role('button',name='Graph selected metric',exact=True).click()
    expect(page.get_by_label('Start time (ISO8601 with UTC/offset)',exact=True)).to_be_focused()
    read_values(page,start,end,'Count')
    literal_row(values,'Count',['0','0','0','0','1'])
    select_metric(page,namespace,metric)
    read_values(page,start,end,'Count')
    literal_row(values,'Count',['3','6','2','4','2'])
    # Locale changes presentation only; actual captured identifiers/query survive.
    # Observe actual browser requests, not native fixture counters: changing only
    # labels must not silently reload either Metrics channel or publish data.
    locale_metric_requests=[]
    def observe_locale_request(request):
        parsed=urlsplit(request.url)
        if (parsed.scheme+'://'+parsed.netloc==native.origin and parsed.path=='/'
                and request.method=='POST'):
            action=parse_qs(request.post_data or '').get('Action')
            if action in (['ListMetrics'],['GetMetricStatistics'],['PutMetricData']):
                if len(locale_metric_requests)<4:
                    locale_metric_requests.append(action[0])
    def settled_locale_frame():
        page.evaluate("() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))")
    english_headers=['Timestamp (UTC)','Unit','Average','Sum','Minimum','Maximum','SampleCount']
    japanese_headers=['タイムスタンプ（UTC）','単位','平均 (Average)','合計 (Sum)','最小 (Minimum)','最大 (Maximum)','サンプル数 (SampleCount)']
    expect(values.get_by_role('columnheader')).to_have_text(english_headers)
    expect(page.get_by_label('Unit filter',exact=True)).to_contain_text('Count')
    expect(page.get_by_label('Graph statistic',exact=True)).to_contain_text('Average')
    english_chart=page.get_by_label('Metric statistic by UTC time and unit',exact=True)
    expect(english_chart).to_be_visible()
    expect(english_chart).to_contain_text('Average')
    page.on('request',observe_locale_request)
    page.get_by_label('Language',exact=True).select_option('ja')
    expect(page.get_by_role('tab',name='グラフ化したメトリクス',exact=True)).to_have_attribute('aria-selected','true')
    expect(page.get_by_label('開始日時（UTC/オフセット付き ISO8601）',exact=True)).to_have_value(start)
    expect(page.get_by_label('終了日時（含まない、ISO8601）',exact=True)).to_have_value(end)
    expect(page.get_by_label('期間（秒、60の倍数）',exact=True)).to_have_value('60')
    japanese_values=page.get_by_role('table',name='ネイティブ統計値',exact=True)
    literal_row(japanese_values,'Count',['3','6','2','4','2'])
    expect(japanese_values.get_by_role('columnheader')).to_have_text(japanese_headers)
    expect(page.get_by_label('単位フィルター',exact=True)).to_contain_text('Count')
    expect(page.get_by_label('グラフの統計',exact=True)).to_contain_text('平均 (Average)')
    japanese_chart=page.get_by_label('UTC 時刻・単位ごとのメトリクス統計',exact=True)
    expect(japanese_chart).to_be_visible()
    expect(japanese_chart).to_contain_text('平均 (Average)')
    settled_locale_frame()
    assert locale_metric_requests==[], 'English to Japanese presentation issued a Metrics HTTP request'
    japanese_audits=[]
    for width in (1440,390):
        page.set_viewport_size({'width':width,'height':1000});assert_reflow(page);page.evaluate(axe)
        result=page.evaluate("async () => {const r=await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}});return {violations:r.violations.map(v=>({id:v.id,count:v.nodes.length})),passes:r.passes.length};}")
        assert result['violations']==[], 'Japanese metric accessibility violation; IDs only'
        japanese_audits.append({'width':width,**result})
        page.screenshot(path=str(REPORTS/('cloudwatch-metrics-ja-%s.png'%width)),full_page=True)
    page.get_by_label('言語',exact=True).select_option('en')
    expect(page.get_by_role('tab',name='Graphed metrics',exact=True)).to_have_attribute('aria-selected','true')
    expect(page.get_by_label('Start time (ISO8601 with UTC/offset)',exact=True)).to_have_value(start)
    expect(page.get_by_label('End time (exclusive, ISO8601)',exact=True)).to_have_value(end)
    expect(page.get_by_label('Period (seconds, multiple of 60)',exact=True)).to_have_value('60')
    english_values=page.get_by_role('table',name='Native statistic values',exact=True)
    literal_row(english_values,'Count',['3','6','2','4','2'])
    expect(english_values.get_by_role('columnheader')).to_have_text(english_headers)
    expect(page.get_by_label('Unit filter',exact=True)).to_contain_text('Count')
    expect(page.get_by_label('Graph statistic',exact=True)).to_contain_text('Average')
    expect(english_chart).to_be_visible()
    expect(english_chart).to_contain_text('Average')
    settled_locale_frame()
    assert locale_metric_requests==[], 'Loaded EN/JA/EN presentation issued a Metrics HTTP request'
    page.remove_listener('request',observe_locale_request)
    # Peer browser has distinct session/origin and real data, not an empty list.
    peer_context=browser.new_context(viewport={'width':1440,'height':1000})
    peer_primary=None
    try:
        peer_outside,peer_errors=[],[]
        peer_page=peer_context.new_page()
        peer_page.on('pageerror',lambda error:peer_errors.append(type(error).__name__))
        def peer_guard(route):
            parsed=urlsplit(route.request.url)
            if parsed.scheme+'://'+parsed.netloc==peer.origin:
                route.continue_()
            else:
                peer_outside.append(True);route.abort()
        peer_page.route('**/*',peer_guard)
        peer_page.goto(peer.origin+'/_simulator/console/')
        peer_page.get_by_label('Access token',exact=True).fill(peer.token)
        peer_page.get_by_role('button',name='Open sandbox',exact=True).click()
        peer_page.get_by_role('button',name='Services',exact=True).click()
        peer_page.get_by_role('menuitem',name='CloudWatch Metrics',exact=True).click()
        select_metric(peer_page,namespace,metric)
        peer_values=read_values(peer_page,start,end,'Count')
        literal_row(peer_values,'Count',['101','202','100','102','2'])
        expect(peer_values.get_by_role('cell',name='3',exact=True)).to_have_count(0)
        assert not peer_outside and not peer_errors, 'peer browser made unexpected external request/page error'
    except BaseException as error:
        peer_primary=error;raise
    finally:
        try:
            close_marked(peer_context.close,'peerContext',cleanup,failures)
        except BaseException:
            if peer_primary is not None:raise peer_primary
            raise
    literal_row(page.get_by_role('table',name='Native statistic values',exact=True),'Count',['3','6','2','4','2'])
    assert before == (frame(Path(directory)/'a.db'),frame(Path(directory)/'b.db')), 'read-only UI/error/isolation path changed a complete persisted table frame'
    assert page.evaluate('() => window.__metricsXMLGate.completed()') == 3, 'not all three real retired XML completions reached the render fence'
    return {'readOnlyFrames':True,'realReadAbortAndRepair':1,'lateXMLCompletions':3,
            'sameMountUnitGuard':True,'regionDistinctResult':True,'principalListAllowedStatisticsDenied':True,
            'crossTokens401':2,'distinctTwoDBUIRead':True,'namespaceDimensionIdentity':True,
            'unitsNeverMerged':True,'actualZeroSample':True,'absentUnitNotZero':True,'paginationSeries':501,
            'jaQueryPreserved':True,'loadedLocaleRoundTripPreserved':True,'localeMetricHTTPRequests':len(locale_metric_requests),'japaneseAudits':japanese_audits,'fixtureFulfillment':False,'fixtureFulfillmentScope':'original lifecycle path only; labelled boundary transport fault recorded separately'}


def exercise_point_boundary(page, native, directory, failures):
    """Actual native1440; labelled transport fault1441; distinct native400.

    The transport test fetches a real valid native response and inserts exactly
    one explicitly synthetic, otherwise valid unique point. It does NOT claim
    the native producer returned1441 points or replace the capability response.
    """
    namespace, metric, dimension = 'Simulator/PointBoundaryAcceptance', 'DummyPointBoundary', 'point-boundary'
    first = datetime.now(timezone.utc).replace(second=0,microsecond=0) - timedelta(minutes=722)
    end = first + timedelta(minutes=720)
    start_text, end_text = first.isoformat(), end.isoformat()
    stamp_text = lambda dt: dt.isoformat().replace('+00:00','Z')
    samples = [(first+timedelta(minutes=i),unit,value) for i in range(720)
               for unit,value in [('Count',1),('Bytes',10)]]
    assert len(samples)==1440
    # Two permitted native batches, not a second fixture or a DB shortcut.
    for chunk in (samples[:1000],samples[1000:]):
        form={'Action':'PutMetricData','Version':'2010-08-01','Namespace':namespace}
        for i,(stamp,unit,value) in enumerate(chunk,1):
            prefix='MetricData.member.%s.'%i
            form.update({prefix+'MetricName':metric,prefix+'Value':str(value),prefix+'Timestamp':stamp.isoformat(),
                         prefix+'Unit':unit,prefix+'Dimensions.member.1.Name':'Fixture',
                         prefix+'Dimensions.member.1.Value':dimension})
        assert len(urlencode(form).encode())<=1048576
        assert ET.fromstring(native.send('/',form)).tag==XML+'PutMetricDataResponse'
    query={'Action':'GetMetricStatistics','Version':'2010-08-01','Namespace':namespace,'MetricName':metric,
           'StartTime':start_text,'EndTime':end_text,'Period':'60',
           'Dimensions.member.1.Name':'Fixture','Dimensions.member.1.Value':dimension}
    for i,name in enumerate(('Average','Sum','Minimum','Maximum','SampleCount'),1):
        query['Statistics.member.%s'%i]=name
    expected_keys={(stamp_text(stamp),unit) for stamp,unit,_ in samples}
    def native_points(raw):
        assert native.token.encode() not in raw
        root=ET.fromstring(raw)
        assert root.tag==XML+'GetMetricStatisticsResponse'
        result=root.find(XML+'GetMetricStatisticsResult')
        assert result is not None and result.findtext(XML+'Label')==metric
        container=result.find(XML+'Datapoints')
        assert container is not None
        points=container.findall(XML+'member')
        assert len(points)==1440, 'real native boundary reply must contain1440 points'
        keys=set()
        for point in points:
            assert {n.tag for n in point}=={XML+n for n in ['Timestamp','Unit','Average','Sum','Minimum','Maximum','SampleCount']}
            unit=point.findtext(XML+'Unit');key=(point.findtext(XML+'Timestamp'),unit)
            assert key not in keys;keys.add(key)
            assert unit in ('Count','Bytes')
            value='1' if unit=='Count' else '10'
            assert [point.findtext(XML+n) for n in ['Average','Sum','Minimum','Maximum','SampleCount']]==[value,value,value,value,'1']
        assert keys==expected_keys
        return root,container,points
    raw=native.send('/',query)
    native_points(raw)
    before=(frame(Path(directory)/'a.db'),frame(Path(directory)/'b.db'))
    page.set_viewport_size({'width':1440,'height':1000})
    select_metric(page,namespace,metric,dimension)
    with page.expect_response(lambda response: is_boundary_response(response,native.origin,namespace,metric)) as accepted:
        values=read_values(page,start_text,end_text,'All native units')
    assert accepted.value.status==200
    expect(values.get_by_role('row')).to_have_count(51)
    expect(values.get_by_role('row').nth(1).get_by_role('cell')).to_have_text([stamp_text(first),'Bytes','10','10','10','10','1'])
    expect(values.get_by_role('row').nth(2).get_by_role('cell')).to_have_text([stamp_text(first),'Count','1','1','1','1','1'])
    # Existing local table pagination:1440rows is29pages, finalpage40rows.
    pages=page.get_by_role('list',name='Statistic pages',exact=True)
    pages.get_by_role('button',name='29',exact=True).click()
    expect(values.get_by_role('row')).to_have_count(41)
    expect(values.get_by_role('row').last.get_by_role('cell')).to_have_text([stamp_text(first+timedelta(minutes=719)),'Count','1','1','1','1','1'])
    fault={'matched':0,'fetches':0,'fulfilled':0,'nativePoints':0,'syntheticPoints':0,
           'inverseSpliceExact':False,'responseDisposed':False}
    def transport_overflow(route):
        if not is_boundary_request(route.request,native.origin,namespace,metric):
            route.continue_();return
        fault['matched']+=1
        assert fault['matched']==1, 'only one labelled transport fault is allowed'
        response=None;primary=None
        try:
            fault['fetches']+=1
            response=route.fetch(max_redirects=0,max_retries=0,timeout=5000)
            assert response.status==200, 'fault requires an actual valid native200'
            body=response.body();assert len(body)<=2097152
            native_points(body);fault['nativePoints']=1440;fault['nativeHTTPStatus']=response.status
            assert 'Unit' not in parse_qs(route.request.post_data or ''), 'transport case requires the real All native units request'
            # None is a valid third unit at the same native minute bucket. It
            # passes range/unit/finite-value/uniqueness checks; only total count
            # should reject the explicitly injected transport reply.
            extra_time=stamp_text(first)
            assert (extra_time,'None') not in expected_keys
            extra=(f'<member><Timestamp>{extra_time}</Timestamp><SampleCount>1</SampleCount>'
                   '<Average>42</Average><Sum>42</Sum><Minimum>42</Minimum><Maximum>42</Maximum><Unit>None</Unit></member>').encode()
            closing=b'</Datapoints>'
            assert body.count(closing)==1
            mutated=body.replace(closing,extra+closing,1)
            assert len(mutated)<=2097152 and mutated.replace(extra+closing,closing,1)==body
            parsed=ET.fromstring(mutated)
            members=parsed.find(XML+'GetMetricStatisticsResult').find(XML+'Datapoints').findall(XML+'member')
            assert len(members)==1441
            keys={(p.findtext(XML+'Timestamp'),p.findtext(XML+'Unit')) for p in members}
            assert len(keys)==1441 and keys==expected_keys|{(extra_time,'None')}
            fault.update({'syntheticPoints':1441,'inverseSpliceExact':True,
                          'nativeBodySHA256':hashlib.sha256(body).hexdigest(),
                          'mutatedBodySHA256':hashlib.sha256(mutated).hexdigest()})
            headers={k:v for k,v in response.headers.items()
                     if k.lower() not in ('content-length','content-encoding','transfer-encoding','connection')}
            headers['content-type']='text/xml; charset=utf-8'
            headers['x-simulator-test-transport-fault']='metric-point-overflow'
            route.fulfill(status=200,headers=headers,body=mutated)
            fault['fulfilled']+=1
        except BaseException as error:
            primary=error
            fault['failureType']=type(error).__name__ if type(error).__name__ in ('AssertionError','Error','TimeoutError','KeyboardInterrupt') else 'Other'
            if not fault['fulfilled']:
                try:
                    route.abort('failed');fault['failedRouteAborted']=True
                except BaseException:
                    fault['failedRouteAbortError']=True;failures.append('RouteAbortCleanup')
            raise
        finally:
            if response is not None:
                try:
                    response.dispose();fault['responseDisposed']=True
                except BaseException:
                    fault['responseDisposeFailed']=True;failures.append('ResponseDisposeCleanup')
                    if primary is None:
                        raise
    page.route(native.origin+'/',transport_overflow)
    try:
        with page.expect_response(lambda response:is_boundary_response(response,native.origin,namespace,metric)) as injected:
            page.get_by_role('button',name='Read statistics',exact=True).click()
        assert injected.value.status==200 and injected.value.headers.get('x-simulator-test-transport-fault')=='metric-point-overflow'
        expect(page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.',exact=True)).to_be_visible()
        expect_empty_statistics_table(values)
        expect(page.get_by_text('No datapoints loaded. Read statistics; missing data is not zero.',exact=True)).to_be_visible()
        expect(page.get_by_text(re.compile('^Last completed read'))).to_have_count(0)
        expect(page.get_by_role('button',name='Read statistics',exact=True)).to_be_enabled()
        assert fault['matched']==fault['fetches']==fault['fulfilled']==1 and fault['nativePoints']==1440 and fault['syntheticPoints']==1441
        assert fault['inverseSpliceExact'] and fault['responseDisposed']
    finally:
        page.unroute(native.origin+'/',transport_overflow)
    with page.expect_response(lambda response:is_boundary_response(response,native.origin,namespace,metric)) as repaired:
        page.get_by_role('button',name='Read statistics',exact=True).click()
    assert repaired.value.status==200 and 'x-simulator-test-transport-fault' not in repaired.value.headers
    expect(page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.',exact=True)).to_have_count(0)
    expect(values.get_by_role('row')).to_have_count(51)
    expect(page.get_by_text(re.compile('^Last completed read'))).to_have_count(1)
    assert before==(frame(Path(directory)/'a.db'),frame(Path(directory)/'b.db')), 'native1440/transportfault/recovery reads changed saved state'
    # A separate, genuine native limit case. One explicit setup write adds a
    # third unit at an existing time; native1441groups must return400, not200.
    peer_before=frame(Path(directory)/'b.db')
    native.put(namespace,metric,(42,),start_text,unit='None',dimension=dimension)
    after_write=(frame(Path(directory)/'a.db'),frame(Path(directory)/'b.db'))
    assert after_write[1]==peer_before
    refused=native.send('/',query,status=400)
    root=ET.fromstring(refused)
    assert root.tag==XML+'ErrorResponse' and root.findtext(XML+'Error/'+XML+'Code')=='InvalidParameterValue'
    assert root.findtext(XML+'Error/'+XML+'Message')=='At most 1440 unit/time datapoints may be returned.'
    assert root.find('.//'+XML+'Datapoints') is None
    with page.expect_response(lambda response:is_boundary_response(response,native.origin,namespace,metric)) as native_refusal:
        page.get_by_role('button',name='Read statistics',exact=True).click()
    assert native_refusal.value.status==400
    expect(page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.',exact=True)).to_be_visible()
    expect_empty_statistics_table(values)
    with page.expect_response(lambda response:is_boundary_response(response,native.origin,namespace,metric)) as count_repair:
        read_values(page,start_text,end_text,'Count')
    assert count_repair.value.status==200
    expect(values.get_by_role('row')).to_have_count(51)
    expect(values.get_by_role('row').nth(1).get_by_role('cell')).to_have_text([stamp_text(first),'Count','1','1','1','1','1'])
    pages.get_by_role('button',name='15',exact=True).click()
    expect(values.get_by_role('row')).to_have_count(21)
    expect(values.get_by_role('row').last.get_by_role('cell')).to_have_text([stamp_text(first+timedelta(minutes=719)),'Count','1','1','1','1','1'])
    count_result=ET.fromstring(native.send('/',{**query,'Unit':'Count'}))
    assert len(count_result.find(XML+'GetMetricStatisticsResult').find(XML+'Datapoints').findall(XML+'member'))==720
    page.get_by_label('Unit filter',exact=True).click();page.get_by_role('option',name='All native units',exact=True).click()
    page.get_by_label('Period (seconds, multiple of 60)',exact=True).fill('120')
    with page.expect_response(lambda response:is_boundary_response(response,native.origin,namespace,metric)) as period_repair:
        page.get_by_role('button',name='Read statistics',exact=True).click()
    assert period_repair.value.status==200
    expect(values.get_by_role('row')).to_have_count(51)
    expect(values.get_by_role('row').nth(1).get_by_role('cell')).to_have_text([stamp_text(first),'Bytes','10','20','10','10','2'])
    expect(values.get_by_role('row').nth(2).get_by_role('cell')).to_have_text([stamp_text(first),'Count','1','2','1','1','2'])
    expect(values.get_by_role('row').nth(3).get_by_role('cell')).to_have_text([stamp_text(first),'None','42','42','42','42','1'])
    pages.get_by_role('button',name='15',exact=True).click()
    expect(values.get_by_role('row')).to_have_count(22)
    period_result=ET.fromstring(native.send('/',{**query,'Period':'120'}))
    assert len(period_result.find(XML+'GetMetricStatisticsResult').find(XML+'Datapoints').findall(XML+'member'))==721
    assert after_write==(frame(Path(directory)/'a.db'),frame(Path(directory)/'b.db')), 'realnative1441refusal/readrepairs changed saved state'
    return {'actualNative1440Points200':True,'actualUI1440FirstAndLastPage':True,
            'syntheticTransport1441ClientRejected':True,'nativeDidNotProduceSynthetic1441':True,
            'transportFault':fault,'native1440ReadRecovery':True,'actualNative1441Groups400':True,
            'countFilter720Recovery':True,'period120AllUnits721Recovery':True,'allReadFramesUnchanged':True,
            'runtimeQualification':'Test results only when executed; source preparation is not a pass.'}


def is_boundary_request(request,origin,namespace,metric):
    parsed=urlsplit(request.url)
    fields=parse_qs(request.post_data or '')
    return (parsed.scheme+'://'+parsed.netloc==origin and parsed.path=='/' and request.method=='POST'
            and fields.get('Action')==['GetMetricStatistics'] and fields.get('Namespace')==[namespace]
            and fields.get('MetricName')==[metric])


def is_boundary_response(response,origin,namespace,metric):
    return is_boundary_request(response.request,origin,namespace,metric)


def safe_failure_type(error):
    name=type(error).__name__
    return name if name in ('AssertionError','Error','TimeoutError','KeyboardInterrupt','OSError') else 'Other'


def safe_owned_frames(error):
    allowed={Path(__file__).resolve(),Path(__file__).with_name('reflow.py').resolve()}
    frames=[];current=error.__traceback__
    while current is not None:
        code=current.tb_frame.f_code
        if Path(code.co_filename).resolve() in allowed:
            frames.append({'function':code.co_name,'line':current.tb_lineno})
        current=current.tb_next
    return frames[-16:]


def close_marked(close,name,cleanup,failures):
    try:
        close();cleanup[name]=True
    except BaseException as error:
        failures.append(safe_failure_type(error));raise




def expect_empty_statistics_table(table):
    # Cloudscape renders a header row AND one NoDataCell row for an empty table.
    # Exact shape + literal placeholder forbids an old/synthetic datapoint row.
    expect(table.get_by_role('row')).to_have_count(2)
    expect(table.get_by_role('columnheader')).to_have_count(7)
    expect(table.get_by_role('cell')).to_have_count(1)
    expect(table.get_by_role('cell')).to_have_text(['No returned datapoints.'])
    expect(table.get_by_text('No returned datapoints.',exact=True)).to_be_visible()


def expect_empty_metrics_browse_table(table):
    # Browse has three data columns plus its radio-selection header. Its sole
    # body cell is the actual native-list empty placeholder, not a data record.
    expect(table.get_by_role('row')).to_have_count(2)
    expect(table.get_by_role('columnheader')).to_have_count(4)
    expect(table.get_by_role('cell')).to_have_count(1)
    expect(table.get_by_role('cell')).to_have_text(['No metrics on this page. Publish raw data through the native API first, or change the exact filters.'])
    expect(table.get_by_text('No metrics on this page. Publish raw data through the native API first, or change the exact filters.',exact=True)).to_be_visible()


def record_invalid_period_table_diagnostic(page, table, diagnostics):
    # Only closed counts/known-literal booleans leave the browser. Never retain
    # DOM, selector-derived text, field values, headers, tokens or exception text.
    record={'phase':'invalid-period-empty-table','diagnosticStatus':'UNAVAILABLE'}
    diagnostics['invalidPeriodTable']=record
    try:
        observed=table.evaluate("""table => {
          const rows=[...table.querySelectorAll('tr')];
          const cells=[...table.querySelectorAll('td')];
          const headers=[...table.querySelectorAll('th')];
          const gold=['Count','3','6','2','4','2'];
          return {domRows:rows.length,domCells:cells.length,domColumnHeaders:headers.length,
            domHeaderRows:table.querySelectorAll('thead tr').length,
            oldGoldRowPresent:rows.some(row => {
              const values=[...row.querySelectorAll('td')].map(cell=>cell.textContent.trim());
              return values.length===7 && values.slice(1).every((value,index)=>value===gold[index]);
            })};
        }""")
        for name in ('domRows','domCells','domColumnHeaders','domHeaderRows'):
            value=observed[name]
            record[name]=min(value,128)
            record[name+'Capped']=value>128
        record['accessibleRowCount']=min(table.get_by_role('row').count(),128)
        record['accessibleCellCount']=min(table.get_by_role('cell').count(),128)
        record['accessibleColumnHeaderCount']=min(table.get_by_role('columnheader').count(),128)
        record['fixedEmptyTextVisible']=table.get_by_text('No returned datapoints.',exact=True).is_visible()
        record['oldGoldRowPresent']=observed['oldGoldRowPresent'] is True
        record['loadedQueryMarkerPresent']=page.get_by_text(re.compile(r'^Last completed read:')).count()>0
        record['fixedReadErrorVisible']=page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.',exact=True).is_visible()
        viewport=page.viewport_size or {}
        record['width']=viewport.get('width') if viewport.get('width') in (1440,390) else None
        record['diagnosticStatus']='CAPTURED'
    except BaseException as error:
        record['captureFailureType']=safe_failure_type(error)
    try:
        screenshot=REPORTS/'metrics-invalid-period-empty-diagnostic.png'
        page.screenshot(path=str(screenshot),full_page=True,timeout=2000)
        record['dummyScreenshot']=screenshot.name
    except BaseException as error:
        record['screenshotFailureType']=safe_failure_type(error)


def _run_native_acceptance(cleanup, failures, diagnostics):
    # Fixed dummy data; real authenticated native Put, not in-browser invented data.
    stamp = datetime.now(timezone.utc).replace(second=0, microsecond=0) - timedelta(minutes=2)
    start, end = stamp.isoformat(), (stamp + timedelta(minutes=1)).isoformat()
    namespace, metric = 'Simulator/ConsoleAcceptance', 'DummyRequests'
    form = {'Action': 'PutMetricData', 'Version': '2010-08-01', 'Namespace': namespace}
    for i, (value, dimension) in enumerate(((2, 'selected'), (4, 'selected'), (999, 'other')), 1):
        prefix = 'MetricData.member.%s.' % i
        form.update({prefix+'MetricName': metric, prefix+'Value': str(value),
                     prefix+'Timestamp': start, prefix+'Unit': 'Count',
                     prefix+'Dimensions.member.1.Name': 'Fixture',
                     prefix+'Dimensions.member.1.Value': dimension})
    axe_path = Path(os.environ['AXE_SCRIPT'])
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == 'e9e5863c33a874f09bc01acd9234b7e3c871479f5eef8802fa582544465e6d01'
    axe = axe_path.read_text()
    reports = []
    stack=ExitStack();primary=None;result=None;entered=set()
    try:
        directory=stack.enter_context(TemporaryDirectory(prefix='metrics-console-owned-'));entered.add('temporaryDirectory')
        endpoint,token=stack.enter_context(running(Path(directory)/'a.db',authenticated=True));entered.add('cliA')
        peer_endpoint,peer_token=stack.enter_context(running(Path(directory)/'b.db',authenticated=True));entered.add('cliB')
        playwright=stack.enter_context(sync_playwright());entered.add('playwright')
        native, peer = Native(endpoint, token), Native(peer_endpoint, peer_token)
        peer.put(namespace, metric, (100,102), start)
        native.put(namespace, metric, (20,24), start, region='us-west-2')
        native.put('Simulator/OtherNamespace', metric, (777,), start)
        origin = urlsplit(endpoint)
        assert origin.scheme == 'http' and origin.hostname == '127.0.0.1' and origin.port
        assert not origin.username and not origin.password and not origin.query and not origin.fragment
        wire = Request(endpoint+'/', data=urlencode(form).encode(), method='POST', headers={
            'Content-Type':'application/x-www-form-urlencoded', 'X-Simulator-Token':token,
            'X-Simulator-Principal':'simulator-admin', 'X-Simulator-Service':'monitoring',
            'X-Simulator-Region':'us-east-1'})
        with build_opener(ProxyHandler({}), NoRedirect()).open(wire, timeout=5) as response:
            assert response.status == 200, 'native seed refused; body redacted'
            raw = response.read(1048577)
            assert len(raw) <= 1048576 and token.encode() not in raw
            assert ET.fromstring(raw).tag == XML+'PutMetricDataResponse'
        browser = playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
        stack.callback(close_marked,browser.close,'browser',cleanup,failures)
        context = browser.new_context(viewport={'width':1440,'height':1000})
        stack.callback(close_marked,context.close,'contextA',cleanup,failures)
        context.add_init_script(XML_GATE)
        outside, errors = [], []
        page = context.new_page()
        page.on('pageerror', lambda error: errors.append(type(error).__name__))
        def local_only(route):
            parsed = urlsplit(route.request.url)
            if parsed.scheme == origin.scheme and parsed.netloc == origin.netloc:
                route.continue_()
            else:
                outside.append(True)
                route.abort()
        page.route('**/*', local_only)
        page.goto(endpoint+'/_simulator/console/')
        page.get_by_label('Access token', exact=True).fill(token)
        page.get_by_role('button', name='Open sandbox', exact=True).click()
        page.get_by_role('button', name='Services', exact=True).click()
        page.get_by_role('menuitem', name='CloudWatch Metrics', exact=True).click()
        expect(page.get_by_role('heading', name='Metrics', exact=True)).to_be_visible()
        expect(page.get_by_text(re.compile('^Unofficial local CloudWatch Metrics simulator'))).to_be_visible()
        for width in (1440, 390):
            page.set_viewport_size({'width':width, 'height':1000})
            page.get_by_role('tab', name='Browse', exact=True).click()
            page.get_by_label('Namespace (exact, optional)', exact=True).fill(namespace)
            page.get_by_label('Metric name (exact, optional)', exact=True).fill(metric)
            page.get_by_role('button', name='Browse metrics', exact=True).click()
            table = page.get_by_role('table', name='Native metrics in current page', exact=True)
            row = table.get_by_role('row').filter(has_text='Fixture=selected')
            expect(row).to_have_count(1)
            expect(table.get_by_role('row').filter(has_text='Fixture=other')).to_have_count(1)
            expect(row.get_by_role('radio')).to_have_accessible_name(namespace+': '+metric+' — Fixture=selected')
            expect(table.get_by_role('row').filter(has_text='Fixture=other').get_by_role('radio')).to_have_accessible_name(namespace+': '+metric+' — Fixture=other')
            row.get_by_role('radio').check()
            page.get_by_role('button', name='Graph selected metric', exact=True).click()
            expect(page.get_by_label('Start time (ISO8601 with UTC/offset)', exact=True)).to_be_focused()
            page.get_by_label('Start time (ISO8601 with UTC/offset)', exact=True).fill(start)
            page.get_by_label('End time (exclusive, ISO8601)', exact=True).fill(end)
            page.get_by_label('Period (seconds, multiple of 60)', exact=True).fill('60')
            page.get_by_role('button', name='Read statistics', exact=True).click()
            values = page.get_by_role('table', name='Native statistic values', exact=True)
            expect(values.get_by_role('row')).to_have_count(2)
            # Column order is timestamp,unit,Average,Sum,Minimum,Maximum,SampleCount.
            expect(values.get_by_role('row').nth(1).get_by_role('cell')).to_have_text([
                stamp.isoformat().replace('+00:00','Z'), 'Count', '3', '6', '2', '4', '2'])
            expect(values).not_to_contain_text('999')
            expect(page.get_by_role('button', name='Read statistics', exact=True)).to_be_enabled()
            # Invalid read must clear old results and offer a genuine read retry.
            page.get_by_label('Period (seconds, multiple of 60)', exact=True).fill('61')
            page.get_by_role('button', name='Read statistics', exact=True).click()
            try:
                expect_empty_statistics_table(values)
            except BaseException:
                record_invalid_period_table_diagnostic(page, values, diagnostics)
                raise
            expect(page.get_by_text(re.compile('^Last completed read'))).to_have_count(0)
            expect(page.get_by_text('The metric request failed. Refresh or retry the read; no metric data was changed.', exact=True)).to_be_visible()
            page.get_by_label('Period (seconds, multiple of 60)', exact=True).fill('60')
            page.get_by_role('button', name='Read statistics', exact=True).click()
            expect(values.get_by_role('row')).to_have_count(2)
            assert_reflow(page)
            page.evaluate(axe)
            result = page.evaluate("async () => { const r=await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}}); return {violations:r.violations.map(v=>({id:v.id,count:v.nodes.length})),passes:r.passes.length}; }")
            assert result['violations'] == [], 'metric accessibility violations; details saved as IDs only'
            reports.append({'width':width, **result})
            page.screenshot(path=str(REPORTS/('cloudwatch-metrics-%s.png'%width)), full_page=True)
        strengthened = exercise_lifetimes(page, context, browser, native, peer, directory, namespace, metric, start, end, stamp, axe, cleanup, failures)
        point_boundary = exercise_point_boundary(page, native, directory, failures)
        assert not outside and not errors, 'unexpected external request or page error; contents redacted'
        assert native.calls == native.responses and peer.calls == peer.responses
        result={'screens':reports,'strengthened':strengthened,'pointBoundary':point_boundary,
                'capabilityOrPresentationFulfillmentUsed':False,
                'labelledTransportFaultFulfillments':point_boundary['transportFault']['fulfilled'],
                'nativeCalls':native.calls+peer.calls+1+point_boundary['transportFault']['fetches'],
                'nativeResponses':native.responses+peer.responses+1+point_boundary['transportFault']['fetches'],
                'nativeCounterScope':'Actual setup/control HTTP plus original native seed and single real transport forward; browser-origin calls separately asserted, not SDKcall/send parity.',
                'nativeOperations':{op:native.operations.get(op,0)+peer.operations.get(op,0)+(1 if op=='PutMetricData' else 0)+(point_boundary['transportFault']['fetches'] if op=='GetMetricStatistics' else 0) for op in ['PutMetricData','ListMetrics','GetMetricStatistics','HostPolicySetup']}}
    except BaseException as error:
        primary=error
    finally:
        try:
            stack.close()
            cleanup.update({name:True for name in entered})
        except BaseException as error:
            failures.append(safe_failure_type(error))
            if primary is None:primary=error
    if primary is not None:raise primary
    return result


def run():
    report=REPORTS/'cloudwatch-metrics-a11y.json'
    assert not report.exists(), 'A fresh Metrics reports directory is required; stale receipt preserved'
    helper=Path(__file__).resolve()
    binary_before=hashlib.sha256(BINARY.read_bytes()).hexdigest()
    helper_before=hashlib.sha256(helper.read_bytes()).hexdigest()
    expected_binary=os.environ.get('SIMULATOR_BINARY_SHA256')
    if expected_binary:assert expected_binary==binary_before, 'Expected Metrics binary identity mismatch'
    cleanup={name:False for name in ['contextA','peerContext','browser','cliA','cliB','playwright','temporaryDirectory']}
    UNIT_DIAGNOSTICS.clear();PAGINATION_DIAGNOSTICS.clear()
    failures=[];diagnostics={'unitSelect':UNIT_DIAGNOSTICS,'pagination':PAGINATION_DIAGNOSTICS};started=time.monotonic()
    receipt={'status':'RUNNING','binarySHA256':binary_before,'helperSHA256':helper_before,
             'binaryPostExact':False,'helperPostExact':False,'cleanup':False,'cleanupDetails':cleanup}
    report.write_text(json.dumps(receipt,indent=2)+chr(10))
    primary=None
    try:
        data=_run_native_acceptance(cleanup,failures,diagnostics)
        assert all(cleanup.values()), 'Owned Metrics runtime cleanup was not verified'
        assert not failures, 'Secondary Metrics cleanup failure was recorded'
        assert data['nativeCalls']==data['nativeResponses']==17, 'Native control HTTP counters mismatched'
        assert data['nativeOperations']=={'PutMetricData':9,'ListMetrics':2,'GetMetricStatistics':5,'HostPolicySetup':1}
        assert sorted(screen['width'] for screen in data['screens'])==[390,1440]
        st,point=data['strengthened'],data['pointBoundary']
        counters={'lateXMLCompletions':st['lateXMLCompletions'],'crossTokens401':st['crossTokens401'],
                  'paginationSeries':st['paginationSeries'],'nativeAcceptedPoints':point['transportFault']['nativePoints'],
                  'syntheticRejectedPoints':point['transportFault']['syntheticPoints'],
                  'nativeRejectedGroups':1441 if point['actualNative1441Groups400'] else 0,
                  'countRecoveryPoints':720 if point['countFilter720Recovery'] else 0,
                  'period120RecoveryPoints':721 if point['period120AllUnits721Recovery'] else 0,
                  'transportFetches':point['transportFault']['fetches'],'transportFulfills':point['transportFault']['fulfilled'],
                  'desktopMobileWidths':len({screen['width'] for screen in data['screens']})}
        assert counters=={'lateXMLCompletions':3,'crossTokens401':2,'paginationSeries':501,'nativeAcceptedPoints':1440,
                          'syntheticRejectedPoints':1441,'nativeRejectedGroups':1441,'countRecoveryPoints':720,
                          'period120RecoveryPoints':721,'transportFetches':1,'transportFulfills':1,'desktopMobileWidths':2}
        receipt.update(data,caseCounters=counters)
    except BaseException as error:
        primary=error
    def post_hash(path):
        try:return hashlib.sha256(path.read_bytes()).hexdigest()
        except OSError:
            failures.append('PostguardReadFailure');return None
    binary_after=post_hash(BINARY)
    helper_after=post_hash(helper)
    receipt.update(binarySHA256After=binary_after,helperSHA256After=helper_after,
                   binaryPostExact=binary_after==binary_before,helperPostExact=helper_after==helper_before,
                   cleanup=all(cleanup.values()),cleanupDetails=cleanup,secondaryCleanupTypes=failures,
                   elapsedSeconds=round(time.monotonic()-started,3),diagnostics=diagnostics)
    if primary is None and not (receipt['binaryPostExact'] and receipt['helperPostExact'] and receipt['cleanup'] and not failures):
        primary=AssertionError('Metrics immutable input or cleanup postguard failed')
    receipt['status']='PASS' if primary is None else 'FAIL'
    if primary is not None:
        receipt['failureType']=safe_failure_type(primary)
        receipt['failureFrames']=safe_owned_frames(primary)
    report.write_text(json.dumps(receipt,indent=2)+chr(10))
    if primary is not None:
        raise AssertionError('Metrics acceptance failed; see the redacted receipt') from None


if __name__ == '__main__':
    run()
