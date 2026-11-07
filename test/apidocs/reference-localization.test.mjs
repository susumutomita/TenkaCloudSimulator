import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import vm from 'node:vm';
import {pinnedEnglish,literalObject,digest,ASSET} from './scalar-locale-data.mjs';
const files=name=>readFileSync(new URL('../../internal/server/apidocs/'+name,import.meta.url),'utf8');
const english=pinnedEnglish(readFileSync(new URL('../../internal/server/apidocs/scalar.js',import.meta.url)));
const context=vm.createContext({});vm.runInContext(files('scalar-locale-ja.js'),context);const scalarLocale=context.SimulatorScalarLocale;
test('all535 actual pinned English keys have Japanese overrides with exact placeholder multisets',()=>{
 assert.equal(Object.keys(english).length,535);assert.deepEqual(Object.keys(scalarLocale.values).sort(),Object.keys(english).sort());
 assert.equal(scalarLocale.assetSHA256,ASSET);
 for(const [key,value]of Object.entries(english))assert.deepEqual((scalarLocale.values[key].match(/\{[A-Za-z][A-Za-z0-9_]*\}/g)||[]).sort(),(value.match(/\{[A-Za-z][A-Za-z0-9_]*\}/g)||[]).sort(),key);
 assert.equal(scalarLocale.translations('ja').apiClient.addressBar.send,'送信');assert.equal(scalarLocale.translations('ja').operation.testRequest,'リクエストを試す');
 assert.equal(scalarLocale.translations('ja').apiClient.addressBar.sendRequest,'{url} へ {method} リクエストを送信');
 assert.deepEqual(Object.keys(scalarLocale.translations('en')),[]);
});
test('missing unknown empty and changed/duplicate placeholder overrides fail instead of English fallback',()=>{
 const key='apiClient.addressBar.sendRequest';
 for(const edit of [v=>delete v[key],v=>v['unknown.test']='不明',v=>v[key]='',v=>v[key]='{url} へ送信',v=>v[key]='{url} {method} {method}']){
  const values={...scalarLocale.values};edit(values);assert.throws(()=>scalarLocale.validate(values));
 }
 assert.throws(()=>scalarLocale.translations('invalid'));
});
test('English data reader rejects executable syntax and unpinned bundle bytes',()=>{
 assert.throws(()=>literalObject('{key:evil()}'));assert.throws(()=>literalObject('{...spread}'));assert.throws(()=>literalObject('{key:"a",key:"b"}'));assert.throws(()=>literalObject('{key:unknown.alias}'));assert.throws(()=>pinnedEnglish(Buffer.from('unreviewed')));
});
test('full config survives JA/EN replacements with identical guarded customFetch and safe settings',()=>{
 vm.runInContext(files('reference-config.js'),context);const build=context.SimulatorReferenceConfig.create,fetch=()=>{};
 const initial=build(null,fetch,'en');initial.customLocalOption={retained:true};
 const ja=build(initial,fetch,'ja'),en=build(ja,fetch,'en');
 for(const cfg of [initial,ja,en]){assert.equal(cfg.customFetch,fetch);assert.equal(cfg.url,'/_simulator/docs/openapi.json');assert.equal(cfg.telemetry,false);assert.equal(cfg.persistAuth,false);assert.equal(cfg.proxyUrl,'');assert.equal(cfg.agent.disabled,true);assert.equal(cfg.withDefaultFonts,false);assert.equal(cfg.showDeveloperTools,'never');}
 assert.equal(ja.customLocalOption.retained,true);assert.equal(ja.localization.translations.apiClient.requestTable.key,'キー');assert.deepEqual(Object.keys(en.localization.translations),[]);
 assert.equal(initial.localization.locale,'en');assert.equal(ja.localization.locale,'ja');
});
test('init keeps one instance and performs full safe config updates; cross-origin rejected before fetch',async()=>{
 const subscribers=[],calls=[],updates=[],meta={content:''},message={textContent:''};let cfg;
 const env={URL,Promise,location:{origin:'http://127.0.0.1:4566',href:'http://127.0.0.1:4566/_simulator/docs/'},fetch:()=>{throw Error('native fetch should not run');},SimulatorScalarLocale:scalarLocale,
 SimulatorReferenceLocale:{get:()=> 'en',subscribe:f=>{subscribers.push(f);f('en');}},SimulatorS3ResponseGuide:{wrapFetch:f=>f},Scalar:{createApiReference:(selector,configuration)=>{calls.push(selector);cfg=configuration;return{getConfiguration:()=>cfg,updateConfiguration:c=>{cfg=c;updates.push(c);}};}},document:{title:'',documentElement:{lang:'en'},getElementById:()=>message,querySelector:()=>meta}};
 vm.createContext(env);vm.runInContext(files('reference-config.js'),env);vm.runInContext(files('init.js'),env);
 subscribers[0]('ja');subscribers[0]('en');assert.equal(calls.length,1);assert.equal(updates.length,2);assert.equal(updates[0].customFetch,updates[1].customFetch);assert.equal(updates[0].localization.translations.apiClient.addressBar.send,'送信');assert.equal(updates[1].telemetry,false);
 await assert.rejects(cfg.customFetch('https://outside.invalid/',{}),/Only this simulator origin/);
});
test('S3 route parser body bounds and native fetch wrapper remain byte-exact base',()=>{
 const old=execFileSync('git',['show','3a9b0ce9b7afed3f6b3253f4df9e7c59c3764e9a:internal/server/apidocs/response-guide.js'],{encoding:'utf8'}),current=files('response-guide.js');
 const section=(s,start,end)=>s.slice(s.indexOf(start),s.indexOf(end,s.indexOf(start)));
 assert.equal(section(current,'  function operation(','  async function boundedText('),section(old,'  function operation(','  async function boundedText('));
 assert.equal(section(current,'  async function boundedText(','  function parse('),section(old,'  async function boundedText(','  function parse('));
 assert.equal(section(current,'  function parse(','  // Presentation-only adapter.'),section(old,'  function parse(','  function render('));
 assert.equal(section(current,'    wrapFetch(nativeFetch)','\n  };'),section(old,'    wrapFetch(nativeFetch)','\n  };'));
});
test('Japanese S3 presentation preserves actual field names values item titles path and status',()=>{
 class Element{constructor(tag){this.tag=tag;this.children=[];this.attrs={};this.textContent='';this.open=false;}append(...nodes){this.children.push(...nodes);}replaceChildren(){this.children=[];}setAttribute(k,v){this.attrs[k]=v;}addEventListener(){}focus(){}close(){this.open=false;}}
 const nodes=new Map();const document={getElementById:id=>{if(!nodes.has(id))nodes.set(id,new Element('div'));return nodes.get(id);},createElement:tag=>new Element(tag),addEventListener(){}};
 const env={document,SimulatorReferenceLocale:{get:()=> 'ja',subscribe:f=>f('ja')}};vm.createContext(env);
 const source=files('response-guide.js').replace('  globalThis.SimulatorS3ResponseGuide = {','  globalThis.testRender=render;\n  globalThis.SimulatorS3ResponseGuide = {');vm.runInContext(source,env);
 env.testRender({operation:'List buckets',summary:'1 bucket in this response',status:200,path:'GET /literal-key',fields:[{name:'Name',value:'Close',meaning:'Saved metadata value.'}],items:[{title:'Read fields',fields:[]}]});
 const all=[];function walk(n){all.push(n);for(const c of n.children||[])walk(c);}walk(nodes.get('s3-result-content'));
 assert.ok(all.some(n=>n.tag==='dt'&&n.textContent==='Name'));assert.ok(all.some(n=>n.tag==='strong'&&n.textContent==='Close'));assert.ok(all.some(n=>n.tag==='h4'&&n.textContent==='Read fields'));assert.ok(all.some(n=>n.textContent==='GET /literal-key'));assert.ok(all.some(n=>n.textContent==='保存されたメタデータの値です。'));assert.ok(all.some(n=>String(n.textContent).includes('HTTP 200')));
});
