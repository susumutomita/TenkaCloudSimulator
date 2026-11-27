import {compareIPv4RoutePrefixes,ec2IPv4Prefix,nativeIPv4RouteLimit} from './ec2-ipv4-route-prefix.mjs';

// Parse only the bounded EC2 responses. Missing state is not empty success.
const namespace='http://ec2.amazonaws.com/doc/2016-11-15/';
const invalid=()=>{throw Error('Invalid or incomplete EC2 response.');};
function nodes(node,name){
  const found=[...(node?.children??[])].filter(n=>n.localName===name);
  if(found.some(n=>n.namespaceURI!==namespace))invalid();
  return found;
}
function one(node,name,required=false){
  const found=nodes(node,name);
  if(found.length>1||(required&&found.length!==1))invalid();
  return found[0];
}
function value(node,name,required=false){
  const found=one(node,name,required);
  if(found?.children?.length)invalid();
  return found?.textContent??'';
}
function nameTag(node){
  const tags=nodes(one(node,'tagSet'),'item');
  const names=tags.filter(t=>value(t,'key')==='Name');
  if(names.length>1)invalid();
  return names.length?value(names[0],'value'):'';
}
// managedBy is the simulator extension: 'ec2' VPCs can be deleted natively,
// 'cloudformation' VPCs only with their stack; absent means unconfirmed.
function vpcRow(n){return {id:value(n,'vpcId'),name:nameTag(n),cidr:value(n,'cidrBlock'),managedBy:value(n,'simulatorManagedBy')};}
function instanceRow(n){
  const groups=nodes(one(n,'groupSet'),'item');
  const tagItems=nodes(one(n,'tagSet'),'item');
  if(groups.length>5||tagItems.length>50)invalid();
  const groupIds=groups.map(group=>value(group,'groupId',true));
  const tags=tagItems.map(tag=>({key:value(tag,'key',true),value:value(tag,'value',true)}));
  if(groupIds.some(id=>!id)||new Set(groupIds).size!==groupIds.length||
     tags.some(tag=>!tag.key)||new Set(tags.map(tag=>tag.key)).size!==tags.length)invalid();
  return {id:value(n,'instanceId'),name:tags.find(tag=>tag.key==='Name')?.value??'',
    state:value(n,'simulatorRuntimeState'),type:value(n,'instanceType'),image:value(n,'imageId'),
    vpc:value(n,'vpcId'),subnet:value(n,'subnetId'),groupIds,tags};
}
export function parseEC2Document(document,kind){
  if(kind!=='ec2'&&kind!=='vpc')invalid();
  const instance=kind==='ec2',root=document?.documentElement;
  if(root?.namespaceURI!==namespace||root.localName!==(instance?'DescribeInstancesResponse':'DescribeVpcsResponse'))invalid();
  if(value(root,'nextToken'))invalid();
  const records=instance?nodes(one(root,'reservationSet',true),'item').flatMap(r=>nodes(one(r,'instancesSet',true),'item')):nodes(one(root,'vpcSet',true),'item');
  if(records.length>1000)invalid();
  const rows=records.map(instance?instanceRow:vpcRow);
  if(rows.some(row=>!row.id)||new Set(rows.map(row=>row.id)).size!==rows.length)invalid();
  return rows;
}

export const VPC_NAME_LIMIT=256;
// One optional Name tag; the server validates the CIDR and tag bounds.
export function createVpcParameters({name='',cidr=''}){
  const params=new URLSearchParams({Action:'CreateVpc',Version:'2016-11-15',CidrBlock:cidr.trim()});
  if(name.length>VPC_NAME_LIMIT)throw Error('The Name tag can have up to 256 characters.');
  if(name)for(const [key,v] of [['ResourceType','vpc'],['Tag.1.Key','Name'],['Tag.1.Value',name]])params.set('TagSpecification.1.'+key,v);
  return params;
}
function root(document,name){
  const node=document?.documentElement;
  if(node?.namespaceURI!==namespace||node.localName!==name)invalid();
  return node;
}
// DescribeSecurityGroups is a native, regional summary. Validate its bounded
// wire shape before the client selects one VPC; a bad or partial response isn't empty.
const groupId=/^sg-[0-9a-f]{17}$/;
const vpcId=/^vpc-[0-9a-f]{17}$/;
function exactFields(node,names){
  const children=[...(node?.children??[])];
  if(children.some(n=>n.namespaceURI!==namespace||!names.includes(n.localName))||
     (!children.length&&node?.textContent?.trim())||
     [...(node?.childNodes??[])].some(n=>(n.nodeType===3||n.nodeType===4)&&n.textContent.trim()))invalid();
}
function members(node,name,max,required=true){
  const set=one(node,name,required);
  exactFields(set,['item']);
  const items=nodes(set,'item');
  if(items.length>max)invalid();
  return items;
}
function groupCIDR(text){
  const parts=/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(0|[1-9]|[12]\d|3[0-2])$/.exec(text);
  if(!parts||parts.slice(1,5).some(n=>Number(n)>255||String(Number(n))!==n))invalid();
  const address=parts.slice(1,5).reduce((n,octet)=>n*256+Number(octet),0);
  if(address % (2 ** (32-Number(parts[5]))) !== 0)invalid();
  return text;
}
function groupPort(n,name){
  const text=value(n,name,true), port=Number(text);
  if(!/^[1-9]\d{0,4}$/.test(text)||port>65535)invalid();
  return port;
}
function groupIngress(rule){
  exactFields(rule,['ipProtocol','fromPort','toPort','ipRanges']);
  const protocol=value(rule,'ipProtocol',true), from=groupPort(rule,'fromPort'), to=groupPort(rule,'toPort');
  const ranges=members(rule,'ipRanges',1);
  if(protocol!=='tcp'||from>to||ranges.length!==1)invalid();
  exactFields(ranges[0],['cidrIp']);
  return {from,to,cidr:groupCIDR(value(ranges[0],'cidrIp',true))};
}
function securityGroupRow(node){
  exactFields(node,['ownerId','groupId','groupDescription','vpcId','ipPermissions','tagSet']);
  const id=value(node,'groupId',true), vpc=value(node,'vpcId',true), description=value(node,'groupDescription',true);
  if(!groupId.test(id)||!vpcId.test(vpc)||!description||value(node,'ownerId',true)!=='000000000000')invalid();
  const ingress=members(node,'ipPermissions',60).map(groupIngress);
  const tags=members(node,'tagSet',50,false).map(tag=>{
    exactFields(tag,['key','value']);
    return {key:value(tag,'key',true),value:value(tag,'value',true)};
  });
  if(tags.some(tag=>!tag.key)||new Set(tags.map(tag=>tag.key)).size!==tags.length)invalid();
  return {id,vpc,description,ingress,tags};
}
export function parseEC2SecurityGroups(document,selectedVPC){
  if(!vpcId.test(selectedVPC))invalid();
  const response=root(document,'DescribeSecurityGroupsResponse');
  exactFields(response,['requestId','securityGroupInfo']);
  if(!value(response,'requestId',true))invalid();
  const groups=members(response,'securityGroupInfo',1000).map(securityGroupRow);
  if(new Set(groups.map(group=>group.id)).size!==groups.length)invalid();
  return groups.filter(group=>group.vpc===selectedVPC);
}
export function parseCreateVpc(document){
  const row=vpcRow(one(root(document,'CreateVpcResponse'),'vpc',true));
  if(!row.id)invalid();
  return row;
}
export function parseDeleteVpc(document){
  if(value(root(document,'DeleteVpcResponse'),'return')!=='true')invalid();
  return true;
}
// EC2 Query errors are <Response><Errors><Error><Code/><Message/>. Anything else is null.
export function parseEC2Error(document){
  const node=document?.documentElement;
  if(node?.localName!=='Response')return null;
  const child=(parent,name)=>[...(parent?.children??[])].find(n=>n.localName===name);
  const error=child(child(node,'Errors'),'Error'),code=child(error,'Code')?.textContent??'';
  return code?{code,message:child(error,'Message')?.textContent??''}:null;
}

export const routeTableInventoryLimit=1000;
export const routeTablePageSize=100;

function routePageFields(node,names) {
  if (!node||[...(node.childNodes??[])].some(n=>n.nodeType!==1&&(n.nodeType!==3||n.textContent.trim()))) invalid();
  exactFields(node,names);
}
function routePageValue(node,name,required) {
  const member=one(node,name,required);
  if (member?.children?.length||[...(member?.childNodes??[])].some(n=>n.nodeType!==3&&n.nodeType!==4)) invalid();
  return member?.textContent??'';
}
function routePageMembers(node,name,max) {
  routePageFields(one(node,name,true),['item']);
  return members(node,name,max);
}

const whole=(pattern,text)=>pattern.exec(text)?.[0]===text;
function distinct(set,id) {
  if (set.has(id)) invalid();
  set.add(id);
}

// Keep the witnessed system route and main association separate from user
// routes and explicit subnet associations. Neither has a mutation control.
export function parseEC2RouteTableWireRow(item) {
  routePageFields(item,['routeTableId','vpcId','ownerId','routeSet','associationSet','tagSet']);
  const id=routePageValue(item,'routeTableId',true),vpc=routePageValue(item,'vpcId',true);
  if (!whole(/^rtb-[0-9a-f]{17}$/,id)||!whole(vpcId,vpc)||routePageValue(item,'ownerId',true)!=='000000000000') invalid();
  let previous=null, gatewayForTable='', localRoute=null;
  const routes=[];
  for (const route of routePageMembers(item,'routeSet',nativeIPv4RouteLimit+1)) {
    routePageFields(route,['destinationCidrBlock','gatewayId','origin','state']);
    const cidr=routePageValue(route,'destinationCidrBlock',true),gateway=routePageValue(route,'gatewayId',true);
    const prefix=ec2IPv4Prefix(cidr),origin=routePageValue(route,'origin',true);
    if (!prefix||prefix.masked!==cidr||previous&&compareIPv4RoutePrefixes(previous,prefix)>=0||
        routePageValue(route,'state',true)!=='active') invalid();
    previous=prefix;
    if (gateway==='local') {
      if (localRoute||origin!=='CreateRouteTable'||prefix.bits<16||prefix.bits>28) invalid();
      localRoute={cidr,gateway};
    } else {
      if (origin!=='CreateRoute'||!whole(/^igw-[0-9a-f]{17}$/,gateway)||gatewayForTable&&gatewayForTable!==gateway||
          routes.length>=nativeIPv4RouteLimit) invalid();
      gatewayForTable=gateway;
      routes.push({cidr,gateway});
    }
  }
  let mainAssociation=null;
  const associationIds=new Set(),subnetIds=new Set(),associations=[];
  for (const association of routePageMembers(item,'associationSet',routeTableInventoryLimit)) {
    routePageFields(association,['routeTableAssociationId','routeTableId','subnetId','main','associationState']);
    const assoc=routePageValue(association,'routeTableAssociationId',true), main=routePageValue(association,'main',true);
    if (!whole(/^rtbassoc-[0-9a-f]{17}$/,assoc)||routePageValue(association,'routeTableId',true)!==id) invalid();
    distinct(associationIds,assoc);
    const state=one(association,'associationState');
    if (main==='true') {
      if (!localRoute||mainAssociation||one(association,'subnetId')) invalid();
      if (state) {
        routePageFields(state,['state']);
        if (routePageValue(state,'state',true)!=='associated') invalid();
      }
      mainAssociation={id:assoc};
    } else {
      const subnet=routePageValue(association,'subnetId',true);
      if (main!=='false'||state||!whole(/^subnet-[0-9a-f]{17}$/,subnet)) invalid();
      distinct(subnetIds,subnet);
      associations.push({id:assoc,subnet});
    }
  }
  const bytes=new TextEncoder(),keys=new Set();
  const tags=routePageMembers(item,'tagSet',50).map(tag=>{
    routePageFields(tag,['key','value']);
    const key=routePageValue(tag,'key',true),val=routePageValue(tag,'value',true);
    if (!key||/^aws:/i.test(key)||bytes.encode(key).length>128||bytes.encode(val).length>256) invalid();
    distinct(keys,key);
    return {key,value:val};
  });
  if (mainAssociation&&tags.length) invalid();
  return {id,vpc,routes,associations,tags,...(localRoute?{localRoute}:{}),...(mainAssociation?{mainAssociation}:{})};
}

// Only a complete inventory must include every main row. An exact table
// lookup or explicit-subnet filter has no obligation to include that VPC's main.
export function validateEC2RouteTableSystem(tables,complete) {
  const byVPC=new Map();
  for (const table of tables) {
    const local=table.localRoute?.cidr??'';
    const state=byVPC.get(table.vpc)??{local,main:false};
    if (local!==state.local||state.main&&table.mainAssociation) invalid();
    state.main ||= !!table.mainAssociation;
    byVPC.set(table.vpc,state);
  }
  if (complete&&[...byVPC.values()].some(state=>state.local&&!state.main)) invalid();
}

export function parseEC2RouteTablePage(document, {selectedSubnet='',paginated=false, rowLimit=routeTableInventoryLimit}={}) {
  if (document?.doctype) invalid();
  const response = root(document, 'DescribeRouteTablesResponse');
  routePageFields(response, paginated?['requestId','routeTableSet','nextToken']:['requestId','routeTableSet']);
  if (!routePageValue(response, 'requestId', true).trim()) invalid();
  const token=one(response,'nextToken')===undefined?null:routePageValue(response,'nextToken',true);
  if (token!==null&&/^[A-Za-z0-9_-]{48}$/.exec(token)?.[0]!==token) invalid();

  let count = 0;
  const tableIds = new Set(), associationIds = new Set(), subnetIds = new Set();
  const tables = routePageMembers(response, 'routeTableSet', rowLimit).map(item => {
    const table=parseEC2RouteTableWireRow(item);
    distinct(tableIds,table.id);
    if (table.mainAssociation) distinct(associationIds,table.mainAssociation.id);
    for (const association of table.associations) {
      distinct(associationIds,association.id);
      distinct(subnetIds,association.subnet);
    }
    if (selectedSubnet&&!table.associations.some(association=>association.subnet===selectedSubnet)) invalid();
    // Main/local markers project the owner/table row, not additional records.
    count+=1+table.routes.length+table.associations.length;
    if (count > routeTableInventoryLimit) invalid();
    return table;
  });
  return {tables,token};
}

// DescribeRouteTables is regional, never a per-VPC filter or a data-plane observation.
export function parseEC2RouteTables(document, selectedVPC, selectedSubnet='') {
  if (!vpcId.test(selectedVPC)) invalid();
  const {tables}=parseEC2RouteTablePage(document,{selectedSubnet});
  validateEC2RouteTableSystem(tables,!selectedSubnet);
  return tables.filter(table=>table.vpc===selectedVPC);
}
