import assert from 'node:assert/strict';
import test from 'node:test';
import {focusPTRResult, initialPTRDraft, ptrRecord, route53Client, route53ConsoleAvailable, updatePTRDraft} from '../src/route53-console.mjs';
import {hashForService, providerForService, serviceAvailable, serviceFromHash, servicesForProvider} from '../src/console-navigation.mjs';

const ns = 'https://route53.amazonaws.com/doc/2013-04-01/';
const cap = {services:{route53:{console:true,publicZonesOnly:true,protocol:'REST/XML 2013-04-01',
  operations:['ListHostedZones','GetHostedZone','ListResourceRecordSets','ChangeResourceRecordSets']}}};
const zone = {id:'Z11',name:'2.0.192.in-addr.arpa.',count:2};
const fields = {name:'7.2.0.192.IN-ADDR.ARPA',ttl:'60',target:'Web.Example.Test'};
const canonical = {name:'7.2.0.192.in-addr.arpa.',type:'PTR',ttl:60,values:['web.example.test.']};

function el(localName, contents, namespaceURI=ns) {
  return {localName,namespaceURI,children:Array.isArray(contents)?contents:[],textContent:typeof contents==='string'?contents:''};
}
const doc = (...a) => ({documentElement:el(...a)});
const zoneElement = (overrides={}) => {
  const z = {...zone,...overrides};
  return el('HostedZone',[el('Id','/hostedzone/'+z.id),el('Name',z.name),
    el('ResourceRecordSetCount',String(z.count)),el('Config',[el('PrivateZone',z.private?'true':'false')])]);
};
const rr = (record=canonical) => el('ResourceRecordSet', [
  el('Name',record.name),el('Type',record.type),el('TTL',String(record.ttl)),
  el('ResourceRecords',record.values.map(v=>el('ResourceRecord',[el('Value',v)]))),
]);
const list = (rows=[],next) => doc('ListResourceRecordSetsResponse',[
  el('ResourceRecordSets',rows), el('IsTruncated',next?'true':'false'),
  ...(next?[el('NextRecordName',next.name),el('NextRecordType',next.type)]:[]),
]);
const ack = (status='INSYNC', id='/change/C42') => doc('ChangeResourceRecordSetsResponse',[
  el('ChangeInfo',[el('Id',id),el('Status',status),el('SubmittedAt','2026-11-25T18:00:00Z')]),
]);
const fixture = (outcomes) => {
  const calls = [];
  const api = route53Client(async (path,opts) => {
    calls.push({path,...opts});
    const response = outcomes[calls.length-1];
    assert.ok(response,'Unexpected request: '+path);
    if (response instanceof Error) throw response;
    return new Response(String(calls.length-1),{status:200,headers:{'Content-Type':'text/xml; charset=utf-8'}});
  },text=>outcomes[Number(text)]);
  return {api,calls};
};
const signal = () => new AbortController().signal;

test('admission requires native public-zone discovery and ordinary required authentication',()=>{
  assert.equal(route53ConsoleAvailable(cap,true),true);
  assert.equal(route53ConsoleAvailable(cap,false),false);
  assert.equal(route53ConsoleAvailable({...cap,consoleScope:{parameterName:'/team/value'}},true),false);
  assert.equal(route53ConsoleAvailable({services:{route53:true}},true),false);
  const scope = cap.services.route53;
  for (const changes of [{console:false},{publicZonesOnly:false},{protocol:'JSON'},{operations:scope.operations.slice(1)}]) {
    assert.equal(route53ConsoleAvailable({services:{route53:{...scope,...changes}}},true),false);
  }
});

test('AWS navigation recognizes only the authenticated native Route 53 console, not catalog or GCP DNS',()=>{
  assert.equal(providerForService('route53'),'aws');
  assert.equal(serviceFromHash('#/route53'),'route53');
  assert.equal(hashForService('route53'),'/route53');
  assert.equal(serviceAvailable('route53',cap),true);
  assert.equal(serviceAvailable('route53',{services:{route53:true}}),false);
  assert.equal(serviceAvailable('route53',{services:{route53:{...cap.services.route53,console:false}}}),false);
  assert.equal(serviceAvailable('route53',{...cap,consoleScope:{instanceId:'i-bound'}}),false);
  assert.deepEqual(servicesForProvider('aws',cap,'ptr').map(x=>x.id),['route53']);
  assert.deepEqual(servicesForProvider('gcp',cap,'ptr'),[]);
});

test('PTR validation mirrors bounded simple native names and TTL, canonicalizing ASCII only',()=>{
  assert.deepEqual(ptrRecord(zone.name,fields),canonical);
  assert.equal(ptrRecord(zone.name,{...fields,name:zone.name,ttl:'0'}).ttl,0);
  assert.equal(ptrRecord(zone.name,{...fields,ttl:'2147483647'}).ttl,2147483647);
  for(const patch of [{name:'foo2.0.192.in-addr.arpa.'},{name:'elsewhere.example.'},{name:'*.2.0.192.in-addr.arpa.'},
    {target:'web..test.'},{target:'web-.test.'},{target:'WKB.test'},{target:'bad\n.test.'},
    {target:'a'.repeat(64)+'.test.'},{target:'<test>.invalid'},{ttl:''},{ttl:'1e2'},{ttl:'-1'},{ttl:'2147483648'}]) {
    assert.equal(ptrRecord(zone.name,{...fields,...patch}),null,JSON.stringify(patch));
  }
});

test('public zones use their selected stable ID; record continuations use exact native start keys',async()=>{
  const rows = Array.from({length:25},(_,n)=>rr({...canonical,name:'r'+n+'.'+zone.name}));
  const next = {name:'zzz.'+zone.name,type:'PTR'};
  const {api,calls} = fixture([
    doc('ListHostedZonesResponse',[el('HostedZones',[zoneElement(),zoneElement({id:'Z12'})]),el('IsTruncated','false')]),
    doc('GetHostedZoneResponse',[zoneElement()]), list(rows,next),list([rr({...canonical,...next})]),
  ]);
  assert.deepEqual((await api.zones(signal())).map(z=>z.id),['Z11','Z12']);
  assert.deepEqual(await api.zone(zone.id,signal()),zone);
  const first = await api.records(zone.id,signal());
  assert.equal(first.rows.length,25);
  assert.deepEqual(first.next,next);
  assert.equal((await api.records(zone.id,signal(),first.next)).rows[0].name,next.name);
  assert.deepEqual(calls.map(c=>c.path),[
    '/2013-04-01/hostedzone?maxitems=100','/2013-04-01/hostedzone/Z11',
    '/2013-04-01/hostedzone/Z11/rrset?maxitems=25',
    '/2013-04-01/hostedzone/Z11/rrset?maxitems=25&name=zzz.2.0.192.in-addr.arpa.&type=PTR',
  ]);
  for (const c of calls) {
    assert.equal(c.method,'GET'); assert.equal(c.credentials,'same-origin'); assert.equal(c.redirect,'error');
    assert.deepEqual(c.headers,{'X-Simulator-Service':'route53','X-Simulator-Region':'us-east-1'});
  }
});

test('one CREATE PTR is accepted only after an exact INSYNC receipt and a fresh selected-zone readback',async()=>{
  const {api,calls} = fixture([ack(),list([rr()])]);
  const out = await api.createPTR(zone,fields,signal());
  assert.equal(out.changeID,'/change/C42');
  assert.deepEqual(out.page.rows,[canonical]);
  assert.equal(calls[0].path,'/2013-04-01/hostedzone/Z11/rrset');
  assert.equal(calls[0].method,'POST');
  assert.equal(calls[0].headers['Content-Type'],'application/xml');
  assert.equal(calls[0].body,'<ChangeResourceRecordSetsRequest xmlns="'+ns+'"><ChangeBatch><Changes><Change>'+
    '<Action>CREATE</Action><ResourceRecordSet><Name>7.2.0.192.in-addr.arpa.</Name><Type>PTR</Type><TTL>60</TTL>'+
    '<ResourceRecords><ResourceRecord><Value>web.example.test.</Value></ResourceRecord></ResourceRecords>'+
    '</ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>');
  assert.equal(calls[1].path,calls[0].path+'?maxitems=25&name=7.2.0.192.in-addr.arpa.&type=PTR');
  assert.equal(calls[1].method,'GET');
});

test('bad IDs and form data never send a request; denied or unconfirmed writes are not retried',async()=>{
  const {api,calls} = fixture([]);
  await assert.rejects(api.createPTR(zone,{...fields,ttl:'-1'},signal()),/valid.*TTL/);
  await assert.rejects(api.zone('Z1/other',signal()),/ID/);
  assert.equal(calls.length,0);
  for (const bad of [new Error('AccessDenied: expected fixture refusal'),ack('PENDING'),ack('INSYNC','other/C42')]) {
    const f = fixture([bad]);
    await assert.rejects(f.api.createPTR(zone,fields,signal()),/AccessDenied|Invalid local/);
    assert.equal(f.calls.length,1);
  }
  for (const stale of [list([]),list([rr({...canonical,ttl:3600})]),list([rr({...canonical,values:['elsewhere.test.']})])]) {
    const f = fixture([ack(),stale]);
    await assert.rejects(f.api.createPTR(zone,fields,signal()),/could not be confirmed/);
    assert.equal(f.calls.length,2);
  }
});

test('scope switches abort in-flight reads; namespaced XML, exact selection and public zone are checked',async()=>{
  const ctl = new AbortController();
  const f = fixture([doc('GetHostedZoneResponse',[zoneElement({id:'Z12'})]),doc('GetHostedZoneResponse',[zoneElement({private:true})])]);
  await assert.rejects(f.api.zone('Z11',ctl.signal),/Invalid local/);
  await assert.rejects(f.api.zone('Z11',ctl.signal),/Invalid local/);
  ctl.abort();
  await assert.rejects(f.api.zone('Z11',ctl.signal),/abort/i);
  assert.equal(f.calls.length,2);
  const wrongNamespace = fixture([doc('GetHostedZoneResponse',[zoneElement()],'invalid')]);
  await assert.rejects(wrongNamespace.api.zone('Z11',signal()),/Invalid local/);
});

test('refused native-shaped write hands focused Submit to Refresh and keeps retry locked until a fresh same-zone read',async()=>{
  const f = fixture([new Error('AccessDenied: fixture policy refusal'), doc('GetHostedZoneResponse',[zoneElement()]),
    new Error('AccessDenied: fixture list refusal'), doc('GetHostedZoneResponse',[zoneElement()]), list([])]);
  let draft = initialPTRDraft;
  for (const [field,value] of Object.entries(fields)) draft = updatePTRDraft(draft,{type:'edit',field,value});
  const docState = {activeElement:null,hasFocus:()=>true};
  const submit = {ownerDocument:docState,isConnected:true};
  const refresh = {ownerDocument:docState,isConnected:true};
  docState.activeElement = submit;
  await assert.rejects(f.api.createPTR(zone,draft.fields,signal()),/AccessDenied/);
  draft = updatePTRDraft(draft,{type:'unconfirmed'});
  focusPTRResult(submit,()=>{docState.activeElement = refresh;});
  assert.equal(docState.activeElement,refresh);
  assert.equal(draft.mustRefresh,true);
  assert.deepEqual(draft.fields,fields);
  await f.api.zone(zone.id,signal());
  await assert.rejects(f.api.records(zone.id,signal()),/AccessDenied/);
  assert.equal(draft.mustRefresh,true);
  assert.deepEqual(draft.fields,fields);
  await f.api.zone(zone.id,signal());
  await f.api.records(zone.id,signal());
  draft = updatePTRDraft(draft,{type:'fresh'});
  assert.equal(draft.mustRefresh,false);
  assert.deepEqual(draft.fields,fields);
  assert.deepEqual(f.calls.map(c=>c.method),['POST','GET','GET','GET','GET']);
});

test('a delayed verified readback clears the draft but never steals focus after the user moves',async()=>{
  let finish;
  const held = new Promise(resolve=>{finish=resolve;});
  const actual = fixture([ack(),list([rr()])]);
  const document = {activeElement:null,hasFocus:()=>true};
  const submit = {ownerDocument:document,isConnected:true}, elsewhere = {ownerDocument:document,isConnected:true};
  document.activeElement = submit;
  const pending = actual.api.createPTR(zone,fields,signal()).then(async result=>{await held;return result;});
  document.activeElement = elsewhere;
  finish();
  assert.equal((await pending).changeID,'/change/C42');
  focusPTRResult(submit,()=>assert.fail('must not steal focus from another control'));
  document.activeElement = submit;
  focusPTRResult(submit,()=>{document.activeElement = elsewhere;});
  assert.equal(document.activeElement,elsewhere);
  document.activeElement = submit;
  document.hasFocus = ()=>false;
  focusPTRResult(submit,()=>assert.fail('must not focus an inactive document'));
  document.hasFocus=()=>true;
  document.activeElement = {tagName:'BODY'};
  submit.isConnected=false;
  focusPTRResult(submit,()=>assert.fail('body focus is not sufficient ownership'));
  assert.equal(updatePTRDraft({fields,mustRefresh:false},{type:'verified'}),initialPTRDraft);
});
