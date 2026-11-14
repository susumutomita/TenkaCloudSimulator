import test from 'node:test';
import assert from 'node:assert/strict';
import {SourceDrafts,sourceZip,blueprint} from '../src/lambda-source.mjs';
const config=(name='one',revision='revision-1')=>({FunctionName:name,RevisionId:revision,Handler:'lambda_function.lambda_handler'});
test('ZIP contains precisely the edited UTF-8 file, not a fixed blueprint',()=>{
 const source='def lambda_handler(event, context):\n    return "こんにちは"\n';
 const bytes=Buffer.from(sourceZip(source),'base64');
 assert.equal(bytes.readUInt32LE(0),0x04034b50);
 const size=bytes.readUInt32LE(18),nameSize=bytes.readUInt16LE(26);
 assert.equal(bytes.subarray(30,30+nameSize).toString(),'lambda_function.py');
 assert.equal(bytes.subarray(30+nameSize,30+nameSize+size).toString(),source);
 assert.equal(bytes.readUInt16LE(bytes.length-12),1);
 assert.equal(bytes.readUInt32LE(30+nameSize+size),0x02014b50);
 assert.throws(()=>sourceZip(' '));assert.throws(()=>sourceZip('a'.repeat(262144)));
});
test('unknown or uploaded packages are never inferred from a blueprint',()=>{
 const drafts=new SourceDrafts();assert.equal(drafts.get(config()),null);assert.throws(()=>drafts.edit(config(),'danger'));
 drafts.remember(config(),blueprint);drafts.forget('one');assert.equal(drafts.get(config()),null);
 assert.throws(()=>drafts.remember({...config(),Handler:''},blueprint));
 const saved={...config(),Handler:'index.handler',Path:'index.py'};drafts.remember(saved,'from helper import answer');assert.equal(drafts.get(saved).path,'index.py');assert.equal(drafts.get(saved).source,'from helper import answer');
 assert.equal(drafts.get(config()),null);
});
test('draft and deployed versions are separated and isolated across functions',()=>{
 const drafts=new SourceDrafts();drafts.remember(config(),blueprint);drafts.remember(config('two'),blueprint);
 drafts.edit(config(),'changed');assert.equal(drafts.get(config()).deployed,blueprint);assert.equal(drafts.get(config()).source,'changed');
 assert.equal(drafts.get(config('two')).source,blueprint);assert.equal(drafts.dirty(),true);
 drafts.remember(config('one','revision-2'),'changed');assert.equal(drafts.dirty(),false);assert.equal(drafts.get(config('one','revision-2')).source,'changed');
});
test('external revision/handler changes fail closed; reload does not resurrect source',()=>{
 const drafts=new SourceDrafts();drafts.remember(config(),blueprint);drafts.edit(config(),'draft');
 assert.equal(drafts.get(config('one','external')),null);
 assert.equal(drafts.get({...config(),Handler:'other.handler'}),null);
 assert.equal(new SourceDrafts().get(config()),null);
 const copy=drafts.get(config());copy.source='tamper';assert.equal(drafts.get(config()).source,'draft');
});

test('exact ZIP byte boundary and stale draft recovery',()=>{
 const max=262144-(30+46+22+2*18);assert.equal(Buffer.from(sourceZip('a'.repeat(max)),'base64').length,262144);assert.throws(()=>sourceZip('a'.repeat(max+1)));
 const drafts=new SourceDrafts();drafts.remember(config(),blueprint);drafts.edit(config(),'precious draft');assert.equal(drafts.get(config('one','external')),null);assert.equal(drafts.recover('one').source,'precious draft');
});
