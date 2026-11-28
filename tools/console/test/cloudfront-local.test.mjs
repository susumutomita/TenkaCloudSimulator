import assert from 'node:assert/strict';
import test from 'node:test';
import {cloudFrontAvailable, cloudFrontClient, cloudFrontTasks, confirmedRefusal, invalidationXML, localObjectKey} from '../src/cloudfront-local.mjs';
import {serviceAvailable, serviceFromHash} from '../src/console-navigation.mjs';

const ns = 'http://cloudfront.amazonaws.com/doc/2020-05-31/';
const id = 'D' + '1'.repeat(24), second = 'D' + '2'.repeat(24), receiptID = 'I' + '3'.repeat(24);
const etag = '4'.repeat(32), base = '/2020-05-31/distribution';
const cap = {services:{cloudfront:{distributionConsole:true, protocol:'REST/XML 2020-05-31',
  delivery:'/_simulator/cloudfront/distributions/', distributionOrigin:'one existing local S3 bucket',
  externalOriginFetch:false, dataPlane:true,
  operations:['ListDistributions','GetDistribution','CreateInvalidation','GetInvalidation']}}};
const el = (localName, contents) => ({localName,namespaceURI:ns,children:Array.isArray(contents)?contents:[],textContent:typeof contents==='string'?contents:''});
const doc = root => ({documentElement:root,getElementsByTagName:()=>[]});
const config = () => [el('Origins',[el('Quantity','1'),el('Items',[el('Origin',[el('Id','saved'),el('DomainName','local-origin.s3.amazonaws.com')])])]),
  el('DefaultCacheBehavior',[el('TargetOriginId','saved'),el('MinTTL','0'),el('DefaultTTL','60'),el('MaxTTL','86400')]),el('Enabled','true'),el('Comment','saved & inert')];
const common = (identifier=id) => [el('Id',identifier),el('ARN','arn:aws:cloudfront::000000000000:distribution/'+identifier),
  el('DomainName',identifier+'.cloudfront.invalid'),el('Status','Deployed'),el('LastModifiedTime','2026-11-28T00:00:00.123456789Z')];
const detail = () => doc(el('Distribution',[...common(),el('DistributionConfig',config())]));
const list = (rows=[id],marker='',next='') => doc(el('DistributionList',[
  el('Marker',marker),el('MaxItems','25'),el('IsTruncated',String(!!next)),el('Quantity',String(rows.length)),
  el('Items',rows.map(value=>el('DistributionSummary',[...common(value),...config()]))),...(next?[el('NextMarker',next)]:[]),
]));
const receipt = () => doc(el('Invalidation',[el('Id',receiptID),el('Status','Completed'),el('CreateTime','2026-11-28T00:00:00Z'),
  el('InvalidationBatch',[el('CallerReference','console-original'),el('Paths',[el('Quantity','1'),el('Items',[el('Path','/*')])])])]));
const xml = () => new Response('<original/>',{status:200,headers:{'Content-Type':'application/xml','ETag':etag}});
const signal = () => new AbortController().signal;

function client(document, requests=[]) {
  return cloudFrontClient(async (path,options)=>{requests.push({path,options});return xml();}, text=>{assert.equal(text,'<original/>');return document;});
}

test('CloudFront navigation requires the exact advertised bounded content capability and no handoff',()=>{
  assert.equal(serviceFromHash('#/cloudfront'),'cloudfront');
  assert.equal(cloudFrontAvailable(cap),true);assert.equal(serviceAvailable('cloudfront',cap),true);
  for (const changed of [{distributionConsole:false},{externalOriginFetch:true},{delivery:'https://outside.invalid/'},{operations:['GetDistribution']}]) {
    assert.equal(cloudFrontAvailable({services:{cloudfront:{...cap.services.cloudfront,...changed}}}),false);
  }
  assert.equal(cloudFrontAvailable({...cap,consoleScope:{instanceId:'i-local'}}),false);
  assert.equal(serviceAvailable('cloudfront',{}),false);
});

test('native list preserves original marker, fixed page size, native routing and active signal',async()=>{
  const requests=[], pending=signal();
  const result=await client(list([second],id),requests).list(id,pending);
  assert.equal(result.rows[0].id,second);assert.equal(result.next,'');
  assert.equal(requests[0].path,base+'?MaxItems=25&Marker='+id);
  assert.deepEqual(requests[0].options,{method:'GET',redirect:'error',credentials:'same-origin',signal:pending,
    headers:{'X-Simulator-Service':'cloudfront','X-Simulator-Region':'us-east-1'}});
  assert.deepEqual(await client(list([])).list('',signal()),{rows:[],next:''});
  assert.equal((await client(list([id],'',id)).list('',signal())).next,id);
});

test('native XML refusal includes repeated IDs, inconsistent marker and malformed TTL/config',async()=>{
  for (const malformed of [list([id,id]),list([id],second),list([id],'',second)]) {
    await assert.rejects(client(malformed).list(malformed.documentElement.children[0].textContent,signal()),/Invalid local/);
  }
  const malformed=detail();malformed.documentElement.children.find(x=>x.localName==='DistributionConfig').children
    .find(x=>x.localName==='DefaultCacheBehavior').children.find(x=>x.localName==='DefaultTTL').textContent='90000';
  await assert.rejects(client(malformed).detail(id,signal()),/Invalid local/);
});

test('detail displays exact saved ETag, origin, config and nanosecond timestamp',async()=>{
  const requests=[], saved=await client(detail(),requests).detail(id,signal());
  assert.equal(requests[0].path,base+'/'+id);assert.equal(saved.etag,etag);
  assert.equal(saved.origin,'local-origin.s3.amazonaws.com');assert.equal(saved.ttl,60);
  assert.equal(saved.comment,'saved & inert');assert.equal(saved.modified,'2026-11-28T00:00:00.123456789Z');
  await assert.rejects(client(detail()).detail(second,signal()),/Invalid local/);
});

test('invalidation uses the original caller and paths for deliberate idempotent replay',async()=>{
  const requests=[];
  const api=cloudFrontClient(async(path,options)=>{requests.push({path,options});return new Response('<original/>',{status:201,headers:{'Content-Type':'application/xml'}});},()=>receipt());
  const original=await api.invalidate(id,'console-original',signal());
  const repeated=await api.invalidate(id,'console-original',signal());
  assert.deepEqual(original,repeated);assert.equal(original.id,receiptID);
  assert.equal(requests[0].path,base+'/'+id+'/invalidation');
  assert.equal(requests[0].options.method,'POST');assert.equal(requests[0].options.body,invalidationXML('console-original'));
  assert.equal(requests[0].options.body,requests[1].options.body);
  assert.equal(requests[0].options.headers['Content-Type'],'application/xml');
  assert.throws(()=>invalidationXML('different & unsafe'),/Invalid invalidation/);
  const read=[];await client(receipt(),read).invalidation(original,signal());
  assert.equal(read[0].path,base+'/'+id+'/invalidation/'+receiptID);
  await assert.rejects(client(receipt()).invalidation({...original,id:''},signal()),/Invalid local/);
});

const protection = {'Content-Disposition':'attachment','X-Content-Type-Options':'nosniff','Cache-Control':'no-store',
  'Content-Security-Policy':"sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
  'Content-Type':'text/html','ETag':'saved-etag','X-Cache':'Hit from local CloudFront simulator','Age':'12'};
test('local content is a same-origin GET with unchanged inert preview and complete original bytes',async()=>{
  const requests=[], payload=new TextEncoder().encode('<img src="https://outside.invalid/"> & saved');
  const api=cloudFrontClient(async(path,options)=>{requests.push({path,options});return new Response(payload,{headers:protection});});
  const pending=signal(),result=await api.object(id,'assets/message.txt',pending);
  assert.equal(requests[0].path,'/_simulator/cloudfront/distributions/'+id+'/objects/assets/message.txt');
  assert.deepEqual(requests[0].options,{method:'GET',redirect:'error',credentials:'same-origin',signal:pending});
  assert.deepEqual(result.body,payload);assert.equal(result.preview,new TextDecoder().decode(payload));assert.equal(result.type,'text/html');
  assert.equal(result.cache,'Hit from local CloudFront simulator');assert.equal(result.age,'12');
});

test('unsafe transfer headers, oversized content and noncanonical keys refuse without broadening the request',async()=>{
  for (const name of ['Content-Disposition','X-Content-Type-Options','Content-Security-Policy','Cache-Control']) {
    const headers={...protection};delete headers[name];
    await assert.rejects(cloudFrontClient(async()=>new Response('benign',{headers})).object(id,'asset.txt',signal()),/Invalid local/);
  }
  await assert.rejects(cloudFrontClient(async()=>new Response(new Uint8Array((1<<20)+1),{headers:protection})).object(id,'asset.txt',signal()),/exceeds/);
  let count=0;
  const api=cloudFrontClient(async()=>{count++;return new Response('unused');});
  for (const key of ['','../asset','a/../b','a//b','/absolute','a?x=1','a%2fb','https://outside.invalid','a#fragment']) {
    assert.equal(localObjectKey(key),false);await assert.rejects(api.object(id,key,signal()),/supported local/);
  }
  assert.equal(count,0);
});

test('late reads cannot publish or clear a newer task; active writes cannot be duplicated',()=>{
  const tasks=cloudFrontTasks(),old=tasks.start('read'),fresh=tasks.start('read');
  assert.equal(old.signal.aborted,true);assert.equal(tasks.current(old),false);assert.equal(tasks.finish(old),false);assert.equal(tasks.current(fresh),true);
  const write=tasks.start('write');assert.equal(fresh.signal.aborted,true);assert.equal(tasks.start('write'),null);assert.equal(tasks.start('read'),null);
  assert.equal(tasks.cancelRead(),false);assert.equal(tasks.current(write),true);
  tasks.abort();assert.equal(write.signal.aborted,true);assert.equal(tasks.current(write),false);
  const newer=tasks.start('read');assert.equal(tasks.finish(write),false);assert.equal(tasks.current(newer),true);
  assert.equal(tasks.cancelRead(),true);assert.equal(tasks.current(newer),false);
});

test('only explicit native refusal is known; network, parsing and storage failures retain uncertain outcome',()=>{
  for (const name of ['AccessDenied','InvalidArgument','NoSuchDistribution','InvalidationBatchAlreadyExists']) assert.equal(confirmedRefusal(Error(name+': refused')),true);
  for (const value of ['InternalError: unavailable','Failed to fetch','Invalid local CloudFront response.','AbortError']) assert.equal(confirmedRefusal(Error(value)),false);
});
