// Native SNS Query only. Endpoint values are request data, never destinations.
export const SNS_ENDPOINT_PROTOCOLS = Object.freeze(['http', 'https']);
const namespace = 'http://sns.amazonaws.com/doc/2010-03-31/';
const invalid = () => new Error('Invalid local SNS response.');

function entries(node, name) {
  if (!node) throw invalid();
  return [...node.children].filter(child => child.localName === name && child.namespaceURI === namespace);
}
function element(node, name, optional = false) {
  const found = entries(node, name);
  if (found.length === 1) return found[0];
  if (optional && found.length === 0) return null;
  throw invalid();
}
function content(node, name) {
  const child = element(node, name);
  if (child.children.length !== 0 || !child.textContent) throw invalid();
  return child.textContent;
}

export function isLocalTopicArn(value, region) {
  const prefix = 'arn:aws:sns:' + region + ':000000000000:';
  return typeof value === 'string' && value.startsWith(prefix)
    && /^(?:[A-Za-z0-9_-]{1,256}|[A-Za-z0-9_-]{1,251}\.fifo)$/.test(value.slice(prefix.length));
}

function isSubscriptionArn(value, topic) {
  return typeof value === 'string' && value.startsWith(topic + ':')
    && /^[a-f0-9]{32}$/.test(value.slice(topic.length + 1));
}

export function snsClient(request, region, parse = text => new DOMParser().parseFromString(text, 'application/xml')) {
  async function query(action, fields, signal, result = true) {
    signal?.throwIfAborted();
    const response = await request('/', {
      method: 'POST',
      redirect: 'error',
      credentials: 'same-origin',
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
        'X-Simulator-Service': 'sns',
        'X-Simulator-Region': region,
      },
      body: new URLSearchParams({...fields, Action: action, Version: '2010-03-31'}),
      signal,
    });
    const type = response.headers.get('Content-Type')?.split(';', 1)[0];
    if (type !== 'application/xml' && type !== 'text/xml') {
      if (!response.ok) throw new Error('SNS request failed (HTTP ' + response.status + ').');
      throw invalid();
    }
    const root = parse(await response.text())?.documentElement;
    signal?.throwIfAborted();
    if (root?.namespaceURI !== namespace) throw invalid();
    if (!response.ok) {
      if (root.localName !== 'ErrorResponse') throw invalid();
      const error = element(root, 'Error');
      throw new Error(content(error, 'Code') + ': ' + content(error, 'Message'));
    }
    if (root.localName !== action + 'Response') throw invalid();
    return result ? element(root, action + 'Result') : undefined;
  }

  async function list(action, fields, collection, maxPages, select, signal) {
    const rows = new Map();
    const previous = new Set();
    let token = '';
    for (let i = 0; i < maxPages; i++) {
      const result = await query(action, token ? {...fields, NextToken: token} : fields, signal);
      const members = entries(element(result, collection), 'member');
      if (members.length > 100) throw invalid();
      for (const member of members) {
        const [id, row] = select(member);
        if (rows.has(id)) throw invalid();
        rows.set(id, row);
      }
      token = element(result, 'NextToken', true)?.textContent || '';
      if (!token) return [...rows.values()];
      if (token.length > 2048 || previous.has(token)) throw invalid();
      previous.add(token);
    }
    throw new Error('SNS pagination exceeded the local service limit.');
  }

  function topicArn(arn) {
    if (!isLocalTopicArn(arn, region)) throw new Error('Enter a local SNS topic ARN in the selected region.');
    return arn;
  }

  return {
    listTopics: signal => list('ListTopics', {}, 'Topics', 10, member => {
      const arn = content(member, 'TopicArn');
      if (!isLocalTopicArn(arn, region)) throw invalid();
      return [arn, arn];
    }, signal),
    listSubscriptions: (topic, signal) => list('ListSubscriptionsByTopic', {TopicArn: topicArn(topic)}, 'Subscriptions', 100, member => {
      const arn = content(member, 'SubscriptionArn');
      if (!isSubscriptionArn(arn, topic) || content(member, 'TopicArn') !== topic) throw invalid();
      const row = {arn, protocol: content(member, 'Protocol'), endpoint: content(member, 'Endpoint')};
      return [arn, row];
    }, signal),
    async subscribe(topic, protocol, endpoint, signal) {
      if (!SNS_ENDPOINT_PROTOCOLS.includes(protocol)) throw new Error('Choose HTTP or HTTPS.');
      // Do not normalize or pre-approve URL syntax: the native API owns admission.
      const result = await query('Subscribe', {TopicArn: topicArn(topic), Protocol: protocol, Endpoint: endpoint}, signal);
      const arn = content(result, 'SubscriptionArn');
      if (!isSubscriptionArn(arn, topic)) throw invalid();
      return arn;
    },
    async unsubscribe(topic, arn, signal) {
      if (!isSubscriptionArn(arn, topicArn(topic))) throw new Error('Choose a subscription from the loaded topic.');
      await query('Unsubscribe', {SubscriptionArn: arn}, signal, false);
    },
  };
}
