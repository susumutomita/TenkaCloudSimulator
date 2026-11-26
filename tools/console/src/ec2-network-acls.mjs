// The native wire exposes only detached, non-default metadata. A wrong or
// incomplete reply is unavailable, not an empty list or a successful write.
const namespace = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const mode = 'detached-config-only';
const vpcId = /^vpc-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const aclId = /^acl-(?:[0-9a-f]{8}|[0-9a-f]{17})$/;
const tokenShape = /^[ -~]{1,64}$/;
const operations = ['DescribeNetworkAcls','CreateNetworkAcl','DeleteNetworkAcl'];
const isShape = (value, re) => typeof value === 'string' && re.exec(value)?.[0] === value;
const invalid = () => {throw Error('Invalid or incomplete detached network ACL response.');};
const bytes = new TextEncoder();
const xml = text => new DOMParser().parseFromString(text,'application/xml');

export function vpcNetworkACLsAvailable(capabilities, authenticationRequired) {
  const ec2 = capabilities?.services?.ec2;
  const selectors = ec2?.describeSelectors?.DescribeNetworkAcls;
  return authenticationRequired === true && !capabilities?.consoleScope && ec2?.console === true &&
    Array.isArray(ec2.operations) && operations.every(action=>ec2.operations.includes(action)) &&
    Array.isArray(ec2.detachedConfigurationOnly) && ec2.detachedConfigurationOnly.includes('native network ACLs') &&
    Array.isArray(selectors) && selectors.includes('vpc-id');
}

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

function row(node, selected) {
  const cols=fields(node,['networkAclId','vpcId','ownerId','default','entrySet','associationSet','tagSet','simulatorNetworkAclMode']);
  const id=text(cols.get('networkAclId')), vpc=text(cols.get('vpcId'));
  if (!isShape(id,aclId) || vpc!==selected || text(cols.get('ownerId'))!=='000000000000' ||
      text(cols.get('default'))!=='false' || text(cols.get('simulatorNetworkAclMode'))!==mode ||
      children(cols.get('associationSet')).length!==0) invalid();
  const directions=items(cols.get('entrySet'),2).map(entry=>{
    const f=fields(entry,['ruleNumber','protocol','ruleAction','egress','cidrBlock']);
    if (text(f.get('ruleNumber'))!=='32767' || text(f.get('protocol'))!=='-1' ||
        text(f.get('ruleAction'))!=='deny' || text(f.get('cidrBlock'))!=='0.0.0.0/0') invalid();
    return text(f.get('egress'));
  });
  if (!directions.includes('true') || !directions.includes('false')) invalid();
  const tags=new Map();
  for (const tag of cols.has('tagSet') ? items(cols.get('tagSet'),50) : []) {
    const f=fields(tag,['key','value']);
    const key=text(f.get('key')), value=text(f.get('value'));
    if (!key || tags.has(key) || bytes.encode(key).length > 128 || /^aws:/i.test(key) ||
        !validNetworkACLName(key) || !validNetworkACLName(value)) invalid();
    tags.set(key,value);
  }
  // Terminal entries are compatibility metadata only and are not editable or
  // presented as in-use firewall rules. The native rows never apply to traffic.
  return {id,vpc,mode,name:tags.get('Name')??''};
}

export function parseVpcNetworkACLs(document, selected) {
  if (!isShape(selected,vpcId)) invalid();
  const members=envelope(document,'DescribeNetworkAcls',['networkAclSet']);
  const rows=items(members.get('networkAclSet'),1000).map(item=>row(item,selected));
  if (new Set(rows.map(item=>item.id)).size!==rows.length) invalid();
  return rows;
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
      if (size > 16*1024*1024) invalid();
      data+=decoder.decode(part.value,{stream:true});
    }
    signal?.throwIfAborted();
    return data+decoder.decode();
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function send(request, region, vpc, action, params, {signal,parse=xml}) {
  signal?.throwIfAborted();
  if (networkACLTarget(vpc)!=='ec2' || !isShape(region,/^[a-z]{2}(?:-[a-z0-9]+)+-\d+$/) || region.length > 32) invalid();
  const response=await request('/',{method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',...params})});
  signal?.throwIfAborted();
  if (!response?.ok) throw Error((response?.status??0)+': Network ACL request failed.');
  return parse(await readReply(response,signal));
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
  const created=row(members.get('networkAcl'),vpc.id);
  if (created.name!==name || text(members.get('clientToken'))!==clientToken) invalid();
  const rows=await loadVpcNetworkACLs(request,region,vpc,options);
  if (!rows.some(item=>item.id===created.id&&item.name===name)) invalid();
  return {created,rows};
}

export async function deleteVpcNetworkACL(request, region, vpc, acl, options={}) {
  if (acl?.vpc!==vpc?.id || acl?.mode!==mode || !isShape(acl?.id,aclId)) invalid();
  const document=await send(request,region,vpc,'DeleteNetworkAcl',{NetworkAclId:acl.id},options);
  if (text(envelope(document,'DeleteNetworkAcl',['return']).get('return'))!=='true') invalid();
  const rows=await loadVpcNetworkACLs(request,region,vpc,options);
  if (rows.some(item=>item.id===acl.id)) invalid();
  return {removed:acl,rows};
}

export function networkACLFailure(error) {
  const value=String(error?.message??'');
  if (/^(401|403|UnauthorizedOperation|AccessDenied(?:Exception)?)(?=:|$)/.test(value) || value==='Sign in to continue.') return 'denied';
  if (/^(InvalidNetworkAclID\.NotFound|InvalidVpcID\.NotFound)(?=:|$)/.test(value)) return 'not-found';
  if (/^NetworkAclLimitExceeded(?=:|$)/.test(value)) return 'limit';
  return 'unconfirmed';
}
