import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {createVpcNativeRouteTable,deleteVpcNativeRouteTable,loadVpcNativeRouteTables,nativeRouteTableFailure,
  nativeRouteTableTarget,validNativeRouteTableName,vpcNativeRouteTablesAvailable} from '../src/ec2-native-route-tables.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const id=(prefix,digit)=>prefix+'-'+digit.repeat(17);
const vpc={id:id('vpc','1'),managedBy:'ec2'}, other={id:id('vpc','2'),managedBy:'ec2'}, stack={id:id('vpc','3'),managedBy:'cloudformation'};
const tableID=id('rtb','4'), sibling=id('rtb','5'), cfnID=id('rtb','6'), region='ap-northeast-1', token='receipt-local-01';
const lifecycle=['DescribeRouteTables','CreateRouteTable','DeleteRouteTable'];
const label='構成 <保存> & exact';
const failure=/Invalid or incomplete detached route table response/;
const node=(localName,content=[],namespaceURI=ns)=>{
  const children=Array.isArray(content)?content:[];
  const textContent=typeof content==='string'?content:children.map(c=>c.textContent).join('');
  const childNodes=children.length?children:textContent?[{nodeType:3,textContent}]:[];
  return {localName,namespaceURI,children,textContent,nodeType:1,childNodes};
};
const fields=object=>Object.entries(object).map(([k,v])=>node(k,v));
const row=(overrides={})=>({routeTableId:tableID,vpcId:vpc.id,ownerId:'000000000000',routeSet:[],associationSet:[],tagSet:[],...overrides});
const tag=(key,value)=>node('item',fields({key,value}));
const describe=(tables=[],extra={})=>({documentElement:node('DescribeRouteTablesResponse',fields({requestId:'read',routeTableSet:[...tables].sort((a,b)=>a.routeTableId.localeCompare(b.routeTableId)).map(t=>node('item',fields(t))),...extra}))});
const create=(table=row(),extra={})=>({documentElement:node('CreateRouteTableResponse',fields({requestId:'created',routeTable:fields(table),clientToken:token,...extra}))});
const remove=value=>({documentElement:node('DeleteRouteTableResponse',fields({requestId:'deleted',return:value}))});
const defaultRoute=node('item',fields({destinationCidrBlock:'0.0.0.0/0',gatewayId:id('igw','7'),origin:'CreateRoute',state:'active'}));
const association=node('item',fields({routeTableAssociationId:id('rtbassoc','7'),routeTableId:cfnID,subnetId:id('subnet','8'),main:'false'}));
const cfnRow=row({routeTableId:cfnID,vpcId:stack.id,routeSet:[defaultRoute],associationSet:[association]});
const response=(body='encoded',type='text/xml; charset=utf-8')=>new Response(body,{headers:{'Content-Type':type}});

function queryFixture(replies,expectedRegion=region) {
  const calls=[];
  async function request(...args) {
    assert.equal(args.length,2,'must not override the selected identity');
    const [path,opts]=args;
    assert.equal(path,'/');assert.equal(opts.method,'POST');assert.equal(opts.credentials,'same-origin');assert.equal(opts.redirect,'error');
    assert.deepEqual(opts.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':expectedRegion});
    const body=Object.fromEntries(opts.body);
    calls.push({body,signal:opts.signal});
    assert.ok(calls.length<=replies.length,'unexpected Query operation');
    return response(String(calls.length-1));
  }
  return {calls,request,parse:index=>replies[Number(index)]};
}

test('detached lifecycle is available only with authenticated, unscoped, fully declared native capability',()=>{
  const ec2={console:true,routeTableReadback:'DescribeRouteTables',operations:[...lifecycle,'DescribeVpcs'],routeTableLifecycle:lifecycle,
    detachedConfigurationOnly:['native route tables']};
  const available=(value=ec2,extra={})=>vpcNativeRouteTablesAvailable({services:{ec2:value},...extra},true);
  assert.equal(available(),true);
  assert.equal(vpcNativeRouteTablesAvailable(null,true),false);
  assert.equal(vpcNativeRouteTablesAvailable({services:{ec2}},false),false);
  assert.equal(available(ec2,{consoleScope:{instanceId:'local'}}),false);
  for(const change of [{console:false},{routeTableReadback:true},{operations:lifecycle.slice(0,2)},{routeTableLifecycle:undefined},
    {routeTableLifecycle:[...lifecycle,'AssociateRouteTable']},{routeTableLifecycle:[]},{detachedConfigurationOnly:['native network ACLs']}])
    assert.equal(available({...ec2,...change}),false);
  for(const invalid of [stack,{...vpc,id:vpc.id+'\n'},{...vpc,id:id('vpc','1').substring(0,12)},null])
    assert.equal(nativeRouteTableTarget(invalid),false);
  assert.equal(nativeRouteTableTarget(vpc),true);
  for(const valid of ['',label,'a'.repeat(256),'💧'.repeat(64),'TAB\t LF\n CR\r']) assert.equal(validNativeRouteTableName(valid),true);
  for(const invalid of ['a'.repeat(257),'💧'.repeat(65),'bad\0','bad\u000b','bad\ud800','bad\uffff',null]) assert.equal(validNativeRouteTableName(invalid),false);
});

test('loads only the exact detached VPC from a validated complete mixed regional snapshot',async()=>{
  const f=queryFixture([describe([cfnRow,row({tagSet:[tag('Name',label)]}),row({routeTableId:sibling,vpcId:other.id})])]);
  const controller=new AbortController();
  const rows=await loadVpcNativeRouteTables(f.request,region,vpc,{parse:f.parse,signal:controller.signal});
  assert.deepEqual(rows,[{id:tableID,vpc:vpc.id,name:label}]);
  assert.equal(Object.isFrozen(rows[0]),true);
  assert.deepEqual(f.calls,[{body:{Action:'DescribeRouteTables',Version:'2016-11-15',MaxResults:'100'},signal:controller.signal}]);
  const empty=queryFixture([describe([cfnRow,row({routeTableId:sibling,vpcId:other.id})])]);
  assert.deepEqual(await loadVpcNativeRouteTables(empty.request,region,vpc,empty),[]);
  for(const doc of [
    describe([row({routeSet:[defaultRoute]})]),
    describe([row(),row({vpcId:other.id})]),
    describe([row(),{...cfnRow,associationSet:[node('item',fields({main:'true'}))]}]),
    describe([row({routeTableId:tableID+'\n'})]),
    describe([],{nextToken:'partial'}),
    {...describe([]),doctype:{}},
    {documentElement:node('DescribeRouteTablesResponse',[],'urn:foreign')},
  ]) {
    const invalid=queryFixture([doc]);
    await assert.rejects(loadVpcNativeRouteTables(invalid.request,region,vpc,invalid));
  }
});

test('creates only in a verified native VPC and confirms exact allocated ID and tag via a new readback',async()=>{
  const named=row({tagSet:[tag('Name',label)]});
  for(const [input,wire] of [[label,named],['',row()]]) {
    const f=queryFixture([create(wire),describe([cfnRow,wire])]);
    const result=await createVpcNativeRouteTable(f.request,region,vpc,input,token,f);
    assert.equal(result.created,result.rows[0]);
    assert.deepEqual(result.created,{id:tableID,vpc:vpc.id,name:input});
    assert.deepEqual(f.calls.map(x=>x.body),[
      {Action:'CreateRouteTable',Version:'2016-11-15',VpcId:vpc.id,ClientToken:token,
        ...(input?{'TagSpecification.1.ResourceType':'route-table','TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':input}:{})},
      {Action:'DescribeRouteTables',Version:'2016-11-15',MaxResults:'100'},
    ]);
  }
  const absent=()=>assert.fail('invalid target cannot send a request');
  for(const [target,input,tok,loc] of [[stack,label,token,region],[vpc,'bad\0',token,region],[vpc,label,'t\n',region],[vpc,label,token,region+'\n']])
    await assert.rejects(createVpcNativeRouteTable(absent,loc,target,input,tok),failure);
  for(const [immediate,list] of [
    [create(named,{clientToken:'different'}),describe([named])],
    [create({...named,vpcId:other.id}),describe([named])],
    [create({...named,routeSet:[defaultRoute]}),describe([named])],
    [create(named),describe([{...named,vpcId:other.id}])],
    [create(named),describe([{...named,routeTableId:sibling}])],
    [create(named),describe([{...named,tagSet:[tag('Name',label),tag('Fake','unexpected')]}])],
  ]) {
    const bad=queryFixture([immediate,list]);
    await assert.rejects(createVpcNativeRouteTable(bad.request,region,vpc,label,token,bad),failure);
  }
});

test('deletes only a row verified in the same identity/region/VPC and confirms regional absence',async()=>{
  const f=queryFixture([describe([row(),cfnRow]),remove('true'),describe([cfnRow,row({routeTableId:sibling})])]);
  const [target]=await loadVpcNativeRouteTables(f.request,region,vpc,f);
  for(const [request,loc,selected,wrong] of [
    [f.request,region,vpc,{...target}],[f.request,region,other,target],[f.request,'us-west-2',vpc,target],
    [()=>assert.fail('stale identity cannot mutate'),region,vpc,target],[f.request,region,stack,target],
  ]) await assert.rejects(deleteVpcNativeRouteTable(request,loc,selected,wrong,f),failure);
  assert.equal(f.calls.length,1);
  const result=await deleteVpcNativeRouteTable(f.request,region,vpc,target,f);
  assert.equal(result.removed,target);assert.deepEqual(result.rows.map(x=>x.id),[sibling]);
  assert.deepEqual(f.calls.slice(1).map(x=>x.body),[{Action:'DeleteRouteTable',Version:'2016-11-15',RouteTableId:tableID},
    {Action:'DescribeRouteTables',Version:'2016-11-15',MaxResults:'100'}]);
  await assert.rejects(deleteVpcNativeRouteTable(f.request,region,vpc,target,f),failure);
  for(const [receipt,list] of [[remove('false'),describe([])],[remove('true'),describe([row()])],
    [remove('true'),describe([row({vpcId:other.id})])]]) {
    const bad=queryFixture([describe([row()]),receipt,list]);
    const [seen]=await loadVpcNativeRouteTables(bad.request,region,vpc,bad);
    await assert.rejects(deleteVpcNativeRouteTable(bad.request,region,vpc,seen,bad),failure);
  }
});

test('refused, disrupted, oversized, wrong-content and aborted reads cannot produce a successful empty view',async()=>{
  for(const [message,status] of [['403: denied','denied'],['Sign in to continue.','denied'],['UnauthorizedOperation: denied','denied'],
    ['InvalidRouteTableID.NotFound: invalid','not-found'],['InvalidVpcID.NotFound: invalid','not-found'],
    ['DependencyViolation: busy','dependency'],['RouteTableLimitExceeded: full','limit'],['500: unavailable','unconfirmed']])
    await assert.rejects(loadVpcNativeRouteTables(async()=>{throw Error(message);},region,vpc),e=>nativeRouteTableFailure(e)===status);
  for(const reply of [()=>response('not-xml','text/html'),()=>response(new Uint8Array(16*1024*1024+1)),
    ()=>response(new Uint8Array([0xc0,0x80])),()=>({ok:true,headers:new Headers({'Content-Type':'text/xml'})})])
    await assert.rejects(loadVpcNativeRouteTables(reply,region,vpc,{parse:()=>assert.fail('bad body cannot be parsed')}));
  const controller=new AbortController();
  const options={signal:controller.signal,parse:()=>assert.fail('aborted stream cannot be parsed')};
  let cancelled=false;
  const aborted=async()=>response(new ReadableStream({
    pull(c){c.enqueue(new Uint8Array([65]));controller.abort();},
    cancel(){cancelled=true;},
  },{highWaterMark:0}));
  await assert.rejects(loadVpcNativeRouteTables(aborted,region,vpc,options),{name:'AbortError'});
  assert.equal(cancelled,true);
  await assert.rejects(loadVpcNativeRouteTables(()=>assert.fail('already aborted'),region,vpc,options),{name:'AbortError'});
});

const require=createRequire(import.meta.url);
const consoleDir=fileURLToPath(new URL('..',import.meta.url));
const {outputFiles}=await build({absWorkingDir:consoleDir,entryPoints:['src/ec2-vpc-native-route-tables.jsx'],bundle:true,write:false,
  platform:'node',format:'cjs',loader:{'.css':'empty'},external:['react','react-dom','react/jsx-runtime']});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement}=require('react'),{renderToStaticMarkup}=require('react-dom/server');

test('actual Cloudscape is native only, never shows unverified data or offers an attachment/routing action on mount',()=>{
  const view=target=>renderToStaticMarkup(createElement(module.exports.default,
    {request:()=>assert.fail('SSR cannot query'),region,vpc:target,acquireWrite:()=>assert.fail('SSR cannot write')}));
  for(const invalid of [undefined,stack,{...vpc,managedBy:'unverified'}]) assert.equal(view(invalid),'');
  const content=view(vpc);
  assert.match(content,/>Reading route tables</);
  assert.match(content,/no routes or subnet associations and do not forward traffic/);
  assert.match(content,/disabled=""[^>]*>(?:<span[^>]*>)*Create route table</);
  assert.doesNotMatch(content,/>Associate |>[Ee]dit |>[Cc]reate route<|No detached route tables are saved/);
});
