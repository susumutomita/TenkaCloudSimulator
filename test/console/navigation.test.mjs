import test from 'node:test';
import assert from 'node:assert/strict';
import {PROVIDERS, SERVICES, serviceAvailable, servicesForProvider, serviceFromHash, hashForService, providerForService} from '../../tools/console/src/console-navigation.mjs';
const capabilities={services:{s3:{},ssm:{},iam:{},lambda:{},apigateway:{},cloudformation:{},kms:{},sqs:{},logs:{},secretsmanager:{},dynamodb:{}},cloudStorage:{gcp:{project:'dummy-project'},azure:{account:'dummyaccount'}}};
test('each provider is the same catalog level and contains only real console routes',()=>{
  assert.deepEqual(PROVIDERS.map(p=>p.id),['aws','gcp','azure']);
  assert.equal(servicesForProvider('aws',capabilities).length,10);
  assert.deepEqual(servicesForProvider('gcp',capabilities).map(s=>s.id),['gcp-storage']);
  assert.deepEqual(servicesForProvider('azure',capabilities).map(s=>s.id),['azure-blob']);
  for (const id of ['sqs','logs','secretsmanager']) assert.equal(serviceAvailable(id,capabilities),true);
  assert.equal(serviceAvailable('dynamodb',capabilities),false); // API-only: never advertise a nonexistent console.
  assert.equal(serviceAvailable('unknown',capabilities),false);
  assert.deepEqual(servicesForProvider('unknown',capabilities),[]);
});
test('missing capabilities cannot invent provider scopes or enabled services',()=>{
  for(const c of [undefined,{}, {cloudStorage:{gcp:{},azure:{}}}]) {
    assert.deepEqual(servicesForProvider('gcp',c),[]);assert.deepEqual(servicesForProvider('azure',c),[]);
  }
  assert.equal(serviceAvailable('s3',{}),false);
  for (const id of ['sqs','logs','secretsmanager']) assert.equal(serviceAvailable(id,{services:{}}),false);
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
    ['aws','パラメータストア',['ssm']],
    ['aws','暗号化',['kms']],
    ['aws','APIゲートウェイ',['apigateway']],
    ['aws','クラウドフォーメーション',['cfn']],
    ['aws','ストレージ',['s3']],
    ['aws','キュー',['sqs']],
    ['aws','ログ',['logs']],
    ['aws','シークレット',['secretsmanager']],
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


test('Secrets is an AWS-only capability-gated route with an exact legacy alias',()=>{
  const enabled={...capabilities,services:{...capabilities.services,secretsmanager:{}}};
  assert.equal(serviceAvailable('secretsmanager',{...capabilities,services:{...capabilities.services,secretsmanager:undefined}}),false);
  assert.equal(serviceAvailable('secretsmanager',enabled),true);
  assert.deepEqual(servicesForProvider('aws',enabled,'シークレット').map(s=>s.id),['secretsmanager']);
  assert.deepEqual(servicesForProvider('aws',enabled,'credentials').map(s=>s.id),['secretsmanager']);
  assert.deepEqual(servicesForProvider('gcp',enabled,'secret'),[]);
  assert.deepEqual(servicesForProvider('azure',enabled,'secret'),[]);
  assert.equal(servicesForProvider('aws',enabled).length,10);
  assert.equal(new Set(servicesForProvider('aws',enabled).map(service=>service.id)).size,servicesForProvider('aws',enabled).length);
  assert.equal(providerForService('secretsmanager'),'aws');
  assert.equal(serviceFromHash('#/secrets'),'secretsmanager');
  assert.equal(serviceFromHash('#/secretsmanager'),'secretsmanager');
  assert.equal(serviceFromHash('#/secretsmanager-fake'),null);
  assert.equal(hashForService('secretsmanager'),'/secretsmanager');
});
