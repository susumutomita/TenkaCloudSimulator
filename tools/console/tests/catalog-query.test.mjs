import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import {transformSync} from 'esbuild';
import * as query from '../src/catalog-query.mjs';
import * as input from '../src/catalog-input.mjs';

// The catalog view's request builder, compiled from the real source.
const source = fs.readFileSync(new URL('../src/catalog.jsx', import.meta.url), 'utf8');
const compiled = transformSync(source, {loader: 'jsx', format: 'cjs', target: 'es2022'}).code;
const view = {exports: {}};
vm.runInNewContext(compiled, {module: view, exports: view.exports, console, URLSearchParams,
  require: id => id === './catalog-query.mjs' ? query : id === './catalog-input.mjs' ? input : id === 'react' ? {} : id === './i18n.jsx' ? {useLocale: () => ({t: en => en})} : {}});
const {buildCatalogRequest} = view.exports;

// Bodies botocore 1.41.6's own QuerySerializer produced for these inputs
// (tools/awsmodels/query_vectors.py writes them).
const vectors = JSON.parse(fs.readFileSync(new URL('../../../internal/aws/catalog/testdata/query_serializer.json', import.meta.url), 'utf8'));
const botocore = operation => vectors.cases.find(c => c.model === 'rds' && c.operation === operation);

// Descriptor fragments as /_simulator/v1/catalog/aws serves them for RDS.
const rds = {
  model: 'rds', protocol: 'query', apiVersion: '2014-10-31', signingName: 'rds',
  xmlShapes: {
    DBSubnetGroupMessage: {type: 'structure', members: [{name: 'DBSubnetGroups', element: 'DBSubnetGroups', shape: 'DBSubnetGroups'}, {name: 'Marker', element: 'Marker', shape: 'String'}]},
    DBSubnetGroups: {type: 'list', item: {name: '', element: 'DBSubnetGroup', shape: 'DBSubnetGroup'}},
    DBSubnetGroup: {type: 'structure', members: [{name: 'DBSubnetGroupArn', element: 'DBSubnetGroupArn', shape: 'String'}, {name: 'DBSubnetGroupName', element: 'DBSubnetGroupName', shape: 'String'},
      {name: 'Subnets', element: 'Subnets', shape: 'SubnetList'}, {name: 'SupportedNetworkTypes', element: 'SupportedNetworkTypes', shape: 'StringList'}]},
    SubnetList: {type: 'list', item: {name: '', element: 'Subnet', shape: 'Subnet'}},
    Subnet: {type: 'structure', members: [{name: 'SubnetIdentifier', element: 'SubnetIdentifier', shape: 'String'}]},
    StringList: {type: 'list', item: {name: '', element: 'member', shape: 'String'}},
    String: {type: 'string'}, Integer: {type: 'integer'}, Boolean: {type: 'boolean'},
    TagListMessage: {type: 'structure', members: [{name: 'TagList', element: 'TagList', shape: 'TagList'}]},
    TagList: {type: 'list', item: {name: '', element: 'Tag', shape: 'Tag'}},
    Tag: {type: 'structure', members: [{name: 'Key', element: 'Key', shape: 'String'}, {name: 'Value', element: 'Value', shape: 'String'}]},
    Flat: {type: 'structure', members: [{name: 'Items', element: 'Item', shape: 'StringList', flattened: true}, {name: 'Count', element: 'Count', shape: 'Integer'},
      {name: 'On', element: 'On', shape: 'Boolean'}, {name: 'Labels', element: 'Labels', shape: 'Labels'}, {name: 'Flags', element: 'Flag', shape: 'Labels', flattened: true}]},
    Labels: {type: 'map', key: {name: '', element: 'key', shape: 'String'}, value: {name: '', element: 'value', shape: 'String'}},
  },
};
const modify = {name: 'ModifyDBSubnetGroup', members: [{name: 'DBSubnetGroupDescription', type: 'string', key: 'DBSubnetGroupDescription'}, {name: 'DBSubnetGroupName', type: 'string', key: 'DBSubnetGroupName'},
  {name: 'SubnetIds', type: 'list', element: 'string', key: 'SubnetIds', itemKey: 'SubnetIdentifier'}]};
const untag = {name: 'RemoveTagsFromResource', members: [{name: 'ResourceName', type: 'string', key: 'ResourceName'}, {name: 'TagKeys', type: 'list', element: 'string', key: 'TagKeys', itemKey: 'member'}]};
const describe = {name: 'DescribeDBSubnetGroups', output: 'DBSubnetGroupMessage', resultWrapper: 'DescribeDBSubnetGroupsResult',
  members: [{name: 'DBSubnetGroupName', type: 'string', key: 'DBSubnetGroupName'}, {name: 'Filters', type: 'list', element: 'structure', key: 'Filters', itemKey: 'Filter'}, {name: 'MaxRecords', type: 'integer', key: 'MaxRecords'}]};

test('Query request bodies are byte-identical to botocore QuerySerializer output', () => {
  for (const [op, operation] of [[modify, 'ModifyDBSubnetGroup'], [untag, 'RemoveTagsFromResource']]) {
    const vector = botocore(operation);
    assert.equal(query.queryBody(rds, op, vector.params), vector.body, operation);
  }
  const request = buildCatalogRequest(rds, untag, botocore('RemoveTagsFromResource').params, 'ap-northeast-1');
  assert.equal(request.path, '/');
  assert.equal(request.options.method, 'POST');
  assert.equal(request.options.body, botocore('RemoveTagsFromResource').body);
  assert.deepEqual({...request.options.headers}, {'Content-Type': 'application/x-www-form-urlencoded; charset=utf-8', 'X-Simulator-Service': 'rds', 'X-Simulator-Region': 'ap-northeast-1'});
});

test('Query bodies encode empty, flattened and scalar members as botocore does', () => {
  // botocore sends an empty Filters=[] as "Filters=".
  assert.equal(query.queryBody(rds, describe, {DBSubnetGroupName: 'app', Filters: [], MaxRecords: 20}), 'Action=DescribeDBSubnetGroups&Version=2014-10-31&DBSubnetGroupName=app&Filters=&MaxRecords=20');
  const flat = {name: 'Echo', members: [{name: 'Flat', type: 'list', element: 'string', key: 'Item', emptyKey: 'Flat', flattened: true}, {name: 'On', type: 'boolean', key: 'On'}, {name: 'Text', type: 'string', key: 'Alias'}]};
  assert.equal(query.queryBody({apiVersion: '1'}, flat, {Flat: ['a', 'b c'], On: false, Text: 'x&y=+'}), 'Action=Echo&Version=1&Item.1=a&Item.2=b+c&On=false&Alias=x%26y%3D%2B');
  assert.equal(query.queryBody({apiVersion: '1'}, flat, {Flat: []}), 'Action=Echo&Version=1&Flat=');
  assert.throws(() => query.queryBody(rds, describe, {Filters: 'x'}), /must be a list/);
  assert.throws(() => query.queryBody(rds, {name: 'X', members: [{name: 'S', type: 'structure'}]}, {S: {}}), /not editable/);
  assert.throws(() => query.queryBody(rds, untag, {ResourceName: {}}), /single value/);
});

test('JSON and REST requests are built exactly as before', () => {
  const json = buildCatalogRequest({protocol: 'json', jsonVersion: '1.1', targetPrefix: 'T_1'}, {name: 'Op', members: [{name: 'a', locationName: 'A'}]}, {a: 1}, 'us-east-1');
  assert.deepEqual(JSON.parse(JSON.stringify(json)), {path: '/', options: {method: 'POST', headers: {'Content-Type': 'application/x-amz-json-1.1', 'X-Amz-Target': 'T_1.Op', 'X-Simulator-Region': 'us-east-1'}, body: '{"A":1}'}});
  const rest = buildCatalogRequest({protocol: 'rest-json', signingName: 'svc'}, {name: 'Get', method: 'GET', uri: '/things/{id}?x=1', members: [{name: 'id', location: 'uri'}, {name: 'q', location: 'querystring'}]}, {id: 'a b', q: ['1', '2']}, 'us-east-1');
  assert.equal(rest.path, '/things/a%20b?x=1&q=1&q=2');
  assert.equal(rest.options.method, 'GET');
  assert.equal(rest.options.body, undefined);
});

const envelope = (action, inner) => '<?xml version="1.0" encoding="UTF-8"?>\n<' + action + 'Response xmlns="http://rds.amazonaws.com/doc/2014-10-31/">' + inner +
  '<ResponseMetadata><RequestId>035b7573-e153-484a-9484-ce6026ada0ef</RequestId></ResponseMetadata></' + action + 'Response>';

test('Query XML responses decode with the described output shapes', () => {
  const one = envelope('DescribeDBSubnetGroups', '<DescribeDBSubnetGroupsResult><DBSubnetGroups><DBSubnetGroup><DBSubnetGroupArn>arn:aws:rds:us-east-1:000000000000:subgrp:a</DBSubnetGroupArn><DBSubnetGroupName>a &amp; &lt;b&gt; &#x263A;</DBSubnetGroupName><Subnets><Subnet><SubnetIdentifier>subnet-0a1b2c3d</SubnetIdentifier></Subnet></Subnets><SupportedNetworkTypes/></DBSubnetGroup></DBSubnetGroups><Marker>m</Marker></DescribeDBSubnetGroupsResult>');
  const page = query.decodeQuery(rds, describe, one);
  // One element is still a list, an empty element an empty list, an absent member missing.
  assert.deepEqual(page, {DBSubnetGroups: [{DBSubnetGroupArn: 'arn:aws:rds:us-east-1:000000000000:subgrp:a', DBSubnetGroupName: 'a & <b> ☺', Subnets: [{SubnetIdentifier: 'subnet-0a1b2c3d'}], SupportedNetworkTypes: []}], Marker: 'm'});
  assert.deepEqual(query.decodeQuery(rds, describe, envelope('DescribeDBSubnetGroups', '<DescribeDBSubnetGroupsResult><DBSubnetGroups></DBSubnetGroups></DescribeDBSubnetGroupsResult>')), {DBSubnetGroups: []});
  const tags = {name: 'ListTagsForResource', output: 'TagListMessage', resultWrapper: 'ListTagsForResourceResult'};
  assert.deepEqual(query.decodeQuery(rds, tags, envelope('ListTagsForResource', '<ListTagsForResourceResult><TagList><Tag><Key>team</Key><Value></Value></Tag></TagList></ListTagsForResourceResult>')), {TagList: [{Key: 'team', Value: ''}]});
  assert.deepEqual(query.decodeQuery(rds, {name: 'DeleteDBSubnetGroup'}, envelope('DeleteDBSubnetGroup', '')), {});
  const flat = {name: 'Echo', output: 'Flat'};
  assert.deepEqual(query.decodeQuery(rds, flat, '<EchoResponse><Item>a</Item><Item>b</Item><Count>3</Count><On>true</On><Labels><entry><key>k</key><value>v</value></entry></Labels><Flag><key>f</key><value>1</value></Flag></EchoResponse>'),
    {Items: ['a', 'b'], Count: 3, On: true, Labels: {k: 'v'}, Flags: {f: '1'}});
  assert.throws(() => query.decodeQuery(rds, flat, '<EchoResponse><Count>many</Count></EchoResponse>'), /not a number/);
  assert.throws(() => query.decodeQuery(rds, describe, envelope('Other', '')), /Unexpected response/);
  assert.throws(() => query.decodeQuery(rds, describe, envelope('DescribeDBSubnetGroups', '')), /lacks DescribeDBSubnetGroupsResult/);
  assert.throws(() => query.decodeQuery({}, describe, one), /No output shape/);
});

test('Query errors surface the service Code and Message', () => {
  const error = '<?xml version="1.0" encoding="UTF-8"?>\n<ErrorResponse xmlns="http://rds.amazonaws.com/doc/2014-10-31/"><Error><Type>Sender</Type><Code>DBSubnetGroupNotFoundFault</Code><Message>The dbSubnetGroup &lt;x&gt; does not exist.</Message></Error><RequestId>r</RequestId></ErrorResponse>';
  assert.throws(() => query.decodeQuery(rds, describe, error), {message: 'DBSubnetGroupNotFoundFault: The dbSubnetGroup <x> does not exist.'});
});

test('the XML reader refuses documents it cannot read', () => {
  assert.deepEqual(query.parseXML('<a:R x="1" y=\'2\'><!-- c --><![CDATA[<raw>]]><b/></a:R>'), {name: 'R', text: '<raw>', children: [{name: 'b', text: '', children: []}]});
  for (const bad of ['', '<a>', '<a></b>', '<a/><b/>', '</a>', '<a>x</a>trailing<', 'plain']) assert.throws(() => query.parseXML(bad), /well-formed/, bad);
  assert.equal(query.parseXML('<a>&unknown; &#99999999;</a>').text, '&unknown; &#99999999;');
});
