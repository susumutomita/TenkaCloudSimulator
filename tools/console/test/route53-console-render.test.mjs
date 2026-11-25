import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';
import {initialPTRDraft} from '../src/route53-console.mjs';

// Compile the real Cloudscape view using the existing pinned console build
// dependencies; these are render/label assertions, not browser automation.
const require = createRequire(import.meta.url);
const {outputFiles} = await build({
  absWorkingDir:fileURLToPath(new URL('..',import.meta.url)),
  entryPoints:['src/route53-console.jsx'],bundle:true,platform:'node',format:'cjs',write:false,loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'],define:{'process.env.NODE_ENV':'"production"'},
});
const module = {exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,module,module.exports);
const {default:View,Route53PTRForm:Form} = module.exports;
const {createElement} = require('react'), {renderToStaticMarkup} = require('react-dom/server');
const request = () => assert.fail('SSR must not issue a native request');
const capabilities = {services:{route53:{console:true,publicZonesOnly:true,protocol:'REST/XML 2013-04-01',
  operations:['ListHostedZones','GetHostedZone','ListResourceRecordSets','ChangeResourceRecordSets']}}};
const markup = (component,props) => renderToStaticMarkup(createElement(component,props));

test('public native zone list is only visible in a normal authenticated console',()=>{
  const allowed = markup(View,{request,capabilities,authenticationRequired:true});
  assert.match(allowed,/>Route 53</);
  assert.match(allowed,/>Public hosted zones</);
  assert.match(allowed,/Existing local public zones only\./);
  assert.doesNotMatch(allowed,/>Create PTR record</);
  for(const props of [{authenticationRequired:false},{authenticationRequired:true,capabilities:{...capabilities,consoleScope:{instanceId:'i-handoff'}}}]) {
    const blocked = markup(View,{request,capabilities,...props});
    assert.match(blocked,/normal signed-in local session/);
    assert.doesNotMatch(blocked,/>Public hosted zones</);
  }
});

test('the mounted PTR form uses labeled fields and exposes only a bounded create action',()=>{
  const form = markup(Form,{zone:'2.0.192.in-addr.arpa.',fields:initialPTRDraft.fields,busy:false,mustRefresh:false,onSave:request});
  for(const label of ['Full record name','TTL (seconds)','PTR target']) {
    const labelText = form.split(label+'</label>')[0];
    const match = /<label[^>]*for="([^"]+)"[^>]*>$/.exec(labelText);
    assert.ok(match,label);
    assert.ok(form.match(new RegExp('<input[^>]*id="'+match[1]+'"')),label);
  }
  const submit = form.match(/<button[^>]*type="submit"[^>]*>/)?.[0];
  assert.match(submit,/aria-disabled="true"/);
  assert.doesNotMatch(submit,/ disabled(?:=| )/); // blocked but keyboard-focusable

  assert.match(form,/>Create PTR record</);
  assert.doesNotMatch(form,/>Delete | selected policy|>Upsert |<textarea/);
});

test('a refused PTR draft remains visible in read-only fields with a focusable blocked Submit',()=>{
  const fields = {name:'7.2.0.192.in-addr.arpa.',ttl:'75',target:'deny.example.test.'};
  const props = {zone:'2.0.192.in-addr.arpa.',fields,busy:false,mustRefresh:true,onSave:request};
  const locked = markup(Form,props);
  for (const value of Object.values(fields)) {
    const input = locked.match(/<input[^>]+>/g).find(el => el.includes('value="'+value+'"'));
    assert.match(input,/readOnly=""/i);
  }
  const submit = locked.match(/<button[^>]*type="submit"[^>]*>/)[0];
  assert.match(submit,/aria-disabled="true"/);
  assert.doesNotMatch(submit,/ disabled(?:=| )/);
  const fresh = markup(Form,{...props,mustRefresh:false});
  assert.doesNotMatch(fresh,/aria-disabled="true"|readOnly=""/i);
});
