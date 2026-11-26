import {parseEC2Error} from './ec2-response.mjs';
import {vpcSecurityGroupsAvailable} from './ec2-security-groups.mjs';

const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const nativeVPC = /^vpc-[0-9a-f]{17}$/;
const groupID = /^sg-[0-9a-f]{17}$/;
const regionID = /^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$/;
const actions = ['CreateSecurityGroup','DescribeSecurityGroups','DeleteSecurityGroup'];
const mode = 'detached-config-only';
const bytes = new TextEncoder();
const xml = text => new DOMParser().parseFromString(text, 'application/xml');
const invalid = () => {throw Error('Invalid or incomplete native security group response.');};

export function vpcNativeSecurityGroupsAvailable(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2;
  const selectors = ec2?.describeSelectors?.DescribeSecurityGroups;
  return vpcSecurityGroupsAvailable(capabilities,authenticationRequired)
    && Array.isArray(ec2.operations) && actions.every(action=>ec2.operations.includes(action))
    && Array.isArray(ec2.detachedConfigurationOnly) && ec2.detachedConfigurationOnly.includes('native security groups')
    && Array.isArray(selectors) && selectors.includes('vpc-id');
}

export function nativeSGTarget(vpc) {
  return vpc?.managedBy === 'ec2' && typeof vpc.id==='string' && nativeVPC.test(vpc.id);
}

function validText(text) {
  return typeof text === 'string' && text.length>=1 && text.length<=255 &&
    [...text].every(c=>/^[A-Za-z0-9]$/.test(c)||' ._-:/()#,@[]+=&;{}!$*'.includes(c));
}

export function nativeSGName(name) {
  if (!validText(name)) return '';
  const trimmed = name.replace(/ +$/,'');
  return trimmed && !/^(default$|sg-)/i.test(trimmed) ? trimmed : '';
}

export function validNativeSGDescription(description) {
  return validText(description);
}

function cleanChildren(node) {
  if (!node || [...(node.childNodes??[])].some(c=>c.nodeType!==1 && (c.nodeType!==3||c.textContent.trim()))) invalid();
  return [...node.children];
}

function fields(node, allowed) {
  const found = new Map();
  for (const child of cleanChildren(node)) {
    const key = child.localName;
    if (child.namespaceURI!==ns || !allowed.includes(key) || found.has(key)) invalid();
    found.set(key,child);
  }
  return found;
}

function leaf(found, key) {
  const node = found.get(key);
  if (!node || node.children.length) invalid();
  return node.textContent;
}

function members(node, max) {
  const rows = cleanChildren(node);
  if (rows.length>max || rows.some(row=>row.namespaceURI!==ns||row.localName!=='item')) invalid();
  return rows;
}

function envelope(document, action, allowed) {
  const root = document?.documentElement;
  if (document?.doctype || root?.localName!==action+'Response' || root.namespaceURI!==ns) invalid();
  const found = fields(root,['requestId',...allowed]);
  if (!leaf(found,'requestId').trim()) invalid();
  return found;
}

function tags(found) {
  if (!found.has('tagSet')) return [];
  const keys = new Set();
  return members(found.get('tagSet'),50).map(item=>{
    const tag = fields(item,['key','value']), key = leaf(tag,'key'), value = leaf(tag,'value');
    if (!key || /^aws:/i.test(key) || keys.has(key) || bytes.encode(key).length>128 || bytes.encode(value).length>256) invalid();
    keys.add(key);
    return {key,value};
  });
}

export function parseVpcNativeSecurityGroups(document,vpc) {
  if (!nativeSGTarget(vpc)) invalid();
  const envelopeFields = envelope(document,'DescribeSecurityGroups',['securityGroupInfo']);
  const ids = new Set(), names = new Set();
  return members(envelopeFields.get('securityGroupInfo'),1000).map(item=>{
    const row = fields(item,['ownerId','groupId','groupName','groupDescription','vpcId','ipPermissions','tagSet','simulatorSecurityGroupMode']);
    const id = leaf(row,'groupId'), name = leaf(row,'groupName'), description = leaf(row,'groupDescription');
    const lower = name.toLowerCase();
    if (!groupID.test(id) || ids.has(id) || !name || nativeSGName(name)!==name || names.has(lower) || !validNativeSGDescription(description)
        || leaf(row,'vpcId')!==vpc.id || leaf(row,'ownerId')!=='000000000000' || leaf(row,'simulatorSecurityGroupMode')!==mode) invalid();
    members(row.get('ipPermissions'),0);
    ids.add(id);names.add(lower);
    return {id,name,description,vpc:vpc.id,mode,tags:tags(row)};
  });
}

async function send(request,region,action,params,opts) {
  if (typeof region!=='string' || region.length>32 || !regionID.test(region) || !actions.includes(action)) invalid();
  const {signal,parse=xml} = opts;
  signal?.throwIfAborted();
  const response = await request('/',{
    method:'POST',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',...params}),
  });
  const text = await response.text();
  signal?.throwIfAborted();
  const doc = parse(text);
  if (!response.ok) throw Error((parseEC2Error(doc)?.code??response.status)+': Native security group request refused.');
  return doc;
}

export async function loadVpcNativeSecurityGroups(request,region,vpc,opts={}) {
  if (!nativeSGTarget(vpc)) invalid();
  const document = await send(request,region,'DescribeSecurityGroups',{'Filter.1.Name':'vpc-id','Filter.1.Value.1':vpc.id},opts);
  return parseVpcNativeSecurityGroups(document,vpc);
}

export async function createVpcNativeSecurityGroup(request,region,vpc,{name,description},opts={}) {
  const normalized = nativeSGName(name);
  if (!nativeSGTarget(vpc) || !normalized || !validNativeSGDescription(description)) invalid();
  const doc = await send(request,region,'CreateSecurityGroup',{VpcId:vpc.id,GroupName:normalized,GroupDescription:description},opts);
  const receipt = envelope(doc,'CreateSecurityGroup',['groupId','securityGroupArn','return','tagSet']);
  const id = leaf(receipt,'groupId');
  if (!groupID.test(id) || leaf(receipt,'return')!=='true' || tags(receipt).length ||
      leaf(receipt,'securityGroupArn')!==`arn:aws:ec2:${region}:000000000000:security-group/${id}`) invalid();
  const rows = await loadVpcNativeSecurityGroups(request,region,vpc,opts);
  const created = rows.find(g=>g.id===id && g.name===normalized && g.description===description && g.tags.length===0);
  if (!created) invalid();
  return {created,rows};
}

export async function deleteVpcNativeSecurityGroup(request,region,vpc,group,opts={}) {
  if (!nativeSGTarget(vpc) || group?.vpc!==vpc.id || group?.mode!==mode || !groupID.test(group.id)) invalid();
  const doc = await send(request,region,'DeleteSecurityGroup',{GroupId:group.id},opts);
  const receipt = envelope(doc,'DeleteSecurityGroup',['groupId','return','tagSet']);
  if (leaf(receipt,'return')!=='true' || leaf(receipt,'groupId')!==group.id || tags(receipt).length) invalid();
  const rows = await loadVpcNativeSecurityGroups(request,region,vpc,opts);
  if (rows.some(g=>g.id===group.id)) invalid();
  return {removed:group,rows};
}

export function nativeSGFailure(error) {
  const message = error?.message ?? '';
  if (/^(401:|403:|UnauthorizedOperation:|Sign in to continue\.)/.test(message)) return 'denied';
  if (message.startsWith('InvalidGroup.Duplicate:')) return 'duplicate';
  if (message.startsWith('DependencyViolation:')) return 'dependency';
  return 'unconfirmed';
}
