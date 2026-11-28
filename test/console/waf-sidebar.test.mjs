import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {serviceAvailable,serviceFromHash} from '../../tools/console/src/console-navigation.mjs';
import {vpcConsoleRoute} from '../../tools/console/src/ec2-vpc-navigation.mjs';

// Execute the actual sidebar expressions without replacing React/Cloudscape UI
// acceptance. Keep the service menu independent from optional identity controls.
const source=readFileSync(new URL('../../tools/console/src/main.jsx',import.meta.url),'utf8');
const line=source.split('\n').find(line=>line.includes('navigation={<SideNavigation'));
assert.ok(line);
const items=line.match(/ items=\{(\[.*\])\}\/?>/)[1];
const follow=line.match(/ onFollow=\{(e=>\{.*?\})\} items=/)[1];
function sidebar(service,identitySelection,capabilities={}){
 const destinations=[],opened=[];
 const context=vm.createContext({service,identitySelection,capabilities,serviceAvailable,serviceFromHash,vpcConsoleRoute,principal:'dummy',t:english=>english,
  navigate:value=>destinations.push(value),open:(...args)=>opened.push(args)});
 return {items:JSON.parse(JSON.stringify(vm.runInContext(items,context))),
  follow:vm.runInContext('('+follow+')',context),destinations,opened};
}
for(const identitySelection of [true,false]){
 test(`WAF service links remain independent of identity controls (${identitySelection})`,()=>{
  const s=sidebar('wafv2',identitySelection);
  assert.deepEqual(s.items.filter(item=>item.type==='link').map(item=>[item.text,item.href]),[['Web ACLs','#/wafv2']]);
  assert.equal(s.items.some(item=>item.href==='#/cfn'),false);
  assert.equal(s.items.some(item=>item.type==='section'),identitySelection);
  let prevented=0;
  for(const href of ['#/wafv2','#'])s.follow({preventDefault:()=>prevented++,detail:{href}});
  assert.equal(prevented,2);
  assert.deepEqual(s.destinations,['wafv2','wafv2']);
  assert.deepEqual(s.opened,[]);
  const parameters=sidebar('ssm',identitySelection);
  for(const href of ['#/ssm','#/parameters'])parameters.follow({preventDefault(){},detail:{href}});
  assert.deepEqual(parameters.destinations,['ssm','ssm']);
  assert.equal(parameters.items.some(item=>item.href==='#/ssm-documents'),false);
  const capabilities={services:{ssm:{documentManagement:{definitionOnly:true,execution:false,operations:['ListDocuments','ListDocumentVersions','GetDocument']}}}};
  assert.equal(sidebar('ssm',identitySelection,capabilities).items.some(item=>item.href==='#/ssm-documents'),true);
  for(const [service,href] of [['ssm','#/parameters'],['cfn','#/cfn'],['apigateway','#/apigateway']]){
   assert.equal(sidebar(service,identitySelection).items[0].href,href);
  }
 });
}
