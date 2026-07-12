import type { ProviderSourceArtifact, SingleRuntimeTarget } from './domain';
import { CoreError } from './errors';

const ARTIFACT_BUNDLE_FORMAT = 'tenkacloud.simulator.artifacts.v1';
const ARTIFACT_BUNDLE_PREFIX = 'tenkacloud.simulator.artifacts.';
const MAX_ARTIFACTS_PER_TARGET = 256;
const MAX_ARTIFACT_PATH_LENGTH = 1024;

interface IdentifiedTarget extends SingleRuntimeTarget {
  readonly id: string;
}

export interface ResolvedTargetSource {
  readonly targetId: string;
  readonly templateBody: string;
  readonly artifacts: readonly ProviderSourceArtifact[];
}

function validationFailed(message: string): never {
  throw new CoreError('ValidationFailed', message);
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function requireExactKeys(
  value: Readonly<Record<string, unknown>>,
  expected: readonly string[],
  label: string
): void {
  const actual = Object.keys(value).sort();
  const canonical = [...expected].sort();
  if (
    actual.length !== canonical.length ||
    actual.some((key, index) => key !== canonical[index])
  ) {
    validationFailed(`${label} has unknown or missing fields`);
  }
}

function requireNonEmptyText(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    validationFailed(`${label} must be a non-empty string`);
  }
  return value;
}

function relativeArtifactPath(value: unknown, label: string): string {
  const path = requireNonEmptyText(value, label);
  if (
    path.length > MAX_ARTIFACT_PATH_LENGTH ||
    path.includes('\0') ||
    path.includes('\\') ||
    path.startsWith('/') ||
    /^[A-Za-z]:/.test(path)
  ) {
    validationFailed(`${label} must be a bounded POSIX relative path`);
  }
  if (path.split('/').some((part) => !part || part === '.' || part === '..')) {
    validationFailed(`${label} must not contain empty or traversal segments`);
  }
  return path;
}

function parseArtifact(
  value: unknown,
  targetId: string
): ProviderSourceArtifact {
  if (!isRecord(value))
    validationFailed(`artifact for ${targetId} must be an object`);
  requireExactKeys(value, ['content', 'path'], `artifact for ${targetId}`);
  return {
    path: relativeArtifactPath(value['path'], `artifact path for ${targetId}`),
    content: requireNonEmptyText(
      value['content'],
      `artifact content for ${targetId}`
    ),
  };
}

function requireCanonicalOrder(values: readonly string[], label: string): void {
  const expected = [...values].sort((left, right) => left.localeCompare(right));
  if (values.some((value, index) => value !== expected[index])) {
    validationFailed(`${label} must use canonical sort order`);
  }
}

function parseTargetArtifacts(
  value: Readonly<Record<string, unknown>>,
  expected: IdentifiedTarget
): ResolvedTargetSource {
  requireExactKeys(
    value,
    ['artifacts', 'engine', 'entry', 'id', 'provider'],
    `artifact target ${expected.id}`
  );
  const identity = {
    id: requireNonEmptyText(value['id'], 'artifact target id'),
    provider: requireNonEmptyText(
      value['provider'],
      'artifact target provider'
    ),
    engine: requireNonEmptyText(value['engine'], 'artifact target engine'),
    entry: relativeArtifactPath(value['entry'], 'artifact target entry'),
  };
  if (
    identity.id !== expected.id ||
    identity.provider !== expected.provider ||
    identity.engine !== expected.engine ||
    identity.entry !== expected.entry
  ) {
    validationFailed(`artifact target ${identity.id} does not match runtime`);
  }
  const values = value['artifacts'];
  if (
    !Array.isArray(values) ||
    values.length < 1 ||
    values.length > MAX_ARTIFACTS_PER_TARGET
  ) {
    validationFailed(
      `artifact target ${expected.id} must contain between 1 and ${MAX_ARTIFACTS_PER_TARGET} files`
    );
  }
  const artifacts = values.map((artifact) =>
    parseArtifact(artifact, expected.id)
  );
  const paths = artifacts.map((artifact) => artifact.path);
  requireCanonicalOrder(paths, `artifacts for ${expected.id}`);
  if (new Set(paths).size !== paths.length) {
    validationFailed(`artifacts for ${expected.id} must have unique paths`);
  }
  const entryArtifact = artifacts.find(
    (artifact) => artifact.path === expected.entry
  );
  if (entryArtifact) {
    return {
      targetId: expected.id,
      templateBody: entryArtifact.content,
      artifacts,
    };
  }
  const directoryPrefix = `${expected.entry}/`;
  if (
    artifacts.some((artifact) => !artifact.path.startsWith(directoryPrefix))
  ) {
    validationFailed(
      `directory artifacts for ${expected.id} must stay below ${expected.entry}`
    );
  }
  const main = artifacts.find(
    (artifact) => artifact.path === `${directoryPrefix}main.tf`
  );
  if (!main) {
    validationFailed(
      `directory artifact target ${expected.id} requires main.tf`
    );
  }
  return { targetId: expected.id, templateBody: main.content, artifacts };
}

function rawSources(
  targets: readonly IdentifiedTarget[],
  templateBody: string
): readonly ResolvedTargetSource[] {
  requireNonEmptyText(templateBody, 'templateBody');
  return targets.map((target) => ({
    targetId: target.id,
    templateBody,
    artifacts: [
      {
        path: relativeArtifactPath(target.entry, 'runtime entry'),
        content: templateBody,
      },
    ],
  }));
}

export function resolveTargetSources(
  targets: readonly IdentifiedTarget[],
  templateBody: string
): readonly ResolvedTargetSource[] {
  let candidate: unknown;
  try {
    candidate = JSON.parse(templateBody);
  } catch {
    return rawSources(targets, templateBody);
  }
  if (!isRecord(candidate)) return rawSources(targets, templateBody);
  const format = candidate['format'];
  if (format !== ARTIFACT_BUNDLE_FORMAT) {
    if (
      typeof format === 'string' &&
      format.startsWith(ARTIFACT_BUNDLE_PREFIX)
    ) {
      validationFailed(`unsupported artifact bundle format: ${format}`);
    }
    return rawSources(targets, templateBody);
  }
  requireExactKeys(candidate, ['format', 'targets'], 'artifact bundle');
  const values = candidate['targets'];
  if (!Array.isArray(values) || values.length !== targets.length) {
    validationFailed(
      'artifact bundle must contain exactly one entry per runtime target'
    );
  }
  if (values.some((value) => !isRecord(value))) {
    validationFailed('artifact bundle targets must be objects');
  }
  const records = values.filter(isRecord);
  const ids = records.map((value) =>
    requireNonEmptyText(value['id'], 'artifact target id')
  );
  requireCanonicalOrder(ids, 'artifact bundle targets');
  if (new Set(ids).size !== ids.length) {
    validationFailed('artifact bundle target ids must be unique');
  }
  const expected = new Map(targets.map((target) => [target.id, target]));
  return records.map((value) => {
    const id = requireNonEmptyText(value['id'], 'artifact target id');
    const target = expected.get(id);
    if (!target)
      validationFailed(`artifact target ${id} is not in the runtime`);
    return parseTargetArtifacts(value, target);
  });
}
