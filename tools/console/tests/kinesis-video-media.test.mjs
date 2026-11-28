import test from 'node:test';
import assert from 'node:assert/strict';
import {currentStream, localMediaPath, limitedBytes, readMediaChunks, uploadReceipt} from '../src/kinesis-video-media.mjs';

test('the endpoint binds the exact local listener and supported media operation',()=>{
  const origin='http://127.0.0.1:48123';
  for(const endpoint of [origin+'/',origin+'/getMedia',origin+'?x',origin+'#x','http://localhost:48123','http://127.0.0.1:48124','https://127.0.0.1:48123','http://x@127.0.0.1:48123','http://example.test']){
    assert.throws(()=>localMediaPath(endpoint,origin,'PUT_MEDIA'));
  }
  assert.throws(()=>localMediaPath('http://example.test','http://example.test','GET_MEDIA'));
  assert.throws(()=>localMediaPath(origin,origin,'LIST'));
  assert.equal(localMediaPath(origin,origin,'PUT_MEDIA'),origin+'/putMedia');
  assert.equal(localMediaPath('http://[::1]:12','http://[::1]:12','GET_MEDIA'),'http://[::1]:12/getMedia');
});
test('the selected stream is active, supported and physically bound to its region',()=>{
  const info={StreamARN:'arn:aws:kinesisvideo:us-west-2:000000000000:stream/demo/123',StreamName:'demo',Status:'ACTIVE',KmsKeyId:'alias/aws/kinesisvideo',DataRetentionInHours:0};
  assert.ok(currentStream(info,'us-west-2'));
  for(const change of [{StreamName:'another'},{Status:'CREATING'},{KmsKeyId:'other'},{DataRetentionInHours:-1}])assert.equal(currentStream({...info,...change},'us-west-2'),false);
  assert.equal(currentStream(info,'us-east-1'),false);
});
test('zero-retention and durable acknowledgements have distinct complete receipts',()=>{
  function receipt(events){return new TextEncoder().encode(events.map(e=>JSON.stringify(e)).join('\n')+'\n')}
  const received=['BUFFERING','RECEIVED'].map(EventType=>({EventType,FragmentTimecode:0}));
  const saved=[...received,{EventType:'PERSISTED',FragmentTimecode:0}].map(e=>({...e,FragmentNumber:'14'}));
  assert.equal(uploadReceipt(receipt(received)).number,undefined);
  assert.equal(uploadReceipt(receipt(saved)).number,'14');
  for(const wrong of [[received[0]],saved.slice(1),[...saved.slice(0,2),{...saved[2],FragmentNumber:'15'}],[{...received[0],FragmentNumber:'1'},received[1]]])assert.throws(()=>uploadReceipt(receipt(wrong)));
});
test('response reads are bounded and EOF is an empty result',async()=>{
  assert.deepEqual(readMediaChunks(await limitedBytes(new Response(new Uint8Array()))),[]);
  await assert.rejects(limitedBytes(new Response(new Uint8Array(9)),8));
  await assert.rejects(limitedBytes(new Response('abc',{headers:{'Content-Length':'9'}}),8));
  for(const partial of [[0],[0x1a,0x45,0xdf,0xa3,0x80],[0x1a,0x45,0xdf,0xa3,0xff]])assert.throws(()=>readMediaChunks(Uint8Array.from(partial)));
});
