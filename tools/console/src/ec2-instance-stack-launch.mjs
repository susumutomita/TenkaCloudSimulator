import {parseEC2Document} from './ec2-response.mjs';

const cfnNamespace = 'http://cloudformation.amazonaws.com/doc/2010-05-15/';
const capabilitiesPath = '/_simulator/v1/capabilities';
const regionNames = new Set(['us-east-1', 'us-west-2', 'eu-west-1', 'ap-northeast-1']);
const instanceType = 't3.micro';
const responseByteLimit = 8 * 1024 * 1024;
const outputID = {
  InstanceId: /^i-[0-9a-f]{17}$/, VpcId: /^vpc-[0-9a-f]{17}$/,
  SubnetId: /^subnet-[0-9a-f]{17}$/, GroupId: /^sg-[0-9a-f]{17}$/,
};
const invalid = () => { throw Error('Unconfirmed instance stack response.'); };

export function instanceStackLaunchVisible(capabilities, authenticated, principal) {
  return authenticated === true && principal === 'simulator-admin' &&
    capabilities?.identitySelection === true && !capabilities.consoleScope;
}
export function instanceStackLaunchAvailable(capabilities, authenticated, principal) {
  return instanceStackLaunchVisible(capabilities, authenticated, principal) &&
    capabilities?.services?.ec2?.instanceStackLaunch === 'CloudFormation/CreateStack';
}
export function instanceStackInputProblem({stackName, imageId, acknowledge}) {
  if (typeof stackName !== 'string' || !/^[A-Za-z][A-Za-z0-9-]{0,127}$/.test(stackName)) return 'name';
  if (typeof imageId !== 'string' || !/^ami-[0-9a-f]{8,32}$/.test(imageId)) return 'image';
  if (acknowledge !== true) return 'iam';
  return '';
}

export function instanceStackParameters(form) {
  if (instanceStackInputProblem(form)) throw Error('Invalid instance stack settings.');
  const ref = logical => ({Ref: logical});
  const resource = (Type, Properties) => ({Type, Properties});
  const resources = {
    Vpc: resource('AWS::EC2::VPC', {CidrBlock: '10.91.0.0/16'}),
    Subnet: resource('AWS::EC2::Subnet', {VpcId: ref('Vpc'), CidrBlock: '10.91.1.0/24'}),
    Group: resource('AWS::EC2::SecurityGroup', {VpcId: ref('Vpc'), GroupDescription: 'Private instance stack; no ingress'}),
    Role: resource('AWS::IAM::Role', {AssumeRolePolicyDocument: {Version: '2012-10-17',
      Statement: [{Effect: 'Allow', Principal: {Service: 'ec2.amazonaws.com'}, Action: 'sts:AssumeRole'}]}}),
    Profile: resource('AWS::IAM::InstanceProfile', {Roles: [ref('Role')]}),
    Instance: resource('AWS::EC2::Instance', {
      ImageId: form.imageId, InstanceType: instanceType, SubnetId: ref('Subnet'),
      SecurityGroupIds: [ref('Group')], IamInstanceProfile: ref('Profile'),
      UserData: 'I2Nsb3VkLWNvbmZpZwp7fQo=', // Empty cloud-config, not caller-provided code.
      Tags: [{Key: 'Name', Value: form.stackName}],
    }),
  };
  const outputs = Object.fromEntries(Object.keys(outputID).map(key => [key, {Value: ref(key.slice(0, -2))}]));
  return new URLSearchParams({Action: 'CreateStack', Version: '2010-05-15', StackName: form.stackName,
    'Capabilities.member.1': 'CAPABILITY_IAM',
    TemplateBody: JSON.stringify({AWSTemplateFormatVersion: '2010-09-09', Resources: resources, Outputs: outputs})});
}

const toDocument = text => new DOMParser().parseFromString(text, 'application/xml');
function fields(node, name) {
  const matches = [...(node?.children ?? [])].filter(child => child.localName === name);
  if (matches.some(child => child.namespaceURI !== cfnNamespace)) invalid();
  return matches;
}
function one(node, name) {
  const matched = fields(node, name);
  if (matched.length !== 1) invalid();
  return matched[0];
}
function value(node, name) {
  const field = one(node, name);
  if (field.children.length) invalid();
  return field.textContent;
}
function result(doc, action) {
  const root = doc?.documentElement;
  if (root?.namespaceURI !== cfnNamespace || root.localName !== action + 'Response') invalid();
  return one(root, action + 'Result');
}
const stackPrefix = (region, stackName) => 'arn:aws:cloudformation:' + region + ':000000000000:stack/' + stackName + '/';
function parseStackReceipt(doc, region, stackName) {
  const stackId = value(result(doc, 'CreateStack'), 'StackId'), prefix = stackPrefix(region, stackName);
  if (!stackId.startsWith(prefix) || !/^[0-9a-f]{32}$/.test(stackId.slice(prefix.length))) invalid();
  return stackId;
}

async function queryDocument(request, region, service, body, {signal, parse = toDocument} = {}) {
  signal?.throwIfAborted();
  const response = await request('/', {method: 'POST', signal, redirect: 'error', body, headers: {
    'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': service, 'X-Simulator-Region': region,
  }});
  if (response.status !== 200 || !response.body) invalid();
  // Bound bytes before DOM parsing. The regional read has at most 1,000 EC2
  // rows but may also include unrelated saved tags.
  const reader = response.body.getReader();
  const decoder = new TextDecoder('utf-8', {fatal: true});
  let size = 0, complete = false, raw = '';
  try {
    while (true) {
      signal?.throwIfAborted();
      const {done, value} = await reader.read();
      signal?.throwIfAborted();
      if (done) {
        complete = true;
        raw += decoder.decode();
        break;
      }
      if (value.byteLength > responseByteLimit - size) invalid();
      size += value.byteLength;
      raw += decoder.decode(value, {stream: true});
    }
  } finally {
    if (!complete) void reader.cancel().catch(() => {});
    reader.releaseLock();
  }
  if (/<!DOCTYPE/i.test(raw)) invalid();
  return parse(raw);
}

export async function submitInstanceStack(request, region, form, {allowed, signal, parse, onDispatch = () => {}} = {}) {
  if (allowed !== true) throw Error('This console session cannot create an instance stack.');
  if (!regionNames.has(region)) throw Error('Unknown local region.');
  const body = instanceStackParameters(form);
  // Recheck the same authenticated session before its only mutating request.
  // A stale startup advertisement, changed identity or expired runtime cannot
  // turn this into a silent CloudFormation fallback.
  signal?.throwIfAborted();
  const discovery = await request(capabilitiesPath, {method: 'GET', cache: 'no-store', redirect: 'error', signal});
  if (discovery.status !== 200 ||
      !instanceStackLaunchAvailable(await discovery.json(), discovery.headers.get('X-Simulator-Authentication') === 'required', 'simulator-admin')) {
    throw Error('The configured local instance runtime is unavailable.');
  }
  signal?.throwIfAborted();
  onDispatch();
  const doc = await queryDocument(request, region, 'cloudformation', body, {signal, parse});
  return {stackId: parseStackReceipt(doc, region, form.stackName), stackName: form.stackName, imageId: form.imageId};
}

export async function confirmInstanceStack(request, region, receipt, {allowed, signal, parse} = {}) {
  if (allowed !== true || !regionNames.has(region)) throw Error('The instance stack readback is unavailable.');
  const prefix = stackPrefix(region, receipt.stackName);
  if (!receipt.stackId.startsWith(prefix) || !/^[0-9a-f]{32}$/.test(receipt.stackId.slice(prefix.length))) invalid();
  const doc = await queryDocument(request, region, 'cloudformation', new URLSearchParams({
    Action: 'DescribeStacks', Version: '2010-05-15', StackName: receipt.stackId,
  }), {signal, parse});
  const root = result(doc, 'DescribeStacks');
  const member = one(one(root, 'Stacks'), 'member');
  if (fields(root, 'NextToken').length || value(member, 'StackId') !== receipt.stackId ||
      value(member, 'StackName') !== receipt.stackName) invalid();
  if (value(member, 'StackStatus') !== 'CREATE_COMPLETE') throw Error('CloudFormation did not report CREATE_COMPLETE.');
  const outputs = fields(one(member, 'Outputs'), 'member');
  if (outputs.length !== Object.keys(outputID).length) invalid();
  const ids = new Map();
  for (const entry of outputs) {
    const key = value(entry, 'OutputKey'), id = value(entry, 'OutputValue');
    if (ids.has(key) || !Object.hasOwn(outputID, key) || !outputID[key].test(id)) invalid();
    ids.set(key, id);
  }
  const ec2 = await queryDocument(request, region, 'ec2', new URLSearchParams({
    Action: 'DescribeInstances', Version: '2016-11-15',
  }), {signal, parse});
  const rows = parseEC2Document(ec2, 'ec2');
  const instance = rows.find(row => row.id === ids.get('InstanceId'));
  if (!instance || instance.name !== receipt.stackName || instance.image !== receipt.imageId || instance.type !== instanceType ||
      instance.vpc !== ids.get('VpcId') || instance.subnet !== ids.get('SubnetId') ||
      instance.groupIds.length !== 1 || instance.groupIds[0] !== ids.get('GroupId')) invalid();
  return {stackId: receipt.stackId, instance};
}
