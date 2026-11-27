import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import test from 'node:test';
import {fileURLToPath} from 'node:url';
import {build} from 'esbuild';
import {
  loadRouteTableConsoleInventory, loadRouteTableImplicitSubnets, loadRouteTableVpcContext, routeTableConsoleAvailable,
  routeTableConsoleFilter, routeTableMainStatus, routeTableConsoleHref, validRouteTableConsoleID,
} from '../src/ec2-route-table-console.mjs';

const ns = 'http://ec2.amazonaws.com/doc/2016-11-15/', region = 'us-east-1';
const id = (kind, number) => kind + '-' + String(number).padStart(17, '0');
const vpc = {id: id('vpc',1), cidr:'10.11.0.0/16', managedBy:'ec2'};
const stack = {id: id('vpc',2), cidr:'10.12.0.0/16', managedBy:'cloudformation'};
const [main, custom, stackTable] = [1,2,3].map(n => id('rtb',n));
const explicit = id('subnet',1), implicit = id('subnet',2);
const element = (localName,data=[]) => ({localName, namespaceURI:ns, nodeType:1,
  children:Array.isArray(data) ? data : [], textContent:typeof data === 'string' ? data : ''});
const fields = object => Object.entries(object).map(([name, value]) => element(name,value));
const rec = values => element('item', fields(values));
const doc = (action,values) => ({documentElement:element(action+'Response',fields({requestId:'request',...values}))});
const route = (cidr, gateway, origin) => rec({destinationCidrBlock:cidr,gatewayId:gateway,origin,state:'active'});
const table = (tableId, owner, values) => rec({
  routeTableId:tableId,vpcId:owner.id,ownerId:'000000000000',routeSet:[],associationSet:[],tagSet:[],...values,
});
const subnet = (subnetId,cidr) => rec({
  subnetId,vpcId:vpc.id,cidrBlock:cidr,state:'available',availabilityZone:region+'a',simulatorManagedBy:'ec2',
});
const routeDoc = () => doc('DescribeRouteTables',{routeTableSet:[
  table(main,vpc,{routeSet:[route(vpc.cidr,'local','CreateRouteTable')],associationSet:[rec({
    routeTableId:main,routeTableAssociationId:id('rtbassoc',1),main:'true',
  })]}),
  table(custom,vpc,{routeSet:[route('0.0.0.0/0',id('igw',1),'CreateRoute'),route(vpc.cidr,'local','CreateRouteTable')],
    associationSet:[rec({routeTableId:custom,routeTableAssociationId:id('rtbassoc',2),main:'false',subnetId:explicit})],
    tagSet:[rec({key:'Name',value:'Public Test'})]}),
  table(stackTable,stack,{}),
]});
const subnetDoc = () => doc('DescribeSubnets',{subnetSet:[
  subnet(explicit,'10.11.1.0/24'),subnet(implicit,'10.11.2.0/24'),
]});
function vpcDoc(cidr = vpc.cidr) {
  return doc('DescribeVpcs',{vpcSet:[
    rec({vpcId:vpc.id,cidrBlock:cidr,simulatorManagedBy:'ec2'}),
    rec({vpcId:stack.id,cidrBlock:stack.cidr,simulatorManagedBy:'cloudformation'}),
    rec({vpcId:id('vpc',3),cidrBlock:'10.13.0.0/16'}),
  ]});
}
function fixture(...documents) {
  const seen = [];
  const request = (...args) => {
    assert.equal(args.length,2,'must keep the console request identity');
    const [path, options] = args;
    assert.equal(path,'/');
    assert.equal(options.credentials,'same-origin');
    assert.equal(options.redirect,'error');
    assert.equal(options.headers['X-Simulator-Region'],region);
    assert.equal(options.headers['X-Simulator-Service'],'ec2');
    assert.ok(!('X-Simulator-Principal' in options.headers));
    const n = seen.length;
    seen.push(Object.fromEntries(options.body));
    assert.ok(n < documents.length,'unexpected request');
    return Promise.resolve(new Response(String(n),
      {status:typeof documents[n] === 'number' ? documents[n] : 200, headers:{'Content-Type':'text/xml; charset=utf-8'}}));
  };
  return {seen,request,parse:encoded=>documents[Number(encoded)]};
}
const describe = action => ({Action:action,Version:'2016-11-15'});

test('regional inventory labels only confirmed main tables and filters the completed snapshot', async () => {
  const f = fixture(routeDoc());
  const result = await loadRouteTableConsoleInventory(f.request,region,f);
  assert.deepEqual(f.seen,[{...describe('DescribeRouteTables'),MaxResults:'100'}]);
  assert.deepEqual(result.map(row=>[row.id,routeTableMainStatus(result,row)]),
    [[main,'yes'],[custom,'no'],[stackTable,'unreported']]);
  assert.equal(result[1].name,'Public Test');
  assert.deepEqual(result[1].localRoute,{cidr:vpc.cidr,gateway:'local'});
  assert.ok(Object.isFrozen(result[1].routes[0]) && Object.isFrozen(result[1].associations));
  assert.deepEqual(routeTableConsoleFilter(result,' PUBLIC ',vpc.id).map(row=>row.id),[custom]);
  assert.deepEqual(routeTableConsoleFilter(result,explicit,'').map(row=>row.id),[custom]);
  assert.deepEqual(routeTableConsoleFilter(result,explicit,stack.id),[]);
  assert.equal(validRouteTableConsoleID(main),true);
  assert.equal(validRouteTableConsoleID(main+'\n'),false);
});

test('VPC write context comes only from a bounded authenticated DescribeVpcs response', async () => {
  const f = fixture(vpcDoc());
  const rows = await loadRouteTableVpcContext(f.request,region,f);
  assert.deepEqual(f.seen,[describe('DescribeVpcs')]);
  assert.deepEqual(rows,[{...vpc,name:''},{...stack,name:''}]);
  assert.ok(Object.isFrozen(rows[0]));
  const acl = fixture(403), malformed = fixture(vpcDoc('10.11.0.7/16'));
  await assert.rejects(loadRouteTableVpcContext(acl.request,region,acl),/^Error: 403:/);
  await assert.rejects(loadRouteTableVpcContext(malformed.request,region,malformed),/Invalid or incomplete/);
  const aborted = new AbortController();
  aborted.abort();
  await assert.rejects(loadRouteTableVpcContext(()=>assert.fail('aborted'),region,{signal:aborted.signal}),{name:'AbortError'});
});

test('implicit fallback excludes all explicit associations and refuses partial, foreign or denied input', async () => {
  const f = fixture(routeDoc(),subnetDoc());
  const snapshot = await loadRouteTableConsoleInventory(f.request,region,f);
  await assert.rejects(loadRouteTableImplicitSubnets(f.request,region,vpc,snapshot.slice(),f),/Invalid or incomplete/);
  await assert.rejects(loadRouteTableImplicitSubnets(f.request.bind(null),region,vpc,snapshot,f),/Invalid or incomplete/);
  await assert.rejects(loadRouteTableImplicitSubnets(f.request,region,stack,snapshot,f),/Invalid or incomplete/);
  const implicitSet = await loadRouteTableImplicitSubnets(f.request,region,vpc,snapshot,f);
  assert.deepEqual(implicitSet,{mainId:main,items:[{
    id:implicit, vpc:vpc.id, cidr:'10.11.2.0/24', az:region+'a', owner:'ec2',
  }]});
  assert.deepEqual(f.seen.slice(1),[describe('DescribeSubnets')]);
  const missing = fixture(routeDoc(),doc('DescribeSubnets',{subnetSet:[subnet(implicit,'10.11.2.0/24')]}));
  await assert.rejects(loadRouteTableImplicitSubnets(missing.request,region,vpc,
    await loadRouteTableConsoleInventory(missing.request,region,missing),missing),/Invalid or incomplete/);
  const denied = fixture(routeDoc(),403);
  await assert.rejects(loadRouteTableImplicitSubnets(denied.request,region,vpc,
    await loadRouteTableConsoleInventory(denied.request,region,denied),denied),/^Error: 403:/);
});

test('only local hash links from the router are honored', () => {
  const ref = {kind:'vpc',id:vpc.id,vpcId:vpc.id,region};
  const target = '#/vpc?vpcId='+vpc.id;
  assert.equal(routeTableConsoleHref(value=>{assert.deepEqual(value,ref);return target;},ref),target);
  for (const value of ['https://another.test/','//another.test/','/not/a/fragment','javascript:void(0)',target+'\n','']) {
    assert.equal(routeTableConsoleHref(()=>value,ref),'');
  }
  assert.equal(routeTableConsoleHref(undefined,ref),'');
});

const require = createRequire(import.meta.url);
const consoleDir = fileURLToPath(new URL('..',import.meta.url));
const {outputFiles} = await build({absWorkingDir:consoleDir,entryPoints:['src/ec2-route-table-console.jsx'],bundle:true,write:false,
  platform:'node',format:'cjs',loader:{'.css':'empty'},external:['react','react-dom','react/jsx-runtime']});
const module = {exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {createElement} = require('react'), {renderToStaticMarkup} = require('react-dom/server');
const capabilities = {services:{ec2:{console:true,routeTableReadback:'DescribeRouteTables',
  routeTableLifecycle:['DescribeRouteTables','CreateRouteTable','DeleteRouteTable'],
  operations:['DescribeVpcs','DescribeRouteTables','CreateRouteTable','DeleteRouteTable'],
  detachedConfigurationOnly:['native route tables']}}};

test('Cloudscape entry keeps unsupported discovery unavailable and pending rows non-writable', () => {
  assert.equal(routeTableConsoleAvailable(capabilities,true),true);
  assert.equal(routeTableConsoleAvailable({...capabilities,consoleScope:{instanceId:'fixture'}},true),false);
  const render = props => renderToStaticMarkup(createElement(module.exports.default,
    {request:()=>assert.fail('SSR must not query'),region,capabilities,...props}));
  const unavailable = render({authenticationRequired:false});
  assert.match(unavailable,/Route table discovery is unavailable/);
  assert.doesNotMatch(unavailable,/Create route table/);
  const pending = render({authenticationRequired:true,routeTableId:custom});
  assert.match(pending,/Reading route tables/);
  assert.match(pending,/does not forward packets or test network reachability/);
  assert.match(pending,/disabled=""[^>]*>(?:<span[^>]*>)*Create route table/);
  assert.doesNotMatch(pending,/Saved route table change verified|No route tables are recorded/);
});
