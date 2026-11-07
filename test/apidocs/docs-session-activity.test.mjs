import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFileSync} from 'node:fs';
import {createSessionActivity,sessionRenewalMessage} from '../../internal/server/web/session-activity.mjs';
const source=readFileSync(new URL('../../internal/server/apidocs/docs-session-activity.mjs',import.meta.url),'utf8');
function fixture(result){
 const events=new Map(),requests=[],doc={visibilityState:'visible',hasFocus:()=>true,addEventListener:(key,fn)=>events.set(key,fn),removeEventListener:key=>events.delete(key),defaultView:{addEventListener(){},removeEventListener(){}}};
 const env={createSessionActivity,sessionRenewalMessage};vm.createContext(env);vm.runInContext(source.replace(/^import[^\n]+;\n/m,'').replaceAll('export ',''),env);
 const activity=env.createDocsActivity(createSessionActivity,{document:doc,now:()=>100,fetch:async(path,options)=>{requests.push({path,options});return result;}});
 return {activity,events,requests};
}
test('docs explicit activity uses SAME helper cookie-only renewal once and no passive renewal',async()=>{
 const f=fixture({status:200,json:async()=>({expiresAt:'2026-11-07T00:15:00Z',hardExpiresAt:'2026-11-07T01:00:00Z'})});
 assert.equal((await f.activity.beforeExplicitSend()).status,'not-requested');assert.equal(f.requests.length,0);
 f.events.get('pointerdown')({type:'pointerdown',isTrusted:true});await f.activity.beforeExplicitSend();await f.activity.beforeExplicitSend();assert.equal(f.requests.length,1);
 assert.equal(f.requests[0].path,'/_simulator/session/renew');assert.equal(f.requests[0].options.credentials,'same-origin');assert.equal(f.requests[0].options.body,undefined);assert.deepEqual(f.requests[0].options.headers,{'X-Simulator-Session-Action':'renew'});
});
test('renewal failure is not converted to action retry; participant lifetime remains unchanged',async()=>{
 const f=fixture({status:401,json:async()=>({code:'session_required'})});f.events.get('keydown')({type:'keydown',isTrusted:true});await assert.rejects(f.activity.beforeExplicitSend(),e=>e.code==='session_required');await f.activity.beforeExplicitSend();assert.equal(f.requests.length,1);
 const p=fixture({status:403,json:async()=>({code:'participant_fixed_lifetime'})});p.events.get('input')({type:'input',isTrusted:true});assert.equal((await p.activity.beforeExplicitSend()).status,'participant');assert.equal(p.requests.length,1);
});
test('actual runner handler blocks API on renewal failure or stale selection; never replays',async()=>{
 const source=readFileSync(new URL('../../internal/server/apidocs/execution-ui.js',import.meta.url),'utf8');
 const start=source.indexOf("send.addEventListener('click',"),end=source.indexOf('\nfor(const [button,key]',start);const code=source.slice(start,end);
 async function run(renew){let handler;const calls=[],states=[];const env={caps:{regions:['us-east-1'],identitySelection:false},regions:{value:'us-east-1'},principal:{value:''},body:{value:'{}'},entry:()=>({service:'sqs',operation:'ListQueues'}),requestFor:()=>({path:'/',options:{method:'POST',body:'{}'}}),scope:()=> 'scope',revision:0,controller:null,busy:false,send:{addEventListener:(_name,fn)=>handler=fn},state:s=>states.push(s),docsSession:{beforeExplicitSend:()=>renew(env)},AbortController,fetch:async()=>{calls.push(true);return {status:200,ok:true,headers:{get:()=> 'application/json'}};},boundedResponse:async()=> '{}',responseMeta:{},responseBody:{dataset:{}},lastValues:new Map(),recordSQS(){},preview(){}};env.retire=()=>env.revision++;vm.createContext(env);vm.runInContext(code,env);await handler();return {calls,states};}
 const failure=await run(async()=>{const error=Error('private');error.name='SessionRenewalError';error.code='session_required';throw error;});assert.equal(failure.calls.length,0);assert.equal(failure.states.at(-1),'sessionEnded');
 for(const context of ['service','region','principal','draft']){
  const stale=await run(async env=>{if(context==='service')env.entry=()=>({service:'sns',operation:'Publish'});if(context==='region')env.regions.value='ap-northeast-1';if(context==='principal')env.principal.value='other';if(context==='draft')env.body.value='{"QueueUrl":"other"}';env.retire();});
  assert.equal(stale.calls.length,0,context+' changed while renewal pending');
 }
 const success=await run(async()=>{});assert.equal(success.calls.length,1);
});
test('actual Scalar fetch hook excludes passive reads, stops renewal failures and sends participant action once',async()=>{
 const init=readFileSync(new URL('../../internal/server/apidocs/init.js',import.meta.url),'utf8');
 async function fixture(fail){
  let hook;const calls=[],actions=[],status={};
  const env={URL,location:{href:'http://127.0.0.1:8888/_simulator/docs/',origin:'http://127.0.0.1:8888'},document:{getElementById:()=>status},SimulatorReferenceLocale:{get:()=> 'ja',subscribe(){}},SimulatorS3ResponseGuide:{wrapFetch:fn=>fn},SimulatorReferenceConfig:{create:(_previous,fn)=>{hook=fn;return {};}},Scalar:{createApiReference:()=>({})},SimulatorDocsSessionActivity:{beforeExplicitSend:async()=>{actions.push(true);if(fail)throw fail;return {status:'participant'};},message:()=> 'localized session error'},fetch:async(input)=>{calls.push(input);return {status:200};}};
  vm.createContext(env);vm.runInContext(init,env);return {hook,calls,actions,status};
 }
 const good=await fixture();
 for(const path of ['/_simulator/docs/openapi.json','/_simulator/v1/capabilities','/_simulator/session/renew'])await good.hook(path);
 assert.equal(good.actions.length,0);assert.equal(good.calls.length,3);
 await good.hook('/');await good.hook('/_simulator/providers/aws/');
 assert.equal(good.actions.length,2);assert.equal(good.calls.length,5);
 const failure=Error('not displayed');failure.code='session_required';const bad=await fixture(failure);
 await assert.rejects(bad.hook('/'),error=>error===failure);assert.equal(bad.actions.length,1);assert.equal(bad.calls.length,0);assert.equal(bad.status.textContent,'localized session error');
});
