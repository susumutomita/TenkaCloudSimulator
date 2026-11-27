import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {createVpcResourceMapScope,loadVpcResourceMap,startVpcResourceMapRead} from '../src/ec2-vpc-topology-read.mjs';

const ns='http://ec2.amazonaws.com/doc/2016-11-15/', region='us-east-1', otherRegion='us-west-2';
const id=(prefix,n)=>prefix+'-'+n.toString(16).padStart(17,'0');
const a={id:id('vpc',1),cidr:'10.10.0.0/16',managedBy:'ec2'};
const b={id:id('vpc',2),cidr:'10.20.0.0/16',managedBy:'cloudformation'};
const c={id:id('vpc',3),cidr:'10.30.0.0/16',managedBy:'ec2'};
const first=id('subnet',1), second=id('subnet',2), peerSub=id('subnet',3), cfnSub=id('subnet',4), cfnOther=id('subnet',5);
const main=id('rtb',1), custom=id('rtb',2), peerTable=id('rtb',3), cfnTable=id('rtb',4);
const gateway=id('igw',1), detached=id('igw',2), peerGateway=id('igw',3), cfnGateway=id('igw',4);
const actions=['DescribeSubnets','DescribeRouteTables','DescribeInternetGateways'];
const capabilities={services:{ec2:{console:true,operations:['DescribeVpcs','DescribeInstances',...actions],
  subnetLifecycle:actions.slice(0,1).concat('CreateSubnet','DeleteSubnet'),routeTableReadback:'DescribeRouteTables',
  internetGatewayLifecycle:actions.slice(2),internetGatewayPageLimit:100}}};
const el=(localName,value='')=>{
  const children=Array.isArray(value)?value:[];
  return {localName,namespaceURI:ns,children,childNodes:children,textContent:typeof value==='string'?value:'',nodeType:1};
};
const shape=(action,list,items,tail=[])=>({documentElement:el(action+'Response',[el('requestId','fixture'),el(list,items),...tail])});
const subnet=(name,vpc,cidr,az=region+'a')=>el('item',[el('subnetId',name),el('vpcId',vpc.id),el('cidrBlock',cidr),el('state','available'),
  ...(vpc.managedBy==='ec2'?[el('simulatorManagedBy','ec2')]:[]),el('availabilityZone',az),el('tagSet',[])]);
const route=(cidr,target)=>el('item',[el('destinationCidrBlock',cidr),el('gatewayId',target),
  el('origin',target==='local'?'CreateRouteTable':'CreateRoute'),el('state','active')]);
const assoc=(name,table,sub='')=>el('item',[el('routeTableAssociationId',name),el('routeTableId',table),
  el('main',sub?'false':'true'),...(sub?[el('subnetId',sub)]:[el('associationState',[el('state','associated')])])]);
const table=(name,vpc,routes,assocs=[])=>el('item',[el('routeTableId',name),el('vpcId',vpc.id),el('ownerId','000000000000'),
  el('routeSet',routes),el('associationSet',assocs),el('tagSet',[])]);
const igw=(name,vpc,owner=vpc?.managedBy??'ec2')=>el('item',[el('internetGatewayId',name),el('ownerId','000000000000'),
  el('attachmentSet',vpc?[el('item',[el('vpcId',vpc.id),el('state','available')])]:[]),
  el('tagSet',[]),el('simulatorManagedBy',owner)]);
const tableDoc=(items,tail=[])=>shape(actions[1],'routeTableSet',items,tail);
const subnetDoc=(items)=>shape(actions[0],'subnetSet',items);
const gwDoc=(items)=>shape(actions[2],'internetGatewaySet',items);

function nativeDocs(r=region,target=gateway) {
  return {
    DescribeSubnets:subnetDoc([subnet(first,a,'10.10.1.0/24',r+'a'),subnet(second,a,'10.10.2.0/24',r+'b'),
      subnet(peerSub,c,'10.30.1.0/24',r+'c')]),
    DescribeRouteTables:tableDoc([
      table(main,a,[route(a.cidr,'local')],[assoc(id('rtbassoc',1),main)]),
      table(custom,a,[route('0.0.0.0/0',target),route(a.cidr,'local')],[assoc(id('rtbassoc',2),custom,second)]),
      table(peerTable,c,[],[]),
    ]),
    DescribeInternetGateways:gwDoc([igw(gateway,a),igw(detached,null),igw(peerGateway,c)]),
  };
}
const defer=()=>{let resolve;const promise=new Promise(done=>{resolve=done;});return {resolve,promise};};
let fixtureCounter=0;
const contexts=new WeakMap();
function fixture(t,bodies,{r=region,delays={}}={}) {
  if (!contexts.has(t)) {
    const receipts=new Map(),original=globalThis.DOMParser;
    globalThis.DOMParser=class {
      parseFromString(text,type){
        assert.equal(type,'application/xml');
        assert.ok(receipts.has(text),'unexpected response reaching existing XML parser');
        return receipts.get(text);
      }
    };
    t.after(()=>{if(original===undefined)delete globalThis.DOMParser;else globalThis.DOMParser=original;});
    contexts.set(t,receipts);
  }
  const receipts=contexts.get(t),calls=[],key=++fixtureCounter;
  const request=async(path,init)=>{
    assert.equal(path,'/');
    assert.equal(init.method,'POST');
    assert.equal(init.credentials,'same-origin');
    assert.equal(init.redirect,'error');
    assert.deepEqual(init.headers,{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':r});
    const params=Object.fromEntries(init.body), action=params.Action;
    assert.ok(actions.includes(action),'operation must be Describe only: '+action);
    calls.push({params,signal:init.signal});
    assert.deepEqual(params,{Action:action,Version:'2016-11-15',...(action==='DescribeSubnets'?{}:{MaxResults:'100'})});
    if(delays[action])await delays[action].promise; // accepted response may arrive after abort
    const value=bodies[action];
    if(typeof value==='number')return new Response('refused',{status:value});
    if(value instanceof Error)throw value;
    assert.ok(value,'no fixture for '+action);
    const label='response-'+key+'-'+action;
    receipts.set(label,value);
    return new Response(label,{headers:{'Content-Type':'text/xml; charset=UTF-8'}});
  };
  return {request,calls};
}
const scopeFor=(f,vpc=a,r=region,cap=capabilities,auth=true)=>createVpcResourceMapScope(f.request,r,vpc,cap,auth);
const read=(f,vpc=a,r=region,cap=capabilities)=>loadVpcResourceMap(scopeFor(f,vpc,r,cap));

test('ordinary signed-in capability, canonical selected VPC and exact region gate every read',async t=>{
  const f=fixture(t,nativeDocs());
  for(const scope of [scopeFor(f,a,region,capabilities,false),scopeFor(f,{...a,id:a.id+'\n'}),
    scopeFor(f,{...a,managedBy:'unknown'}),scopeFor(f,{...a,cidr:'10.10.1.1/16'}),scopeFor(f,a,region+'\n'),
    scopeFor(f,a,region,{...capabilities,consoleScope:{instanceId:'isolated'}}),scopeFor(f,a,region,{services:{ec2:{console:true}}})]){
    assert.equal(scope,null);
    await assert.rejects(loadVpcResourceMap(scope));
  }
  const good=scopeFor(f);
  assert.equal(Object.isFrozen(good),true);
  assert.deepEqual(good.features,{subnets:true,tables:true,gateways:true});
  await assert.rejects(loadVpcResourceMap({...good}));
  assert.equal(f.calls.length,0);
});

test('native map retains explicit overrides, one witnessed implicit fallback, and attached gateways only',async t=>{
  const f=fixture(t,nativeDocs());
  const s=await read(f);
  assert.deepEqual(s.subnets.rows.map(row=>[row.id,row.association]),[
    [first,{id:main,kind:'main'}],[second,{id:custom,kind:'explicit'}],
  ]);
  assert.equal(s.subnetAssociationStatus,'ready');
  assert.deepEqual(s.routeTables,{status:'ready',rows:[{id:main,main:true},{id:custom,main:false}]});
  assert.deepEqual(s.gateways,{status:'ready',rows:[{id:gateway}]});
  assert.deepEqual(s.gatewayRoutes,{status:'ready',rows:[{table:custom,cidr:'0.0.0.0/0',gateway}]});
  const printed=JSON.stringify(s);
  for(const excluded of [peerSub,peerTable,detached,peerGateway])assert.ok(!printed.includes(excluded));
  assert.deepEqual(f.calls.map(c=>c.params.Action).sort(),[...actions].sort());
});

test('stack rows display explicit saved associations only, with neither default nor implicit claims',async t=>{
  const f=fixture(t,{
    DescribeSubnets:subnetDoc([subnet(cfnSub,b,'10.20.1.0/24'),subnet(cfnOther,b,'10.20.2.0/24')]),
    DescribeRouteTables:tableDoc([table(cfnTable,b,[route('0.0.0.0/0',cfnGateway)],[assoc(id('rtbassoc',4),cfnTable,cfnSub)])]),
    DescribeInternetGateways:gwDoc([igw(cfnGateway,b)]),
  });
  const s=await read(f,b);
  assert.deepEqual(s.subnets.rows.map(row=>row.association),[{id:cfnTable,kind:'explicit'},null]);
  assert.deepEqual(s.routeTables.rows,[{id:cfnTable,main:false}]);
  assert.deepEqual(s.gatewayRoutes.rows,[{table:cfnTable,cidr:'0.0.0.0/0',gateway:cfnGateway}]);
});

test('legacy native VPC without a main marker never gains a guessed fallback',async t=>{
  const docs=nativeDocs();
  docs.DescribeRouteTables=tableDoc([table(custom,a,[],[assoc(id('rtbassoc',2),custom,second)])]);
  const snapshot=await read(fixture(t,docs));
  assert.equal(snapshot.subnetAssociationStatus,'ready');
  assert.deepEqual(snapshot.subnets.rows.map(row=>row.association),[null,{id:custom,kind:'explicit'}]);
  assert.deepEqual(snapshot.routeTables.rows,[{id:custom,main:false}]);
});

test('unsupported stack or overlapping native route does not create a gateway edge',async t=>{
  const malformedStack=fixture(t,{
    DescribeSubnets:subnetDoc([subnet(cfnSub,b,'10.20.1.0/24')]),
    DescribeRouteTables:tableDoc([table(cfnTable,b,[route('198.51.100.0/24',cfnGateway)])]),
    DescribeInternetGateways:gwDoc([igw(cfnGateway,b)]),
  });
  const cfn=await read(malformedStack,b);
  assert.equal(cfn.routeTables.status,'inconsistent');
  assert.equal(cfn.gatewayRoutes.status,'unconfirmed');
  const docs=nativeDocs();
  docs.DescribeRouteTables=tableDoc([table(custom,a,[route('10.10.7.0/24',gateway)])]);
  const native=await read(fixture(t,docs));
  assert.equal(native.routeTables.status,'inconsistent');
  assert.equal(native.gatewayRoutes.status,'unconfirmed');
});

test('orphan and foreign association witnesses suppress even the main fallback and show join inconsistency',async t=>{
  for(const mismatch of ['orphan','peer']){
    const docs=nativeDocs();
    const orphan=table(custom,a,[route(a.cidr,'local')],[assoc(id('rtbassoc',8),custom,id('subnet',99))]);
    const foreign=table(peerTable,c,[],[assoc(id('rtbassoc',9),peerTable,first)]);
    const found=docs.DescribeRouteTables.documentElement.children[1].children;
    found[mismatch==='orphan'?1:2]=mismatch==='orphan'?orphan:foreign;
    const f=fixture(t,docs),s=await read(f);
    assert.equal(s.subnetAssociationStatus,'inconsistent',mismatch);
    assert.ok(s.subnets.rows.every(row=>row.association===null));
    assert.equal(s.routeTables.status,'ready');
    assert.deepEqual(s.gateways.rows,[{id:gateway}]);
  }
});

test('wrong main CIDR and cross-owner internet attachment cannot serve as relationship witnesses',async t=>{
  const docs=nativeDocs();
  docs.DescribeRouteTables=tableDoc([table(main,a,[route(c.cidr,'local')],[assoc(id('rtbassoc',1),main)])]);
  docs.DescribeInternetGateways=gwDoc([igw(gateway,a,'cloudformation')]);
  const f=fixture(t,docs),s=await read(f);
  assert.deepEqual(s.routeTables,{status:'inconsistent',rows:[]});
  assert.deepEqual(s.gateways,{status:'inconsistent',rows:[]});
  assert.deepEqual(s.gatewayRoutes,{status:'unconfirmed',rows:[]});
  assert.ok(s.subnets.rows.every(row=>row.association===null));
});

test('unattached target reference is visible as incomplete, not silently empty or a fake edge',async t=>{
  const f=fixture(t,nativeDocs(region,peerGateway));
  const s=await read(f);
  assert.deepEqual(s.gatewayRoutes,{status:'partial',rows:[]});
  assert.deepEqual(s.gateways.rows,[{id:gateway}]);
  assert.ok(!JSON.stringify(s).includes(peerGateway));
});

test('a partial gateway join preserves only the separately confirmed routes',async t=>{
  const docs=nativeDocs();
  docs.DescribeRouteTables=tableDoc([
    table(main,a,[route('0.0.0.0/0',detached),route(a.cidr,'local')],[assoc(id('rtbassoc',1),main)]),
    table(custom,a,[route(a.cidr,'local'),route('198.51.100.0/24',gateway)]),
  ]);
  const s=await read(fixture(t,docs));
  assert.deepEqual(s.gatewayRoutes,{status:'partial',rows:[{table:custom,cidr:'198.51.100.0/24',gateway}]});
  assert.ok(!JSON.stringify(s).includes(detached));
});

test('denial, unadvertised reads, malformed pages and complete absence are distinct',async t=>{
  const denied=fixture(t,{...nativeDocs(),DescribeRouteTables:403});
  const deniedResult=await read(denied);
  assert.equal(deniedResult.routeTables.status,'denied');
  assert.equal(deniedResult.subnets.status,'ready');
  assert.equal(deniedResult.subnetAssociationStatus,'unconfirmed');
  assert.equal(deniedResult.gatewayRoutes.status,'unconfirmed');
  const cap={services:{ec2:{...capabilities.services.ec2,operations:['DescribeVpcs','DescribeSubnets']}}};
  const hidden=fixture(t,nativeDocs());
  const hiddenResult=await read(hidden,a,region,cap);
  assert.deepEqual(hidden.calls.map(c=>c.params.Action),['DescribeSubnets']);
  assert.equal(hiddenResult.routeTables.status,'unsupported');
  assert.equal(hiddenResult.gateways.status,'unsupported');
  const token=el('nextToken','a'.repeat(48));
  const malformed=fixture(t,{...nativeDocs(),DescribeRouteTables:tableDoc([], [token])});
  assert.deepEqual((await read(malformed)).routeTables,{status:'error',rows:[]});
  assert.equal(malformed.calls.length,3);
  const empty=fixture(t,{DescribeSubnets:subnetDoc([]),DescribeRouteTables:tableDoc([]),DescribeInternetGateways:gwDoc([])});
  const cleared=await read(empty);
  assert.deepEqual(cleared.routeTables,{status:'ready',rows:[]});
  assert.deepEqual(cleared.gateways,{status:'ready',rows:[]});
  assert.deepEqual(cleared.gatewayRoutes,{status:'ready',rows:[]});
});

test('refresh/unmount cancels pending old scope; even late transport replies cannot replace a new selection or region',async t=>{
  const late=defer();
  const firstRead=fixture(t,nativeDocs(),{delays:Object.fromEntries(actions.map(action=>[action,late]))});
  const reported=[];
  const originalScope=scopeFor(firstRead);
  const firstCycle=startVpcResourceMapRead(originalScope,receipt=>reported.push(receipt));
  assert.equal(firstRead.calls.length,3);
  firstCycle.cancel();
  assert.ok(firstRead.calls.every(call=>call.signal.aborted));
  const newRead=fixture(t,nativeDocs(otherRegion),{r:otherRegion});
  const currentScope=scopeFor(newRead,c,otherRegion);
  await startVpcResourceMapRead(currentScope,receipt=>reported.push(receipt)).done;
  late.resolve();
  await firstCycle.done;
  assert.equal(reported.length,1);
  assert.equal(reported[0].scope,currentScope);
  assert.equal(reported[0].snapshot.vpc.id,c.id);
  assert.deepEqual(reported[0].snapshot.subnets.rows.map(row=>row.id),[peerSub]);
});

const require=createRequire(import.meta.url);
const {outputFiles}=await build({
  absWorkingDir:fileURLToPath(new URL('..',import.meta.url)),
  entryPoints:['src/ec2-vpc-resource-map.jsx'],bundle:true,platform:'node',format:'cjs',write:false,loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'],define:{'process.env.NODE_ENV':'"production"'},
});
const runtime={exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,runtime,runtime.exports);
const {default:ResourceMap,VPCResourceMapContents:Contents}=runtime.exports;
const {createElement}=require('react'), {renderToStaticMarkup}=require('react-dom/server');
const markup=(View,props)=>renderToStaticMarkup(createElement(View,props));

test('real Cloudscape render supplies accessible textual, scoped EN and JP relationships without SVG-only dependence',async t=>{
  const snapshot=await read(fixture(t,nativeDocs()));
  const props={snapshot,region,available:{subnets:true,'route-tables':true,'internet-gateways':true}};
  const english=markup(Contents,props);
  const japanese=markup(Contents,{...props,t:(_,ja)=>ja});
  for(const term of ['aria-label="Subnets and route table associations"','Explicit association','Main route table, implicit',
      'Destination:','Routes to attached internet gateways','<ul', '<li>'])assert.ok(english.includes(term),term);
  for(const term of ['aria-label="サブネットとルートテーブルの関連付け"','明示的な関連付け','メインルートテーブルへの暗黙の関連付け','送信先:',
      '接続済みインターネットゲートウェイへのルート'])assert.ok(japanese.includes(term),term);
  assert.match(english,new RegExp('#/vpc/subnets/'+first+'\\?region=us-east-1&amp;vpc='+a.id));
  assert.match(english,new RegExp('#/vpc/internet-gateways/'+gateway+'\\?region=us-east-1'));
  for(const blocked of [{...props,navigationBlocked:true},{...props,available:{}}]) assert.doesNotMatch(markup(Contents,blocked),/<a\b/);
  assert.doesNotMatch(english,new RegExp(peerGateway+'|'+detached+'|NAT gateway|RunInstances|<svg'));
});

test('render labels failure honestly and does not mount a genuine read in an inactive/missing-access view',async t=>{
  const f=fixture(t,{...nativeDocs(),DescribeRouteTables:403});
  const snapshot=await read(f),props={snapshot,region,available:{subnets:true}};
  const denied=markup(Contents,props);
  assert.match(denied,/current identity cannot read this inventory/);
  assert.match(denied,/Association not confirmed/);
  assert.match(denied,/needed to confirm these relationships are not available/);
  assert.doesNotMatch(denied,/No route tables|No routes to attached/);
  const guard={request:()=>assert.fail('SSR must not touch the transport'),vpc:a,region,capabilities,authenticationRequired:true};
  assert.match(markup(ResourceMap,guard),/Loading resource map/);
  assert.equal(markup(ResourceMap,{...guard,active:false}),'');
  assert.equal(markup(ResourceMap,{...guard,authenticationRequired:false}),'');
});
