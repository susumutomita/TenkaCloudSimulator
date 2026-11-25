import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {transformSync} from 'esbuild';
import * as input from '../src/catalog-input.mjs';

const source = fs.readFileSync(new URL('../src/catalog.jsx', import.meta.url), 'utf8');
const compiled = transformSync(source, {loader: 'jsx', format: 'cjs', target: 'es2022'}).code;
const view = {exports: {}};
vm.runInNewContext(compiled, {module: view, exports: view.exports, URLSearchParams,
  require: id => id === './catalog-input.mjs' ? input : {}});
const {buildCatalogRequest, catalogReadSnapshot, catalogUpdateInput} = view.exports;
const awsModel = name => JSON.parse(fs.readFileSync(new URL(
  '../../../internal/aws/catalog/models/' + name + '.json', import.meta.url), 'utf8'));
const operation = (model, name, token, editable = []) => {
  const entry = model.operations[name];
  const shape = model.shapes[entry.input];
  assert.ok(shape.required.includes(token), name + ' must require the token');
  return {name, uri: entry.http.requestUri, method: entry.http.method, token, editable,
    members: Object.entries(shape.members).map(([name, member]) => ({
      name, type: model.shapes[member.shape].type, location: member.location,
      locationName: member.locationName, required: shape.required.includes(name)
    }))};
};
const request = (model, op, values) => buildCatalogRequest({
  protocol: model.metadata.protocol, jsonVersion: model.metadata.jsonVersion,
  targetPrefix: model.metadata.targetPrefix, signingName: model.metadata.signingName
}, op, values, 'us-east-1');

test('Firewall policy edit keeps the native sibling body and exact current token', () => {
  const model = awsModel('network-firewall');
  const update = operation(model, 'UpdateFirewallPolicy', 'UpdateToken', ['FirewallPolicy', 'Description']);
  const read = {wrapper: 'FirewallPolicyResponse'};
  const resource = {tokenWire: 'UpdateToken', operations: {update}};
  const body = {StatelessDefaultActions: ['aws:pass'], StatelessFragmentDefaultActions: ['aws:pass']};
  const native = {FirewallPolicyResponse: {FirewallPolicyName: 'demo', Description: 'before'},
    FirewallPolicy: body, UpdateToken: 'version-live', Unrelated: 'not an edit field'};
  const snapshot = catalogReadSnapshot(read, resource, native);
  assert.equal(snapshot.value.FirewallPolicy, body);
  assert.equal(snapshot.value.Unrelated, undefined);
  assert.equal(snapshot.value.UpdateToken, undefined);
  const changes = catalogUpdateInput(update, {FirewallPolicyName: 'demo'},
    {FirewallPolicy: snapshot.value.FirewallPolicy, Description: 'after'}, snapshot.token);
  const wire = request(model, update, changes);
  assert.equal(wire.options.headers['X-Amz-Target'], 'NetworkFirewall_20201112.UpdateFirewallPolicy');
  assert.deepEqual(JSON.parse(wire.options.body),
    {FirewallPolicyName: 'demo', FirewallPolicy: body, Description: 'after', UpdateToken: 'version-live'});
});

test('video stream and channel edits bind the wrapped version from their own read', () => {
  const model = awsModel('kinesisvideo');
  for (const [kind, name, key, values] of [
    ['StreamInfo', 'UpdateStream', 'StreamName', {MediaType: 'video/h264'}],
    ['ChannelInfo', 'UpdateSignalingChannel', 'ChannelARN', {SingleMasterConfiguration: {MessageTtlSeconds: 60}}]
  ]) {
    const update = operation(model, name, 'CurrentVersion', Object.keys(values));
    const read = {wrapper: kind};
    const resource = {tokenWire: 'Version', operations: {update}};
    const first = catalogReadSnapshot(read, resource, {[kind]: {[key]: 'target', Version: 'old'}});
    const latest = catalogReadSnapshot(read, resource, {[kind]: {[key]: 'target', Version: 'latest'}});
    const olderEdit = catalogUpdateInput(update, {[key]: 'target'}, values, first.token);
    const freshEdit = catalogUpdateInput(update, {[key]: 'target'}, values, latest.token);
    // No substitution with a newer response while an older editor is open;
    // the native API can reject its stale token. A new edit uses its own read.
    assert.equal(JSON.parse(request(model, update, olderEdit).options.body).CurrentVersion, 'old');
    assert.deepEqual(JSON.parse(request(model, update, freshEdit).options.body),
      {[key]: 'target', ...values, CurrentVersion: 'latest'});
  }
});

test('no write payload is built if the required token was absent or invalid', () => {
  const op = {token: 'CurrentVersion', members: [{name: 'CurrentVersion', type: 'string', required: true}]};
  const resource = {tokenWire: 'Version', operations: {update: op}};
  for (const Version of [undefined, null, '', ' ', 13, {}]) {
    const read = catalogReadSnapshot({wrapper: 'StreamInfo'}, resource, {StreamInfo: {Version}});
    assert.equal(catalogUpdateInput(op, {StreamName: 'demo'}, {MediaType: 'video/h264'}, read.token), null);
  }
  const missingWrapper = catalogReadSnapshot({wrapper: 'StreamInfo'}, resource, {Version: 'other'});
  assert.equal(catalogUpdateInput(op, {}, {}, missingWrapper.token), null);
  const plain = catalogUpdateInput({}, {Name: 'demo'}, {Description: 'updated'}, undefined);
  assert.deepEqual(JSON.parse(JSON.stringify(plain)), {Name: 'demo', Description: 'updated'});
});
