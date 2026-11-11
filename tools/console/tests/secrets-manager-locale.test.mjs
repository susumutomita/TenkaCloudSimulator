// Pure presentation/source-contract checks. No HTTP, browser or secret retrieval.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import crypto from 'node:crypto';
import vm from 'node:vm';
const {default: esbuild} = await import(process.env.ESBUILD_MODULE || 'esbuild');
const file = new URL('../src/secrets-manager.jsx', import.meta.url);
const source = fs.readFileSync(file, 'utf8');
const lock = JSON.parse(fs.readFileSync(new URL('../package-lock.json', import.meta.url), 'utf8'));
const begin = source.indexOf('// Canonical presentation state only.');
const end = source.indexOf('// Parent remounts');
assert.ok(begin >= 0 && end > begin);
const helperSource = source.slice(begin, end);
const helpers = vm.runInNewContext(helperSource + '\n({localeErrors, localeNotices, SecretViewError, captureError, presentError, presentNotice})');
const {localeErrors, localeNotices, SecretViewError, captureError, presentError, presentNotice} = helpers;
const en = english => english;
const ja = (_, japanese) => japanese;

test('uses the existing locked compiler, not a new dependency version', () => {
  assert.equal(esbuild.version, lock.packages['node_modules/esbuild'].version);
});

const ackOriginal = "const result=await call(operation,input);if(!alive.current||ticket!==epoch.current)return;setDraft(freshDraft());";
const ackFixed = ackOriginal.replace('setDraft', "setMode('');setDetail(null);setDraft");
// Exact incoming main415 recovery deltas, independently reviewed; no general erasure.
const incomingMetadataAlert = "header={metadataError?t(\"Metadata refresh failed\",\"メタデータの取得に失敗しました\"):undefined} action={metadataError?<Button formAction=\"none\" disabled={busy||loading} onClick={()=>browse(error.arn)}>{t(\"Retry metadata refresh\",\"メタデータを再取得\")}</Button>:undefined}>{metadataError?<SpaceBetween size=\"xs\"><Box>{error.message}</Box><Box>{t(\"Retrying only reloads metadata.\",\"再試行ではメタデータの読み取りだけを行います。\")}</Box></SpaceBetween>:presentError(error,t)}";
const incomingDeleteFence = "if(operation==='DeleteSecret')setSecrets(items=>items.filter(secret=>secret.ARN!==result.ARN));";
const incomingMetadataCatch = "setError({kind:'metadata',message:e.message,arn})}finally{if(alive.current&&ticket===epoch.current)setLoading(false)}";
function replaceExactlyOne(candidate, before, after) {
  assert.equal(candidate.split(before).length-1,1,'Reviewed incoming recovery block must occur exactly once');
  return candidate.replace(before,after);
}
function normalizeIncomingRecovery(candidate) {
  candidate=replaceExactlyOne(candidate,incomingDeleteFence,'');
  candidate=replaceExactlyOne(candidate,incomingMetadataCatch,"setError(captureError(e))}finally{if(alive.current&&ticket===epoch.current)setLoading(false)}");
  candidate=replaceExactlyOne(candidate," const metadataError=error?.kind==='metadata';\n",'');
  return replaceExactlyOne(candidate,"onDismiss={()=>setError('')} "+incomingMetadataAlert,"onDismiss={()=>setError('')}>{presentError(error,t)}");
}
function normalizeACK(candidate) {
  assert.equal(candidate.split(ackFixed).length-1, 1, 'Exactly one ACK-bound form close is required');
  return candidate.replace(ackFixed, ackOriginal);
}
const motionImport="import React,{useEffect,useLayoutEffect,useRef,useState} from 'react';\nimport {disableMotion} from '@cloudscape-design/global-styles';";
const motionSetup=" const motionRoot=useRef(null);\n useLayoutEffect(()=>{const target=motionRoot.current;if(!target)return;disableMotion(true,target);return()=>disableMotion(false,target)},[]);\n";
function normalizeScopedMotion(candidate) {
  candidate=replaceExactlyOne(candidate,motionImport,"import React,{useEffect,useRef,useState} from 'react';");
  candidate=replaceExactlyOne(candidate,motionSetup,'');
  candidate=replaceExactlyOne(candidate,' return <div data-secrets-motion ref={motionRoot}><SpaceBetween',' return <SpaceBetween');
  return replaceExactlyOne(candidate,'</SpaceBetween></div>\n}\n','</SpaceBetween>\n}\n');
}
const descriptionImport="import SecretDescription from './secret_description.jsx';\n";
const descriptionTab="{id:'description',label:t('Edit description','説明を編集'),disabled:busy||!!mode,content:<SecretDescription region={region} request={request} secretARN={detail?.ARN||''} active={section==='description'}/>},";
function normalizeDescriptionEntry(candidate) {
  candidate=replaceExactlyOne(candidate,descriptionImport,'');
  return replaceExactlyOne(candidate,descriptionTab,'');
}
// Exact integration delta from 6abcbca4bf6612e36470f7e69f29ea090566b8ea (#430).
// Reversing these six literals reproduces its parent source byte-for-byte.
const inheritedVersionIntegration=[
  [
    "import SecretVersions from './secret_versions.jsx';\n",
    ""
  ],
  [
    " const [section,setSection]=useState('secrets');\n",
    ""
  ],
  [
    "{section==='secrets'&&error&&",
    "{error&&"
  ],
  [
    "{section==='secrets'&&notice&&",
    "{notice&&"
  ],
  [
    "<Tabs activeTabId={section} onChange={({detail})=>{if(busy||mode)return;invalidate();setLoading(false);setError('');setSection(detail.activeTabId);if(detail.activeTabId==='secrets')browse()}} tabs={[{id:'secrets',label:t('Secrets','シークレット'),content:<>",
    ""
  ],
  [
    "</>},{id:'versions',label:t('Version metadata','バージョンメタデータ'),disabled:busy||!!mode,content:<SecretVersions region={region} request={request} secretARN={detail?.ARN||''} active={section==='versions'}/>}]} />",
    ""
  ]
];
function normalizeInheritedVersionEntry(candidate) {
 for(const [before,after] of inheritedVersionIntegration)candidate=replaceExactlyOne(candidate,before,after);
 return candidate;
}
function assertEnglishContract(candidate) {
  candidate=normalizeInheritedVersionEntry(normalizeDescriptionEntry(candidate));
  let erased = normalizeACK(normalizeIncomingRecovery(normalizeScopedMotion(candidate))).replace("import {useLocale} from './i18n.jsx';\n", '').replace(' const {t}=useLocale();\n', '');
  const a = erased.indexOf('// Canonical presentation state only.');
  const b = erased.indexOf('// Parent remounts');
  erased = erased.slice(0, a) + erased.slice(b);
  for (const [code, [english]] of Object.entries(localeErrors)) {
    erased = erased.replaceAll("new SecretViewError('" + code + "')", "Error('" + english + "')");
  }
  for (const [kind, [english]] of Object.entries(localeNotices)) {
    erased = erased.replaceAll("{kind:'" + kind + "'}", "'" + english + "'");
  }
  erased = erased.replaceAll('captureError(e)', 'e.message')
    .replaceAll('presentError(error,t)', 'error').replaceAll('presentNotice(notice,t)', 'notice');
  // The warning is a module-local static literal, never user-supplied content.
  erased = erased.replace(/t\(warning,"(?:[^"\\]|\\.)*"\)/g, 'warning');
  erased = erased.replace(/\bt\(("(?:[^"\\]|\\.)*"),("(?:[^"\\]|\\.)*")\)/g, (_, english) => english);
  assert.equal(/\bt\(/.test(erased), false, 'Unaccounted translation must fail closed');
  const compiled = esbuild.transformSync(erased, {loader:'jsx',format:'esm',target:'es2022',legalComments:'none',logLevel:'silent'}).code;
  const hash = crypto.createHash('sha256').update(compiled).digest('hex');
  // Independently compiled from the preserved external-owner leaf, not candidate output.
  assert.equal(hash, 'b702c583d5fc09250ad3d219284367a32715f7337e2e1d33fd0da33c01fafa83');
}
test('English erasure preserves ffdf except exact reviewed ACK/main415 recovery blocks', () => {
  assertEnglishContract(source);
});
test('ACK normalization rejects missing, moved, wrong, duplicated and unrelated mutations', () => {
  for (const candidate of [source.replace(ackFixed,ackOriginal),
    source.replace(ackFixed,ackOriginal+"setMode('');"),
    source.replace(ackFixed,ackFixed.replace("setMode('')","setMode('edit')")),
    source.replace(ackFixed,ackFixed+ackFixed),
    source.replace(ackFixed,ackFixed.replace('await call(operation,input)','await call(operation,{})'))]) {
    assert.throws(()=>assertEnglishContract(candidate));
  }
  assert.throws(()=>assertEnglishContract(source.replace("setNotice(message)","setNotice('changed')")));
});

test('incoming metadata recovery normalization rejects missing or changed fences', () => {
  for (const candidate of [source.replace(incomingDeleteFence,''),
    source.replace(incomingDeleteFence,incomingDeleteFence.replace('!==','===')),
    source.replace('setDetail(null);setDraft','setDetail(detail);setDraft'),
    source.replace(incomingMetadataCatch,incomingMetadataCatch.replace('arn})','arn:null})')),
    source.replace(incomingMetadataAlert,incomingMetadataAlert.replace('browse(error.arn)','mutate(error.arn)'))]) {
    assert.throws(()=>assertEnglishContract(candidate));
  }
});

test('scoped before-paint motion normalization rejects global, missing, late or uncleaned effects',()=>{
 for(const candidate of [source.replace('disableMotion(true,target)','disableMotion(true)'),
  source.replace('disableMotion(true,target)','disableMotion(true,document.body)'),
  source.replace('useLayoutEffect(()=>','useEffect(()=>'),
  source.replace('return()=>disableMotion(false,target)','return()=>{}'),
  source.replace('if(!target)return;',''),
  source.replace('data-secrets-motion ref={motionRoot}','data-secrets-motion')]) {
   assert.throws(()=>assertEnglishContract(candidate));
 }
});

test('all seven internal errors keep canonical English and render current Japanese', () => {
  assert.equal(Object.keys(localeErrors).length, 7);
  for (const [code, copy] of Object.entries(localeErrors)) {
    const error = new SecretViewError(code), captured = captureError(error);
    assert.equal(error.message, copy[0]);
    assert.equal(presentError(captured, en), copy[0]);
    assert.equal(presentError(captured, ja), copy[1]);
    assert.notEqual(copy[0], copy[1]);
  }
});

test('server errors remain opaque even when they equal an internal English literal', () => {
  for (const message of [...Object.values(localeErrors).map(copy => copy[0]), 'AccessDeniedException: arn:aws:secretsmanager:local:dummy', 'literal user JSON {"key":"dummy-only"}']) {
    const captured = captureError({message});
    assert.equal(captured, message);
    assert.equal(presentError(captured, ja), message);
  }
});

test('four canonical success notices follow the current language without parsing data', () => {
  assert.equal(Object.keys(localeNotices).length, 4);
  for (const [kind, copy] of Object.entries(localeNotices)) {
    const notice = {kind};
    assert.equal(presentNotice(notice, en), copy[0]);
    assert.equal(presentNotice(notice, ja), copy[1]);
    assert.notEqual(copy[0], copy[1]);
  }
  assert.equal(presentNotice({kind:'user-supplied-secret'}, ja), '');
});

test('locale helpers introduce no credential or reveal sinks', () => {
  assert.equal(/request\(|fetch\(|localStorage|sessionStorage|console\.|setRevealed|SecretString|JSON\.parse|JSON\.stringify/.test(helperSource), false);
  assert.equal(/\bt\((?:draft|revealed|detail|value|e\.message)/.test(source), false);
  assert.equal(/setNotice\(t\(|setError\(t\(/.test(source), false);
});

test('description entry normalization rejects missing, duplicated or changed wiring',()=>{
 for(const candidate of [source.replace(descriptionImport,''),source.replace(descriptionImport,descriptionImport+descriptionImport),
  source.replace(descriptionImport,descriptionImport.replace('./secret_description.jsx','./secret_versions.jsx')),
  source.replace(descriptionTab,''),source.replace(descriptionTab,descriptionTab+descriptionTab),
  source.replace(descriptionTab,descriptionTab.replace('request={request}','request={()=>{}}')),
  source.replace(descriptionTab,descriptionTab.replace('region={region}',"region={'us-east-1'}")),
  source.replace(descriptionTab,descriptionTab.replace("secretARN={detail?.ARN||''}","secretARN={detail?.Name||''}")),
  source.replace(descriptionTab,descriptionTab.replace("active={section==='description'}",'active={true}')),
  source.replace(descriptionTab,descriptionTab.replace('disabled:busy||!!mode','disabled:false'))])assert.throws(()=>assertEnglishContract(candidate));
});

test('inherited version integration requires every exact historical literal',()=>{
 const inherited=normalizeDescriptionEntry(source);
 for(const [before] of inheritedVersionIntegration){
  assert.throws(()=>normalizeInheritedVersionEntry(inherited.replace(before,'')));
  assert.throws(()=>normalizeInheritedVersionEntry(inherited.replace(before,before+before)));
  assert.throws(()=>normalizeInheritedVersionEntry(inherited.replace(before,before.replace(/.$/s,'!'))));
 }
});
