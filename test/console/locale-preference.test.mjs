import test from 'node:test';
import assert from 'node:assert/strict';
import {preferenceKey, localeChangeEvent, readLocalePreference, writeLocalePreference,
  publishLocalePreference, subscribeLocalePreference} from '../../tools/console/src/locale-preference.mjs';

class LocalEvent extends Event {
  constructor(type, {detail}) { super(type); this.detail=detail; }
}
function fixture(initial='en') {
  const target=new EventTarget(); target.CustomEvent=LocalEvent;
  const data=new Map(initial===null?[]:[[preferenceKey,initial]]), writes=[];
  const storage={getItem:key=>data.get(key)??null,setItem:(key,value)=>{writes.push([key,value]);data.set(key,value);}};
  return {target,data,storage,writes,getStorage:()=>storage};
}
function storageEvent(f, key=preferenceKey, newValue=f.data.get(preferenceKey)??null, area=f.storage) {
  const event=new Event('storage');
  Object.assign(event,{key,newValue,storageArea:area});
  f.target.dispatchEvent(event);
}
function localEvent(f,detail) { f.target.dispatchEvent(new LocalEvent(localeChangeEvent,{detail})); }

test('initial preference preserves Japanese and otherwise defaults to English',()=>{
  for(const [value,expected] of [['ja','ja'],['en','en'],[null,'en'],['fr','en'],['JA','en'],['','en']]) {
    const f=fixture(value); assert.equal(readLocalePreference(f.getStorage),expected);
    assert.deepEqual(f.writes,[]);
  }
});
test('blocked storage getters and reads default to English without propagating errors',()=>{
  assert.equal(readLocalePreference(()=>{throw new Error('unavailable');}),'en');
  assert.equal(readLocalePreference(()=>({getItem(){throw new Error('unavailable');}})),'en');
});
test('persistence writes only the existing locale key and rejects unsupported values',()=>{
  const f=fixture();f.data.set('native-draft','unchanged');
  for(const locale of ['ja','en']) assert.equal(writeLocalePreference(f.getStorage,locale),true);
  for(const locale of ['fr','browser-default',null,undefined,{},'']) assert.equal(writeLocalePreference(f.getStorage,locale),false);
  assert.deepEqual(f.writes,[[preferenceKey,'ja'],[preferenceKey,'en']]);
  assert.equal(f.data.get('native-draft'),'unchanged');
});
test('failed writes return false and do not require any fallback storage',()=>{
  assert.equal(writeLocalePreference(()=>{throw new Error('unavailable');},'ja'),false);
  assert.equal(writeLocalePreference(()=>({setItem(){throw new Error('unavailable');}}),'ja'),false);
});
test('explicit UI publishing persists first and emits exactly one locale field',()=>{
  const f=fixture(),events=[];
  f.target.addEventListener(localeChangeEvent,event=>events.push({detail:event.detail,stored:f.data.get(preferenceKey)}));
  assert.equal(publishLocalePreference(f.target,f.getStorage,'ja'),true);
  assert.deepEqual(events,[{detail:{locale:'ja'},stored:'ja'}]);
  assert.deepEqual(f.writes,[[preferenceKey,'ja']]);
  assert.equal(publishLocalePreference(f.target,f.getStorage,'fr'),false);
  assert.equal(events.length,1);assert.equal(f.writes.length,1);
});
test('same-document observers receive an explicit choice without echoing persistence or events',()=>{
  const f=fixture(),received=[];let broadcasts=0;
  f.target.addEventListener(localeChangeEvent,()=>broadcasts++);
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  publishLocalePreference(f.target,f.getStorage,'ja');
  assert.deepEqual(received,['ja']);assert.equal(broadcasts,1);
  assert.deepEqual(f.writes,[[preferenceKey,'ja']]);stop();
});
test('same-document choices still update presentation when storage is blocked',()=>{
  const f=fixture(),received=[];const blocked=()=>{throw new Error('unavailable');};
  const stop=subscribeLocalePreference(f.target,blocked,locale=>received.push(locale));
  assert.equal(publishLocalePreference(f.target,blocked,'ja'),true);
  assert.deepEqual(received,['ja']);assert.deepEqual(f.writes,[]);stop();
});
test('matching localStorage changes synchronize both locales without writing back',()=>{
  const f=fixture(),received=[];let broadcasts=0;
  f.target.addEventListener(localeChangeEvent,()=>broadcasts++);
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  for(const locale of ['ja','en']) {f.data.set(preferenceKey,locale);storageEvent(f);}
  assert.deepEqual(received,['ja','en']);assert.deepEqual(f.writes,[]);assert.equal(broadcasts,0);stop();
});
test('removing the preference or clearing localStorage selects the English default',()=>{
  const f=fixture('ja'),received=[];
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  f.data.delete(preferenceKey);storageEvent(f);
  f.data.set(preferenceKey,'ja');storageEvent(f);
  f.data.clear();storageEvent(f,null,null);
  assert.deepEqual(received,['en','ja','en']);assert.deepEqual(f.writes,[]);stop();
});
test('storage observers ignore other keys, sessionStorage and malformed values',()=>{
  const f=fixture('ja'),received=[];
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  storageEvent(f,'other-key','en');storageEvent(f,preferenceKey,'en',{});
  storageEvent(f,preferenceKey,'fr');storageEvent(f,null,'ja');
  f.data.set(preferenceKey,'invalid');storageEvent(f,preferenceKey,'ja');
  assert.deepEqual(received,[]);assert.deepEqual(f.writes,[]);stop();
});
test('queued stale storage events cannot replace a newer stored choice',()=>{
  const f=fixture('ja'),received=[];
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  storageEvent(f,preferenceKey,'en');storageEvent(f,preferenceKey,null);
  assert.deepEqual(received,['ja','ja']);assert.equal(f.data.get(preferenceKey),'ja');stop();
});
test('storage access failure leaves the current presentation untouched',()=>{
  const f=fixture(),received=[];
  let stop=subscribeLocalePreference(f.target,()=>{throw new Error('unavailable');},locale=>received.push(locale));
  storageEvent(f);stop();
  const broken={getItem(){throw new Error('unavailable');}};
  stop=subscribeLocalePreference(f.target,()=>broken,locale=>received.push(locale));
  storageEvent(f,preferenceKey,'ja',broken);
  assert.deepEqual(received,[]);stop();
});
test('local observers reject extra fields, inherited values and unimplemented locales',()=>{
  const f=fixture(),received=[];
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  for(const detail of [null,undefined,'ja',{}, {locale:'fr'}, {locale:'ja',native:'unchanged'},
    Object.assign(Object.create({locale:'ja'}),{other:'ignored'})]) localEvent(f,detail);
  assert.deepEqual(received,[]);assert.deepEqual(f.writes,[]);stop();
});
for(const [name,malformed] of [
  ['array',()=>Object.assign([],{locale:'ja'})],
  ['nonenumerable extra',()=>Object.defineProperty({locale:'ja'},'extra',{value:'ignored'})],
  ['symbol extra',()=>({locale:'ja',[Symbol('extra')]:'ignored'})],
  ['class instance',()=>new (class { constructor() {this.locale='ja';} })()],
]) test(`local observers reject ${name} payloads while ordinary locale records still work`,()=>{
  const f=fixture(),received=[];
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  localEvent(f,malformed());
  assert.deepEqual(received,[]);
  localEvent(f,{locale:'ja'});localEvent(f,{locale:'en'});
  assert.deepEqual(received,['ja','en']);assert.deepEqual(f.writes,[]);stop();
});
test('unsubscribe removes both listeners without changing storage or presentation',()=>{
  const f=fixture(),received=[];
  const stop=subscribeLocalePreference(f.target,f.getStorage,locale=>received.push(locale));
  stop();stop();f.data.set(preferenceKey,'ja');storageEvent(f);localEvent(f,{locale:'ja'});
  assert.deepEqual(received,[]);assert.deepEqual(f.writes,[]);
});
