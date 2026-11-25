import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {parseEC2SecurityGroups} from '../src/ec2-response.mjs';
import {loadVpcSecurityGroups, vpcSecurityGroupsAvailable, vpcSecurityGroupsFailure} from '../src/ec2-security-groups.mjs';

const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const id = digit => 'vpc-' + digit.repeat(17);
const groupId = digit => 'sg-' + digit.repeat(17);
const selected = {id:id('a'), managedBy:'cloudformation'};
const ignored = {id:id('b'), managedBy:'cloudformation'};
function node(localName, content=[], namespaceURI=ns) {
  const children = Array.isArray(content) ? content : [];
  return {localName,namespaceURI,children,textContent:Array.isArray(content) ? content.map(n=>n.textContent).join('') : content};
}
const tag = (key,value) => node('item',[node('key',key),node('value',value)]);
const inbound = (cidr='192.0.2.128/25',from='8000',to='8001',protocol='tcp') => node('item',[
  node('ipProtocol',protocol),node('fromPort',from),node('toPort',to),node('ipRanges',[node('item',[node('cidrIp',cidr)])]),
]);
const row = ({group=groupId('c'),vpc=selected.id, description='Web <local> & 学習', rules=[inbound()], tags=[tag('A','\r\n\t値'),tag('Z','')], extra=[]}={}) => node('item',[
  node('ownerId','000000000000'),node('groupId',group),node('groupDescription',description),node('vpcId',vpc),
  node('ipPermissions',rules),...(tags===null?[]:[node('tagSet',tags)]),...extra,
]);
const response = (rows=[],extra=[]) => ({documentElement:node('DescribeSecurityGroupsResponse',[
  node('requestId','ec2-local-0000000000000001'),node('securityGroupInfo',rows),...extra,
])});

test('capability is an exact signed-in readback advertisement, not a general EC2 flag', () => {
  const advertised = {services:{ec2:{console:true,securityGroupReadback:'DescribeSecurityGroups'}}};
  assert.equal(vpcSecurityGroupsAvailable({...advertised,identitySelection:false},true),true);
  assert.equal(vpcSecurityGroupsAvailable({...advertised,identitySelection:true},true),true);
  assert.equal(vpcSecurityGroupsAvailable(advertised,false),false);
  assert.equal(vpcSecurityGroupsAvailable({...advertised,consoleScope:{instanceId:'i-bound'}},true),false);
  assert.equal(vpcSecurityGroupsAvailable({...advertised,consoleScope:{parameterName:'/bound'}},true),false);
  for (const ec2 of [true,{console:true},{console:false,securityGroupReadback:'DescribeSecurityGroups'},
    {console:true,securityGroupReadback:true},{console:true,securityGroupReadback:'RunInstances'}]) {
    assert.equal(vpcSecurityGroupsAvailable({services:{ec2}},true),false);
  }
});

test('native projection filters exact selected VPC and preserves persisted TCP rules and empty tags', () => {
  const doc = response([row({}),row({group:groupId('d'),vpc:ignored.id,description:'Not this VPC'}),
    row({group:groupId('e'),description:'No rules',rules:[],tags:null})]);
  assert.deepEqual(parseEC2SecurityGroups(doc, selected.id),[
    {id:groupId('c'),vpc:selected.id,description:'Web <local> & 学習',ingress:[{from:8000,to:8001,cidr:'192.0.2.128/25'}],
      tags:[{key:'A',value:'\r\n\t値'},{key:'Z',value:''}]},
    {id:groupId('e'),vpc:selected.id,description:'No rules',ingress:[],tags:[]},
  ]);
  assert.deepEqual(parseEC2SecurityGroups(response([]),selected.id),[]);
  assert.deepEqual(parseEC2SecurityGroups(response([row({vpc:ignored.id})]),selected.id),[]);
  const edges = parseEC2SecurityGroups(response([row({rules:[inbound('0.0.0.0/0','1','1'),inbound('203.0.113.27/32','65535','65535')]})]),selected.id);
  assert.deepEqual(edges[0].ingress,[{from:1,to:1,cidr:'0.0.0.0/0'},{from:65535,to:65535,cidr:'203.0.113.27/32'}]);
});

test('wrong shape, claim or bounds produce errors instead of partial or valid empty responses', () => {
  const idReceipt=node('requestId','ec2-local-request');
  const resultRoot=children=>({documentElement:node('DescribeSecurityGroupsResponse',children)});
  for (const doc of [
    {documentElement:node('DescribeSecurityGroupsResponse',[], 'urn:other')},
    resultRoot([idReceipt]),resultRoot([node('securityGroupInfo')]),
    resultRoot([idReceipt,node('securityGroupInfo','not an empty successful set')]),
    resultRoot([idReceipt,node('securityGroupInfo'),node('securityGroupInfo')]),
    response([], [node('nextToken','truncated')]),response([], [node('vpcSet'),node('reservationSet')]),
    response([row({}),row({vpc:ignored.id})]),response([row({group:'vpc-wrong-id'})]),
    response([row({description:''})]),response([row({extra:[node('ipPermissionsEgress')]})]),
    response([row({tags:[tag('repeat','one'),tag('repeat','two')]})]),
    response([row({tags:[node('item',[node('key','missing-value')]) ]})]),
    response([row({rules:[inbound('192.0.2.129/25')]})]),
    response([row({rules:[inbound('192.0.2.128/25','0','33')]})]),
    response([row({rules:[inbound('192.0.2.128/25','8001','8000')]})]),
    response([row({rules:[inbound('192.0.2.128/25','8000','8000','udp')]})]),
    response([row({rules:Array.from({length:61},()=>inbound())})]),
    response(Array.from({length:1001},()=>row({}))),
  ]) assert.throws(()=>parseEC2SecurityGroups(doc,selected.id),/Invalid or incomplete EC2 response/);
});

test('calls fixed native identity-preserving regional POST, no selected-ID argument or write', async () => {
  const signal = new AbortController().signal, requests=[];
  const fixture=async (path,options) => {
    requests.push([path,options]);
    return {ok:true,text:async()=>response([row({}),row({group:groupId('d'),vpc:ignored.id})])};
  };
  const groups=await loadVpcSecurityGroups(fixture,'us-west-2',selected,{signal,parse:x=>x});
  assert.deepEqual(groups.map(group=>group.id),[groupId('c')]);
  assert.equal(requests.length,1);
  const [path, opts] = requests[0];
  assert.equal(path,'/'); assert.equal(opts.method,'POST');
  assert.equal(opts.redirect,'error'); assert.equal(opts.signal,signal);
  assert.deepEqual(opts.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':'us-west-2'});
  assert.equal(opts.body.toString(),'Action=DescribeSecurityGroups&Version=2016-11-15');
  for (const managedBy of ['ec2','']) await assert.rejects(loadVpcSecurityGroups(fixture,'us-west-2',{...selected, managedBy}),/CloudFormation-managed/);
  assert.equal(requests.length,1);
});

test('denial and malformed responses are not cached as empty; aborted scopes cannot publish a response', async () => {
  await assert.rejects(loadVpcSecurityGroups(async()=>({ok:false,status:403}), 'us-east-1',selected),
    e=>vpcSecurityGroupsFailure(e)==='denied');
  await assert.rejects(loadVpcSecurityGroups(async()=>{throw Error('UnauthorizedOperation: denied');}, 'us-east-1',selected),
    e=>vpcSecurityGroupsFailure(e)==='denied');
  await assert.rejects(loadVpcSecurityGroups(async()=>({ok:true,text:async()=>null}), 'us-east-1',selected,{parse:x=>x}),
    e=>vpcSecurityGroupsFailure(e)==='error');
  const controller=new AbortController();
  await assert.rejects(loadVpcSecurityGroups(async()=>({ok:true,text:async()=>{controller.abort();return response([row({})]);}}),
    'us-east-1',selected,{signal:controller.signal,parse:()=>assert.fail('aborted reply must not be parsed')}),
    {name:'AbortError'});
  await assert.rejects(loadVpcSecurityGroups(async()=>assert.fail('aborted scope cannot issue a call'),
    'us-east-1',selected,{signal:controller.signal}),{name:'AbortError'});
});

const require=createRequire(import.meta.url);
const consoleDir=fileURLToPath(new URL('..',import.meta.url));
const {outputFiles}=await build({
  absWorkingDir:consoleDir,stdin:{contents:"export * from './src/ec2-vpc-security-groups.jsx'; export {default} from './src/ec2-vpc-security-groups.jsx';",resolveDir:consoleDir},
  bundle:true,write:false,platform:'node',format:'cjs',loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'],define:{'process.env.NODE_ENV':'"production"'},
});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement}=require('react'), {renderToStaticMarkup}=require('react-dom/server');

test('real React pane shows progress, a native-managed message, and readable escaped values without edit controls', () => {
  const render=vpc=>renderToStaticMarkup(createElement(module.exports.default,{vpc,region:'us-east-1',
    request:()=>assert.fail('static rendering makes no call')}));
  assert.match(render(selected),/Loading security groups/);
  assert.match(render(selected),/Refresh security groups/);
  assert.match(render({...selected,managedBy:'ec2'}),/EC2-managed VPCs do not have security groups/);
  assert.doesNotMatch(render({...selected,managedBy:'ec2'}),/Loading security groups|Refresh security groups/);
  const details=group=>renderToStaticMarkup(createElement(module.exports.SecurityGroupDetails,{group}));
  const full=details(parseEC2SecurityGroups(response([row({})]),selected.id)[0]);
  assert.match(full,/TCP 8000–8001 · 192\.0\.2\.128\/25/);
  assert.match(full,/Web &lt;local&gt; &amp; 学習/);
  assert.match(full,/\(empty\)/);
  assert.match(full,/Security group tags/);
  assert.doesNotMatch(full,/<input|Delete tag|Edit inbound/);
  const blank=details(parseEC2SecurityGroups(response([row({rules:[],tags:null})]),selected.id)[0]);
  assert.match(blank,/No inbound rules are recorded/);
  assert.match(blank,/No tags are recorded/);
});
