import {parseEC2Error} from './ec2-response.mjs';
import {ec2IPv4Prefix} from './ec2-ipv4-route-prefix.mjs';
import {vpcSecurityGroupsAvailable} from './ec2-security-groups.mjs';

const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const nativeVPC = /^vpc-[0-9a-f]{17}$/;
const groupID = /^sg-[0-9a-f]{17}$/;
const regionID = /^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$/;
const actions = ['CreateSecurityGroup','DescribeSecurityGroups','DeleteSecurityGroup'];
const ruleActions = Object.freeze({
  ingress:Object.freeze({add:'AuthorizeSecurityGroupIngress',remove:'RevokeSecurityGroupIngress'}),
  egress:Object.freeze({add:'AuthorizeSecurityGroupEgress',remove:'RevokeSecurityGroupEgress'}),
});
const ruleOperations = Object.values(ruleActions).flatMap(Object.values);
const ruleRows = new WeakMap();
const ruleLimit = 60;
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

export function vpcNativeSecurityGroupRulesAvailable(capabilities, authenticationRequired) {
  const ec2=capabilities?.services?.ec2;
  return vpcNativeSecurityGroupsAvailable(capabilities,authenticationRequired)
    && ec2.detachedSecurityGroupRuleProfile==='tcp-ipv4-same-vpc'
    && ec2.detachedSecurityGroupRulesPerDirection===ruleLimit
    && ruleOperations.every(action=>ec2.operations.includes(action));
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

const compareRules=(a,b)=>a.from-b.from||a.to-b.to||(a.cidr<b.cidr?-1:a.cidr>b.cidr?1:0);
export function nativeSGRule(rule,vpc) {
  const parent=ec2IPv4Prefix(vpc?.cidr), prefix=ec2IPv4Prefix(rule?.cidr);
  function port(raw) {
    const t=String(raw??'');
    return /^[1-9][0-9]{0,4}$/.test(t)&&Number(t)<=65535?Number(t):0;
  }
  const from=port(rule?.from),to=port(rule?.to);
  if (!nativeSGTarget(vpc)||!parent||parent.masked!==vpc.cidr||parent.bits<16||parent.bits>28
      ||!prefix||prefix.masked!==rule.cidr||prefix.start<parent.start||prefix.end>parent.end
      ||!from||to<from) return null;
  return {from,to,cidr:rule.cidr};
}

function readRules(node,vpc) {
  const result=members(node,ruleLimit).map(item=>{
    const rule=fields(item,['ipProtocol','fromPort','toPort','ipRanges']);
    const ranges=members(rule.get('ipRanges'),1);
    if (leaf(rule,'ipProtocol')!=='tcp'||ranges.length!==1) invalid();
    const r=nativeSGRule({from:leaf(rule,'fromPort'),to:leaf(rule,'toPort'),cidr:leaf(fields(ranges[0],['cidrIp']),'cidrIp')},vpc);
    if (!r) invalid();
    return Object.freeze(r);
  });
  if (result.some((r,i)=>i>0&&compareRules(result[i-1],r)>=0)) invalid();
  return Object.freeze(result);
}

export function parseVpcNativeSecurityGroups(document,vpc,{rulesEnabled=false}={}) {
  if (!nativeSGTarget(vpc)) invalid();
  const envelopeFields = envelope(document,'DescribeSecurityGroups',['securityGroupInfo']);
  const ids = new Set(), names = new Set();
  return members(envelopeFields.get('securityGroupInfo'),1000).map(item=>{
    const row = fields(item,['ownerId','groupId','groupName','groupDescription','vpcId','ipPermissions','tagSet',
      'simulatorSecurityGroupMode',...(rulesEnabled?['ipPermissionsEgress']:[])]);
    const id = leaf(row,'groupId'), name = leaf(row,'groupName'), description = leaf(row,'groupDescription');
    const lower = name.toLowerCase();
    if (!groupID.test(id) || ids.has(id) || !name || nativeSGName(name)!==name || names.has(lower) || !validNativeSGDescription(description)
        || leaf(row,'vpcId')!==vpc.id || leaf(row,'ownerId')!=='000000000000' || leaf(row,'simulatorSecurityGroupMode')!==mode) invalid();
    ids.add(id);names.add(lower);
    const group={id,name,description,vpc:vpc.id,mode,tags:tags(row)};
    if (!rulesEnabled) {
      members(row.get('ipPermissions'),0);
      return group;
    }
    return Object.freeze({...group,tags:Object.freeze(group.tags.map(Object.freeze)),ingress:readRules(row.get('ipPermissions'),vpc),
      egress:row.has('ipPermissionsEgress')?readRules(row.get('ipPermissionsEgress'),vpc):Object.freeze([])});
  });
}

async function send(request,region,action,params,opts) {
  if (typeof region!=='string' || region.length>32 || !regionID.test(region) || !(actions.includes(action)||ruleOperations.includes(action))) invalid();
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
  const rows=parseVpcNativeSecurityGroups(document,vpc,opts);
  if (opts.rulesEnabled===true) for (const row of rows) ruleRows.set(row,{request,region,id:vpc.id,cidr:vpc.cidr});
  return rows;
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

export async function changeVpcNativeSecurityGroupRule(request,region,vpc,group,direction,draft,add,opts={}) {
  const scoped=ruleRows.get(group);
  const r=nativeSGRule(draft,vpc);
  if ((direction!=='ingress'&&direction!=='egress')||typeof add!=='boolean'||opts.rulesEnabled!==true
      ||!scoped||scoped.request!==request||scoped.region!==region||scoped.id!==vpc.id||scoped.cidr!==vpc.cidr||!r) invalid();
  const original=group[direction], exists=original.some(saved=>compareRules(saved,r)===0);
  if (exists===add||(add&&original.length>=ruleLimit)) invalid();
  const action=ruleActions[direction][add?'add':'remove'];
  const doc=await send(request,region,action,{GroupId:group.id,'IpPermissions.1.IpProtocol':'tcp',
    'IpPermissions.1.FromPort':r.from,'IpPermissions.1.ToPort':r.to,'IpPermissions.1.IpRanges.1.CidrIp':r.cidr},opts);
  if (leaf(envelope(doc,action,['return']),'return')!=='true') invalid();
  const rows=await loadVpcNativeSecurityGroups(request,region,vpc,opts);
  const checked=rows.find(item=>item.id===group.id);
  const expected=add?[...original,r].sort(compareRules):original.filter(saved=>compareRules(saved,r)!==0);
  const peer=direction==='ingress'?'egress':'ingress';
  const equal=(a,b)=>a.length===b.length&&a.every((saved,i)=>compareRules(saved,b[i])===0);
  if (!checked||checked.name!==group.name||checked.description!==group.description
      ||!equal(checked[direction],expected)||!equal(checked[peer],group[peer])) invalid();
  return {changed:checked,rows};
}

export function nativeSGFailure(error) {
  const message = error?.message ?? '';
  if (/^(401:|403:|UnauthorizedOperation:|Sign in to continue\.)/.test(message)) return 'denied';
  if (message.startsWith('InvalidGroup.Duplicate:')) return 'duplicate';
  if (message.startsWith('InvalidPermission.Duplicate:')) return 'duplicateRule';
  if (message.startsWith('InvalidPermission.NotFound:')) return 'missingRule';
  if (message.startsWith('RulesPerSecurityGroupLimitExceeded:')) return 'ruleLimit';
  if (message.startsWith('DependencyViolation:')) return 'dependency';
  return 'unconfirmed';
}
