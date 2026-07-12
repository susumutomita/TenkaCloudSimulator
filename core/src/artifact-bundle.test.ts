import { describe, expect, it } from 'bun:test';
import { resolveTargetSources } from './artifact-bundle';
import type { SingleRuntimeTarget } from './domain';
import { CoreError } from './errors';

const TARGETS: readonly (SingleRuntimeTarget & { readonly id: string })[] = [
  {
    id: 'aws-main',
    provider: 'aws',
    engine: 'cloudformation',
    entry: 'template.yaml',
  },
  {
    id: 'gcp-edge',
    provider: 'gcp',
    engine: 'infra-manager',
    entry: 'gcp/terraform',
  },
];

const AWS_TARGET = {
  id: 'aws-main',
  provider: 'aws',
  engine: 'cloudformation',
  entry: 'template.yaml',
  artifacts: [{ path: 'template.yaml', content: 'Resources: {}\n' }],
};

const GCP_TARGET = {
  id: 'gcp-edge',
  provider: 'gcp',
  engine: 'infra-manager',
  entry: 'gcp/terraform',
  artifacts: [
    { path: 'gcp/terraform/main.tf', content: 'resource "x" "main" {}\n' },
    { path: 'gcp/terraform/variables.tf', content: 'variable "project" {}\n' },
  ],
};

function artifactBundle(targets: readonly unknown[]): string {
  return JSON.stringify({
    format: 'tenkacloud.simulator.artifacts.v1',
    targets,
  });
}

function validationError(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (error) {
    expect(error).toBeInstanceOf(CoreError);
    return error as CoreError;
  }
  throw new Error('CoreError was not thrown');
}

describe('artifact bundle の target source 解決', () => {
  it('raw source と bundle ではない JSON を後方互換の本文として各 target に渡す', () => {
    for (const body of [
      'Resources: {}\n',
      '{"Resources":{}}',
      '["terraform"]',
      '{invalid-json',
    ]) {
      expect(resolveTargetSources(TARGETS, body)).toEqual(
        TARGETS.map((target) => ({
          targetId: target.id,
          templateBody: body,
          artifacts: [{ path: target.entry, content: body }],
        }))
      );
    }
    expect(validationError(() => resolveTargetSources(TARGETS, '')).code).toBe(
      'ValidationFailed'
    );
    expect(
      validationError(() =>
        resolveTargetSources(
          [
            {
              id: 'unsafe',
              provider: 'aws',
              engine: 'cloudformation',
              entry: '../template.yaml',
            },
          ],
          '{}'
        )
      ).message
    ).toContain('traversal');
  });

  it('Composite bundle から file entry と directory main.tf を分離して返す', () => {
    expect(
      resolveTargetSources(TARGETS, artifactBundle([AWS_TARGET, GCP_TARGET]))
    ).toEqual([
      {
        targetId: 'aws-main',
        templateBody: 'Resources: {}\n',
        artifacts: AWS_TARGET.artifacts,
      },
      {
        targetId: 'gcp-edge',
        templateBody: 'resource "x" "main" {}\n',
        artifacts: GCP_TARGET.artifacts,
      },
    ]);
  });

  it('unknown version、余剰 field、target 件数と型の不一致を拒否する', () => {
    const invalidBodies = [
      JSON.stringify({ format: 'tenkacloud.simulator.artifacts.v2' }),
      JSON.stringify({
        format: 'tenkacloud.simulator.artifacts.v1',
        targets: [AWS_TARGET, GCP_TARGET],
        extra: true,
      }),
      JSON.stringify({
        format: 'tenkacloud.simulator.artifacts.v1',
        targets: 'invalid',
      }),
      artifactBundle([]),
      artifactBundle([AWS_TARGET, null]),
    ];
    for (const body of invalidBodies) {
      expect(
        validationError(() => resolveTargetSources(TARGETS, body)).code
      ).toBe('ValidationFailed');
    }
  });

  it('target identity、field、canonical order、重複を runtime と照合する', () => {
    const invalidTargets: readonly (readonly unknown[])[] = [
      [GCP_TARGET, AWS_TARGET],
      [AWS_TARGET, { ...GCP_TARGET, id: 'aws-main' }],
      [AWS_TARGET, { ...GCP_TARGET, id: 'unknown-target' }],
      [AWS_TARGET, { ...GCP_TARGET, provider: 'azure' }],
      [AWS_TARGET, { ...GCP_TARGET, engine: 'terraform' }],
      [AWS_TARGET, { ...GCP_TARGET, entry: 'gcp/other' }],
      [AWS_TARGET, { ...GCP_TARGET, id: '' }],
      [AWS_TARGET, { ...GCP_TARGET, provider: '' }],
      [AWS_TARGET, { ...GCP_TARGET, engine: '' }],
      [AWS_TARGET, { ...GCP_TARGET, entry: '' }],
      [AWS_TARGET, { ...GCP_TARGET, extra: true }],
    ];
    for (const targets of invalidTargets) {
      expect(
        validationError(() =>
          resolveTargetSources(TARGETS, artifactBundle(targets))
        ).code
      ).toBe('ValidationFailed');
    }
  });

  it('artifact の型、件数、field、content、path 境界を拒否する', () => {
    const invalidArtifacts: readonly unknown[] = [
      'invalid',
      [],
      Array.from({ length: 257 }, (_, index) => ({
        path: `gcp/terraform/file-${String(index).padStart(3, '0')}.tf`,
        content: 'x',
      })),
      [null],
      [{ path: 'template.yaml', content: 'x', extra: true }],
      [{ path: 'template.yaml', content: '' }],
      [{ path: '', content: 'x' }],
      [{ path: '/template.yaml', content: 'x' }],
      [{ path: 'C:/template.yaml', content: 'x' }],
      [{ path: 'dir\\template.yaml', content: 'x' }],
      [{ path: 'dir\0template.yaml', content: 'x' }],
      [{ path: 'dir//template.yaml', content: 'x' }],
      [{ path: 'dir/./template.yaml', content: 'x' }],
      [{ path: 'dir/../template.yaml', content: 'x' }],
      [{ path: `${'x'.repeat(1025)}.yaml`, content: 'x' }],
    ];
    for (const artifacts of invalidArtifacts) {
      const target = { ...AWS_TARGET, artifacts };
      expect(
        validationError(() =>
          resolveTargetSources(TARGETS, artifactBundle([target, GCP_TARGET]))
        ).code
      ).toBe('ValidationFailed');
    }
  });

  it('artifact path の順序・重複と directory entrypoint 境界を拒否する', () => {
    const invalidGcpTargets = [
      {
        ...GCP_TARGET,
        artifacts: [...GCP_TARGET.artifacts].reverse(),
      },
      {
        ...GCP_TARGET,
        artifacts: [GCP_TARGET.artifacts[0], GCP_TARGET.artifacts[0]],
      },
      {
        ...GCP_TARGET,
        artifacts: [
          { path: 'gcp/other.tf', content: 'x' },
          GCP_TARGET.artifacts[0],
        ],
      },
      {
        ...GCP_TARGET,
        artifacts: [{ path: 'gcp/terraform/variables.tf', content: 'x' }],
      },
    ];
    for (const target of invalidGcpTargets) {
      expect(
        validationError(() =>
          resolveTargetSources(TARGETS, artifactBundle([AWS_TARGET, target]))
        ).code
      ).toBe('ValidationFailed');
    }
  });
});
