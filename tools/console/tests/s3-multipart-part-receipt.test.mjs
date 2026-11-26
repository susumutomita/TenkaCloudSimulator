import assert from 'node:assert/strict';
import test from 'node:test';
import {confirmsSavedMultipartPart, listMultipartParts, parseMultipartPartPage} from '../src/s3-multipart-parts.mjs';

const ns = 'http://s3.amazonaws.com/doc/2006-03-01/';
const scope = {bucket:'parts-example',key:'some /escaped & key',uploadId:'abc123',marker:0};
const etag = '"5d41402abc4b2a76b9719d911017c592"';
const otherETag = '"7d793037a0760186574b0282f2f435e7"';
const element = (localName, textContent = '', children = [], namespaceURI = ns) =>
  ({localName, textContent, namespaceURI, children});

function page({scope:bound = scope, parts = [['1','0',etag]], extra = [], tag = 'ListPartsResult', xmlns = ns, more = 'false'} = {}) {
  return {doctype:null,documentElement:element(tag,'',[
    element('Bucket',bound.bucket),element('Key',bound.key),element('UploadId',bound.uploadId),
    element('PartNumberMarker',String(bound.marker)),element('MaxParts','1000'),element('IsTruncated',more),
    ...parts.map(([number,size,hash])=>element('Part','',[
      element('PartNumber',number),element('Size',size),element('ETag',hash)
    ])),...extra
  ],xmlns)};
}

test('typed ListParts echoes the selected scope and preserves a genuine zero-byte or maximum-size part', () => {
  const result = parseMultipartPartPage(page({parts:[['1','0',etag],['10000','8388608',otherETag]]}),scope);
  assert.deepEqual(result,{parts:[{number:1,size:0,etag},{number:10000,size:8388608,etag:otherETag}],
    truncated:'false',nextMarker:''});
  assert.equal(confirmsSavedMultipartPart(result.parts,{number:1,size:0,etag,status:200}),true);
});

test('each ListParts page must echo exact bucket, key, upload and request marker', () => {
  for (const change of [{bucket:'other-bucket'},{key:'same but not exact'},{uploadId:'another'},{marker:1}]) {
    assert.throws(()=>parseMultipartPartPage(page({scope:{...scope,...change}}),scope),/out-of-scope/);
  }
  for (const bad of [page({tag:'NoSuchUpload'}),page({xmlns:'urn:other'}),page({extra:[element('UploadId',scope.uploadId)]}),
    page({extra:[element('NextPartNumberMarker','1')]}),{...page(),doctype:{}}]) {
    assert.throws(()=>parseMultipartPartPage(bad,scope),/Invalid/);
  }
});

test('malformed stored metadata cannot confirm a saved part', () => {
  for (const part of [['1','',etag],['1','8388609',etag],['1','1e2',etag],['1','-1',etag],['01','1',etag],
    ['1','1','W/'+etag],['1','1',etag+'\n'],['0','1',etag],['10001','1',etag]]) {
    assert.throws(()=>parseMultipartPartPage(page({parts:[part]}),scope),/Invalid/);
  }
});

test('confirmation requires actual UploadPart status plus exact number, size and strong ETag', () => {
  const receipt = {number:1,size:5,etag,status:200};
  const same = [{number:1,size:5,etag}];
  assert.equal(confirmsSavedMultipartPart(same,receipt),true);
  for (const stored of [[],[{number:1,size:5,etag:otherETag}],[{number:2,size:5,etag}],[{number:1,size:6,etag}]]) {
    assert.equal(confirmsSavedMultipartPart(stored,receipt),false);
  }
  for (const incorrect of [{status:204},{etag:null},{etag:'W/'+etag}]) {
    assert.equal(confirmsSavedMultipartPart(same,{...receipt,...incorrect}),false);
  }
});

test('native pages cannot announce more after a short page or end on an empty continuation', () => {
  const empty = parseMultipartPartPage(page({parts:[]}),scope);
  assert.deepEqual(empty.parts,[]);
  const second = {...scope,marker:1000};
  assert.throws(()=>parseMultipartPartPage(page({scope:second,parts:[]}),second),/Invalid/);
  for (const damaged of [page({more:'true',extra:[element('NextPartNumberMarker','1')]}),
    page({extra:[element('NextPartNumberMarker','')]})]) {
    assert.throws(()=>parseMultipartPartPage(damaged,scope),/Invalid/);
  }
});

test('a stopped or incomplete part scan cannot become a receipt', async () => {
  const receipt = {number:1,size:1,etag,status:200};
  const full = Array.from({length:1000},(_,i)=>[String(i+1),'1',etag]);
  const first = parseMultipartPartPage(page({parts:full,more:'true',
    extra:[element('NextPartNumberMarker','1000')]}),scope);
  const incomplete = await listMultipartParts(async()=>first,()=>false);
  assert.equal(confirmsSavedMultipartPart(incomplete,receipt),false);
  let checked = false;
  await assert.rejects(listMultipartParts(async marker=>{
    if(marker===0)return first;
    checked=true;
    return parseMultipartPartPage(page(),{...scope,marker});
  },()=>true),/out-of-scope/);
  assert.ok(checked);
});
