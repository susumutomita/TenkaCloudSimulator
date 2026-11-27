import assert from 'node:assert/strict';
import test from 'node:test';
import {confirmInstanceStack, instanceStackLaunchAvailable, instanceStackLaunchVisible,
  instanceStackParameters, submitInstanceStack} from '../src/ec2-instance-stack-launch.mjs';

const settings = {stackName: 'local-instance', imageId: 'ami-0123456789abcdef0', acknowledge: true};
const region = 'us-east-1';
const id = 'arn:aws:cloudformation:us-east-1:000000000000:stack/local-instance/' + '0'.repeat(32);
const i = 'i-0123456789abcdef0', v = 'vpc-0123456789abcdef0', s = 'subnet-0123456789abcdef0', g = 'sg-0123456789abcdef0';
const cfn = 'http://cloudformation.amazonaws.com/doc/2010-05-15/';
const ec2 = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const cap = {identitySelection: true, services: {ec2: {instanceStackLaunch: 'CloudFormation/CreateStack'}}};
const capResponse = (capability = cap) => new Response(JSON.stringify(capability), {headers: {'X-Simulator-Authentication': 'required'}});
const response = (action, data) => new Response(`<${action}Response xmlns="${cfn}"><${action}Result>${data}</${action}Result></${action}Response>`);
const receipt = {stackId: id, stackName: settings.stackName, imageId: settings.imageId};
const outputs = {InstanceId: i, VpcId: v, SubnetId: s, GroupId: g};
const stack = (state = 'CREATE_COMPLETE', entries = Object.entries(outputs)) => response('DescribeStacks',
  '<Stacks><member><StackId>'+id+'</StackId><StackName>local-instance</StackName><StackStatus>'+state+
  '</StackStatus><Outputs>'+entries.map(([key, value]) => '<member><OutputKey>'+key+'</OutputKey><OutputValue>'+value+'</OutputValue></member>').join('')+
  '</Outputs></member></Stacks>');
const instance = ({image = settings.imageId, vpc = v} = {}) => new Response(
  `<DescribeInstancesResponse xmlns="${ec2}"><reservationSet><item><instancesSet><item>
  <instanceId>${i}</instanceId><imageId>${image}</imageId><instanceType>t3.micro</instanceType><vpcId>${vpc}</vpcId><subnetId>${s}</subnetId>
  <groupSet><item><groupId>${g}</groupId></item></groupSet><tagSet><item><key>Name</key><value>local-instance</value></item></tagSet>
  <simulatorRuntimeState>ready</simulatorRuntimeState></item></instancesSet></item></reservationSet></DescribeInstancesResponse>`);

function parse(raw) {
  // Use the same browser DOMParser shape as the served UI without starting a
  // process. The repository's lightweight exact DOM test helper is sufficient.
  const source = raw.replace(/<\?xml[^>]*\?>/g, '');
  const doc = {children: []}, open = [doc];
  for (const token of source.match(/<[^>]+>|[^<]+/g) ?? []) {
    const node = open.at(-1);
    if (token.startsWith('</')) open.pop();
    else if (token.startsWith('<')) {
      const [, name, namespace] = /^<([^ >]+)(?: xmlns="([^"]+)")?>$/.exec(token) ?? [];
      assert.ok(name, token);
      const element = {localName: name, namespaceURI: namespace || node.namespaceURI, children: [], textContent: ''};
      node.children.push(element);
      open.push(element);
    } else node.textContent += token;
  }
  assert.equal(open.length, 1);
  return {documentElement: doc.children[0]};
}

test('explicit local operator capability and bounded generated declaration', () => {
  assert.equal(instanceStackLaunchVisible({}, true, 'simulator-admin'), false);
  assert.equal(instanceStackLaunchAvailable({identitySelection: true}, true, 'simulator-admin'), false);
  assert.equal(instanceStackLaunchAvailable(cap, true, 'simulator-admin'), true);
  assert.equal(instanceStackLaunchAvailable(cap, false, 'simulator-admin'), false);
  assert.equal(instanceStackLaunchAvailable(cap, true, 'reader'), false);
  assert.equal(instanceStackLaunchAvailable({...cap, consoleScope: {instanceId: i}}, true, 'simulator-admin'), false);
  const p = instanceStackParameters(settings);
  assert.deepEqual([...p.keys()], ['Action', 'Version', 'StackName', 'Capabilities.member.1', 'TemplateBody']);
  assert.equal(p.get('Action'), 'CreateStack');
  assert.equal(p.get('Capabilities.member.1'), 'CAPABILITY_IAM');
  const {Resources: r, Outputs: out} = JSON.parse(p.get('TemplateBody'));
  assert.equal(Object.keys(r).length, 6);
  assert.deepEqual(r.Instance.Properties.SecurityGroupIds, [{Ref: 'Group'}]);
  assert.deepEqual(r.Instance.Properties.SubnetId, {Ref: 'Subnet'});
  assert.deepEqual(r.Group.Properties.VpcId, r.Subnet.Properties.VpcId);
  assert.equal(r.Group.Properties.SecurityGroupIngress, undefined);
  assert.equal(r.Role.Properties.Policies, undefined);
  assert.equal(r.Role.Properties.RoleName, undefined);
  assert.deepEqual(out.InstanceId.Value, {Ref: 'Instance'});
  for (const invalid of [{stackName: 'not valid'}, {imageId: 'https://unapproved.invalid'}, {acknowledge: false}]) {
    assert.throws(() => instanceStackParameters({...settings, ...invalid}), /Invalid/);
  }
});

function transport(...responses) {
  const actions = [];
  const request = async (path, options) => {
    if (path.endsWith('/capabilities')) actions.push('capabilities');
    else {
      assert.equal(path, '/');
      assert.equal(options.method, 'POST');
      assert.equal(options.redirect, 'error');
      assert.equal(options.headers['X-Simulator-Region'], region);
      const action = options.body.get('Action');
      actions.push(action);
      assert.equal(options.headers['X-Simulator-Service'], action === 'DescribeInstances' ? 'ec2' : 'cloudformation');
      if (action === 'DescribeStacks') assert.equal(options.body.get('StackName'), id);
    }
    assert.ok(responses.length, 'unexpected retry or fallback');
    return responses.shift();
  };
  return {request, actions};
}

test('absent or revoked capability cannot send any create', async () => {
  const blocked = transport();
  await assert.rejects(submitInstanceStack(blocked.request, region, settings, {allowed: false}), /cannot create/);
  assert.deepEqual(blocked.actions, []);
  const revoked = transport(capResponse({identitySelection: true}));
  await assert.rejects(submitInstanceStack(revoked.request, region, settings, {allowed: true}), /unavailable/);
  assert.deepEqual(revoked.actions, ['capabilities']);
});

test('separate acknowledgement and complete exact readback; retry is read-only', async () => {
  const fixture = transport(capResponse(), response('CreateStack', '<StackId>'+id+'</StackId>'),
    stack('CREATE_IN_PROGRESS'), stack(), instance({vpc: 'vpc-fffffffffffffffff'}), stack(), instance());
  const options = {allowed: true, parse};
  const ack = await submitInstanceStack(fixture.request, region, settings, options);
  assert.deepEqual(ack, receipt);
  assert.deepEqual(fixture.actions, ['capabilities', 'CreateStack']);
  await assert.rejects(confirmInstanceStack(fixture.request, region, receipt, options), /did not report CREATE_COMPLETE/);
  assert.deepEqual(fixture.actions, ['capabilities', 'CreateStack', 'DescribeStacks']);
  await assert.rejects(confirmInstanceStack(fixture.request, region, receipt, options), /Unconfirmed/);
  const observed = await confirmInstanceStack(fixture.request, region, receipt, options);
  assert.equal(observed.stackId, id);
  assert.equal(observed.instance.id, i);
  assert.equal(observed.guestAlive, undefined, 'saved ready is not liveness');
  assert.deepEqual(fixture.actions, ['capabilities', 'CreateStack', 'DescribeStacks', 'DescribeStacks',
    'DescribeInstances', 'DescribeStacks', 'DescribeInstances']);
});

test('transport failure or cancelled scope never manufactures or replays a receipt', async () => {
  const bad = transport(capResponse(), response('CreateStack', '<StackId>'+id.replace('us-east-1','us-west-2')+'</StackId>'));
  await assert.rejects(submitInstanceStack(bad.request, region, settings, {allowed: true, parse}), /Unconfirmed/);
  assert.deepEqual(bad.actions, ['capabilities', 'CreateStack']);
  const controller = new AbortController();
  const base = transport(capResponse());
  await assert.rejects(submitInstanceStack(async (...args) => {
    const res = await base.request(...args);
    controller.abort();
    return res;
  }, region, settings, {allowed: true, parse, signal: controller.signal}), {name:'AbortError'});
  assert.deepEqual(base.actions, ['capabilities']);
});

test('receipt body is bounded before interpreting XML, and declarations are not readback', async () => {
  let cancelled = false, reads = 0;
  const oversize = new Response(new ReadableStream({
    pull(stream) { reads++; stream.enqueue(new Uint8Array(4 * 1024 * 1024 + 1)); },
    cancel() { cancelled = true; },
  }));
  const large = transport(capResponse(), oversize);
  const unexpectedParse = () => assert.fail('an incomplete or untrusted body reached XML parsing');
  await assert.rejects(submitInstanceStack(large.request, region, settings, {allowed: true, parse: unexpectedParse}), /Unconfirmed/);
  assert.equal(cancelled, true);
  assert.ok(reads >= 2 && reads <= 3);
  const projected = transport(capResponse(), new Response('<!DOCTYPE CreateStackResponse><other/>'));
  await assert.rejects(submitInstanceStack(projected.request, region, settings, {allowed: true, parse: unexpectedParse}), /Unconfirmed/);
  assert.deepEqual(projected.actions, ['capabilities', 'CreateStack']);
});
