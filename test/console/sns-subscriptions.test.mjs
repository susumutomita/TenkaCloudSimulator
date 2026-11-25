import test from 'node:test';
import assert from 'node:assert/strict';
import {isLocalTopicArn, snsClient} from '../../tools/console/src/sns-client.mjs';

const ns = 'http://sns.amazonaws.com/doc/2010-03-31/';
const region = 'us-east-1';
const topic = 'arn:aws:sns:' + region + ':000000000000:test-subscriptions';
const otherTopic = topic + '-other';
const arn = topic + ':' + 'a'.repeat(32);
const endpoint = 'https://127.0.0.1:18443/my%20events?team=blue&show=all';
const form = 'application/x-www-form-urlencoded';

const node = (localName, value, namespaceURI = ns) => ({
  localName, namespaceURI, children: Array.isArray(value) ? value : [], textContent: typeof value === 'string' ? value : '',
});
const doc = (action, children = []) => node(action + 'Response',
  children === null ? [] : [node(action + 'Result', children)]);
const topicMember = value => node('member', [node('TopicArn', value)]);
const subMember = (subscription = arn, parent = topic) => node('member', [
  node('TopicArn', parent), node('SubscriptionArn', subscription), node('Protocol', 'https'), node('Endpoint', endpoint),
]);

function fixture(replies) {
  const calls = [];
  let root;
  const request = async (path, options) => {
    assert.equal(path, '/', 'SNS endpoints are never fetch destinations');
    assert.equal(options.method, 'POST');
    assert.equal(options.redirect, 'error');
    assert.equal(options.credentials, 'same-origin');
    assert.deepEqual(options.headers, {
      'Content-Type': form, 'X-Simulator-Service': 'sns', 'X-Simulator-Region': region,
    });
    const body = Object.fromEntries(options.body);
    assert.equal(body.Version, '2010-03-31');
    assert.equal(new Set(options.body.keys()).size, [...options.body].length);
    calls.push({body, signal: options.signal});
    const reply = replies.shift();
    assert.ok(reply, 'unexpected SNS request');
    root = reply.root ?? reply;
    const status = reply.status || 200;
    return new Response('<fixture/>', {status, headers: {'Content-Type': reply.type || (status === 200 ? 'text/xml' : 'application/xml')}});
  };
  return {calls, api: snsClient(request, region, () => ({documentElement: root}))};
}

test('console client uses native SNS topic list, Subscribe, read-back and Unsubscribe', async () => {
  const {api, calls} = fixture([
    doc('ListTopics', [node('Topics', [topicMember(topic)]), node('NextToken', 'next-a')]),
    doc('ListTopics', [node('Topics', [topicMember(otherTopic)])]),
    doc('Subscribe', [node('SubscriptionArn', arn)]),
    doc('ListSubscriptionsByTopic', [node('Subscriptions', [subMember()])]),
    doc('Unsubscribe', null),
    doc('ListSubscriptionsByTopic', [node('Subscriptions', [])]),
  ]);
  const {signal} = new AbortController();
  assert.deepEqual(await api.listTopics(signal), [topic, otherTopic]);
  assert.equal(await api.subscribe(topic, 'https', endpoint, signal), arn);
  assert.deepEqual(await api.listSubscriptions(topic, signal), [{arn, protocol: 'https', endpoint}]);
  await api.unsubscribe(topic, arn, signal);
  assert.deepEqual(await api.listSubscriptions(topic, signal), []);
  assert.ok(calls.every(call => call.signal === signal));
  assert.deepEqual(calls.map(({body: {Version, ...rest}}) => rest), [
    {Action: 'ListTopics'}, {Action: 'ListTopics', NextToken: 'next-a'},
    {Action: 'Subscribe', TopicArn: topic, Protocol: 'https', Endpoint: endpoint},
    {Action: 'ListSubscriptionsByTopic', TopicArn: topic},
    {Action: 'Unsubscribe', SubscriptionArn: arn},
    {Action: 'ListSubscriptionsByTopic', TopicArn: topic},
  ]);
});

test('scheme mismatch is submitted intact and exposes native InvalidParameter, not a success row', async () => {
  const mismatch = 'http://127.0.0.1:18080/my%20events?team=blue&show=all';
  const {api, calls} = fixture([
    {status: 400, root: node('ErrorResponse', [node('Error', [node('Code', 'InvalidParameter'),
      node('Message', 'HTTP/S endpoints must be absolute URLs with a hostname and the selected protocol.')])])},
    doc('ListSubscriptionsByTopic', [node('Subscriptions', [])]),
  ]);
  await assert.rejects(api.subscribe(topic, 'https', mismatch), {message:
    'InvalidParameter: HTTP/S endpoints must be absolute URLs with a hostname and the selected protocol.'});
  assert.equal(calls[0].body.Endpoint, mismatch);
  assert.deepEqual(await api.listSubscriptions(topic), []);
});

test('scope and disabled protocol are refused without a write or external fetch', async () => {
  const {api, calls} = fixture([]);
  assert.equal(isLocalTopicArn(topic, region), true);
  assert.equal(isLocalTopicArn(topic + '.fifo', region), true);
  assert.equal(isLocalTopicArn(topic + ':extra', region), false);
  assert.equal(isLocalTopicArn(topic + '!', region), false);
  assert.equal(isLocalTopicArn(topic, 'eu-west-1'), false);
  assert.equal(isLocalTopicArn(topic.replace('000000000000', '123456789012'), region), false);
  assert.throws(() => api.listSubscriptions(topic.replace(region, 'eu-west-1')), /selected region/);
  await assert.rejects(api.subscribe(topic, 'sqs', 'unused'), /Choose HTTP or HTTPS/);
  await assert.rejects(api.unsubscribe(otherTopic, arn), /loaded topic/);
  const canceled = new AbortController();
  canceled.abort();
  await assert.rejects(api.listTopics(canceled.signal), {name: 'AbortError'});
  assert.deepEqual(calls, []);
});

test('invalid wire response cannot become an empty or unverified success', async () => {
  const bad = [
    doc('ListTopics', [node('Topics', [topicMember(otherTopic)])]),
    node('ListSubscriptionsByTopicResponse', [], 'urn:unsupported'),
    doc('ListSubscriptionsByTopic', []),
    doc('ListSubscriptionsByTopic', [node('Subscriptions', [subMember(arn, otherTopic)])]),
    {...doc('Subscribe', [node('SubscriptionArn', arn)]), type: 'text/html'},
    doc('Subscribe', [node('SubscriptionArn', otherTopic + ':' + 'b'.repeat(32))]),
    doc('Subscribe', [node('SubscriptionArn', arn), node('SubscriptionArn', arn)]),
  ];
  for (const root of bad.slice(0, 4)) {
    await assert.rejects(fixture([root]).api.listSubscriptions(topic), /Invalid local SNS response/);
  }
  for (const root of bad.slice(4)) {
    await assert.rejects(fixture([root]).api.subscribe(topic, 'https', endpoint), /Invalid local SNS response/);
  }
});

test('authorized list permission cannot be inferred from Subscribe and list tokens are bounded', async () => {
  const {api, calls} = fixture([
    doc('Subscribe', [node('SubscriptionArn', arn)]),
    {status: 403, root: node('ErrorResponse', [node('Error', [node('Code', 'AuthorizationError'), node('Message', 'IAM evaluation: implicitDeny')])])},
    doc('ListTopics', [node('Topics', []), node('NextToken', 'repeat')]),
    doc('ListTopics', [node('Topics', []), node('NextToken', 'repeat')]),
  ]);
  assert.equal(await api.subscribe(topic, 'https', endpoint), arn);
  await assert.rejects(api.listSubscriptions(topic), /AuthorizationError/);
  assert.equal(calls.filter(call => call.body.Action === 'Subscribe').length, 1);
  await assert.rejects(api.listTopics(), /Invalid local SNS response/);
});
