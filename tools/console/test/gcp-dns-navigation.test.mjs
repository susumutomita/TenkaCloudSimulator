import assert from 'node:assert/strict';
import test from 'node:test';
import {dnsCapabilitiesForPrincipal, serviceAvailable, serviceDescriptor} from '../src/console-navigation.mjs';

const dns = {console: true, api: 'dns.v1', project: 'project-one',
  endpoint: '/_simulator/providers/gcp/dns/v1/projects/project-one/managedZones',
  operations: ['managedZones.list', 'managedZones.create', 'managedZones.get', 'resourceRecordSets.list']};

test('Cloud DNS requires its exact owner-only native capability and bounded paths', () => {
  assert.equal(serviceDescriptor('gcp-dns').route, '/gcp-dns');
  assert.equal(serviceAvailable('gcp-dns', {cloudStorage: {gcp: {project: 'storage-only'}}}), false);
  for (const invalid of [
    {project: 'project-one'}, {...dns, console: false}, {...dns, project: ''}, {...dns, project: 'x/other'},
    {...dns, api: 'other'}, {...dns, endpoint: 'https://example.com/'}, {...dns, operations: dns.operations.slice(1)},
    {...dns, operations: [...dns.operations, 'managedZones.delete']},
    {...dns, operations: [...dns.operations, 'changes.create', 'changes.create']},
    {...dns, operations: [...dns.operations.slice(1), 'changes.create', 'changes.create']},
  ]) assert.equal(serviceAvailable('gcp-dns', {gcpDns: invalid}), false);
  assert.equal(serviceAvailable('gcp-dns', {gcpDns: dns, consoleScope: {instanceId: 'x'}}), false);
  assert.equal(serviceAvailable('gcp-dns', {gcpDns: dns}), true); // Legacy owner: read-only record sets.
  assert.equal(serviceAvailable('gcp-dns', {gcpDns: {...dns, operations: [...dns.operations, 'changes.create']}}), true);
  assert.equal(serviceAvailable('gcp-storage', {cloudStorage: {gcp: {project: 'p'}}}), true);
});

test('owner discovery belongs to both the authorized identity and epoch, including A-B-A', () => {
  const session = {gcpDns: dns, cloudStorage: {gcp: {project: 'p'}}};
  const discover = (epoch, principal, value) => ({epoch, principal, value});
  assert.strictEqual(dnsCapabilitiesForPrincipal(session, 0, 'learner', null), session);
  assert.equal(dnsCapabilitiesForPrincipal(session, 1, 'other', null).gcpDns, undefined);
  assert.equal(dnsCapabilitiesForPrincipal(session, 2, 'learner', discover(1, 'other', dns)).gcpDns, undefined);
  assert.equal(dnsCapabilitiesForPrincipal(session, 3, 'learner', discover(1, 'learner', dns)).gcpDns, undefined);
  assert.equal(dnsCapabilitiesForPrincipal(session, 3, 'learner', discover(3, 'other', dns)).gcpDns, undefined);
  assert.strictEqual(dnsCapabilitiesForPrincipal(session, 3, 'learner', discover(3, 'learner', dns)).gcpDns, dns);
  assert.strictEqual(dnsCapabilitiesForPrincipal(session, 3, 'learner', null).cloudStorage, session.cloudStorage);
});
