import assert from 'node:assert/strict';
import test from 'node:test';
import {parseEC2Document} from '../src/ec2-response.mjs';

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
  assert.deepEqual(parseEC2Document(input, 'vpc'), [{id: 'vpc-one', name: '学習 VPC', cidr: '10.0.0.0/16'}]);
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
