import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {transformSync} from 'esbuild';
import * as sourceModule from '../src/lambda-source.mjs';
const compiled=transformSync(fs.readFileSync(new URL('../src/lambda.jsx',import.meta.url),'utf8'),{loader:'jsx',format:'cjs'}).code;
function harness(handler){
 let leave=0,confirm=true;
 let slots=[],cursor=0,effects=[],tree;const calls=[];
 const equal=(a,b)=>a&&b&&a.length===b.length&&a.every((v,i)=>v===b[i]);
 const react={createElement:(type,props,...children)=>({type,props:props||{},children}),useState(initial){const i=cursor++;if(!slots[i])slots[i]={value:typeof initial==='function'?initial():initial};return [slots[i].value,value=>{slots[i].value=typeof value==='function'?value(slots[i].value):value}]},useRef(value){const i=cursor++;return slots[i]||(slots[i]={current:value})},useEffect(fn,deps){const i=cursor++;if(!slots[i]||!equal(slots[i].deps,deps)){const old=slots[i];slots[i]={deps,cleanup:old?.cleanup};effects.push(()=>{old?.cleanup?.();slots[i].cleanup=fn()})}}};
 const module={exports:{}};
 const props={region:'us-east-1',onSaving(){},onIAM(){leave++},request:async(path,options={})=>{calls.push({path,...options,body:options.body?JSON.parse(options.body):null});return handler(path,options)}};
 vm.runInNewContext(compiled,{module,exports:module.exports,AbortController,TextEncoder,TextDecoder,Uint8Array,Blob,URL,console,window:{addEventListener(){},removeEventListener(){},confirm:()=>confirm},require:id=>id==='react'?react:id==='./i18n.jsx'?{useLocale:()=>({t:a=>a})}:id==='./lambda-source.mjs'?sourceModule:id==='./iam-client.js'?{listNativeRoles:async()=>[]}:id==='./aws-child-notices.js'?{localizeAwsNotice:x=>x}:id==='./lambda-invoke.mjs'?{invokeFunction:async(...args)=>{calls.push({invoke:args});return {data:'ok',logTail:null}}}:id});
 const render=()=>{cursor=0;tree=module.exports.default(props);for(const effect of effects.splice(0))effect();cursor=0;tree=module.exports.default(props);return tree};
 const walk=node=>{if(!node||typeof node!=='object')return [];if(Array.isArray(node))return node.flatMap(walk);return [...(node.type?[node]:[]),...Object.values(node).flatMap(walk)]};
 const all=(node=tree)=>walk(node);
 const find=(type)=>all().filter(n=>n.type===type||n.type==='@cloudscape-design/components/'+type);
 const button=(label)=>find('button').find(n=>n.children.includes(label));
 const click=label=>{const n=button(label);assert.ok(n,label);n.props.onClick();render()};
 const flush=async()=>{await new Promise(r=>setImmediate(r));render()};
 const input=(label,value)=>{const field=find('form-field').find(n=>n.props.label===label);const n=all(field).find(n=>n.type==='@cloudscape-design/components/input');n.props.onChange({detail:{value}});render()};
 render();return {calls,render,find,button,click,flush,input,all,get leave(){return leave},setConfirm(value){confirm=value},inspect(name){find('table')[0].props.columnDefinitions[0].cell({FunctionName:name}).props.onFollow({preventDefault(){}});render()},edit(value){find('./python-editor.jsx')[0].props.onChange(value);render()},async create(){input('Function name','demo');input('Execution role ARN','arn:aws:iam::000000000000:role/exec');const form=find('form')[0];form.props.onSubmit({preventDefault(){}});await flush()},tab(id){find('tabs')[0].props.onChange({detail:{activeTabId:id}});render()}};
}
const cfg=revision=>({FunctionName:'demo',Handler:'lambda_function.lambda_handler',RevisionId:revision,Runtime:'python3.12'});
const reply=data=>({status:200,headers:new Headers(),json:async()=>data});
function setup(){let revision=1,fail=false;const h=harness(async(path,o)=>{if(path==='/_simulator/v1/iam/principals')return reply({principals:[]});if(o.method==='POST')return reply(cfg('r'+revision));if(o.method==='PUT'){if(fail)throw Error('Revision conflict');return reply(cfg('r'+(++revision)))}if(path.endsWith('/configuration'))return reply(cfg('r'+revision));return reply({Functions:[]})});return {h,fail:()=>fail=true,external:()=>revision++};}
test('create edited source, deploy with revision, test only deployed code',async()=>{
 const {h}=setup();await h.flush();h.click('Create function');h.edit('def lambda_handler(event, context): return 42');await h.create();
 const create=h.calls.find(c=>c.method==='POST');assert.ok(Buffer.from(create.body.Code.ZipFile,'base64').includes(Buffer.from('return 42')));
 assert.equal(h.find('tabs')[0].props.activeTabId,'code');h.edit('def lambda_handler(event, context): return 43');h.tab('test');assert.equal(h.button('Test').props.disabled,true);
 h.tab('code');h.click('Deploy code');await h.flush();const update=h.calls.find(c=>c.method==='PUT');assert.equal(update.body.RevisionId,'r1');assert.equal(update.path,'/_simulator/v1/lambda/demo/source');assert.equal(update.body.Source,'def lambda_handler(event, context): return 43');assert.equal(update.body.ZipFile,undefined);
 h.tab('test');assert.equal(h.button('Test').props.disabled,false);h.click('Test');await h.flush();assert.equal(h.calls.filter(c=>c.invoke).length,1);
});
test('failed deploy preserves draft and does not invoke or advance revision',async()=>{
 const {h,fail}=setup();await h.flush();h.click('Create function');await h.create();h.edit('print("draft")');fail();h.click('Deploy code');await h.flush();assert.equal(h.find('./python-editor.jsx')[0].props.value,'print("draft")');assert.equal(h.calls.filter(c=>c.invoke).length,0);h.click('Deploy code');await h.flush();assert.equal(h.calls.filter(c=>c.method==='PUT').at(-1).body.RevisionId,'r1');
});
test('ZIP update invalidates inline source instead of replacing other files later',async()=>{
 const {h}=setup();await h.flush();h.click('Create function');await h.create();h.find('file-upload')[0].props.onChange({detail:{value:[{size:3,arrayBuffer:async()=>Uint8Array.of(1,2,3).buffer}]}});h.render();h.click('Deploy');await h.flush();assert.equal(h.find('./python-editor.jsx').length,0);assert.equal(h.button('Deploy code'),undefined);
});
test('synchronous repeated create submits issue only one request',async()=>{
 let release;const wait=new Promise(r=>release=r);const h=harness(async(path,o)=>o.method==='POST'?(await wait,reply(cfg('r1'))):reply(path.includes('principals')?{principals:[]}:{Functions:[]}));await h.flush();h.click('Create function');const form=h.find('form')[0];form.props.onSubmit({preventDefault(){}});form.props.onSubmit({preventDefault(){}});assert.equal(h.calls.filter(c=>c.method==='POST').length,1);release();await h.flush();
});

test('create draft warns on IAM navigation and cancellation keeps the source',async()=>{
 const {h}=setup();await h.flush();h.click('Create function');h.edit('precious draft');h.setConfirm(false);h.click('Create role in IAM');assert.equal(h.leave,0);assert.equal(h.find('./python-editor.jsx')[0].props.value,'precious draft');assert.ok(h.button('Download source'));h.setConfirm(true);h.click('Create role in IAM');assert.equal(h.leave,1);
});
test('external revision keeps stale source visible and exportable but not deployable',async()=>{
 const {h,external}=setup();await h.flush();h.click('Create function');await h.create();h.edit('precious draft');h.click('Functions');external();h.inspect('demo');await h.flush();h.tab('code');assert.equal(h.find('./python-editor.jsx')[0].props.value,'precious draft');assert.equal(h.find('./python-editor.jsx')[0].props.readOnly,true);assert.equal(h.button('Deploy code').props.disabled,true);assert.ok(h.button('Download source'));h.setConfirm(false);h.click('Discard draft');assert.equal(h.find('./python-editor.jsx').length,1);h.setConfirm(true);h.click('Discard draft');assert.equal(h.find('./python-editor.jsx').length,0);h.tab('test');assert.equal(h.button('Test').props.disabled,false);
});

test('existing handler is read from saved source after a fresh mount and deployed without replacing ZIP',async()=>{
 const saved={...cfg('r8'),Handler:'index.handler',Path:'index.py',Source:'from helper import answer\ndef handler(e,c): return answer'};
 const h=harness(async(path,o)=>{
  if(path.endsWith('/configuration'))return reply(saved);
  if(path.endsWith('/source'))return reply(o.method==='PUT'?{...saved,RevisionId:'r9',Source:JSON.parse(o.body).Source}:saved);
  return reply({Functions:[saved]});
 });await h.flush();h.inspect('demo');await h.flush();h.tab('code');
 assert.equal(h.find('./python-editor.jsx')[0].props.value,saved.Source);assert.equal(h.find('./python-editor.jsx')[0].props.label,'index.py');
 h.edit('from helper import answer\ndef handler(e,c): return answer+1');h.click('Deploy code');await h.flush();
 const update=h.calls.find(c=>c.method==='PUT');assert.equal(update.body.RevisionId,'r8');assert.equal(update.body.ZipFile,undefined);assert.equal(h.button('Deploy code').props.disabled,true);
});
test('denied source read keeps configuration and ZIP path available without inventing code',async()=>{
 const h=harness(async(path)=>{if(path.endsWith('/configuration'))return reply(cfg('r1'));if(path.endsWith('/source'))throw Error('Access denied');return reply({Functions:[]})});
 await h.flush();h.inspect('demo');await h.flush();h.tab('code');assert.equal(h.find('./python-editor.jsx').length,0);assert.ok(h.button('Load saved source'));assert.ok(h.find('file-upload').length);assert.ok(h.find('alert').some(n=>n.children.includes('Access denied')));
});

test('a delayed saved-source read cannot repopulate code after a ZIP deployment',async()=>{
 let resolveSource;const delayed=new Promise(r=>resolveSource=r);
 const h=harness(async(path,o)=>{
  if(path.endsWith('/configuration'))return reply(cfg('r1'));
  if(path.endsWith('/source'))return delayed;
  if(o.method==='PUT')return reply(cfg('r2'));
  return reply({Functions:[]});
 });await h.flush();h.inspect('demo');await h.flush();h.tab('code');
 h.find('file-upload')[0].props.onChange({detail:{value:[{size:3,arrayBuffer:async()=>Uint8Array.of(1,2,3).buffer}]}});h.render();h.click('Deploy');await h.flush();
 resolveSource(reply({...cfg('r1'),Path:'lambda_function.py',Source:'old source'}));await h.flush();
 assert.equal(h.find('./python-editor.jsx').length,0);assert.equal(h.button('Load saved source').props.loading,false);
});
