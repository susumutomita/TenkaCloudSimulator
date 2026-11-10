// Exercise the actual served configuration without a browser or network.
import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../../internal/server/apidocs/init.js', import.meta.url), 'utf8');
function fixture(fetch = () => { throw Error('Unexpected fetch'); }) {
  let config;
  vm.runInNewContext(source, {
    Scalar: {createApiReference(selector, options) {
      assert.equal(selector, '#reference'); config = options;
    }},
    SimulatorS3ResponseGuide: {wrapFetch: fn => fn},
    location: {origin: 'http://127.0.0.1:4566', href: 'http://127.0.0.1:4566/_simulator/docs/'},
    TextDecoder, URL, fetch,
  });
  return config;
}
function handler() {
  const config = fixture();
  assert.equal(config.plugins.length, 1);
  const plugin = config.plugins[0]();
  assert.equal(plugin.extensions.length, 0);
  assert.equal(plugin.apiClientPlugins.length, 1);
  assert.equal(plugin.apiClientPlugins[0].responseBody.length, 1);
  return plugin.apiClientPlugins[0].responseBody[0];
}

test('AWS JSON display is restricted to the two exact AWS JSON media types', () => {
  const renderer = handler();
  assert.deepEqual(Array.from(renderer.mimeTypes), [
    'application/x-amz-json-1.0', 'application/x-amz-json-1.1',
  ]);
  assert.equal(renderer.language, 'json');
  assert.equal(renderer.rawComponent, undefined);
  assert.equal(renderer.previewComponent, undefined);
  assert.equal(renderer.hooks, undefined);
});

test('UTF-8 AWS bodies remain exact text, with no precision loss or reserialization', () => {
  for (const body of [
    '{"Parameter":{"Name":"/docs/value","Type":"String","Value":"練習の値 😀","Version":1}}\n',
    '  { "Value": 900719925474099312345, "precise": 1.0000000000000001 }\r\n',
    '{"__type":"ParameterNotFound","message":"Not found"}',
    '\ufeff{"bom":true}', '[1,true,null,"text"]', 'false', '0', 'null', '"string"', '', '{malformed JSON',
    '{"Value":"</script><img src=x onerror=globalThis.injected=true> & <svg/onload=alert(1)>"}',
  ]) {
    const bytes = new TextEncoder().encode(body);
    const before = bytes.slice();
    assert.equal(handler().decode(bytes.buffer), body);
    assert.deepEqual(bytes, before);
  }
});

test('decoder uses only supplied bytes, without requiring credentials or response metadata', () => {
  const renderer = handler();
  const unavailable = new Proxy({}, {get() { throw Error('Response metadata read'); }});
  assert.equal(renderer.decode(new TextEncoder().encode('{"ok":true}').buffer, unavailable), '{"ok":true}');
});

test('same-origin fetch keeps the exact Response, arguments, bytes, status and headers', async () => {
  for (const type of ['application/x-amz-json-1.1', 'application/x-amz-json-1.0; charset=UTF-8', 'application/octet-stream']) {
    const bytes = new Uint8Array([0, 1, 127, 128, 255]);
    const response = new Response(bytes, {status: 418, headers: {'Content-Type': type, 'X-Test': 'preserved'}});
    const input = new URL('http://127.0.0.1:4566/');
    const init = {method: 'POST', body: '{}', headers: {'X-Amz-Target': 'AmazonSSM.GetParameter'}};
    const config = fixture((actualInput, actualInit) => {
      assert.equal(actualInput, input); assert.equal(actualInit, init); return Promise.resolve(response);
    });
    assert.equal(await config.customFetch(input, init), response);
    assert.equal(response.status, 418);
    assert.equal(response.headers.get('Content-Type'), type);
    assert.equal(response.headers.get('X-Test'), 'preserved');
    assert.deepEqual(new Uint8Array(await response.arrayBuffer()), bytes);
  }
});

test('same-origin restriction and privacy configuration are retained', async () => {
  let calls = 0;
  const config = fixture(() => { calls++; });
  await assert.rejects(config.customFetch('https://example.invalid/'), /Only this simulator origin/);
  assert.equal(calls, 0);
  assert.equal(config.persistAuth, false);
  assert.equal(config.telemetry, false);
  assert.equal(config.agent.disabled, true);
  assert.equal(config.proxyUrl, '');
});
