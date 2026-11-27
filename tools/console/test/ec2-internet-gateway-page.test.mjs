import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import test from 'node:test';
import {fileURLToPath} from 'node:url';
import {build} from 'esbuild';
import {availableInternetGatewayVPCs,filterInternetGateways,internetGatewayActions,loadInternetGatewayVPCs}
  from '../src/ec2-internet-gateway-console.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/';
const region='us-east-1';
const vpcID=n=>'vpc-'+n.toString(16).padStart(17,'0');
const gatewayID=n=>'igw-'+n.toString(16).padStart(17,'0');
const node=(localName,value=[])=>({localName,namespaceURI:ns,children:Array.isArray(value)?value:[],
  textContent:typeof value==='string'?value:''});
const row=(id,managedBy='ec2',name='Network Éast')=>node('item',[
  node('vpcId',id),node('simulatorManagedBy',managedBy),node('cidrBlock','10.0.0.0/16'),
  node('tagSet',[node('item',[node('key','Name'),node('value',name)])]),
]);
const documentOf=(rows,extra=[])=>({documentElement:node('DescribeVpcsResponse',[node('requestId','fixture'),node('vpcSet',rows),...extra])});
const okResponse=()=>new Response('synthetic XML parser input',{headers:{'Content-Type':'text/xml'}});
const vpcRequest=async(...args)=>{
  assert.equal(args.length,2);
  const [path,init]=args;
  assert.equal(path,'/');
  assert.equal(init.credentials,'same-origin');
  assert.equal(init.redirect,'error');
  assert.equal(init.headers['X-Simulator-Service'],'ec2');
  assert.equal(init.headers['X-Simulator-Region'],region);
  assert.deepEqual(Object.fromEntries(init.body),{Action:'DescribeVpcs',Version:'2016-11-15'});
  assert.equal(init.method,'POST');
  assert.ok(!('X-Simulator-Principal' in init.headers));
  return okResponse();
};

test('VPC picker uses verified native regional inventory, with no ambiguous ownership or partial state',async ()=>{
  const parsed=await loadInternetGatewayVPCs(vpcRequest,region,{parse:()=>documentOf([row(vpcID(1)),row(vpcID(2),'cloudformation')])});
  assert.deepEqual(parsed.map(v=>[v.id,v.managedBy]),[[vpcID(1),'ec2'],[vpcID(2),'cloudformation']]);
  assert.ok(Object.isFrozen(parsed)&&parsed.every(Object.isFrozen));
  assert.deepEqual(await loadInternetGatewayVPCs(vpcRequest,region,{parse:()=>documentOf([])}),[]);
  for (const invalid of [
    documentOf([row(vpcID(1)),row(vpcID(1))]),
    documentOf([row('not-a-vpc')]),
    documentOf([row(vpcID(1),'unknown')]),
    documentOf([], [node('nextToken','more')]),
    {...documentOf([row(vpcID(1))]),doctype:{}},
  ]) await assert.rejects(loadInternetGatewayVPCs(vpcRequest,region,{parse:()=>invalid}));
  await assert.rejects(loadInternetGatewayVPCs(()=>assert.fail('invalid region crossed transport'),region+'\n'),/saved VPC/);
  const controller=new AbortController();
  controller.abort();
  await assert.rejects(loadInternetGatewayVPCs(()=>assert.fail('aborted request sent'),region,{signal:controller.signal}),{name:'AbortError'});
});

const a={id:gatewayID(1),name:'West Transit',vpc:vpcID(1),managedBy:'ec2'};
const b={id:gatewayID(2),name:'East Transit',vpc:'',managedBy:'ec2'};
const c={id:gatewayID(3),name:'Team Stack',vpc:vpcID(3),managedBy:'cloudformation'};
const networking=[
  {id:vpcID(1),name:'Customer network',managedBy:'ec2'},
  {id:vpcID(2),name:'Spare network',managedBy:'ec2'},
  {id:vpcID(3),name:'CloudFormation VPC',managedBy:'cloudformation'},
  {id:vpcID(4),name:'Reserved stack',managedBy:'cloudformation'},
];
const list=[a,b,c];

test('search combines case-folded name, gateway ID, VPC name/ID, attachment and ownership without cloning rows',()=>{
  assert.equal(filterInternetGateways(list,networking,'  '),list);
  assert.deepEqual(filterInternetGateways(list,networking,'WEST customer Attached'),[a]);
  assert.deepEqual(filterInternetGateways(list,networking,b.id+' detached'),[b]);
  assert.deepEqual(filterInternetGateways(list,networking,'stack '+vpcID(3)+' cloudformation'),[c]);
  assert.deepEqual(filterInternetGateways(list,networking,'接続済み Network',{attached:'接続済み',detached:'未接続'}),[a]);
  assert.equal(filterInternetGateways(list,networking,'east')[0],b);
  assert.deepEqual(filterInternetGateways(list,networking,'does not exist'),[]);
});

test('attachment choices and mutating affordances require an exact current native row and VPC ownership',()=>{
  assert.deepEqual(availableInternetGatewayVPCs(list,networking).map(v=>v.id),[vpcID(2)]);
  assert.deepEqual(internetGatewayActions(a,list,networking),{attachTargets:[],detach:true,delete:false});
  assert.deepEqual(internetGatewayActions(b,list,networking),{attachTargets:[networking[1]],detach:false,delete:true});
  assert.deepEqual(internetGatewayActions(c,list,networking),{attachTargets:[],detach:false,delete:false});
  assert.deepEqual(internetGatewayActions({...b},list,networking),{attachTargets:[],detach:false,delete:false});
  assert.deepEqual(internetGatewayActions(a,list,[]),{attachTargets:[],detach:false,delete:false});
});

const require=createRequire(import.meta.url);
const {outputFiles}=await build({
  absWorkingDir:fileURLToPath(new URL('..',import.meta.url)),
  entryPoints:['src/ec2-internet-gateway-page.jsx'],bundle:true,platform:'node',format:'cjs',write:false,loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'],define:{'process.env.NODE_ENV':'"production"'},
});
const module={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {InternetGatewayInventory:Inventory,InternetGatewayDetails:Details}=module.exports;
const {createElement}=require('react'), {renderToStaticMarkup}=require('react-dom/server');

test('real Cloudscape view has a labeled search, state and VPC columns, and separate details',()=>{
  const props={rows:list,vpcs:networking,filter:'',page:1,busy:false,open:()=>{},setFilter:()=>{},setPage:()=>{}};
  const table=renderToStaticMarkup(createElement(Inventory,props));
  assert.match(table,/aria-label="Search internet gateways"/);
  for (const value of ['Internet gateway ID','Attachment state','VPC ID','Managed by','West Transit',
    vpcID(1),'Detached','CloudFormation']) assert.ok(table.includes(value),value);
  const filtered=renderToStaticMarkup(createElement(Inventory,{...props,filter:'not present'}));
  assert.match(filtered,/No internet gateways match your search/);
  assert.doesNotMatch(filtered,/West Transit/);
  const detail=renderToStaticMarkup(createElement(Details,{row:a,vpcs:networking}));
  for (const value of ['Gateway details','Attached VPC','Customer network',a.id,a.vpc]) assert.ok(detail.includes(value),value);
  assert.doesNotMatch(detail,/East Transit/);
});
