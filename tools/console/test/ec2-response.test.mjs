import assert from 'node:assert/strict';
import test from 'node:test';
import {parseEC2Document, parseCreateVpc, parseDeleteVpc, parseEC2Error, createVpcParameters} from '../src/ec2-response.mjs';

const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
// Minimal DOM-shaped fixtures: the reader must not need browser side effects.
function element(localName, value = [], namespaceURI = ns) {
  const children = Array.isArray(value) ? value : [];
  return {localName, namespaceURI, children,
    textContent: Array.isArray(value) ? children.map(child => child.textContent).join('') : String(value)};
}
const document = root => ({documentElement: root});
const vpc = (id = 'vpc-one', extra = []) => element('item', [element('vpcId', id), element('cidrBlock', '10.0.0.0/16'), ...extra]);
const instance = (id = 'i-one', state = 'unknown', extra = []) => element('item', [
  element('instanceId', id), element('instanceType', 't3.micro'), element('imageId', 'ami-local'),
  element('vpcId', 'vpc-one'), element('subnetId', 'subnet-one'),
  element('simulatorRuntimeState', state), ...extra]);
const vpcDocument = (rows = [], extra = []) => document(element('DescribeVpcsResponse', [element('vpcSet', rows), ...extra]));
const instanceDocument = (rows = [], extra = []) => document(element('DescribeInstancesResponse', [
  element('reservationSet', rows.length ? [element('item', [element('instancesSet', rows)])] : []), ...extra]));
const tag = (key, value) => element('item', [element('key', key), element('value', value)]);

test('valid empty collections are empty, rather than failure', () => {
  assert.deepEqual(parseEC2Document(vpcDocument(), 'vpc'), []);
  assert.deepEqual(parseEC2Document(instanceDocument(), 'ec2'), []);
});

test('VPC reader returns only documented columns and the Name tag', () => {
  const input = vpcDocument([vpc('vpc-one', [element('tagSet', [tag('Other', 'not the name'), tag('Name', '学習 VPC')]),
    element('UserData', 'SECRET'), element('plan', 'PRIVATE')])]);
  assert.deepEqual(parseEC2Document(input, 'vpc'), [{id: 'vpc-one', name: '学習 VPC', cidr: '10.0.0.0/16', managedBy: ''}]);
  const owned = vpcDocument([vpc('vpc-two', [element('simulatorManagedBy', 'ec2')]), vpc('vpc-three', [element('simulatorManagedBy', 'cloudformation')])]);
  assert.deepEqual(parseEC2Document(owned, 'vpc').map(row => row.managedBy), ['ec2', 'cloudformation']);
});

test('EC2 reader preserves recorded state, never invents running, and ignores private fields', () => {
  for (const state of ['unknown', 'ready', 'launch_pending', 'seal_pending', '']) {
    const input = instanceDocument([instance('i-one', state, [element('UserData', 'SECRET'), element('plan', 'PRIVATE'),
      element('tagSet', [tag('Name', 'Sample')])])]);
    assert.deepEqual(parseEC2Document(input, 'ec2'), [{id: 'i-one', name: 'Sample', state,
      type: 't3.micro', image: 'ami-local', vpc: 'vpc-one', subnet: 'subnet-one'}]);
  }
});

test('missing and duplicate mandatory collections are rejected, never interpreted as empty', () => {
  for (const [kind, root, collection] of [['vpc', 'DescribeVpcsResponse', 'vpcSet'], ['ec2', 'DescribeInstancesResponse', 'reservationSet']]) {
    assert.throws(() => parseEC2Document(document(element(root)), kind));
    assert.throws(() => parseEC2Document(document(element(root, [element(collection), element(collection)])), kind));
  }
  for (const contents of [[], [element('instancesSet'), element('instancesSet')]]) {
    const input = document(element('DescribeInstancesResponse', [element('reservationSet', [element('item', contents)])]));
    assert.throws(() => parseEC2Document(input, 'ec2'));
  }
});

test('wrong root or namespace is rejected, including nested namespace substitution', () => {
  assert.throws(() => parseEC2Document(document(element('DescribeVpcsResponse', [element('vpcSet')], 'urn:wrong')), 'vpc'));
  assert.throws(() => parseEC2Document(document(element('DescribeInstancesResponse', [element('vpcSet')])), 'vpc'));
  assert.throws(() => parseEC2Document(document(element('DescribeVpcsResponse', [element('vpcSet', [], 'urn:wrong')])), 'vpc'));
  assert.throws(() => parseEC2Document(vpcDocument([element('item', [element('vpcId', 'vpc-one'), element('cidrBlock', '10.0.0.0/16')], 'urn:wrong')]), 'vpc'));
  assert.throws(() => parseEC2Document(vpcDocument([element('item', [element('vpcId', 'vpc-one', 'urn:wrong')])]), 'vpc'));
});

test('pagination tokens, empty IDs and duplicate IDs fail closed', () => {
  for (const [kind, make, row] of [['vpc', vpcDocument, vpc], ['ec2', instanceDocument, instance]]) {
    assert.throws(() => parseEC2Document(make([], [element('nextToken', 'more')]), kind));
    assert.throws(() => parseEC2Document(make([row('')]), kind));
    assert.throws(() => parseEC2Document(make([row('same'), row('same')]), kind));
  }
});

test('1000 resource rows are accepted and 1001 are rejected without truncation', () => {
  for (const [kind, make, row] of [['vpc', vpcDocument, vpc], ['ec2', instanceDocument, instance]]) {
    const rows = Array.from({length: 1000}, (_, i) => row('resource-' + i));
    assert.equal(parseEC2Document(make(rows), kind).length, 1000);
    assert.throws(() => parseEC2Document(make([...rows, row('overflow')]), kind));
  }
});

test('CreateVpc parameters send one optional Name tag and the trimmed CIDR', () => {
  assert.deepEqual([...createVpcParameters({cidr: ' 10.0.0.0/16 '})],
    [['Action', 'CreateVpc'], ['Version', '2016-11-15'], ['CidrBlock', '10.0.0.0/16']]);
  assert.deepEqual(Object.fromEntries(createVpcParameters({name: '学習 VPC', cidr: '10.1.0.0/16'})), {
    Action: 'CreateVpc', Version: '2016-11-15', CidrBlock: '10.1.0.0/16',
    'TagSpecification.1.ResourceType': 'vpc', 'TagSpecification.1.Tag.1.Key': 'Name', 'TagSpecification.1.Tag.1.Value': '学習 VPC'});
  assert.equal(createVpcParameters({name: 'x'.repeat(256), cidr: '10.0.0.0/16'}).get('TagSpecification.1.Tag.1.Value').length, 256);
  assert.throws(() => createVpcParameters({name: 'x'.repeat(257), cidr: '10.0.0.0/16'}));
});

test('CreateVpc and DeleteVpc responses must be complete EC2 documents', () => {
  const created = document(element('CreateVpcResponse', [element('requestId', 'r'), element('vpc', [element('vpcId', 'vpc-new'),
    element('cidrBlock', '10.0.0.0/16'), element('tagSet', [tag('Name', 'web')]), element('simulatorManagedBy', 'ec2')])]));
  assert.deepEqual(parseCreateVpc(created), {id: 'vpc-new', name: 'web', cidr: '10.0.0.0/16', managedBy: 'ec2'});
  for (const bad of [document(element('CreateVpcResponse')), document(element('CreateVpcResponse', [element('vpc', [element('cidrBlock', '10.0.0.0/16')])])),
    document(element('CreateVpcResponse', [element('vpc', [element('vpcId', 'vpc-a')]), element('vpc', [element('vpcId', 'vpc-b')])])),
    document(element('DeleteVpcResponse', [element('return', 'true')])), document(element('CreateVpcResponse', [element('vpc', [element('vpcId', 'vpc-a')])], 'urn:wrong'))])
    assert.throws(() => parseCreateVpc(bad));
  assert.equal(parseDeleteVpc(document(element('DeleteVpcResponse', [element('return', 'true')]))), true);
  for (const bad of [document(element('DeleteVpcResponse')), document(element('DeleteVpcResponse', [element('return', 'false')])),
    document(element('CreateVpcResponse', [element('return', 'true')]))])
    assert.throws(() => parseDeleteVpc(bad));
});

test('EC2 error documents expose code and message; anything else is not an error claim', () => {
  const error = (code, message) => document(element('Response', [element('Errors', [element('Error', [element('Code', code, null), element('Message', message, null)], null)], null)], null));
  assert.deepEqual(parseEC2Error(error('InvalidVpc.Range', 'Use /16 to /28.')), {code: 'InvalidVpc.Range', message: 'Use /16 to /28.'});
  assert.equal(parseEC2Error(error('', 'no code')), null);
  assert.equal(parseEC2Error(document(element('html', []))), null);
  assert.equal(parseEC2Error(null), null);
});
