import { afterEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import {
  type OverlayScanContext,
  scanSimulationOverlay,
} from '../src/overlay.ts';

const temporaryDirectories: string[] = [];
const REFERENCE = { schemaVersion: '1', entry: 'simulation.json' };

interface Fixture {
  readonly context: OverlayScanContext;
  readonly problemDirectory: string;
}

async function writeBytes(
  root: string,
  path: string,
  contents: string | Uint8Array
): Promise<void> {
  const absolute = join(root, path);
  await mkdir(dirname(absolute), { recursive: true });
  await writeFile(absolute, contents);
}

async function fixture(
  reference: unknown,
  overlay?: string | Uint8Array
): Promise<Fixture> {
  const root = await mkdtemp(join(tmpdir(), 'tenkacloud-overlay-scan-'));
  temporaryDirectories.push(root);
  const problemDirectory = join(root, 'challenges', 'overlay-case');
  const metadata = {
    id: 'overlay-case',
    category: 'Challenge',
    status: 'ready',
    cfnTemplate: 'template.yaml',
    ...(reference === undefined ? {} : { simulationOverlay: reference }),
  };
  const metadataContents = `${JSON.stringify(metadata, null, 2)}\n`;
  const metadataPath = 'challenges/overlay-case/metadata.json';
  await writeBytes(root, metadataPath, metadataContents);
  if (overlay !== undefined) {
    await writeBytes(root, 'challenges/overlay-case/simulation.json', overlay);
  }
  return {
    problemDirectory,
    context: {
      catalogRoot: root,
      problemDirectory,
      problemId: 'overlay-case',
      metadata,
      metadataContents,
      metadataPath,
      targets: [
        {
          targetId: 'default',
          provider: 'aws',
          engine: 'cloudformation',
          entry: 'template.yaml',
          delivery: 'cloud',
        },
      ],
    },
  };
}

function requirement(
  operation: string,
  overrides: Readonly<Record<string, unknown>> = {}
): Record<string, unknown> {
  return {
    targetId: 'default',
    service: 'runtime',
    resourceType: 'Runtime::Endpoint',
    operation,
    fidelity: 'L1',
    plane: 'scoring',
    ...overrides,
  };
}

function overlay(value: Readonly<Record<string, unknown>>): string {
  return `${JSON.stringify({ schemaVersion: '1', ...value }, null, 2)}\n`;
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories
      .splice(0)
      .map((directory) => rm(directory, { recursive: true, force: true }))
  );
});

describe('simulation overlay の参照と JSON boundary', () => {
  it('未参照、壊れた参照、欠損 file、UTF-8、JSON、root shape を個別に invalid にする', async () => {
    const unreferenced = await fixture(
      undefined,
      overlay({ requirements: [requirement('Resolve')] })
    );
    expect(
      (await scanSimulationOverlay(unreferenced.context)).diagnostics[0]
        ?.message
    ).toContain('does not reference');

    for (const reference of [
      null,
      { schemaVersion: '2', entry: 'simulation.json' },
      { schemaVersion: '1', entry: 'other.json' },
      { ...REFERENCE, extra: true },
    ]) {
      const invalid = await fixture(reference);
      expect(
        (await scanSimulationOverlay(invalid.context)).diagnostics[0]?.message
      ).toContain('reference must select');
    }

    const missing = await fixture(REFERENCE);
    expect(
      (await scanSimulationOverlay(missing.context)).diagnostics[0]?.message
    ).toContain('missing');

    const invalidUtf8 = await fixture(REFERENCE, new Uint8Array([0xc3, 0x28]));
    expect(
      (await scanSimulationOverlay(invalidUtf8.context)).diagnostics[0]?.message
    ).toContain('UTF-8');

    const invalidJson = await fixture(REFERENCE, '{invalid');
    expect(
      (await scanSimulationOverlay(invalidJson.context)).diagnostics[0]?.message
    ).toContain('JSON is invalid');

    const invalidRoots: readonly unknown[] = [
      null,
      [],
      { schemaVersion: '2', requirements: [requirement('A')] },
      { schemaVersion: '1' },
      { schemaVersion: '1', requirements: [requirement('B')], extra: true },
      { $schema: 1, schemaVersion: '1', requirements: [requirement('C')] },
    ];
    for (const root of invalidRoots) {
      const invalid = await fixture(REFERENCE, `${JSON.stringify(root)}\n`);
      expect(
        (await scanSimulationOverlay(invalid.context)).diagnostics[0]?.message
      ).toContain('root is invalid');
    }
  });
});

describe('simulation overlay requirement と artifact boundary', () => {
  it('配列、field、target、重複、path、symlink、regular file、hash を検証する', async () => {
    const invalidArray = await fixture(
      REFERENCE,
      overlay({ requirements: [] })
    );
    expect(
      (await scanSimulationOverlay(invalidArray.context)).diagnostics[0]
        ?.message
    ).toContain('1 to 128');

    const artifact = 'real behavior\n';
    const artifactHash = createHash('sha256').update(artifact).digest('hex');
    const testFixture = await fixture(REFERENCE);
    await writeBytes(
      testFixture.problemDirectory,
      'real/behavior.txt',
      artifact
    );
    await mkdir(join(testFixture.problemDirectory, 'artifact-directory'));
    await symlink(
      join(testFixture.problemDirectory, 'real', 'behavior.txt'),
      join(testFixture.problemDirectory, 'linked-artifact')
    );
    const requirements: readonly unknown[] = [
      null,
      { ...requirement('UnknownField'), extra: true },
      requirement('BadFields', { service: 'Bad Service' }),
      requirement('UnknownTarget', { targetId: 'missing' }),
      requirement('Duplicate'),
      requirement('Duplicate', { fidelity: 'L4' }),
      requirement('BadArtifact', { artifact: null }),
      requirement('MissingArtifact', {
        artifact: { path: 'missing.txt', sha256: artifactHash },
      }),
      requirement('UnsafeArtifact', {
        artifact: { path: '../outside.txt', sha256: artifactHash },
      }),
      requirement('LinkedArtifact', {
        artifact: { path: 'linked-artifact', sha256: artifactHash },
      }),
      requirement('DirectoryArtifact', {
        artifact: { path: 'artifact-directory', sha256: artifactHash },
      }),
      requirement('StaleArtifact', {
        artifact: { path: 'real/behavior.txt', sha256: '0'.repeat(64) },
      }),
    ];
    await writeBytes(
      testFixture.problemDirectory,
      'simulation.json',
      overlay({ requirements })
    );

    const result = await scanSimulationOverlay(testFixture.context);
    const messages = result.diagnostics
      .map((diagnostic) => diagnostic.message)
      .join('\n');
    expect(messages).toContain('unknown or missing fields');
    expect(messages).toContain('invalid, duplicated, or targets an unknown');
    expect(messages).toContain('lowercase SHA-256');
    expect(messages).toContain('missing');
    expect(messages).toContain('safe relative path');
    expect(messages).toContain('symbolic link');
    expect(messages).toContain('regular file');
    expect(messages).toContain('sha256 does not match');
  });
});

describe('simulation overlay workload boundary', () => {
  it('配列、shape、runtime target、ID 重複を検証し明示 requirement は重複生成しない', async () => {
    const invalidArray = await fixture(
      REFERENCE,
      overlay({
        requirements: [requirement('Resolve')],
        workloads: [],
      })
    );
    expect(
      (await scanSimulationOverlay(invalidArray.context)).diagnostics[0]
        ?.message
    ).toContain('1 to 32');

    const materialize = requirement('Materialize', {
      resourceType: 'Runtime::Workload',
      fidelity: 'L4',
      plane: 'workload',
    });
    const valid = {
      id: 'api',
      targetId: 'default',
      resourceRef: 'Api',
      image: `registry.example/api@sha256:${'a'.repeat(64)}`,
      containerPort: 8080,
    };
    const workloadFixture = await fixture(
      REFERENCE,
      overlay({
        requirements: [materialize],
        workloads: [
          valid,
          { ...valid },
          { ...valid, id: 'other', targetId: 'missing' },
          { ...valid, id: 'extra', extra: true },
          { ...valid, id: 'bad-image', image: 'registry.example/api:latest' },
          null,
        ],
      })
    );
    const result = await scanSimulationOverlay(workloadFixture.context);

    expect(
      result.requirements.filter((item) => item.operation === 'Materialize')
    ).toHaveLength(1);
    expect(result.diagnostics).toHaveLength(5);
    expect(result.diagnostics.every((item) => item.targetId !== 'secret')).toBe(
      true
    );
  });
});
