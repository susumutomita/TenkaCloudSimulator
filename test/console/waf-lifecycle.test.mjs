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

function saveFixture(){
  const events=[],requests=[];
  let acknowledge,refresh,started;
  const refreshStarted=new Promise(resolve=>{started=resolve});
  const write=new Promise(resolve=>{acknowledge=resolve});
  const read=new Promise(resolve=>{refresh=resolve});
  const context=vm.createContext({
    events,requests,
    call:async(operation,body)=>{requests.push({operation,body});await write},
    inspect:async()=>{events.push('refresh started');started();await read;events.push('refresh finished')},
    setMessage:value=>events.push(value),setBusy:()=>{},onSaving:()=>{},
    setError:value=>events.push(['error',value]),setConflict:()=>{},
  });
  const submit=source.slice(source.indexOf(' async function submit()'),source.indexOf(' const disabled='));
  assert.ok(submit.endsWith(' })}\n'),'Execute the complete shipped submit function');
  vm.runInContext(`
    const busy=false,alive={current:true},mode='rule',token='old-lock';
    const detail={Name:'acl',Id:'id',Rules:[],DefaultAction:{Allow:{}},VisibilityConfig:{MetricName:'acl'},Description:'Preserve me'};
    const form={name:'rule',priority:'0',action:'Count',statement:'{"ByteMatchStatement":{}}'};
    const visibility=name=>({MetricName:name});
    ${shippedLine(' async function mutate(operation)')}
    ${shippedLine(' async function update(patch)')}
    ${submit}
    globalThis.submit=submit;
  `,context);
  return {submit:context.submit,events,requests,acknowledge,refresh,refreshStarted};
}

test('rule save completion follows native acknowledgement and subsequent refresh',async()=>{
  const f=saveFixture();
  const pending=f.submit();
  assert.equal(f.requests.length,1);
  assert.equal(f.requests[0].operation,'UpdateWebACL');
  assert.equal(f.requests[0].body.Rules[0].Action.Count!==undefined,true);
  assert.equal(f.requests[0].body.Description,'Preserve me');
  assert.equal(f.events.includes('Rule saved.'),false);
  assert.equal(f.events.includes('refresh started'),false);
  f.acknowledge();
  // Await the actual boundary, without a timer or network-speed assumption.
  await f.refreshStarted;
  assert.equal(f.events.includes('refresh started'),true);
  assert.equal(f.events.includes('Rule saved.'),false);
  f.refresh();
  await pending;
  assert.deepEqual(f.events.slice(-2),['refresh finished','Rule saved.']);
});
