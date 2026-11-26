import assert from 'node:assert/strict';
import test from 'node:test';
import {exampleEC2Template, parseTemplatePreview, templatePreviewFailure, templatePreviewInputProblem, validateEC2Template} from '../src/ec2-template-preview.mjs';

const ns = 'http://cloudformation.amazonaws.com/doc/2010-05-15/';
function el(localName, ...contents) {
  const childNodes = contents.map(child => typeof child === 'string' ? {nodeType: 3, textContent: child} : child);
  return {nodeType: 1, localName, namespaceURI: ns, childNodes, children: childNodes.filter(child => child.nodeType === 1),
    textContent: childNodes.map(child => child.textContent).join('')};
}
const item = (key, echo, ...props) => el('member', el('ParameterKey', key), el('NoEcho', echo), ...props);
const meta = el('ResponseMetadata', el('RequestId', 'local-simulator'));
const doc = (...result) => ({documentElement: el('ValidateTemplateResponse', el('ValidateTemplateResult', ...result), meta)});
const preview = doc(el('Parameters', item('ImageId', 'false', el('DefaultValue', '<not displayed>')),
  item('MaskedKey', 'true', el('Description', 'also not displayed'))),
el('Capabilities', el('member', 'CAPABILITY_NAMED_IAM')), el('CapabilitiesReason', 'Role'));

test('uses the native UTF-8 byte bound and does not approve an empty draft locally', () => {
  assert.equal(templatePreviewInputProblem(exampleEC2Template), '');
  assert.equal(templatePreviewInputProblem(' \n'), 'empty');
  assert.equal(templatePreviewInputProblem('a'.repeat(51200)), '');
  assert.equal(templatePreviewInputProblem('a'.repeat(51201)), 'size');
  assert.equal(templatePreviewInputProblem('界'.repeat(17066) + 'ab'), '');
  assert.equal(templatePreviewInputProblem('界'.repeat(17066) + 'abc'), 'size');
});

test('returns declaration metadata only, preserving presence of an empty default without returning its value', () => {
  assert.deepEqual(parseTemplatePreview(preview), {parameters: [
    {name: 'ImageId', noEcho: false, defaultDeclared: true},
    {name: 'MaskedKey', noEcho: true, defaultDeclared: false}], acknowledgement: 'CAPABILITY_NAMED_IAM'});
  assert.deepEqual(parseTemplatePreview(doc(el('Parameters', item('X', 'false', el('DefaultValue'))))),
    {parameters: [{name: 'X', noEcho: false, defaultDeclared: true}], acknowledgement: ''});
  assert.deepEqual(parseTemplatePreview(doc(el('Parameters', '\n '))), {parameters: [], acknowledgement: ''});
});

test('invalid or partial XML cannot confirm the declaration', () => {
  const otherNamespace = {...el('Parameters'), namespaceURI: 'urn:foreign'};
  for (const broken of [
    {documentElement: el('CreateStackResponse', meta)}, {documentElement: el('ValidateTemplateResponse', meta)},
    {...preview, doctype: {}}, doc(), doc(otherNamespace), doc(el('Parameters', 'truncated')),
    doc(el('Parameters', item('Id', 'yes'))),
    doc(el('Parameters', item('Id', 'true', el('DefaultValue', 'must not return')))),
    doc(el('Parameters', item('Id', 'false'), item('Id', 'false'))),
    doc(el('Parameters', item('Id', 'false', el('Description', el('Unexpected'))))),
    doc(el('Parameters', item('Id', 'false', el('Unknown')))),
    doc(el('Parameters'), el('Capabilities', el('member', 'CAPABILITY_AUTO_EXPAND')), el('CapabilitiesReason', 'X')),
    doc(el('Parameters'), el('Capabilities', el('member', 'CAPABILITY_IAM'))),
    doc(el('Parameters'), el('CapabilitiesReason', 'X')),
    doc(el('Parameters'), el('Parameters')), doc(el('Parameters', ...Array.from({length:101}, (_, i) => item('Param'+i, 'false')))),
  ]) assert.throws(() => parseTemplatePreview(broken), /Unconfirmed/);
});

test('sends only the fixed same-origin regional ValidateTemplate call with untouched input', async () => {
  const controller = new AbortController(), calls = [];
  const request = async (...args) => {
    calls.push(args);
    return new Response('<fixture/>', {headers: {'Content-Type':'text/xml; charset=utf-8'}});
  };
  const template = '\nResources: {}\n';
  await validateEC2Template(request, 'ap-northeast-1', template, {signal: controller.signal,
    parse: text => {assert.equal(text, '<fixture/>'); return preview;}});
  const [[url, options]] = calls;
  assert.equal(calls.length, 1);
  assert.equal(calls[0].length, 2, 'no identity override');
  assert.equal(url, '/');
  assert.equal(options.method, 'POST');
  assert.equal(options.credentials, 'same-origin');
  assert.equal(options.redirect, 'error');
  assert.equal(options.signal, controller.signal);
  assert.deepEqual(options.headers, {'Content-Type':'application/x-www-form-urlencoded',
    'X-Simulator-Service':'cloudformation', 'X-Simulator-Region':'ap-northeast-1'});
  assert.deepEqual([...options.body.entries()], [['Action','ValidateTemplate'],['Version','2010-05-15'],['TemplateBody',template]]);
  await assert.rejects(validateEC2Template(request, 'us-east-1', ''), /Invalid preview input/);
  controller.abort();
  await assert.rejects(validateEC2Template(request, 'us-east-1', template, {signal: controller.signal}), {name:'AbortError'});
  assert.equal(calls.length, 1);
});

test('bad media type, invalid UTF-8, oversized bodies and aborts cannot publish a preview', async () => {
  const rejects = [
    new Response('<fixture/>', {headers:{'Content-Type':'application/json'}}),
    new Response(Uint8Array.of(0xff), {headers:{'Content-Type':'text/xml'}}),
    new Response('a'.repeat(384*1024+1), {headers:{'Content-Type':'text/xml'}}),
    new Response('provider error', {status:403}),
  ];
  for (const response of rejects) {
    await assert.rejects(validateEC2Template(async () => response, 'us-east-1', 'example', {
      parse: () => assert.fail('must not parse an invalid body'),
    }));
  }
  const controller = new AbortController();
  await assert.rejects(validateEC2Template(async () => {
    controller.abort();
    return new Response('<fixture/>', {headers:{'Content-Type':'text/xml'}});
  }, 'us-east-1', 'example', {signal:controller.signal, parse: () => assert.fail('aborted')}), {name:'AbortError'});
  assert.equal(templatePreviewFailure(Error('AccessDenied: anything')), 'denied');
  assert.equal(templatePreviewFailure(Error('Sign in to continue.')), 'denied');
  assert.equal(templatePreviewFailure(Error('ValidationError: rejected fields')), 'invalid');
  assert.equal(templatePreviewFailure(Error('provider error content')), 'unconfirmed');
});
