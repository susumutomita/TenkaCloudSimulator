import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {loadNetworkACLInventory,loadNetworkACLVpcs,networkACLInventoryOperations,networkACLRuleActionsAvailable,parseNetworkACLInventory} from '../src/ec2-network-acls.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/',region='us-east-1';
const vpc='vpc-'+'1'.repeat(17),other='vpc-'+'2'.repeat(17);
const acl='acl-'+'3'.repeat(17),another='acl-'+'4'.repeat(17);
const node=(localName,body=[])=>({localName,namespaceURI:ns,children:Array.isArray(body)?body:[],textContent:typeof body==='string'?body:''});
const item=obj=>node('item',Object.entries(obj).map(([key,value])=>node(key,value)));
const terminal=egress=>item({ruleNumber:'32767',protocol:'-1',ruleAction:'deny',cidrBlock:'0.0.0.0/0',egress});
const response=(action,member,rows)=>({documentElement:node(action+'Response',[node('requestId','wire-id'),node(member,rows)])});
const aclRow=(id,vpcId,entries=[terminal('true'),terminal('false')],associations=[])=>item({networkAclId:id,vpcId,ownerId:'000000000000',
  default:'false',simulatorNetworkAclMode:'detached-config-only',entrySet:entries,associationSet:associations,tagSet:[item({key:'Name',value:'lab'})]});
const list=(...rows)=>response('DescribeNetworkAcls','networkAclSet',rows);
const caps=(operations=['DescribeVpcs','DescribeNetworkAcls','CreateNetworkAcl','DeleteNetworkAcl'])=>({services:{ec2:{console:true,operations,
  detachedConfigurationOnly:['native network ACLs'],describeSelectors:{DescribeNetworkAcls:['vpc-id','NetworkAclId.N']}}}});

function transport(document) {
  const calls=[];
  return {calls,request:async(path,options)=>{calls.push({path,options});return new Response('local XML',{headers:{'Content-Type':'text/xml'}});},
    options:{parse:()=>document}};
}

test('standalone ACL read and writes are independent capabilities, never available in handoffs or public mode',()=>{
  assert.deepEqual(networkACLInventoryOperations(caps(),true),{read:true,create:true,delete:true,vpcs:true});
  assert.deepEqual(networkACLInventoryOperations(caps(['DescribeNetworkAcls']),true),{read:true,create:false,delete:false,vpcs:false});
  assert.deepEqual(networkACLInventoryOperations(caps(['DescribeNetworkAcls','DeleteNetworkAcl']),true),{read:true,create:false,delete:true,vpcs:false});
  for (const [capabilities,auth] of [[caps(),false],[{...caps(),consoleScope:{kind:'instance'}},true],[caps(['CreateNetworkAcl','DescribeVpcs']),true]]) {
    assert.deepEqual(networkACLInventoryOperations(capabilities,auth),{read:false,create:false,delete:false,vpcs:false});
  }
});

test('inventory retains only returned rules and associations; an incomplete or wrong-scope reply is never empty',()=>{
  assert.deepEqual(parseNetworkACLInventory(list(aclRow(acl,vpc),aclRow(another,other))).map(row=>row.vpc),[vpc,other]);
  const [configured]=parseNetworkACLInventory(list(aclRow(acl,vpc)),vpc);
  assert.deepEqual(configured,{id:acl,vpc,mode:'detached-config-only',name:'lab',isDefault:false,associations:[],entries:[
    {number:32767,protocol:'-1',action:'deny',cidr:'0.0.0.0/0',egress:true},
    {number:32767,protocol:'-1',action:'deny',cidr:'0.0.0.0/0',egress:false},
  ]});
  assert.deepEqual(parseNetworkACLInventory(list(),vpc),[]);
  for (const bad of [list(aclRow(acl,other)),list(aclRow(acl,vpc,[terminal('true')])),list(aclRow(acl,vpc,[terminal('false'),terminal('false')])),
    list(aclRow(acl,vpc,[terminal('false'),terminal('true')],[item({networkAclId:acl,subnetId:'subnet-'+'5'.repeat(17)})])),
    list(aclRow(acl,vpc),aclRow(acl,vpc)),response('DescribeNetworkAcls','wrongSet',[])]) {
    assert.throws(()=>parseNetworkACLInventory(bad,vpc),/Invalid or incomplete/);
  }
});

test('queries use actual native EC2 wire and create candidates require verified native VPCs',async()=>{
  const f=transport(list(aclRow(acl,vpc)));
  await loadNetworkACLInventory(f.request,region,'',f.options);
  await loadNetworkACLInventory(f.request,region,vpc,f.options);
  for (const {path,options} of f.calls) {
    assert.equal(path,'/');assert.equal(options.credentials,'same-origin');assert.equal(options.redirect,'error');
    assert.equal(options.headers['X-Simulator-Service'],'ec2');assert.equal(options.headers['X-Simulator-Region'],region);
  }
  assert.deepEqual(Object.fromEntries(f.calls[0].options.body),{Action:'DescribeNetworkAcls',Version:'2016-11-15'});
  assert.deepEqual(Object.fromEntries(f.calls[1].options.body),{Action:'DescribeNetworkAcls',Version:'2016-11-15',
    'Filter.1.Name':'vpc-id','Filter.1.Value.1':vpc});
  const vpcReply=response('DescribeVpcs','vpcSet',[
    item({vpcId:vpc,simulatorManagedBy:'ec2',cidrBlock:'10.1.0.0/16',tagSet:[item({key:'Name',value:'native'})]}),
    item({vpcId:other,simulatorManagedBy:'cloudformation',cidrBlock:'10.2.0.0/16'}),
  ]);
  vpcReply.documentElement.children.push(node('reservationSet'));
  const v=transport(vpcReply);
  assert.deepEqual(await loadNetworkACLVpcs(v.request,region,v.options),[{id:vpc,managedBy:'ec2',cidr:'10.1.0.0/16',name:'native'}]);
  assert.deepEqual(Object.fromEntries(v.calls[0].options.body),{Action:'DescribeVpcs',Version:'2016-11-15'});
  vpcReply.documentElement.children.at(-1).children.push(item({reservationId:'not-a-vpc'}));
  await assert.rejects(loadNetworkACLVpcs(v.request,region,v.options),/Invalid or incomplete/);
  const stop=new AbortController();stop.abort();
  await assert.rejects(loadNetworkACLInventory(()=>assert.fail('must not query after abort'),region,vpc,{signal:stop.signal}),{name:'AbortError'});
  await assert.rejects(loadNetworkACLInventory(()=>assert.fail('must not query bad ID'),region,vpc+'\n'),/Invalid or incomplete/);
});

const require=createRequire(import.meta.url),consoleDir=fileURLToPath(new URL('..',import.meta.url));
const {outputFiles}=await build({absWorkingDir:consoleDir,entryPoints:['src/ec2-network-acl-inventory.jsx'],bundle:true,write:false,
  platform:'node',format:'cjs',loader:{'.css':'empty'},external:['react','react-dom','react/jsx-runtime']});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement}=require('react'),{renderToStaticMarkup}=require('react-dom/server');
const render=(capabilities,authenticationRequired=true)=>renderToStaticMarkup(createElement(module.exports.default,{capabilities,authenticationRequired,region,
  request:()=>assert.fail('SSR cannot query')}));

test('Cloudscape inventory and details distinguish unavailable, read-only and detached saved configuration',()=>{
  const unavailable=render(caps(),false),readOnly=render(caps(['DescribeNetworkAcls']));
  assert.match(unavailable,/unavailable in this session/);
  assert.doesNotMatch(unavailable,/Filter by VPC|Create network ACL|Delete network ACL/);
  assert.match(readOnly,/Filter by VPC/);assert.match(readOnly,/Reading network ACLs/);
  assert.match(readOnly,/packet filtering are not supported/);
  assert.doesNotMatch(readOnly,/Create network ACL|Delete network ACL|Edit rules/);
  const markup=renderToStaticMarkup(createElement(module.exports.NetworkACLDetails,{acl:parseNetworkACLInventory(list(aclRow(acl,vpc)))[0],
    onOpenSubnet:()=>assert.fail('no unverified subnet navigation')}));
  for (const text of ['Inbound rules','Outbound rules','Subnet associations','32767','0.0.0.0/0','Deny','do not filter traffic']) assert.ok(markup.includes(text),text);
  assert.doesNotMatch(markup,/Edit inbound|Edit outbound|Edit subnet/);
});


test('editing appears only with exact detached capability and never applies to fixed terminal rules',()=>{
  const actions=['CreateNetworkAclEntry','ReplaceNetworkAclEntry','DeleteNetworkAclEntry'];
  const native=caps(['DescribeVpcs','DescribeNetworkAcls',...actions]);
  assert.equal(networkACLRuleActionsAvailable(native,true),true);
  assert.equal(networkACLRuleActionsAvailable(native,false),false);
  assert.equal(networkACLRuleActionsAvailable({...native,consoleScope:{kind:'instance'}},true),false);
  assert.equal(networkACLRuleActionsAvailable(caps(['DescribeVpcs','DescribeNetworkAcls',actions[0],actions[1]]),true),false);
  assert.equal(networkACLRuleActionsAvailable(caps(['DescribeNetworkAcls',...actions]),true),false);
  assert.match(render(native),/Edit saved IPv4 rules on detached custom ACLs/);
  assert.match(render(native),/Subnet attachment and packet filtering are not supported/);
  const snapshot=parseNetworkACLInventory(list(aclRow(acl,vpc,[
    item({ruleNumber:'100',protocol:'6',ruleAction:'allow',cidrBlock:'192.0.2.0/24',egress:'false',
      portRange:[node('from','80'),node('to','443')]}),terminal('false'),terminal('true'),
  ])))[0];
  const view=onRuleChange=>renderToStaticMarkup(createElement(module.exports.NetworkACLDetails,{acl:snapshot,onRuleChange}));
  for (const markup of [view(undefined),view(()=>assert.fail('SSR cannot submit'))]) {
    assert.match(markup,/TCP \(6\)/);assert.match(markup,/80–443/);assert.match(markup,/do not filter traffic/);
    assert.doesNotMatch(markup,/Edit rule 32767|Delete rule 32767/);
  }
  assert.doesNotMatch(view(undefined),/Add rule|Edit rule 100/);
  assert.match(view(()=>{}),/Add rule/);assert.match(view(()=>{}),/Edit rule 100/);
});
