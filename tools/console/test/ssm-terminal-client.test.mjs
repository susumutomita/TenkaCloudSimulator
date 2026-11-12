import test from 'node:test';
import assert from 'node:assert/strict';
import {createTerminalClient} from '../src/ssm-terminal-client.mjs';
function fixture(response={SessionId:'session_1',TokenValue:'x'.repeat(64),StreamUrl:'ws://127.0.0.1:4566/_simulator/v1/terminal-channel/session_1'}){
 const requests=[],sockets=[],output=[],states=[];
 const client=createTerminalClient({origin:'http://127.0.0.1:4566',onOutput:v=>output.push(v),onState:v=>states.push(v),request:async(url,init)=>{requests.push({url,init});return Response.json(response);},socket:url=>{const ws={url,sent:[],send(v){this.sent.push(JSON.parse(v));},close(){this.closed=true;}};sockets.push(ws);return ws;}});
 return {client,requests,sockets,output,states};
}
test('explicit start, first-frame token, input/output and termination',async()=>{
 const f=fixture();assert.equal(f.requests.length,0);await f.client.start('i-12345678');const ws=f.sockets[0];
 assert.equal(new URL(ws.url).search,'');assert.ok(!ws.url.includes('xxx'));ws.onopen();assert.equal(ws.sent[0].type,'open');
 ws.onmessage({data:JSON.stringify({type:'ready'})});f.client.input('hello\n');f.client.resize(24,80);
 assert.deepEqual(ws.sent[2],{type:'resize',rows:24,cols:80});
 ws.onmessage({data:JSON.stringify({type:'output',data:btoa('<script>not executable</script>')})});assert.equal(f.output[0],'<script>not executable</script>');
 await f.client.terminate();assert.equal(f.client.phase,'terminated');assert.equal(f.requests[1].init.headers['X-Amz-Target'],'AmazonSSM.TerminateSession');
});
test('foreign channel forbidden, known identity retained for explicit cleanup',async()=>{
 const f=fixture({SessionId:'session_1',TokenValue:'x'.repeat(64),StreamUrl:'ws://evil.invalid/channel'});
 await assert.rejects(f.client.start('i-12345678'));assert.equal(f.sockets.length,0);assert.equal(f.client.sessionId,'session_1');assert.throws(()=>f.client.start('i-12345678'));
});
test('no automatic reconnect, stale output discarded and explicit resume rotates channel',async()=>{
 const f=fixture();await f.client.start('i-12345678');const old=f.sockets[0];old.onopen();old.onmessage({data:'{"type":"ready"}'});old.onclose();assert.equal(f.requests.length,1);
 await f.client.resume();old.onmessage({data:'{"type":"output","data":"eA=="}'});assert.deepEqual(f.output,[]);assert.equal(f.requests.length,2);
});
test('bounded input/output and no send before ready',async()=>{
 const f=fixture();await f.client.start('i-12345678');assert.throws(()=>f.client.input('x'));const ws=f.sockets[0];ws.onopen();ws.onmessage({data:'{"type":"ready"}'});
 assert.throws(()=>f.client.input('x'.repeat(8193)));assert.throws(()=>f.client.resize(301,80));ws.onmessage({data:'{"type":"output","data":"!"}'});assert.equal(f.client.phase,'disconnected');
});

function deferredFixture() {
 const pending=[], requests=[], sockets=[], states=[], output=[];
 const client=createTerminalClient({origin:'http://127.0.0.1:4566', onOutput:v=>output.push(v),onState:v=>states.push(v),
  request:(url,init)=>{requests.push({url,init});return new Promise((resolve,reject)=>pending.push({resolve,reject}));},
  socket:url=>{const ws={url,sent:[],send(v){this.sent.push(JSON.parse(v));},close(){this.closed=true;}};sockets.push(ws);return ws;}});
 const receipt=(id='session_1')=>Response.json({SessionId:id,TokenValue:'x'.repeat(64),StreamUrl:`ws://127.0.0.1:4566/_simulator/v1/terminal-channel/${id}`});
 return {client,pending,requests,sockets,states,output,receipt};
}
test('pending Start after disposal cannot create a channel or publish late state',async()=>{
 const f=deferredFixture(), pending=f.client.start('i-12345678');
 f.client.dispose(); const states=[...f.states];
 f.pending[0].resolve(f.receipt()); await assert.rejects(pending,/disposed/);
 assert.deepEqual(f.states,states);assert.equal(f.sockets.length,0);assert.equal(f.client.sessionId,null);assert.equal(f.client.phase,'disconnected');
 assert.throws(()=>f.client.start('i-12345678'),/disposed/);assert.throws(()=>f.client.resume(),/disposed/);
 assert.throws(()=>f.client.input('x'),/disposed/);assert.throws(()=>f.client.resize(24,80),/disposed/);await assert.rejects(f.client.terminate(),/disposed/);
 assert.equal(f.requests.length,1);f.client.dispose();assert.deepEqual(f.states,states);
});
test('pending Resume after disposal retains known identity without reconnect or token send',async()=>{
 const f=deferredFixture();const start=f.client.start('i-12345678');f.pending[0].resolve(f.receipt());await start;
 const old=f.sockets[0];old.onopen();old.onmessage({data:'{"type":"ready"}'});old.onclose();
 const resume=f.client.resume();f.client.dispose();const states=[...f.states],sent=[...old.sent];
 f.pending[1].resolve(f.receipt('session_2'));await assert.rejects(resume,/disposed/);
 old.onopen();old.onmessage({data:'{"type":"output","data":"eA=="}'});old.onerror();old.onclose();
 assert.equal(f.client.sessionId,'session_1');assert.equal(f.sockets.length,1);assert.deepEqual(old.sent,sent);assert.deepEqual(f.states,states);assert.deepEqual(f.output,[]);assert.equal(f.requests.length,2);
});
test('closed channel fences late open and messages; normal explicit Resume still works',async()=>{
 const f=fixture();await f.client.start('i-12345678');const old=f.sockets[0];old.onclose();const states=[...f.states];
 old.onopen();old.onmessage({data:'{"type":"ready"}'});old.onerror();old.onclose();
 assert.deepEqual(old.sent,[]);assert.deepEqual(f.states,states);
 await f.client.resume();const current=f.sockets[1];current.onopen();current.onopen();assert.equal(current.sent.length,1);
 current.onmessage({data:'{"type":"ready"}'});f.client.input('ok');assert.equal(current.sent[1].type,'input');assert.equal(f.client.phase,'open');
 f.client.dispose();const after=[...f.states],sent=[...current.sent];current.onopen();current.onmessage({data:'{"type":"output","data":"eA=="}'});current.onerror();current.onclose();
 assert.deepEqual(f.states,after);assert.deepEqual(current.sent,sent);assert.deepEqual(f.output,[]);
});
test('late native rejection and termination response never publish after disposal',async()=>{
 const f=deferredFixture();const start=f.client.start('i-12345678');f.client.dispose();f.pending[0].reject(Error('transport refused'));
 await assert.rejects(start,/transport refused/);assert.deepEqual(f.states,[]);
 const g=deferredFixture();const ready=g.client.start('i-12345678');g.pending[0].resolve(g.receipt());await ready;
 const term=g.client.terminate();g.client.dispose();const states=[...g.states];g.pending[1].resolve(Response.json({}));await assert.rejects(term,/disposed/);
 assert.deepEqual(g.states,states);assert.equal(g.client.sessionId,'session_1');assert.equal(g.client.phase,'disconnected');assert.equal(g.requests.length,2);
});
