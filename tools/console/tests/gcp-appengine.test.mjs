import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {transformSync} from 'esbuild';
import {serviceAvailable, servicesForProvider, serviceFromHash, hashForService, providerForService} from '../src/console-navigation.mjs';

const source = fs.readFileSync(new URL('../src/gcp-appengine.jsx', import.meta.url), 'utf8');
const compiled = transformSync(source, {loader: 'jsx', format: 'cjs', target: 'es2022'}).code;
const endpoint = '/_simulator/providers/gcp/appengine/';
const appEngine = {endpoint, project: 'tenkacloud-local', dataPlane: '/_simulator/providers/gcp/appspot/{hostname}/',
  locations: [{locationId: 'asia-northeast1', displayName: 'Tokyo'}, {locationId: 'us-central', displayName: 'Iowa'}], bounds: []};

// A minimal hook runtime: enough to render the view, run its effects and
// inspect the element tree without a browser.
function harness(props) {
  let slots = [], cursor = 0, effects = [], tree;
  const equal = (a, b) => a && b && a.length === b.length && a.every((x, i) => x === b[i]);
  const react = {
    createElement: (type, p, ...children) => ({type, props: p || {}, children}), Fragment: 'fragment',
    useState(initial) { const i = cursor++; if (!slots[i]) slots[i] = {value: typeof initial === 'function' ? initial() : initial}; return [slots[i].value, v => { slots[i].value = typeof v === 'function' ? v(slots[i].value) : v; }]; },
    useRef(value) { const i = cursor++; return slots[i] || (slots[i] = {current: value}); },
    useId() { return 'id'; }, useContext() { return null; },
    useEffect(fn, deps) { const i = cursor++; if (!slots[i] || !deps || !equal(slots[i].deps, deps)) { const old = slots[i]; slots[i] = {deps, cleanup: old?.cleanup}; effects.push(() => { old?.cleanup?.(); slots[i].cleanup = fn(); }); } },
  };
  const module = {exports: {}};
  vm.runInNewContext(compiled, {module, exports: module.exports, console, require: id => id === 'react' ? react : id === './i18n.jsx' ? {useLocale: () => ({t: (en, ja) => props.locale === 'ja' ? ja : en})} : id === './gcp-console-shell.jsx' ? {GCPConsoleContext: null} : {}});
  const render = () => { cursor = 0; tree = module.exports.default(props); for (const effect of effects.splice(0)) effect(); cursor = 0; tree = module.exports.default(props); return tree; };
  const all = (node = tree) => node && typeof node === 'object' ? [node, ...(node.children || []).flat(Infinity).flatMap(child => all(child))] : [];
  render();
  return {api: module.exports, render, all, text: () => JSON.stringify(tree)};
}
const flush = async h => { for (let i = 0; i < 5; i++) await new Promise(resolve => setImmediate(resolve)); h.render(); };
const plain = value => JSON.parse(JSON.stringify(value));
const response = (status, body) => ({status, text: async () => JSON.stringify(body)});

test('paths, hostnames and splits follow the official App Engine forms', () => {
  const {api} = harness({appEngine, request: async () => response(404, {})});
  assert.equal(api.appURL(endpoint, 'tenkacloud-local', 'services', 'a b', 'versions'), endpoint + 'v1/apps/tenkacloud-local/services/a%20b/versions');
  assert.equal(api.versionHost('v1', 'default', 'tenkacloud-local'), 'v1-dot-default-dot-tenkacloud-local.appspot.com');
  assert.equal(api.dataPlaneURL(appEngine.dataPlane, 'h.appspot.com'), '/_simulator/providers/gcp/appspot/h.appspot.com/');
  assert.equal(api.splitText({allocations: {v2: 0.25, v1: 0.75}}), 'v1 75%, v2 25%');
  assert.equal(api.percent(0.333), '33.3%');
  assert.deepEqual(plain(api.parseEnvironment('A=1\n\n B = x=y \n')), {A: '1', B: ' x=y'});
  for (const bad of ['=1', 'novalue', '1A=x', 'A-B=x']) assert.throws(() => api.parseEnvironment(bad));
  assert.deepEqual(plain(api.deployBody('v1', 'https://storage.googleapis.com/b/a.zip', {})), {id: 'v1', runtime: 'python312', deployment: {zip: {sourceUrl: 'https://storage.googleapis.com/b/a.zip'}}});
  assert.deepEqual(plain(api.deployBody('v1', 'u', {A: '1'}).envVariables), {A: '1'});
  assert.ok(api.notFound(Error('404: Apps instance not found')) && !api.notFound(Error('403: denied')));
});

test('a missing application offers creation and reloads only after the acknowledged write', async () => {
  const calls = [];
  let created = false;
  const h = harness({appEngine, request: async (path, options = {}) => {
    calls.push({path, method: options.method || 'GET', body: options.body});
    if (options.method === 'POST') { created = true; return response(200, {done: true}); }
    if (!created) throw Error('404: Apps instance not found: apps/tenkacloud-local.');
    if (path.includes('/services?')) return response(200, {services: [{id: 'default', split: {allocations: {v1: 1}}}]});
    if (path.includes('/ingressRules')) return response(200, {ingressRules: [{priority: 2147483647, action: 'ALLOW', sourceRange: '*'}]});
    if (path.includes('/versions')) return response(200, {versions: [{id: 'v1', runtime: 'python312', servingStatus: 'SERVING'}]});
    return response(200, {id: 'tenkacloud-local', locationId: 'us-central', servingStatus: 'SERVING', defaultHostname: 'tenkacloud-local.uc.r.appspot.com'});
  }});
  await flush(h);
  assert.match(h.text(), /Create an application/);
  assert.match(h.text(), /cannot be deleted/);
  const select = h.all().find(n => n.type === 'select');
  select.props.onChange({target: {value: 'us-central'}});
  h.render();
  const form = h.all().find(n => n.type === 'form');
  await form.props.onSubmit({preventDefault() {}});
  await flush(h);
  const post = calls.find(c => c.method === 'POST');
  assert.equal(post.path, endpoint + 'v1/apps');
  assert.deepEqual(JSON.parse(post.body), {id: 'tenkacloud-local', locationId: 'us-central'});
  assert.ok(calls.indexOf(post) < calls.length - 1, 'the view reloads after the write');
  assert.match(h.text(), /Application tenkacloud-local created/);
  assert.match(h.text(), /v1-dot-default-dot-tenkacloud-local.appspot.com/);
  assert.match(h.text(), /v1 100%/);
});

test('a refused write stays visible and does not reload', async () => {
  const calls = [];
  const h = harness({appEngine, locale: 'ja', request: async (path, options = {}) => {
    calls.push(options.method || 'GET');
    if (options.method === 'POST') throw Error('409: An App Engine application already exists');
    throw Error('404: Apps instance not found');
  }});
  await flush(h);
  const reads = calls.length;
  await h.all().find(n => n.type === 'form').props.onSubmit({preventDefault() {}});
  await flush(h);
  assert.equal(calls.length, reads + 1);
  assert.match(h.text(), /409: An App Engine application already exists/);
  assert.match(h.text(), /アプリケーションを作成/);
});

test('App Engine is a Google Cloud route only when its capability is advertised', () => {
  const base = {cloudStorage: {gcp: {project: 'tenkacloud-local'}}};
  assert.equal(serviceAvailable('gcp-appengine', base), false);
  assert.equal(serviceAvailable('gcp-appengine', {...base, gcpAppEngine: {endpoint, project: ''}}), false);
  const enabled = {...base, gcpAppEngine: appEngine};
  assert.equal(serviceAvailable('gcp-appengine', enabled), true);
  assert.equal(serviceAvailable('gcp-appengine', {...enabled, consoleScope: {region: 'us-east-1'}}), false);
  assert.deepEqual(servicesForProvider('gcp', enabled).map(s => s.id), ['gcp-storage', 'gcp-appengine']);
  assert.deepEqual(servicesForProvider('gcp', enabled, 'バージョン').map(s => s.id), ['gcp-appengine']);
  assert.deepEqual(servicesForProvider('aws', enabled, 'app engine'), []);
  assert.equal(serviceFromHash('#' + hashForService('gcp-appengine')), 'gcp-appengine');
  assert.equal(providerForService('gcp-appengine'), 'gcp');
});
