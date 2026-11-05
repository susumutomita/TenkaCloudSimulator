import test from 'node:test';
import assert from 'node:assert/strict';
import {PROVIDERS, SERVICES, serviceAvailable, servicesForProvider, serviceFromHash, hashForService, providerForService} from '../../tools/console/src/console-navigation.mjs';
const capabilities={services:{s3:{},ssm:{},iam:{},lambda:{},apigateway:{},cloudformation:{},kms:{},sqs:{}},cloudStorage:{gcp:{project:'dummy-project'},azure:{account:'dummyaccount'}}};
test('each provider is the same catalog level and contains only real console routes',()=>{
  assert.deepEqual(PROVIDERS.map(p=>p.id),['aws','gcp','azure']);
  assert.equal(servicesForProvider('aws',capabilities).length,8);
  assert.deepEqual(servicesForProvider('gcp',capabilities).map(s=>s.id),['gcp-storage']);
  assert.deepEqual(servicesForProvider('azure',capabilities).map(s=>s.id),['azure-blob']);
  assert.equal(serviceAvailable('sqs',capabilities),true);
  assert.equal(serviceAvailable('unknown',capabilities),false);
  assert.deepEqual(servicesForProvider('unknown',capabilities),[]);
});
test('missing capabilities cannot invent provider scopes or enabled services',()=>{
  for(const c of [undefined,{}, {cloudStorage:{gcp:{},azure:{}}}]) {
    assert.deepEqual(servicesForProvider('gcp',c),[]);assert.deepEqual(servicesForProvider('azure',c),[]);
  }
  assert.equal(serviceAvailable('s3',{}),false);
  assert.equal(serviceAvailable('sqs',{services:{}}),false);
  assert.equal(serviceAvailable('gcp-storage',{cloudStorage:{gcp:{project:''}}}),false);
});
test('English/Japanese filtering is provider scoped and never mutates capabilities',()=>{
  const before=JSON.stringify(capabilities);
  assert.deepEqual(servicesForProvider('aws',capabilities,'パラメータ').map(s=>s.id),['ssm']);
  assert.deepEqual(servicesForProvider('aws',capabilities,'ｌａｍｂｄａ').map(s=>s.id),['lambda']);
  assert.deepEqual(servicesForProvider('azure',capabilities,'google'),[]);
  assert.deepEqual(servicesForProvider('gcp',capabilities,'コンテナ'),[]);
  assert.equal(JSON.stringify(capabilities),before);
});
test('existing hashes and aliases roundtrip; unknown routes do not claim S3 support',()=>{
  for(const s of SERVICES) assert.equal(serviceFromHash('#'+hashForService(s.id)),s.id);
  for(const [alias,id] of [['parameters','ssm'],['stacks','cfn'],['functions','lambda'],['roles','iam']])assert.equal(serviceFromHash('#/'+alias),id);
  assert.equal(serviceFromHash(''),'s3');
  for(const bad of ['#/unknown','#/gcp-storage-fake','#/azure-blob-fake','#account'])assert.equal(serviceFromHash(bad),null);
  assert.equal(hashForService('unknown'),null);assert.equal(providerForService('unknown'),null);
});

test('natural Japanese aliases find canonical services within the selected cloud',()=>{
  for(const [provider,query,expected] of [
    ['aws','ラムダ',['lambda']],
    ['aws','キュー',['sqs']],
    ['aws','シンプルキューサービス',['sqs']],
    ['gcp','キュー',[]],
    ['aws','パラメータストア',['ssm']],
    ['aws','暗号化',['kms']],
    ['aws','APIゲートウェイ',['apigateway']],
    ['aws','クラウドフォーメーション',['cfn']],
    ['aws','ストレージ',['s3']],
    ['gcp','ストレージ',['gcp-storage']],
    ['gcp','クラウドストレージ',['gcp-storage']],
    ['azure','ストレージ',['azure-blob']],
    ['azure','ブロブストレージ',['azure-blob']],
    ['gcp','ラムダ',[]],
    ['azure','暗号化',[]],
  ]) assert.deepEqual(servicesForProvider(provider,capabilities,query).map(s=>s.id),expected);
  assert.deepEqual(servicesForProvider('gcp',{cloudStorage:{gcp:{project:''}}},'ストレージ'),[]);
  assert.deepEqual(servicesForProvider('aws',{services:{}},'ラムダ'),[]);
});
