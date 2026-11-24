import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {humanize, matches, phrase, pickColumns, plural, tableShape, words} from '../src/catalog-names.mjs';

test('official names become sentence-case labels with acronyms intact', () => {
  const cases = {
    DBSubnetGroupName: 'DB subnet group name', dbSubnetGroup: 'DB subnet group', CacheParameterGroupFamily: 'Cache parameter group family',
    VpcId: 'VPC ID', KmsKeyId: 'KMS key ID', SubnetIds: 'Subnet IDs', DBClusterParameterGroupArn: 'DB cluster parameter group ARN', ARN: 'ARN',
    repositoryName: 'Repository name', imageScanningConfiguration: 'Image scanning configuration', IAMRoleArn: 'IAM role ARN', iamRoleArn: 'IAM role ARN',
    Ipv6CidrBlock: 'IPv6 CIDR block', IPv6Address: 'IPv6 address', EC2InstanceType: 'EC2 instance type', S3BucketName: 'S3 bucket name', userId: 'User ID',
    enableDnsHostnames: 'Enable DNS hostnames', HTTPEndpoint: 'HTTP endpoint', 'snake_case-name': 'Snake case name', OAuthToken: 'OAuth token', MaxRecords: 'Max records',
    TTL: 'TTL', engine: 'Engine', a: 'A',
  };
  for (const [name, label] of Object.entries(cases)) assert.equal(humanize(name), label, name);
  assert.deepEqual(words('DBSubnetGroupName'), ['db', 'subnet', 'group', 'name']);
  assert.equal(humanize(''), '');
  assert.equal(humanize(undefined), '');
  assert.equal(humanize('---'), '---');
});

test('kinds take English plurals and read inside sentences', () => {
  const cases = {'DB subnet group': 'DB subnet groups', Policy: 'Policies', Alias: 'Aliases', Key: 'Keys', Box: 'Boxes', Match: 'Matches', Mesh: 'Meshes',
    'Access control ACL': 'Access control ACLs', 'Item IDs': 'Item IDs', 'Data catalog': 'Data catalogs', 'Web ACL': 'Web ACLs', '': ''};
  for (const [label, many] of Object.entries(cases)) assert.equal(plural(label), many, label);
  assert.equal(phrase('DB subnet groups'), 'DB subnet groups');
  assert.equal(phrase('Users'), 'users');
  assert.equal(phrase('IPv6 addresses'), 'IPv6 addresses');
  assert.equal(phrase('A'), 'a');
  assert.equal(phrase(''), '');
});

test('list columns rank state, kind and description among loaded scalars', () => {
  const rows = [
    {Name: 'a', Arn: 'arn:a', Status: 'ACTIVE', Description: 'd', Engine: 'mysql', Nested: {x: 1}, Count: 2, Empty: ''},
    {Name: 'b', Arn: 'arn:b', Status: 'CREATING', CreationTime: 5, Count: 3},
  ];
  assert.deepEqual(pickColumns(rows, ['Name', 'Arn']), ['Status', 'Engine', 'Description']);
  assert.deepEqual(pickColumns(rows, ['Name', 'Arn'], 5), ['Status', 'Engine', 'Description', 'CreationTime', 'Count']);
  assert.deepEqual(pickColumns([], []), []);
  assert.deepEqual(pickColumns([null, {B: 1, A: 1}], []), ['A', 'B']);
});

test('lists of flat structures read as tables; text filters scalar values', () => {
  assert.deepEqual(tableShape([{SubnetIdentifier: 's-1'}, {SubnetIdentifier: 's-2', Zone: 'a', Ports: [1, 2]}]), ['SubnetIdentifier', 'Zone', 'Ports']);
  for (const value of [[], [{a: {b: 1}}], [{a: [{b: 1}]}], ['x'], {a: 1}, [[1]], null]) assert.equal(tableShape(value), null, JSON.stringify(value));
  const row = {Name: 'App-Subnets', Count: 3, Deep: {Name: 'hidden'}};
  assert.ok(matches(row, 'app-'));
  assert.ok(matches(row, ' 3 '));
  assert.ok(!matches(row, 'hidden'));
  assert.ok(matches(row, ''));
  assert.ok(!matches(null, 'x'));
});

test('the catalog view keeps no per-service label tables', () => {
  const source = fs.readFileSync(new URL('../src/catalog.jsx', import.meta.url), 'utf8');
  for (const model of ['rds', 'elasticache', 'redshift', 'amplify', 'ecr']) assert.ok(!new RegExp(`['"]${model}['"]`).test(source), model);
});
