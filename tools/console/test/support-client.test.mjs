import assert from 'node:assert/strict';
import test from 'node:test';
import {File} from 'node:buffer';
import {hashForService, serviceAvailable, serviceFromHash, servicesForProvider} from '../src/console-navigation.mjs';
import {localDownloadName, supportClient, supportOperations, validSupportFile} from '../src/support-client.mjs';

const signal = new AbortController().signal;
const details = {caseId:'case-abc', displayId:'2', subject:'Practice', status:'opened', timeCreated:'2026-11-28T07:00:00Z'};
const when = '2026-11-28T08:00:00Z';
const meta = {fileName:'note.bin', attachmentId:'attachment-123'};
const msg = {caseId:details.caseId, body:'A message', timeCreated:when, submittedBy:'student', attachmentSet:[meta]};
function stub(responses) {
  const sent = [];
  const api = supportClient(async (path, options) => {
    const op = options.headers['X-Amz-Target'].split('.')[1];
    assert.equal(path, '/');
    assert.equal(options.method, 'POST');
    assert.equal(options.headers['Content-Type'], 'application/x-amz-json-1.1');
    assert.equal(options.headers['X-Amz-Target'], 'AWSSupport_20130415.' + op);
    assert.equal(options.headers['X-Simulator-Service'], 'support');
    assert.equal(options.headers['X-Simulator-Region'], 'us-east-1');
    assert.ok(options.signal instanceof AbortSignal);
    sent.push([op, JSON.parse(options.body)]);
    assert.ok(Object.hasOwn(responses, op), op);
    return responses[op] instanceof Response ? responses[op] : Response.json(responses[op]);
  });
  return {api, sent};
}

test('discover only a compatible ordinary Support service in provider search, route and home', () => {
  const service = {operations:[...supportOperations], console:true, protocol:'AWS JSON 1.1', scope:'team',
    region:'us-east-1', attachmentBytes:65536, attachmentFilesPerSet:3};
  const caps = {services:{support:service}};
  assert.equal(hashForService('support'), '/support');
  assert.equal(serviceFromHash('#/support'), 'support');
  assert.deepEqual(servicesForProvider('aws', caps, 'サポート').map(x => x.id), ['support']);
  assert.equal(serviceAvailable('support', {...caps, consoleScope:{instanceId:'i-123'}}), false);
  for (const supplied of [undefined, {...service, console:false}, {...service, region:'us-west-2'},
    ...supportOperations.map(op => ({...service, operations:supportOperations.filter(x => x !== op)}))]) {
    assert.equal(serviceAvailable('support', {services:{support:supplied}}), false);
  }
});

test('use opaque case and continuation IDs for inventory, detail, history and three acknowledged writes', async () => {
  const {api, sent} = stub({DescribeCases:{cases:[details]}, DescribeCommunications:{communications:[msg], nextToken:'older'},
    CreateCase:{caseId:details.caseId}, AddCommunicationToCase:{result:true}, ResolveCase:{initialCaseStatus:'opened', finalCaseStatus:'resolved'}});
  await api.cases(false, 'cases-more', signal);
  assert.deepEqual(await api.detail(details.caseId, signal), {item:details, history:{rows:[msg], next:'older'}});
  await api.messages(details.caseId, 'older', signal);
  assert.equal(await api.send('', 'S', 'M', 'set-initial', 'ja', signal), details.caseId);
  assert.equal(await api.send(details.caseId, '', 'M2', '', 'ja', signal), details.caseId);
  await api.resolve(details.caseId, signal);
  assert.deepEqual(sent, [
    ['DescribeCases', {includeResolvedCases:false, includeCommunications:false, maxResults:10, nextToken:'cases-more'}],
    ['DescribeCases', {includeResolvedCases:true, includeCommunications:false, maxResults:10, caseIdList:[details.caseId]}],
    ['DescribeCommunications', {caseId:details.caseId, maxResults:10}],
    ['DescribeCommunications', {caseId:details.caseId, maxResults:10, nextToken:'older'}],
    ['CreateCase', {subject:'S', communicationBody:'M', language:'ja', attachmentSetId:'set-initial'}],
    ['AddCommunicationToCase', {caseId:details.caseId, communicationBody:'M2'}], ['ResolveCase', {caseId:details.caseId}],
  ]);
});

test('bound exact encoded and decoded file bytes and preserve display metadata while choosing a local filename', async () => {
  const file = new File([Uint8Array.from([0, 255, 10])], meta.fileName);
  const {api, sent} = stub({AddAttachmentsToSet:{attachmentSetId:'set-1', expiryTime:when},
    DescribeAttachment:{attachment:{fileName:meta.fileName, data:'AP8K'}}});
  const staged = await api.stage(file, signal);
  assert.equal(staged.file, file); assert.equal(staged.id, 'set-1');
  const blob = await api.download(meta, signal);
  assert.equal(blob.type, 'application/octet-stream');
  assert.deepEqual([...new Uint8Array(await blob.arrayBuffer())], [0, 255, 10]);
  assert.deepEqual(sent, [['AddAttachmentsToSet', {attachments:[{fileName:'note.bin', data:'AP8K'}]}],
    ['DescribeAttachment', {attachmentId:'attachment-123'}]]);
  assert.equal(validSupportFile(new File([new Uint8Array(65536)], 'ok')), true);
  assert.equal(validSupportFile(new File([new Uint8Array(65537)], 'too-large')), false);
  assert.equal(validSupportFile(new File([], 'x'.repeat(256))), false);
  assert.equal(localDownloadName('../moved/..'), 'attachment');
  assert.equal(localDownloadName('C:\\folder\\test.bin'), 'test.bin');
});

test('never bind a different case, malformed projection, changed filename or failed write acknowledgement', async () => {
  for (const [fragment, read] of [
    [{DescribeCases:{cases:[{...details, caseId:'other'}]}, DescribeCommunications:{communications:[msg]}}, api => api.detail(details.caseId, signal)],
    [{DescribeCommunications:{communications:[{...msg, caseId:'other'}]}}, api => api.messages(details.caseId, '', signal)],
    [{DescribeCommunications:{communications:[{...msg, attachmentSet:[{fileName:'only name'}]}]}}, api => api.messages(details.caseId, '', signal)],
    [{DescribeCases:{cases:Array(11).fill(details)}}, api => api.cases(true, '', signal)],
    [{DescribeAttachment:{attachment:{fileName:'changed', data:'AA=='}}}, api => api.download(meta, signal)],
    [{DescribeAttachment:{attachment:{fileName:meta.fileName, data:'AB=='}}}, api => api.download(meta, signal)],
    [{AddCommunicationToCase:{result:false}}, api => api.send('id', '', 'msg', '', 'en', signal)],
    [{ResolveCase:{initialCaseStatus:'opened', finalCaseStatus:'opened'}}, api => api.resolve('id', signal)],
  ]) await assert.rejects(read(stub(fragment).api), /Invalid local AWS Support response/);
});

test('server rejection preserves native status, transport uncertainty stays distinguishable, abort prevents disclosure', async () => {
  const err = {__type:'AccessDeniedException', message:'not authorized'};
  for (const [status, rejected] of [[403, true], [500, false]]) {
    const {api} = stub({ResolveCase:Response.json(err, {status})});
    await assert.rejects(api.resolve('id', signal), cause => cause.rejected === rejected && cause.message.startsWith(String(status)));
  }
  const delayed = new AbortController();
  const api = supportClient(async () => {delayed.abort(); return Response.json({cases:[details]});});
  await assert.rejects(api.cases(true, '', delayed.signal), {name:'AbortError'});
});

test('send the AMS incident classification only on explicit creation and verify the returned case metadata', async () => {
  const serviceCode = 'sentinel-report-incident';
  const {api, sent} = stub({CreateCase:{caseId:details.caseId}, AddCommunicationToCase:{result:true},
    DescribeCases:{cases:[{...details, serviceCode}]}});
  await api.send('', 'Case', 'First message', undefined, 'ja', signal, true);
  await api.send(details.caseId, '', 'Follow-up', undefined, 'ja', signal, true);
  assert.deepEqual(sent, [
    ['CreateCase', {subject:'Case', communicationBody:'First message', language:'ja', serviceCode}],
    ['AddCommunicationToCase', {caseId:details.caseId, communicationBody:'Follow-up'}],
  ]);
  assert.equal((await api.cases(false, '', signal)).rows[0].serviceCode, serviceCode);
  const bad = stub({DescribeCases:{cases:[{...details, serviceCode:'unknown-service'}]}});
  await assert.rejects(bad.api.cases(false, '', signal), /Invalid local AWS Support response/);
});
