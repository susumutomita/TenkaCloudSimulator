import {parseEC2RouteTables} from './ec2-response.mjs';
import {vpcRouteTablesAvailable} from './ec2-route-tables.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const actions=['DescribeRouteTables','CreateRouteTable','DeleteRouteTable'];
const vpcID=/^vpc-[0-9a-f]{17}$/;
const tableID=/^rtb-[0-9a-f]{17}$/;
const regionID=/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/;
const tokenShape=/^[ -~]{1,64}$/;
const wireFields=['routeTableId','vpcId','ownerId','routeSet','associationSet','tagSet'];
const bytes=new TextEncoder();
const invalid=()=>{throw Error('Invalid or incomplete detached route table response.');};
const xml=text=>new DOMParser().parseFromString(text,'application/xml');
const shape=(value,pattern)=>typeof value==='string'&&pattern.exec(value)?.[0]===value;
// A delete target is a frozen row from a validated native read in this
// transport/identity, region and VPC, not an ID copied from another view.
const rowScopes=new WeakMap();

export function vpcNativeRouteTablesAvailable(capabilities,authenticationRequired) {
  const ec2=capabilities?.services?.ec2;
  return vpcRouteTablesAvailable(capabilities,authenticationRequired) &&
    Array.isArray(ec2.operations) && actions.every(action=>ec2.operations.includes(action)) &&
    Array.isArray(ec2.routeTableLifecycle) && ec2.routeTableLifecycle.length===actions.length &&
    actions.every(action=>ec2.routeTableLifecycle.includes(action)) &&
    Array.isArray(ec2.detachedConfigurationOnly) && ec2.detachedConfigurationOnly.includes('native route tables');
}

export function nativeRouteTableTarget(vpc) {
  return vpc?.managedBy==='ec2'&&shape(vpc.id,vpcID);
}

export function validNativeRouteTableName(name) {
  return typeof name==='string' && bytes.encode(name).length<=256 &&
    !/[\u0000-\u0008\u000b\u000c\u000e-\u001f\ud800-\udfff\ufffe\uffff]/u.test(name);
}

function children(node) {
  if (!node || [...(node.childNodes??[])].some(n=>n.nodeType!==1&&(n.nodeType!==3||n.textContent.trim()))) invalid();
  const elements=[...node.children];
  if (elements.some(n=>n.namespaceURI!==ns)) invalid();
  return elements;
}
function fields(node,allowed) {
  const result=new Map();
  for (const member of children(node)) {
    if (!allowed.includes(member.localName) || result.has(member.localName)) invalid();
    result.set(member.localName,member);
  }
  return result;
}
function leaf(node) {
  if (!node || node.children.length || [...(node.childNodes??[])].some(n=>n.nodeType!==3&&n.nodeType!==4)) invalid();
  return node.textContent;
}
function envelope(document,action,members) {
  const root=document?.documentElement;
  if (document?.doctype || root?.namespaceURI!==ns || root.localName!==action+'Response') invalid();
  const result=fields(root,['requestId',...members]);
  if (!leaf(result.get('requestId')).trim()) invalid();
  return result;
}

async function readReply(response,signal) {
  const type=response.headers?.get('Content-Type')?.split(';')[0].trim().toLowerCase();
  const reader=response.body?.getReader();
  if (!reader) invalid();
  const decoder=new TextDecoder('utf-8',{fatal:true});
  let data='',size=0,done=false;
  try {
    if (type!=='text/xml' && type!=='application/xml') invalid();
    for (;;) {
      signal?.throwIfAborted();
      const chunk=await reader.read();
      if (chunk.done) {done=true;break;}
      size+=chunk.value.byteLength;
      if (size>16*1024*1024) invalid();
      data+=decoder.decode(chunk.value,{stream:true});
    }
    signal?.throwIfAborted();
    return data+decoder.decode();
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function send(request,region,vpc,action,params,{signal,parse=xml}) {
  signal?.throwIfAborted();
  if (!nativeRouteTableTarget(vpc) || !shape(region,regionID) || region.length>32) invalid();
  const response=await request('/',{method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',...params})});
  signal?.throwIfAborted();
  if (!response?.ok) throw Error((response?.status??0)+': Route table request failed.');
  return parse(await readReply(response,signal));
}

async function snapshot(request,region,vpc,options) {
  // Retain unfiltered regional Describe and the original CFN decoder. The
  // server has verified typed ownership; the selected native VPC must have
  // only detached records. A damaged row elsewhere invalidates the snapshot.
  const doc=await send(request,region,vpc,'DescribeRouteTables',{},options);
  const found=envelope(doc,'DescribeRouteTables',['routeTableSet']);
  const saved=parseEC2RouteTables(doc,vpc.id);
  const regionalIDs=new Set(children(found.get('routeTableSet')).map(item=>{
    const id=leaf(fields(item,wireFields).get('routeTableId'));
    if (!shape(id,tableID)) invalid();
    return id;
  }));
  const rows=saved.map(item=>{
    if (item.routes.length || item.associations.length || item.tags.some(tag=>!validNativeRouteTableName(tag.value))) invalid();
    const row=Object.freeze({id:item.id,vpc:item.vpc,name:item.tags.find(tag=>tag.key==='Name')?.value??''});
    rowScopes.set(row,{request,region,id:vpc.id});
    return row;
  });
  return {rows,saved,regionalIDs};
}

export async function loadVpcNativeRouteTables(request,region,vpc,options={}) {
  return (await snapshot(request,region,vpc,options)).rows;
}

export async function createVpcNativeRouteTable(request,region,vpc,name,clientToken,options={}) {
  if (!validNativeRouteTableName(name) || !shape(clientToken,tokenShape)) invalid();
  const tags=name ? {'TagSpecification.1.ResourceType':'route-table','TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name} : {};
  const doc=await send(request,region,vpc,'CreateRouteTable',{VpcId:vpc?.id,ClientToken:clientToken,...tags},options);
  const receipt=envelope(doc,'CreateRouteTable',['routeTable','clientToken']);
  const table=fields(receipt.get('routeTable'),wireFields), id=leaf(table.get('routeTableId'));
  if (!shape(id,tableID) || leaf(table.get('vpcId'))!==vpc.id || leaf(table.get('ownerId'))!=='000000000000' ||
      leaf(receipt.get('clientToken'))!==clientToken || children(table.get('routeSet')).length ||
      children(table.get('associationSet')).length) invalid();
  const got=children(table.get('tagSet'));
  if (got.length!==(name?1:0)) invalid();
  if (name) {
    if (got[0].localName!=='item') invalid();
    const tag=fields(got[0],['key','value']);
    if (leaf(tag.get('key'))!=='Name' || leaf(tag.get('value'))!==name) invalid();
  }
  const {rows,saved}=await snapshot(request,region,vpc,options);
  const created=rows.find(row=>row.id===id&&row.name===name);
  if (!created || saved.find(row=>row.id===id).tags.length!==got.length) invalid();
  return {created,rows};
}

export async function deleteVpcNativeRouteTable(request,region,vpc,table,options={}) {
  const binding=rowScopes.get(table);
  if (!nativeRouteTableTarget(vpc) || !binding || binding.request!==request || binding.region!==region ||
      binding.id!==vpc.id || table.vpc!==vpc.id) invalid();
  const doc=await send(request,region,vpc,'DeleteRouteTable',{RouteTableId:table.id},options);
  if (leaf(envelope(doc,'DeleteRouteTable',['return']).get('return'))!=='true') invalid();
  const {rows,regionalIDs}=await snapshot(request,region,vpc,options);
  // Absence only in the selected VPC is not enough if the ID was moved.
  if (regionalIDs.has(table.id)) invalid();
  rowScopes.delete(table);
  return {removed:table,rows};
}

export function nativeRouteTableFailure(error) {
  const message=error?.message??'';
  if (/^(401|403|UnauthorizedOperation|AccessDenied(?:Exception)?)(?=:|$)/.test(message) || message==='Sign in to continue.') return 'denied';
  if (/^(InvalidRouteTableID\.NotFound|InvalidVpcID\.NotFound)(?=:|$)/.test(message)) return 'not-found';
  if (/^DependencyViolation(?=:|$)/.test(message)) return 'dependency';
  if (/^RouteTableLimitExceeded(?=:|$)/.test(message)) return 'limit';
  return 'unconfirmed';
}
