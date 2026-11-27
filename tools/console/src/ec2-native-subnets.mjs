import {parseEC2Document} from './ec2-response.mjs';

const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const vpcId = /^vpc-([0-9a-f]{8}|[0-9a-f]{17})$/;
const subnetId = /^subnet-([0-9a-f]{8}|[0-9a-f]{17})$/;
const operations = ['DescribeSubnets', 'CreateSubnet', 'DeleteSubnet'];
const modifySubnet = 'ModifySubnetAttribute';
const subnetScope = new WeakMap();
const invalid = () => {throw Error('Invalid subnet reply');};
const xml = text => new DOMParser().parseFromString(text, 'application/xml');
const bytes = new TextEncoder();
const forbiddenXML = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\ud800-\udfff\ufffe\uffff]/u;
export const nativeSubnetTagLimit = 50;

export function vpcNativeSubnetsAvailable(capabilities, auth) {
  const ec2 = capabilities?.services?.ec2;
  return auth === true && !capabilities?.consoleScope && ec2?.console === true &&
    Array.isArray(ec2.subnetLifecycle) && operations.every(action => ec2.subnetLifecycle.includes(action));
}

export function vpcNativeSubnetPublicIPv4Available(capabilities, auth) {
  const ec2 = capabilities?.services?.ec2;
  return vpcNativeSubnetsAvailable(capabilities, auth) && ec2.subnetLifecycle.includes(modifySubnet) &&
    Array.isArray(ec2.operations) && ec2.operations.includes(modifySubnet);
}


export function vpcNativeSubnetTagsOnCreateAvailable(capabilities, auth) {
  const ec2 = capabilities?.services?.ec2;
  return vpcNativeSubnetsAvailable(capabilities, auth) && ec2.nativeSubnetTagsOnCreate === true &&
    Array.isArray(ec2.operations) && ['CreateSubnet','DescribeSubnets'].every(operation=>ec2.operations.includes(operation));
}

export function validNativeSubnetTagValue(value) {
  return typeof value === 'string' && bytes.encode(value).length <= 256 && !forbiddenXML.test(value);
}

export function validNativeSubnetTagKey(key) {
  return typeof key === 'string' && key.length > 0 && bytes.encode(key).length <= 128 &&
    !/^aws:/i.test(key) && !forbiddenXML.test(key);
}

function assertNativeSubnetTags(tags) {
  if (!Array.isArray(tags) || tags.length > nativeSubnetTagLimit) invalid();
  const seen = new Set();
  for (const tag of tags) {
    if (!validNativeSubnetTagKey(tag?.key) || !validNativeSubnetTagValue(tag?.value) || seen.has(tag.key)) invalid();
    seen.add(tag.key);
  }
}

function nativeSubnetTagParameters(tags) {
  if (!tags.length) return {};
  const values = {'TagSpecification.1.ResourceType':'subnet'};
  for (const [index,{key,value}] of tags.entries()) {
    const member = 'TagSpecification.1.Tag.' + (index + 1);
    values[member+'.Key'] = key;
    values[member+'.Value'] = value;
  }
  return values;
}

function exactTags(item, expected) {
  const found = item.tags ?? [];
  if (found.length !== expected.size) return false;
  return found.every(tag=>expected.get(tag.key) === tag.value);
}

function prefix(text) {
  const match = typeof text === 'string' && /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(1[6-9]|2[0-8])$/.exec(text.trim());
  if (!match || match.slice(1, 5).some(o => Number(o) > 255 || String(Number(o)) !== o)) return null;
  const bits = Number(match[5]);
  const address = match.slice(1, 5).reduce((n, o) => n * 256 + Number(o), 0);
  const size = 2 ** (32 - bits), start = address - address % size;
  const cidr = [24,16,8,0].map(shift => (start >>> shift) & 255).join('.') + '/' + bits;
  return {start, end: start + size, cidr};
}

export function subnetTarget(vpc) {
  if (!vpcId.test(vpc?.id) || !prefix(vpc?.cidr)) return '';
  return ['ec2', 'cloudformation'].includes(vpc.managedBy) ? vpc.managedBy : '';
}

export function nativeSubnetCIDR(vpc, input) {
  const parent = prefix(vpc?.cidr), child = prefix(input);
  return parent && child && child.start >= parent.start && child.end <= parent.end ? child.cidr : '';
}

function fields(node, allowed) {
  if (!node || [...(node.childNodes ?? [])].some(c => c.nodeType !== 1 && (c.nodeType !== 3 || c.textContent.trim()))) invalid();
  const found = new Map();
  for (const c of node.children) {
    if (c.namespaceURI !== ns || !allowed.includes(c.localName) || found.has(c.localName)) invalid();
    found.set(c.localName, c);
  }
  return found;
}

function text(node, optional = false) {
  if (!node && optional) return '';
  if (!node || node.children.length) invalid();
  return node.textContent;
}

function members(node, max) {
  if (!node || node.children.length > max || [...(node.childNodes ?? [])].some(c => c.nodeType !== 1 && (c.nodeType !== 3 || c.textContent.trim()))) invalid();
  const rows = [...node.children];
  if (rows.some(x => x.namespaceURI !== ns || x.localName !== 'item')) invalid();
  return rows;
}

function envelope(doc, action, body) {
  const root = doc?.documentElement;
  if (doc?.doctype || root?.namespaceURI !== ns || root.localName !== action + 'Response') invalid();
  const lookup = fields(root, ['requestId', body]);
  if (!text(lookup.get('requestId')).trim()) invalid();
  return lookup.get(body);
}

function row(item, region, publicIPv4Required, display = false) {
  const lookup = fields(item, ['subnetId','vpcId','cidrBlock','state','availabilityZone','simulatorManagedBy','tagSet','mapPublicIpOnLaunch']);
  const id = text(lookup.get('subnetId')), vpc = text(lookup.get('vpcId')), cidr = text(lookup.get('cidrBlock'));
  const az = text(lookup.get('availabilityZone'), true), owner = text(lookup.get('simulatorManagedBy'), true);
  const tags = [];
  if (!subnetId.test(id) || !vpcId.test(vpc) || prefix(cidr)?.cidr !== cidr || text(lookup.get('state')) !== 'available' ||
      lookup.has('simulatorManagedBy') && owner !== 'ec2' || (az || owner) && !['a','b','c'].some(suffix=>az === region + suffix)) invalid();
  if (lookup.has('tagSet')) {
    const seen = new Set();
    for (const tag of members(lookup.get('tagSet'), nativeSubnetTagLimit)) {
      const fieldsForTag = fields(tag, ['key','value']);
      const key = text(fieldsForTag.get('key')), value = text(fieldsForTag.get('value'));
      if (!key || seen.has(key) || bytes.encode(key).length > 128 || bytes.encode(value).length > 256) invalid();
      if (owner === 'ec2' && (!validNativeSubnetTagKey(key) || !validNativeSubnetTagValue(value))) invalid();
      seen.add(key);
      tags.push(Object.freeze({key,value}));
    }
  }
  const saved = Object.freeze(tags);
  const meta = display ? {name:tags.find(tag=>tag.key==='Name')?.value ?? '',state:'available',tags:saved}
    : owner === 'ec2' && tags.length ? {tags:saved} : {};
  const flag = lookup.get('mapPublicIpOnLaunch');
  if (flag) {
    const value = text(flag);
    if (owner !== 'ec2' || !['true','false'].includes(value)) invalid();
    return {id,vpc,cidr,az,owner,publicIPv4:value === 'true',...meta};
  }
  if (owner && publicIPv4Required) invalid();
  return {id,vpc,cidr,az,owner:owner || 'cloudformation',...meta};
}

function regionalSubnetRows(doc,region,publicIPv4,display=false) {
  const rows=members(envelope(doc, 'DescribeSubnets', 'subnetSet'),1000).map(item=>row(item,region,publicIPv4,display));
  if(new Set(rows.map(item=>item.id)).size!==rows.length) invalid();
  return rows;
}

export function parseVpcSubnets(doc, region, vpc, {publicIPv4=false} = {}) {
  const selected = subnetTarget(vpc);
  if (!selected) invalid();
  const all = regionalSubnetRows(doc,region,publicIPv4);
  const filtered = all.filter(item=>item.vpc===vpc.id);
  if (filtered.some(item=>item.owner!==selected || nativeSubnetCIDR(vpc,item.cidr)!==item.cidr)) invalid();
  return filtered;
}

// Count actual streamed UTF-8 bytes, not a declared Content-Length. Large
// regional snapshots are unavailable in the Console, never truncated to empty.
async function readXML(response, signal) {
  const type = response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase();
  const reader = response.body?.getReader();
  if (!reader) invalid();
  let size = 0, text = '', done = false;
  const decoder = new TextDecoder('utf-8', {fatal:true});
  try {
    if (type !== 'text/xml' && type !== 'application/xml') invalid();
    for (;;) {
      signal?.throwIfAborted();
      const chunk = await reader.read();
      if (chunk.done) {done = true; break;}
      size += chunk.value.byteLength;
      if (size > 16 * 1024 * 1024) invalid();
      text += decoder.decode(chunk.value, {stream:true});
    }
    signal?.throwIfAborted();
    return text + decoder.decode();
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function send(request, region, action, values, signal, parse) {
  signal?.throwIfAborted();
  const response = await request('/', {method:'POST', credentials:'same-origin', redirect:'error', signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action: action, Version:'2016-11-15', ...values})});
  if (response.status !== 200) throw Error(response.status + ': Native subnet request failed');
  return parse(await readXML(response, signal));
}

// One bounded DescribeVpcs and one bounded DescribeSubnets, not one
// DescribeSubnets request per VPC. Reject a mismatched pair (including
// concurrent deletion or mismatched ownership) rather than showing partial
// or falsely empty results. This is not a transactional combined snapshot.
export async function loadRegionalSubnetInventory(request,region,{signal,parse=xml,publicIPv4=false}={}) {
  const [vpcDoc,subnetDoc]=await Promise.all([
    send(request,region,'DescribeVpcs',{},signal,parse),
    send(request,region,'DescribeSubnets',{},signal,parse),
  ]);
  if(vpcDoc?.doctype) invalid();
  const vpcs=parseEC2Document(vpcDoc,'vpc');
  if(vpcs.some(vpc=>!subnetTarget(vpc)||prefix(vpc.cidr)?.cidr!==vpc.cidr)) invalid();
  const byId=new Map(vpcs.map(vpc=>[vpc.id,vpc]));
  const subnets=regionalSubnetRows(subnetDoc,region,publicIPv4,true);
  if(subnets.some(item=>{
    const parent=byId.get(item.vpc);
    return !parent||subnetTarget(parent)!==item.owner||nativeSubnetCIDR(parent,item.cidr)!==item.cidr;
  })) invalid();
  signal?.throwIfAborted();
  return {vpcs,subnets};
}

export async function loadVpcSubnets(request, region, vpc, {signal, parse=xml, publicIPv4=false} = {}) {
  if (!subnetTarget(vpc)) invalid();
  const doc = await send(request,region,'DescribeSubnets',{},signal,parse);
  const rows = parseVpcSubnets(doc,region,vpc,{publicIPv4});
  const binding = {request,region,vpc:vpc.id,cidr:vpc.cidr,publicIPv4};
  for (const item of rows) subnetScope.set(Object.freeze(item),binding);
  return rows;
}

export async function createVpcNativeSubnet(request, region, vpc, cidr, az, opts = {}) {
  const {signal, parse=xml, tags=[]} = opts;
  const canon = nativeSubnetCIDR(vpc,cidr);
  if (subnetTarget(vpc)!=='ec2' || !canon || !['a','b','c'].some(s=>az===region+s)) invalid();
  assertNativeSubnetTags(tags);
  if (tags.length && !vpcNativeSubnetTagsOnCreateAvailable(opts.capabilities, opts.authenticationRequired)) invalid();
  const expected = new Map(tags.map(({key,value})=>[key,value]));
  const doc = await send(request,region,'CreateSubnet',
    {VpcId:vpc.id,CidrBlock:canon,AvailabilityZone:az,...nativeSubnetTagParameters(tags)},signal,parse);
  const created = row(envelope(doc,'CreateSubnet','subnet'),region,opts.publicIPv4);
  if (created.vpc!==vpc.id || created.cidr!==canon || created.az!==az || created.owner!=='ec2' || !exactTags(created,expected)) invalid();
  const rows = await loadVpcSubnets(request,region,vpc,opts);
  if (!rows.some(item=>item.id===created.id && item.vpc===created.vpc && item.cidr===canon && item.az===az &&
      item.owner==='ec2' && exactTags(item,expected))) invalid();
  return {created,rows};
}

export async function deleteVpcNativeSubnet(request, region, vpc, subnet, opts = {}) {
  const {signal, parse=xml} = opts;
  if (subnetTarget(vpc)!=='ec2' || subnet?.vpc!==vpc.id || subnet.owner!=='ec2' || !subnetId.test(subnet.id)) invalid();
  const doc = await send(request,region,'DeleteSubnet',{SubnetId:subnet.id},signal,parse);
  if (text(envelope(doc,'DeleteSubnet','return'))!=='true') invalid();
  const rows = await loadVpcSubnets(request,region,vpc,opts);
  if (rows.some(item=>item.id===subnet.id)) invalid();
  return {removed: subnet,rows};
}

export async function modifyVpcNativeSubnetPublicIPv4(request, region, vpc, subnet, enabled, opts = {}) {
  const {signal, parse=xml, publicIPv4} = opts;
  const binding = subnetScope.get(subnet);
  if (!binding?.publicIPv4 || publicIPv4 !== true || binding.request !== request ||
      binding.region !== region || binding.vpc !== vpc.id || binding.cidr !== vpc.cidr ||
      subnetTarget(vpc) !== 'ec2' || subnet.vpc !== vpc.id || subnet.owner !== 'ec2' ||
      typeof subnet.publicIPv4 !== 'boolean' || typeof enabled !== 'boolean') invalid();
  const doc = await send(request,region,modifySubnet,{SubnetId:subnet.id,'MapPublicIpOnLaunch.Value':String(enabled)},signal,parse);
  if (text(envelope(doc,modifySubnet,'return')) !== 'true') invalid();
  const rows = await loadVpcSubnets(request,region,vpc,opts);
  const updated = rows.find(item=>item.id===subnet.id && item.vpc===subnet.vpc && item.cidr===subnet.cidr &&
    item.az===subnet.az && item.owner==='ec2' && item.publicIPv4===enabled);
  signal?.throwIfAborted();
  if (!updated) invalid();
  return {updated,rows};
}

export function nativeSubnetFailure(error) {
  const message = error?.message ?? '';
  if (/^(401:|403:|UnauthorizedOperation:|Sign in to continue\.)/.test(message)) return 'denied';
  if (message.startsWith('InvalidSubnet.Conflict:')) return 'overlap';
  if (message.startsWith('DependencyViolation:')) return 'dependency';
  return 'unconfirmed';
}
