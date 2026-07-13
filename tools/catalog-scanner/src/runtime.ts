import { basename } from 'node:path';
import { SIMULATOR_RUNTIME_TARGET_ID_PATTERN } from '@tenkacloud/simulator-contracts';
import type { Diagnostic, NormalizedTarget, SourceLocation } from './model.ts';
import { sourceLocation } from './source.ts';
import { isRecord, recordValue, stringValue } from './value.ts';

export interface RuntimeNormalization {
  problemId: string | null;
  category: string;
  status: string;
  targets: NormalizedTarget[];
  diagnostics: Diagnostic[];
}

interface RuntimeContext {
  metadata: Record<string, unknown>;
  metadataPath: string;
  metadataContents: string;
  problemDirectory: string;
}

function diagnostic(
  context: RuntimeContext,
  code: Diagnostic['code'],
  message: string,
  problemId: string | null,
  token: string
): Diagnostic {
  return {
    code,
    message,
    problemId,
    targetId: null,
    source: sourceLocation(
      context.metadataPath,
      context.metadataContents,
      token,
      '/runtime'
    ),
  };
}

function singleTarget(
  provider: string,
  engine: string,
  entry: string
): NormalizedTarget | undefined {
  if (provider === 'docker' && engine === 'compose') {
    return {
      targetId: 'default',
      provider,
      engine,
      entry,
      delivery: 'container',
    };
  }
  if (provider === 'aws' && engine === 'cloudformation') {
    return { targetId: 'default', provider, engine, entry, delivery: 'cloud' };
  }
  if (provider === 'azure' && engine === 'bicep') {
    return { targetId: 'default', provider, engine, entry, delivery: 'cloud' };
  }
  if (
    provider === 'gcp' &&
    (engine === 'infra-manager' || engine === 'terraform')
  ) {
    return { targetId: 'default', provider, engine, entry, delivery: 'cloud' };
  }
  if (provider === 'sakura' && engine === 'apprun') {
    return { targetId: 'default', provider, engine, entry, delivery: 'cloud' };
  }
  return undefined;
}

function normalizeSingleRuntime(
  context: RuntimeContext,
  runtime: Record<string, unknown>,
  problemId: string
): { targets: NormalizedTarget[]; diagnostics: Diagnostic[] } {
  const provider = stringValue(runtime, 'provider');
  const engine = stringValue(runtime, 'engine');
  const entry = stringValue(runtime, 'entry');
  if (provider === undefined || engine === undefined || entry === undefined) {
    return {
      targets: [],
      diagnostics: [
        diagnostic(
          context,
          'INVALID_RUNTIME',
          'single runtime requires provider, engine, and entry',
          problemId,
          '"runtime"'
        ),
      ],
    };
  }
  const normalized = singleTarget(provider, engine, entry);
  if (normalized === undefined) {
    return {
      targets: [],
      diagnostics: [
        diagnostic(
          context,
          'UNSUPPORTED_ENGINE',
          `unsupported runtime provider/engine: ${provider}/${engine}`,
          problemId,
          `"engine"`
        ),
      ],
    };
  }
  const legacyEntry = stringValue(context.metadata, 'cfnTemplate');
  if (legacyEntry !== undefined && legacyEntry !== entry) {
    return {
      targets: [normalized],
      diagnostics: [
        diagnostic(
          context,
          'INVALID_RUNTIME',
          `runtime.entry (${entry}) does not match cfnTemplate (${legacyEntry})`,
          problemId,
          '"entry"'
        ),
      ],
    };
  }
  return { targets: [normalized], diagnostics: [] };
}

function normalizeCompositeTarget(
  value: unknown,
  seenIds: Set<string>
): NormalizedTarget | undefined {
  if (!isRecord(value)) return undefined;
  const id = stringValue(value, 'id');
  const provider = stringValue(value, 'provider');
  const engine = stringValue(value, 'engine');
  const entry = stringValue(value, 'entry');
  if (
    id === undefined ||
    provider === undefined ||
    engine === undefined ||
    entry === undefined ||
    !SIMULATOR_RUNTIME_TARGET_ID_PATTERN.test(id) ||
    seenIds.has(id)
  ) {
    return undefined;
  }
  const normalized = singleTarget(provider, engine, entry);
  if (normalized === undefined || normalized.delivery !== 'cloud')
    return undefined;
  seenIds.add(id);
  return { ...normalized, targetId: id };
}

function normalizeCompositeRuntime(
  context: RuntimeContext,
  runtime: Record<string, unknown>,
  problemId: string
): { targets: NormalizedTarget[]; diagnostics: Diagnostic[] } {
  const rawTargets = recordValue(runtime, 'targets');
  if (
    !Array.isArray(rawTargets) ||
    rawTargets.length < 2 ||
    rawTargets.length > 8
  ) {
    return {
      targets: [],
      diagnostics: [
        diagnostic(
          context,
          'INVALID_RUNTIME',
          'composite runtime requires 2 to 8 targets',
          problemId,
          '"targets"'
        ),
      ],
    };
  }
  const seenIds = new Set<string>();
  const targets: NormalizedTarget[] = [];
  for (const rawTarget of rawTargets) {
    const target = normalizeCompositeTarget(rawTarget, seenIds);
    if (target === undefined) {
      return {
        targets,
        diagnostics: [
          diagnostic(
            context,
            'INVALID_RUNTIME',
            'composite target is invalid, duplicated, or unsupported',
            problemId,
            '"targets"'
          ),
        ],
      };
    }
    targets.push(target);
  }
  return { targets, diagnostics: [] };
}

export function normalizeRuntime(
  context: RuntimeContext
): RuntimeNormalization {
  const problemId = stringValue(context.metadata, 'id') ?? null;
  const category = stringValue(context.metadata, 'category') ?? 'unknown';
  const status = stringValue(context.metadata, 'status') ?? 'unknown';
  const diagnostics: Diagnostic[] = [];
  if (problemId === null) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_METADATA',
        'metadata.id is required',
        null,
        '"id"'
      )
    );
    return { problemId, category, status, targets: [], diagnostics };
  }
  if (basename(context.problemDirectory) !== problemId) {
    diagnostics.push(
      diagnostic(
        context,
        'ID_DIRECTORY_MISMATCH',
        `metadata.id (${problemId}) must match problem directory`,
        problemId,
        '"id"'
      )
    );
  }
  const runtime = recordValue(context.metadata, 'runtime');
  if (runtime === undefined) {
    const entry = stringValue(context.metadata, 'cfnTemplate');
    if (entry === undefined) {
      diagnostics.push(
        diagnostic(
          context,
          'INVALID_RUNTIME',
          'metadata requires cfnTemplate or runtime',
          problemId,
          '"id"'
        )
      );
      return { problemId, category, status, targets: [], diagnostics };
    }
    return {
      problemId,
      category,
      status,
      targets: [
        {
          targetId: 'default',
          provider: 'aws',
          engine: 'cloudformation',
          entry,
          delivery: 'cloud',
        },
      ],
      diagnostics,
    };
  }
  if (!isRecord(runtime)) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_RUNTIME',
        'runtime must be an object',
        problemId,
        '"runtime"'
      )
    );
    return { problemId, category, status, targets: [], diagnostics };
  }
  const result =
    recordValue(runtime, 'kind') === 'composite'
      ? normalizeCompositeRuntime(context, runtime, problemId)
      : normalizeSingleRuntime(context, runtime, problemId);
  return {
    problemId,
    category,
    status,
    targets: result.targets,
    diagnostics: [...diagnostics, ...result.diagnostics],
  };
}

export function metadataSource(
  metadataPath: string,
  metadataContents: string,
  token: string,
  pointer: string
): SourceLocation {
  return sourceLocation(metadataPath, metadataContents, token, pointer);
}
