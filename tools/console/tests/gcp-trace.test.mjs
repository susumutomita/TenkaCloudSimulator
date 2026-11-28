import test from 'node:test';
import assert from 'node:assert/strict';
import {cloudTrace, spanObservations, traceConfiguration} from '../src/gcp-trace.mjs';
import {serviceAvailable, servicesForProvider, serviceFromHash, providerForService, traceCapabilitiesForPrincipal} from '../src/console-navigation.mjs';

const project = 'trace-local';
const id = 'a'.repeat(32);
const cap = {project, endpoint: '/_simulator/providers/gcp/cloudtrace/v1', reference: '/_simulator/docs/gcp-trace.html',
  methods: ['patchTraces', 'get', 'list'], views: ['MINIMAL', 'ROOTSPAN', 'COMPLETE']};
const range = {startTime: '2026-11-28T00:00:00.123456789+09:00', endTime: '2026-11-28T01:00:00.000000001+09:00'};
const span = {spanId: '18446744073709551615', parentSpanId: '0', name: 'saved observation',
  startTime: range.startTime, endTime: range.endTime, labels: {note: '<span>saved</span>', url: 'https://example.invalid/'}};
const full = {projectId: project, traceId: id, spans: [span, {...span, name: 'second observation'}]};
const response = data => ({json: async () => data});

test('Trace navigation requires the exact owner discovery and hides absent, malformed and handoff scopes', () => {
  assert.equal(traceConfiguration(cap), cap); // No console flag is required before integration.
  assert.equal(serviceFromHash('#/gcp-trace'), 'gcp-trace');
  assert.equal(providerForService('gcp-trace'), 'gcp');
  assert.equal(serviceAvailable('gcp-trace', {gcpTrace: cap}), true);
  assert.equal(serviceAvailable('gcp-trace', {gcpTrace: cap, consoleScope: {parameterName: '/owned'}}), false);
  for (const broken of [undefined, {...cap, console: false}, {...cap, project: '../other'},
    {...cap, endpoint: 'https://example.invalid'}, {...cap, reference: '//example.invalid'},
    {...cap, methods: ['patchTraces']}, {...cap, views: ['COMPLETE']}]) {
    assert.equal(traceConfiguration(broken), null);
    assert.equal(serviceAvailable('gcp-trace', {gcpTrace: broken}), false);
    assert.equal(servicesForProvider('gcp', {gcpTrace: broken}).some(service => service.id === 'gcp-trace'), false);
    assert.throws(() => cloudTrace(() => assert.fail('invalid scope issued a request'), broken));
  }
});

test('principal or discovery generation changes hide previous owner metadata until matched discovery arrives', () => {
  const session = {gcpTrace: cap, services: {s3: true}};
  assert.equal(traceCapabilitiesForPrincipal(session, 0, 'owner', null), session);
  for (const stale of [null, {epoch: 1, principal: 'owner', trace: cap}, {epoch: 2, principal: 'other', trace: cap}]) {
    const current = traceCapabilitiesForPrincipal(session, 2, 'owner', stale);
    assert.equal(current.gcpTrace, undefined);
    assert.equal(serviceAvailable('gcp-trace', current), false);
    assert.equal(current.services, session.services);
  }
  assert.equal(traceCapabilitiesForPrincipal(session, 2, 'owner', {epoch: 2, principal: 'owner', trace: cap}).gcpTrace, cap);
  assert.equal(traceCapabilitiesForPrincipal(session, 2, 'other', {epoch: 2, principal: 'other'}).gcpTrace, undefined);
});

test('list is GET-only MINIMAL page size25 and preserves RFC3339 precision and opaque paging bytes', async () => {
  const calls = [], signal = new AbortController().signal;
  const api = cloudTrace(async (path, options) => { calls.push({path, options}); return response({traces: [{projectId: project, traceId: id}], nextPageToken: 'next-token'}); }, cap);
  assert.deepEqual(Object.keys(api).sort(), ['get', 'list']);
  const result = await api.list(range, 'opaque+/=', signal);
  assert.equal(result.traces[0].traceId, id);
  const url = new URL(calls[0].path, 'http://127.0.0.1:8080');
  assert.equal(url.pathname, cap.endpoint + '/projects/' + project + '/traces');
  assert.deepEqual(Object.fromEntries(url.searchParams), {...range, view: 'MINIMAL', pageSize: '25', pageToken: 'opaque+/='});
  assert.deepEqual(calls[0].options, {method: 'GET', redirect: 'error', signal});
});

test('full get binds the selected identity and retains duplicate span observations and exact decimal strings', async () => {
  const calls = [];
  const api = cloudTrace(async (path, options) => { calls.push({path, options}); return response(full); }, cap);
  const result = await api.get(id);
  assert.equal(calls[0].path, cap.endpoint + '/projects/' + project + '/traces/' + id);
  assert.equal(calls[0].options.method, 'GET');
  assert.equal(calls[0].options.body, undefined);
  assert.deepEqual(result.observations.map(row => row.observation), [1, 2]);
  assert.deepEqual(result.observations.map(row => row.spanId), [span.spanId, span.spanId]);
  assert.equal(result.observations[0].parentSpanId, '0');
  assert.equal(result.observations[0].startTime, range.startTime);
  assert.equal(result.observations[0].endTime, range.endTime);
  assert.deepEqual(result.observations[0].labels, span.labels);
  assert.notEqual(result.observations[0].labels, span.labels);
  assert.equal(full.spans.length, 2);
});

test('malformed IDs, precision-losing numbers, cross-project responses and bad paging fail closed', async () => {
  for (const value of [Number(span.spanId), '18446744073709551616', '01', '0', '1e3', '-1']) {
    assert.throws(() => spanObservations([{...span, spanId: value}]), /decimal strings/);
  }
  assert.throws(() => spanObservations([{...span, parentSpanId: 0}]), /decimal strings/);
  for (const data of [{...full, projectId: 'other-project'}, {...full, traceId: 'b'.repeat(32)}, {...full, spans: undefined}]) {
    await assert.rejects(cloudTrace(async () => response(data), cap).get(id));
  }
  for (const data of [{}, {traces: [{projectId: 'other-project', traceId: id}]},
    {traces: [{projectId: project, traceId: id}, {projectId: project, traceId: id}]}, {traces: [], nextPageToken: ''}]) {
    await assert.rejects(cloudTrace(async () => response(data), cap).list(range));
  }
  let calls = 0;
  const api = cloudTrace(async () => { calls++; return response({traces: []}); }, cap);
  await assert.rejects(api.get('../other'));
  await assert.rejects(api.list({...range, startTime: ''}));
  await assert.rejects(api.list(range, 'x'.repeat(2049)));
  assert.equal(calls, 0);
});

test('abort before dispatch or after a late read prevents a result; native read errors remain errors', async () => {
  const early = new AbortController(); early.abort();
  await assert.rejects(cloudTrace(() => assert.fail('aborted request dispatched'), cap).get(id, early.signal), {name: 'AbortError'});
  let finish;
  const pending = new Promise(resolve => { finish = resolve; });
  const controller = new AbortController();
  const api = cloudTrace(() => pending, cap);
  const read = api.get(id, controller.signal);
  controller.abort(); finish(response(full));
  await assert.rejects(read, {name: 'AbortError'});
  await assert.rejects(cloudTrace(async () => { throw Error('403: owner access refused'); }, cap).list(range), /403/);
});
