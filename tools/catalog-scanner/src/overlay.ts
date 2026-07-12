import { createHash } from 'node:crypto';
import { lstat, readFile } from 'node:fs/promises';
import { join } from 'node:path';
import type {
  Diagnostic,
  Fidelity,
  NormalizedTarget,
  Plane,
  Requirement,
} from './model.ts';
import { createRequirement } from './requirements.ts';
import {
  portableRelativePath,
  resolveProblemEntry,
  type SourceDigest,
  sourceLocation,
} from './source.ts';
import {
  isFidelity,
  isRecord,
  recordValue,
  stringValue,
  unexpectedKeys,
} from './value.ts';

const OVERLAY_FILE = 'simulation.json';
const PLANES = new Set<Plane>([
  'deploy',
  'participant',
  'workload',
  'scoring',
  'operator',
  'access',
]);
const SHA256 = /^[a-f0-9]{64}$/;
const WORKLOAD_KEYS = [
  'id',
  'targetId',
  'resourceRef',
  'image',
  'command',
  'containerPort',
  'healthPath',
  'artifact',
] as const;

export interface OverlayScanContext {
  readonly catalogRoot: string;
  readonly problemDirectory: string;
  readonly problemId: string;
  readonly metadata: Record<string, unknown>;
  readonly metadataContents: string;
  readonly metadataPath: string;
  readonly targets: readonly NormalizedTarget[];
}

export interface OverlayScanResult {
  readonly requirements: readonly Requirement[];
  readonly diagnostics: readonly Diagnostic[];
  readonly sources: readonly SourceDigest[];
}

function diagnostic(
  context: OverlayScanContext,
  message: string,
  path = context.metadataPath,
  contents = context.metadataContents,
  pointer: string | null = '/simulationOverlay',
  targetId: string | null = null
): Diagnostic {
  return {
    code: 'INVALID_SIMULATION_OVERLAY',
    message,
    problemId: context.problemId,
    targetId,
    source: sourceLocation(path, contents, '"simulationOverlay"', pointer),
  };
}

function exactKeys(
  value: Record<string, unknown>,
  allowed: readonly string[]
): boolean {
  return unexpectedKeys(value, allowed).length === 0;
}

function isPlane(value: unknown): value is Plane {
  return typeof value === 'string' && PLANES.has(value as Plane);
}

function validRelativePath(value: string): boolean {
  return (
    value.length > 0 &&
    value.length <= 256 &&
    !value.includes('\0') &&
    value
      .split('/')
      .every((part) => Boolean(part) && part !== '.' && part !== '..')
  );
}

async function safeFile(
  context: OverlayScanContext,
  relativePath: string
): Promise<
  | { readonly status: 'ok'; readonly path: string; readonly bytes: Uint8Array }
  | { readonly status: 'invalid'; readonly message: string }
> {
  if (!validRelativePath(relativePath)) {
    return {
      status: 'invalid',
      message: `${relativePath} is not a safe relative path`,
    };
  }
  const resolved = await resolveProblemEntry(
    context.problemDirectory,
    relativePath
  );
  if (resolved.status !== 'ok') {
    return {
      status: 'invalid',
      message: `${relativePath} is ${resolved.status === 'outside' ? 'outside the problem directory' : 'missing'}`,
    };
  }
  let current = context.problemDirectory;
  for (const segment of relativePath.split('/')) {
    current = join(current, segment);
    if ((await lstat(current)).isSymbolicLink()) {
      return {
        status: 'invalid',
        message: `${relativePath} must not contain a symbolic link`,
      };
    }
  }
  const final = await lstat(resolved.path);
  if (!final.isFile()) {
    return {
      status: 'invalid',
      message: `${relativePath} must be a regular file`,
    };
  }
  return {
    status: 'ok',
    path: resolved.path,
    bytes: await readFile(resolved.path),
  };
}

function digest(path: string, bytes: Uint8Array): SourceDigest {
  return {
    path,
    digest: createHash('sha256').update(bytes).digest('hex'),
  };
}

async function artifactDiagnostics(
  context: OverlayScanContext,
  value: unknown,
  label: string,
  overlayPath: string,
  overlayContents: string,
  sources: Map<string, SourceDigest>
): Promise<Diagnostic[]> {
  if (value === undefined) return [];
  if (
    !isRecord(value) ||
    !exactKeys(value, ['path', 'sha256']) ||
    typeof value['path'] !== 'string' ||
    typeof value['sha256'] !== 'string' ||
    !SHA256.test(value['sha256'])
  ) {
    return [
      diagnostic(
        context,
        `${label} must contain only path and lowercase SHA-256`,
        overlayPath,
        overlayContents,
        label
      ),
    ];
  }
  const file = await safeFile(context, value['path']);
  if (file.status === 'invalid') {
    return [
      diagnostic(
        context,
        `${label}: ${file.message}`,
        overlayPath,
        overlayContents,
        label
      ),
    ];
  }
  const sourcePath = portableRelativePath(context.catalogRoot, file.path);
  const sourceDigest = digest(sourcePath, file.bytes);
  sources.set(sourcePath, sourceDigest);
  if (sourceDigest.digest !== value['sha256']) {
    return [
      diagnostic(
        context,
        `${label}.sha256 does not match ${value['path']}`,
        overlayPath,
        overlayContents,
        label
      ),
    ];
  }
  return [];
}

function requirementIdentity(value: Record<string, unknown>): string {
  return [
    value['targetId'],
    value['service'],
    value['resourceType'],
    value['operation'],
    value['plane'],
  ].join('|');
}

function requirementFields(value: Record<string, unknown>):
  | {
      readonly targetId: string;
      readonly service: string;
      readonly resourceType: string;
      readonly operation: string;
      readonly fidelity: Fidelity;
      readonly plane: Plane;
    }
  | undefined {
  const targetId = stringValue(value, 'targetId');
  const service = stringValue(value, 'service');
  const resourceType = stringValue(value, 'resourceType');
  const operation = stringValue(value, 'operation');
  const fidelity = recordValue(value, 'fidelity');
  const plane = recordValue(value, 'plane');
  if (
    !targetId ||
    !service ||
    !resourceType ||
    !operation ||
    !isFidelity(fidelity) ||
    !isPlane(plane) ||
    !/^[a-z][a-z0-9.-]{0,63}$/.test(service) ||
    !/^[A-Za-z][A-Za-z0-9.:_-]{0,127}$/.test(operation) ||
    resourceType.length > 256
  ) {
    return undefined;
  }
  return { targetId, service, resourceType, operation, fidelity, plane };
}

async function overlayRequirements(
  context: OverlayScanContext,
  values: unknown,
  overlayPath: string,
  overlayContents: string,
  sources: Map<string, SourceDigest>,
  diagnostics: Diagnostic[]
): Promise<{ requirements: Requirement[]; identities: Set<string> }> {
  if (values === undefined) return { requirements: [], identities: new Set() };
  if (!Array.isArray(values) || values.length < 1 || values.length > 128) {
    diagnostics.push(
      diagnostic(
        context,
        'overlay requirements must contain 1 to 128 entries',
        overlayPath,
        overlayContents,
        '/requirements'
      )
    );
    return { requirements: [], identities: new Set() };
  }
  const requirements: Requirement[] = [];
  const identities = new Set<string>();
  const targets = new Map(
    context.targets.map((target) => [target.targetId, target])
  );
  for (const [index, value] of values.entries()) {
    const pointer = `/requirements/${index}`;
    if (
      !isRecord(value) ||
      !exactKeys(value, [
        'targetId',
        'service',
        'resourceType',
        'operation',
        'fidelity',
        'plane',
        'artifact',
      ])
    ) {
      diagnostics.push(
        diagnostic(
          context,
          `overlay requirement ${index} has unknown or missing fields`,
          overlayPath,
          overlayContents,
          pointer
        )
      );
      continue;
    }
    const fields = requirementFields(value);
    const target = fields ? targets.get(fields.targetId) : undefined;
    const identity = requirementIdentity(value);
    if (!fields || !target || identities.has(identity)) {
      diagnostics.push(
        diagnostic(
          context,
          `overlay requirement ${index} is invalid, duplicated, or targets an unknown runtime`,
          overlayPath,
          overlayContents,
          pointer,
          fields?.targetId ?? null
        )
      );
      continue;
    }
    identities.add(identity);
    diagnostics.push(
      ...(await artifactDiagnostics(
        context,
        value['artifact'],
        `${pointer}/artifact`,
        overlayPath,
        overlayContents,
        sources
      ))
    );
    requirements.push(
      createRequirement({
        problemId: context.problemId,
        targetId: fields.targetId,
        provider: target.provider,
        engine: target.engine,
        service: fields.service,
        resourceType: fields.resourceType,
        operation: fields.operation,
        fidelity: fields.fidelity,
        plane: fields.plane,
        origin: 'simulation-overlay',
        classification: 'binding',
        source: sourceLocation(
          overlayPath,
          overlayContents,
          '"targetId"',
          pointer
        ),
      })
    );
  }
  return { requirements, identities };
}

function validWorkload(value: Record<string, unknown>): boolean {
  const command = value['command'];
  const healthPath = value['healthPath'];
  return (
    Boolean(stringValue(value, 'id')) &&
    Boolean(stringValue(value, 'targetId')) &&
    Boolean(stringValue(value, 'resourceRef')) &&
    typeof value['image'] === 'string' &&
    /@sha256:[a-f0-9]{64}$/.test(value['image']) &&
    Number.isInteger(value['containerPort']) &&
    Number(value['containerPort']) >= 1024 &&
    Number(value['containerPort']) <= 65_535 &&
    (command === undefined ||
      (Array.isArray(command) &&
        command.length >= 1 &&
        command.length <= 32 &&
        command.every(
          (part) =>
            typeof part === 'string' && part.length > 0 && !part.includes('\0')
        ))) &&
    (healthPath === undefined ||
      (typeof healthPath === 'string' && /^\/[^?#\s]*$/.test(healthPath)))
  );
}

function workloadEntry(
  value: unknown,
  ids: ReadonlySet<string>,
  targets: ReadonlyMap<string, NormalizedTarget>
):
  | {
      readonly value: Record<string, unknown>;
      readonly id: string;
      readonly targetId: string;
      readonly target: NormalizedTarget;
    }
  | undefined {
  if (
    !isRecord(value) ||
    !exactKeys(value, WORKLOAD_KEYS) ||
    !validWorkload(value)
  ) {
    return undefined;
  }
  const id = stringValue(value, 'id');
  const targetId = stringValue(value, 'targetId');
  const target = targetId ? targets.get(targetId) : undefined;
  if (!id || !targetId || ids.has(id) || !target) return undefined;
  return { value, id, targetId, target };
}

async function overlayWorkloads(
  context: OverlayScanContext,
  values: unknown,
  identities: Set<string>,
  overlayPath: string,
  overlayContents: string,
  sources: Map<string, SourceDigest>,
  diagnostics: Diagnostic[]
): Promise<Requirement[]> {
  if (values === undefined) return [];
  if (!Array.isArray(values) || values.length < 1 || values.length > 32) {
    diagnostics.push(
      diagnostic(
        context,
        'overlay workloads must contain 1 to 32 entries',
        overlayPath,
        overlayContents,
        '/workloads'
      )
    );
    return [];
  }
  const targets = new Map(
    context.targets.map((target) => [target.targetId, target])
  );
  const ids = new Set<string>();
  const requirements: Requirement[] = [];
  for (const [index, value] of values.entries()) {
    const pointer = `/workloads/${index}`;
    const entry = workloadEntry(value, ids, targets);
    if (!entry) {
      diagnostics.push(
        diagnostic(
          context,
          `overlay workload ${index} is invalid, duplicated, or targets an unknown runtime`,
          overlayPath,
          overlayContents,
          pointer,
          isRecord(value) ? (stringValue(value, 'targetId') ?? null) : null
        )
      );
      continue;
    }
    const { id, target, targetId, value: workload } = entry;
    ids.add(id);
    diagnostics.push(
      ...(await artifactDiagnostics(
        context,
        workload['artifact'],
        `${pointer}/artifact`,
        overlayPath,
        overlayContents,
        sources
      ))
    );
    const identity = `${targetId}|runtime|Runtime::Workload|Materialize|workload`;
    if (!identities.has(identity)) {
      identities.add(identity);
      requirements.push(
        createRequirement({
          problemId: context.problemId,
          targetId,
          provider: target.provider,
          engine: target.engine,
          service: 'runtime',
          resourceType: 'Runtime::Workload',
          operation: 'Materialize',
          fidelity: 'L4',
          plane: 'workload',
          origin: 'simulation-overlay',
          classification: 'binding',
          source: sourceLocation(overlayPath, overlayContents, '"id"', pointer),
        })
      );
    }
  }
  return requirements;
}

export async function scanSimulationOverlay(
  context: OverlayScanContext
): Promise<OverlayScanResult> {
  const reference = recordValue(context.metadata, 'simulationOverlay');
  const conventional = await lstat(
    join(context.problemDirectory, OVERLAY_FILE)
  ).catch(() => undefined);
  if (reference === undefined) {
    return conventional
      ? {
          requirements: [],
          diagnostics: [
            diagnostic(
              context,
              `${OVERLAY_FILE} exists but metadata does not reference it`
            ),
          ],
          sources: [],
        }
      : { requirements: [], diagnostics: [], sources: [] };
  }
  if (
    !isRecord(reference) ||
    !exactKeys(reference, ['schemaVersion', 'entry']) ||
    reference['schemaVersion'] !== '1' ||
    reference['entry'] !== OVERLAY_FILE
  ) {
    return {
      requirements: [],
      diagnostics: [
        diagnostic(
          context,
          'simulationOverlay reference must select simulation.json schema version 1'
        ),
      ],
      sources: [],
    };
  }
  const file = await safeFile(context, OVERLAY_FILE);
  if (file.status === 'invalid') {
    return {
      requirements: [],
      diagnostics: [diagnostic(context, file.message)],
      sources: [],
    };
  }
  const overlayPath = portableRelativePath(context.catalogRoot, file.path);
  let overlayContents: string;
  try {
    overlayContents = new TextDecoder('utf-8', { fatal: true }).decode(
      file.bytes
    );
  } catch {
    return {
      requirements: [],
      diagnostics: [
        diagnostic(context, 'simulation overlay must be UTF-8 JSON'),
      ],
      sources: [digest(overlayPath, file.bytes)],
    };
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(overlayContents);
  } catch {
    return {
      requirements: [],
      diagnostics: [
        diagnostic(
          context,
          'simulation overlay JSON is invalid',
          overlayPath,
          overlayContents
        ),
      ],
      sources: [digest(overlayPath, file.bytes)],
    };
  }
  const sources = new Map<string, SourceDigest>([
    [overlayPath, digest(overlayPath, file.bytes)],
  ]);
  if (
    !isRecord(parsed) ||
    !exactKeys(parsed, [
      '$schema',
      'schemaVersion',
      'requirements',
      'workloads',
    ]) ||
    parsed['schemaVersion'] !== '1' ||
    (parsed['requirements'] === undefined &&
      parsed['workloads'] === undefined) ||
    (parsed['$schema'] !== undefined && typeof parsed['$schema'] !== 'string')
  ) {
    return {
      requirements: [],
      diagnostics: [
        diagnostic(
          context,
          'simulation overlay root is invalid',
          overlayPath,
          overlayContents
        ),
      ],
      sources: [...sources.values()],
    };
  }
  const diagnostics: Diagnostic[] = [];
  const requirementScan = await overlayRequirements(
    context,
    parsed['requirements'],
    overlayPath,
    overlayContents,
    sources,
    diagnostics
  );
  const workloads = await overlayWorkloads(
    context,
    parsed['workloads'],
    requirementScan.identities,
    overlayPath,
    overlayContents,
    sources,
    diagnostics
  );
  return {
    requirements: [...requirementScan.requirements, ...workloads],
    diagnostics,
    sources: [...sources.values()],
  };
}
