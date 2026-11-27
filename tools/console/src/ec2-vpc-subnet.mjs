// A fixed two-resource template submitted as the authenticated CloudFormation caller.
// Do not pass a role or adopt an existing EC2-owned VPC.
const ns = 'http://cloudformation.amazonaws.com/doc/2010-05-15/';
const unavailable = 'CloudFormation did not confirm the saved VPC and subnet. Check the stack before retrying.';
// The ordinary participant browser cannot read host-only stack outputs even
// when IAM permits creation. Offer the verified readback workflow only to the
// authenticated operator using the built-in administrator identity.
export function vpcSubnetAvailable(capabilities, authenticationRequired, principal) {
  const operations = capabilities?.services?.cloudformation?.operations;
  return authenticationRequired === true && capabilities?.identitySelection === true &&
    !capabilities.consoleScope && principal === 'simulator-admin' && Array.isArray(operations) &&
    operations.includes('CreateStack') && operations.includes('DescribeStacks');
}

const stackNamePattern = /^[A-Za-z][A-Za-z0-9-]{0,127}$/;
function ipv4(s) {
  const m = typeof s === 'string' && /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(1[6-9]|2[0-8])$/.exec(s.trim());
  if (!m || m.slice(1, 5).some(octet => Number(octet) > 255 || String(Number(octet)) !== octet)) return null;
  return {address: m.slice(1,5).reduce((n, octet) => n * 256 + Number(octet), 0), bits: Number(m[5])};
}
// Form feedback before any write. The native EC2 and CloudFormation APIs remain authoritative.
export function vpcCreateIssues({stackName, name = '', cidr, subnetCIDR}, paired) {
  const issues = {};
  const vpc = ipv4(cidr);
  if (!vpc) issues.cidr = 'cidr';
  if (typeof name !== 'string' || new TextEncoder().encode(name).length > 256) issues.name = 'name';
  if (paired) {
    if (typeof stackName !== 'string' || !stackNamePattern.test(stackName)) issues.stack = 'stack';
    const subnet = ipv4(subnetCIDR);
    if (!subnet) issues.subnet = 'subnet';
    else if (vpc && (subnet.bits < vpc.bits || subnet.address >>> (32 - vpc.bits) !== vpc.address >>> (32 - vpc.bits))) {
      issues.subnet = 'containment';
    }
  }
  return issues;
}
export function vpcSubnetInputError({stackName, name, cidr, subnetCIDR}) {
  const issues = vpcCreateIssues({stackName, name, cidr, subnetCIDR}, true);
  return issues.stack ?? issues.name ?? issues.cidr ?? (issues.subnet === 'subnet' ? 'cidr' : issues.subnet) ?? null;
}
export function vpcSubnetParameters(form) {
  const invalid = vpcSubnetInputError(form);
  if (invalid) throw Error('Invalid VPC and subnet settings: ' + invalid);
  const {stackName, name, cidr, subnetCIDR} = form;
  const vpc = {CidrBlock: cidr.trim()};
  if (name) vpc.Tags = [{Key: 'Name', Value: name}];
  return new URLSearchParams({Action: 'CreateStack', Version: '2010-05-15', StackName: stackName,
    TemplateBody: JSON.stringify({AWSTemplateFormatVersion: '2010-09-09', Resources: {
      Vpc: {Type: 'AWS::EC2::VPC', Properties: vpc},
      Subnet: {Type: 'AWS::EC2::Subnet', Properties: {VpcId: {Ref: 'Vpc'}, CidrBlock: subnetCIDR.trim()}},
    }, Outputs: {VpcId: {Value: {Ref: 'Vpc'}}, SubnetId: {Value: {Ref: 'Subnet'}}}})});
}

function children(node, name) {
  const nodes = [...(node?.children ?? [])].filter(child => child.localName === name);
  if (nodes.some(child => child.namespaceURI !== ns)) throw Error(unavailable);
  return nodes;
}
function one(node, name) {
  const nodes = children(node, name);
  if (nodes.length !== 1) throw Error(unavailable);
  return nodes[0];
}
function value(node, name) {
  const field = one(node, name);
  if (field.children.length) throw Error(unavailable);
  return field.textContent;
}
function result(document, action) {
  const root = document?.documentElement;
  if (root?.namespaceURI !== ns || root.localName !== action + 'Response') throw Error(unavailable);
  return one(root, action + 'Result');
}

export function parseVpcSubnetStack(document, stackId, name) {
  const member = one(one(result(document, 'DescribeStacks'), 'Stacks'), 'member');
  if (value(member, 'StackId') !== stackId || value(member, 'StackName') !== name ||
      value(member, 'StackStatus') !== 'CREATE_COMPLETE') throw Error(unavailable);
  const entries = children(one(member, 'Outputs'), 'member');
  if (entries.length !== 2) throw Error(unavailable);
  const outputs = new Map(entries.map(entry => [value(entry, 'OutputKey'), value(entry, 'OutputValue')]));
  const vpcId = outputs.get('VpcId'), subnetId = outputs.get('SubnetId');
  if (!/^vpc-[0-9a-f]{17}$/.test(vpcId ?? '') || !/^subnet-[0-9a-f]{17}$/.test(subnetId ?? '')) throw Error(unavailable);
  return {stackId, vpcId, subnetId};
}

export async function createVpcWithSubnet(request, region, form, {
  allowed, signal, parse = text => new DOMParser().parseFromString(text, 'application/xml'),
} = {}) {
  if (allowed !== true) throw Error('An authenticated operator is required for this console workflow.');
  const headers = {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'cloudformation',
    'X-Simulator-Region': region};
  const submit = async body => {
    signal?.throwIfAborted();
    const res = await request('/', {method: 'POST', headers, body, signal, redirect: 'error'});
    if (!res.ok) throw Error('CloudFormation rejected the request (' + res.status + ').');
    const document = parse(await res.text());
    signal?.throwIfAborted();
    return document;
  };
  const receipt = result(await submit(vpcSubnetParameters(form)), 'CreateStack');
  const stackId = value(receipt, 'StackId');
  const prefix = 'arn:aws:cloudformation:' + region + ':000000000000:stack/' + form.stackName + '/';
  if (!stackId.startsWith(prefix) || !/^[0-9a-f]{32}$/.test(stackId.slice(prefix.length))) throw Error(unavailable);
  // Creation is not presented as verified until the selected stack's saved outputs
  // agree with the exact receipt. Never repeat a potentially committed POST.
  return parseVpcSubnetStack(await submit(new URLSearchParams({
    Action: 'DescribeStacks', Version: '2010-05-15', StackName: stackId,
  })), stackId, form.stackName);
}
