// First-party, build-time source adapter. Never edits installed source or DOM.
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import path from 'node:path';

export const MODULE_PATH = 'node_modules/@scalar/api-client/dist/v2/components/code-input/CodeInputLite.vue_vue_type_script_setup_true_lang.js';
export const ORIGINAL_SHA256 = '6dcb7929ef086ae30d84e0f0e21c31b8e0aa9fa1f3994441a6a25878128730ac';
export const PATCHED_SHA256 = '965055c4bfd49b1e5b38e9710474118f9bb96cadba0a3c19a58d017473155be5';
const packages = Object.freeze({
  'node_modules/@scalar/api-client': Object.freeze({
    version: '3.23.6',
    integrity: 'sha512-VmMAB9kuY2D7v4E1IQnbo2IsHzSmof0vawfloRlribxlvKcRr5zCmtaRMxfvScuhiiutBE7fZ9nZn+88owWprQ==',
  }),
  'node_modules/@scalar/components': Object.freeze({
    version: '0.33.1',
    integrity: 'sha512-tNhtJx1sfX44qPMnC1hgudbIuUuHDhvRW5DgB9s5iWtIIJVKA0TsdjpL65itz9Cz0reDx7eGdA7lEyr+MngDvw==',
  }),
});
const before = '"aria-expanded": displayVariablesDropdown.value ? "true" : void 0,';
const after = '"aria-expanded": __props.withVariables ? (displayVariablesDropdown.value ? "true" : "false") : void 0,';
const sha = value => createHash('sha256').update(value).digest('hex');

/**
 * Transform one original, version-locked ESM module in memory for a future
 * ordinary source rebuild. Caller must verify the full source/rights closure.
 * Only the exact original module may be supplied; no upstream code executes here.
 */
export function transformCodeInputLite({ source, modulePath, lock }) {
  if (modulePath !== MODULE_PATH) throw new Error('Unexpected Scalar module path');
  for (const [name, expected] of Object.entries(packages)) {
    const actual = lock?.packages?.[name];
    if (!actual || actual.version !== expected.version || actual.integrity !== expected.integrity)
      throw new Error('Unexpected Scalar package lock identity: ' + name);
  }
  if (typeof source !== 'string' || sha(source) !== ORIGINAL_SHA256)
    throw new Error('Unexpected original Scalar source hash');
  if (source.split(before).length !== 2)
    throw new Error('Expected exactly one collapsed-state source anchor');
  const output = source.replace(before, after);
  if (sha(output) !== PATCHED_SHA256)
    throw new Error('Unexpected transformed Scalar source hash');
  return output;
}


/** esbuild integration uses only a supplied API object; importing this helper
 * never imports esbuild, Vue, or a Scalar module. Installed source stays intact.
 */
export function createCodeInputLiteAdapter({ root, lock, readSource = readFile }) {
  if (!path.isAbsolute(root)) throw new Error('Absolute Scalar build root required');
  const expectedPath = path.join(root, MODULE_PATH);
  const escape = value => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const filter = new RegExp('^' + escape(expectedPath) + '$');
  let record = null;
  return {
    plugin: {
      name: 'scalar-code-input-lite-collapsed-state',
      setup(build) {
        build.onLoad({ filter, namespace: 'file' }, async args => {
          if (args.path !== expectedPath || args.namespace !== 'file')
            throw new Error('Unexpected Scalar loader path or namespace');
          if (record !== null) throw new Error('Scalar source transformed more than once');
          const source = await readSource(expectedPath, 'utf8');
          const contents = transformCodeInputLite({ source, modulePath: MODULE_PATH, lock });
          record = {
            modulePath: MODULE_PATH,
            originalSHA256: ORIGINAL_SHA256,
            transformedSHA256: PATCHED_SHA256,
            originalBytes: Buffer.byteLength(source),
            transformedBytes: Buffer.byteLength(contents),
            packages,
          };
          return { contents, loader: 'js', resolveDir: path.dirname(expectedPath), watchFiles: [expectedPath] };
        });
      },
    },
    provenance() {
      if (record === null) throw new Error('Required Scalar source was not transformed');
      return structuredClone(record);
    },
  };
}
