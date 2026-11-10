import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {transformSync} from 'esbuild';
const source=fs.readFileSync(new URL('../src/secret_description.jsx',import.meta.url),'utf8');
const compiled=transformSync(source,{loader:'jsx',format:'cjs',target:'es2022'}).code;
const arn='arn:aws:secretsmanager:us-east-1:000000000000:secret:training/description-AbCd12';
const name='training/description';
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b});return {promise,resolve,reject}};
function harness(props){
 let slots=[],cursor=0,effects=[],tree;
 const equal=(a,b)=>a&&b&&a.length===b.length&&a.every((x,i)=>x===b[i]);
 const react={createElement:(type,props,...children)=>({type,props:props||{},children}),useState(initial){const i=cursor++;if(!slots[i])slots[i]={value:typeof initial==='function'?initial():initial};return [slots[i].value,value=>{slots[i].value=typeof value==='function'?value(slots[i].value):value}]},useRef(value){const i=cursor++;return slots[i]||(slots[i]={current:value})},useMemo(fn,deps){const i=cursor++;if(!slots[i]||!equal(slots[i].deps,deps))slots[i]={deps,value:fn()};return slots[i].value},useLayoutEffect(fn,deps){const i=cursor++;if(!slots[i]||!equal(slots[i].deps,deps)){const old=slots[i];slots[i]={deps,cleanup:old?.cleanup};effects.push(()=>{old?.cleanup?.();slots[i].cleanup=fn()})}}};
 const module={exports:{}};
 vm.runInNewContext(compiled,{module,exports:module.exports,AbortController,console,require:id=>id==='react'?react:id==='./i18n.jsx'?{useLocale:()=>({t:(a,b)=>props.locale==='ja'?b:a})}:id});
 const render=()=>{cursor=0;tree=module.exports.default(props);for(const effect of effects.splice(0))effect();cursor=0;tree=module.exports.default(props);return tree};
 const all=(node=tree)=>node&&typeof node==='object'?[node,...(node.children||[]).flat(Infinity).flatMap(child=>all(child))]:[];
 const find=type=>all().filter(node=>node.type===type||node.type==='@cloudscape-design/components/'+type);
 const input=()=>find('input')[0],textarea=()=>find('textarea')[0],checkbox=()=>find('checkbox')[0];
 const change=(node,value)=>{node.props.onChange({detail:{value}});render()};
 render();
 return {api:module.exports,render,find,all,props,edit(value){change(textarea(),value)},arn(value){change(input(),value)},confirm(){checkbox().props.onChange({detail:{checked:true}});render()},submit(){return find('form')[0].props.onSubmit({preventDefault(){}})},refresh(){return find('button')[1].props.onClick()},unmount(){for(const slot of slots)slot?.cleanup?.()},text(){return JSON.stringify(tree)}};
}
const flush=async h=>{await new Promise(resolve=>setImmediate(resolve));h.render()};
function setup(handler){const calls=[];const h=harness({region:'us-east-1',secretARN:arn,request:async(path,options)=>{const call={path,...options,input:JSON.parse(options.body),operation:options.headers['X-Amz-Target']};calls.push(call);return handler(call,calls.length)}});return {h,calls}};
const reply=data=>({json:async()=>data});

test('strict physical ARN, Unicode bounds and metadata-only acknowledgment',()=>{
 const {h}=setup(()=>{});const api=h.api;
 assert.equal(api.descriptionName('us-east-1',arn),name);
 for(const id of [name,arn.replace('us-east-1','us-west-2'),arn+'!'])assert.equal(api.descriptionName('us-east-1',id),null);
 assert.ok(api.validDescription(''));assert.ok(api.validDescription('😀'.repeat(2048)));assert.ok(!api.validDescription('😀'.repeat(2049)));assert.ok(!api.validDescription('\uD800'));
 assert.ok(api.descriptionAck({ARN:arn,Name:name},arn,name));
 for(const data of [{ARN:arn,Name:name,SecretString:'never-render'},null,{ARN:arn,Name:'wrong'}])assert.ok(!api.descriptionAck(data,arn,name));
});
test('synchronous duplicate submits produce one native write, refresh only metadata',async()=>{
 const gate=deferred();const {h,calls}=setup(async call=>call.operation.endsWith('UpdateSecret')?gate.promise:reply({ARN:arn,Name:name,Description:'updated'}));
 h.edit('updated');h.confirm();h.submit();h.submit();assert.equal(calls.length,1);assert.deepEqual(calls[0].input,{SecretId:arn,Description:'updated'});
 assert.equal(calls[0].headers['X-Simulator-Region'],'us-east-1');
 gate.resolve(reply({ARN:arn,Name:name}));await flush(h);assert.equal(calls.length,2);assert.equal(calls[1].operation,'secretsmanager.DescribeSecret');assert.match(h.text(),/Description saved/);assert.match(h.text(),/updated/);
});
test('write-only caller sees acknowledged success separately from denied refresh; retry is read only',async()=>{
 const {h,calls}=setup(async call=>{if(call.operation.endsWith('UpdateSecret'))return reply({ARN:arn,Name:name});throw Error('AccessDenied')});
 h.edit('');h.confirm();h.submit();await flush(h);assert.match(h.text(),/Description saved, but metadata refresh failed/);
 h.refresh();await flush(h);assert.equal(calls.filter(c=>c.operation.endsWith('UpdateSecret')).length,1);assert.match(h.text(),/Description saved, but metadata refresh failed/);
});
test('failed or uncertain write never retries or falsely announces success',async()=>{
 const {h,calls}=setup(async()=>{throw Error('server payload secret must not render')});
 h.edit('text');h.confirm();h.submit();await flush(h);assert.match(h.text(),/update was not confirmed/);assert.doesNotMatch(h.text(),/server payload/);h.submit();assert.equal(calls.length,1);assert.doesNotMatch(h.text(),/Description saved/);
});
test('stale completed write cannot refresh or announce in changed context',async()=>{
 for(const field of ['region','request','secretARN','active','unmount']){
  const gate=deferred();const {h,calls}=setup(()=>gate.promise);h.edit('old');h.confirm();h.submit();const signal=calls[0].signal;
  if(field==='unmount')h.unmount();else{h.props[field]=field==='region'?'us-west-2':field==='request'?async()=>{throw Error('unexpected')}:field==='secretARN'?arn.replace('AbCd12','EfGh34'):false;h.render()}
  assert.ok(signal.aborted);gate.resolve(reply({ARN:arn,Name:name}));await flush(h);assert.equal(calls.length,1);assert.doesNotMatch(h.text(),/Description saved/);
 }
});
test('validation prevents requests; locale switch preserves draft and request count',()=>{
 const {h,calls}=setup(()=>{});h.submit();h.render();assert.equal(calls.length,0);assert.match(h.text(),/Confirm replacement/);h.edit('x'.repeat(2049));h.confirm();h.submit();assert.equal(calls.length,0);h.edit('日本語');h.props.locale='ja';h.render();assert.match(h.text(),/新しい説明/);assert.match(h.text(),/日本語/);assert.equal(calls.length,0);
});
test('new view contains no value retrieval or privilege override',()=>{
 for(const text of ['GetSecretValue','PutSecretValue','X-Simulator-Principal','localStorage','sessionStorage','SecretString','SecretBinary','ClientRequestToken'])assert.ok(!source.includes(text),text);
});
test('malformed write ACK cannot leak response fields or initiate metadata read',async()=>{
 const {h,calls}=setup(()=>reply({ARN:arn,Name:name,SecretString:'hidden-sentinel'}));
 h.edit('text');h.confirm();h.submit();await flush(h);assert.equal(calls.length,1);assert.match(h.text(),/not confirmed/);assert.doesNotMatch(h.text(),/hidden-sentinel/);
});
test('late metadata is discarded after ARN edit, including transports ignoring abort',async()=>{
 const gate=deferred();const {h,calls}=setup(()=>gate.promise);h.refresh();h.render();
 // Direct event exercises the epoch even if the browser disables this control.
 h.arn(arn.replace('AbCd12','EfGh34'));assert.ok(calls[0].signal.aborted);
 gate.resolve(reply({ARN:arn,Name:name,Description:'stale-sentinel'}));await flush(h);assert.doesNotMatch(h.text(),/stale-sentinel/);
});
test('failed reconciliation retains uncertain write fence until a successful metadata read',async()=>{
 let readable=false;
 const {h,calls}=setup(async call=>{
  if(call.operation.endsWith('UpdateSecret'))throw Error('connection lost');
  if(!readable)throw Error('DescribeSecret denied');
  return reply({ARN:arn,Name:name,Description:'reconciled'});
 });
 h.edit('proposed');h.confirm();h.submit();await flush(h);
 assert.match(h.text(),/update was not confirmed/);
 h.refresh();await flush(h);
 assert.match(h.text(),/update was not confirmed/);
 assert.ok(h.find('button')[0].props.disabled);
 h.submit();assert.equal(calls.filter(c=>c.operation.endsWith('UpdateSecret')).length,1);
 readable=true;h.refresh();await flush(h);
 assert.match(h.text(),/Current description loaded/);
 assert.ok(!h.find('button')[0].props.disabled);
 assert.equal(calls.filter(c=>c.operation.endsWith('UpdateSecret')).length,1);
});
