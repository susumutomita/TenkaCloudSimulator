export const queueReference = '/_simulator/docs/azure-queue.html';
const prefix = '/_simulator/providers/azure-queue/';
const version = '2023-11-03';
export const queueMaxTTL = 604800, queueMaxText = 64 * 1024;
const maxResponse = 32 * (6 * queueMaxText + 1024) + 1024; // XML escaping and batch overhead
const accountName = /^[a-z0-9]{3,24}$/;
const queueName = /^(?!.*--)[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/;
const messageID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const xmlCharacters = /^[\t\n\r\u0020-\uD7FF\uE000-\uFFFD\u{10000}-\u{10FFFF}]*$/u;
const valid = (value, pattern) => typeof value === 'string' && value.trim() === value && pattern.test(value);
export const validQueueAccount = value => valid(value, accountName);
export const validQueueName = value => valid(value, queueName);

function refuse(code) { throw new Error(code); }
function integer(value, low, high) {
  if ((typeof value !== 'number' && typeof value !== 'string') ||
      String(value).trim() !== String(value) || !/^-?(0|[1-9][0-9]*)$/.test(String(value)) ||
      !Number.isInteger(Number(value)) || Number(value) < low || Number(value) > high)
    refuse('InvalidNumber');
  return Number(value);
}
function validReceipt(value) {
  return typeof value === 'string' && value.length > 0 && value.length <= 512 && !/[\s\u0000-\u001f\u007f]/.test(value);
}
function receiptData(receipt, visible) {
  if (!validReceipt(receipt) || typeof visible !== 'string' || !visible.endsWith(' GMT') || !Number.isFinite(Date.parse(visible)))
    refuse('InvalidResponse');
  return {receipt, visible};
}
const entities = {'&': '&amp;', '<': '&lt;', '>': '&gt;', '\r': '&#13;'};
export function queueMessageXML(text) {
  if (typeof text !== 'string' || !xmlCharacters.test(text)) refuse('InvalidText');
  if (new TextEncoder().encode(text).byteLength > queueMaxText) refuse('MessageTooLarge');
  return '<QueueMessage><MessageText>' + text.replace(/[&<>\r]/g, ch => entities[ch]) + '</MessageText></QueueMessage>';
}

async function boundedText(response, signal) {
  if (response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase() !== 'application/xml' || !response.body)
    refuse('InvalidResponse');
  const reader = response.body.getReader(), decoder = new TextDecoder('utf-8', {fatal: true}), parts = [];
  let length = 0;
  try {
    for (;;) {
      signal?.throwIfAborted();
      const {done, value} = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > maxResponse) refuse('ResponseTooLarge');
      parts.push(decoder.decode(value, {stream: true}));
    }
    signal?.throwIfAborted();
    return parts.join('') + decoder.decode();
  } finally {
    void reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

function nodeName(node, name) {
  return node?.nodeType === 1 && node.localName === name && !node.namespaceURI && node.attributes.length === 0;
}
function elements(node) {
  const result = [];
  for (const child of node.childNodes) {
    if (child.nodeType === 1) result.push(child);
    else if (child.nodeType !== 3 || child.textContent.trim() !== '') refuse('InvalidResponse');
  }
  return result;
}

// A DOMParser (production) or a fixture DOM (tests) is only used for detached
// XML. Content is rendered as React text, never inserted as markup.
function decodeMessages(text, mode, count, parse) {
  if (/<!\s*(DOCTYPE|ENTITY)\b/i.test(text)) refuse('InvalidResponse');
  const root = parse(text)?.documentElement;
  if (!nodeName(root, 'QueueMessagesList')) refuse('InvalidResponse');
  const rows = elements(root), ids = new Set();
  if (rows.length > count || (mode === 'send' && rows.length !== 1)) refuse('InvalidResponse');
  const withPayload = mode !== 'send', withReceipt = mode !== 'peek';
  const names = ['MessageId', 'InsertionTime', 'ExpirationTime'];
  if (withPayload) names.push('MessageText', 'DequeueCount');
  if (withReceipt) names.push('PopReceipt', 'TimeNextVisible');
  return rows.map(row => {
    if (!nodeName(row, 'QueueMessage')) refuse('InvalidResponse');
    const fields = new Map();
    for (const child of elements(row)) {
      const key = child.localName;
      if (!names.includes(key) || !nodeName(child, key) || fields.has(key) ||
          Array.from(child.childNodes).some(n => n.nodeType !== 3 && n.nodeType !== 4))
        refuse('InvalidResponse');
      fields.set(key, child.textContent);
    }
    const id = fields.get('MessageId'), inserted = fields.get('InsertionTime'), expires = fields.get('ExpirationTime');
    if (fields.size !== names.length || !valid(id, messageID) || ids.has(id) ||
        ![inserted, expires].every(stamp => stamp.endsWith(' GMT') && Number.isFinite(Date.parse(stamp))))
      refuse('InvalidResponse');
    ids.add(id);
    const item = {id, inserted, expires};
    if (withPayload) {
      const text = fields.get('MessageText'), count = fields.get('DequeueCount');
      if (text.length > queueMaxText || !/^(0|[1-9][0-9]{0,18})$/.test(count) ||
          (mode === 'receive' && count === '0') || new TextEncoder().encode(text).byteLength > queueMaxText)
        refuse('InvalidResponse');
      Object.assign(item, {text, count});
    }
    if (withReceipt) Object.assign(item, receiptData(fields.get('PopReceipt'), fields.get('TimeNextVisible')));
    return item;
  });
}

export function azureQueue(request, account, name, parseXML = text => new DOMParser().parseFromString(text, 'application/xml')) {
  if (!validQueueAccount(account)) refuse('InvalidAccount');
  if (!validQueueName(name)) refuse('InvalidQueueName');
  const queue = prefix + account + '/' + name, messages = queue + '/messages';
  function message(item) {
    if (!valid(item?.id, messageID) || !validReceipt(item?.receipt)) refuse('MissingReceipt');
    return messages + '/' + item.id;
  }
  const call = async (path, method, expected, signal, query, body) => {
    signal?.throwIfAborted();
    const headers = {'x-ms-version': version};
    if (body !== undefined) headers['Content-Type'] = 'application/xml';
    const response = await request(path + (query ? '?' + new URLSearchParams(query) : ''), {
      method, credentials: 'same-origin', redirect: 'error', headers, signal, ...(body !== undefined ? {body} : {}),
    });
    signal?.throwIfAborted();
    if (!response.ok) refuse('HTTP' + response.status);
    if (response.redirected || !expected.includes(response.status) || response.headers.get('x-ms-version') !== version)
      refuse('InvalidResponse');
    return response;
  };
  const read = async (response, mode, count, signal) => decodeMessages(await boundedText(response, signal), mode, count, parseXML);

  return {
    async create(signal) { return (await call(queue, 'PUT', [201, 204], signal)).status === 201; },
    async peek(limit, signal) {
      const count = integer(limit, 1, 32);
      return read(await call(messages, 'GET', [200], signal, {peekonly: 'true', numofmessages: count}), 'peek', count, signal);
    },
    async receive(limit, duration, signal) {
      const count = integer(limit, 1, 32), visibility = integer(duration, 1, queueMaxTTL);
      return read(await call(messages, 'GET', [200], signal, {numofmessages: count, visibilitytimeout: visibility}), 'receive', count, signal);
    },
    async send({text, ttl, delay}, signal) {
      const lifetime = integer(ttl, -1, queueMaxTTL), visibility = integer(delay, 0, queueMaxTTL);
      if (lifetime === 0 || (lifetime > 0 && visibility >= lifetime)) refuse('InvalidLifetime');
      const response = await call(messages, 'POST', [201], signal, {messagettl: lifetime, visibilitytimeout: visibility}, queueMessageXML(text));
      return (await read(response, 'send', 1, signal))[0];
    },
    async update(item, {visibility, text}, signal) {
      const path = message(item), duration = integer(visibility, 0, queueMaxTTL);
      const response = await call(path, 'PUT', [204], signal, {popreceipt: item.receipt, visibilitytimeout: duration},
        text === undefined ? undefined : queueMessageXML(text));
      const result = receiptData(response.headers.get('x-ms-popreceipt'), response.headers.get('x-ms-time-next-visible'));
      if (result.receipt === item.receipt) refuse('InvalidResponse');
      return result;
    },
    async remove(item, signal) { await call(message(item), 'DELETE', [204], signal, {popreceipt: item.receipt}); },
    async clear(signal) { await call(messages, 'DELETE', [204], signal); },
    async deleteQueue(signal) { await call(queue, 'DELETE', [204], signal); },
  };
}

// The global console may throw native status/code plus an arbitrary body.
// Preserve known codes for UI presentation; never display raw response/errors.
export function queueErrorCode(error) {
  const code = String(error?.message || '').split(':', 1)[0];
  if (['QueueNotFound', 'MessageNotFound', 'PopReceiptMismatch', 'QuotaExceeded', 'AuthenticationFailed',
    'AuthorizationFailure', 'Sign in to continue.', 'InvalidLifetime', 'InvalidNumber', 'InvalidText', 'MessageTooLarge',
    'InvalidResponse', 'ResponseTooLarge', 'MissingReceipt', 'HTTP400', 'HTTP401', 'HTTP403', 'HTTP404', 'HTTP409',
    '400', '401', '403', '404', '409'].includes(code)) return code;
  return 'RequestFailed';
}
