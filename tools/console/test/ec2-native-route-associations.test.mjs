import assert from 'node:assert/strict';
import test from 'node:test';
import {associateVpcRouteSubnet,deleteVpcNativeRouteTable,disassociateVpcRouteSubnet,loadVpcNativeRouteTables,
  loadVpcRouteAssociationTargets,nativeRouteTableFailure,vpcNativeRouteTableAssociationsAvailable,
  vpcNativeRouteTablesAvailable} from '../src/ec2-native-route-tables.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const region='us-east-1', id=(prefix,n)=>prefix+'-'+n.toString(16).padStart(17,'0');
const vpc={id:id('vpc',1),managedBy:'ec2',cidr:'10.44.0.0/16'};
const other={id:id('vpc',2),managedBy:'ec2',cidr:'10.45.0.0/16'};
const stack={id:id('vpc',3),managedBy:'cloudformation',cidr:'10.46.0.0/16'};
const table=id('rtb',1), peerTable=id('rtb',2), otherTable=id('rtb',3), stackTable=id('rtb',4);
const target=id('subnet',1), occupied=id('subnet',2), foreign=id('subnet',3), legacy=id('subnet',4);
const assigned=id('rtbassoc',1), occupiedAssociation=id('rtbassoc',2), legacyAssociation=id('rtbassoc',3);
const options={associations:true};
const failure=/Invalid or incomplete (?:detached route table response|EC2 response)|Invalid subnet reply/;

// Like the neighboring route tests, use only the parser's Document shape;
// no browser/network is required for fast, deterministic admission tests.
const node=(localName,data=[])=>{
  const children=Array.isArray(data)?data:[];
  const textContent=typeof data==='string'?data:children.map(n=>n.textContent).join('');
  return {localName,namespaceURI:ns,children,childNodes:children.length?children:textContent?[{nodeType:3,textContent}]:[],textContent,nodeType:1};
};
const fields=values=>Object.entries(values).map(([key,data])=>node(key,data));
const doc=(action,values)=>({documentElement:node(action+'Response',fields({requestId:'local-request',...values}))});
const assoc=(routeTableId,subnetId,routeTableAssociationId,extra={})=>node('item',fields({
  routeTableId,subnetId,routeTableAssociationId,main:'false',...extra,
}));
const tableRow=(routeTableId,vpcId,associations=[],routes=[])=>node('item',fields({
  routeTableId,vpcId,ownerId:'000000000000',tagSet:[],routeSet:routes,associationSet:associations,
}));
const subnetRow=(subnetId,vpcId,cidrBlock,native=true)=>node('item',fields({
  subnetId,vpcId,cidrBlock,state:'available',...(native?{simulatorManagedBy:'ec2',availabilityZone:region+'a'}:{}),
}));
const mixed=({current=[],peers=[],foreign=[],currentRoutes=[]}={})=>doc('DescribeRouteTables',{routeTableSet:[
  tableRow(table,vpc.id,current,currentRoutes),tableRow(peerTable,vpc.id,peers),tableRow(otherTable,other.id,foreign),
  tableRow(stackTable,stack.id,[assoc(stackTable,legacy,legacyAssociation)]),
]});
const onPeer=assoc(peerTable,occupied,occupiedAssociation);
const onTable=assoc(table,target,assigned);
const scopedSubnets=doc('DescribeSubnets',{subnetSet:[
  subnetRow(target,vpc.id,'10.44.0.0/24'),subnetRow(occupied,vpc.id,'10.44.1.0/24'),
  subnetRow(foreign,other.id,'10.45.0.0/24'),subnetRow(legacy,stack.id,'10.46.0.0/24',false),
]});
const receipt=(associationId=assigned,state='associated')=>doc('AssociateRouteTable',{associationId,associationState:fields({state})});
const removal=(ok='true')=>doc('DisassociateRouteTable',{return:ok});
const route=node('item',fields({destinationCidrBlock:'0.0.0.0/0',gatewayId:id('igw',7),origin:'CreateRoute',state:'active'}));

function fixture(replies) {
  const calls=[];
  async function request(...args) {
    assert.equal(args.length,2,'never override the effective identity');
    const [path,init]=args;
    assert.equal(path,'/');assert.equal(init.method,'POST');assert.equal(init.credentials,'same-origin');assert.equal(init.redirect,'error');
    assert.deepEqual(init.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region});
    const index=calls.length;
    calls.push({body:Object.fromEntries(init.body),signal:init.signal});
    assert.ok(index<replies.length,'unexpected mutation, retry or read');
    const entry=replies[index];
    return typeof entry==='function'?entry(init):new Response(String(index),{headers:{'Content-Type':'text/xml'}});
  }
  return {request,calls,config:{...options,parse:data=>replies[Number(data)]}};
}

test('associations need the exact authenticated capability; detached-only compatibility stays distinct',()=>{
  const routeTableLifecycle=['DescribeRouteTables','CreateRouteTable','DeleteRouteTable'];
  const routeTableAssociationLifecycle=['DescribeRouteTables','DescribeSubnets','AssociateRouteTable','DisassociateRouteTable'];
  const ec2={console:true,routeTableReadback:'DescribeRouteTables',routeTableLifecycle,routeTableAssociationLifecycle,
    operations:[...new Set([...routeTableLifecycle,...routeTableAssociationLifecycle])]};
  const capabilities=(state=ec2,extra={})=>({services:{ec2:state},...extra});
  assert.equal(vpcNativeRouteTablesAvailable(capabilities(),true),true);
  assert.equal(vpcNativeRouteTableAssociationsAvailable(capabilities(),true),true);
  assert.equal(vpcNativeRouteTableAssociationsAvailable(capabilities(),false),false);
  assert.equal(vpcNativeRouteTableAssociationsAvailable(capabilities(ec2,{consoleScope:{instanceId:'i-local'}}),true),false);
  for(const change of [{routeTableAssociationLifecycle:[]},{routeTableAssociationLifecycle:routeTableAssociationLifecycle.join(',')},
    {routeTableAssociationLifecycle:[...routeTableAssociationLifecycle,'ReplaceRouteTableAssociation']},
    {routeTableAssociationLifecycle:['DescribeRouteTables','DescribeSubnets','AssociateRouteTable','AssociateRouteTable']},
    {operations:ec2.operations.filter(a=>a!=='DescribeSubnets')},{routeTableLifecycle:[]},{console:false}])
    assert.equal(vpcNativeRouteTableAssociationsAvailable(capabilities({...ec2,...change}),true),false);
  const old={...ec2,operations:routeTableLifecycle,routeTableAssociationLifecycle:undefined,detachedConfigurationOnly:['native route tables']};
  assert.equal(vpcNativeRouteTablesAvailable(capabilities(old),true),true);
  assert.equal(vpcNativeRouteTableAssociationsAvailable(capabilities(old),true),false);
});

test('only exact native table snapshots and unassociated same-VPC native subnet rows can start a write',async()=>{
  const f=fixture([mixed({peers:[onPeer]}),scopedSubnets,receipt(),mixed({current:[onTable],peers:[onPeer]})]);
  const rows=await loadVpcNativeRouteTables(f.request,region,vpc,f.config);
  assert.deepEqual(rows.map(row=>[row.id,row.associations.length]),[[table,0],[peerTable,1]]);
  const [selected,peer]=rows;
  assert.ok(Object.isFrozen(peer.associations)&&Object.isFrozen(peer.associations[0]));
  const selections=await loadVpcRouteAssociationTargets(f.request,region,vpc,selected,f.config);
  assert.deepEqual(selections.map(row=>row.id),[target]); // foreign, owned by a stack and already bound are not choices
  assert.ok(Object.isFrozen(selections[0]));
  const [subnet]=selections;
  for(const [req,loc,scope,t,s,flags] of [
    [f.request,region,vpc,{...selected},subnet,f.config],
    [f.request,region,vpc,selected,{...subnet},f.config],
    [f.request,region,vpc,peer,subnet,f.config],
    [f.request,'us-west-2',vpc,selected,subnet,f.config],
    [f.request,region,{...vpc,cidr:'10.66.0.0/16'},selected,subnet,f.config],
    [f.request,region,other,selected,subnet,f.config],
    [f.request,region,stack,selected,subnet,f.config],
    [()=>assert.fail('new identity must not replay a stale selection'),region,vpc,selected,subnet,f.config],
    [f.request,region,vpc,selected,subnet,{...f.config,associations:false}],
  ]) await assert.rejects(associateVpcRouteSubnet(req,loc,scope,t,s,flags),failure);
  await assert.rejects(deleteVpcNativeRouteTable(f.request,region,vpc,peer,f.config),failure);
  assert.equal(f.calls.length,2);
  const result=await associateVpcRouteSubnet(f.request,region,vpc,selected,subnet,f.config);
  assert.deepEqual(result.associated,{id:assigned,subnet:target});
  assert.equal(result.rows[0].associations[0],result.associated);
  assert.deepEqual(f.calls.map(call=>call.body),[
    {Action:'DescribeRouteTables',Version:'2016-11-15'},
    {Action:'DescribeSubnets',Version:'2016-11-15'},
    {Action:'AssociateRouteTable',Version:'2016-11-15',RouteTableId:table,SubnetId:target},
    {Action:'DescribeRouteTables',Version:'2016-11-15'},
  ]);
});

test('association acknowledgment requires the exact saved ID, table and subnet after a fresh regional read',async()=>{
  for(const [ack,read] of [
    [receipt('rtbassoc-broken'),mixed({current:[onTable]})],
    [receipt(assigned,'associating'),mixed({current:[onTable]})],
    [receipt(),mixed()],
    [receipt(),mixed({current:[assoc(table,target,id('rtbassoc',5))]})],
    [receipt(),mixed({current:[assoc(table,occupied,assigned)]})],
    [receipt(),mixed({peers:[assoc(peerTable,target,assigned)]})],
    [receipt(),mixed({foreign:[assoc(otherTable,foreign,assigned)]})],
    [receipt(),mixed({current:[assoc(table,target,assigned,{main:'true'})]})],
    [receipt(),mixed({current:[onTable],currentRoutes:[route]})],
    [receipt(),mixed({current:[onTable],peers:[assoc(peerTable,target,id('rtbassoc',5))]})],
  ]) {
    const f=fixture([mixed(),scopedSubnets,ack,read]);
    const [t]=await loadVpcNativeRouteTables(f.request,region,vpc,f.config);
    const [s]=await loadVpcRouteAssociationTargets(f.request,region,vpc,t,f.config);
    await assert.rejects(associateVpcRouteSubnet(f.request,region,vpc,t,s,f.config),failure);
    assert.equal(f.calls.filter(x=>x.body.Action==='AssociateRouteTable').length,1);
  }
  const invalid=fixture([mixed(),doc('DescribeSubnets',{subnetSet:[subnetRow(legacy,vpc.id,'10.44.0.0/24',false)]})]);
  const [t]=await loadVpcNativeRouteTables(invalid.request,region,vpc,invalid.config);
  await assert.rejects(loadVpcRouteAssociationTargets(invalid.request,region,vpc,t,invalid.config),failure);
});

test('disassociation requires a bound saved association and verifies regional absence, not a moved relationship',async()=>{
  const f=fixture([mixed({current:[onTable],peers:[onPeer]}),removal(),mixed({peers:[onPeer]})]);
  const [t,peer]=await loadVpcNativeRouteTables(f.request,region,vpc,f.config);
  const [association]=t.associations;
  for (const [row,selection] of [[t,{...association}],[t,peer.associations[0]],[peer,association]])
    await assert.rejects(disassociateVpcRouteSubnet(f.request,region,vpc,row,selection,f.config),failure);
  const result=await disassociateVpcRouteSubnet(f.request,region,vpc,t,association,f.config);
  assert.equal(result.disassociated,association);
  assert.deepEqual(result.rows.map(row=>[row.id,row.associations.length]),[[table,0],[peerTable,1]]);
  assert.deepEqual(f.calls.map(call=>call.body),[
    {Action:'DescribeRouteTables',Version:'2016-11-15'},
    {Action:'DisassociateRouteTable',Version:'2016-11-15',AssociationId:assigned},
    {Action:'DescribeRouteTables',Version:'2016-11-15'},
  ]);
  for(const [ack,read] of [[removal('false'),mixed()],[removal(),mixed({current:[onTable]})],
    [removal(),mixed({foreign:[assoc(otherTable,foreign,assigned)]})],
    [removal(),mixed({peers:[assoc(peerTable,target,id('rtbassoc',5))]})],
    [removal(),doc('DescribeRouteTables',{routeTableSet:[]})]]) {
    const f=fixture([mixed({current:[onTable]}),ack,read]);
    const [original]=await loadVpcNativeRouteTables(f.request,region,vpc,f.config);
    await assert.rejects(disassociateVpcRouteSubnet(f.request,region,vpc,original,original.associations[0],f.config),failure);
    assert.equal(f.calls.filter(x=>x.body.Action==='DisassociateRouteTable').length,1);
  }
});

test('abort or ordinary IAM denial cannot become successful or retry a write',async()=>{
  const controller=new AbortController();
  const f=fixture([mixed(),scopedSubnets,receipt(),()=>{
    controller.abort();
    return new Response('3',{headers:{'Content-Type':'text/xml'}});
  }]);
  const scoped={...f.config,signal:controller.signal};
  const [t]=await loadVpcNativeRouteTables(f.request,region,vpc,scoped);
  const [s]=await loadVpcRouteAssociationTargets(f.request,region,vpc,t,scoped);
  await assert.rejects(associateVpcRouteSubnet(f.request,region,vpc,t,s,scoped),{name:'AbortError'});
  assert.equal(f.calls.filter(x=>x.body.Action==='AssociateRouteTable').length,1);
  const denied=fixture([mixed({current:[onTable]}),()=>new Response('not authorized',{status:403})]);
  const [selected]=await loadVpcNativeRouteTables(denied.request,region,vpc,denied.config);
  await assert.rejects(disassociateVpcRouteSubnet(denied.request,region,vpc,selected,selected.associations[0],denied.config),
    error=>nativeRouteTableFailure(error)==='denied');
  assert.equal(denied.calls.length,2);
  assert.equal(nativeRouteTableFailure(Error('Resource.AlreadyAssociated: conflict')),'associated');
});
