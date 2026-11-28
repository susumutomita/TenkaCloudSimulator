import assert from 'node:assert/strict';
import test from 'node:test';
import {elbAvailable, elbClient, validHealthPath} from '../src/elb.mjs';
import {serviceAvailable, serviceDescriptor} from '../src/console-navigation.mjs';

const operations = ['DescribeLoadBalancers','DescribeTargetGroups','DescribeListeners','DescribeTargetHealth','ModifyTargetGroup'];
const native = {console:true, protocol:'Query/XML 2015-12-01', operations};
const capabilities = service => ({services:{elasticloadbalancing:service}});

test('ELB navigation requires the complete regional Console capability', () => {
  assert.equal(serviceDescriptor('elasticloadbalancing').route,'/elasticloadbalancing');
  assert.ok(serviceAvailable('elasticloadbalancing',capabilities(native)));
  for (const missing of operations) {
    assert.equal(elbAvailable(capabilities({...native,operations:operations.filter(op=>op!==missing)})),false);
  }
  assert.equal(elbAvailable(capabilities({...native,console:false})),false);
  assert.equal(elbAvailable({...capabilities(native),consoleScope:{instanceId:'i-private'}}),false);
});

test('health check path follows native printable ASCII bounds', () => {
  assert.ok(validHealthPath('/'));
  assert.ok(validHealthPath('/'+'a'.repeat(1023)));
  for (const value of ['plain','/with space','/a\nb','/非ASCII','/'+'a'.repeat(1024)]) assert.equal(validHealthPath(value),false);
});

test('only exact missing-name filter maps to an empty page, and writes preserve native refusal', async () => {
  const calls = [];
  const api = elbClient(async (path,options) => {
    calls.push([path,options]);
    const action=new URLSearchParams(options.body).get('Action');
    throw Error(action === 'ModifyTargetGroup' ? 'AccessDenied: denied' : 'LoadBalancerNotFound: absent');
  },'us-west-2');
  const signal = new AbortController().signal;
  assert.deepEqual(await api.list('practice','',signal),{rows:[],next:''});
  await assert.rejects(api.list('practice','cursor',signal),/LoadBalancerNotFound/);
  await assert.rejects(api.modify('arn:example','arn:parent','/health',signal),/AccessDenied/);
  await assert.rejects(api.modify('arn:example','arn:parent','bad',signal),/Invalid health/);
  assert.equal(calls.length,3);
  const [path,options]=calls[2];
  assert.equal(path,'/');
  assert.equal(options.method,'POST');
  assert.equal(options.headers['X-Simulator-Service'],'elasticloadbalancing');
  assert.equal(options.headers['X-Simulator-Region'],'us-west-2');
  assert.deepEqual(Object.fromEntries(new URLSearchParams(options.body)),{
    Action:'ModifyTargetGroup',Version:'2015-12-01',TargetGroupArn:'arn:example',HealthCheckPath:'/health',
  });
});
