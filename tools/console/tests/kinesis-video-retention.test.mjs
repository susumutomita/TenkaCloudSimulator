import test from 'node:test';
import assert from 'node:assert/strict';
import {isKinesisVideoRetention,kinesisRetentionEdit,kinesisRetentionUpdate} from '../src/kinesis-video-retention.mjs';

const service={model:'kinesisvideo'};
const resource={kind:'stream'};
const operation={name:'UpdateDataRetention',uri:'/updateDataRetention',lookup:'StreamName',token:'CurrentVersion'};
const edit=(hours,token='Abc1')=>kinesisRetentionEdit({StreamName:'cam-1',DataRetentionInHours:hours},token);

test('absolute retention only replaces exact Kinesis Video stream edit descriptor',()=>{
 assert.ok(isKinesisVideoRetention(service,resource,operation));
 for(const args of [[{model:'kinesis'},resource,operation],[service,{kind:'channel'},operation],
                    [service,resource,{...operation,name:'UpdateStream'}],
                    [service,resource,{...operation,token:undefined}],
                    [service,resource,{...operation,uri:'/updateStream'}]])
  assert.ok(!isKinesisVideoRetention(...args));
});

test('use the freshly read stream identity and version for increase and decrease',()=>{
 assert.deepEqual(kinesisRetentionUpdate({...edit(24),hours:'48'}),{input:{
  StreamName:'cam-1',CurrentVersion:'Abc1',Operation:'INCREASE_DATA_RETENTION',DataRetentionChangeInHours:24}});
 assert.deepEqual(kinesisRetentionUpdate({...edit(48,'Fresh01'),hours:'24'}),{input:{
  StreamName:'cam-1',CurrentVersion:'Fresh01',Operation:'DECREASE_DATA_RETENTION',DataRetentionChangeInHours:24}});
});

test('toggle no retention without a synthetic mutation when unchanged',()=>{
 assert.deepEqual(kinesisRetentionUpdate({...edit(48),enabled:false,hours:'n/a'}),{input:{
  StreamName:'cam-1',CurrentVersion:'Abc1',Operation:'DECREASE_DATA_RETENTION',DataRetentionChangeInHours:48}});
 assert.deepEqual(kinesisRetentionUpdate(edit(0)),{problem:'unchanged'});
 assert.deepEqual(kinesisRetentionUpdate({...edit(0),enabled:true}),{input:{
  StreamName:'cam-1',CurrentVersion:'Abc1',Operation:'INCREASE_DATA_RETENTION',DataRetentionChangeInHours:1}});
 assert.deepEqual(kinesisRetentionUpdate({...edit(24),hours:'00024'}),{problem:'unchanged'});
});

test('missing or malformed snapshot, version and hours never generate a write',()=>{
 assert.equal(edit(undefined),null);
 assert.equal(edit('24'),null);
 assert.equal(edit(0,''),null);
 assert.equal(kinesisRetentionEdit({StreamName:'wrong / row',DataRetentionInHours:24},'A1'),null);
 assert.deepEqual(kinesisRetentionUpdate({...edit(24),token:'bad version',hours:'48'}),{problem:'snapshot'});
 for(const hours of ['0','1.5','12e1','87601','','-42','Infinity'])
  assert.deepEqual(kinesisRetentionUpdate({...edit(24),hours}),{problem:'hours'});
 assert.deepEqual(kinesisRetentionUpdate(edit(1)),{problem:'unchanged'});
 assert.deepEqual(kinesisRetentionUpdate({...edit(1),hours:'7'}),{input:{
  StreamName:'cam-1',CurrentVersion:'Abc1',Operation:'INCREASE_DATA_RETENTION',DataRetentionChangeInHours:6}});
 assert.deepEqual(kinesisRetentionUpdate({...edit(7),hours:'1'}),{input:{
  StreamName:'cam-1',CurrentVersion:'Abc1',Operation:'DECREASE_DATA_RETENTION',DataRetentionChangeInHours:6}});
 assert.equal(kinesisRetentionUpdate({...edit(24),hours:'87600'}).input.DataRetentionChangeInHours,87576);
});
