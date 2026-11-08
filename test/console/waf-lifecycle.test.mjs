// Execute the shipped async mutation and unmount effect with first-party state
// callbacks. This is a lifecycle contract check, not a browser/React acceptance.
import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source=readFileSync(new URL('../../tools/console/src/wafv2.jsx',import.meta.url),'utf8');
function shippedLine(prefix){
  const matches=source.split('\n').filter(line=>line.startsWith(prefix));
  assert.equal(matches.length,1,'The shipped lifecycle boundary must remain uniquely testable');
  return matches[0];
}
function fixture(){
  const saving=[],state=[];
  const context=vm.createContext({AbortController,onSaving:value=>saving.push(value),state});
  vm.runInContext(`
    const alive={current:true},generation={current:0},abort={current:new AbortController()};
    const busy=false;let cleanup;
    const load=()=>{},useEffect=effect=>{cleanup=effect()};
    const setBusy=value=>state.push(['busy',value]);
    const setError=value=>state.push(['error',value]);
    const setConflict=value=>state.push(['conflict',value]);
    ${shippedLine(' useEffect(()=>{load();')}
    ${shippedLine(' async function mutate(operation)')}
    globalThis.harness={mutate,unmount:()=>cleanup(),signal:abort.current.signal};
  `,context);
  return {...context.harness,saving,state};
}

test('mounted WAF mutation releases parent saving on success and failure',async()=>{
  for(const failure of [false,true]){
    const f=fixture();
    await f.mutate(async()=>{if(failure)throw Error('dummy lock changed')});
    assert.deepEqual(f.saving,[true,false]);
    assert.equal(f.state.at(-1)[0],'busy');
    assert.equal(f.state.at(-1)[1],false);
  }
});

test('unmount releases saving immediately and an old completion cannot clear a new view save',async()=>{
  for(const failure of [false,true]){
    const f=fixture();let resolve,reject;
    const operation=new Promise((yes,no)=>{resolve=yes;reject=no});
    const pending=f.mutate(()=>operation);
    assert.deepEqual(f.saving,[true]);
    f.unmount();
    assert.equal(f.signal.aborted,true);
    assert.deepEqual(f.saving,[true,false]);
    const oldStateWrites=f.state.length;
    f.saving.push(true); // A newly mounted service starts an independent save.
    if(failure)reject(Error('dummy cancelled request'));else resolve();
    await pending;
    assert.deepEqual(f.saving,[true,false,true]);
    assert.equal(f.state.length,oldStateWrites,'No state writes after unmount');
  }
});
