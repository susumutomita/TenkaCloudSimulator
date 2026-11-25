import test from 'node:test';
import assert from 'node:assert/strict';
import {azureQueue, queueErrorCode, queueMaxText, queueMessageXML, queueReference, validQueueName} from '../../tools/console/src/azure-queue-requests.mjs';

const base = '/_simulator/providers/azure-queue/tenkacloudlocal/exact-queue';
const version = '2023-11-03', date = 'Thu, 19 Nov 2026 00:00:00 GMT';
const id = 'a4040fde-0bbd-4f2b-a093-c6af7a22a001', old = '+/original=', current = '+/replacement=';
const element = (localName, children = []) => ({localName, attributes: [], namespaceURI:null, nodeType:1, childNodes:children,
  textContent: children.map(child => child.textContent).join('')});
const value = (tag, text) => element(tag, [{nodeType:3, textContent:text}]);
const fields = (mode, text = 'a & b < c 月') => [
  value('MessageId', id), value('InsertionTime', date), value('ExpirationTime', date),
  ...(mode === 'peek' || mode === 'receive' ? [value('DequeueCount', mode === 'peek' ? '0' : '1'), value('MessageText', text)] : []),
  ...(mode === 'send' || mode === 'receive' ? [value('PopReceipt', old), value('TimeNextVisible', date)] : []),
];
const document = (...rows) => ({documentElement:element('QueueMessagesList', rows.map(row => element('QueueMessage', row)))});
function reply(status, body = null, extra = {}) {
  return new Response(body, {status, headers: {'x-ms-version':version, ...(body !== null ? {'Content-Type':'application/xml'} : {}), ...extra}});
}

test('native named-queue lifecycle has one path family, distinct query modes and current receipts', async () => {
  const script = [
    ['PUT', '', 201], ['PUT', '', 204], ['GET', '/messages?peekonly=true&numofmessages=8', 200, 'empty'],
    ['POST', '/messages?messagettl=-1&visibilitytimeout=0', 201, 'send', '<QueueMessage><MessageText>a &amp; b &lt; c 月</MessageText></QueueMessage>'],
    ['GET', '/messages?peekonly=true&numofmessages=8', 200, 'peek'],
    ['GET', '/messages?numofmessages=1&visibilitytimeout=30', 200, 'receive'],
    ['PUT', '/messages/'+id+'?popreceipt=%2B%2Foriginal%3D&visibilitytimeout=0', 204, null, undefined, {'x-ms-popreceipt':current, 'x-ms-time-next-visible':date}],
    ['DELETE', '/messages/'+id+'?popreceipt=%2B%2Freplacement%3D', 204], ['DELETE', '/messages', 204], ['DELETE','',204],
  ];
  const docs = new Map([['empty', document()], ...['send','peek','receive'].map(k => [k,document(fields(k))])]);
  const job = new AbortController();
  const client = azureQueue(async (path, init) => {
    assert.ok(script.length, 'unexpected call');
    const [method, suffix, status, xml, body, headers] = script.shift();
    assert.equal(path, base+suffix);
    assert.equal(init.method, method);
    assert.equal(init.signal, job.signal);
    assert.equal(init.redirect, 'error');
    assert.equal(init.credentials, 'same-origin');
    assert.deepEqual(init.headers, {'x-ms-version':version, ...(body === undefined ? {} : {'Content-Type':'application/xml'})});
    assert.equal(init.body, body);
    return reply(status, xml ?? null, headers);
  }, 'tenkacloudlocal', 'exact-queue', text => docs.get(text));
  assert.equal(await client.create(job.signal), true);
  assert.equal(await client.create(job.signal), false);
  assert.deepEqual(await client.peek('8',job.signal),[]);
  assert.equal((await client.send({text:'a & b < c 月', ttl:'-1', delay:'0'},job.signal)).id,id);
  const [peek] = await client.peek('8',job.signal);
  assert.equal(peek.count, '0');
  assert.equal(peek.text,'a & b < c 月');
  assert.equal('receipt' in peek,false);
  const [received] = await client.receive('1','30',job.signal);
  assert.equal(received.count,'1');
  assert.equal(received.receipt,old);
  const updated = await client.update(received,{visibility:'0'},job.signal);
  assert.deepEqual(updated,{receipt:current,visible:date});
  await client.remove({...received,...updated},job.signal);
  await client.clear(job.signal);
  await client.deleteQueue(job.signal);
  assert.equal(script.length,0);
  assert.equal(queueReference,'/_simulator/docs/azure-queue.html');
});

test('update accepts a replacement XML body but only reports a rotated receipt', async () => {
  const item = {id,receipt:old};
  const client = azureQueue(async (path, init) => {
    assert.equal(path,base+'/messages/'+id+'?popreceipt=%2B%2Foriginal%3D&visibilitytimeout=30');
    assert.equal(init.body,'<QueueMessage><MessageText>updated &#13;&amp;</MessageText></QueueMessage>');
    return reply(204,null, {'x-ms-popreceipt':old,'x-ms-time-next-visible':date});
  },'tenkacloudlocal','exact-queue');
  await assert.rejects(client.update(item,{visibility:30,text:'updated \r&'}),/InvalidResponse/);
});

test('unconfigured or invalid scope, number and payload never call the transport', async () => {
  const unreachable = () => assert.fail('transport must not be used');
  for (const name of ['','ab','my--queue','abc\n','-queue','my.queue','Aqueue']) {
    assert.equal(validQueueName(name),false);
    assert.throws(() => azureQueue(unreachable,'tenkacloudlocal',name),/InvalidQueueName/);
  }
  assert.throws(() => azureQueue(unreachable,undefined,'exact-queue'),/InvalidAccount/);
  assert.throws(() => azureQueue(unreachable,'external.site','exact-queue'),/InvalidAccount/);
  const client = azureQueue(unreachable,'tenkacloudlocal','exact-queue');
  await assert.rejects(client.peek(33),/InvalidNumber/);
  await assert.rejects(client.receive(1,0),/InvalidNumber/);
  await assert.rejects(client.receive(' ',30),/InvalidNumber/);
  await assert.rejects(client.send({text:'valid',ttl:'10',delay:'10'}),/InvalidLifetime/);
  await assert.rejects(client.send({text:'valid',ttl:0,delay:0}),/InvalidLifetime/);
  await assert.rejects(client.remove({id}),/MissingReceipt/);
  assert.doesNotThrow(() => queueMessageXML('月'.repeat(Math.floor(queueMaxText/3))));
  assert.throws(() => queueMessageXML('月'.repeat(Math.floor(queueMaxText/3)+1)),/MessageTooLarge/);
  assert.throws(() => queueMessageXML('\u0001'),/InvalidText/);
});

test('mismatched responses do not masquerade as a successful operation', async () => {
  function client(response, doc) {
    return azureQueue(async () => response,'tenkacloudlocal','exact-queue',() => doc);
  }
  await assert.rejects(client(reply(404)).peek(1),/HTTP404/);
  await assert.rejects(client(reply(200)).deleteQueue(),/InvalidResponse/);
  await assert.rejects(client(reply(204,null,{'x-ms-version':'wrong'})).deleteQueue(),/InvalidResponse/);
  await assert.rejects(client(reply(200,'xml'),document(fields('receive'))).peek(1),/InvalidResponse/);
  await assert.rejects(client(reply(200,'xml'),document(fields('peek'),fields('peek'))).peek(1),/InvalidResponse/);
  await assert.rejects(client(reply(200,'xml'),document(fields('peek'),fields('peek'))).peek(2),/InvalidResponse/);
  await assert.rejects(client(reply(200,'xml'),document(fields('peek').filter(x => x.localName!=='DequeueCount'))).peek(1),/InvalidResponse/);
  await assert.rejects(client(reply(200,'xml',{'Content-Type':'text/html'}),document()).peek(1),/InvalidResponse/);
  assert.equal(queueErrorCode(new Error('QueueNotFound: backend detail stays hidden')),'QueueNotFound');
  assert.equal(queueErrorCode(new Error('arbitrary body detail')),'RequestFailed');
});

test('an aborted job never exposes late backend success', async () => {
  const aborted = new AbortController();
  aborted.abort();
  const never = azureQueue(() => assert.fail('transport must not be used'),'tenkacloudlocal','exact-queue');
  await assert.rejects(never.create(aborted.signal),{name:'AbortError'});
  const late = new AbortController();
  const client = azureQueue(() => {late.abort();return reply(201);},'tenkacloudlocal','exact-queue');
  await assert.rejects(client.create(late.signal),{name:'AbortError'});
});
