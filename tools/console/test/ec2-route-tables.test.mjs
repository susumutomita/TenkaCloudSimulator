import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {parseEC2RouteTables} from '../src/ec2-response.mjs';
import {loadVpcRouteTables, routeTableFailure, vpcRouteScope, vpcRouteTablesAvailable} from '../src/ec2-route-tables.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const id=(type,digit)=>type+'-'+digit.repeat(17);
const selected={id:id('vpc','1'),managedBy:'cloudformation'}, other=id('vpc','2');
const tab=id('rtb','3'), gateway=id('igw','4'), subnet=id('subnet','5'), assoc=id('rtbassoc','6');
const n=(localName,content=[],namespaceURI=ns)=>{
  const children=Array.isArray(content)?content:[];
  return {localName,namespaceURI,children,textContent:typeof content==='string'?content:children.map(v=>v.textContent).join('')};
};
const fields=obj=>Object.entries(obj).map(([k,v])=>n(k,v));
const item=obj=>n('item',fields(obj));
const route=(opts={})=>item({destinationCidrBlock:'0.0.0.0/0',gatewayId:gateway,origin:'CreateRoute',state:'active',...opts});
const association=(opts={})=>item({routeTableAssociationId:assoc,routeTableId:tab,subnetId:subnet,main:'false',...opts});
const table=(opts={})=>item({routeTableId:tab,vpcId:selected.id,ownerId:'000000000000',
  routeSet:[route()],associationSet:[association()],tagSet:[item({key:'Name',value:'<saved> & 値'}),item({key:'Empty',value:''})],...opts});
const blank=(opts={})=>table({routeSet:[],associationSet:[],tagSet:[],...opts});
const envelope=(children)=>({documentElement:n('DescribeRouteTablesResponse',children)});
const document=(rows,extra=[])=>envelope([n('requestId','local-request'),n('routeTableSet',rows),...extra]);
const parse=doc=>parseEC2RouteTables(doc,selected.id);
const reply=()=>new Response('captured XML',{headers:{'Content-Type':'text/xml'}});

test('a normal signed-in Console requires the specific native capability; handoffs never qualify',()=>{
  const services={ec2:{console:true,routeTableReadback:'DescribeRouteTables'}};
  for(const selection of [true,false]) assert.equal(vpcRouteTablesAvailable({services,identitySelection:selection},true),true);
  for(const scope of [{instanceId:'i-bound'},{parameterName:'/only'}]) assert.equal(vpcRouteTablesAvailable({services,consoleScope:scope},true),false);
  for(const value of [true,'DescribeSecurityGroups',undefined]) assert.equal(vpcRouteTablesAvailable({services:{ec2:{console:true,routeTableReadback:value}}},true),false);
  assert.equal(vpcRouteTablesAvailable({services},false),false);
  assert.equal(vpcRouteTablesAvailable({services:{ec2:{...services.ec2,console:false}}},true),false);
  assert.equal(vpcRouteScope(selected),'stack');
  assert.equal(vpcRouteScope({...selected,managedBy:'ec2'}),'native');
  assert.equal(vpcRouteScope({...selected,managedBy:'unknown'}),'unverified');
  assert.equal(vpcRouteScope({...selected,id:'invalid'}),'unverified');
});

test('preserves recorded default, explicit association, bare tables and escaped tags for the exact VPC only',()=>{
  const row={id:tab,vpc:selected.id,routes:[{cidr:'0.0.0.0/0',gateway}],associations:[{id:assoc,subnet}],
    tags:[{key:'Name',value:'<saved> & 値'},{key:'Empty',value:''}]};
  const second=id('rtb','8'), third=id('rtb','9');
  assert.deepEqual(parse(document([table(),blank({routeTableId:second}),blank({routeTableId:third,vpcId:other})])),
    [row,{id:second,vpc:selected.id,routes:[],associations:[],tags:[]}]);
  assert.deepEqual(parse(document([blank({vpcId:other})])),[]);
  assert.deepEqual(parse(document([])),[]);
});

test('rejects malformed, partial, unknown, duplicate and out-of-scope corrupt data without publishing empty or partial rows',()=>{
  for(const doc of [
    {documentElement:n('DescribeRouteTablesResponse',[],'urn:foreign')},
    envelope([n('routeTableSet')]),envelope([n('requestId','x')]),envelope([n('requestId','x'),n('routeTableSet','partial')]),
    document([],[n('nextToken','more')]),document([],[n('routeTableSet')]),document([],[n('routeTableSet',[],'urn:wrong')]),
    document([table({ownerId:'unverified'})]),document([blank(),blank()]),document([blank({routeTableId:'no'})]),
    document([table({routeSet:[route({gatewayId:'local'})]})]),document([table({routeSet:[route({state:'blackhole'})]})]),
    document([table({routeSet:[route({origin:'CreateRouteTable'})]})]),document([table({routeSet:[route({destinationCidrBlock:'10.0.0.7/16'})]})]),
    document([table({routeSet:[route({destinationCidrBlock:'198.51.100.128/25'}),route({destinationCidrBlock:'198.51.100.0/24'})]})]),
    document([table({routeSet:[route(),route({destinationCidrBlock:'198.51.100.0/24',gatewayId:id('igw','7')})]})]),
    document([table({routeSet:[route(),route()]})]),document([table({associationSet:[association({main:'true'})]})]),
    document([table({associationSet:[association({routeTableId:id('rtb','7')})]})]),
    document([table(),table({routeTableId:id('rtb','7'),vpcId:other,associationSet:[association({routeTableId:id('rtb','7')})]})]),
    document([table({tagSet:[item({key:'tag',value:'1'}),item({key:'tag',value:'2'})]})]),
    document([blank({tagSet:[item({key:'missing value'})]})]),document([blank({tagSet:[item({key:'a',value:'値'.repeat(86)})]})]),
    document([blank(),blank({routeTableId:id('rtb','8'),vpcId:other,routeSet:[route({gatewayId:'bad'})]})]),
  ]) assert.throws(()=>parse(doc),/Invalid or incomplete EC2 response/);
});

test('CFN view keeps its single default contract and coexists with multi-route native VPC rows',async()=>{
  const extra={routeTableId:id('rtb','8'),vpcId:other,associationSet:[],
    routeSet:[route(),route({destinationCidrBlock:'198.51.100.0/24'})]};
  const query=async()=>reply();
  const retained=await loadVpcRouteTables(query,'us-east-1',selected,{parse:()=>document([table(),table(extra)])});
  assert.equal(retained.length,1);
  assert.deepEqual(retained[0].routes,[{cidr:'0.0.0.0/0',gateway}]);
  await assert.rejects(loadVpcRouteTables(query,'us-east-1',selected,{parse:()=>document([table({...extra,vpcId:selected.id})])}),/Invalid CloudFormation/);
});

test('uses only fixed regional EC2 Query through the existing signed-in transport; rejects invalid scope without a request',async()=>{
  const controller=new AbortController(), requests=[];
  const fixture=async(...args)=>{requests.push(args);return reply();};
  const rows=await loadVpcRouteTables(fixture,'ap-northeast-1',selected,{signal:controller.signal,parse:()=>document([blank()])});
  assert.equal(rows[0].id,tab);
  const [[path,opts]]=requests;
  assert.equal(requests[0].length,2,'do not pass an identity override');
  assert.equal(path,'/');assert.equal(opts.method,'POST');assert.equal(opts.credentials,'same-origin');
  assert.equal(opts.redirect,'error');assert.equal(opts.signal,controller.signal);
  assert.deepEqual(opts.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':'ap-northeast-1'});
  assert.equal(opts.body.toString(),'Action=DescribeRouteTables&Version=2016-11-15&MaxResults=100');
  for(const vpc of [{...selected,managedBy:'ec2'}, {...selected,id:'bad'}, {...selected,managedBy:'unknown'}])
    await assert.rejects(loadVpcRouteTables(fixture,'ap-northeast-1',vpc),/verified CloudFormation/);
  assert.equal(requests.length,1);
});

test('requests one exact subnet association and refuses an out-of-filter table', async()=>{
  const calls=[], fixture=async(path, options)=>{
    calls.push([path, options]);
    return reply();
  };
  const rows=await loadVpcRouteTables(fixture,'us-east-1',selected,{subnetId:subnet,parse:()=>document([table()])});
  assert.equal(rows[0].id,tab);
  assert.equal(calls[0][0],'/');
  assert.deepEqual(Object.fromEntries(calls[0][1].body),
    {Action:'DescribeRouteTables',Version:'2016-11-15',MaxResults:'100','Filter.1.Name':'association.subnet-id','Filter.1.Value.1':subnet});
  for(const value of ['subnet-*','subnet-1234',subnet+'\n'])
    await assert.rejects(loadVpcRouteTables(()=>assert.fail('invalid input must not query'),'us-east-1',selected,{subnetId:value}),/exact subnet ID/);
  await assert.rejects(loadVpcRouteTables(fixture,'us-east-1',selected,{subnetId:id('subnet','8'),parse:()=>document([table()])}),/Invalid or incomplete EC2 response/);
  // A real match in a different selected VPC is not shown under this VPC.
  assert.deepEqual(await loadVpcRouteTables(fixture,'us-east-1',{...selected,id:other},{subnetId:subnet,parse:()=>document([table()])}),[]);
});

test('refusal, unavailable, aborted responses are not empty success',async()=>{
  for(const msg of ['403: refused','UnauthorizedOperation: denied','Sign in to continue.'])
    await assert.rejects(loadVpcRouteTables(async()=>{throw Error(msg);},'us-east-1',selected), e=>routeTableFailure(e)==='denied');
  await assert.rejects(loadVpcRouteTables(async()=>({ok:false,status:500}),'us-east-1',selected), e=>routeTableFailure(e)==='error');
  const controller=new AbortController(), opts={signal:controller.signal,parse:()=>assert.fail('stale result cannot be parsed')};
  await assert.rejects(loadVpcRouteTables(async()=>{controller.abort();return reply();},'us-east-1',selected,opts),{name:'AbortError'});
  await assert.rejects(loadVpcRouteTables(()=>assert.fail('unmounted pane cannot issue a read'),'us-east-1',selected,opts),{name:'AbortError'});
});

const require=createRequire(import.meta.url), consoleDir=fileURLToPath(new URL('..',import.meta.url));
const {outputFiles}=await build({absWorkingDir:consoleDir,entryPoints:['src/ec2-vpc-route-tables.jsx'],bundle:true,write:false,
  platform:'node',format:'cjs',loader:{'.css':'empty'},external:['react','react-dom','react/jsx-runtime']});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement}=require('react'), {renderToStaticMarkup}=require('react-dom/server');
const readout=(status,tables=[],preferred,subnetId)=>renderToStaticMarkup(createElement(module.exports.RouteTableReadout,{status,tables,preferred,subnetId}));

test('real Cloudscape readout distinguishes refused, loading, scope, empty and bare without a route mutation control',()=>{
  const view=vpc=>renderToStaticMarkup(createElement(module.exports.default,{vpc,region:'us-east-1',request:()=>assert.fail('SSR cannot query')}));
  assert.match(view(selected),/Loading route tables/);assert.match(view(selected),/does not test network reachability/);
  assert.match(view({...selected,managedBy:'ec2'}),/No lookup was made/);
  assert.doesNotMatch(view({...selected,managedBy:'ec2'}),/Refresh route tables/);
  assert.match(view({...selected,managedBy:''}),/Select a verified CloudFormation-managed VPC/);
  assert.match(readout('ready'),/No explicit route tables are recorded/);
  assert.match(readout('ready',[],'',subnet),/No explicit route table is associated with this subnet/);
  assert.equal(readout('editing-filter',[{}]),'');
  assert.match(view(selected),/Filter by subnet ID/);
  assert.doesNotMatch(view({...selected,managedBy:'ec2'}),/Filter by subnet ID/);
  for(const [status,expected] of [['denied',/Route table access denied/],['error',/Could not read route tables/],['loading',/Loading route tables/]]){
    assert.match(readout(status),expected);assert.doesNotMatch(readout(status),/No explicit route tables are recorded/);
  }
  const second=id('rtb','7'), rows=parse(document([table(),blank({routeTableId:second})]));
  const full=readout('ready',rows);
  for(const expected of [subnet,assoc,gateway,tab,'0.0.0.0/0','Explicit subnet associations (not main)','&lt;saved&gt; &amp; 値','(empty)'])
    assert.ok(full.includes(expected),expected);
  assert.doesNotMatch(full,/>Edit<|>Delete<|>Create route</);
  const bare=readout('ready',rows,second);
  assert.match(bare,/No enabled IPv4 default route/);assert.match(bare,/No explicit subnet associations/);assert.match(bare,/No tags are recorded/);
  assert.ok(!bare.includes(gateway));
});
