import test from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import {NATIVE,inventory,draft,requestFor,boundedResponse,MAX_BYTES,recordSQS,sqsValue} from '../../internal/server/apidocs/execution-contract.mjs';
const source=execFileSync('git',['show','HEAD:internal/server/server.go'],{encoding:'utf8'});
const caps={regions:['us-east-1','ap-northeast-1'],identitySelection:true,services:{}};
for(const service of Object.keys(NATIVE)){
 const match=source.match(new RegExp('"'+service+'": map\\[string\\]any\\{"operations": \\[\\]string\\{([^}]+)\\}'));
 assert.ok(match,'Actual capabilities declaration missing: '+service);
 caps.services[service]={operations:[...match[1].matchAll(/"([A-Za-z0-9]+)"/g)].map(m=>m[1])};
}
const context={region:'us-east-1',regions:caps.regions,identitySelection:true,principal:'dummy-reader'};
const entry=(service,operation)=>inventory(caps).entries.find(e=>e.service===service&&e.operation===operation);
test('all82 actual native capability operations have executable drafts, not just existing Scalar examples',()=>{
 const {entries,failures}=inventory(caps);assert.deepEqual(failures,[]);assert.equal(entries.length,82);
 for(const e of entries){const r=requestFor(e,draft(e,1000),context);assert.equal(r.path,'/');assert.equal(r.options.method,'POST');assert.ok(r.options.body);assert.equal(r.options.credentials,'same-origin');assert.equal(r.options.redirect,'error');}
 const before=JSON.stringify(caps);inventory(caps);assert.equal(JSON.stringify(caps),before);
});
test('missing/new/duplicate capability operations are hard visible coverage failures',()=>{
 for(const edit of [c=>c.services.sqs.operations.pop(),c=>c.services.sqs.operations.push('NewUnsupported'),c=>c.services.sqs.operations.push('CreateQueue'),c=>delete c.services.logs]){
  const c=structuredClone(caps);edit(c);assert.ok(inventory(c).failures.length);
 }
 const c=structuredClone(caps);c.services.newJSON={operations:['Unknown']};const r=inventory(c);assert.ok(r.failures.length);assert.ok(r.entries.some(e=>e.service==='newJSON'&&e.blocked));
});
test('selection reconstructs correct SQS Logs IAM CFN headers and never retains stale target',()=>{
 const cases=[['sqs','CreateQueue','application/x-amz-json-1.0','AmazonSQS.CreateQueue'],['logs','CreateLogGroup','application/x-amz-json-1.1','Logs_20140328.CreateLogGroup'],['sns','ListTopics','application/x-www-form-urlencoded',undefined],['iam','ListRoles','application/x-www-form-urlencoded',undefined],['cloudformation','DeleteStack','application/x-www-form-urlencoded',undefined]];
 let previous;
 for(const [s,o,type,target]of cases){const r=requestFor(entry(s,o),draft(entry(s,o)),context);assert.equal(r.options.headers['Content-Type'],type);assert.equal(r.options.headers['X-Amz-Target'],target);assert.equal(r.options.headers['X-Simulator-Service'],s);assert.notEqual(r.options.headers,previous);previous=r.options.headers;assert.equal(r.options.headers.Authorization,undefined);assert.equal(r.options.headers['X-Simulator-Token'],undefined);}
});
test('participant authority cannot gain principal header and region must be declared',()=>{
 const e=entry('sqs','ListQueues');assert.throws(()=>requestFor(e,'{}',{...context,identitySelection:false}),/IDENTITY/);
 const r=requestFor(e,'{}',{...context,identitySelection:false,principal:''});assert.equal(r.options.headers['X-Simulator-Principal'],undefined);
 assert.throws(()=>requestFor(e,'{}',{...context,region:'invented'}),/REGION/);
});
test('query operation/version cannot drift; JSON body stays user data',()=>{
 const e=entry('iam','ListRoles');assert.throws(()=>requestFor(e,'{"Action":"CreateRole","Version":"2010-05-08"}',context),/QUERY/);
 assert.throws(()=>requestFor(e,'{"Action":"ListRoles","Version":"2010-05-08","Unexpected":{}}',context),/QUERY/);
 const value={QueueUrl:'http://127.0.0.1:4566/actual',ReceiptHandle:'DUMMY_HANDLE',MessageBody:'日本語 data'};
 assert.deepEqual(JSON.parse(requestFor(entry('sqs','SendMessage'),JSON.stringify(value),context).options.body),value);
});
test('missing SSM templates and CFN Delete are meaningful first-party native drafts',()=>{
 for(const op of ['PutParameter','GetParameters','GetParametersByPath','DescribeParameters','DeleteParameter'])assert.ok(entry('ssm',op).template);
 assert.equal(entry('ssm','PutParameter').template.Type,'String');assert.equal(entry('cloudformation','DeleteStack').template.Action,'DeleteStack');
 assert.equal(JSON.parse(draft(entry('logs','PutLogEvents'),123456)).logEvents[0].timestamp,123456);
});
test('catalog JSON descriptor operations are exhaustive and missing binding is blocked',()=>{
 const c=structuredClone(caps);const service={model:'example',title:'Example',protocol:'json',jsonVersion:'1.1',targetPrefix:'ExampleAPI',signingName:'example',operations:['Create'],resources:[{operations:{create:{name:'Create',method:'POST',uri:'/',members:[{name:'Name',required:true,type:'string'}]}}}]};c.services.catalog={services:[service]};
 let r=inventory(c);assert.deepEqual(r.failures,[]);const e=r.entries.find(e=>e.service==='catalog:example');assert.equal(requestFor(e,draft(e),context).options.headers['X-Amz-Target'],'ExampleAPI.Create');
 service.operations.push('NotDescribed');r=inventory(c);assert.ok(r.failures.length);assert.ok(r.entries.find(e=>e.operation==='NotDescribed').blocked);
});
test('bounded request and response reject oversized data; actual response text is not HTML evaluated',async()=>{
 assert.throws(()=>requestFor(entry('sqs','SendMessage'),'x'.repeat(MAX_BYTES+1),context),/SIZE/);
 assert.equal(await boundedResponse(new Response('<script>dummy</script>')),'<script>dummy</script>');
 await assert.rejects(boundedResponse(new Response('x'.repeat(20)),10),/SIZE/);
});
test('first-party UI source retires stale replies, separates live response and syncs shared locale without persisting payload',()=>{
 const source=readFileSync(new URL('../../internal/server/apidocs/execution-ui.js',import.meta.url),'utf8');
 assert.ok(source.includes('generation!==revision'));assert.ok(source.includes('controller?.abort()'));
 assert.ok(source.includes('responseBody.textContent=raw'));assert.ok(!source.includes('innerHTML'));
 assert.ok(source.includes('localeControl.subscribe'));assert.ok(source.includes('localeControl.set(language.value)'));
 assert.equal((source.match(/localStorage\.setItem\(/g)||[]).length,0);
});

test('every service key is explicitly classified and an unknown empty service cannot vanish',()=>{
 const c=structuredClone(caps);c.services.s3={operations:['ListBuckets']};c.services.lambda={operations:['Invoke']};c.services.apigateway={operations:['GetRestApis']};c.providers=['aws','gcp','azure'];
 const r=inventory(c);assert.deepEqual(r.failures,[]);assert.deepEqual(r.dedicated,['s3','lambda','apigateway']);assert.deepEqual(r.otherProviders,['gcp','azure']);
 c.services.future={};assert.ok(inventory(c).failures.some(f=>f.includes('future')));assert.ok(inventory(c).entries.some(e=>e.service==='future'&&e.blocked));
});

test('SQS receipt is bound to actual Receive queue, never the later created queue; deletes invalidate',()=>{
 const cache=new Map(),scope='dummy-scope';
 recordSQS(cache,scope,'CreateQueue',{}, {QueueUrl:'queueA'});
 recordSQS(cache,scope,'ReceiveMessage',{QueueUrl:'queueA'},{Messages:[{ReceiptHandle:'handleA'}]});
 recordSQS(cache,scope,'CreateQueue',{}, {QueueUrl:'queueB'});
 assert.equal(sqsValue(cache,scope,'QueueUrl',{}),'queueB');
 assert.equal(sqsValue(cache,scope,'ReceiptHandle',{QueueUrl:'queueB'}),'');
 assert.equal(sqsValue(cache,scope,'ReceiptHandle',{QueueUrl:'queueA'}),'handleA');
 recordSQS(cache,scope,'DeleteMessage',{QueueUrl:'queueA',ReceiptHandle:'handleA'},{});
 assert.equal(sqsValue(cache,scope,'ReceiptHandle',{QueueUrl:'queueA'}),'');
 recordSQS(cache,scope,'ReceiveMessage',{QueueUrl:'queueB'},{Messages:[{ReceiptHandle:'handleB'}]});
 recordSQS(cache,scope,'DeleteQueue',{QueueUrl:'queueB'},{});
 assert.equal(sqsValue(cache,scope,'QueueUrl',{}),'');assert.equal(sqsValue(cache,scope,'ReceiptHandle',{QueueUrl:'queueB'}),'');
 cache.clear();assert.equal(sqsValue(cache,scope,'QueueUrl',{}),'');
});
test('encoded Query wire bytes enforce the bound even when JSON draft itself fits',()=>{
 const e=entry('iam','PutRolePolicy');const value={Action:e.operation,Version:e.target,RoleName:'dummy',PolicyName:'dummy',PolicyDocument:'あ'.repeat(40000)};
 const body=JSON.stringify(value);assert.ok(new TextEncoder().encode(body).length<MAX_BYTES);
 assert.throws(()=>requestFor(e,body,context),/SIZE/);
});
test('unknown catalog protocol is visible failed coverage; only known rest-json is excluded',()=>{
 const c=structuredClone(caps);c.services.catalog={services:[{model:'future',title:'Future',protocol:'future-json',operations:['Create']}]};
 const r=inventory(c);assert.ok(r.failures.length);assert.ok(r.entries.some(e=>e.service==='catalog:future'&&e.blocked));
 c.services.catalog.services[0].protocol='rest-json';assert.deepEqual(inventory(c).failures,[]);
});
test('actual load function ignores stale401 after new successful inventory and aborts old fetch',async()=>{
 const source=readFileSync(new URL('../../internal/server/apidocs/execution-ui.js',import.meta.url),'utf8');
 const code=source.slice(source.indexOf('async function load(){'),source.indexOf("\nsend.addEventListener"));assert.ok(code.startsWith('async function load'));
 const pending=[],states=[];const select=()=>({options:[],replaceChildren(){this.options=[];},value:''});
 const context={revision:0,controller:null,busy:false,lastValues:new Map(),caps:null,entries:[],failures:[],services:select(),operations:select(),regions:select(),principal:{parentElement:{},value:''},coverage:{},locale:'en',AbortController,
  state:key=>states.push(key),fetch:(_url,options)=>new Promise(resolve=>pending.push({resolve,options})),boundedResponse:async r=>JSON.stringify(r.data),inventory:()=>({entries:[],failures:[]}),el:(_tag,parent,attrs)=>{const n={value:attrs.value};parent.options.push(n);return n;},chooseService:()=>states.push('ready')};
 context.retire=()=>{context.revision++;context.controller?.abort();context.controller=null;};
 vm.createContext(context);vm.runInContext(code+';globalThis.runLoad=load;',context);
 const first=context.runLoad(),second=context.runLoad();assert.equal(pending[0].options.signal.aborted,true);
 pending[1].resolve({status:200,ok:true,data:{regions:['us-east-1'],services:{},identitySelection:false}});await second;
 pending[0].resolve({status:401,ok:false});await first;
 assert.equal(states.at(-1),'ready');assert.equal(states.filter(s=>s==='auth').length,0);
});
test('shared locale helper validates values, no echo, current-store ordering and blocked storage',()=>{
 const source=readFileSync(new URL('../../internal/server/apidocs/reference-locale.js',import.meta.url),'utf8');
 const window=new EventTarget(),values=new Map(),writes=[];
 const localStorage={getItem:key=>values.get(key)??null,setItem(key,value){writes.push([key,value]);values.set(key,value);}};
 const context={window,localStorage,CustomEvent};vm.createContext(context);vm.runInContext(source,context);const api=context.SimulatorReferenceLocale,seen=[];api.subscribe(v=>seen.push(v));
 assert.equal(api.set('ja'),true);assert.equal(api.get(),'ja');assert.equal(writes.length,1);assert.equal(api.set('bad'),false);
 window.dispatchEvent(new CustomEvent(api.event,{detail:vm.runInContext("({locale:'en'})",context)}));assert.equal(api.get(),'en');assert.equal(writes.length,1);
 function storage(key,newValue){const e=new Event('storage');Object.defineProperties(e,{key:{value:key},newValue:{value:newValue},storageArea:{value:localStorage}});window.dispatchEvent(e);}
 values.set(api.key,'ja');storage(api.key,'en');assert.equal(api.get(),'ja','queued oldevent must reread newer preference');
 values.set(api.key,'ja');storage(null,'ja');assert.equal(api.get(),'ja','malformed clear event must be ignored');
 values.delete(api.key);storage(null,null);assert.equal(api.get(),'en');storage(api.key,'invalid');assert.equal(api.get(),'en');
 const blocked={window:new EventTarget(),CustomEvent};Object.defineProperty(blocked,'localStorage',{get(){throw Error('blocked');}});vm.createContext(blocked);vm.runInContext(source,blocked);assert.equal(blocked.SimulatorReferenceLocale.set('ja'),true);assert.equal(blocked.SimulatorReferenceLocale.get(),'ja');
});
test('same-page locale events require exactly one own field on a plain object, without echo',()=>{
 const source=readFileSync(new URL('../../internal/server/apidocs/reference-locale.js',import.meta.url),'utf8');const window=new EventTarget(),writes=[];
 const env={window,CustomEvent,localStorage:{getItem:()=> 'en',setItem:(...args)=>writes.push(args)}};vm.createContext(env);vm.runInContext(source,env);const api=env.SimulatorReferenceLocale;
 for(const expression of ["['ja']","Object.assign(Object.create({}),{locale:'ja'})","({locale:'ja',extra:'ignored'})","Object.defineProperty({locale:'ja'},'hidden',{value:1})","Object.assign({locale:'ja'},{[Symbol('hidden')]:1})","({get locale(){throw Error('dummy')}})"]){
  window.dispatchEvent(new CustomEvent(api.event,{detail:vm.runInContext(expression,env)}));assert.equal(api.get(),'en');
 }
 window.dispatchEvent(new CustomEvent(api.event,{detail:vm.runInContext("({locale:'ja'})",env)}));assert.equal(api.get(),'ja');assert.equal(writes.length,0);
});

test('SNS nine drafts match exact native Query fields/version without JSON target',()=>{
 const actions=['CreateTopic','ListTopics','DeleteTopic','GetTopicAttributes','Subscribe','Unsubscribe','ListSubscriptions','ListSubscriptionsByTopic','Publish'];
 assert.deepEqual([...NATIVE.sns.operations],actions);
 const native=execFileSync('git',['show','HEAD:internal/aws/sns.go'],{encoding:'utf8'});
 assert.ok(native.includes('form.Get("Version") != "2010-03-31"'));assert.ok(native.includes('r.Header.Get("X-Amz-Target") != ""'));
 for(const operation of actions){const e=entry('sns',operation),request=requestFor(e,draft(e),context),fields=new URLSearchParams(request.options.body);assert.equal(fields.get('Action'),operation);assert.equal(fields.get('Version'),'2010-03-31');assert.equal(request.options.headers['X-Simulator-Service'],'sns');assert.equal(request.options.headers['X-Amz-Target'],undefined);}
 assert.equal(entry('sns','Subscribe').template.Protocol,'email');assert.ok(entry('sns','Publish').template.Message);assert.ok(entry('sns','Unsubscribe').template.SubscriptionArn.includes('REPLACE_WITH_ACTUAL'));
});
