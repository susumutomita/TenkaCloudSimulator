import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

// Execute the component's actual async action closures with injected references.
// This is not a React mount, DOM/browser test or a network request.
const source=readFileSync(new URL('../src/ssm-terminal.jsx',import.meta.url),'utf8');
const start=source.indexOf('  async function act(action){');
const end=source.indexOf('  return <main',start);
assert.ok(start>=0&&end>start,'component action projection anchors');
const project=new Function('client','setError','setPhase','fetch','location',source.slice(start,end)+';return {act,signOut};');
function fixture(){
 const connection={disposed:false,dispose(){this.disposed=true;}},client={current:connection},errors=[],phases=[],navigations=[],pending=[];
 const actions=project(client,e=>errors.push(e),p=>phases.push(p),()=>new Promise((resolve,reject)=>pending.push({resolve,reject})),{assign:u=>navigations.push(u)});
 return {connection,client,errors,phases,navigations,pending,...actions};
}
test('late action error is ignored after unmount or connection replacement',async()=>{
 for(const next of [null,{}]){
  const f=fixture();let reject;
  const work=f.act(connection=>{assert.equal(connection,f.connection);return new Promise((_,r)=>{reject=r;});});
  f.client.current=next;reject(Error('late failure'));await work;assert.deepEqual(f.errors,['']);
 }
});
test('mounted failed sign-out displays disconnected, never terminated or navigated',async()=>{
 const f=fixture(),work=f.act(f.signOut);assert.equal(f.connection.disposed,true);assert.deepEqual(f.phases,['disconnected']);
 f.pending[0].resolve({ok:false});await work;assert.match(f.errors.at(-1),/termination is not confirmed/);assert.deepEqual(f.navigations,[]);assert.deepEqual(f.phases,['disconnected']);
});
test('late successful sign-out cannot navigate after unmount or replacement',async()=>{
 for(const next of [null,{}]){
  const f=fixture(),work=f.act(f.signOut);f.client.current=next;f.pending[0].resolve({ok:true});await work;
  assert.deepEqual(f.navigations,[]);assert.deepEqual(f.errors,['']);assert.deepEqual(f.phases,['disconnected']);
 }
});
test('current successful sign-out navigates and current action failures remain visible',async()=>{
 const f=fixture(),work=f.act(f.signOut);f.pending[0].resolve({ok:true});await work;assert.deepEqual(f.navigations,['/_simulator/terminal']);
 const g=fixture();await g.act(()=>{throw Error('current refusal');});assert.deepEqual(g.errors,['','current refusal']);
});
test('component handlers capture the connection and disable retired-client actions',()=>{
 assert.match(source,/onClick=\{\(\)=>act\(signOut\)\}/);
 assert.match(source,/connection\.input\(input\+'\\n'\);if\(client\.current===connection\)setInput\(''\)/);
 for(const operation of ['start','resume','terminate','resize'])assert.ok(source.includes(`act(connection=>connection.${operation}(`));
 assert.equal((source.match(/disabled=\{client\.current\?\.disposed\|\|/g)||[]).length,3);
});
