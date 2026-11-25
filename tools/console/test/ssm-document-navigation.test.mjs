import test from 'node:test';
import assert from 'node:assert/strict';
import {hashForService, serviceAvailable, serviceFromHash} from '../src/console-navigation.mjs';

const entries = ['ListDocuments', 'ListDocumentVersions', 'GetDocument'];
const cap = (documentManagement, consoleScope) => ({services: {ssm: {documentManagement}}, consoleScope});
const advertised = {operations: entries, definitionOnly: true, execution: false};

test('document history route appears only with a supported definition-only contract', () => {
  assert.equal(serviceFromHash('#/ssm-documents?region=us-east-1'), 'ssm-documents');
  assert.equal(hashForService('ssm-documents'), '/ssm-documents');
  assert.equal(serviceAvailable('ssm-documents', cap(advertised)), true);
  assert.equal(serviceAvailable('ssm-documents', cap({...advertised, operations: entries.slice(1)})), false);
  assert.equal(serviceAvailable('ssm-documents', cap({...advertised, execution: true})), false);
  assert.equal(serviceAvailable('ssm-documents', cap(undefined)), false);
});

test('parameter and instance handoffs do not offer the documents route', () => {
  for (const scope of [{parameterName: '/owned'}, {instanceId: 'i-synthetic'}, {}]) {
    assert.equal(serviceAvailable('ssm-documents', cap(advertised, scope)), false);
  }
});
