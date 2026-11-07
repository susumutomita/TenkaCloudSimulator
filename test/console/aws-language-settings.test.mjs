import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {awsLanguageSettings} from '../../tools/console/src/aws-language-settings.mjs';

const en=(english)=>english, ja=(_,japanese)=>japanese;
test('AWS gear exposes only implemented Language choices and stays out of the overflow utility',()=>{
  const settings=awsLanguageSettings('en',()=>assert.fail('construction changed locale'),en);
  assert.equal(settings.type,'menu-dropdown');
  assert.equal(settings.iconName,'settings');
  assert.equal(settings.ariaLabel,'Settings');
  assert.equal(settings.disableUtilityCollapse,true);
  assert.equal(settings.items.length,1);
  const [language]=settings.items;
  assert.equal(language.text,'Language');
  assert.equal(language.expandable,true);
  assert.deepEqual(language.items.map(({id,text})=>[id,text]),[['en','English'],['ja','日本語']]);
  assert.equal('href' in settings,false);
  assert.equal('disabled' in settings,false);
});
for(const locale of ['en','ja']) test(`settings reflect the actual ${locale} preference and selecting it cannot unset it`,()=>{
  const writes=[];
  const settings=awsLanguageSettings(locale,value=>writes.push(value),locale==='en'?en:ja);
  assert.equal(settings.title,locale==='en'?'Settings':'設定');
  assert.equal(settings.items[0].text,locale==='en'?'Language':'言語');
  const choices=settings.items[0].items;
  assert.deepEqual(choices.filter(choice=>choice.checked).map(choice=>choice.id),[locale]);
  assert.ok(choices.every(choice=>choice.itemType==='checkbox' && !choice.disabled));
  settings.onItemClick({detail:{id:locale,checked:false}});
  assert.deepEqual(writes,[locale]);
});
test('selection dispatches the chosen locale value and the next menu reflects it',()=>{
  const writes=[];
  const settings=awsLanguageSettings('en',value=>writes.push(value),en);
  settings.onItemClick({detail:{id:'ja',checked:true}});
  assert.deepEqual(writes,['ja']);
  assert.deepEqual(awsLanguageSettings(writes[0],()=>{},ja).items[0].items.map(i=>i.checked),[false,true]);
});
test('settings reject unrelated menu actions and unimplemented locales',()=>{
  const writes=[],settings=awsLanguageSettings('en',value=>writes.push(value),en);
  for(const id of ['language','browser-default','fr','identity','signout','us-east-1','',undefined]) settings.onItemClick({detail:{id}});
  assert.deepEqual(writes,[]);
});
test('global navigation wires AWS settings and retains the exact nonAWS select persistence callback',()=>{
  const source=readFileSync(new URL('../../tools/console/src/global-navigation.jsx',import.meta.url),'utf8');
  assert.match(source,/\{aws&&<div className="aws-console-top-navigation"><TopNavigation/);
  assert.match(source,/utilities=\{\[\s*awsLanguageSettings\(locale, setLocale, t\),/);
  assert.match(source,/\{!aws&&<label className="cloud-language">/);
  assert.match(source,/onChange=\{event=>setLocale\(event.target.value\)\}/);
  // This existing LocaleProvider owns persistence/defaults; navigation never owns them.
  assert.doesNotMatch(source,/localStorage|sessionStorage|fetch\(|key=\{locale/);
});
