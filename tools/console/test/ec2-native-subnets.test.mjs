import assert from 'node:assert/strict';
import test from 'node:test';
import {changeSubnetTag,loadSubnetTagSnapshot,nativeSubnetTagsAvailable,createVpcNativeSubnet, deleteVpcNativeSubnet, loadVpcSubnets, modifyVpcNativeSubnetPublicIPv4, nativeSubnetCIDR, nativeSubnetFailure, parseVpcSubnets, vpcNativeSubnetsAvailable, vpcNativeSubnetPublicIPv4Available} from '../src/ec2-native-subnets.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/', region='us-east-1';
const vpc={id:'vpc-'+'1'.repeat(17),cidr:'10.1.0.0/16',managedBy:'ec2'};
const stack={id:'vpc-'+'2'.repeat(17),cidr:'10.2.0.0/16',managedBy:'cloudformation'};
const child={id:'subnet-'+'3'.repeat(17),vpc:vpc.id,cidr:'10.1.1.0/24',az:region+'b',owner:'ec2'};
const legacy={id:'subnet-'+'4'.repeat(17),vpc:stack.id,cidr:'10.2.1.0/24',az:region+'a',owner:'cloudformation'};
const element=(localName,value='',namespaceURI=ns)=>({localName,namespaceURI,children:Array.isArray(value)?value:[],textContent:typeof value==='string'?value:''});
const tag=(item,name='item')=>element(name,[element('subnetId',item.id),element('vpcId',item.vpc),element('cidrBlock',item.cidr),element('state','available'),
  element('availabilityZone',item.az),...(item.owner==='ec2'?[element('simulatorManagedBy','ec2')]:[]),
  ...(item.publicIPv4!==undefined?[element('mapPublicIpOnLaunch',String(item.publicIPv4))]:[]),element('tagSet',(item.tags??[]).map(([key,value])=>element('item',[element('key',key),element('value',value)])))]);
const doc=(action,body)=>({documentElement:element(action+'Response',[element('requestId','local'),body])});
const list=(items)=>doc('DescribeSubnets',element('subnetSet',items.map(item=>tag(item))));
const options=parse=>({parse});
const fixture=(...docs)=>{
  const calls=[]; return {calls, request:async(path,init)=>{calls.push([path,init]);return new Response(String(calls.length-1),{headers:{'Content-Type':'text/xml; charset=UTF-8'}});},parse:text=>docs[Number(text)]};
};

test('ordinary signed-in capability, bounded IPv4 range and distinct owners',()=>{
  const services={ec2:{console:true,subnetLifecycle:['DescribeSubnets','CreateSubnet','DeleteSubnet']}};
  assert.equal(vpcNativeSubnetsAvailable({services},true),true);
  assert.equal(vpcNativeSubnetsAvailable({services},false),false);
  assert.equal(vpcNativeSubnetsAvailable({services,consoleScope:{instanceId:'i-bound'}},true),false);
  assert.equal(vpcNativeSubnetsAvailable({services:{ec2:{console:true,subnetLifecycle:['DescribeSubnets']}}},true),false);
  assert.equal(nativeSubnetCIDR(vpc,'10.1.1.23/24'),'10.1.1.0/24');
  for(const cidr of ['10.2.1.0/24','10.1.1.0/31','10.01.0.0/24','2001:db8::/24']) assert.equal(nativeSubnetCIDR(vpc,cidr),'');
  assert.deepEqual(parseVpcSubnets(list([legacy,child]),region,vpc),[child]);
  assert.deepEqual(parseVpcSubnets(list([legacy,child]),region,stack),[legacy]);
  assert.throws(()=>parseVpcSubnets(list([child,child]),region,vpc));
  assert.throws(()=>parseVpcSubnets(list([{...child,owner:'cloudformation'}]),region,vpc));
  assert.throws(()=>parseVpcSubnets(list([{...child,cidr:'10.1.1.3/24'}]),region,vpc));
  assert.throws(()=>parseVpcSubnets(list([{...legacy,az:'us-west-2a'}]),region,vpc));
  assert.throws(()=>parseVpcSubnets(doc('DescribeSubnets',element('wrong')),region,vpc));
});

test('subnet is saved only with a matching receipt and exact VPC readback; delete is verified absent',async()=>{
  const f=fixture(doc('CreateSubnet',tag(child,'subnet')),list([legacy,child]),doc('DeleteSubnet',element('return','true')),list([legacy]));
  const saved=await createVpcNativeSubnet(f.request,region,vpc,'10.1.1.69/24',region+'b',options(f.parse));
  assert.deepEqual(saved,{created:child,rows:[child]});
  assert.deepEqual(await deleteVpcNativeSubnet(f.request,region,vpc,child,options(f.parse)),{removed:child,rows:[]});
  assert.deepEqual(f.calls.map(([_,init])=>Object.fromEntries(init.body)),[
    {Action:'CreateSubnet',Version:'2016-11-15',VpcId:vpc.id,CidrBlock:child.cidr,AvailabilityZone:child.az},
    {Action:'DescribeSubnets',Version:'2016-11-15'},
    {Action:'DeleteSubnet',Version:'2016-11-15',SubnetId:child.id},
    {Action:'DescribeSubnets',Version:'2016-11-15'},
  ]);
  for(const [path,init] of f.calls){
    assert.equal(path,'/');assert.equal(init.credentials,'same-origin');assert.equal(init.redirect,'error');assert.equal(init.method,'POST');
    assert.deepEqual(init.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region});
  }
});

test('refuses unsupported owner locally and does not retry an unconfirmed or denied write',async()=>{
  const untouched=()=>assert.fail('wrong scope requested the simulator');
  await assert.rejects(createVpcNativeSubnet(untouched,region,stack,legacy.cidr,legacy.az));
  await assert.rejects(deleteVpcNativeSubnet(untouched,region,vpc,legacy));
  for (const [responses,length] of [
    [[doc('CreateSubnet',tag({...child,vpc:stack.id},'subnet'))],1],
    [[doc('CreateSubnet',tag(child,'subnet')),list([legacy])],2],
  ]) {
    const f=fixture(...responses);
    await assert.rejects(createVpcNativeSubnet(f.request,region,vpc,child.cidr,child.az,options(f.parse)));
    assert.equal(f.calls.length,length);
  }
  const denied=async()=>{throw Error('UnauthorizedOperation: read denied');};
  await assert.rejects(loadVpcSubnets(denied,region,vpc),e=>nativeSubnetFailure(e)==='denied');
  const controller=new AbortController(), aborted=async()=>{controller.abort();return new Response('refused',{headers:{'Content-Type':'text/xml'}});};
  await assert.rejects(loadVpcSubnets(aborted,region,vpc,{signal:controller.signal,parse:untouched}),{name:'AbortError'});
});

const settingsAction = 'ModifySubnetAttribute';
const off = {...child,publicIPv4:false}, on = {...child,publicIPv4:true};
const settings = parse=>({parse,publicIPv4:true});
const acknowledge = doc(settingsAction,element('return','true'));

test('settings require native advertised capability and typed explicit true/false without changing CFN reads',()=>{
  const lifecycle=['DescribeSubnets','CreateSubnet','DeleteSubnet'];
  const capabilities=withSettings=>({services:{ec2:{console:true,subnetLifecycle:withSettings?[...lifecycle,settingsAction]:lifecycle,
    operations:[...lifecycle,settingsAction]}}});
  assert.equal(vpcNativeSubnetPublicIPv4Available(capabilities(false),true),false);
  assert.equal(vpcNativeSubnetPublicIPv4Available(capabilities(true),false),false);
  assert.equal(vpcNativeSubnetPublicIPv4Available({...capabilities(true),consoleScope:{}},true),false);
  assert.equal(vpcNativeSubnetPublicIPv4Available(capabilities(true),true),true);
  assert.deepEqual(parseVpcSubnets(list([off,legacy]),region,vpc,{publicIPv4:true}),[off]);
  assert.deepEqual(parseVpcSubnets(list([on,legacy]),region,vpc,{publicIPv4:true}),[on]);
  assert.deepEqual(parseVpcSubnets(list([on,legacy]),region,stack,{publicIPv4:true}),[legacy]);
  assert.throws(()=>parseVpcSubnets(list([child]),region,vpc,{publicIPv4:true}));
  assert.throws(()=>parseVpcSubnets(list([{...child,publicIPv4:'TRUE'}]),region,vpc,{publicIPv4:true}));
  assert.throws(()=>parseVpcSubnets(list([off,{...legacy,publicIPv4:true}]),region,stack,{publicIPv4:true}));
});

test('only the exact verified row, transport, VPC and region can save the selected boolean',async()=>{
  const f=fixture(list([off,legacy]),acknowledge,list([on,legacy]),acknowledge,list([off,legacy]));
  const [local]=await loadVpcSubnets(f.request,region,vpc,settings(f.parse));
  await assert.rejects(modifyVpcNativeSubnetPublicIPv4(f.request,region,vpc,{...local},true,settings(f.parse)));
  await assert.rejects(modifyVpcNativeSubnetPublicIPv4(f.request,'us-west-2',vpc,local,true,settings(f.parse)));
  await assert.rejects(modifyVpcNativeSubnetPublicIPv4(f.request,region,stack,local,true,settings(f.parse)));
  await assert.rejects(modifyVpcNativeSubnetPublicIPv4(f.request,region,vpc,local,true,options(f.parse)));
  await assert.rejects(modifyVpcNativeSubnetPublicIPv4(f.request,region,vpc,local,'true',settings(f.parse)));
  assert.equal(f.calls.length,1);
  const result=await modifyVpcNativeSubnetPublicIPv4(f.request,region,vpc,local,true,settings(f.parse));
  assert.deepEqual(result,{updated:on,rows:[on]});
  const again=await modifyVpcNativeSubnetPublicIPv4(f.request,region,vpc,result.updated,false,settings(f.parse));
  assert.deepEqual(again,{updated:off,rows:[off]});
  assert.deepEqual(f.calls.map(([,i])=>Object.fromEntries(i.body)),[
    {Action:'DescribeSubnets',Version:'2016-11-15'},
    {Action:settingsAction,Version:'2016-11-15',SubnetId:child.id,'MapPublicIpOnLaunch.Value':'true'},
    {Action:'DescribeSubnets',Version:'2016-11-15'},
    {Action:settingsAction,Version:'2016-11-15',SubnetId:child.id,'MapPublicIpOnLaunch.Value':'false'},
    {Action:'DescribeSubnets',Version:'2016-11-15'},
  ]);
});

test('settings are unconfirmed for a wrong or incomplete live reply and never replayed',async()=>{
  for(const [ack,after,expected] of [
    [doc(settingsAction,element('return','false')),list([on]),2],
    [acknowledge,list([off]),3],
    [acknowledge,list([{...on,id:'subnet-'+'9'.repeat(17)}]),3],
    [acknowledge,list([{...on,az:region+'a'}]),3],
    [acknowledge,list([child]),3],
  ]){
    const f=fixture(list([off]),ack,after);
    const [saved]=await loadVpcSubnets(f.request,region,vpc,settings(f.parse));
    await assert.rejects(modifyVpcNativeSubnetPublicIPv4(f.request,region,vpc,saved,true,settings(f.parse)));
    assert.equal(f.calls.length,expected);
    assert.equal(f.calls.filter(([,i])=>i.body.get('Action')===settingsAction).length,1);
  }
});

test('native subnet Tags require their own capability and strictly bound stored rows',async()=>{
  const ec2={console:true,subnetLifecycle:['DescribeSubnets','CreateSubnet','DeleteSubnet'],
    operations:['DescribeSubnets','CreateTags','DeleteTags'],vpcTagMutationProfile:'single-native-vpc'};
  assert.equal(nativeSubnetTagsAvailable({services:{ec2}},true),false);
  ec2.subnetTagMutationProfile='single-native-subnet';
  assert.equal(nativeSubnetTagsAvailable({services:{ec2}},true),true);
  assert.equal(nativeSubnetTagsAvailable({services:{ec2},consoleScope:{}},true),false);
  const record={...child,tags:[['Name','演習-subnet'],['Owner','']]};
  const f=fixture(list([legacy,record]));
  const chosen=await loadSubnetTagSnapshot(f.request,region,child,options(f.parse));
  assert.equal(chosen.name,'演習-subnet');
  assert.deepEqual(chosen.tags,[{key:'Name',value:'演習-subnet'},{key:'Owner',value:''}]);
  await assert.rejects(loadSubnetTagSnapshot(f.request,region,{...child,id:child.id+'\n'},options(f.parse)));
  await assert.rejects(loadSubnetTagSnapshot(f.request,region,legacy,options(f.parse)));
  assert.equal(f.calls.length,1);
});
test('native subnet tag edit and explicit blank delete verify same regional row and send one write',async()=>{
  const before={...child,publicIPv4:true,tags:[['Name','old'],['Owner','']]};
  const after={...before,tags:[['Name','new'],['Owner','']]};
  const removed={...before,tags:[['Name','new']]};
  const ack=action=>doc(action,element('return','true'));
  const f=fixture(list([before]),list([before]),ack('CreateTags'),list([after]),list([after]),ack('DeleteTags'),list([removed]));
  const current=await loadSubnetTagSnapshot(f.request,region,child,options(f.parse));
  const name=await changeSubnetTag(f.request,region,child,current,{kind:'upsert',key:'Name',value:'new'},options(f.parse));
  const final=await changeSubnetTag(f.request,region,child,name,{kind:'remove',key:'Owner'},options(f.parse));
  assert.equal(final.name,'new');assert.equal(final.publicIPv4,true);
  assert.deepEqual(final.tags,[{key:'Name',value:'new'}]);
  const sent=f.calls.map(([,v])=>Object.fromEntries(v.body));
  assert.deepEqual(sent.map(row=>row.Action),['DescribeSubnets','DescribeSubnets','CreateTags',
    'DescribeSubnets','DescribeSubnets','DeleteTags','DescribeSubnets']);
  assert.deepEqual(sent[5],{Action:'DeleteTags',Version:'2016-11-15','ResourceId.1':child.id,'Tag.1.Key':'Owner','Tag.1.Value':''});
});
test('subnet tag update does not replay stale or mismatched scopes or claim success from a receipt',async()=>{
  const old={...child,tags:[['Name','old']]}, changed={...child,tags:[['Name','other']]};
  let f=fixture(list([old]),list([changed]));
  let current=await loadSubnetTagSnapshot(f.request,region,child,options(f.parse));
  const newName={kind:'upsert',key:'Name',value:'new'};
  await assert.rejects(changeSubnetTag(f.request,region,{...child,vpc:stack.id},current,newName,options(f.parse)));
  await assert.rejects(changeSubnetTag(f.request,region,child,{...current},newName,options(f.parse)));
  await assert.rejects(changeSubnetTag(f.request,region,child,current,newName,options(f.parse)));
  assert.equal(f.calls.length,2);
  f=fixture(list([old]),list([old]),doc('CreateTags',element('return','true')),list([old]));
  current=await loadSubnetTagSnapshot(f.request,region,child,options(f.parse));
  await assert.rejects(changeSubnetTag(f.request,region,child,current,newName,options(f.parse)));
  assert.equal(f.calls.length,4);
});

test('the shared inventory error distinguishes the unpaginated Console response limit',async()=>{
  const signal=new AbortController().signal;
  let canceled=false;
  const regionReply=new Response(new ReadableStream({
    start(controller){
      const chunk=new Uint8Array(1024*1024).fill(32);
      for(let i=0;i<17;i++)controller.enqueue(chunk);
    },
    cancel(){canceled=true;},
  }),{headers:{'Content-Type':'text/xml'}});
  await assert.rejects(loadSubnetTagSnapshot(async()=>regionReply,region,child,{signal,
    parse:()=>assert.fail('oversized XML cannot be interpreted as an empty inventory')}),
    e=>nativeSubnetFailure(e)==='response-too-large');
  assert.equal(canceled,true);
});
