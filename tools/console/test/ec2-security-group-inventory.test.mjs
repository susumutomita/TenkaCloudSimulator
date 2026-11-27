import test from 'node:test';
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import {build} from 'esbuild';
import {securityGroupInventoryAvailable, securityGroupInventoryVPCs, securityGroupVPCProfile} from '../src/ec2-security-group-inventory.mjs';

const vpcID = 'vpc-0123456789abcdef0';
const ec2 = {console:true, securityGroupReadback:'DescribeSecurityGroups',
  operations:['DescribeVpcs','DescribeSecurityGroups','CreateSecurityGroup','DeleteSecurityGroup',
    'AuthorizeSecurityGroupIngress','AuthorizeSecurityGroupEgress','RevokeSecurityGroupIngress','RevokeSecurityGroupEgress'],
  detachedConfigurationOnly:['native security groups'], describeSelectors:{DescribeSecurityGroups:['vpc-id']},
  detachedSecurityGroupRuleProfile:'tcp-ipv4-same-vpc',detachedSecurityGroupRulesPerDirection:60};
const caps = {services:{ec2}};
const stack = {id:vpcID,managedBy:'cloudformation'};
const owned = {id:vpcID,managedBy:'ec2'};
const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/';
const node = (localName, children=[], textContent='') => ({localName,children,textContent,namespaceURI:ns});
const leaf = (n,v) => node(n,[],v);
const vpcRow = id => node('item',[leaf('vpcId',id),leaf('cidrBlock','10.40.0.0/16'),leaf('simulatorManagedBy','ec2')]);
const doc = (...rows) => ({documentElement:node('DescribeVpcsResponse',[node('vpcSet',rows)])});

test('requires a local unbound read API and never enables writes for stack-owned or unknown VPCs', () => {
  assert.equal(securityGroupInventoryAvailable(caps,true),true);
  for (const [c,auth] of [[caps,false],[{...caps,consoleScope:{}},true],[{services:{ec2:{...ec2,operations:['DescribeSecurityGroups']}}},true]])
    assert.equal(securityGroupInventoryAvailable(c,auth),false);
  assert.equal(securityGroupVPCProfile(stack,caps,true),'stack-read-only');
  assert.equal(securityGroupVPCProfile(owned,caps,true),'native-rules');
  assert.equal(securityGroupVPCProfile({...owned,managedBy:''},caps,true),null);
  assert.equal(securityGroupVPCProfile({...owned,id:vpcID+'\n'},caps,true),null);
  assert.equal(securityGroupVPCProfile(owned,{services:{ec2:{...ec2,operations:ec2.operations.slice(0,4)}}},true),'native-groups');
  assert.equal(securityGroupVPCProfile(owned,{services:{ec2:{...ec2,operations:ec2.operations.slice(0,2)}}},true),null);
});

test('uses the direct scoped EC2 discovery call and refuses errors, inconsistent XML and late results', async () => {
  const calls=[];
  const request=async (url,opts)=>{calls.push([url,opts]);return {ok:true,text:async()=>doc(vpcRow(vpcID))};};
  await assert.rejects(securityGroupInventoryVPCs(request,'us-east-1\n'),/Invalid EC2 region/);
  const rows=await securityGroupInventoryVPCs(request,'us-east-1',{parse:x=>x});
  const [[url,options]]=calls;
  assert.equal(url,'/');
  assert.equal(options.method,'POST');
  assert.equal(options.redirect,'error');
  assert.equal(options.headers['X-Simulator-Region'],'us-east-1');
  assert.deepEqual(Object.fromEntries(options.body),{Action:'DescribeVpcs',Version:'2016-11-15'});
  assert.deepEqual(rows,[{id:vpcID,cidr:'10.40.0.0/16',managedBy:'ec2',name:''}]);
  await assert.rejects(securityGroupInventoryVPCs(async()=>({ok:false,status:403}),'us-east-1'),/^Error: 403:/);
  await assert.rejects(securityGroupInventoryVPCs(async()=>({ok:true,text:async()=>doc(vpcRow(vpcID),vpcRow(vpcID))}),'us-east-1',{parse:x=>x}),/Invalid or incomplete EC2/);
  const abort=new AbortController();
  await assert.rejects(securityGroupInventoryVPCs(async()=>({ok:true,text:async()=>{abort.abort();return doc();}}),'us-east-1',
    {signal:abort.signal,parse:()=>assert.fail('superseded response must not be parsed')}),{name:'AbortError'});
});

const require=createRequire(import.meta.url);
const dir=fileURLToPath(new URL('..',import.meta.url));
const {outputFiles}=await build({absWorkingDir:dir,
  stdin:{contents:"export {default} from './src/ec2-security-groups-console.jsx'; export {SecurityGroupInventoryTable} from './src/ec2-vpc-security-groups.jsx';",resolveDir:dir},
  bundle:true,write:false,platform:'node',format:'cjs',loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'],define:{'process.env.NODE_ENV':'"production"'},
});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement}=require('react'),{renderToStaticMarkup}=require('react-dom/server');

test('real page and inventory render unsupported/loading separately; table uses exact ID, not a display name', () => {
  const render=authenticationRequired=>renderToStaticMarkup(createElement(module.exports.default,{capabilities:caps,authenticationRequired,region:'us-east-1',request:()=>assert.fail('SSR must not send')}));
  assert.match(render(false),/inventory is unavailable/);
  assert.match(render(true),/Loading VPCs/);
  assert.match(render(true),/do not affect traffic/);
  assert.doesNotMatch(render(false),/Create security group|Delete security group|Add inbound rule/);
  const rows=[{id:'sg-0123456789abcdef0',vpc:vpcID,name:'same',description:'First <description>'},
    {id:'sg-0123456789abcdef1',vpc:vpcID,name:'same',description:'Second'}];
  const table=renderToStaticMarkup(createElement(module.exports.SecurityGroupInventoryTable,{groups:rows,group:rows[1],onSelect:()=>{}}));
  for (const row of rows) assert.match(table,new RegExp(row.id));
  assert.match(table,/First &lt;description&gt;/);
  assert.match(table,/type="radio"[^>]*checked/);
  assert.doesNotMatch(table,/Add inbound rule|Delete security group/);
});
