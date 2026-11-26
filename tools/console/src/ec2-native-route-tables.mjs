import {parseEC2RouteTables} from './ec2-response.mjs';
import {loadVpcSubnets} from './ec2-native-subnets.mjs';
import {vpcRouteTablesAvailable} from './ec2-route-tables.mjs';
import {describeInternetGateways,internetGatewaysAvailable} from './ec2-internet-gateways.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const actions=['DescribeRouteTables','CreateRouteTable','DeleteRouteTable'];
const associationActions=['DescribeRouteTables','DescribeSubnets','AssociateRouteTable','DisassociateRouteTable'];
const routeActions=['DescribeRouteTables','DescribeInternetGateways','CreateRoute','DeleteRoute'];
export const ipv4DefaultRoute='0.0.0.0/0';
const vpcID=/^vpc-[0-9a-f]{17}$/;
const tableID=/^rtb-[0-9a-f]{17}$/;
const subnetID=/^subnet-[0-9a-f]{17}$/;
const associationID=/^rtbassoc-[0-9a-f]{17}$/;
const regionID=/^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$/;
const tokenShape=/^[ -~]{1,64}$/;
const wireFields=['routeTableId','vpcId','ownerId','routeSet','associationSet','tagSet'];
const bytes=new TextEncoder();
const invalid=()=>{throw Error('Invalid or incomplete detached route table response.');};
const xml=text=>new DOMParser().parseFromString(text,'application/xml');
const shape=(value,pattern)=>typeof value==='string'&&pattern.exec(value)?.[0]===value;
// A delete target is a frozen row from a validated native read in this
// transport/identity, region and VPC, not an ID copied from another view.
const rowScopes=new WeakMap(), subnetScopes=new WeakMap(), gatewayScopes=new WeakMap();
const includesAll=(actual,expected)=>Array.isArray(actual)&&expected.every(value=>actual.includes(value));
const exactActions=(actual,expected)=>actual?.length===expected.length&&includesAll(actual,expected);
const associationEnabled=ec2=>exactActions(ec2?.routeTableAssociationLifecycle,associationActions)&&includesAll(ec2?.operations,associationActions);

export function vpcNativeRouteTablesAvailable(capabilities,authenticationRequired) {
  const ec2=capabilities?.services?.ec2;
  return vpcRouteTablesAvailable(capabilities,authenticationRequired) &&
    includesAll(ec2.operations,actions) && exactActions(ec2.routeTableLifecycle,actions) &&
    (associationEnabled(ec2)||includesAll(ec2.detachedConfigurationOnly,['native route tables']));
}

export function vpcNativeRouteTableAssociationsAvailable(capabilities,authenticationRequired) {
  return vpcNativeRouteTablesAvailable(capabilities,authenticationRequired)&&associationEnabled(capabilities.services.ec2);
}

export function vpcNativeDefaultRoutesAvailable(capabilities,authenticationRequired) {
  const ec2=capabilities?.services?.ec2;
  return vpcNativeRouteTablesAvailable(capabilities,authenticationRequired)&&internetGatewaysAvailable(capabilities,authenticationRequired)&&
    exactActions(ec2.defaultRouteLifecycle,routeActions)&&includesAll(ec2.operations,routeActions);
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

async function readReply(response,signal,limit=16*1024*1024) {
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
      if (chunk.value.byteLength>limit-size) invalid();
      size+=chunk.value.byteLength;
      data+=decoder.decode(chunk.value,{stream:true});
    }
    signal?.throwIfAborted();
    return data+decoder.decode();
  } finally {
    if (!done) void reader.cancel().catch(()=>{});
    reader.releaseLock();
  }
}

async function send(request,region,vpc,action,params,{signal,parse=xml,maxBytes}) {
  signal?.throwIfAborted();
  if (!nativeRouteTableTarget(vpc) || !shape(region,regionID) || region.length>32) invalid();
  const response=await request('/',{method:'POST',credentials:'same-origin',redirect:'error',signal,
    headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},
    body:new URLSearchParams({Action:action,Version:'2016-11-15',...params})});
  signal?.throwIfAborted();
  if (!response?.ok) throw Error((response?.status??0)+': Route table request failed.');
  return parse(await readReply(response,signal,maxBytes));
}

async function snapshot(request,region,vpc,options) {
  // Describe is regional and validates both owners in one server snapshot.
  // The existing decoder rejects main, duplicate and non-subnet associations.
  const exact=options.exactID;
  // A single route-table readback cannot carry more than 50 bounded tags,
  // 1,000 bounded association elements and one route. Do not depend on the
  // size of the rest of the regional list after an acknowledged mutation.
  const doc=await send(request,region,vpc,'DescribeRouteTables',exact?{'RouteTableId.1':exact}:{},
    exact?{...options,maxBytes:512*1024}:options);
  const found=envelope(doc,'DescribeRouteTables',['routeTableSet']);
  const saved=parseEC2RouteTables(doc,vpc.id), associations=options.associations===true;
  const regionalIDs=new Set(), regionalAssociations=new Set(), regionalSubnets=new Set();
  for (const item of children(found.get('routeTableSet'))) {
    const table=fields(item,wireFields), id=leaf(table.get('routeTableId'));
    if (!shape(id,tableID)) invalid();
    regionalIDs.add(id);
    if (associations) for (const item of children(table.get('associationSet'))) {
      const pair=fields(item,['routeTableAssociationId','routeTableId','subnetId','main']);
      const assoc=leaf(pair.get('routeTableAssociationId')), subnet=leaf(pair.get('subnetId'));
      if (!shape(assoc,associationID)||!shape(subnet,subnetID)) invalid();
      regionalAssociations.add(assoc);regionalSubnets.add(subnet);
    }
  }
  if (exact&&(regionalIDs.size!==1||!regionalIDs.has(exact)||saved.length!==1)) invalid();
  const defaultRoutes=options.defaultRoutes===true;
  const binding={request,region,vpc:vpc.id,cidr:vpc.cidr,associations,defaultRoutes,regionalSubnets};
  const rows=saved.map(item=>{
    if (!defaultRoutes&&item.routes.length || !associations&&item.associations.length || item.tags.some(tag=>!validNativeRouteTableName(tag.value))) invalid();
    const row=Object.freeze({id:item.id,vpc:item.vpc,name:item.tags.find(tag=>tag.key==='Name')?.value??'',
      ...(associations?{associations:Object.freeze(item.associations.map(Object.freeze))}:{}),
      ...(defaultRoutes?{route:item.routes.length?Object.freeze(item.routes[0]):null}:{})});
    rowScopes.set(row,binding);
    return row;
  });
  return {rows,saved,regionalIDs,regionalAssociations,regionalSubnets};
}

function scopedTable(request,region,vpc,table,options) {
  const binding=rowScopes.get(table);
  if (!nativeRouteTableTarget(vpc) || !binding || binding.request!==request || binding.region!==region ||
      binding.vpc!==vpc.id || binding.cidr!==vpc.cidr || table.vpc!==vpc.id ||
      binding.associations!==(options.associations===true)||binding.defaultRoutes!==(options.defaultRoutes===true)) invalid();
  return binding;
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
  scopedTable(request,region,vpc,table,options);
  if (table.associations?.length||table.route) invalid();
  const doc=await send(request,region,vpc,'DeleteRouteTable',{RouteTableId:table.id},options);
  if (leaf(envelope(doc,'DeleteRouteTable',['return']).get('return'))!=='true') invalid();
  const {rows,regionalIDs}=await snapshot(request,region,vpc,options);
  // Absence only in the selected VPC is not enough if the ID was moved.
  if (regionalIDs.has(table.id)) invalid();
  rowScopes.delete(table);
  return {removed:table,rows};
}

// Candidates are sourced only from ordinary DescribeSubnets for the same
// native VPC, not from text entry or from a chosen service/identity override.
export async function loadVpcRouteAssociationTargets(request,region,vpc,table,options={}) {
  const binding=scopedTable(request,region,vpc,table,options);
  if (!binding.associations) invalid();
  const subnets=await loadVpcSubnets(request,region,vpc,options);
  options.signal?.throwIfAborted();
  return subnets.filter(s=>!binding.regionalSubnets.has(s.id)).map(subnet=>{
    if (subnet.owner!=='ec2'||subnet.vpc!==vpc.id||!shape(subnet.id,subnetID)) invalid();
    const target=Object.freeze(subnet);
    subnetScopes.set(target,table);
    return target;
  });
}

export async function associateVpcRouteSubnet(request,region,vpc,table,subnet,options={}) {
  const binding=scopedTable(request,region,vpc,table,options);
  if (!binding.associations||subnetScopes.get(subnet)!==table||binding.regionalSubnets.has(subnet.id)) invalid();
  const doc=await send(request,region,vpc,'AssociateRouteTable',{RouteTableId:table.id,SubnetId:subnet.id},options);
  const receipt=envelope(doc,'AssociateRouteTable',['associationId','associationState']);
  const id=leaf(receipt.get('associationId')), state=fields(receipt.get('associationState'),['state']);
  if (!shape(id,associationID)||leaf(state.get('state'))!=='associated') invalid();
  const {rows}=await snapshot(request,region,vpc,options);
  const associated=rows.find(row=>row.id===table.id)?.associations.find(row=>row.id===id&&row.subnet===subnet.id);
  if (!associated) invalid();
  return {associated,rows};
}

export async function disassociateVpcRouteSubnet(request,region,vpc,table,association,options={}) {
  const binding=scopedTable(request,region,vpc,table,options);
  if (!binding.associations||!table.associations.includes(association)) invalid();
  const doc=await send(request,region,vpc,'DisassociateRouteTable',{AssociationId:association.id},options);
  if (leaf(envelope(doc,'DisassociateRouteTable',['return']).get('return'))!=='true') invalid();
  const {rows,regionalAssociations,regionalSubnets}=await snapshot(request,region,vpc,options);
  // A moved ID or a different binding on the same subnet is not absence.
  if (!rows.some(row=>row.id===table.id)||regionalAssociations.has(association.id)||regionalSubnets.has(association.subnet)) invalid();
  return {disassociated:association,rows};
}

// Only a verified native gateway currently linked to this VPC is offered.
export async function loadVpcNativeRouteGateway(request,region,vpc,table,options={}) {
  const binding=scopedTable(request,region,vpc,table,options);
  if (!binding.defaultRoutes||table.route) invalid();
  const rows=await describeInternetGateways(request,region,options.signal);
  options.signal?.throwIfAborted();
  const gateway=rows.find(row=>row.vpc===vpc.id);
  if (!gateway) return null;
  if (gateway.managedBy!=='ec2') invalid();
  gatewayScopes.set(gateway,table);
  return gateway;
}

export async function changeVpcNativeDefaultRoute(request,region,vpc,table,target,create,options={}) {
  const binding=scopedTable(request,region,vpc,table,options);
  if (!binding.defaultRoutes || (create ? table.route||gatewayScopes.get(target)!==table : !table.route||table.route!==target)) invalid();
  const action=create?'CreateRoute':'DeleteRoute';
  const params={RouteTableId:table.id,DestinationCidrBlock:ipv4DefaultRoute,...(create?{GatewayId:target.id}:{})};
  const doc=await send(request,region,vpc,action,params,options);
  if (leaf(envelope(doc,action,['return']).get('return'))!=='true') invalid();
  const {rows,saved}=await snapshot(request,region,vpc,{...options,exactID:table.id});
  const next=rows[0];
  // Absence is confirmed only inside the same still-existing table. The
  // untouched association projection must agree with the previous scoped
  // view before we can keep its regional selectors and other table rows.
  if ((create?next.route?.gateway!==target.id||next.route.cidr!==ipv4DefaultRoute:next.route!==null) ||
      next.name!==table.name||JSON.stringify(next.associations)!==JSON.stringify(table.associations)||
      saved[0].id!==table.id) invalid();
  options.signal?.throwIfAborted();
  rowScopes.set(next,binding);
  rowScopes.delete(table);
  return next;
}

export function nativeRouteTableFailure(error) {
  const message=error?.message??'';
  if (/^(401|403|UnauthorizedOperation|AccessDenied(?:Exception)?)(?=:|$)/.test(message) || message==='Sign in to continue.') return 'denied';
  if (/^Invalid(?:Route|RouteTableID|SubnetID|AssociationID|VpcID)\.NotFound(?=:|$)/.test(message)) return 'not-found';
  if (/^RouteAlreadyExists(?=:|$)/.test(message)) return 'route-exists';
  if (/^(Gateway\.NotAttached|InvalidInternetGatewayID\.NotFound)(?=:|$)/.test(message)) return 'no-gateway';
  if (/^DependencyViolation(?=:|$)/.test(message)) return 'dependency';
  if (/^Resource\.AlreadyAssociated(?=:|$)/.test(message)) return 'associated';
  if (/^(RouteLimitExceeded|RouteTableLimitExceeded|ResourceLimitExceeded)(?=:|$)/.test(message)) return 'limit';
  return 'unconfirmed';
}
