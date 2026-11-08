import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {createRequire} from 'node:module';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import vm from 'node:vm';
import test from 'node:test';

const require=createRequire(import.meta.url);
// An explicit existing same-lock compiler path supports isolated worktrees;
// absence is a failure, never an install, download or alternate scanner path.
const esbuild=require(process.env.SQS_ESBUILD_MODULE||'esbuild');
assert.equal(esbuild.version,'0.28.2');
const source=readFileSync(fileURLToPath(new URL('../src/sqs.jsx',import.meta.url)),'utf8');
const hash=value=>createHash('sha256').update(value).digest('hex');
const options={loader:'jsx',format:'esm',target:'es2022',legalComments:'none'};
const compile=value=>esbuild.transformSync(value,options).code;
// Public PR410 immutable source39fca989, original JSX SHA recorded independently.
const originalSourceSHA='14ea0106a43019d97f1107ab4e6d6dbc17fb159e1f9f9640f86cc094f845b227';
const originalEnglishCompiledSHA='c41de3c0ed3f73804de25a11340f87debaa53fde30561b0efa689a9eeabf96e6';
const helperPattern=/\/\/ SQS presentation helpers BEGIN:[\s\S]*?\/\/ SQS presentation helpers END\.\n\n/g;
const helpers=source.match(helperPattern);
assert.equal(helpers?.length,1);
const helperSHA='dc22f61879479144b9737785be80d21da25781b5266c0500a9c738ecf9a39f3e';

function exactly(text,old,next,count){
 assert.equal(text.split(old).length-1,count,`Unexpected source seam: ${old}`);
 return text.split(old).join(next);
}
function englishErasure(input){
 // Deliberately narrow source recognizer. It accepts our literal t(en,ja)
 // expressions only; unknown syntax fails instead of erasing arbitrary logic.
 assert.equal(hash(input.match(helperPattern)?.[0]||''),helperSHA);
 let value=input.replace(helperPattern,'');
 value=exactly(value,"import {useLocale} from './i18n.jsx';\n",'',1);
 value=exactly(value,' const {t}=useLocale();\n','',1);
 value=exactly(value,'const identifier=(value,t)=>','const identifier=value=>',1);
 let identifiers=0;
 value=value.replace(/identifier\(([^()]*)?,t\)/g,(_,args)=>{identifiers++;return `identifier(${args})`;});
 assert.equal(identifiers,8);
 value=exactly(value,'localizeSqsLabel(label,t)','label',2);
 value=exactly(value,'localizeSqsLabel(description,t)','description',2);
 value=exactly(value,'localizeSqsNotice(notice,t)','notice',1);
 let translations=0;
 value=value.replace(/\bt\(("(?:\\.|[^"\\])*"),("(?:\\.|[^"\\])*")\)/g,(_,en,ja)=>{
  assert.equal(typeof JSON.parse(en),'string');assert.equal(typeof JSON.parse(ja),'string');translations++;return en;
 });
 assert.ok(translations>50,'Expected static localized surface');
 assert.doesNotMatch(value,/\bt\(/,'Unrecognized translation expression');
 return value;
}

function pureExports(){
 const module={exports:{}};
 const code=esbuild.transformSync(source,{...options,format:'cjs'}).code;
 // Only the exported pure presentation helpers are invoked. Components/hooks
 // are not rendered, and these stubs are not UI/native/backend evidence.
 new vm.Script(code,{filename:'sqs-presentation-only.js'}).runInNewContext({module,exports:module.exports,require(name){
  assert.ok(name==='react'||name==='./i18n.jsx'||name.startsWith('@cloudscape-design/components/'));
  return {};
 }},{timeout:1000});
 return module.exports;
}
const en=english=>english,ja=(_,japanese)=>japanese;
const presentation=pureExports();

test('localized JSX parses and English erasure matches the immutable original program',()=>{
 assert.match(originalSourceSHA,/^[a-f0-9]{64}$/);
 assert.ok(compile(source).length>0);
 assert.equal(hash(compile(englishErasure(source))),originalEnglishCompiledSHA);
});

test('API, receipt, epoch, effect and raw-error mutations cannot hide in English erasure',()=>{
 for(const [old,next] of [
  ["'AmazonSQS.'+operation","'Other.'+operation"],
  ['ReceiptHandle:handle',"ReceiptHandle:'fabricated'"],
  ['alive.current&&epoch===revision.current','true'],
  [' },[]);',' },[t]);'],
  ['setError(e.message)','setError("ignored")'],
  ["number(form.visibility,0,43200,'Visibility timeout')","number(form.visibility,0,43201,'Visibility timeout')"]
 ]){
  assert.ok(source.includes(old),old);
  assert.notEqual(hash(compile(englishErasure(source.replace(old,next)))),originalEnglishCompiledSHA,old);
 }
});

test('canonical success notices retranslate when the locale changes, including received count',()=>{
 const cases=[['Queue created.','キューを作成しました。'],['Message sent.','メッセージを送信しました。'],['Queue deleted.','キューを削除しました。'],['Message deleted.','メッセージを削除しました。'],['Message visibility changed.','メッセージの可視性を変更しました。'],['No messages returned by this poll.','今回のポーリングでメッセージは取得されませんでした。']];
 for(const [canonical,japanese] of cases){
  assert.equal(presentation.localizeSqsNotice(canonical,en),canonical);
  assert.equal(presentation.localizeSqsNotice(canonical,ja),japanese);
  assert.equal(presentation.localizeSqsNotice(canonical,en),canonical);
 }
 for(const count of [1,2,10])assert.equal(presentation.localizeSqsNotice(`${count} message(s) received.`,ja),`${count} 件のメッセージを受信しました。`);
 for(const canonical of ['Polling stopped locally. A server claim may already have completed; refresh queue details before retrying.','Message released. Poll again for a new receipt.']){
  assert.equal(presentation.localizeSqsNotice(canonical,en),canonical);
  assert.notEqual(presentation.localizeSqsNotice(canonical,ja),canonical);
 }
});

test('only static known labels/notices translate; canonical API/resource/opaque values stay intact',()=>{
 assert.equal(presentation.localizeSqsLabel('Visibility timeout (seconds)',ja),'可視性タイムアウト (秒)');
 assert.equal(presentation.localizeSqsLabel('Maximum messages',ja),'最大メッセージ数');
 for(const value of ['ReceiveMessage','Standard','FIFO','ARN','arn:aws:sqs:us-east-1:000000000000:queue','https://local/queue','403: AccessDenied','Queue created. for queue-1']){
  assert.equal(presentation.localizeSqsLabel(value,ja),value);
  assert.equal(presentation.localizeSqsNotice(value,ja),value);
 }
 assert.match(source,/errorText=\{error\}/);
 assert.match(source,/>\{error\}<\/Alert>/);
 assert.doesNotMatch(source,/localizeSqs(?:Label|Notice)\(error/);
});
