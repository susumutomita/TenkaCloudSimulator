// Pure Node tests: upstream ESM is DATA only, never imported or executed.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { MODULE_PATH, ORIGINAL_SHA256, PATCHED_SHA256, transformCodeInputLite, createCodeInputLiteAdapter } from './collapsed-state-transform.mjs';

const root = path.dirname(fileURLToPath(import.meta.url));
const override = process.env.SCALAR_ORIGINAL_CODE_INPUT;
if (Object.hasOwn(process.env, 'CI') && override) throw new Error('CI must verify the installed pinned Scalar module');
if (override && !path.isAbsolute(override)) throw new Error('Original source data path must be absolute');
const original = await readFile(override || path.join(root, MODULE_PATH), 'utf8');
const lock = JSON.parse(await readFile(path.join(root, 'package-lock.json'), 'utf8'));
const helperSource = await readFile(new URL('./collapsed-state-transform.mjs', import.meta.url), 'utf8');
const before = '"aria-expanded": displayVariablesDropdown.value ? "true" : void 0,';
const after = '"aria-expanded": __props.withVariables ? (displayVariablesDropdown.value ? "true" : "false") : void 0,';
const sha = value => createHash('sha256').update(value).digest('hex');
const options = { source: original, modulePath: MODULE_PATH, lock };
const patched = transformCodeInputLite(options);

// Exercise otherwise unreachable defense-in-depth checks by mutating ONLY the
// first-party helper's test-local hash constant. Production has no pin override.
async function helperWithTestPin(name, value) {
  const anchor = new RegExp("export const " + name + " = '[a-f0-9]{64}';", 'g');
  assert.equal([...helperSource.matchAll(anchor)].length, 1);
  const mutant = helperSource.replace(anchor, `export const ${name} = '${value}';`);
  return import('data:text/javascript;base64,' + Buffer.from(mutant).toString('base64'));
}
function expression(source, name) {
  const lines = source.split('\n').filter(line => line.trimStart().startsWith('"' + name + '":'));
  assert.equal(lines.length, 1);
  return lines[0].trim().slice(('"' + name + '":').length).trim().replace(/,$/, '');
}
// Interpret only identifiers, property reads, string literals, void0 and ?:.
// No eval/new Function/import of source fragments or upstream modules.
function interpretConditional(source, values) {
  const tokens = source.match(/void\s+0|"true"|"false"|[A-Za-z_][\w]*(?:\.[A-Za-z_][\w]*)*|[?:()]/g) || [];
  assert.equal(tokens.join('').replace(/\s/g, ''), source.replace(/\s/g, ''), 'Unexpected expression grammar');
  let index = 0;
  function primary() {
    const token = tokens[index++];
    if (token === '(') { const value = conditional(); assert.equal(tokens[index++], ')'); return value; }
    if (/^void\s+0$/.test(token)) return undefined;
    if (token === '"true"' || token === '"false"') return token.slice(1, -1);
    assert.match(token, /^[A-Za-z_][\w]*(?:\.[A-Za-z_][\w]*)*$/);
    return token.split('.').reduce((value, key) => {
      assert.ok(value !== null && typeof value === 'object' && Object.hasOwn(value, key), 'Unknown expression property');
      return value[key];
    }, values);
  }
  function conditional() {
    const condition = primary();
    if (tokens[index] !== '?') return condition;
    index++; const yes = conditional(); assert.equal(tokens[index++], ':'); const no = conditional();
    return condition ? yes : no;
  }
  const result = conditional(); assert.equal(index, tokens.length); return result;
}
function loaderFixture(readSource = async () => original, suppliedLock = lock) {
  const buildRoot = '/owned/scalar+[exact]';
  const adapter = createCodeInputLiteAdapter({ root: buildRoot, lock: suppliedLock, readSource });
  let registration;
  adapter.plugin.setup({ onLoad(settings, callback) { assert.equal(registration, undefined); registration = { settings, callback }; } });
  return { adapter, ...registration, expectedPath: path.join(buildRoot, MODULE_PATH) };
}

test('original module changes exactly one attribute and has exact whole-byte inverse', () => {
  assert.equal(sha(original), ORIGINAL_SHA256); assert.equal(sha(patched), PATCHED_SHA256);
  assert.equal(original.split(before).length, 2);
  assert.equal(patched, original.replace(before, after));
  assert.equal(patched.replace(after, before), original);
});
test('wrong module paths are rejected with exact original source', () => {
  for (const modulePath of ['scalar.js', '../' + MODULE_PATH, MODULE_PATH + '.map'])
    assert.throws(() => transformCodeInputLite({ ...options, modulePath }), /module path/);
});
test('both package versions must match the installed lock', () => {
  for (const name of ['node_modules/@scalar/api-client', 'node_modules/@scalar/components']) {
    const changed = structuredClone(lock); changed.packages[name].version += '-changed';
    assert.throws(() => transformCodeInputLite({ ...options, lock: changed }), /lock identity/);
  }
});
test('both archive integrities must match the installed lock', () => {
  for (const name of ['node_modules/@scalar/api-client', 'node_modules/@scalar/components']) {
    const changed = structuredClone(lock); changed.packages[name].integrity += '-changed';
    assert.throws(() => transformCodeInputLite({ ...options, lock: changed }), /lock identity/);
  }
});
test('missing package records or lock objects fail closed', () => {
  for (const name of ['node_modules/@scalar/api-client', 'node_modules/@scalar/components']) {
    const changed = structuredClone(lock); delete changed.packages[name];
    assert.throws(() => transformCodeInputLite({ ...options, lock: changed }), /lock identity/);
  }
  assert.throws(() => transformCodeInputLite({ ...options, lock: null }), /lock identity/);
});
test('changed duplicate already-patched and non-string source is rejected', () => {
  for (const source of [original + '\n', original + original, patched, null, Buffer.from(original)])
    assert.throws(() => transformCodeInputLite({ ...options, source }), /original Scalar source hash/);
});
test('missing anchor is rejected even if its test-local input hash is permitted', async () => {
  const source = original.replace(before, '"aria-expanded": void 0,');
  const mutant = await helperWithTestPin('ORIGINAL_SHA256', sha(source));
  assert.throws(() => mutant.transformCodeInputLite({ ...options, source }), /exactly one/);
});
test('duplicate anchor is rejected even if its test-local input hash is permitted', async () => {
  const source = original + '\n' + before;
  const mutant = await helperWithTestPin('ORIGINAL_SHA256', sha(source));
  assert.throws(() => mutant.transformCodeInputLite({ ...options, source }), /exactly one/);
});
test('wrong expected output hash refuses the otherwise exact transform', async () => {
  const mutant = await helperWithTestPin('PATCHED_SHA256', '0'.repeat(64));
  assert.throws(() => mutant.transformCodeInputLite(options), /transformed Scalar source hash/);
});
test('actual expanded expression tracks collapsed/open states only for variable inputs', () => {
  const code = expression(patched, 'aria-expanded');
  for (const withVariables of [false, true]) for (const open of [false, true])
    assert.equal(interpretConditional(code, { __props: { withVariables }, displayVariablesDropdown: { value: open } }), withVariables ? (open ? 'true' : 'false') : undefined);
});
test('genuine controls and active-descendant expressions remain unchanged and open-only', () => {
  for (const name of ['aria-controls', 'aria-activedescendant']) {
    assert.equal(expression(patched, name), expression(original, name));
    for (const open of [false, true]) {
      const values = { displayVariablesDropdown: { value: open }, listboxId: { value: 'actual-listbox' }, activeOptionId: { value: 'actual-option' } };
      assert.equal(interpretConditional(expression(patched, name), values), open ? (name === 'aria-controls' ? 'actual-listbox' : 'actual-option') : undefined);
    }
  }
});
test('real popup predicate mount IDs role and close handlers are preserved', () => {
  for (const fragment of [
    'const displayVariablesDropdown = computed(() => showDropdown.value && __props.withVariables && __props.layout !== "modal" && (Boolean(__props.environment) || __props.withFakeData));',
    'displayVariablesDropdown.value ? (openBlock(), createBlock(EnvironmentVariablesDropdown_default, {',
    'listboxId: listboxId.value,', 'role: __props.withVariables ? "combobox" : "textbox",',
    'onBlur: handleBlur,', 'onKeydown: handleKeyDown,', 'onSelect: handleDropdownSelect',
    'onClose: _cache[0] || (_cache[0] = ($event) => showDropdown.value = false),',
  ]) assert.ok(original.includes(fragment) && patched.includes(fragment), fragment);
});
test('onLoad filter matches only the exact module path and file namespace', () => {
  const f = loaderFixture(); assert.equal(f.settings.namespace, 'file');
  assert.equal(f.settings.filter.test(f.expectedPath), true);
  for (const other of [f.expectedPath + '.map', '/elsewhere/' + MODULE_PATH, f.expectedPath.replace('scalar+[exact]', 'scalarZZexact')])
    assert.equal(f.settings.filter.test(other), false);
});
test('loader reads original source as data and records truthful transformed provenance', async () => {
  const reads = []; const f = loaderFixture(async (filename, encoding) => { reads.push([filename, encoding]); return original; });
  const result = await f.callback({ path: f.expectedPath, namespace: 'file' });
  assert.deepEqual(reads, [[f.expectedPath, 'utf8']]); assert.equal(result.contents, patched);
  assert.equal(result.loader, 'js'); assert.equal(result.resolveDir, path.dirname(f.expectedPath));
  assert.deepEqual(result.watchFiles, [f.expectedPath]);
  const record = f.adapter.provenance(); assert.equal(record.originalSHA256, sha(original)); assert.equal(record.transformedSHA256, sha(result.contents));
  assert.equal(record.originalBytes, Buffer.byteLength(original)); assert.equal(record.transformedBytes, Buffer.byteLength(patched));
  record.originalSHA256 = 'changed'; assert.equal(f.adapter.provenance().originalSHA256, ORIGINAL_SHA256);
});
test('loader refuses wrong paths namespaces and duplicate transformation', async () => {
  const f = loaderFixture();
  await assert.rejects(f.callback({ path: f.expectedPath + '.map', namespace: 'file' }), /path or namespace/);
  await assert.rejects(f.callback({ path: f.expectedPath, namespace: 'virtual' }), /path or namespace/);
  await f.callback({ path: f.expectedPath, namespace: 'file' });
  await assert.rejects(f.callback({ path: f.expectedPath, namespace: 'file' }), /more than once/);
});
test('missing or altered module cannot manufacture successful provenance', async () => {
  const absent = loaderFixture(async () => { throw Object.assign(new Error('missing module'), { code: 'ENOENT' }); });
  assert.throws(() => absent.adapter.provenance(), /not transformed/);
  await assert.rejects(absent.callback({ path: absent.expectedPath, namespace: 'file' }), /missing module/);
  assert.throws(() => absent.adapter.provenance(), /not transformed/);
  const changed = loaderFixture(async () => original + '\n');
  await assert.rejects(changed.callback({ path: changed.expectedPath, namespace: 'file' }), /original Scalar source hash/);
  assert.throws(() => changed.adapter.provenance(), /not transformed/);
});
test('loader enforces both lock identities and absolute build roots', async () => {
  assert.throws(() => createCodeInputLiteAdapter({ root: 'relative', lock }), /Absolute/);
  const changed = structuredClone(lock); changed.packages['node_modules/@scalar/api-client'].version = 'wrong';
  const f = loaderFixture(async () => original, changed);
  await assert.rejects(f.callback({ path: f.expectedPath, namespace: 'file' }), /lock identity/);
  assert.throws(() => f.adapter.provenance(), /not transformed/);
});

// A present but empty CI marker must not silently select retained fixture data.
test('CI source override is refused for empty, false-like and true markers', () => {
  for (const marker of ['', 'false', 'true']) {
    const result = spawnSync(process.execPath, [fileURLToPath(import.meta.url)], {
      env: { ...process.env, CI: marker, SCALAR_ORIGINAL_CODE_INPUT: '/nonexistent-scalar-test-data.js' },
      encoding: 'utf8', timeout: 10000,
    });
    assert.notEqual(result.status, 0);
    const output = (result.stdout || '') + (result.stderr || '');
    assert.match(output, /CI must verify the installed pinned Scalar module/);
    assert.ok(!output.includes('ENOENT'), 'Override must fail before attempting source access');
  }
});
