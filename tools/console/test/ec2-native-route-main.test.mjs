import assert from 'node:assert/strict';
import test from 'node:test';
import {loadEC2RouteTablePages} from '../src/ec2-route-table-pages.mjs';
import {loadVpcRouteTables} from '../src/ec2-route-tables.mjs';
import {associateVpcRouteSubnet,changeVpcNativeIPv4Route,createVpcNativeRouteTable,deleteVpcNativeRouteTable,
  disassociateVpcRouteSubnet,loadVpcNativeRouteFallback,loadVpcNativeRouteTables,loadVpcRouteAssociationTargets,
  replaceVpcRouteSubnet} from '../src/ec2-native-route-tables.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/',region='us-east-1',cidr='10.1.0.0/16';
const id=(kind,n)=>kind+'-'+n.toString(16).padStart(17,'0');
const vpc={id:id('vpc',1),managedBy:'ec2',cidr}, legacy={id:id('vpc',2),managedBy:'ec2',cidr:'10.2.0.0/16'},
  cfn={id:id('vpc',3),managedBy:'cloudformation',cidr:'10.3.0.0/16'};
const main=id('rtb',1),custom=id('rtb',2),mainID=id('rtbassoc',1),explicitID=id('rtbassoc',2);
const turn='1'.repeat(48),opt={associations:true,replacement:true,routeLimit:16};
const node=(localName,data=[])=>({localName,namespaceURI:ns,children:Array.isArray(data)?data:[],
  textContent:typeof data==='string'?data:'',nodeType:1});
const members=fields=>Object.entries(fields).map(([name,value])=>node(name,value));
const doc=(action,fields)=>({documentElement:node(action+'Response',members({requestId:'owned-test-request',...fields}))});
const record=fields=>node('item',members(fields));
const page=(rows,token)=>doc('DescribeRouteTables',{routeTableSet:rows,...(token?{nextToken:token}:{})});
const route=(destination=cidr,gateway='local',origin='CreateRouteTable',state='active')=>
  record({destinationCidrBlock:destination,gatewayId:gateway,origin,state});
const primary=(fields={})=>record({routeTableId:main,routeTableAssociationId:mainID,main:'true',
  associationState:members({state:'associated'}),...fields});
const pair=(table=custom,subnet=2,associationId=explicitID)=>record({
  routeTableId:table,routeTableAssociationId:associationId,main:'false',subnetId:id('subnet',subnet)});
const values=(table,fields={})=>({routeTableId:table,vpcId:vpc.id,ownerId:'000000000000',routeSet:[route()],
  associationSet:table===main?[primary()]:[],tagSet:[],...fields});
const table=(tableId,fields)=>record(values(tableId,fields));
const subnets=(pairs)=>doc('DescribeSubnets',{subnetSet:pairs.map(([n,owner=vpc])=>record({
  subnetId:id('subnet',n),vpcId:owner.id,cidrBlock:'10.'+owner.cidr.split('.')[1]+'.'+n+'.0/24',
  state:'available',availabilityZone:region+'a',simulatorManagedBy:'ec2'}))});
const describe={Action:'DescribeRouteTables',Version:'2016-11-15',MaxResults:'100'};
const criterion={...describe,'Filter.1.Name':'association.subnet-id','Filter.1.Value.1':id('subnet',2)};
const listSubnets={Action:'DescribeSubnets',Version:'2016-11-15'};
const invalid=/Invalid or incomplete/;
const response=(i,status=200)=>new Response(String(i),{status,headers:{'Content-Type':'text/xml'}});
function fixture(...replies) {
  const calls=[];
  const request=async(...args)=>{
    assert.equal(args.length,2,'must not override identity');
    const [path,init]=args,index=calls.length;
    assert.equal(path,'/');assert.equal(init.method,'POST');assert.equal(init.credentials,'same-origin');
    assert.equal(init.headers['X-Simulator-Region'],region);
    calls.push(Object.fromEntries(init.body));
    assert.ok(index<replies.length,'unexpected request');
    return typeof replies[index]==='function'?replies[index](index,init.signal):response(index);
  };
  return {calls,request,parse:encoded=>replies[Number(encoded)]};
}
const cfg=f=>({...opt,parse:f.parse});

test('fresh native main/local are verified in full pagination; exact/read filter never synthesizes main subnet IDs',async()=>{
  const many=Array.from({length:99},(_,i)=>table(id('rtb',i+3),{vpcId:legacy.id,routeSet:[]}));
  // Other VPC rows fill page one after the custom row. The same VPC's main
  // table (with empty tags) arrives only on page two.
  const later=id('rtb',200), laterAssoc=primary({routeTableId:later});
  const f=fixture(page([table(custom),...many],turn),page([table(later,{associationSet:[laterAssoc]})]));
  const rows=await loadVpcNativeRouteTables(f.request,region,vpc,cfg(f));
  assert.deepEqual(f.calls,[describe,{...describe,NextToken:turn}]);
  assert.deepEqual(rows.map(row=>[row.id,row.localRoute,row.mainAssociation,row.routes,row.associations]),[
    [custom,{cidr,gateway:'local'},undefined,[],[]],[later,{cidr,gateway:'local'},{id:mainID},[],[]]]);
  assert.ok(Object.isFrozen(rows[1].mainAssociation)&&Object.isFrozen(rows[1].localRoute));

  const filtered=fixture(page([table(custom,{associationSet:[pair()]})]));
  const selected=await loadEC2RouteTablePages(filtered.request,region,{...filtered,selectedSubnet:id('subnet',2)});
  assert.deepEqual(selected.tables[0].associations,[{id:explicitID,subnet:id('subnet',2)}]);
  assert.equal(selected.tables[0].mainAssociation,undefined);
  assert.deepEqual(filtered.calls,[criterion]);
  const filterCannotInfer=fixture(page([table(main)]));
  await assert.rejects(loadEC2RouteTablePages(filterCannotInfer.request,region,{...filterCannotInfer,selectedSubnet:id('subnet',2)}),invalid);
  const byId=fixture(page([table(custom)]));
  assert.equal((await loadEC2RouteTablePages(byId.request,region,{...byId,exactID:custom})).tables[0].localRoute.cidr,cidr);
  assert.deepEqual(byId.calls,[{Action:'DescribeRouteTables',Version:'2016-11-15','RouteTableId.1':custom}]);
});

test('rejects malformed, ambiguous, cross-scope and duplicate main or local witnesses',async()=>{
  for (const rows of [
    [table(main,{routeSet:[]})],
    [table(main,{routeSet:[route(cidr,'local','CreateRoute')]})],
    [table(main,{routeSet:[route(cidr,'local','CreateRouteTable','blackhole')]})],
    [table(main,{routeSet:[route(cidr),route('10.2.0.0/16')]})],
    [table(main,{routeSet:[route('10.9.0.0/16')]})],
    [table(main,{associationSet:[primary({subnetId:''})]})],
    [table(main,{associationSet:[primary({gatewayId:id('igw',1)})]})],
    [table(main,{associationSet:[primary({associationState:members({state:'disassociated'})})]})],
    [table(main,{associationSet:[primary({routeTableId:custom})]})],
    [table(main,{associationSet:[primary(),primary({routeTableAssociationId:explicitID})]})],
    [table(main),table(custom,{associationSet:[primary({routeTableId:custom,routeTableAssociationId:explicitID})]})],
    [table(main),table(custom,{associationSet:[pair(custom,2,mainID)]})],
    [table(main),table(custom,{routeSet:[]})],
    [table(main),table(custom,{routeSet:[route('10.2.0.0/16')]})],
    [table(custom)],
  ]) {
    const f=fixture(page(rows));
    await assert.rejects(loadVpcNativeRouteTables(f.request,region,vpc,cfg(f)),invalid);
    assert.deepEqual(f.calls,[describe]);
  }
  const cfnWitness=fixture(page([table(main,{vpcId:cfn.id,routeSet:[route(cfn.cidr)]})]));
  await assert.rejects(loadVpcRouteTables(cfnWitness.request,region,cfn,cfnWitness),/Invalid CloudFormation-managed/);
  const older=fixture(page([table(main,{vpcId:legacy.id,routeSet:[],associationSet:[]})]));
  const rows=await loadVpcNativeRouteTables(older.request,region,legacy,cfg(older));
  assert.deepEqual(await loadVpcNativeRouteFallback(older.request,region,legacy,rows,cfg(older)),null);
  assert.deepEqual(older.calls,[describe]);
});

test('effective subnets require complete same-identity inventory; independently verified explicit rows never double count',async()=>{
  const inventory=page([table(main,{associationSet:[primary(),pair(main,3,id('rtbassoc',3))]}),
    table(custom,{associationSet:[pair()]})]);
  const f=fixture(inventory,subnets([[1],[2],[3],[4,legacy]]));
  const rows=await loadVpcNativeRouteTables(f.request,region,vpc,cfg(f));
  await assert.rejects(loadVpcNativeRouteFallback(f.request,region,vpc,rows.slice(0,1),cfg(f)),invalid);
  await assert.rejects(loadVpcNativeRouteFallback(f.request,region,vpc,[rows[0],{...rows[1]}],cfg(f)),invalid);
  await assert.rejects(loadVpcNativeRouteFallback(f.request,'us-west-2',vpc,rows,cfg(f)),invalid);
  await assert.rejects(loadVpcNativeRouteFallback(()=>assert.fail('unverified identity'),region,vpc,rows,cfg(f)),invalid);
  assert.deepEqual(await loadVpcNativeRouteFallback(f.request,region,vpc,rows,cfg(f)),{tableID:main,subnetIDs:[id('subnet',1)]});
  assert.deepEqual(f.calls,[describe,listSubnets]);
  for(const second of [subnets([[1],[2],[3,legacy]]),()=>new Response('',{status:403})]){
    const unavailable=fixture(inventory,second);
    const rows=await loadVpcNativeRouteTables(unavailable.request,region,vpc,cfg(unavailable));
    await assert.rejects(loadVpcNativeRouteFallback(unavailable.request,region,vpc,rows,cfg(unavailable)));
    assert.equal(rows[0].mainAssociation.id,mainID);
    assert.deepEqual(unavailable.calls,[describe,listSubnets]);
  }
  const controller=new AbortController();
  const aborted=fixture(inventory,(index,signal)=>{assert.equal(signal,controller.signal);controller.abort();return response(index);});
  const before=await loadVpcNativeRouteTables(aborted.request,region,vpc,cfg(aborted));
  await assert.rejects(loadVpcNativeRouteFallback(aborted.request,region,vpc,before,{...cfg(aborted),signal:controller.signal}),{name:'AbortError'});
  assert.deepEqual(aborted.calls,[describe,listSubnets]);
});

test('protects system rows, confirms local on create and does not let local consume a static-route slot or prevent custom deletion',async()=>{
  const token='bounded-local-create';
  const fresh=fixture(doc('CreateRouteTable',{clientToken:token,routeTable:members(values(custom))}),
    page([table(main),table(custom)]),doc('DeleteRouteTable',{return:'true'}),page([table(main)]));
  const {rows,created}=await createVpcNativeRouteTable(fresh.request,region,vpc,'',token,cfg(fresh));
  const protectedTable=rows.find(row=>row.mainAssociation);
  await assert.rejects(deleteVpcNativeRouteTable(fresh.request,region,vpc,protectedTable,cfg(fresh)),invalid);
  await assert.rejects(disassociateVpcRouteSubnet(fresh.request,region,vpc,protectedTable,protectedTable.mainAssociation,cfg(fresh)),invalid);
  await assert.rejects(replaceVpcRouteSubnet(fresh.request,region,vpc,protectedTable,protectedTable.mainAssociation,created,cfg(fresh)),invalid);
  await assert.rejects(changeVpcNativeIPv4Route(fresh.request,region,vpc,created,created.localRoute,cidr,false,cfg(fresh)),invalid);
  assert.equal(fresh.calls.length,2);
  const {rows:remaining}=await deleteVpcNativeRouteTable(fresh.request,region,vpc,created,cfg(fresh));
  assert.equal(remaining[0].id,main);
  assert.deepEqual(fresh.calls.map(q=>q.Action),['CreateRouteTable','DescribeRouteTables','DeleteRouteTable','DescribeRouteTables']);
  const missingLocal=fixture(doc('CreateRouteTable',{clientToken:token,routeTable:members(values(custom,{routeSet:[]}))}),
    page([table(main),table(custom)]));
  await assert.rejects(createVpcNativeRouteTable(missingLocal.request,region,vpc,'',token,cfg(missingLocal)),invalid);
  const droppedMain=fixture(page([table(main),table(custom)]),doc('DeleteRouteTable',{return:'true'}),page([]));
  const [,detached]=await loadVpcNativeRouteTables(droppedMain.request,region,vpc,cfg(droppedMain));
  await assert.rejects(deleteVpcNativeRouteTable(droppedMain.request,region,vpc,detached,cfg(droppedMain)),invalid);
  assert.equal(droppedMain.calls.filter(q=>q.Action==='DeleteRouteTable').length,1);

  const staticRoutes=Array.from({length:16},(_,i)=>route('198.51.100.'+i+'/32',id('igw',1),'CreateRoute'));
  const atLimit=fixture(page([table(main,{routeSet:[route(),...staticRoutes]})]));
  const [one]=await loadVpcNativeRouteTables(atLimit.request,region,vpc,cfg(atLimit));
  assert.equal(one.routes.length,16);assert.equal(one.localRoute.cidr,cidr);
});

test('explicit attach and move remain scoped with a main; refreshed fallback removes and returns only the exact subnet',async()=>{
  const movedId=id('rtbassoc',8);
  const wire=pair(custom,1),moved=pair(main,1,movedId);
  const f=fixture(page([table(main),table(custom)]),subnets([[1],[2]]),
    doc('AssociateRouteTable',{associationId:explicitID,associationState:members({state:'associated'})}),
    page([table(main),table(custom,{associationSet:[wire]})]),subnets([[1],[2]]),
    doc('ReplaceRouteTableAssociation',{newAssociationId:movedId,associationState:members({state:'associated'})}),
    page([table(main,{associationSet:[primary(),moved]}),table(custom)]),subnets([[1],[2]]),
    doc('DisassociateRouteTable',{return:'true'}),page([table(main),table(custom)]),subnets([[1],[2]]));
  const [mainRow,customRow]=await loadVpcNativeRouteTables(f.request,region,vpc,cfg(f));
  const targets=await loadVpcRouteAssociationTargets(f.request,region,vpc,customRow,cfg(f));
  assert.deepEqual(targets.map(row=>row.id),[id('subnet',1),id('subnet',2)]);
  const attached=await associateVpcRouteSubnet(f.request,region,vpc,customRow,targets[0],cfg(f));
  assert.deepEqual(await loadVpcNativeRouteFallback(f.request,region,vpc,attached.rows,cfg(f)),{tableID:main,subnetIDs:[id('subnet',2)]});
  const movedRow=await replaceVpcRouteSubnet(f.request,region,vpc,attached.rows[1],attached.associated,attached.rows[0],cfg(f));
  assert.equal(movedRow.rows[0].mainAssociation.id,mainRow.mainAssociation.id);
  assert.deepEqual(await loadVpcNativeRouteFallback(f.request,region,vpc,movedRow.rows,cfg(f)),{tableID:main,subnetIDs:[id('subnet',2)]});
  const gone=await disassociateVpcRouteSubnet(f.request,region,vpc,movedRow.rows[0],movedRow.replaced,cfg(f));
  assert.deepEqual(await loadVpcNativeRouteFallback(f.request,region,vpc,gone.rows,cfg(f)),
    {tableID:main,subnetIDs:[id('subnet',1),id('subnet',2)]});
  assert.deepEqual(f.calls.filter(q=>!q.Action.startsWith('Describe')),[
    {Action:'AssociateRouteTable',Version:'2016-11-15',RouteTableId:custom,SubnetId:id('subnet',1)},
    {Action:'ReplaceRouteTableAssociation',Version:'2016-11-15',AssociationId:explicitID,RouteTableId:main},
    {Action:'DisassociateRouteTable',Version:'2016-11-15',AssociationId:movedId}]);
});
