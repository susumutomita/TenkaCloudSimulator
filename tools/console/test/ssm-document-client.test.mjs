import assert from 'node:assert/strict';
import test from 'node:test';
import {getDocumentVersion, listDocumentVersions, listOwnedDocuments} from '../src/ssm-document-client.mjs';

const name = 'revision-test';
const region = 'ap-northeast-1';
const version = (n, more) => ({
  Name: name, DocumentVersion: String(n), IsDefaultVersion: n === 1, DocumentFormat: 'JSON',
  Status: 'Active', CreatedDate: 1000, ...more,
});
const page = Array.from({length: 50}, (_, index) => version(index + 1));
const signal = () => new AbortController().signal;
const ok = data => ({ok: true, json: async () => data});

test('selects the local native API and lists the saved history, but never requests content', async () => {
  const calls = [], tiedTo = signal();
  const got = await listDocumentVersions(async (path, options) => {
    assert.equal(path, '/');
    assert.equal(options.method, 'POST');
    assert.equal(options.signal, tiedTo);
    assert.equal(options.redirect, 'error');
    assert.deepEqual(options.headers, {
      'Content-Type': 'application/x-amz-json-1.1',
      'X-Amz-Target': 'AmazonSSM.ListDocumentVersions',
      'X-Simulator-Region': region,
    });
    calls.push(JSON.parse(options.body));
    return ok(calls.length === 1
      ? {DocumentVersions: page, NextToken: 'second'}
      : {DocumentVersions: [version(51)]});
  }, region, tiedTo, name);
  assert.deepEqual(calls, [{Name: name, MaxResults: 50}, {Name: name, MaxResults: 50, NextToken: 'second'}]);
  assert.equal(got.length, 51);
  assert.deepEqual(got[0], {id: '1', isDefault: true, created: 1000});
  assert.deepEqual(got.at(-1), {id: '51', isDefault: false, created: 1000});
});

test('rejects mismatched, partial, or superseded history', async () => {
  for (const response of [
    {DocumentVersions: [version(1, {Name: 'other-doc'})]},
    {DocumentVersions: [version(2)]},
    {DocumentVersions: [version(1, {IsDefaultVersion: false})]},
    {DocumentVersions: [version(1), version(2, {IsDefaultVersion: true})]},
    {DocumentVersions: [version(1)], NextToken: 'not-full'},
  ]) {
    await assert.rejects(listDocumentVersions(async () => ok(response), region, signal(), name), /Systems Manager|default/);
  }
  let calls = 0;
  await assert.rejects(listDocumentVersions(async () => {
    if (++calls === 1) return ok({DocumentVersions: page, NextToken: 'second'});
    throw Error('InvalidNextToken');
  }, region, signal(), name), /InvalidNextToken/);
  const canceled = new AbortController();
  await assert.rejects(listDocumentVersions(async () => {
    canceled.abort();
    return ok({DocumentVersions: page});
  }, region, canceled.signal, name), {name:'AbortError'});
});

test('lists only local owned definitions and verifies explicitly requested content', async () => {
  const owned = await listOwnedDocuments(async (_, request) => {
    assert.equal(request.headers['X-Amz-Target'], 'AmazonSSM.ListDocuments');
    return ok({DocumentIdentifiers: [{Name: name, Owner: '000000000000', DocumentType: 'Command', DocumentFormat: 'JSON'}]});
  }, region, signal());
  assert.deepEqual(owned, [{id:name}]);
  const content = await getDocumentVersion(async (_, request) => {
    assert.equal(request.headers['X-Amz-Target'], 'AmazonSSM.GetDocument');
    assert.deepEqual(JSON.parse(request.body), {Name: name, DocumentVersion:'2', DocumentFormat:'JSON'});
    return ok({Name: name, DocumentVersion:'2', CreatedDate: 1000, Content:'{"definition":"example"}'});
  }, region, signal(), name, '2', 1000);
  assert.deepEqual(content, {id:'2', content:'{"definition":"example"}'});
  await assert.rejects(getDocumentVersion(
    async () => ok({Name:name, DocumentVersion:'1', CreatedDate: 1000, Content:'{}'}), region, signal(), name, '2', 1000),
    /selected document version/);
  await assert.rejects(getDocumentVersion(
    async () => ok({Name:name, DocumentVersion:'2', CreatedDate: 2000, Content:'{}'}), region, signal(), name, '2', 1000),
    /selected document version/);
  await assert.rejects(getDocumentVersion(
    async () => {throw Error('AccessDeniedException');}, region, signal(), name, '1', 1000),
    /AccessDeniedException/);
});
