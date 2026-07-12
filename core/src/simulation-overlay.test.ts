import { describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import type { ResolvedTargetSource } from './artifact-bundle';
import type { SingleRuntimeTarget } from './domain';
import { CoreError } from './errors';
import { resolveSimulationOverlay } from './simulation-overlay';

interface IdentifiedTarget extends SingleRuntimeTarget {
  readonly id: string;
}

const CONTENT = 'services:\n  api:\n    image: example\n';
const HASH = createHash('sha256').update(CONTENT, 'utf8').digest('hex');
const TARGETS: readonly IdentifiedTarget[] = [
  {
    id: 'default',
    provider: 'aws',
    engine: 'cloudformation',
    entry: 'template.yaml',
  },
];
const SOURCES: readonly ResolvedTargetSource[] = [
  {
    targetId: 'default',
    templateBody: 'Resources: {}',
    artifacts: [
      { path: 'template.yaml', content: 'Resources: {}' },
      { path: 'services/docker-compose.yml', content: CONTENT },
    ],
  },
];

function captureCoreError(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('CoreError が発生しませんでした');
}

const MATERIALIZE_REQUIREMENT = {
  targetId: 'default',
  service: 'runtime',
  resourceType: 'Runtime::Workload',
  operation: 'Materialize',
  fidelity: 'L4',
  plane: 'workload',
  artifact: { path: 'services/docker-compose.yml', sha256: HASH },
} as const;

const WORKLOAD = {
  id: 'api',
  targetId: 'default',
  resourceRef: 'ApiFunction',
  image: `ghcr.io/tenkacloud/api@sha256:${'a'.repeat(64)}`,
  containerPort: 3000,
  healthPath: '/healthz',
  artifact: { path: 'services/docker-compose.yml', sha256: HASH },
} as const;

describe('simulation overlay の core 境界', () => {
  it('未指定overlayは追加requirementを生成しない', () => {
    expect(resolveSimulationOverlay(undefined, TARGETS, SOURCES)).toEqual({
      requirements: [],
      workloads: [],
    });
  });

  it('providerとengineをtargetから継承しartifact hashを照合してworkloadを重複排除する', () => {
    const overlay = {
      schemaVersion: '1',
      requirements: [MATERIALIZE_REQUIREMENT],
      workloads: [WORKLOAD],
    } as const;

    const resolved = resolveSimulationOverlay(overlay, TARGETS, SOURCES);

    expect(resolved.document).toEqual(overlay);
    expect(resolved.requirements).toEqual([
      {
        provider: 'aws',
        engine: 'cloudformation',
        service: 'runtime',
        resourceType: 'Runtime::Workload',
        operation: 'Materialize',
        fidelity: ['L4'],
        source: { path: 'services/docker-compose.yml' },
      },
    ]);
  });

  it('artifactなしのrequirementとworkloadはoverlay内sourceを保持する', () => {
    const resolved = resolveSimulationOverlay(
      {
        schemaVersion: '1',
        requirements: [
          {
            targetId: 'default',
            service: 'http',
            resourceType: 'HTTP::Endpoint',
            operation: 'Probe',
            fidelity: 'L4',
            plane: 'scoring',
          },
        ],
        workloads: [
          {
            id: 'worker',
            targetId: 'default',
            resourceRef: 'WorkerFunction',
            image: WORKLOAD.image,
            containerPort: 3000,
          },
        ],
      },
      TARGETS,
      SOURCES
    );

    expect(resolved.requirements.map((item) => item.source?.path)).toEqual([
      'simulation-overlay',
      'simulation-overlay#workloads/worker',
    ]);
  });

  it('schema違反、target不一致、artifact欠落とhash不一致を拒否する', () => {
    const invalidValues: readonly unknown[] = [
      { schemaVersion: '1', requirements: [] },
      {
        schemaVersion: '1',
        requirements: [{ ...MATERIALIZE_REQUIREMENT, targetId: 'missing' }],
      },
      {
        schemaVersion: '1',
        workloads: [{ ...WORKLOAD, targetId: 'missing' }],
      },
      {
        schemaVersion: '1',
        requirements: [
          {
            ...MATERIALIZE_REQUIREMENT,
            artifact: { ...MATERIALIZE_REQUIREMENT.artifact, path: 'missing' },
          },
        ],
      },
      {
        schemaVersion: '1',
        requirements: [
          {
            ...MATERIALIZE_REQUIREMENT,
            artifact: {
              ...MATERIALIZE_REQUIREMENT.artifact,
              sha256: 'b'.repeat(64),
            },
          },
        ],
      },
    ];

    for (const value of invalidValues) {
      expect(
        captureCoreError(() =>
          resolveSimulationOverlay(value, TARGETS, SOURCES)
        ).code
      ).toBe('ValidationFailed');
    }
  });

  it('同じcapability requirementとtarget内workload IDの重複を拒否する', () => {
    expect(
      captureCoreError(() =>
        resolveSimulationOverlay(
          {
            schemaVersion: '1',
            requirements: [MATERIALIZE_REQUIREMENT, MATERIALIZE_REQUIREMENT],
          },
          TARGETS,
          SOURCES
        )
      ).message
    ).toContain('requirement');
    expect(
      captureCoreError(() =>
        resolveSimulationOverlay(
          {
            schemaVersion: '1',
            workloads: [WORKLOAD, WORKLOAD],
          },
          TARGETS,
          SOURCES
        )
      ).message
    ).toContain('workload');
  });
});
