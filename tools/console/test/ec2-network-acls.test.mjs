import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {createVpcNetworkACL,deleteVpcNetworkACL,loadVpcNetworkACLs,networkACLFailure,networkACLTarget,
  parseVpcNetworkACLs,validNetworkACLName,vpcNetworkACLsAvailable} from '../src/ec2-network-acls.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const vpc={id:'vpc-'+'1'.repeat(17),managedBy:'ec2'}, cfg={...vpc,managedBy:'cloudformation'};
const other='vpc-'+'2'.repeat(17), id='acl-'+'3'.repeat(17), region='us-east-1';
const name='R&D 日本語 <demo>', token='0e28c499-3403-4a16-b901-21f81b7d3e87', mode='detached-config-only';
const node=(localName,body=[],namespaceURI=ns)=>({localName,namespaceURI,children:Array.isArray(body)?body:[],textContent:typeof body==='string'?body:''});
const columns=obj=>Object.entries(obj).map(([key,value])=>node(key,value));
const item=obj=>node('item',columns(obj));
const entry=egress=>item({ruleNumber:'32767',protocol:'-1',ruleAction:'deny',cidrBlock:'0.0.0.0/0',egress});
const record=(values={},tag=name)=>columns({networkAclId:id,vpcId:vpc.id,ownerId:'000000000000',default:'false',
  simulatorNetworkAclMode:mode,entrySet:[entry('false'),entry('true')],associationSet:[],
  tagSet:tag?[item({key:'Name',value:tag})]:[],...values});
const doc=(action,members)=>({documentElement:node(action+'Response',[node('requestId','abc'),...members])});
const list=(...rows)=>doc('DescribeNetworkAcls',[node('networkAclSet',rows.map(value=>node('item',value)))]);
const create=(recordValue=record(),echo=token)=>doc('CreateNetworkAcl',[node('networkAcl',recordValue),node('clientToken',echo)]);
const remove=(ack='true')=>doc('DeleteNetworkAcl',[node('return',ack)]);
const xmlResponse=()=>new Response('wire',{status:200,headers:{'content-type':'application/xml; charset=UTF-8'}});
function transport(...answers){
  const requests=[];
  return {requests,request:async(...args)=>{requests.push(args);return xmlResponse()},options:{parse:()=>{
    assert.ok(answers.length,'Only the fixed native write and its required readback are allowed');return answers.shift();
  }}};
}

test('only a signed-in unscoped Console with the actual detached ACL contract can expose the feature',()=>{
  const ec2={console:true,operations:['DescribeNetworkAcls','CreateNetworkAcl','DeleteNetworkAcl'],
    describeSelectors:{DescribeNetworkAcls:['NetworkAclId.N','vpc-id']},detachedConfigurationOnly:['native network ACLs']};
  const cap={services:{ec2}};
  assert.equal(vpcNetworkACLsAvailable(cap,true),true);
  assert.equal(vpcNetworkACLsAvailable({...cap,consoleScope:{instanceId:'i-scoped'}},true),false);
  assert.equal(vpcNetworkACLsAvailable(cap,false),false);
  assert.equal(vpcNetworkACLsAvailable(null,true),false);
  for (const extra of [{operations:'DescribeNetworkAcls CreateNetworkAcl DeleteNetworkAcl'},
    {operations:['CreateNetworkAcl','DescribeNetworkAcls']},{console:false},{describeSelectors:{DescribeNetworkAcls:['NetworkAclId.N']}},
    {detachedConfigurationOnly:['native security groups']}]) assert.equal(vpcNetworkACLsAvailable({services:{ec2:{...ec2,...extra}}},true),false);
  assert.equal(networkACLTarget(vpc),'ec2');assert.equal(networkACLTarget(cfg),'cloudformation');
  assert.equal(networkACLTarget({...vpc,managedBy:'unknown'}),'');
  assert.equal(networkACLTarget({...vpc,id:vpc.id+'\n'}),'');assert.equal(networkACLTarget({id:'no',managedBy:'ec2'}),'');
  for (const good of [name,'','値'.repeat(85),'a'.repeat(256)]) assert.equal(validNetworkACLName(good),true);
  for (const bad of [null,'\0','\ud800','値'.repeat(86),'a'.repeat(257)]) assert.equal(validNetworkACLName(bad),false);
});

test('filtered list accepts only explicit detached shape and fails closed rather than hiding foreign or partial rows',()=>{
  assert.deepEqual(parseVpcNetworkACLs(list(record()),vpc.id),[{id,vpc:vpc.id,mode,name}]);
  assert.deepEqual(parseVpcNetworkACLs(list(),vpc.id),[]);
  for (const bad of [list(record({networkAclId:'rtb-wrong'})),list(record({vpcId:other})),list(record({default:'true'})),
    list(record({simulatorNetworkAclMode:'live'})),list(record({associationSet:[item({subnetId:'subnet-bound'})]})),
    list(record({entrySet:[entry('false'),entry('false')]})),list(record(),record()),list(record({ownerId:'unscoped'})),
    list(record({tagSet:[item({key:'Name',value:'a'}),item({key:'Name',value:'b'})]})),
    doc('DescribeNetworkAcls',[]),doc('DescribeNetworkAcls',[node('networkAclSet'),node('nextToken','partial')]),
    {...list(),doctype:{}},{documentElement:node('DescribeNetworkAclsResponse',[],'invalid')},list(...Array(1001).fill(record()))]) {
    assert.throws(()=>parseVpcNetworkACLs(bad,vpc.id),/Invalid or incomplete/);
  }
});

function assertRequest([path,options,...identity],params,signal){
  assert.deepEqual(identity,[],'no override of the selected principal');
  assert.equal(path,'/');assert.equal(options.method,'POST');assert.equal(options.redirect,'error');
  assert.equal(options.credentials,'same-origin');assert.equal(options.signal,signal);
  assert.deepEqual(options.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region});
  assert.deepEqual(Object.fromEntries(options.body),{Version:'2016-11-15',...params});
}
const filter={Action:'DescribeNetworkAcls','Filter.1.Name':'vpc-id','Filter.1.Value.1':vpc.id};

test('create sends one bounded native write and verifies the token, exact VPC, literal name and scoped readback',async()=>{
  const f=transport(create(),list(record())),{signal}=new AbortController();
  const result=await createVpcNetworkACL(f.request,region,vpc,name,token,{...f.options,signal});
  assert.deepEqual(result,{created:{id,vpc:vpc.id,mode,name},rows:[{id,vpc:vpc.id,mode,name}]});
  assertRequest(f.requests[0],{Action:'CreateNetworkAcl',VpcId:vpc.id,ClientToken:token,'TagSpecification.1.ResourceType':'network-acl',
    'TagSpecification.1.Tag.1.Key':'Name','TagSpecification.1.Tag.1.Value':name},signal);
  assertRequest(f.requests[1],filter,signal);
  const blank=transport(create(record({},'')),list(record({},'')));
  await createVpcNetworkACL(blank.request,region,vpc,'',token,blank.options);
  assertRequest(blank.requests[0],{Action:'CreateNetworkAcl',VpcId:vpc.id,ClientToken:token});
  for (const [failure,readback,count] of [[create(record(),'different'),list(),1],[create(record({vpcId:other})),list(),1],
    [create(),list(record({},'different label')),2],[create(),list(),2]]) {
    const t=transport(failure,readback);
    await assert.rejects(createVpcNetworkACL(t.request,region,vpc,name,token,t.options),/Invalid or incomplete/);
    assert.equal(t.requests.length,count);
  }
});

test('delete accepts only this selected detached row, requires the native acknowledgement and verifies its disappearance',async()=>{
  const found={id,vpc:vpc.id,mode,name}, f=transport(remove(),list());
  assert.deepEqual(await deleteVpcNetworkACL(f.request,region,vpc,found,f.options),{removed:found,rows:[]});
  assertRequest(f.requests[0],{Action:'DeleteNetworkAcl',NetworkAclId:id});assertRequest(f.requests[1],filter);
  for (const [failure,readback,count] of [[remove('false'),list(),1],[remove(),list(record()),2]]) {
    const t=transport(failure,readback);
    await assert.rejects(deleteVpcNetworkACL(t.request,region,vpc,found,t.options),/Invalid or incomplete/);
    assert.equal(t.requests.length,count);
  }
  const never=()=>assert.fail('Do not send a request for an unverified or inapplicable scope');
  for (const invalidRow of [{...found,id:'wrong'},{...found,vpc:other},{...found,mode:'unknown'}])
    await assert.rejects(deleteVpcNetworkACL(never,region,vpc,invalidRow),/Invalid or incomplete/);
  await assert.rejects(createVpcNetworkACL(never,region,cfg,name,token),/Invalid or incomplete/);
  await assert.rejects(loadVpcNetworkACLs(never,region,cfg),/Invalid or incomplete/);
  await assert.rejects(loadVpcNetworkACLs(never,'other\nregion',vpc),/Invalid or incomplete/);
  await assert.rejects(createVpcNetworkACL(never,region,vpc,name,'é'),/Invalid or incomplete/);
  await assert.rejects(createVpcNetworkACL(never,region,vpc,name,token+'\n'),/Invalid or incomplete/);
  await assert.rejects(loadVpcNetworkACLs(never,region+'\n',vpc),/Invalid or incomplete/);
});

test('late/discarded responses, refused requests and non-XML bodies are never empty or success',async()=>{
  const controller=new AbortController();
  await assert.rejects(loadVpcNetworkACLs(async()=>{controller.abort();return xmlResponse()},region,vpc,{signal:controller.signal,
    parse:()=>assert.fail('expired scope result must not be parsed')}),{name:'AbortError'});
  for (const [message,status] of [['UnauthorizedOperation: refused','denied'],['403: forbidden','denied'],
    ['Sign in to continue.','denied'],['InvalidNetworkAclID.NotFound: missing','not-found'],
    ['NetworkAclLimitExceeded: limit','limit'],['500: unknown','unconfirmed']]) {
    await assert.rejects(loadVpcNetworkACLs(async()=>{throw Error(message)},region,vpc),error=>networkACLFailure(error)===status);
  }
  await assert.rejects(loadVpcNetworkACLs(async()=>new Response('unexpected'),region,vpc,
    {parse:()=>assert.fail('non-XML body must never be parsed')}),/Invalid or incomplete/);
});

const require=createRequire(import.meta.url), consoleDir=fileURLToPath(new URL('..',import.meta.url));
const {outputFiles}=await build({absWorkingDir:consoleDir,entryPoints:['src/ec2-vpc-network-acls.jsx'],bundle:true,write:false,
  platform:'node',format:'cjs',loader:{'.css':'empty'},external:['react','react-dom','react/jsx-runtime']});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement}=require('react'),{renderToStaticMarkup}=require('react-dom/server');
const render=scope=>renderToStaticMarkup(createElement(module.exports.default,{vpc:scope,region,request:()=>assert.fail('SSR cannot query')}));

test('real Cloudscape markup offers only bounded detached controls and explains the non-mutating CFN case',()=>{
  const native=render(vpc),stack=render(cfg),unknown=render({...vpc,managedBy:''});
  assert.match(native,/network ACLs/i);assert.match(native,/not associated with subnets/i);
  assert.match(native,/Rule editing and packet filtering are not available/);assert.match(native,/Reading network ACLs/);
  assert.doesNotMatch(native,/No detached network ACLs are saved/);
  assert.match(stack,/CloudFormation manages this VPC/);assert.doesNotMatch(stack,/Create network ACL|Delete network ACL|Refresh network ACL/);
  assert.match(unknown,/Select a verified EC2-managed VPC/);
});
