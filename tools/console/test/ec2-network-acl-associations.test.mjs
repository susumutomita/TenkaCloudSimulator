import assert from 'node:assert/strict';
import test from 'node:test';
import {changeNetworkACLRule,createVpcNetworkACL,deleteVpcNetworkACL,loadSubnetNetworkACL,parseNetworkACLInventory,parseVpcNetworkACLs,replaceSubnetNetworkACL,subnetACLAssociationAvailable} from '../src/ec2-network-acls.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const id=(prefix,h)=>prefix+'-'+h.repeat(17);
const vpc={id:id('vpc','1'),managedBy:'ec2'}, subnet={id:id('subnet','2'),vpc:vpc.id,owner:'ec2'};
const region='us-east-1', baseline=id('acl','3'), custom=id('acl','4'), old=id('aclassoc','5'), next=id('aclassoc','6');
const node=(localName,x=[])=>({localName,namespaceURI:ns,children:Array.isArray(x)?x:[],textContent:typeof x==='string'?x:''});
const item=obj=>node('item',Object.entries(obj).map(([k,v])=>node(k,v)));
const env=(a,...values)=>({documentElement:node(a+'Response',[node('requestId','test'),...values])});
const assoc=(target,association,member=subnet.id)=>item({networkAclId:target,networkAclAssociationId:association,subnetId:member});
const row=(target,mode,links=[],extra={})=>item({
  networkAclId:target,vpcId:vpc.id,ownerId:'000000000000',
  default:mode==='default-config-only'?'true':'false',simulatorNetworkAclMode:mode,associationSet:links,
  entrySet:['false','true'].flatMap(egress=>[
    ...(mode==='default-config-only'?[item({ruleNumber:'100',protocol:'-1',ruleAction:'allow',cidrBlock:'0.0.0.0/0',egress})]:[]),
    item({ruleNumber:'32767',protocol:'-1',ruleAction:'deny',cidrBlock:'0.0.0.0/0',egress}),
  ]),...extra,
});
const d=(...links)=>row(baseline,'default-config-only',links);
const e=()=>row(custom,'detached-config-only');
const a=(...links)=>row(custom,'attached-config-only',links);
const list=(...acl)=>env('DescribeNetworkAcls',node('networkAclSet',acl));
const replace=(nid=next)=>env('ReplaceNetworkAclAssociation',node('newAssociationId',nid));
const current=list(d(assoc(baseline,old)));
const changed=list(a(assoc(custom,next)));
const prior={acl:{id:baseline,vpc:vpc.id},association:{id:old,subnetId:subnet.id}};
const destination={id:custom,vpc:vpc.id};
const filter={'Filter.1.Name':'association.subnet-id','Filter.1.Value.1':subnet.id};
function mock(...documents){
  const calls=[];
  return {calls,options:{parse:()=>{assert.ok(documents.length,'unexpected retry');return documents.shift()}},
    request:async(path,args)=>{assert.equal(path,'/');assert.equal(args.headers['X-Simulator-Region'],region);
      calls.push(Object.fromEntries(args.body));return new Response('xml',{headers:{'content-type':'text/xml'}})}};
}
test('native defaults and attached ACLs require explicit typed wire rows; old editor projects detached only',()=>{
  const rows=parseNetworkACLInventory(list(d(assoc(baseline,old)),e()),vpc.id);
  assert.equal(rows[0].isDefault,true);assert.equal(rows[0].entries[0].number,100);
  assert.deepEqual(rows[0].associations,[{id:old,subnetId:subnet.id}]);
  assert.deepEqual(parseVpcNetworkACLs(list(d(assoc(baseline,old)),e()),vpc.id),[{id:custom,vpc:vpc.id,mode:'detached-config-only',name:''}]);
  assert.equal(parseNetworkACLInventory(list(d(),a(assoc(custom,next))),vpc.id)[1].mode,'attached-config-only');
  for(const invalid of [list(row(baseline,'default-config-only',[],{entrySet:[]})),list(a()),list(d(assoc(custom,old))),list(d(),row(custom,'default-config-only')),
    list(d(assoc(baseline,old)),a(assoc(custom,next))),list(d(assoc(baseline,old)),a(assoc(custom,old,id('subnet','7')))),
    list(row(custom,'detached-config-only',[assoc(custom,next)])),list(row(custom,'attached-config-only',[assoc(custom,next)],{default:'true'}))]){
    assert.throws(()=>parseNetworkACLInventory(invalid,vpc.id),/Invalid or incomplete/);
    assert.throws(()=>parseVpcNetworkACLs(invalid,vpc.id),/Invalid or incomplete/);
  }
});
test('only one selected saved association is replaced; require newly issued exact ID then fresh readback',async()=>{
  const m=mock(current,replace(),changed);
  const result=await replaceSubnetNetworkACL(m.request,region,vpc,subnet,prior,destination,m.options);
  assert.deepEqual(result.association,{id:next,subnetId:subnet.id});assert.equal(result.acl.id,custom);
  assert.deepEqual(m.calls,[
    {Action:'DescribeNetworkAcls',Version:'2016-11-15',...filter},
    {Action:'ReplaceNetworkAclAssociation',Version:'2016-11-15',AssociationId:old,NetworkAclId:custom},
    {Action:'DescribeNetworkAcls',Version:'2016-11-15',...filter},
  ]);
  for (const [docs,calls] of [[[changed],1],[[current,replace(old)],2],
    [[current,replace(),current],3],[[current,replace(),list()],3],
    [[current,replace(),list(a(assoc(custom,next,id('subnet','7'))))],3]]){
    const bad=mock(...docs);
    await assert.rejects(replaceSubnetNetworkACL(bad.request,region,vpc,subnet,prior,destination,bad.options),/Invalid or incomplete/);
    assert.equal(bad.calls.length,calls);
  }
});
test('native-only admission and real absence do not imply a default',async()=>{
  const m=mock(list());
  assert.equal(await loadSubnetNetworkACL(m.request,region,vpc,subnet,m.options),null);
  await assert.rejects(loadSubnetNetworkACL(m.request,region,vpc,{...subnet,owner:'cloudformation'},m.options),/Invalid or incomplete/);
  await assert.rejects(replaceSubnetNetworkACL(m.request,region,vpc,subnet,prior,{...destination,vpc:id('vpc','9')},m.options),/Invalid or incomplete/);
  assert.equal(m.calls.length,1);
});
test('legacy detached ACL actions never confuse attached readback with disappearance or editability',async()=>{
  const saved={id:custom,vpc:vpc.id,mode:'detached-config-only',name:''};
  const del=mock(env('DeleteNetworkAcl',node('return','true')),changed);
  await assert.rejects(deleteVpcNetworkACL(del.request,region,vpc,saved,del.options),/Invalid or incomplete/);
  assert.equal(del.calls.length,2);
  const [detached]=parseNetworkACLInventory(list(e()),vpc.id);
  const edit=mock(changed);
  await assert.rejects(changeNetworkACLRule(edit.request,region,vpc,detached,'create',
    {number:110,egress:false,protocol:'6',action:'allow',cidr:'192.0.2.0/24',ports:{from:443,to:443}},edit.options),/Invalid or incomplete/);
  assert.equal(edit.calls.length,1);
});

test('subnet ACL controls require exact feature, matching actions, selector and native non-scoped management session',()=>{
  const ec2={console:true,operations:['DescribeSubnets','DescribeNetworkAcls','ReplaceNetworkAclAssociation'],
    networkAclAssociationProfile:'single-native-ipv4-subnet-config-only',
    describeSelectors:{DescribeNetworkAcls:['vpc-id','association.subnet-id']},
    detachedConfigurationOnly:['native network ACLs'],subnetLifecycle:['CreateSubnet']};
  const cap={services:{ec2}};
  assert.equal(subnetACLAssociationAvailable(cap,true),true);
  assert.equal(subnetACLAssociationAvailable(cap,false),false);
  assert.equal(subnetACLAssociationAvailable({...cap,consoleScope:{instanceId:'instance'}},true),false);
  for(const subset of [{networkAclAssociationProfile:'generic'},{subnetLifecycle:[]},
    {describeSelectors:{DescribeNetworkAcls:['vpc-id']}},{operations:['DescribeSubnets','DescribeNetworkAcls']}]) {
    assert.equal(subnetACLAssociationAvailable({services:{ec2:{...ec2,...subset}}},true),false);
  }
});

test('an exact create receipt for an already-attached custom ACL verifies live association, not a new detached resource',async()=>{
  const token='previous-create-token';
  const creation=env('CreateNetworkAcl',node('networkAcl',a(assoc(custom,next)).children),node('clientToken',token));
  const m=mock(creation,list(d(),a(assoc(custom,next))));
  const result=await createVpcNetworkACL(m.request,region,vpc,'',token,m.options);
  assert.equal(result.created.id,custom);assert.equal(result.created.mode,'attached-config-only');
  assert.deepEqual(result.rows,[]);
  const missing=mock(creation,list(d(),e()));
  await assert.rejects(createVpcNetworkACL(missing.request,region,vpc,'',token,missing.options),/Invalid or incomplete/);
  assert.equal(missing.calls.length,2);
});
