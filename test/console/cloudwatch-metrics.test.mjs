import test from 'node:test';
import assert from 'node:assert/strict';
import {alarmPageSize, cloudWatchMetrics} from '../../tools/console/src/cloudwatch-metrics.mjs';
import {serviceAvailable} from '../../tools/console/src/console-navigation.mjs';

const ns = 'http://monitoring.amazonaws.com/doc/2010-08-01/';
const region = 'us-east-1';
function element(localName, ...children) {
  const leaf = typeof children[0] === 'string';
  return {localName, namespaceURI: ns, textContent: leaf ? children[0] : '', children: leaf ? [] : children};
}
function envelope(action, ...nodes) {
  return {documentElement: element(action + 'Response', element(action + 'Result', ...nodes),
    element('ResponseMetadata', element('RequestId','local-cloudwatch')))};
}
const start='2026-11-28T12:01:00Z', end='2026-11-28T12:02:00Z';
const name='size', metric='QueueSize';
const alarm = element('member', ...Object.entries({
  AlarmName:name,AlarmArn:'arn:aws:cloudwatch:us-east-1:000000000000:alarm:'+name,
  StateValue:'OK',Statistic:'Average',Period:'60',Namespace:'Training/Test',MetricName:metric,Unit:'Count',
  ComparisonOperator:'LessThanThreshold',Threshold:'3',StateReason:'Local evaluator',StateReasonData:JSON.stringify({
    Start:start,End:end,Statistic:'Average'}),
}).map(([key,v])=>element(key,v)), element('Dimensions',element('member',element('Name','Node'),element('Value','west'))));
function setup(document) {
  const sent=[];
  const api=cloudWatchMetrics(async (path, options) => {
    sent.push({path,...options});
    return new Response('<placeholder/>',{headers:{'Content-Type':'text/xml; charset=utf-8'}});
  },region,()=>document);
  return {sent,api};
}

test('named read preserves the canonical resource and queries its exact evaluated series',async()=>{
  const signal=new AbortController().signal;
  const {api,sent}=setup(envelope('DescribeAlarms',element('MetricAlarms',alarm)));
  const a=await api.named(name,signal);
  assert.deepEqual(Object.fromEntries(sent[0].body),{Action:'DescribeAlarms',Version:'2010-08-01','AlarmNames.member.1':name});
  const points=setup(envelope('GetMetricStatistics',element('Label',metric),element('Datapoints',
    element('member',element('Unit','Count'),element('Timestamp',start),element('Average','0')))));
  assert.equal(await points.api.measurement(a,signal),0);
  assert.deepEqual(Object.fromEntries(points.sent[0].body),{Action:'GetMetricStatistics',Version:'2010-08-01',
    Namespace:'Training/Test',MetricName:metric,Unit:'Count',Period:'60',StartTime:'2026-11-28T12:01:00.000Z',
    EndTime:'2026-11-28T12:02:00.000Z','Statistics.member.1':'Average','Dimensions.member.1.Name':'Node','Dimensions.member.1.Value':'west'});
  assert.equal(points.sent[0].path,'/');
  assert.equal(points.sent[0].headers['X-Simulator-Service'],'monitoring');
  assert.equal(points.sent[0].headers['X-Simulator-Region'],region);
  const blank=setup(envelope('GetMetricStatistics',element('Label',metric),element('Datapoints')));
  assert.equal(await blank.api.measurement(a,signal),null);
  const wrong=setup(envelope('GetMetricStatistics',element('Label',metric),element('Datapoints',
    element('member',element('Unit','Seconds'),element('Timestamp',start),element('Average','0')))));
  await assert.rejects(wrong.api.measurement(a,signal));
});

test('duplicate/foreign selection, wrong XML namespace and a repeated cursor are refused',async()=>{
  const signal=new AbortController().signal;
  const many=setup(envelope('DescribeAlarms',element('MetricAlarms',alarm,alarm)));
  await assert.rejects(many.api.list('',signal));
  const item=setup(envelope('DescribeAlarms',element('MetricAlarms',alarm)));
  await assert.rejects(item.api.named('other',signal));
  const bad=envelope('DescribeAlarms',element('MetricAlarms',alarm));
  bad.documentElement.children[0].namespaceURI='';
  await assert.rejects(setup(bad).api.named(name,signal));
  const page=setup(envelope('DescribeAlarms',element('MetricAlarms'),element('NextToken','opaque')));
  await assert.rejects(page.api.list('opaque',signal));
  assert.equal(page.sent[0].body.get('MaxRecords'),String(alarmPageSize));
});

test('capability and aborted read never issue an unadvertised or retired request',async()=>{
  const cap = {services:{cloudwatch:{alarms:true,console:true,operations:['DescribeAlarms','GetMetricStatistics']}}};
  assert.ok(serviceAvailable('cloudwatch',cap));
  assert.equal(serviceAvailable('cloudwatch',{services:{cloudwatch:{...cap.services.cloudwatch,console:false}}}),false);
  assert.equal(serviceAvailable('cloudwatch',{services:{cloudwatch:{...cap.services.cloudwatch,operations:['PutMetricData','GetMetricStatistics']}}}),false);
  const {api,sent}=setup(null);
  await assert.rejects(api.list('',AbortSignal.abort()));
  assert.equal(sent.length,0);
});
