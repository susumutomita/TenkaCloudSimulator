import test from 'node:test';
import assert from 'node:assert/strict';
import {FAVORITES_KEY, normalizeFavorites, decodeFavorites, readFavorites, writeFavorites, toggleFavorite} from '../../tools/console/src/console-favorites.mjs';
import {serviceAvailable} from '../../tools/console/src/console-navigation.mjs';
test('favorites start empty, accept only unique implemented AWS console IDs, and preserve order',()=>{
  for(const value of [null,{},'s3',42])assert.deepEqual(normalizeFavorites(value),[]);
  assert.deepEqual(normalizeFavorites(['ssm','s3','ssm','home','gcp-storage','unknown',null,{},'__proto__']),['ssm','s3']);
  for(const text of [null,'broken','{}','"s3"'])assert.deepEqual(decodeFavorites(text),[]);
});
test('pinning and removing are reversible without mutating input or changing authority',()=>{
  const ids=['ssm'];assert.deepEqual(toggleFavorite(ids,'s3'),['ssm','s3']);assert.deepEqual(ids,['ssm']);
  assert.deepEqual(toggleFavorite(['ssm','s3'],'ssm'),['s3']);
  assert.deepEqual(toggleFavorite(ids,'gcp-storage'),ids);
  const capabilities={services:{s3:{},ssm:{}},consoleScope:{parameterName:'/team/example',region:'us-east-1'}};
  const before=JSON.stringify(capabilities);toggleFavorite(ids,'s3');
  assert.equal(serviceAvailable('s3',capabilities),false);assert.equal(serviceAvailable('ssm',capabilities),true);assert.equal(JSON.stringify(capabilities),before);
});
test('persistence stores only service IDs and restores empty/removal state',()=>{
  const values=new Map(),storage={getItem:key=>values.get(key)??null,setItem:(key,value)=>values.set(key,value)};
  assert.deepEqual(readFavorites(storage),[]);assert.equal(writeFavorites(storage,['s3','ssm','invalid']),true);
  assert.equal(values.get(FAVORITES_KEY),'["s3","ssm"]');assert.deepEqual(readFavorites(storage),['s3','ssm']);
  assert.equal(writeFavorites(storage,[]),true);assert.deepEqual(readFavorites(storage),[]);
});
test('blocked/corrupt browser storage does not break console navigation or claim persistence',()=>{
  const storage={getItem:()=>{throw new Error('blocked')},setItem:()=>{throw new Error('blocked')}};
  assert.deepEqual(readFavorites(storage),[]);assert.equal(writeFavorites(storage,['s3']),false);
  assert.deepEqual(readFavorites(null),[]);assert.equal(writeFavorites(null,[]),false);
});
