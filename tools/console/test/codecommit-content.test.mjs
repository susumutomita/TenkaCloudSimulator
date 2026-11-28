import assert from 'node:assert/strict';
import test from 'node:test';
import {contentClient,encodeBytes,maxFileSize,previewText,repositoryPath} from '../src/codecommit-content.mjs';

const id='1'.repeat(40),parent='2'.repeat(40),tree='3'.repeat(40),blob='4'.repeat(40);
const user={name:'W',email:'w@example.invalid',date:'1700000000 +0000'};
const stored={commitId:id,treeId:tree,parents:[parent],message:'new',author:user,committer:user};
const data={commitId:id,blobId:blob,filePath:'nested/content',fileContent:'AP8=',fileSize:2,fileMode:'EXECUTABLE'};
const input={branchName:'main',parentCommitId:parent,filePath:'/nested/content',fileContent:'AP8=',fileMode:'EXECUTABLE',commitMessage:'new'};
const metadata={repositoryId:'original-id',repositoryName:'example'};
const answers={GetRepository:{repositoryMetadata:metadata},GetFile:data,GetBranch:{branch:{branchName:'main',commitId:id}},
  GetCommit:{commit:stored},PutFile:{commitId:id,treeId:tree,blobId:blob},CreateBranch:{}};
function harness(overrides={}) {
  const calls=[];
  const api=contentClient(async(path,options)=>{
    const operation=options.headers['X-Amz-Target'].split('.')[1];
    const body=JSON.parse(options.body);
    assert.equal(path,'/');assert.equal(options.method,'POST');
    assert.equal(options.headers['Content-Type'],'application/x-amz-json-1.1');
    assert.equal(options.headers['X-Simulator-Region'],'us-west-2');assert.equal(body.repositoryName,'example');
    calls.push({operation,body});
    const response=Object.hasOwn(overrides,operation)?overrides[operation]:answers[operation];
    if(response instanceof Error)throw response;
    return Response.json(typeof response==='function'?response(calls.length):response);
  },'us-west-2','example','original-id');
  return {api,calls};
}
test('CodeCommit permits one optional leading slash and counts UTF-8 bytes and components',()=>{
  for(const yes of ['file','/nested/file','a/'.repeat(7)+'z','é'.repeat(128)])assert(repositoryPath(yes));
  for(const no of ['','/','//file','a//b','../x','a/.GIT/b','a/.','a/','a\\b','a/'.repeat(8)+'z','é'.repeat(129),'a\nb'])assert(!repositoryPath(no));
});
test('file preview distinguishes empty, exact UTF-8 and opaque bytes',()=>{
  assert.equal(encodeBytes(Uint8Array.of(0,255)),'AP8=');
  assert.equal(previewText(new Uint8Array()),'');
  assert.equal(previewText(new TextEncoder().encode('\uFEFF日本語\n')),'\uFEFF日本語\n');
  for(const bytes of [[0,97],[255],[7,8]])assert.equal(previewText(Uint8Array.from(bytes)),null);
});
test('write checks the selected physical repository then reads exact acknowledged bytes, mode, parent and ref',async()=>{
  const {api,calls}=harness();
  const observed=await api.change(input,false,new AbortController().signal,()=>{});
  assert.deepEqual(observed.file.bytes,Uint8Array.of(0,255));assert.deepEqual(observed.commit,stored);
  assert.deepEqual(calls.map(c=>c.operation),['GetRepository','PutFile','GetFile','GetCommit','GetBranch','GetRepository']);
  assert.deepEqual(calls[1].body,{repositoryName:'example',...input});
  assert.equal(calls[2].body.commitSpecifier,id);assert.equal(calls[2].body.filePath,input.filePath);
});
test('wrong repository before mutation, altered bytes, wrong tip and changed physical readback fail verification',async()=>{
  for(const [changes,count] of [[{GetRepository:{repositoryMetadata:{...metadata,repositoryId:'replacement'}}},0],
    [{GetFile:{...data,fileContent:'AP4='}},1],[{GetCommit:{commit:{...stored,parents:[]}}},1],
    [{GetBranch:{branch:{branchName:'main',commitId:parent}}},1],
    [{GetRepository:n=>({repositoryMetadata:{...metadata,repositoryId:n===1?'original-id':'replacement'}})},1]]){
    const {api,calls}=harness(changes);
    await assert.rejects(api.change(input,false,new AbortController().signal,()=>{}));
    assert.equal(calls.filter(c=>c.operation==='PutFile').length,count);
  }
});
test('a native write refusal differs from a missing acknowledgement or a failed followup read',async()=>{
  for(const [changes,rejected] of [[{PutFile:Error('400: stale parent')},true],[{PutFile:Error('fetch failed')},false],
    [{PutFile:{}},false],[{GetFile:Error('403: denied')},false]]){
    const {api,calls}=harness(changes);
    await assert.rejects(api.change(input,false,new AbortController().signal,()=>{}),e=>!!e.writeRejected===rejected);
    assert.equal(calls.filter(c=>c.operation==='PutFile').length,1);
  }
});
test('raw GetFile validates size, mode and canonical base64 including the 64-KiB boundary',async()=>{
  const signal=new AbortController().signal;
  for(const patch of [{fileContent:'AP9='},{fileMode:'SYMLINK'},{fileSize:4},{fileSize:maxFileSize+1},{filePath:'other'}]){
    await assert.rejects(harness({GetFile:{...data,...patch}}).api.file(id,'nested/content',signal));
  }
  const bytes=new Uint8Array(maxFileSize).fill(255);
  assert.deepEqual((await harness({GetFile:{...data,fileContent:encodeBytes(bytes),fileSize:bytes.length}})
    .api.file(id,'/nested/content',signal)).bytes,bytes);
});
