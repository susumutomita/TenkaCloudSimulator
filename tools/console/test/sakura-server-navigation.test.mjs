import assert from 'node:assert/strict';
import test from 'node:test';
import {serviceAvailable, serviceFromHash, hashForService} from '../src/console-navigation.mjs';

const common = '/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/';
const sakura = {console: true, apiVersion: '1.1', service: 'IaaS Disk configuration', availabilityReported: false,
  endpoint: common + 'disk', operations: ['GET /disk','POST /disk','GET /disk/{id}','PUT /disk/{id}','DELETE /disk/{id}']};
const server = {console: true, apiVersion: '1.1', zone: 'is1a', availabilityReported: false,
  endpoint: common + 'server', operations: ['GET /server','GET /server/{id}']};
test('Server route requires a distinct, exact, enabled read-only capability', () => {
  assert.equal(serviceFromHash('#/sakura-servers'), 'sakura-servers');
  assert.equal(hashForService('sakura-servers'), '/sakura-servers');
  for (const invalid of [undefined, {...server, console: false}, {...server, endpoint: common + 'disk'},
      {...server, zone: 'is1b'}, {...server, operations: [...server.operations, 'POST /server']},
      {...server, availabilityReported: true}]) {
    assert.equal(serviceAvailable('sakura-servers', {providers: ['sakura'], sakura, sakuraServer: invalid}), false);
  }
  assert.equal(serviceAvailable('sakura-servers', {providers: ['sakura'], sakuraServer: server, consoleScope: {instanceId: 'i-local'}}), false);
  assert.equal(serviceAvailable('sakura-servers', {providers: ['sakura'], sakuraServer: server}), true);
  assert.equal(serviceAvailable('sakura-disks', {providers: ['sakura'], sakura}), true);
  assert.equal(serviceAvailable('sakura-disks', {providers: ['sakura'], sakuraServer: server}), false);
});
