import test from 'node:test';
import assert from 'node:assert/strict';
import {queryStack, stackEvents, stackOutputs} from '../../tools/console/src/cloudformation-events.mjs';
const element = (name, children = [], content = '') => ({localName:name, children, textContent:content});
const field = (name, value) => element(name, [], value);
const response = (action, children) => ({
  getElementsByTagName: () => [],
  documentElement: element(action+'Response', [element(action+'Result', children)]),
});

test('event projection includes only recorded fields and preserves newest-first order', () => {
  const event = id => element('member', [field('EventId', id), field('StackId','stack-id'),
    field('Timestamp','2026-11-08T00:00:00Z'), field('ResourceStatus','CREATE_COMPLETE'),
    field('LogicalResourceId','Resource'), field('PhysicalResourceId','physical'),
    field('ResourceType','AWS::SSM::Parameter'), field('ResourceProperties','private'),
    field('ResourceStatusReason','private'), field('ClientRequestToken','private')]);
  const projected = stackEvents(response('DescribeStackEvents', [element('StackEvents', [event('new'), event('old')]),field('NextToken','cursor')]));
  assert.deepEqual(projected.events.map(event => event.id), ['new','old']);
  assert.deepEqual(projected.events[0], {id:'new',stackId:'stack-id',timestamp:'2026-11-08T00:00:00Z',status:'CREATE_COMPLETE',logicalId:'Resource',physicalId:'physical',resourceType:'AWS::SSM::Parameter'});
  assert.equal(projected.nextToken,'cursor');
  assert.equal(JSON.stringify(projected).includes('private'),false);
});

test('empty recorded history is distinct from malformed or wrong operation responses', () => {
  assert.deepEqual(stackEvents(response('DescribeStackEvents',[element('StackEvents')])),{events:[],nextToken:''});
  for(const xml of [response('DescribeStacks',[]),response('DescribeStackEvents',[]),{getElementsByTagName:()=>[{}]},
    {getElementsByTagName:()=>[],documentElement:element('DescribeStackEventsResponse')}]) {
    assert.throws(()=>stackEvents(xml),/Invalid CloudFormation/);
  }
});

test('outputs are separate and only output key/value fields are projected', () => {
  const xml=response('DescribeStacks',[element('Stacks',[element('member',[
    element('Outputs',[element('member',[field('OutputKey','Result'),field('OutputValue','test-value'),field('Description','not-projected')])]),
    element('Parameters',[element('member',[field('ParameterValue','private')])]),
  ])])]);
  assert.deepEqual(stackOutputs(xml),[{label:'Result',value:'test-value'}]);
  assert.deepEqual(stackOutputs(response('DescribeStacks',[element('Stacks',[element('member')])])),[]);
  assert.throws(()=>stackOutputs(response('DescribeStacks',[element('Stacks')])),/Deleted stacks/);
  assert.throws(()=>stackOutputs(response('DescribeStackEvents',[])),/Invalid CloudFormation/);
});

test('query uses existing authority, exact region/name/cursor and cancellable native root POST', async () => {
  const previous=globalThis.DOMParser;
  globalThis.DOMParser=class {parseFromString(text,type){assert.equal(type,'application/xml');return {text};}};
  try {
    const controller=new AbortController();let seen;
    const request=async (path,options)=>{seen={path,options};return {text:async()=>'<native/>'};};
    assert.deepEqual(await queryStack(request,'eu-west-1','DescribeStackEvents',' stack&?雪 ',{nextToken:'cursor',signal:controller.signal}),{text:'<native/>'});
    assert.equal(seen.path,'/');assert.equal(seen.options.method,'POST');assert.equal(seen.options.signal,controller.signal);
    assert.deepEqual(seen.options.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Region':'eu-west-1'});
    assert.deepEqual(Object.fromEntries(seen.options.body),{Action:'DescribeStackEvents',Version:'2010-05-15',StackName:'stack&?雪',NextToken:'cursor'});
    await queryStack(request,'us-east-1','DescribeStacks','stack');
    assert.equal(seen.options.body.has('NextToken'),false);
    for(const [operation,name] of [['DeleteStack','stack'],['DescribeStackEvents','   ']])
      await assert.rejects(queryStack(()=>assert.fail('must not issue an invalid request'),'us-east-1',operation,name));
    await assert.rejects(queryStack(async()=>{throw Error('AccessDenied');},'us-east-1','DescribeStackEvents','stack'),/AccessDenied/);
  } finally {if(previous===undefined)delete globalThis.DOMParser;else globalThis.DOMParser=previous;}
});
