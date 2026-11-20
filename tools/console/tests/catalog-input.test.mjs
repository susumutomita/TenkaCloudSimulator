import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {transformSync} from 'esbuild';
import * as input from '../src/catalog-input.mjs';
import * as query from '../src/catalog-query.mjs';

// Real descriptors (internal/aws/catalog writes this fixture and checks it
// is fresh) and bodies botocore's own Query serializer produced.
const {services} = JSON.parse(fs.readFileSync(new URL('./fixtures/catalog-descriptors.json', import.meta.url), 'utf8'));
const service = model => services.find(s => s.model === model);
const vectors = JSON.parse(fs.readFileSync(new URL('../../../test/fixtures/catalog-query/serializer.json', import.meta.url), 'utf8')).cases;
const vector = (model, operation) => vectors.find(c => c.model === model && c.operation === operation);
const resource = (model, kind) => service(model).resources.find(r => r.kind === kind);

const source = fs.readFileSync(new URL('../src/catalog.jsx', import.meta.url), 'utf8');
const compiled = transformSync(source, {loader: 'jsx', format: 'cjs', target: 'es2022'}).code;
const view = {exports: {}};
vm.runInNewContext(compiled, {module: view, exports: view.exports, console, URLSearchParams,
  require: id => id === './catalog-query.mjs' ? query : id === './catalog-input.mjs' ? input : id === 'react' ? {} : id === './i18n.jsx' ? {useLocale: () => ({t: en => en})} : {}});
const {buildCatalogRequest, formMembers, control, convertField, fieldText, resourceARN} = view.exports;
const plain = value => JSON.parse(JSON.stringify(value));

// Parameter order and the escaping of characters such as "~" and "*" differ
// between URLSearchParams and Python's urlencode; the decoded parameters must
// be identical.
const params = body => [...new URLSearchParams(body)].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0);

test('complex Query creates and tag writes send exactly botocore\'s parameters', () => {
  for (const [model, kind, operation] of [['elasticache', 'user', 'CreateUser'], ['redshift', 'clusterSubnetGroup', 'CreateClusterSubnetGroup'], ['elasticache', 'cacheSubnetGroup', 'ModifyCacheSubnetGroup']]) {
    const ops = resource(model, kind).operations;
    const op = Object.values(ops).find(o => o.name === operation);
    const expected = vector(model, operation);
    assert.deepEqual(params(query.queryBody(service(model), op, expected.params)), params(expected.body), operation);
  }
  for (const [model, kind, operation] of [['elasticache', 'tag', 'AddTagsToResource'], ['redshift', 'tag', 'CreateTags'], ['redshift', 'untag', 'DeleteTags']]) {
    const expected = vector(model, operation);
    assert.deepEqual(params(query.queryBody(service(model), service(model).tagging[kind], expected.params)), params(expected.body), operation);
  }
});

test('the forms offer complex members with a control per kind', () => {
  const elasticache = service('elasticache');
  const create = resource('elasticache', 'user').operations.create;
  const members = formMembers(create, new Set());
  const kinds = Object.fromEntries(members.map(m => [m.name, control(elasticache, m)]));
  assert.equal(kinds.AuthenticationMode, 'json');
  assert.equal(kinds.Tags, 'tags');
  assert.equal(kinds.UserId, 'text');
  assert.equal(kinds.NoPasswordRequired, 'toggle');
  const mode = members.find(m => m.name === 'AuthenticationMode');
  assert.deepEqual(plain(convertField(mode, '{"Type":"iam"}', elasticache)), {Type: 'iam'});
  assert.throws(() => convertField(mode, '{"Type":"cert"}', elasticache), /AuthenticationMode.Type: must be one of password, no-password-required, iam/);
  assert.throws(() => convertField(mode, '{"Kind":"iam"}', elasticache), /AuthenticationMode: has no member Kind/);
  assert.throws(() => convertField(mode, '{Type: iam}', elasticache), /AuthenticationMode: enter a JSON value/);
  assert.throws(() => convertField(mode, '{"Passwords":"x"}', elasticache), /AuthenticationMode.Passwords: must be a list/);
  const tags = members.find(m => m.name === 'Tags');
  assert.deepEqual(plain(convertField(tags, 'team = web\n\nempty=\nurl=a=b', elasticache)), [{Key: 'team', Value: 'web'}, {Key: 'empty', Value: ''}, {Key: 'url', Value: 'a=b'}]);
  assert.throws(() => convertField(tags, '=x', elasticache), /needs a key/);
  assert.throws(() => convertField(tags, 'a=1\na=2', elasticache), /given twice/);
  assert.throws(() => convertField({name: 'N', type: 'integer'}, '1.5'), /integer/);
  assert.throws(() => convertField({name: 'N', type: 'double'}, ''), /number/);
  assert.equal(convertField({name: 'N', type: 'double'}, '2.5'), 2.5);
  assert.deepEqual(input.example(elasticache.inputShapes, 'AuthenticationMode'), {Passwords: ['string'], Type: 'password | no-password-required | iam'});
  assert.deepEqual(input.example(elasticache.inputShapes, 'TagList'), [{Key: 'string', Value: 'string'}]);
});

test('JSON and REST-JSON complex members are sent under their wire names', () => {
  const amplify = service('amplify');
  const create = resource('amplify', 'app').operations.create;
  const request = buildCatalogRequest(amplify, create, {name: 'site', tags: {team: 'web'}, environmentVariables: {A: '1'}}, 'us-east-1');
  assert.deepEqual(JSON.parse(request.options.body), {name: 'site', tags: {team: 'web'}, environmentVariables: {A: '1'}});
  const ecr = service('ecr');
  const tag = buildCatalogRequest(ecr, ecr.tagging.tag, {resourceArn: 'arn:aws:ecr:us-east-1:000000000000:repository/r', tags: input.tagValue(input.tagShape(ecr.inputShapes, 'TagList'), [['team', 'web']])}, 'us-east-1');
  assert.deepEqual(JSON.parse(tag.options.body), {resourceArn: 'arn:aws:ecr:us-east-1:000000000000:repository/r', tags: [{Key: 'team', Value: 'web'}]});
  // Nested wire renames follow the shape both ways.
  const shapes = {S: {type: 'structure', members: [{name: 'Name', wire: 'name', shape: 'T'}, {name: 'Items', wire: 'items', shape: 'L'}]}, T: {type: 'string'}, L: {type: 'list', member: {shape: 'S'}}};
  const wire = input.toWire(shapes, 'S', {Name: 'a', Items: [{Name: 'b'}]});
  assert.deepEqual(wire, {name: 'a', items: [{name: 'b'}]});
  assert.deepEqual(input.fromWire(shapes, 'S', {...wire, extra: 1}), {Name: 'a', Items: [{Name: 'b'}]});
});

test('update forms start from the described resource and tags read back in every form', () => {
  const rds = service('rds');
  const update = resource('rds', 'dbSubnetGroup').operations.update;
  assert.deepEqual(update.editable, ['DBSubnetGroupDescription', 'SubnetIds']);
  const fields = formMembers(update, new Set([update.lookup])).filter(m => update.editable.includes(m.name));
  const detail = {DBSubnetGroupName: 'app', DBSubnetGroupDescription: 'old', Subnets: [{SubnetIdentifier: 'subnet-0a1b2c3d'}]};
  assert.deepEqual(fields.map(m => fieldText(rds, m, detail[m.name])), ['old', '']);
  const elasticache = service('elasticache');
  const user = resource('elasticache', 'user').operations.create.members.find(m => m.name === 'AuthenticationMode');
  assert.equal(fieldText(elasticache, user, {Type: 'iam', Ignored: 1}), '{\n  "Type": "iam"\n}');
  const tags = resource('elasticache', 'user').operations.create.members.find(m => m.name === 'Tags');
  assert.equal(fieldText(elasticache, tags, [{Key: 'a', Value: '1'}, {Key: 'b'}]), 'a=1\nb=');
  assert.equal(fieldText(elasticache, {name: 'On', type: 'boolean'}, undefined), false);
  assert.equal(fieldText(elasticache, {name: 'L', type: 'list', element: 'string'}, ['a', 'b']), 'a, b');
  assert.deepEqual(input.tagPairs({a: '1'}), [['a', '1']]);
  assert.deepEqual(input.tagPairs([{key: 'k', value: 'v'}, {TagKey: 't', TagValue: null}, {Value: 'orphan'}]), [['k', 'v'], ['t', '']]);
  assert.deepEqual(input.tagPairs(null), []);
});

test('resources without an ARN member are tagged through the simulator ARN', () => {
  const redshift = service('redshift');
  const group = resource('redshift', 'clusterParameterGroup');
  assert.equal(resourceARN(redshift, group, {ParameterGroupName: 'app'}, 'ap-northeast-1'), 'arn:aws:redshift:ap-northeast-1:000000000000:parametergroup:app');
  assert.equal(resourceARN(redshift, group, {}, 'us-east-1'), '');
  assert.equal(resourceARN(service('rds'), resource('rds', 'dbSubnetGroup'), {DBSubnetGroupArn: 'arn:x'}, 'us-east-1'), 'arn:x');
  assert.equal(resourceARN(service('rds'), resource('rds', 'dbSubnetGroup'), {}, 'us-east-1'), '');
  assert.equal(resourceARN(redshift, group, null, 'us-east-1'), '');
});

test('validation names the member path that breaks', () => {
  const shapes = {
    S: {type: 'structure', members: [{name: 'A', shape: 'N', required: true}, {name: 'B', shape: 'L'}, {name: 'M', shape: 'M'}, {name: 'F', shape: 'F'}, {name: 'T', shape: 'Time'}, {name: 'D', shape: 'Doc', }]},
    N: {type: 'integer', min: 1, max: 3}, L: {type: 'list', member: {shape: 'Str'}, max: 1}, Str: {type: 'string', min: 1}, M: {type: 'map', key: {shape: 'Str'}, value: {shape: 'Bool'}},
    Bool: {type: 'boolean'}, F: {type: 'double'}, Time: {type: 'timestamp'}, Doc: {type: 'structure', document: true}, U: {type: 'structure', union: true, members: [{name: 'X', shape: 'Str'}, {name: 'Y', shape: 'Str'}]},
    Blob: {type: 'blob'}, Odd: {type: 'event'},
  };
  input.validateValue(shapes, 'S', {A: 2, B: ['x'], M: {k: true}, F: 1.5, T: '2026-01-01T00:00:00Z', D: {any: [1]}}, 'S');
  for (const [value, message] of [[{}, /S: needs member A/], [{A: 9}, /S.A: must be from 1 to 3/], [{A: 1.5}, /whole number/], [{A: 1, B: ['x', 'y']}, /at most 1 items/],
    [{A: 1, B: ['']}, /S.B\[0\]: needs at least 1 characters/], [{A: 1, M: {k: 'yes'}}, /S.M.k: must be true or false/], [{A: 1, M: []}, /object of entries/],
    [{A: 1, F: 'x'}, /S.F: must be a number/], [{A: 1, T: {}}, /ISO 8601/], [[], /must be an object, not a list/], [null, /not null/]]) {
    assert.throws(() => input.validateValue(shapes, 'S', value, 'S'), message);
  }
  assert.throws(() => input.validateValue(shapes, 'U', {X: 'a', Y: 'b'}, 'U'), /exactly one member/);
  assert.throws(() => input.validateValue(shapes, 'Blob', 1, 'B'), /base64/);
  assert.throws(() => input.validateValue(shapes, 'Odd', 1, 'O'), /unsupported type event/);
  assert.throws(() => input.validateValue(shapes, 'Missing', 1, 'X'), /not described/);
  assert.deepEqual(input.example(shapes, 'S'), {A: 1});
  assert.deepEqual(input.example(shapes, 'U'), {X: 'string'});
  assert.deepEqual(input.example(shapes, 'M'), {key: false});
  assert.equal(input.tagShape(shapes, 'S'), null);
  assert.equal(input.tagShape(shapes, 'Missing'), null);
});

test('Query maps, flattened lists and empty lists serialize as botocore does', () => {
  const shapes = {
    S: {type: 'structure', members: [{name: 'Flat', key: 'Item', emptyKey: 'Flat', shape: 'L', flattened: true}, {name: 'Dict', shape: 'D'}, {name: 'On', shape: 'B'}, {name: 'Pairs', shape: 'P'}]},
    L: {type: 'list', member: {shape: 'T', key: 'member'}}, D: {type: 'map', key: {shape: 'T', key: 'Name'}, value: {shape: 'T', key: 'Val'}}, T: {type: 'string'}, B: {type: 'boolean'},
    P: {type: 'list', member: {shape: 'Pair', key: 'Pair'}}, Pair: {type: 'structure', members: [{name: 'Key', shape: 'T'}]},
  };
  const form = new URLSearchParams();
  input.queryEntries(form, shapes, 'S', {Flat: ['a'], Dict: {k: 'v'}, On: false, Pairs: [{Key: 'x'}]}, '');
  assert.equal(form.toString(), 'Item.1=a&Dict.entry.1.Name=k&Dict.entry.1.Val=v&On=false&Pairs.Pair.1.Key=x');
  const empty = new URLSearchParams();
  input.queryEntries(empty, shapes, 'S', {Flat: [], Pairs: []}, 'Top');
  assert.equal(empty.toString(), 'Top.Flat=&Top.Pairs=');
});

// The whole view renders with every new control: a minimal hook runtime
// (as in gcp-appengine.test.mjs) mounts it on real descriptors, opens a
// detail and the edit form, and checks the native requests it sends.
test('the catalog view renders, opens a detail, edits and tags through native requests', async () => {
  let slots = [], cursor = 0, effects = [], tree;
  const equal = (a, b) => a && b && a.length === b.length && a.every((x, i) => x === b[i]);
  const react = {
    createElement: (type, p, ...children) => ({type, props: p || {}, children}), Fragment: 'fragment',
    useState(initial) { const i = cursor++; if (!slots[i]) slots[i] = {value: typeof initial === 'function' ? initial() : initial}; return [slots[i].value, v => { slots[i].value = typeof v === 'function' ? v(slots[i].value) : v; }]; },
    useRef(value) { const i = cursor++; return slots[i] || (slots[i] = {current: value}); },
    useMemo(fn) { cursor++; return fn(); },
    useEffect(fn, deps) { const i = cursor++; if (!slots[i] || !deps || !equal(slots[i].deps, deps)) { const old = slots[i]; slots[i] = {deps, cleanup: old?.cleanup}; effects.push(() => { old?.cleanup?.(); slots[i].cleanup = fn(); }); } },
  };
  const module = {exports: {}};
  vm.runInNewContext(compiled, {module, exports: module.exports, console, URLSearchParams,
    require: id => id === './catalog-query.mjs' ? query : id === './catalog-input.mjs' ? input : id === 'react' ? react : id === './i18n.jsx' ? {useLocale: () => ({t: en => en})} : {}});
  const rds = service('rds');
  const sent = [];
  const xml = (action, inner) => '<' + action + 'Response><' + action + 'Result>' + inner + '</' + action + 'Result><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></' + action + 'Response>';
  const group = '<DBSubnetGroup><DBSubnetGroupName>app</DBSubnetGroupName><DBSubnetGroupArn>arn:aws:rds:us-east-1:000000000000:subgrp:app</DBSubnetGroupArn><DBSubnetGroupDescription>old</DBSubnetGroupDescription></DBSubnetGroup>';
  const replies = {
    DescribeDBSubnetGroups: xml('DescribeDBSubnetGroups', '<DBSubnetGroups>' + group + '</DBSubnetGroups>'),
    ListTagsForResource: xml('ListTagsForResource', '<TagList><Tag><Key>team</Key><Value>a</Value></Tag></TagList>'),
    ModifyDBSubnetGroup: xml('ModifyDBSubnetGroup', group), AddTagsToResource: '<AddTagsToResourceResponse/>',
  };
  const request = async (path, options) => {
    if (!options) return {ok: true, json: async () => ({services: [rds]})};
    const action = new URLSearchParams(options.body).get('Action');
    sent.push(options.body);
    return {ok: true, text: async () => replies[action]};
  };
  const props = {region: 'us-east-1', request, catalog: {descriptors: '/d'}, onSaving: () => {}};
  const render = () => { cursor = 0; tree = module.exports.default(props); for (const effect of effects.splice(0)) effect(); cursor = 0; tree = module.exports.default(props); return tree; };
  const flush = async () => { for (let i = 0; i < 6; i++) { await new Promise(resolve => setImmediate(resolve)); render(); } };
  // Elements passed in props (Header actions, Form actions) are part of the tree.
  const isElement = value => value && typeof value === 'object' && 'props' in value && 'children' in value;
  const all = (node = tree) => {
    if (!isElement(node)) return [];
    const inProps = Object.values(node.props).flatMap(v => [v].flat(Infinity)).filter(isElement);
    return [node, ...[...node.children.flat(Infinity), ...inProps].flatMap(child => all(child))];
  };
  const find = predicate => all().find(predicate);
  render();
  await flush();
  const table = find(n => n.props?.columnDefinitions && n.props?.items?.length);
  assert.ok(table, 'the list shows the described group');
  const link = table.props.columnDefinitions[0].cell(table.props.items[0]);
  assert.ok(link, 'the listed group links to its details');
  link.props.onFollow({preventDefault() {}});
  await flush();
  assert.match(sent.at(-1), /Action=ListTagsForResource/);
  find(n => n.children?.flat(Infinity).includes('Edit') && n.props?.onClick).props.onClick();
  render();
  const forms = all().filter(n => n.type === 'form');
  const editForm = forms.find(f => JSON.stringify(f).includes('Save changes'));
  assert.ok(editForm, 'the edit form opens');
  // A required member left empty is refused without any request.
  const before = sent.length;
  editForm.props.onSubmit({preventDefault() {}});
  await flush();
  assert.equal(sent.length, before);
  assert.match(JSON.stringify(tree), /SubnetIds is required/);
  const fieldInputs = all().filter(n => n.props?.onChange && n.props?.value !== undefined);
  fieldInputs.find(n => n.props.value === '').props.onChange({detail: {value: 'subnet-0a1b2c3d, subnet-4e5f6a7b'}});
  render();
  all().filter(n => n.type === 'form').find(f => JSON.stringify(f).includes('Save changes')).props.onSubmit({preventDefault() {}});
  await flush();
  const modify = sent.find(body => body.includes('Action=ModifyDBSubnetGroup'));
  assert.equal(modify, 'Action=ModifyDBSubnetGroup&Version=2014-10-31&DBSubnetGroupName=app&SubnetIds.SubnetIdentifier.1=subnet-0a1b2c3d&SubnetIds.SubnetIdentifier.2=subnet-4e5f6a7b');
  assert.equal(sent.filter(body => body.includes('ModifyDBSubnetGroup')).length, 1);
  assert.match(JSON.stringify(tree), /Resource updated/);
  const keyField = find(n => n.props?.label === 'Tag key');
  keyField.children.flat(Infinity)[0].props.onChange({detail: {value: 'owner'}});
  render();
  all().filter(n => n.type === 'form').find(f => JSON.stringify(f).includes('Add tag')).props.onSubmit({preventDefault() {}});
  await flush();
  assert.ok(sent.includes('Action=AddTagsToResource&Version=2014-10-31&ResourceName=arn%3Aaws%3Ards%3Aus-east-1%3A000000000000%3Asubgrp%3Aapp&Tags.Tag.1.Key=owner&Tags.Tag.1.Value='), sent.at(-2));
  assert.match(JSON.stringify(tree), /Tag added/);
});
