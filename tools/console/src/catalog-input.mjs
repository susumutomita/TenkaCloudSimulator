// Complex catalog inputs: tags and structure, map or list-of-structure
// members, described by the service descriptor's official input shapes.
// Values are keyed by official member names, as SDK users write them, and
// are serialized to each protocol's own wire form only when sent.

// tagShape recognizes the official tag forms: a map of strings, or a list of
// two-member structures (Key/Value, key/value or TagKey/TagValue).
export function tagShape(shapes, name) {
  const shape = shapes?.[name];
  if (!shape) return null;
  if (shape.type === 'map' && shapes[shape.key.shape]?.type === 'string' && shapes[shape.value.shape]?.type === 'string') return {form: 'map'};
  if (shape.type !== 'list') return null;
  const element = shapes[shape.member.shape];
  if (element?.type !== 'structure' || element.members?.length !== 2) return null;
  const names = element.members.map(m => m.name);
  for (const [key, value] of [['Key', 'Value'], ['key', 'value'], ['TagKey', 'TagValue'], ['tagKey', 'tagValue']]) {
    if (names.includes(key) && names.includes(value)) return {form: 'list', key, value};
  }
  return null;
}

// parseTagLines reads "key=value" lines (value may be empty or contain "=").
export function parseTagLines(text) {
  const pairs = [];
  for (const [index, raw] of String(text).split('\n').entries()) {
    const line = raw.trim();
    if (!line) continue;
    const cut = line.indexOf('=');
    const key = (cut < 0 ? line : line.slice(0, cut)).trim();
    if (!key) throw Error('Tag line ' + (index + 1) + ' needs a key before "=".');
    if (pairs.some(([k]) => k === key)) throw Error('Tag key ' + key + ' is given twice.');
    pairs.push([key, cut < 0 ? '' : line.slice(cut + 1).trim()]);
  }
  return pairs;
}

// tagValue builds the official tag member value from [key, value] pairs.
export function tagValue(form, pairs) {
  if (form.form === 'map') return Object.fromEntries(pairs);
  return pairs.map(([k, v]) => ({[form.key]: k, [form.value]: v}));
}

// tagPairs reads tags back from a decoded response: a list of pairs in any
// official spelling, or a map.
export function tagPairs(value) {
  if (Array.isArray(value)) {
    return value.map(tag => {
      const key = tag.Key ?? tag.key ?? tag.TagKey ?? tag.tagKey;
      const v = tag.Value ?? tag.value ?? tag.TagValue ?? tag.tagValue;
      return [String(key ?? ''), v === undefined || v === null ? '' : String(v)];
    }).filter(([k]) => k !== '');
  }
  if (value && typeof value === 'object') return Object.entries(value).map(([k, v]) => [k, String(v)]);
  return [];
}

const kind = value => Array.isArray(value) ? 'a list' : value === null ? 'null' : typeof value === 'object' ? 'an object' : 'a ' + typeof value;

// validateValue checks a value against an official shape before sending,
// naming the member path that breaks: types, required and unknown members,
// unions, enums, lengths and ranges.
export function validateValue(shapes, name, value, path) {
  const shape = shapes?.[name];
  if (!shape) throw Error(path + ': its shape is not described.');
  const fail = message => { throw Error(path + ': ' + message); };
  const length = (n, unit) => {
    if (shape.min !== undefined && n < shape.min) fail('needs at least ' + shape.min + ' ' + unit + '.');
    if (shape.max !== undefined && n > shape.max) fail('allows at most ' + shape.max + ' ' + unit + '.');
  };
  if (shape.document) return;
  switch (shape.type) {
    case 'structure': {
      if (!value || typeof value !== 'object' || Array.isArray(value)) fail('must be an object, not ' + kind(value) + '.');
      const members = new Map((shape.members || []).map(m => [m.name, m]));
      for (const key of Object.keys(value)) if (!members.has(key)) fail('has no member ' + key + '. Members: ' + [...members.keys()].join(', ') + '.');
      for (const member of members.values()) if (member.required && !(member.name in value)) fail('needs member ' + member.name + '.');
      if (shape.union && Object.keys(value).length !== 1) fail('must set exactly one member.');
      for (const [key, item] of Object.entries(value)) validateValue(shapes, members.get(key).shape, item, path + '.' + key);
      return;
    }
    case 'list':
      if (!Array.isArray(value)) fail('must be a list, not ' + kind(value) + '.');
      length(value.length, 'items');
      value.forEach((item, i) => validateValue(shapes, shape.member.shape, item, path + '[' + i + ']'));
      return;
    case 'map':
      if (!value || typeof value !== 'object' || Array.isArray(value)) fail('must be an object of entries, not ' + kind(value) + '.');
      length(Object.keys(value).length, 'entries');
      for (const [key, item] of Object.entries(value)) {
        validateValue(shapes, shape.key.shape, key, path + ' key');
        validateValue(shapes, shape.value.shape, item, path + '.' + key);
      }
      return;
    case 'string':
      if (typeof value !== 'string') fail('must be a string, not ' + kind(value) + '.');
      if (shape.enum && !shape.enum.includes(value)) fail('must be one of ' + shape.enum.join(', ') + '.');
      length([...value].length, 'characters');
      return;
    case 'integer': case 'long':
      if (!Number.isInteger(value)) fail('must be a whole number.');
      if (shape.min !== undefined && value < shape.min || shape.max !== undefined && value > shape.max) fail('must be from ' + (shape.min ?? '-∞') + ' to ' + (shape.max ?? '∞') + '.');
      return;
    case 'float': case 'double':
      if (typeof value !== 'number' || !Number.isFinite(value)) fail('must be a number.');
      return;
    case 'boolean':
      if (typeof value !== 'boolean') fail('must be true or false.');
      return;
    case 'timestamp':
      if (typeof value !== 'string' && typeof value !== 'number') fail('must be an ISO 8601 time or epoch seconds.');
      return;
    case 'blob':
      if (typeof value !== 'string') fail('must be base64 text.');
      return;
    default:
      fail('has an unsupported type ' + shape.type + '.');
  }
}

// parseComplex reads a JSON value field and validates it.
export function parseComplex(shapes, member, text) {
  let value;
  try { value = JSON.parse(text); } catch { throw Error(member.name + ': enter a JSON value.'); }
  validateValue(shapes, member.shape, value, member.name);
  return value;
}

// example builds a skeleton of required members, for the field's hint.
export function example(shapes, name, depth = 0) {
  const shape = shapes?.[name];
  if (!shape || depth > 4) return null;
  switch (shape.type) {
    case 'structure': {
      if (shape.document) return {};
      // Required members, else (a union) one member, else the first few, so
      // the hint always shows the member names a value can use.
      const members = (shape.members || []).filter(m => m.required);
      const chosen = members.length ? members : (shape.members || []).slice(0, shape.union ? 1 : 3);
      return Object.fromEntries(chosen.map(m => [m.name, example(shapes, m.shape, depth + 1)]));
    }
    case 'list': return [example(shapes, shape.member.shape, depth + 1)];
    case 'map': return {key: example(shapes, shape.value.shape, depth + 1)};
    case 'string': return shape.enum ? shape.enum.join(' | ') : 'string';
    case 'integer': case 'long': return shape.min ?? 1;
    case 'float': case 'double': return 1.5;
    case 'boolean': return false;
    default: return 'string';
  }
}

// toWire renames official member names to JSON/REST body keys.
export function toWire(shapes, name, value) {
  const shape = shapes?.[name];
  if (!shape || shape.document || value === null || typeof value !== 'object') return value;
  if (shape.type === 'structure') {
    const members = new Map((shape.members || []).map(m => [m.name, m]));
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [members.get(k)?.wire || k, toWire(shapes, members.get(k)?.shape, v)]));
  }
  if (shape.type === 'list') return value.map(item => toWire(shapes, shape.member.shape, item));
  if (shape.type === 'map') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, toWire(shapes, shape.value.shape, v)]));
  return value;
}

// fromWire maps a decoded response value back to official member names,
// keeping only members the input shape has (used to pre-fill edits).
export function fromWire(shapes, name, value) {
  const shape = shapes?.[name];
  if (!shape || shape.document || value === null || typeof value !== 'object') return value;
  if (shape.type === 'structure') {
    const out = {};
    for (const member of shape.members || []) {
      const wire = member.wire || member.name;
      if (wire in value) out[member.name] = fromWire(shapes, member.shape, value[wire]);
    }
    return out;
  }
  if (shape.type === 'list') return Array.isArray(value) ? value.map(item => fromWire(shapes, shape.member.shape, item)) : value;
  if (shape.type === 'map') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, fromWire(shapes, shape.value.shape, v)]));
  return value;
}

const text = value => typeof value === 'boolean' ? (value ? 'true' : 'false') : String(value);

// queryEntries appends a value at prefix as botocore's Query serializer
// does: structure members joined with ".", list elements as
// <element>.N (N directly when flattened), map entries as entry.N.key /
// entry.N.value, and an empty list as an empty value under emptyPrefix.
export function queryEntries(form, shapes, name, value, prefix, flattened = false, emptyPrefix = prefix) {
  const shape = shapes?.[name];
  if (!shape) { form.append(prefix, text(value)); return; }
  switch (shape.type) {
    case 'structure':
      for (const member of shape.members || []) {
        if (!(member.name in value)) continue;
        const join = key => prefix ? prefix + '.' + key : key;
        queryEntries(form, shapes, member.shape, value[member.name], join(member.key || member.name), member.flattened, join(member.emptyKey || member.key || member.name));
      }
      return;
    case 'list': {
      if (!value.length) { form.append(emptyPrefix, ''); return; }
      const listPrefix = flattened ? prefix : prefix + '.' + (shape.member.key || 'member');
      value.forEach((item, i) => queryEntries(form, shapes, shape.member.shape, item, listPrefix + '.' + (i + 1)));
      return;
    }
    case 'map': {
      const entryPrefix = flattened ? prefix : prefix + '.entry';
      Object.entries(value).forEach(([key, item], i) => {
        form.append(entryPrefix + '.' + (i + 1) + '.' + (shape.key.key || 'key'), key);
        queryEntries(form, shapes, shape.value.shape, item, entryPrefix + '.' + (i + 1) + '.' + (shape.value.key || 'value'));
      });
      return;
    }
    default:
      form.append(prefix, text(value));
  }
}
