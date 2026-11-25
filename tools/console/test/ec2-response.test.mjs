import assert from 'node:assert/strict';
import test from 'node:test';
import {parseEC2Document, describeAction, parseCreateVpc, parseDeleteVpc, parseCreateSubnet, parseDeleteSubnet, parseRunInstances, parseTerminateInstances, parseEC2Error, createVpcParameters, createSubnetParameters, runInstancesParameters, INSTANCE_TYPES} from '../src/ec2-response.mjs';

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
const subnet = (id = 'subnet-one', extra = []) => element('item', [element('subnetId', id), element('vpcId', 'vpc-one'),
  element('cidrBlock', '10.0.1.0/24'), element('state', 'available'), ...extra]);
const subnetDocument = (rows = [], extra = []) => document(element('DescribeSubnetsResponse', [element('subnetSet', rows), ...extra]));

test('valid empty collections are empty, rather than failure', () => {
  assert.deepEqual(parseEC2Document(vpcDocument(), 'vpc'), []);
  assert.deepEqual(parseEC2Document(instanceDocument(), 'ec2'), []);
  assert.deepEqual(parseEC2Document(subnetDocument(), 'subnets'), []);
});

test('each view reads its own Describe action and unknown views are refused', () => {
  assert.deepEqual(['ec2', 'vpc', 'subnets'].map(describeAction), ['DescribeInstances', 'DescribeVpcs', 'DescribeSubnets']);
  for (const kind of ['subnet', 'constructor', '', undefined]) {
    assert.throws(() => describeAction(kind));
    assert.throws(() => parseEC2Document(vpcDocument(), kind));
  }
});

test('subnet reader returns documented columns, the Name tag, the zone and the owner', () => {
  const input = subnetDocument([subnet('subnet-one', [element('availabilityZone', 'us-east-1b'),
    element('tagSet', [tag('Name', '公開サブネット')]), element('simulatorManagedBy', 'ec2'), element('plan', 'PRIVATE')]), subnet('subnet-two')]);
  assert.deepEqual(parseEC2Document(input, 'subnets'), [
    {id: 'subnet-one', name: '公開サブネット', vpc: 'vpc-one', cidr: '10.0.1.0/24', zone: 'us-east-1b', managedBy: 'ec2'},
    {id: 'subnet-two', name: '', vpc: 'vpc-one', cidr: '10.0.1.0/24', zone: '', managedBy: ''}]);
});

test('VPC reader returns only documented columns and the Name tag', () => {
  const input = vpcDocument([vpc('vpc-one', [element('tagSet', [tag('Other', 'not the name'), tag('Name', '学習 VPC')]),
    element('UserData', 'SECRET'), element('plan', 'PRIVATE')])]);
  assert.deepEqual(parseEC2Document(input, 'vpc'), [{id: 'vpc-one', name: '学習 VPC', cidr: '10.0.0.0/16', managedBy: ''}]);
  const owned = vpcDocument([vpc('vpc-two', [element('simulatorManagedBy', 'ec2')]), vpc('vpc-three', [element('simulatorManagedBy', 'cloudformation')])]);
  assert.deepEqual(parseEC2Document(owned, 'vpc').map(row => row.managedBy), ['ec2', 'cloudformation']);
});

test('EC2 reader preserves recorded state, never invents running, and ignores private fields', () => {
  for (const state of ['unknown', 'ready', 'launch_pending', 'seal_pending', 'not_launched', '']) {
    const input = instanceDocument([instance('i-one', state, [element('UserData', 'SECRET'), element('plan', 'PRIVATE'),
      element('instanceState', [element('code', '16'), element('name', 'running')]),
      element('tagSet', [tag('Name', 'Sample')])])]);
    assert.deepEqual(parseEC2Document(input, 'ec2'), [{id: 'i-one', name: 'Sample', state,
      type: 't3.micro', image: 'ami-local', vpc: 'vpc-one', subnet: 'subnet-one', managedBy: ''}]);
  }
  const owned = instanceDocument([instance('i-two', 'not_launched', [element('simulatorManagedBy', 'ec2')]), instance('i-three', 'ready', [element('simulatorManagedBy', 'cloudformation')])]);
  assert.deepEqual(parseEC2Document(owned, 'ec2').map(row => row.managedBy), ['ec2', 'cloudformation']);
});

test('missing and duplicate mandatory collections are rejected, never interpreted as empty', () => {
  for (const [kind, root, collection] of [['vpc', 'DescribeVpcsResponse', 'vpcSet'], ['ec2', 'DescribeInstancesResponse', 'reservationSet'], ['subnets', 'DescribeSubnetsResponse', 'subnetSet']]) {
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
  for (const [kind, make, row] of [['vpc', vpcDocument, vpc], ['ec2', instanceDocument, instance], ['subnets', subnetDocument, subnet]]) {
    assert.throws(() => parseEC2Document(make([], [element('nextToken', 'more')]), kind));
    assert.throws(() => parseEC2Document(make([row('')]), kind));
    assert.throws(() => parseEC2Document(make([row('same'), row('same')]), kind));
  }
});

test('1000 resource rows are accepted and 1001 are rejected without truncation', () => {
  for (const [kind, make, row] of [['vpc', vpcDocument, vpc], ['ec2', instanceDocument, instance], ['subnets', subnetDocument, subnet]]) {
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

test('CreateSubnet parameters send the VPC, the trimmed CIDR and only a chosen zone', () => {
  assert.deepEqual([...createSubnetParameters({vpc: 'vpc-one', cidr: ' 10.0.1.0/24 '})],
    [['Action', 'CreateSubnet'], ['Version', '2016-11-15'], ['VpcId', 'vpc-one'], ['CidrBlock', '10.0.1.0/24']]);
  assert.deepEqual(Object.fromEntries(createSubnetParameters({vpc: 'vpc-one', name: '公開', cidr: '10.0.1.0/24', zone: 'us-east-1b'})), {
    Action: 'CreateSubnet', Version: '2016-11-15', VpcId: 'vpc-one', CidrBlock: '10.0.1.0/24', AvailabilityZone: 'us-east-1b',
    'TagSpecification.1.ResourceType': 'subnet', 'TagSpecification.1.Tag.1.Key': 'Name', 'TagSpecification.1.Tag.1.Value': '公開'});
  assert.throws(() => createSubnetParameters({vpc: 'vpc-one', name: 'x'.repeat(257), cidr: '10.0.1.0/24'}));
});

test('RunInstances parameters request exactly one instance and never send unsupported members', () => {
  assert.deepEqual(INSTANCE_TYPES, ['t3.micro', 't3.small']);
  assert.deepEqual([...runInstancesParameters({image: ' ami-0123456789abcdef0 ', type: 't3.micro', subnet: 'subnet-one'})],
    [['Action', 'RunInstances'], ['Version', '2016-11-15'], ['ImageId', 'ami-0123456789abcdef0'], ['InstanceType', 't3.micro'],
      ['MinCount', '1'], ['MaxCount', '1'], ['SubnetId', 'subnet-one']]);
  assert.deepEqual(Object.fromEntries(runInstancesParameters({name: '練習用', image: 'ami-12345678', type: 't3.small', subnet: 'subnet-one', token: 'token-1'})), {
    Action: 'RunInstances', Version: '2016-11-15', ImageId: 'ami-12345678', InstanceType: 't3.small', MinCount: '1', MaxCount: '1',
    SubnetId: 'subnet-one', ClientToken: 'token-1',
    'TagSpecification.1.ResourceType': 'instance', 'TagSpecification.1.Tag.1.Key': 'Name', 'TagSpecification.1.Tag.1.Value': '練習用'});
  assert.throws(() => runInstancesParameters({name: 'x'.repeat(257), image: 'ami-12345678', type: 't3.micro', subnet: 'subnet-one'}));
});

test('CreateSubnet and DeleteSubnet responses must be complete EC2 documents', () => {
  const created = document(element('CreateSubnetResponse', [element('requestId', 'r'), element('subnet', [element('subnetId', 'subnet-new'),
    element('vpcId', 'vpc-one'), element('cidrBlock', '10.0.1.0/24'), element('state', 'available'), element('availabilityZone', 'us-east-1a'),
    element('tagSet', [tag('Name', 'web')]), element('simulatorManagedBy', 'ec2')])]));
  assert.deepEqual(parseCreateSubnet(created), {id: 'subnet-new', name: 'web', vpc: 'vpc-one', cidr: '10.0.1.0/24', zone: 'us-east-1a', managedBy: 'ec2'});
  for (const bad of [document(element('CreateSubnetResponse')), document(element('CreateSubnetResponse', [element('subnet', [element('vpcId', 'vpc-one')])])),
    document(element('CreateVpcResponse', [element('subnet', [element('subnetId', 'subnet-a')])])),
    document(element('CreateSubnetResponse', [element('subnet', [element('subnetId', 'subnet-a')])], 'urn:wrong'))])
    assert.throws(() => parseCreateSubnet(bad));
  assert.equal(parseDeleteSubnet(document(element('DeleteSubnetResponse', [element('return', 'true')]))), true);
  for (const bad of [document(element('DeleteSubnetResponse')), document(element('DeleteSubnetResponse', [element('return', 'false')])),
    document(element('DeleteVpcResponse', [element('return', 'true')]))])
    assert.throws(() => parseDeleteSubnet(bad));
});

test('RunInstances must return exactly one launch record and never a claimed AWS state', () => {
  const launched = (rows, extra = []) => document(element('RunInstancesResponse', [element('requestId', 'r'), element('ownerId', '000000000000'), element('instancesSet', rows), ...extra]));
  assert.deepEqual(parseRunInstances(launched([instance('i-new', 'not_launched', [element('simulatorManagedBy', 'ec2'), element('tagSet', [tag('Name', 'web')])])])),
    {id: 'i-new', name: 'web', state: 'not_launched', type: 't3.micro', image: 'ami-local', vpc: 'vpc-one', subnet: 'subnet-one', managedBy: 'ec2'});
  for (const bad of [document(element('RunInstancesResponse')), launched([]), launched([instance('i-a'), instance('i-b')]), launched([instance('')]),
    document(element('DescribeInstancesResponse', [element('instancesSet', [instance('i-a')])])),
    document(element('RunInstancesResponse', [element('instancesSet', [instance('i-a')])], 'urn:wrong'))])
    assert.throws(() => parseRunInstances(bad));
});

test('TerminateInstances succeeds only for the requested instance reported as terminated', () => {
  const change = (id, name) => element('item', [element('instanceId', id), element('currentState', [element('code', '48'), element('name', name)])]);
  const terminated = rows => document(element('TerminateInstancesResponse', [element('requestId', 'r'), element('instancesSet', rows)]));
  assert.equal(parseTerminateInstances(terminated([change('i-one', 'terminated')]), 'i-one'), true);
  for (const [bad, id] of [[terminated([change('i-one', 'terminated')]), 'i-two'], [terminated([change('i-one', 'terminated')]), ''],
    [terminated([change('i-one', 'shutting-down')]), 'i-one'], [terminated([]), 'i-one'],
    [terminated([change('i-one', 'terminated'), change('i-two', 'terminated')]), 'i-one'],
    [terminated([element('item', [element('instanceId', 'i-one')])]), 'i-one'],
    [document(element('TerminateInstancesResponse')), 'i-one'], [document(element('DeleteVpcResponse', [element('return', 'true')])), 'i-one']])
    assert.throws(() => parseTerminateInstances(bad, id));
});

test('EC2 error documents expose code and message; anything else is not an error claim', () => {
  const error = (code, message) => document(element('Response', [element('Errors', [element('Error', [element('Code', code, null), element('Message', message, null)], null)], null)], null));
  assert.deepEqual(parseEC2Error(error('InvalidVpc.Range', 'Use /16 to /28.')), {code: 'InvalidVpc.Range', message: 'Use /16 to /28.'});
  assert.equal(parseEC2Error(error('', 'no code')), null);
  assert.equal(parseEC2Error(document(element('html', []))), null);
  assert.equal(parseEC2Error(null), null);
});
