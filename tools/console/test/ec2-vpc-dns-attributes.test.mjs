import assert from 'node:assert/strict';
import test from 'node:test';
import {loadVpcDNSAttributes,parseVpcDNSAttribute,planVpcDNSChanges,saveVpcDNSAttributes,vpcDNSFailure,
  vpcDNSSettingsAvailable,vpcDNSSettingsWritable,vpcDNSTarget} from '../src/ec2-vpc-dns-attributes.mjs';

const xmlNS='http://ec2.amazonaws.com/doc/2016-11-15/';
const describe='DescribeVpcAttribute', write='ModifyVpcAttribute', region='us-east-1';
const support='enableDnsSupport', hostnames='enableDnsHostnames';
const vpc={id:'vpc-1234567890abcdef1',managedBy:'ec2'};
const other={id:'vpc-1234567890abcdef2',managedBy:'ec2'};
const stack={id:'vpc-1234567890abcdef3',managedBy:'cloudformation'};
const flags=(resolution, names)=>({[support]:resolution,[hostnames]:names});
const element=(localName,contents='',namespaceURI=xmlNS)=>{
  const children=Array.isArray(contents)?contents:[];
  return {localName,namespaceURI,children,childNodes:typeof contents==='string'?[{nodeType:3,textContent:contents}]:children,
    nodeType:1,textContent:typeof contents==='string'?contents:''};
};
const document=(action,items,ns=xmlNS)=>({documentElement:element(action+'Response',[element('requestId','request'),...items],ns)});
const describeDoc=(attribute,value,id=vpc.id)=>document(describe,[element('vpcId',id),element(attribute,[element('value',String(value))])]);
const ack=()=>document(write,[element('return','true')]);
const response=(raw,type='text/xml; charset=utf-8',status=200)=>new Response(raw,{status,headers:{'Content-Type':type}});
const reads=(resolution,names,id=vpc.id)=>[describeDoc(support,resolution,id),describeDoc(hostnames,names,id)];
const list=(...operations)=>{
  const calls=[];
  const request=async(path,init)=>{
    assert.equal(path,'/');
    assert.equal(init.method,'POST');
    assert.equal(init.credentials,'same-origin');
    assert.equal(init.redirect,'error');
    assert.deepEqual(init.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region});
    calls.push(Object.fromEntries(init.body));
    const current=operations[calls.length-1];
    assert.ok(current,'Unexpected network request');
    if(current instanceof Error) throw current;
    return typeof current==='function'?current():response(String(calls.length-1));
  };
  return {calls,request,options:{parse:raw=>operations[Number(raw)]}};
};
const d=(attribute,id=vpc.id)=>({Action:describe,Version:'2016-11-15',VpcId:id,Attribute:attribute});
const m=(parameter,value)=>({Action:write,Version:'2016-11-15',VpcId:vpc.id,[parameter]:String(value)});
const ms=value=>m('EnableDnsSupport.Value',value), mh=value=>m('EnableDnsHostnames.Value',value);
const changed=(attribute,value)=>({attribute,value});
const makeCaps=(...operations)=>({services:{ec2:{console:true,operations}}});

test('ordinary signed-in discovery must advertise selected VPC reads and optionally writes',()=>{
  const both=makeCaps('DescribeVpcs',describe,write);
  assert.equal(vpcDNSSettingsAvailable(both,true),true);
  assert.equal(vpcDNSSettingsWritable(both,true),true);
  assert.equal(vpcDNSSettingsAvailable(makeCaps('DescribeVpcs',describe),true),true);
  assert.equal(vpcDNSSettingsWritable(makeCaps('DescribeVpcs',describe),true),false);
  for (const [caps,auth] of [[both,false],[{...both,consoleScope:{instanceId:'i-local'}},true],
    [makeCaps('DescribeVpcs',write),true],[makeCaps(describe,write),true],
    [{services:{ec2:{console:false,operations:both.services.ec2.operations}}},true]]) {
    assert.equal(vpcDNSSettingsAvailable(caps,auth),false);
    assert.equal(vpcDNSSettingsWritable(caps,auth),false);
  }
  for (const id of ['vpc-1','vpc-1234567890abcdef1\n','vpc-1234567890abcdef1/path']) {
    assert.equal(vpcDNSTarget({...vpc,id}),'');
  }
  assert.equal(vpcDNSTarget({id:vpc.id}),'');
  assert.equal(vpcDNSTarget(vpc),'ec2');
  assert.equal(vpcDNSTarget(stack),'cloudformation');
});

test('AWS Query reply matches action, namespace, one typed value, and the exact VPC',()=>{
  assert.equal(parseVpcDNSAttribute(describeDoc(support,false),vpc.id,support),false);
  assert.equal(parseVpcDNSAttribute(describeDoc(hostnames,true),vpc.id,hostnames),true);
  for (const bad of [describeDoc(support,'TRUE'),describeDoc(support,false,other.id),describeDoc(hostnames,true),
    describeDoc(support,'true\n'),{...describeDoc(support,true),doctype:{}},
    document('Wrong',[element('vpcId',vpc.id),element(support,[element('value','true')])]),
    document(describe,[element('vpcId',vpc.id),element(support,[element('value','true')])],'http://other.invalid/'),
    document(describe,[element('vpcId',vpc.id),element(support,[element('value','true','http://other.invalid/')])]),
    document(describe,[element('vpcId',vpc.id),element(support,[element('value','true'),element('value','false')])]),
    document(describe,[element('vpcId',vpc.id),element(support,[element('value',[element('nested','true')])])]),
    document(describe,[element('vpcId',vpc.id),element(support,[element('value','true')]),element(hostnames,[element('value','false')])]),
    document(describe,[element('vpcId',vpc.id),element(support,[])]),
  ]) assert.throws(()=>parseVpcDNSAttribute(bad,vpc.id,support));
  assert.throws(()=>parseVpcDNSAttribute(describeDoc('enableNetworkAddressUsageMetrics',true),vpc.id,'enableNetworkAddressUsageMetrics'));
});

test('planner does not silently change another flag or turn hostnames on without resolution',()=>{
  assert.deepEqual(planVpcDNSChanges(flags(false,false),flags(true,true)),[changed(support,true),changed(hostnames,true)]);
  assert.deepEqual(planVpcDNSChanges(flags(true,true),flags(false,false)),[changed(hostnames,false),changed(support,false)]);
  assert.deepEqual(planVpcDNSChanges(flags(true,true),flags(false,true)),[changed(support,false)]);
  assert.deepEqual(planVpcDNSChanges(flags(false,true),flags(false,false)),[changed(hostnames,false)]);
  assert.equal(planVpcDNSChanges(flags(false,false),flags(false,true)),null);
  assert.equal(planVpcDNSChanges(flags(false,false),flags(true,'true')),null);
  assert.deepEqual(planVpcDNSChanges(flags(true,false),flags(true,false)),[]);
});

test('both writes use separate calls and are each read back before advancing',async()=>{
  const fx=list(...reads(false,false),ack(),...reads(true,false),ack(),...reads(true,true),
    ack(),...reads(true,false),ack(),...reads(false,false));
  const first=await loadVpcDNSAttributes(fx.request,region,vpc,fx.options);
  assert.equal(Object.isFrozen(first),true);
  assert.deepEqual(first,flags(false,false));
  const enabled=await saveVpcDNSAttributes(fx.request,region,vpc,first,flags(true,true),fx.options);
  assert.deepEqual(enabled,{snapshot:flags(true,true),confirmed:[support,hostnames],failed:null});
  const disabled=await saveVpcDNSAttributes(fx.request,region,vpc,enabled.snapshot,flags(false,false),fx.options);
  assert.deepEqual(disabled,{snapshot:flags(false,false),confirmed:[hostnames,support],failed:null});
  assert.deepEqual(fx.calls,[d(support),d(hostnames),ms(true),d(support),d(hostnames),mh(true),d(support),d(hostnames),
    mh(false),d(support),d(hostnames),ms(false),d(support),d(hostnames)]);
  await assert.rejects(saveVpcDNSAttributes(fx.request,region,vpc,first,flags(true,true),fx.options));
  const noop=await saveVpcDNSAttributes(fx.request,region,vpc,disabled.snapshot,flags(false,false),fx.options);
  assert.deepEqual(noop,{snapshot:flags(false,false),confirmed:[],failed:null});
  assert.equal(fx.calls.length,14);
});

test('cannot spend a snapshot for another region, VPC, owner or principal transport; stack reads remain real',async()=>{
  const fx=list(...reads(true,false),...reads(false,true,stack.id));
  const baseline=await loadVpcDNSAttributes(fx.request,region,vpc,fx.options);
  const blocked=()=>assert.fail('invalid scope reached the transport');
  for (const [request,r,target,saved] of [
    [blocked,region,vpc,baseline],[fx.request,'us-west-2',vpc,baseline],[fx.request,region,other,baseline],
    [fx.request,region,{...vpc,managedBy:'unconfirmed'},baseline],[fx.request,region,vpc,{...baseline}],
  ]) await assert.rejects(saveVpcDNSAttributes(request,r,target,saved,flags(true,true),fx.options));
  await assert.rejects(loadVpcDNSAttributes(blocked,'us-east-1\n',vpc,fx.options));
  const stored=await loadVpcDNSAttributes(fx.request,region,stack,fx.options);
  assert.deepEqual(stored,flags(false,true));
  await assert.rejects(saveVpcDNSAttributes(fx.request,region,stack,stored,flags(true,true),fx.options));
  assert.deepEqual(fx.calls,[d(support),d(hostnames),d(support,stack.id),d(hostnames,stack.id)]);
});

test('one acknowledgement followed by a refused second write keeps verified values and reports partial completion',async()=>{
  const denied=Error('UnauthorizedOperation: simulated role lacks write permission');
  const fx=list(...reads(false,false),ack(),...reads(true,false),denied,...reads(true,false));
  const start=await loadVpcDNSAttributes(fx.request,region,vpc,fx.options);
  assert.deepEqual(await saveVpcDNSAttributes(fx.request,region,vpc,start,flags(true,true),fx.options),{
    snapshot:flags(true,false),confirmed:[support],failed:{attribute:hostnames,phase:'request',reason:'denied'},
  });
  assert.deepEqual(fx.calls,[d(support),d(hostnames),ms(true),d(support),d(hostnames),mh(true),d(support),d(hostnames)]);
  assert.equal(vpcDNSFailure(Error('InvalidVpcID.NotFound: no local resource')),'missing');
});

test('failed receipt or unexpected readback never triggers a second write or treats uncertain completion as success',async()=>{
  const cases=[
    [document(write,[element('return','false')]),[],...reads(true,false)],
    [Error('connection reset'),[],...reads(true,false)],
    [ack(),reads(false,false),...reads(false,false)],
    [ack(),[Error('403: denied')],Error('403: denied')],
  ];
  for (const [reply,during,...recovery] of cases) {
    const fx=list(...reads(false,false),reply,...during,...recovery);
    const start=await loadVpcDNSAttributes(fx.request,region,vpc,fx.options);
    const result=await saveVpcDNSAttributes(fx.request,region,vpc,start,flags(true,true),fx.options);
    assert.ok(result.failed);
    assert.equal(result.failed.attribute,support);
    assert.deepEqual(result.confirmed,[]);
    assert.deepEqual(fx.calls.slice(0,3),[d(support),d(hostnames),ms(true)]);
    assert.ok(fx.calls.slice(3).every(call=>call.Action===describe));
    if(recovery[0] instanceof Error) assert.equal(result.snapshot,null);
    else assert.equal(result.snapshot[hostnames],false);
  }
});

test('a concurrent changed flag is displayed, not reported as a completed pair',async()=>{
  const fx=list(...reads(false,false),ack(),...reads(true,false),ack(),...reads(false,true));
  const start=await loadVpcDNSAttributes(fx.request,region,vpc,fx.options);
  assert.deepEqual(await saveVpcDNSAttributes(fx.request,region,vpc,start,flags(true,true),fx.options),{
    snapshot:flags(false,true),confirmed:[support,hostnames],failed:{attribute:support,phase:'readback',reason:'changed'},
  });
});

test('abort, HTML and over-budget replies do not trigger speculative writes or parse stale state',async()=>{
  for (const fake of [()=>response('html','text/html'),()=>response('x'.repeat(8193))]) {
    const fx=list(fake);
    await assert.rejects(loadVpcDNSAttributes(fx.request,region,vpc,{parse:()=>assert.fail('untrusted body parsed')}));
    assert.deepEqual(fx.calls,[d(support)]);
  }
  const controller=new AbortController(), fx=list(...reads(false,false),()=>{
    controller.abort();
    return response('unreachable');
  });
  const start=await loadVpcDNSAttributes(fx.request,region,vpc,fx.options);
  await assert.rejects(saveVpcDNSAttributes(fx.request,region,vpc,start,flags(true,true),{...fx.options,signal:controller.signal}),
    {name:'AbortError'});
  assert.deepEqual(fx.calls,[d(support),d(hostnames),ms(true)]);
});
