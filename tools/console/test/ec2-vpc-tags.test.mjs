import assert from 'node:assert/strict';
import test from 'node:test';
import {changeVpcTag,loadVpcTagSnapshot,parseVpcTagSnapshot,validVpcTagKey,validVpcTagValue,vpcTagEditingAvailable,vpcTagFailure} from '../src/ec2-vpc-tags.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const vpc={id:'vpc-'+'1'.repeat(17),managedBy:'ec2'}, peer={...vpc,id:'vpc-'+'2'.repeat(17)};
const region='us-east-1';
const nd=(localName,body=[])=>({localName,namespaceURI:ns,children:Array.isArray(body)?body:[],textContent:typeof body==='string'?body:''});
const item=obj=>nd('item',Object.entries(obj).map(([k,v])=>nd(k,v)));
const tags=record=>Object.entries(record).map(([key,value])=>item({key,value}));
const row=(target,items)=>item({vpcId:target.id,cidrBlock:'10.20.0.0/16',simulatorManagedBy:target.managedBy,...items!==undefined?{tagSet:items}:{}});
const doc=(action,rows)=>({documentElement:nd(action+'Response',[nd('requestId','fixture-id'),...rows])});
const vpcs=(...rows)=>doc('DescribeVpcs',[nd('vpcSet',rows)]);
const state=(record={})=>vpcs(row(vpc,tags(record)),row(peer,tags({Name:'peer'})));
const ok=(action,result='true')=>doc(action,[nd('return',result)]);
function transport(...responses) {
  const calls=[];
  return {calls,request:async(path,options)=>{
    assert.ok(responses.length,'never send an additional request');
    calls.push([path,options]);
    return new Response('wire',{status:200,headers:{'Content-Type':'application/xml'}});
  },options:{parse:()=>responses.shift()}};
}
function actions(calls) {
  return calls.map(([path,options])=>{
    assert.equal(path,'/');assert.equal(options.method,'POST');assert.equal(options.redirect,'error');
    assert.equal(options.credentials,'same-origin');assert.equal(options.headers['X-Simulator-Service'],'ec2');
    assert.equal(options.headers['X-Simulator-Region'],region);
    assert.ok(!Object.hasOwn(options.headers,'X-Simulator-Principal'));
    return Object.fromEntries(options.body);
  });
}
const describe={Action:'DescribeVpcs',Version:'2016-11-15'};
const write=(Action,key,value)=>({Action,Version:'2016-11-15','ResourceId.1':vpc.id,'Tag.1.Key':key,'Tag.1.Value':value});

test('only the verified native edit surface advertises tag actions; bounded text matches the wire',()=>{
  const capabilities={services:{ec2:{console:true,vpcTagMutationProfile:'single-native-vpc',operations:['DescribeVpcs','CreateTags','DeleteTags']}}};
  assert.equal(vpcTagEditingAvailable(capabilities,true),true);
  assert.equal(vpcTagEditingAvailable(capabilities,false),false);
  assert.equal(vpcTagEditingAvailable({...capabilities,consoleScope:{kind:'instance'}},true),false);
  assert.equal(vpcTagEditingAvailable({services:{ec2:{...capabilities.services.ec2,operations:['DescribeVpcs','CreateTags']}}},true),false);
  assert.equal(validVpcTagKey('日本語'),true);assert.equal(validVpcTagKey('__proto__'),true);
  assert.equal(validVpcTagKey('AWS:Name'),false);assert.equal(validVpcTagKey(''),false);
  assert.equal(validVpcTagKey('名'.repeat(43)),false);assert.equal(validVpcTagValue('値'.repeat(86)),false);
  assert.equal(validVpcTagValue(''),true);assert.equal(validVpcTagValue('\ud800'),false);
});

test('regional readback selects one confirmed native VPC, never interpreting incomplete data as no tags',()=>{
  const expected=parseVpcTagSnapshot(state({Name:'selected',Unspecified:''}),vpc.id);
  assert.deepEqual(expected,{id:vpc.id,managedBy:'ec2',tags:[{key:'Name',value:'selected'},{key:'Unspecified',value:''}]});
  assert.deepEqual(parseVpcTagSnapshot(vpcs(row(vpc)),vpc.id).tags,[]);
  for(const reply of [vpcs(row(peer)),vpcs(row(vpc),row(vpc)),
    vpcs(row({...vpc,managedBy:'cloudformation'},[])),vpcs(row(vpc,[item({key:'Name',value:'first'}),item({key:'Name',value:'second'})])),
    vpcs(row(vpc,[item({key:'Name'})])),{...state(),doctype:{}},
    doc('DescribeVpcs',[nd('vpcSet',[row(vpc)]),nd('nextToken','not-complete')])]) {
    assert.throws(()=>parseVpcTagSnapshot(reply,vpc.id),/Could not verify/);
  }
});

test('set and delete send one exact scoped write with separate prior and durable state confirmation',async()=>{
  const before={Name:'old',Dept:''}, after={Name:'新しい名前',Dept:''}, deleted={Name:'新しい名前'};
  const overwrite=transport(state(before),state(before),ok('CreateTags'),state(after));
  const old=await loadVpcTagSnapshot(overwrite.request,region,vpc,overwrite.options);
  const saved=await changeVpcTag(overwrite.request,region,vpc,old,{kind:'upsert',key:'Name',value:after.Name},overwrite.options);
  assert.deepEqual(saved.tags,[{key:'Name',value:after.Name},{key:'Dept',value:''}]);
  assert.deepEqual(actions(overwrite.calls),[describe,describe,write('CreateTags','Name',after.Name),describe]);
  const remove=transport(state(after),state(after),ok('DeleteTags'),state(deleted));
  const from=await loadVpcTagSnapshot(remove.request,region,vpc,remove.options);
  assert.deepEqual((await changeVpcTag(remove.request,region,vpc,from,{kind:'remove',key:'Dept'},remove.options)).tags,
    [{key:'Name',value:after.Name}]);
  assert.deepEqual(actions(remove.calls),[describe,describe,write('DeleteTags','Dept',''),describe]);
});

test('transitions never retry writes, trust a stale snapshot, or report an unconfirmed change',async()=>{
  const before=state({Name:'first'}), unrelated=state({Name:'another'});
  for(const [replies,count] of [
    [[before,unrelated],2],[[before,before,ok('CreateTags','false')],3],
    [[before,before,ok('CreateTags'),before],4],[[before,before,ok('CreateTags'),vpcs(row(peer))],4],
  ]) {
    const x=transport(...replies);
    const snapshot=await loadVpcTagSnapshot(x.request,region,vpc,x.options);
    await assert.rejects(changeVpcTag(x.request,region,vpc,snapshot,{kind:'upsert',key:'Name',value:'requested'},x.options),/Could not verify/);
    assert.equal(x.calls.length,count);
  }
  const never=()=>assert.fail('forged selection cannot send a write or a read');
  await assert.rejects(changeVpcTag(never,region,vpc,parseVpcTagSnapshot(before,vpc.id),{kind:'remove',key:'Name'}),/Could not verify/);
  assert.throws(()=>loadVpcTagSnapshot(never,region,{...vpc,managedBy:'cloudformation'}),/Could not verify/);
});

test('legitimate tag capacity and permission failures remain distinct',async()=>{
  const full=state(Object.fromEntries(Array.from({length:50},(_,n)=>['Tag-'+n,''])));
  const x=transport(full,full);
  const scope=await loadVpcTagSnapshot(x.request,region,vpc,x.options);
  await assert.rejects(changeVpcTag(x.request,region,vpc,scope,{kind:'upsert',key:'Extra',value:'x'},x.options),
    e=>vpcTagFailure(e)==='tag-limit');
  assert.equal(x.calls.length,2);
  await assert.rejects(loadVpcTagSnapshot(async()=>new Response('',{status:403}),region,vpc),e=>vpcTagFailure(e)==='denied');
});
