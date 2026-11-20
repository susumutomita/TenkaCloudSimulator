// AWS Query protocol support for the catalog console. Requests are the
// form bodies botocore's Query serializer sends; responses are decoded from
// the service's real XML with the descriptor's output shapes. No JSON side
// channel exists: the console sends and reads exactly what SDKs do.

// queryBody serializes official input members (keyed by member name) as a
// form body: Action, Version, scalars under their key, string lists as
// Key.ItemKey.N (Key.N when flattened) and an empty list as EmptyKey=.
export function queryBody(service, op, input) {
  const form = new URLSearchParams();
  form.append('Action', op.name);
  form.append('Version', service.apiVersion);
  for (const member of op.members || []) {
    if (!(member.name in input)) continue;
    const value = input[member.name];
    const key = member.key || member.name;
    if (member.type === 'list') {
      if (!Array.isArray(value)) throw Error(member.name + ' must be a list.');
      if (!value.length) { form.append(member.emptyKey || key, ''); continue; }
      const prefix = member.flattened ? key : key + '.' + (member.itemKey || 'member');
      value.forEach((item, i) => form.append(prefix + '.' + (i + 1), scalar(member.name, item)));
    } else if (['structure', 'map'].includes(member.type)) {
      throw Error(member.name + ' is not editable in the console; use an SDK or the API reference.');
    } else {
      form.append(key, scalar(member.name, value));
    }
  }
  return form.toString();
}

function scalar(name, value) {
  if (value !== null && typeof value === 'object') throw Error(name + ' must be a single value.');
  return typeof value === 'boolean' ? (value ? 'true' : 'false') : String(value);
}

const entities = {lt: '<', gt: '>', amp: '&', quot: '"', apos: "'"};
function unescape(text) {
  return text.replace(/&(#x[0-9a-fA-F]+|#[0-9]+|[a-z]+);/g, (whole, name) => {
    if (name[0] === '#') {
      const code = name[1] === 'x' ? parseInt(name.slice(2), 16) : parseInt(name.slice(1), 10);
      return Number.isFinite(code) && code <= 0x10ffff ? String.fromCodePoint(code) : whole;
    }
    return entities[name] ?? whole;
  });
}

// parseXML reads the element tree of a well-formed response document:
// {name (local name), text, children}. It refuses what it cannot read.
export function parseXML(text) {
  const root = {name: '', text: '', children: []};
  const stack = [root];
  const token = /<\?[^]*?\?>|<!--[^]*?-->|<!\[CDATA\[([^]*?)\]\]>|<(\/?)([A-Za-z_][\w.:-]*)((?:\s+[^\s=/>]+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*(\/?)>|([^<]+)/gy;
  let match, at = 0;
  while (at < text.length) {
    token.lastIndex = at;
    if (!(match = token.exec(text))) throw Error('The response is not well-formed XML.');
    at = token.lastIndex;
    const [, cdata, closing, tag, , selfClosing, chars] = match;
    const top = stack[stack.length - 1];
    if (chars !== undefined) top.text += unescape(chars);
    else if (cdata !== undefined) top.text += cdata;
    else if (tag !== undefined) {
      const name = tag.includes(':') ? tag.slice(tag.indexOf(':') + 1) : tag;
      if (closing) {
        if (stack.length < 2 || top.name !== name) throw Error('The response is not well-formed XML.');
        stack.pop();
      } else {
        const node = {name, text: '', children: []};
        top.children.push(node);
        if (!selfClosing) stack.push(node);
      }
    }
  }
  if (stack.length !== 1 || root.children.length !== 1) throw Error('The response is not well-formed XML.');
  return root.children[0];
}

const child = (node, name) => node.children.find(c => c.name === name);
const children = (node, name) => node.children.filter(c => c.name === name);

function decodeValue(shapes, shapeName, node) {
  const shape = shapes[shapeName];
  if (!shape) throw Error('No output shape ' + shapeName + ' is described.');
  switch (shape.type) {
    case 'structure': {
      const out = {};
      for (const member of shape.members || []) {
        const target = shapes[member.shape];
        if (target?.type === 'list' && member.flattened) {
          const items = children(node, member.element);
          if (items.length) out[member.name] = items.map(item => decodeValue(shapes, target.item.shape, item));
        } else if (target?.type === 'map' && member.flattened) {
          const entries = children(node, member.element);
          if (entries.length) out[member.name] = decodeEntries(shapes, target, entries);
        } else {
          const element = child(node, member.element);
          if (element) out[member.name] = decodeValue(shapes, member.shape, element);
        }
      }
      return out;
    }
    case 'list':
      return children(node, shape.item.element).map(item => decodeValue(shapes, shape.item.shape, item));
    case 'map':
      return decodeEntries(shapes, shape, children(node, 'entry'));
    case 'boolean':
      return node.text.trim() === 'true';
    case 'integer': case 'long': case 'float': case 'double': {
      const n = Number(node.text.trim());
      if (!Number.isFinite(n)) throw Error('A numeric response member is not a number.');
      return n;
    }
    default:
      return node.text;
  }
}

function decodeEntries(shapes, shape, entries) {
  const out = {};
  for (const entry of entries) {
    const key = child(entry, shape.key.element), value = child(entry, shape.value.element);
    if (key) out[key.text] = value ? decodeValue(shapes, shape.value.shape, value) : {};
  }
  return out;
}

// decodeQuery turns one <Action>Response document into an object keyed like
// the JSON protocols' responses, using the operation's described output.
export function decodeQuery(service, op, text) {
  const root = parseXML(text);
  if (root.name === 'ErrorResponse') {
    const error = child(root, 'Error');
    throw Error((error && child(error, 'Code')?.text || 'Error') + ': ' + (error && child(error, 'Message')?.text || ''));
  }
  if (root.name !== op.name + 'Response') throw Error('Unexpected response ' + root.name + ' for ' + op.name + '.');
  if (!op.output) return {};
  const start = op.resultWrapper ? child(root, op.resultWrapper) : root;
  if (!start) throw Error('The response lacks ' + op.resultWrapper + '.');
  return decodeValue(service.xmlShapes || {}, op.output, start);
}
