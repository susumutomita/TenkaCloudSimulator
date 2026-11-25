import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import test from 'node:test';
import {build} from 'esbuild';

// Render the actual JSX using the Console's pinned React/ReactDOM; do not test
// a copy of the markup. Fixture data is local only, not evidence of a browser or
// a live Google API. This focused test requires tools/console's build dependencies.
const require = createRequire(import.meta.url);
const {outputFiles} = await build({
  absWorkingDir: fileURLToPath(new URL('..', import.meta.url)),
  stdin: {contents: `export {default as Form} from './src/gcp-dns-srv-form.jsx';
    export {default as View} from './src/gcp-dns.jsx';`, resolveDir: fileURLToPath(new URL('..', import.meta.url))},
  bundle:true, platform:'node', format:'cjs', write:false, loader:{'.css':'empty'},
  external:['react','react-dom','react/jsx-runtime'], define:{'process.env.NODE_ENV':'"production"'},
});
const result = {exports:{}};
Function('require','module','exports',outputFiles[0].text)(require,result,result.exports);
const {Form, View} = result.exports;
const {createElement} = require('react'), {renderToStaticMarkup} = require('react-dom/server');
const record = {kind:'dns#resourceRecordSet',name:'_mail._tcp.demo.test.',type:'SRV',ttl:60,
  rrdatas:['0 1 25 mail.demo.test.','1 0 0 .']};
function render(mode, original=null, busy=false) {
  return renderToStaticMarkup(createElement(Form, {mode, original, zone:'demo.test.',busy, onCancel(){}, onSave(){}}));
}
test('Cloud DNS entry initially exposes zones, not an unverified SRV editor', () => {
  const html=renderToStaticMarkup(createElement(View,{project:'local-test', request(){throw Error('Unexpected SSR I/O');},canEditSRV:true}));
  assert.match(html,/>Cloud DNS</); assert.match(html,/>Zones</); assert.doesNotMatch(html,/>Add SRV record</);
});
test('new and existing SRV forms render labeled endpoint fields, canonical values and distinct actions', () => {
  const create=render('create'), edit=render('edit',record), del=render('delete',record,true);
  for(const label of ['SRV record name','TTL (seconds)','Priority','Weight','Port','Target']) {
    const inputLabel=create.split('>'+label+'</label>')[0].match(/<label for="([^"]+)"$/);
    assert.ok(inputLabel,label); assert.ok(create.includes('<input id="'+inputLabel[1]+'"'),label);
  }
  assert.match(create,/>Add endpoint</); assert.match(create,/>Save SRV record</);
  assert.doesNotMatch(create,/readOnly/); assert.doesNotMatch(create,/>Confirm deletion</);
  assert.match(edit,/readOnly=""[^>]+value="_mail\._tcp\.demo\.test\."/);
  assert.match(edit,/>Endpoint 2</); assert.match(edit,/value="mail\.demo\.test\."/);
  assert.match(edit,/value="\."/); assert.match(edit,/>Remove endpoint</);
  assert.match(del,/<fieldset disabled="">/); assert.match(del,/>Confirm deletion</);
  for(const old of [record.name,...record.rrdatas]) assert.ok(del.includes('>'+old+'<'),old);
  assert.doesNotMatch(del,/<input|>Save SRV record</);
});

test('the deletion preview keeps long legal DNS labels complete and in a wrapping fieldset', () => {
  const label = 'w'.repeat(63), longName = '_mail._tcp.' + label + '.demo.test.';
  const longRecord = {...record, name:longName, rrdatas:['1 2 443 ' + label + '.demo.test.']};
  const html=render('delete',longRecord);
  assert.ok(html.includes('class="gcp-storage-form gcp-dns-srv-form"'));
  assert.ok(html.includes('>'+longName+'<')); assert.ok(html.includes('>'+longRecord.rrdatas[0]+'<'));
  const css=readFileSync(new URL('../src/gcp-dns-srv.css',import.meta.url),'utf8');
  assert.match(css, /\.gcp-dns-srv-form fieldset\s*\{[^}]*min-inline-size:\s*0;[^}]*overflow-wrap:\s*anywhere;/);
});
