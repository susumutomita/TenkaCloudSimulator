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

test('default-English erasure reproduces the original ffdf1e29 compiled module', () => {
  let erased = source.replace("import {useLocale} from './i18n.jsx';\n", '').replace(' const {t}=useLocale();\n', '');
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
