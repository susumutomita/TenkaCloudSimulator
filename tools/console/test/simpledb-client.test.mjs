import test from 'node:test';
import assert from 'node:assert/strict';
import {simpleDBClient, validDomain, validText} from '../src/simpledb-client.mjs';
import {serviceAvailable, serviceFromHash, servicesForProvider} from '../src/console-navigation.mjs';

const ns = 'http://sdb.amazonaws.com/doc/2009-04-15/';
const signal = () => new AbortController().signal;
const xml = (localName, textContent = '', children = [], encoding = null) =>
  ({localName, namespaceURI: ns, textContent, children, getAttribute: field => field === 'encoding' ? encoding : null});
const root = (action, children = []) => xml(action + 'Response', '', [
  ...children, xml('ResponseMetadata', '', [xml('RequestId', '0'.repeat(32))]),
]);
const response = () => new Response('local xml', {headers: {'Content-Type': 'text/xml; charset=utf-8'}});

test('conditional update distinguishes empty string from absence and only ever uses local Query', async () => {
  const calls = [];
  const api = simpleDBClient(async (path, options) => {
    calls.push({path, ...options, fields: Object.fromEntries(options.body)});
    return response();
  }, () => ({documentElement: root('PutAttributes')}));
  const input = {name: 'revision', value: '空 & +', expected: '', mode: 'equal'};
  await api.putAttribute('versions', 'order +/1', input, signal());
  await api.putAttribute('versions', 'order +/1', {...input, expected: 'ignored', mode: 'absent'}, signal());
  assert.deepEqual(calls.map(({path, method, redirect, credentials, headers}) => ({path, method, redirect, credentials, headers})),
    Array(2).fill({path: '/', method: 'POST', redirect: 'error', credentials: 'same-origin',
      headers: {'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'sdb','X-Simulator-Region':'us-east-1'}}));
  assert.deepEqual(calls[0].fields, {Action: 'PutAttributes', Version: '2009-04-15', DomainName: 'versions',
    ItemName: 'order +/1', 'Attribute.1.Name':'revision', 'Attribute.1.Value':'空 & +',
    'Attribute.1.Replace':'true', 'Expected.Name':'revision', 'Expected.Exists':'true', 'Expected.Value':''});
  assert.equal(calls[1].fields['Expected.Exists'], 'false');
  assert.equal(Object.hasOwn(calls[1].fields, 'Expected.Value'), false);
  await assert.rejects(api.putAttribute('versions', 'row', {...input, mode: 'none'}, signal()), /valid conditional/);
  await assert.rejects(api.putAttribute('https://elsewhere.test', 'row', input, signal()), /valid local/);
  assert.equal(calls.length, 2);
});

test('the console reads native empty and base64 string values with strict response acknowledgement', async () => {
  const attribute = (name, content, encoding) => xml('Attribute', '', [xml('Name', name), xml('Value', content, [], encoding)]);
  let documentElement = root('GetAttributes', [xml('GetAttributesResult', '', [
    attribute('empty', ''), attribute('binary-like', 'ZgBv', 'base64'),
  ])]);
  const api = simpleDBClient(async (_, options) => {
    assert.equal(options.body.get('ConsistentRead'), 'true');
    return response();
  }, () => ({documentElement}));
  assert.deepEqual(await api.getAttributes('versions', 'row', signal()), [
    {id: 0, name: 'empty', value: ''}, {id: 1, name: 'binary-like', value: 'f\u0000o'},
  ]);
  documentElement = root('CreateDomain');
  await assert.rejects(api.getAttributes('versions', 'row', signal()), /Invalid local SimpleDB/);
  documentElement = root('GetAttributes', [xml('GetAttributesResult', '', [attribute('invalid', '%%%', 'base64')])]);
  await assert.rejects(api.getAttributes('versions', 'row', signal()), /Invalid local SimpleDB/);
  assert.equal(validText('水'.repeat(341)), true);
  assert.equal(validText('水'.repeat(342)), false);
  assert.equal(validDomain('abc\n'), false);
});

test('only one bounded domain page is loaded and a repeated continuation is rejected', async () => {
  const names = Array.from({length: 25}, (_, i) => xml('DomainName', 'domain-' + i));
  let token;
  const api = simpleDBClient(async (_, options) => {
    token = options.body.get('NextToken');
    assert.equal(options.body.get('MaxNumberOfDomains'), '25');
    return response();
  }, () => ({documentElement: root('ListDomains', [xml('ListDomainsResult', '', [...names, xml('NextToken','opaque')])])}));
  assert.deepEqual(await api.listDomains('', signal()), {domains: names.map(x => x.textContent), nextToken: 'opaque'});
  assert.equal(token, null);
  await assert.rejects(api.listDomains('opaque', signal()), /Invalid local SimpleDB/);
});

test('navigation is available only for the exact bounded local capability, never a handoff', () => {
  const simpledb = {console: true, conditionalPut: true, region: 'us-east-1', operations: ['ListDomains','CreateDomain','GetAttributes','PutAttributes']};
  const caps = {services: {simpledb}};
  assert.equal(serviceFromHash('#/simpledb'), 'simpledb');
  assert.ok(servicesForProvider('aws', caps, 'simpledb').some(x => x.id === 'simpledb'));
  assert.equal(serviceAvailable('simpledb', {...caps, consoleScope: {instanceId: 'i-test'}}), false);
  for (const missing of [{conditionalPut: false}, {region: 'ap-northeast-1'}, {operations: ['ListDomains']}]) {
    assert.equal(serviceAvailable('simpledb', {services: {simpledb: {...simpledb, ...missing}}}), false);
  }
});

test('row deletion requires the exact observed single value and never sends a whole-item delete', async () => {
  const calls = [];
  const api = simpleDBClient(async (url, options) => {
    calls.push([url, Object.fromEntries(options.body), options.headers]);
    return response();
  }, () => ({documentElement: root('DeleteAttributes')}));
  await api.deleteAttribute('versions', ' row ', {name:'revision', value:''}, signal());
  assert.deepEqual(calls[0][1], {Action:'DeleteAttributes', Version:'2009-04-15', DomainName:'versions',
    ItemName:' row ', 'Attribute.1.Name':'revision', 'Attribute.1.Value':'', 'Expected.Name':'revision', 'Expected.Value':''});
  assert.equal(calls[0][0], '/');
  assert.equal(calls[0][2]['X-Simulator-Service'], 'sdb');
  await assert.rejects(api.deleteAttribute('versions', 'row', {name:'revision'}, signal()), /valid attribute/);
  await assert.rejects(api.deleteAttribute('versions', 'row', {name:'',value:'x'}, signal()), /valid attribute/);
  assert.equal(calls.length, 1);
});

const result = (names, token) => root('Select', [xml('SelectResult', '', [
  ...names.map(name => xml('Item', '', [typeof name === 'string' ? xml('Name', name) : name])),
  ...(token === undefined ? [] : [xml('NextToken', token)]),
])]);

test('Select is a read-only names projection on the existing local domain; every page reuses the exact scoped query', async () => {
  const calls = [];
  const pages = [result([], 'scan-1'), result([xml('Name', 'eAB5', [], 'base64')], 'scan-2'), result(['last'])];
  const api = simpleDBClient(async (url, options) => {
    assert.equal(url, '/'); assert.equal(options.method, 'POST'); assert.equal(options.credentials, 'same-origin'); assert.equal(options.redirect, 'error');
    assert.deepEqual(options.headers, {'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'sdb','X-Simulator-Region':'us-east-1'});
    calls.push(Object.fromEntries(options.body)); return response();
  }, () => ({documentElement: pages.shift()}));
  const page1 = await api.selectNames('shop-demo', 'bad`attr', "a' & +", signal());
  assert.deepEqual(page1.names, []); assert.ok(page1.next);
  const page2 = await page1.next(signal()); assert.deepEqual(page2.names, ['x\0y']);
  const page3 = await page2.next(signal()); assert.deepEqual(page3, {names:['last'], next:null});
  const fields = {Action:'Select', Version:'2009-04-15', SelectExpression:"select itemName() from `shop-demo` where `bad``attr` = 'a'' & +' limit 25", ConsistentRead:'true'};
  assert.deepEqual(calls, [fields, {...fields, NextToken:'scan-1'}, {...fields, NextToken:'scan-2'}]);
});

test('Select refuses out-of-scope inputs, repeated cursors, invalid acknowledgements and excessive pages', async () => {
  let current = result([], 'cursor'), calls = [];
  const api = simpleDBClient(async (_, opt) => {calls.push(Object.fromEntries(opt.body)); return response();}, () => ({documentElement:current}));
  for (const [d,k,v] of [['https://outside.test','a','b'], ['demo','','ignored'], ['demo','水'.repeat(342),'a'],['demo','a','0'.repeat(1025)]])
    await assert.rejects(api.selectNames(d,k,v,signal()), /valid local/);
  assert.equal(calls.length, 0);
  const page = await api.selectNames('demo','','',signal());
  assert.equal(calls[0].SelectExpression,'select itemName() from `demo` limit 25');
  await assert.rejects(page.next(signal()), /Invalid local/);
  current = result(['matches']);
  await api.selectNames('demo','color','', signal());
  assert.equal(calls.at(-1).SelectExpression,"select itemName() from `demo` where `color` = '' limit 25");
  for (const bad of [result(['same','same']), result(Array.from({length:26}, (_,i)=>String(i))),
    result(['']), result([], 'X'.repeat(8193)), result([xml('Name','%',[],'base64')]),
    root('Select', [xml('SelectResult','',[xml('Item','',[xml('Name','ok'),xml('Attribute')])])]), root('GetAttributes')]) {
    current = bad; await assert.rejects(api.selectNames('demo','','',signal()), /Invalid local/);
  }
});

test('Select rejects a late response even from a transport that ignored abort, and never retries a denial', async () => {
  let release;
  const controller = new AbortController();
  const api = simpleDBClient(() => new Promise(resolve => {release=resolve;}), () => ({documentElement: result(['stale'], 'old')}));
  const late = api.selectNames('old-domain','','',controller.signal);
  controller.abort(); release(response());
  await assert.rejects(late, {name:'AbortError'});
  let calls=0;
  const denied = simpleDBClient(async () => { ++calls; return new Response(null,{status:403}); }, () => assert.fail('parse on denial'));
  await assert.rejects(denied.selectNames('demo','','',signal()), /403/); assert.equal(calls,1);
});
