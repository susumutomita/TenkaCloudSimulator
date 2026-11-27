import {parseEC2Document} from './ec2-response.mjs';
import {ec2IPv4Prefix} from './ec2-ipv4-route-prefix.mjs';

// This wire represents saved ACL configuration, not packet filtering. A
// wrong or incomplete reply is unavailable, not an empty list or success.
const namespace = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const mode = 'detached-config-only';
const defaultMode = 'default-config-only';
const attachedMode = 'attached-config-only';
const associationId = /^aclassoc-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const subnetId = /^subnet-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const vpcId = /^vpc-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const aclId = /^acl-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const tokenShape = /^[ -~]{1,64}$/;
const operations = ['DescribeNetworkAcls','CreateNetworkAcl','DeleteNetworkAcl'];
const ruleActions = {create:'CreateNetworkAclEntry',replace:'ReplaceNetworkAclEntry',delete:'DeleteNetworkAclEntry'};
const terminalRule = 32767;
const rulesPerDirection = 20;
const isShape = (value, re) => typeof value === 'string' && re.exec(value)?.[0] === value;
const invalid = () => {throw Error('Invalid or incomplete network ACL response.');};
const bytes = new TextEncoder();
const xml = text => new DOMParser().parseFromString(text,'application/xml');

export function vpcNetworkACLsAvailable(capabilities, authenticationRequired) {
  const access = networkACLInventoryOperations(capabilities, authenticationRequired);
  return access.read && access.create && access.delete;
}

export function networkACLInventoryOperations(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2;
  const selectors = ec2?.describeSelectors?.DescribeNetworkAcls;
  const read = authenticationRequired === true && !capabilities?.consoleScope && ec2?.console === true &&
    Array.isArray(ec2.operations) && ec2.operations.includes(operations[0]) &&
    Array.isArray(ec2.detachedConfigurationOnly) && ec2.detachedConfigurationOnly.includes('native network ACLs') &&
    Array.isArray(selectors) && selectors.includes('vpc-id');
  return {read, create:read && ec2.operations.includes(operations[1]), delete:read && ec2.operations.includes(operations[2]),
    vpcs:read && ec2.operations.includes('DescribeVpcs')};
}

export function subnetACLAssociationAvailable(capabilities, authenticationRequired) {
  if (!networkACLInventoryOperations(capabilities,authenticationRequired).read) return false;
  const ec2=capabilities.services.ec2;
  return ec2.networkAclAssociationProfile==='single-native-ipv4-subnet-config-only' &&
    Array.isArray(ec2.subnetLifecycle) && ec2.subnetLifecycle.includes('CreateSubnet') &&
    ['DescribeSubnets','ReplaceNetworkAclAssociation'].every(action=>ec2.operations.includes(action)) &&
    ec2.describeSelectors.DescribeNetworkAcls.includes('association.subnet-id');
}

export function networkACLRuleActionsAvailable(capabilities, authenticationRequired) {
  if (!networkACLInventoryOperations(capabilities,authenticationRequired).read) return false;
  const ec2=capabilities.services.ec2;
  return ec2.operations.includes('DescribeVpcs') && ec2.describeSelectors.DescribeNetworkAcls.includes('NetworkAclId.N') &&
    Object.values(ruleActions).every(action=>ec2.operations.includes(action));
}

export const validNetworkACLVpcID = value=>isShape(value,vpcId);
export const validNetworkACLID = value=>isShape(value,aclId);

export function networkACLTarget(vpc) {
  if (!isShape(vpc?.id,vpcId)) return '';
  return ['ec2','cloudformation'].includes(vpc.managedBy) ? vpc.managedBy : '';
}

export function validNetworkACLName(value) {
  if (typeof value !== 'string' || bytes.encode(value).length > 256) return false;
  for (const symbol of value) {
    const r = symbol.codePointAt(0);
    if ((r < 32 && r !== 9 && r !== 10 && r !== 13) || (r >= 0xd800 && r <= 0xdfff) || r === 0xfffe || r === 0xffff) return false;
  }
  return true;
}

function children(node) {
  if (!node || [...(node.childNodes ?? [])].some(c=>c.nodeType!==1&&(c.nodeType!==3||c.textContent.trim()))) invalid();
  const elements = [...node.children];
  if (elements.some(c=>c.namespaceURI!==namespace)) invalid();
  return elements;
}
function fields(node, allowed) {
  const result = new Map();
  for (const member of children(node)) {
    if (!allowed.includes(member.localName) || result.has(member.localName)) invalid();
    result.set(member.localName,member);
  }
  return result;
}
function text(node) {
  if (!node || node.children.length || [...(node.childNodes ?? [])].some(c=>c.nodeType!==3&&c.nodeType!==4)) invalid();
  return node.textContent;
}
function items(node, max) {
  const result = children(node);
  if (result.length > max || result.some(c=>c.localName!=='item')) invalid();
  return result;
}
function envelope(document, action, members) {
  const root = document?.documentElement;
  if (document?.doctype || root?.namespaceURI!==namespace || root.localName!==action+'Response') invalid();
  const result = fields(root,['requestId',...members]);
  if (!text(result.get('requestId')).trim()) invalid();
  return result;
}

const decimal = (value, min, max) => {
  const raw = (typeof value === 'string' || typeof value === 'number') ? String(value) : '';
  return isShape(raw,/^(?:0|[1-9][0-9]{0,4})$/) && Number(raw)>=min && Number(raw)<=max ? Number(raw) : null;
};

export function normalizeNetworkACLRule(input) {
  const number=decimal(input?.number,1,terminalRule-1), prefix=ec2IPv4Prefix(input?.cidr);
  if (number===null || !prefix || !['-1','6','17'].includes(input?.protocol) ||
      !['allow','deny'].includes(input?.action) || typeof input?.egress!=='boolean') return null;
  const value={number,protocol:input.protocol,action:input.action,cidr:prefix.masked,egress:input.egress};
  if (value.protocol==='-1') return input.ports===undefined ? value : null;
  const from=decimal(input?.ports?.from,0,65535), to=decimal(input?.ports?.to,0,65535);
  return from!==null && to!==null && from<=to ? {...value,ports:{from,to}} : null;
}

function aclEntries(node) {
  const entries=items(node,2+2*rulesPerDirection).map(entry=>{
    const f=fields(entry,['ruleNumber','protocol','ruleAction','egress','cidrBlock','portRange']);
    const number=text(f.get('ruleNumber')), protocol=text(f.get('protocol')), action=text(f.get('ruleAction'));
    const cidr=text(f.get('cidrBlock')), direction=text(f.get('egress'));
    if (!['false','true'].includes(direction)) invalid();
    const egress=direction==='true';
    if (number===String(terminalRule)) {
      if (protocol!=='-1' || action!=='deny' || cidr!=='0.0.0.0/0' || f.has('portRange')) invalid();
      return {number:terminalRule,protocol,action,cidr,egress};
    }
    const ports=f.has('portRange') ? fields(f.get('portRange'),['from','to']) : null;
    const result=normalizeNetworkACLRule({number,protocol,action,cidr,egress,
      ...(ports ? {ports:{from:text(ports.get('from')),to:text(ports.get('to'))}} : {})});
    if (!result || result.cidr!==cidr) invalid();
    return result;
  });
  for (const direction of [false,true]) {
    const list=entries.filter(rule=>rule.egress===direction);
    if (!list.length || list.length>rulesPerDirection+1 || list.at(-1).number!==terminalRule ||
        list.some((rule,index)=>index>0 && list[index-1].number>=rule.number)) invalid();
  }
  return entries;
}

function row(node, selected) {
  const cols=fields(node,['networkAclId','vpcId','ownerId','default','entrySet','associationSet','tagSet','simulatorNetworkAclMode']);
  const id=text(cols.get('networkAclId')), vpc=text(cols.get('vpcId')), aclMode=text(cols.get('simulatorNetworkAclMode'));
  const defaultValue=text(cols.get('default'));
  if (!validNetworkACLID(id) || !validNetworkACLVpcID(vpc) || (selected && vpc!==selected) || text(cols.get('ownerId'))!=='000000000000' ||
      !['false','true'].includes(defaultValue) || ![mode,defaultMode,attachedMode].includes(aclMode) ||
      (defaultValue==='true')!==(aclMode===defaultMode)) invalid();
  const entries=aclEntries(cols.get('entrySet'));
  if (aclMode===defaultMode && (entries.length!==4 || [false,true].some(egress=>
    !entries.some(rule=>rule.egress===egress&&rule.number===100&&rule.protocol==='-1'&&
      rule.action==='allow'&&rule.cidr==='0.0.0.0/0')))) invalid();
  const associations=items(cols.get('associationSet'),1000).map(item=>{
    const f=fields(item,['networkAclAssociationId','networkAclId','subnetId']);
    const assoc=text(f.get('networkAclAssociationId')), subnet=text(f.get('subnetId'));
    if (!isShape(assoc,associationId) || text(f.get('networkAclId'))!==id || !isShape(subnet,subnetId)) invalid();
    return {id:assoc,subnetId:subnet};
  });
  if ((aclMode===mode && associations.length) || (aclMode===attachedMode && !associations.length)) invalid();
  const tags=new Map();
  for (const tag of cols.has('tagSet') ? items(cols.get('tagSet'),50) : []) {
    const f=fields(tag,['key','value']);
    const key=text(f.get('key')), value=text(f.get('value'));
    if (!key || tags.has(key) || bytes.encode(key).length > 128 || /^aws:/i.test(key) ||
        !validNetworkACLName(key) || !validNetworkACLName(value)) invalid();
    tags.set(key,value);
  }
  return {id,vpc,mode:aclMode,name:tags.get('Name')??'',entries,associations,isDefault:defaultValue==='true'};
}

export function parseVpcNetworkACLs(document, selected) {
  if (!isShape(selected,vpcId)) invalid();
  return networkACLRows(document,selected,false);
}

export function parseNetworkACLInventory(document, selected='') {
  if (selected!=='' && !validNetworkACLVpcID(selected)) invalid();
  return networkACLRows(document,selected,true);
}

function networkACLRows(document, selected, details) {
  const members=envelope(document,'DescribeNetworkAcls',['networkAclSet']);
  const rows=items(members.get('networkAclSet'),1000).map(item=>row(item,selected));
  const seen=new Set(),defaults=new Set(),associations=new Set(),subnets=new Set();
  for (const acl of rows) {
    if (seen.has(acl.id) || (acl.isDefault && defaults.has(acl.vpc))) invalid();
    seen.add(acl.id);
    if (acl.isDefault) defaults.add(acl.vpc);
    for (const a of acl.associations) {
      if (associations.size>=1000 || associations.has(a.id) || subnets.has(a.subnetId)) invalid();
      associations.add(a.id);subnets.add(a.subnetId);
    }
  }
  // The inline VPC editor continues to manage only detached configurations.
  // Validate every wire row before projecting the safe subset.
  return details ? rows : rows.filter(acl=>acl.mode===mode).map(({id,vpc,mode,name})=>({id,vpc,mode,name}));
}

async function readReply(response, signal) {
  const type=response.headers?.get('content-type')?.split(';')[0].trim().toLowerCase();
  const reader=response.body?.getReader();
  if (!reader) invalid();
  const decoder=new TextDecoder('utf-8',{fatal:true});
  let data='', size=0, done=false;
  try {
    if (type!=='text/xml' && type!=='application/xml') invalid();
    for (;;) {
      signal?.throwIfAborted();
      const part=await reader.read();
      if (part.done) {done=true;break;}
      size+=part.value.byteLength;
      if (size > 16*1024*1024) throw Error('Network ACL response exceeds the Console size limit.');
      data+=decoder.decode(part.value,{stream:true});
    }
    signal?.throwIfAborted();
    return data+decoder.decode();
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function sendQuery(request, region, action, params, {signal,parse=xml}) {
  signal?.throwIfAborted();
  if (!isShape(region,/^[a-z]{2}(?:-[a-z0-9]+)+-\d+$/) || region.length > 32) invalid();
  const response=await request('/',{method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',...params})});
  signal?.throwIfAborted();
  if (!response?.ok) throw Error((response?.status??0)+': Network ACL request failed.');
  return parse(await readReply(response,signal));
}

function send(request, region, vpc, action, params, options) {
  if (networkACLTarget(vpc)!=='ec2') invalid();
  return sendQuery(request,region,action,params,options);
}

export async function loadNetworkACLInventory(request, region, selected='', options={}) {
  if (selected!=='' && !validNetworkACLVpcID(selected)) invalid();
  const filter=selected ? {'Filter.1.Name':'vpc-id','Filter.1.Value.1':selected} : {};
  return parseNetworkACLInventory(await sendQuery(request,region,'DescribeNetworkAcls',filter,options),selected);
}

export async function loadNetworkACLVpcs(request, region, options={}) {
  const document=await sendQuery(request,region,'DescribeVpcs',{},options);
  const members=envelope(document,'DescribeVpcs',['vpcSet','reservationSet']);
  // The native shared Describe envelope may include an empty reservationSet.
  if (members.has('reservationSet') && children(members.get('reservationSet')).length!==0) invalid();
  const records=items(members.get('vpcSet'),1000);
  const rows=parseEC2Document(document,'vpc');
  if (rows.length!==records.length || rows.some(row=>!validNetworkACLVpcID(row.id) || !validNetworkACLName(row.name))) invalid();
  return rows.filter(row=>networkACLTarget(row)==='ec2');
}

export async function loadVpcNetworkACLs(request, region, vpc, options={}) {
  const document=await send(request,region,vpc,'DescribeNetworkAcls',{'Filter.1.Name':'vpc-id','Filter.1.Value.1':vpc?.id},options);
  return parseVpcNetworkACLs(document,vpc.id);
}

export async function createVpcNetworkACL(request, region, vpc, name, clientToken, options={}) {
  if (!validNetworkACLName(name) || !isShape(clientToken,tokenShape)) invalid();
  const fieldsForTag=name ? {'TagSpecification.1.ResourceType':'network-acl','TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name} : {};
  const document=await send(request,region,vpc,'CreateNetworkAcl',{VpcId:vpc?.id,ClientToken:clientToken,...fieldsForTag},options);
  const members=envelope(document,'CreateNetworkAcl',['networkAcl','clientToken']);
  const record=row(members.get('networkAcl'),vpc.id);
  if (record.isDefault || record.name!==name || text(members.get('clientToken'))!==clientToken) invalid();
  const all=await loadNetworkACLInventory(request,region,vpc.id,options);
  const saved=all.find(item=>item.id===record.id);
  if (!saved || saved.name!==record.name || saved.mode!==record.mode ||
      saved.associations.length!==record.associations.length || record.associations.some(link=>
        !saved.associations.some(a=>a.id===link.id&&a.subnetId===link.subnetId))) invalid();
  const {id,vpc:createdVpc,mode:createdMode,name:createdName}=saved;
  const created={id,vpc:createdVpc,mode:createdMode,name:createdName};
  const rows=all.filter(item=>item.mode===mode).map(({id,vpc,mode,name})=>({id,vpc,mode,name}));
  return {created,rows};
}

export async function deleteVpcNetworkACL(request, region, vpc, acl, options={}) {
  if (acl?.vpc!==vpc?.id || acl?.mode!==mode || !isShape(acl?.id,aclId)) invalid();
  const document=await send(request,region,vpc,'DeleteNetworkAcl',{NetworkAclId:acl.id},options);
  if (text(envelope(document,'DeleteNetworkAcl',['return']).get('return'))!=='true') invalid();
  const all=await loadNetworkACLInventory(request,region,vpc.id,options);
  if (all.some(item=>item.id===acl.id)) invalid();
  const rows=all.filter(item=>item.mode===mode).map(({id,vpc,mode,name})=>({id,vpc,mode,name}));
  return {removed:acl,rows};
}

export async function loadNetworkACLDetails(request, region, vpc, id, options={}) {
  if (!validNetworkACLID(id)) invalid();
  const document=await send(request,region,vpc,'DescribeNetworkAcls',
    {'NetworkAclId.1':id,'Filter.1.Name':'vpc-id','Filter.1.Value.1':vpc.id},options);
  const rows=parseNetworkACLInventory(document,vpc.id);
  if (rows.length!==1 || rows[0].id!==id) invalid();
  return rows[0];
}

function verifySubnetSelection(vpc, subnet) {
  if (networkACLTarget(vpc)!=='ec2' || subnet?.vpc!==vpc.id ||
      subnet?.owner!=='ec2' || !isShape(subnet?.id,subnetId)) invalid();
}

export async function loadSubnetNetworkACL(request, region, vpc, subnet, options={}) {
  verifySubnetSelection(vpc,subnet);
  const document=await send(request,region,vpc,'DescribeNetworkAcls',
    {'Filter.1.Name':'association.subnet-id','Filter.1.Value.1':subnet.id},options);
  const rows=parseNetworkACLInventory(document,vpc.id);
  if (!rows.length) return null;
  if (rows.length!==1) invalid();
  const [acl]=rows, linked=acl.associations.find(a=>a.subnetId===subnet.id);
  if (!linked) invalid();
  return {acl,association:linked};
}

export async function replaceSubnetNetworkACL(request, region, vpc, subnet, prior, destination, options={}) {
  verifySubnetSelection(vpc,subnet);
  if (prior?.acl?.vpc!==vpc.id || prior?.association?.subnetId!==subnet.id ||
      !isShape(prior?.association?.id,associationId) || !validNetworkACLID(prior?.acl?.id) ||
      !validNetworkACLID(destination?.id) || destination.vpc!==vpc.id || destination.id===prior.acl.id) invalid();
  const before=await loadSubnetNetworkACL(request,region,vpc,subnet,options);
  if (before?.acl.id!==prior.acl.id || before?.association.id!==prior.association.id) invalid();
  const action='ReplaceNetworkAclAssociation';
  const reply=await send(request,region,vpc,action,
    {AssociationId:before.association.id,NetworkAclId:destination.id},options);
  const receipt=text(envelope(reply,action,['newAssociationId']).get('newAssociationId'));
  if (!isShape(receipt,associationId) || receipt===before.association.id) invalid();
  const after=await loadSubnetNetworkACL(request,region,vpc,subnet,options);
  if (after?.acl.id!==destination.id || after?.association.id!==receipt) invalid();
  return after;
}

const equalRule=(a,b)=>a===b || Boolean(a&&b&&a.number===b.number&&a.egress===b.egress&&
  a.protocol===b.protocol&&a.action===b.action&&a.cidr===b.cidr&&a.ports?.from===b.ports?.from&&a.ports?.to===b.ports?.to);

export async function changeNetworkACLRule(request, region, vpc, acl, kind, input, options={}) {
  if (!Object.hasOwn(ruleActions,kind) || networkACLTarget(vpc)!=='ec2' || acl?.vpc!==vpc.id ||
      acl?.mode!==mode || acl?.isDefault!==false || !Array.isArray(acl?.entries) ||
      !Array.isArray(acl?.associations) || acl.associations.length || !validNetworkACLID(acl?.id)) invalid();
  const value=normalizeNetworkACLRule(input);
  if (!value) invalid();
  const locate=rules=>rules.find(rule=>rule.number===value.number&&rule.egress===value.egress);
  const previous=locate(acl.entries);
  if (kind==='create' ? Boolean(previous) : !previous) invalid();
  if (kind==='delete' && !equalRule(value,previous)) invalid();

  // Refresh the exact selected ACL before attempting a write. The API has no
  // compare-and-swap, so this detects stale input but is not an atomic lock.
  const current=await loadNetworkACLDetails(request,region,vpc,acl.id,options);
  if (current.mode!==mode || !equalRule(locate(current.entries),previous) ||
      (kind==='create' && current.entries.filter(rule=>rule.egress===value.egress&&rule.number<terminalRule).length>=rulesPerDirection)) invalid();
  const params={NetworkAclId:acl.id,RuleNumber:String(value.number),Egress:String(value.egress)};
  if (kind!=='delete') {
    Object.assign(params,{Protocol:value.protocol,RuleAction:value.action,CidrBlock:value.cidr});
    if (value.ports) Object.assign(params,{'PortRange.From':String(value.ports.from),'PortRange.To':String(value.ports.to)});
  }
  const action=ruleActions[kind];
  const reply=await send(request,region,vpc,action,params,options);
  if (text(envelope(reply,action,['return']).get('return'))!=='true') invalid();
  const readback=await loadNetworkACLDetails(request,region,vpc,acl.id,options);
  if (readback.mode!==mode || !equalRule(locate(readback.entries),kind==='delete' ? undefined : value)) invalid();
  return readback;
}

export function networkACLFailure(error) {
  const value=String(error?.message??'');
  if (value==='Network ACL response exceeds the Console size limit.') return 'response-too-large';
  if (/^(401|403|UnauthorizedOperation|AccessDenied(?:Exception)?)(?=:|$)/.test(value) || value==='Sign in to continue.') return 'denied';
  if (/^(InvalidNetworkAclID\.NotFound|InvalidVpcID\.NotFound)(?=:|$)/.test(value)) return 'not-found';
  if (/^NetworkAclLimitExceeded(?=:|$)/.test(value)) return 'limit';
  return 'unconfirmed';
}
