// First-party event/control fixtures. No browser, real extension or clipboard.
import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
const source=readFileSync(new URL('../../tools/console/src/login.jsx',import.meta.url),'utf8');
function body(name){
  const start=source.indexOf('  function '+name+'(');assert.notEqual(start,-1);
  const lineEnd=source.indexOf('\n',start);
  if(source.slice(start,lineEnd).endsWith('}'))return source.slice(start,lineEnd);
  const end=source.indexOf('\n  }',start);assert.notEqual(end,-1);
  return source.slice(start,end+4);
}
function fixture(value='',state=''){
  const input={isConnected:true,value};
  const context=vm.createContext({input,initial:state,clearTimeout(){},setTimeout(){return 1}});
  vm.runInContext(`let token=initial,error=null,fileState='loaded';
    const tokenInput={current:input}, inputRevision={current:0},pendingPaste={current:null},pasteTimer={current:null},fileRevision={current:0};
    function setToken(v){token=v} function setError(v){error=v} function setFileState(v){fileState=v}
    ${['clearPendingPaste','newIntent','changeToken','synchronizeNativeInput','flushPendingPaste'].map(body).join('\n')}
    globalThis.call={flush:flushPendingPaste,sync:synchronizeNativeInput,change:changeToken,
      read:()=>({token,revision:inputRevision.current,fileRevision:fileRevision.current,fileState,pending:pendingPaste.current}),
      queue:(v)=>pendingPaste.current={input,value:v,revision:inputRevision.current}};`,context);
  return {input,call:context.call};
}
test('submit snapshots native insertion when no component change event was delivered',()=>{
  const dummy='d'.repeat(64),f=fixture(dummy,'');
  assert.equal(f.call.flush(),dummy);assert.equal(f.call.read().token,dummy);
});
test('an intentionally empty native field never replays stale controlled credentials',()=>{
  const f=fixture('','d'.repeat(64));assert.equal(f.call.flush(),'');assert.equal(f.call.read().token,'');
});
test('capture/blur synchronization adopts native editing and invalidates pending file intent',()=>{
  const dummy='n'.repeat(64),f=fixture(dummy,'old');f.call.sync();
  assert.equal(f.call.read().token,dummy);assert.equal(f.call.read().revision,1);
  assert.equal(f.call.read().fileRevision,null);assert.equal(f.call.read().fileState,'');
  f.call.sync();assert.equal(f.call.read().revision,1);
});
test('queued explicit paste fallback is retained but subsequent editing cancels it',()=>{
  const f=fixture('','');f.call.queue('p'.repeat(64));assert.equal(f.call.flush(),'p'.repeat(64));
  const newer=fixture('','');newer.call.queue('p'.repeat(64));newer.input.value='e'.repeat(64);newer.call.sync();
  assert.equal(newer.call.flush(),'e'.repeat(64));assert.equal(newer.call.read().pending,null);
});
