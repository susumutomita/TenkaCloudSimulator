// This is the simulator's CloudFormation declaration preview, not EC2 Launch Templates.
const namespace = 'http://cloudformation.amazonaws.com/doc/2010-05-15/';
const maxResponseBytes = 384 * 1024;
const identifier = /^[A-Za-z][A-Za-z0-9]{0,254}$/;
const actions = new Set(['CAPABILITY_IAM', 'CAPABILITY_NAMED_IAM']);
export const templatePreviewByteLimit = 51200;

export const exampleEC2Template = `AWSTemplateFormatVersion: '2010-09-09'
Description: Local EC2 declaration example. Preview only.
Parameters:
  ImageId:
    Type: String
    Description: An image configured by the simulator operator.
Resources:
  Vpc:
    Type: AWS::EC2::VPC
    Properties: {CidrBlock: 10.91.0.0/16}
  Subnet:
    Type: AWS::EC2::Subnet
    Properties: {VpcId: !Ref Vpc, CidrBlock: 10.91.1.0/24}
  Group:
    Type: AWS::EC2::SecurityGroup
    Properties: {GroupDescription: No ingress, VpcId: !Ref Vpc}
  Role:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal: {Service: ec2.amazonaws.com}
            Action: sts:AssumeRole
  Profile:
    Type: AWS::IAM::InstanceProfile
    Properties:
      Roles: [!Ref Role]
  Instance:
    Type: AWS::EC2::Instance
    Properties:
      ImageId: !Ref ImageId
      InstanceType: t3.micro
      IamInstanceProfile: !Ref Profile
      SubnetId: !Ref Subnet
      SecurityGroupIds: [!Ref Group]
      UserData: I2Nsb3VkLWNvbmZpZwp7fQo=
`;

export function templatePreviewInputProblem(template) {
  if (typeof template !== 'string' || !template.trim()) return 'empty';
  if (template.length > templatePreviewByteLimit || new TextEncoder().encode(template).length > templatePreviewByteLimit) return 'size';
  return '';
}

const invalid = () => {throw Error('Unconfirmed template preview.');};
function containsOnlyElements(node) {
  return [...node.childNodes].every(child => child.nodeType === 1 || (child.nodeType === 3 && !child.textContent.trim()));
}
function fields(node, allowed) {
  if (!containsOnlyElements(node)) invalid();
  const result = new Map();
  for (const child of node.children) {
    if (child.namespaceURI !== namespace || !allowed.includes(child.localName) || result.has(child.localName)) invalid();
    result.set(child.localName, child);
  }
  return result;
}
function leaf(node) {
  if (!node || [...node.childNodes].some(child => child.nodeType !== 3)) invalid();
  return node.textContent;
}
function members(node, max) {
  if (!node || !containsOnlyElements(node) || node.children.length > max) invalid();
  const children = [...node.children];
  if (children.some(child => child.namespaceURI !== namespace || child.localName !== 'member')) invalid();
  return children;
}

export function parseTemplatePreview(doc) {
  const root = doc.documentElement;
  if (doc.doctype || root?.namespaceURI !== namespace || root.localName !== 'ValidateTemplateResponse') invalid();
  const envelope = fields(root, ['ValidateTemplateResult', 'ResponseMetadata']);
  const result = envelope.get('ValidateTemplateResult'), meta = envelope.get('ResponseMetadata');
  if (!result || !meta || !leaf(fields(meta, ['RequestId']).get('RequestId'))) invalid();
  const declared = fields(result, ['Description', 'Parameters', 'Capabilities', 'CapabilitiesReason']);
  if (declared.has('Description')) leaf(declared.get('Description'));
  const seen = new Set();
  const parameters = members(declared.get('Parameters'), 100).map(node => {
    const item = fields(node, ['ParameterKey', 'NoEcho', 'Description', 'DefaultValue']);
    const name = leaf(item.get('ParameterKey')), mask = leaf(item.get('NoEcho')), defaultDeclared = item.has('DefaultValue');
    if (!identifier.test(name) || seen.has(name) || !['true', 'false'].includes(mask) || (mask === 'true' && defaultDeclared)) invalid();
    seen.add(name);
    if (item.has('Description')) leaf(item.get('Description'));
    if (defaultDeclared) leaf(item.get('DefaultValue'));
    return {name, noEcho: mask === 'true', defaultDeclared};
  });
  let acknowledgement = '';
  if (declared.has('Capabilities')) {
    const capabilities = members(declared.get('Capabilities'), 1);
    acknowledgement = leaf(capabilities[0]);
    if (!actions.has(acknowledgement) || !leaf(declared.get('CapabilitiesReason'))) invalid();
  } else if (declared.has('CapabilitiesReason')) invalid();
  return {parameters, acknowledgement};
}

async function readBoundedXML(response, signal) {
  const kind = response.headers.get('Content-Type')?.split(';')[0]?.trim().toLowerCase();
  const reader = response.body?.getReader();
  if (!reader) invalid();
  let size = 0, text = '', done = false;
  const decode = new TextDecoder('utf-8', {fatal: true});
  try {
    if (kind !== 'text/xml' && kind !== 'application/xml') invalid();
    for (;;) {
      signal?.throwIfAborted();
      const chunk = await reader.read();
      if (chunk.done) {done = true; break;}
      size += chunk.value.byteLength;
      if (size > maxResponseBytes) invalid();
      text += decode.decode(chunk.value, {stream: true});
    }
    signal?.throwIfAborted();
    return text + decode.decode();
  } finally {
    if (!done) void reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export async function validateEC2Template(request, region, template, {signal, parse = text => new DOMParser().parseFromString(text, 'application/xml')} = {}) {
  signal?.throwIfAborted();
  if (templatePreviewInputProblem(template)) throw Error('Invalid preview input.');
  const response = await request('/', {
    method: 'POST', credentials: 'same-origin', redirect: 'error', signal,
    headers: {'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'cloudformation',
      'X-Simulator-Region': region},
    body: new URLSearchParams({Action: 'ValidateTemplate', Version: '2010-05-15', TemplateBody: template}),
  });
  if (!response.ok) throw Error(String(response.status));
  const xml = await readBoundedXML(response, signal);
  return parseTemplatePreview(parse(xml));
}

export function templatePreviewFailure(error) {
  const message = error?.message || '';
  if (/^(401\b|403\b|AccessDenied:|Sign in to continue\.)/.test(message)) return 'denied';
  if (/^(400\b|ValidationError:)/.test(message)) return 'invalid';
  return 'unconfirmed';
}
