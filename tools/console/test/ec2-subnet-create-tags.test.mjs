import assert from 'node:assert/strict';
import test from 'node:test';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import {build} from 'esbuild';
import {nativeSubnetTagDraft} from '../src/ec2-subnet-tag-draft.mjs';
import {createVpcNativeSubnet,loadRegionalSubnetInventory,loadVpcSubnets,nativeSubnetFailure,
  vpcNativeSubnetTagsOnCreateAvailable} from '../src/ec2-native-subnets.mjs';

const region='us-east-1', ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const lifecycle=['DescribeSubnets','CreateSubnet','DeleteSubnet'];
const ec2={console:true,subnetLifecycle:lifecycle,operations:lifecycle,nativeSubnetTagsOnCreate:true};
const capability={services:{ec2}}, vpc={id:'vpc-1234567890abcdef0',cidr:'10.9.0.0/16',managedBy:'ec2'};
const stack={id:'vpc-2234567890abcdef0',cidr:'10.8.0.0/16',managedBy:'cloudformation'};
const saved={id:'subnet-1234567890abcdef0',cidr:'10.9.1.0/24',az:region+'b',owner:'ec2',vpc:vpc.id,publicIPv4:false};
const cfn={id:'subnet-2234567890abcdef0',cidr:'10.8.1.0/24',az:region+'a',owner:'cloudformation',vpc:stack.id};
const name={key:'Name',value:'保存 & <東京>'}, extra={key:'Project',value:'training'}, empty={key:'Stage',value:''};
const intent=[name,extra,empty];
const config=(parse,opts={})=>({parse,publicIPv4:true,tags:intent,capabilities:capability,authenticationRequired:true,...opts});
const field=(localName,data='')=>({localName,namespaceURI:ns,children:Array.isArray(data)?data:[],textContent:typeof data==='string'?data:''});
const envelope=(action,member,items)=>({documentElement:field(action+'Response',[field('requestId','test'),field(member,items)])});
const tags=(pairs=[])=>field('tagSet',pairs.map(({key,value})=>field('item',[field('key',key),field('value',value)])));
const subnet=(row,labels)=>[field('subnetId',row.id),field('vpcId',row.vpc),field('cidrBlock',row.cidr),
  field('state','available'),field('availabilityZone',row.az),...(row.owner==='ec2'?
    [field('simulatorManagedBy','ec2'),field('mapPublicIpOnLaunch',String(row.publicIPv4))]:[]),tags(labels)];
const network=row=>field('item',[field('vpcId',row.id),field('cidrBlock',row.cidr),field('simulatorManagedBy',row.managedBy)]);
const receipt=(labels,row=saved)=>envelope('CreateSubnet','subnet',subnet(row,labels));
const described=(...rows)=>envelope('DescribeSubnets','subnetSet',rows.map(([row,labels])=>field('item',subnet(row,labels))));
const fixture=(...docs)=>{
  const calls=[];
  return {calls,request:async(path,init)=>{
    assert.equal(path,'/');assert.equal(init.method,'POST');assert.equal(init.redirect,'error');
    assert.equal(init.credentials,'same-origin');
    assert.equal(init.headers['X-Simulator-Region'],region);
    assert.equal(init.headers['X-Simulator-Service'],'ec2');
    const index=calls.length, output=docs[index];
    calls.push(Object.fromEntries(init.body));
    assert.ok(output!==undefined,'Unexpected native operation');
    return new Response(String(index),{status:typeof output==='number'?output:200,headers:{'Content-Type':'text/xml; charset=utf-8'}});
  },parse:text=>docs[Number(text)]};
};
const expectedWrite={Action:'CreateSubnet',Version:'2016-11-15',VpcId:vpc.id,CidrBlock:saved.cidr,AvailabilityZone:saved.az};
const expectedRead={Action:'DescribeSubnets',Version:'2016-11-15'};

test('tag-on-create is an exact authenticated capability, not a general tag permission or truthy setting',()=>{
  assert.equal(vpcNativeSubnetTagsOnCreateAvailable(capability,true),true);
  for (const [cap,auth] of [[capability,false],[capability,undefined],[capability,'true'],[{...capability,consoleScope:{}},true],
    ...[{}, {nativeSubnetTagsOnCreate:'true'}, {nativeSubnetTagsOnCreate:false}, {operations:['CreateSubnet']},
      {operations:['DescribeSubnets']},{console:false}].map(field=>[{services:{ec2:{...ec2,nativeSubnetTagsOnCreate:undefined,...field}}},true])]) {
    assert.equal(vpcNativeSubnetTagsOnCreateAvailable(cap,auth),false);
  }
  for(const operations of [['CreateSubnet'],['DescribeSubnets']]) {
    assert.equal(vpcNativeSubnetTagsOnCreateAvailable({services:{ec2:{...ec2,operations}}},true),false);
  }
});

test('optional Name and actual populated rows alone form the tag set; byte limits and key collisions are local errors',()=>{
  const draft=nativeSubnetTagDraft(name.value,[{key:'',value:''},extra,empty]);
  assert.equal(draft.valid,true);assert.deepEqual(draft.tags,intent);
  assert.deepEqual(nativeSubnetTagDraft('',[{key:'',value:''}]).tags,[]);
  assert.equal(nativeSubnetTagDraft('',[empty]).valid,true);
  const rows=Array.from({length:50},(_,i)=>({key:'Key'+i,value:''}));
  assert.equal(nativeSubnetTagDraft('',rows).valid,true);
  assert.equal(nativeSubnetTagDraft('name',rows).overLimit,true);
  assert.equal(nativeSubnetTagDraft('界'.repeat(85)+'a',[]).valid,true);
  assert.equal(nativeSubnetTagDraft('界'.repeat(86),[]).nameError,true);
  assert.equal(nativeSubnetTagDraft('',[{key:'界'.repeat(42)+'ab',value:''}]).valid,true);
  assert.equal(nativeSubnetTagDraft('',[{key:'界'.repeat(43),value:''}]).valid,false);
  assert.equal(nativeSubnetTagDraft('',[{key:'only-value',value:'v'},{key:'ONLY-VALUE',value:''}]).valid,true);
  assert.equal(nativeSubnetTagDraft('',[extra,extra]).errors[1].key,'duplicate');
  assert.equal(nativeSubnetTagDraft('',[{key:'Name',value:'duplicate entry'}]).errors[0].key,'name');
  assert.equal(nativeSubnetTagDraft('',[{key:'',value:'only-value'}]).errors[0].key,'invalid');
  assert.equal(nativeSubnetTagDraft('',[{key:'AWS:Owner',value:'x'}]).valid,false);
  assert.equal(nativeSubnetTagDraft('',[{key:'x',value:'\u0000'}]).valid,false);
});

test('one tagged CreateSubnet and one readback keep the exact set, regardless of server order',async()=>{
  const f=fixture(receipt([extra,name,empty]),described([saved,[empty,name,extra]],[cfn,[{key:'Name',value:'Stack Name'}]]));
  const result=await createVpcNativeSubnet(f.request,region,vpc,'10.9.1.7/24',saved.az,config(f.parse));
  assert.equal(result.created.id,saved.id);
  assert.deepEqual(result.created.tags,[extra,name,empty]);
  assert.deepEqual(result.rows[0].tags,[empty,name,extra]);
  assert.deepEqual(f.calls,[{
    ...expectedWrite,'TagSpecification.1.ResourceType':'subnet',
    'TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name.value,
    'TagSpecification.1.Tag.2.Key':'Project','TagSpecification.1.Tag.2.Value':extra.value,
    'TagSpecification.1.Tag.3.Key':'Stage','TagSpecification.1.Tag.3.Value':'',
  },expectedRead]);
});

test('an empty tag draft omits TagSpecification, and unsupported/invalid/stack inputs never reach transport',async()=>{
  for(const auth of [true,false]) {
    const f=fixture(receipt([]),described([saved,[]]));
    await createVpcNativeSubnet(f.request,region,vpc,saved.cidr,saved.az,config(f.parse,{authenticationRequired:auth,tags:[]}));
    assert.deepEqual(f.calls,[expectedWrite,expectedRead]);
  }
  const never=()=>assert.fail('unsupported input reached transport');
  for(const opts of [{capabilities:undefined},{authenticationRequired:false},{tags:[...intent,name]},
    {tags:[{key:'aws:fake',value:''}]},{tags:[{key:'Key',value:'x'.repeat(257)}]},
    {tags:[{key:'Key',value:'\ud800'}]},{tags:Array(1)},{tags:null}]) {
    await assert.rejects(createVpcNativeSubnet(never,region,vpc,saved.cidr,saved.az,config(undefined,opts)),/Invalid subnet/);
  }
  await assert.rejects(createVpcNativeSubnet(never,region,stack,cfn.cidr,cfn.az,config(undefined)),/Invalid subnet/);
});

test('a denial or extra, missing, duplicate or changed tags leave the write unconfirmed, never retrying untagged',async()=>{
  const f=fixture(403);
  await assert.rejects(createVpcNativeSubnet(f.request,region,vpc,saved.cidr,saved.az,config(f.parse)),e=>nativeSubnetFailure(e)==='denied');
  assert.deepEqual(f.calls.map(call=>call.Action),['CreateSubnet']);
  for(const [docs,count] of [
    [[receipt([name,extra])],1],
    [[receipt([...intent,{key:'Extra',value:'surprise'}])],1],
    [[receipt([...intent,name])],1],
    [[receipt(intent),described([saved,[name,{...extra,value:'different'},empty]])],2],
    [[receipt(intent),described([saved,[name,extra]])],2],
    [[receipt(intent),described([{...saved,id:'subnet-3234567890abcdef0'},intent])],2],
    [[receipt(intent),described([{...saved,vpc:stack.id},intent])],2],
  ]) {
    const f=fixture(...docs);
    await assert.rejects(createVpcNativeSubnet(f.request,region,vpc,saved.cidr,saved.az,config(f.parse)),e=>nativeSubnetFailure(e)==='unconfirmed');
    assert.deepEqual(f.calls.map(call=>call.Action),['CreateSubnet','DescribeSubnets'].slice(0,count));
  }

  const surprising=fixture(receipt([name]));
  await assert.rejects(createVpcNativeSubnet(surprising.request,region,vpc,saved.cidr,saved.az,config(surprising.parse,{tags:[]})),/Invalid subnet/);
  assert.deepEqual(surprising.calls,[expectedWrite]);
});

test('existing read views accept verified native tag metadata without requiring creation rights or reclassifying stack resources',async()=>{
  const rows=described([saved,intent],[cfn,[{key:'Name',value:'Stack Name'}]]);
  const f=fixture(rows,envelope('DescribeVpcs','vpcSet',[network(vpc),network(stack)]),rows);
  const [one]=await loadVpcSubnets(f.request,region,vpc,{parse:f.parse,publicIPv4:true});
  assert.deepEqual(one.tags,intent);
  assert.equal(Object.isFrozen(one.tags),true);
  const {subnets}=await loadRegionalSubnetInventory(f.request,region,{parse:f.parse,publicIPv4:true});
  assert.deepEqual(subnets.map(s=>[s.owner,s.name]),[['ec2',name.value],['cloudformation','Stack Name']]);
  assert.deepEqual(subnets[0].tags,intent);
  assert.deepEqual(f.calls.map(call=>call.Action),['DescribeSubnets','DescribeVpcs','DescribeSubnets']);
});

const require=createRequire(import.meta.url);
const dir=fileURLToPath(new URL('..',import.meta.url));
const {outputFiles}=await build({absWorkingDir:dir,
  stdin:{contents:"export {default} from './src/ec2-subnets-console.jsx';",resolveDir:dir},
  bundle:true,write:false,platform:'node',format:'cjs',loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'],define:{'process.env.NODE_ENV':'"production"'},
});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement}=require('react'),{renderToStaticMarkup}=require('react-dom/server');

test('the actual create page exposes Name/tag fields only in the advertised session and leaves unverified parents locked',()=>{
  const original=globalThis.location;
  globalThis.location={hash:'#/vpc/subnets/create?region='+region};
  try {
    const view=(capability,authenticationRequired,canCreate=true)=>renderToStaticMarkup(createElement(module.exports.default,
      {route:{target:'create',vpc:''},region,capabilities:capability,authenticationRequired,canCreate,
        request:()=>assert.fail('Static rendering must not send requests')}));
    for (const [cap,auth] of [[capability,false],[undefined,true],[{...capability,consoleScope:{}},true]]) {
      const html=view(cap,auth);
      assert.match(html,/Subnet name - optional/);
      assert.match(html,/does not support naming or tagging new subnets/);
      assert.doesNotMatch(html,/Add new tag/);
    }
    const html=view(capability,true);
    assert.match(html,/Saved as the Name tag/);
    assert.match(html,/Add new tag/);
    assert.match(html,/Up to 50 tags, including Name/);
    const add = [...html.matchAll(/<button\b([^>]*)>(.*?)<\/button>/gs)].find(button=>button[2].includes('Add new tag'));
    assert.ok(add,'missing add-tag button');
    assert.match(add[1],/\baria-disabled="true"/);
    assert.doesNotMatch(view(capability,true,false),/Add new tag|Subnet name - optional/);
  } finally {
    if (original===undefined) delete globalThis.location;
    else globalThis.location=original;
  }
});
