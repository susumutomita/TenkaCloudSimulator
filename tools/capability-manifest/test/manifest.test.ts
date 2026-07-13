import { describe, expect, it } from 'bun:test';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { ProviderCapability } from '@tenkacloud/simulator-core';
import { runCapabilityCommand } from '../src/command';
import {
  createCapabilityManifest,
  simulatorCapabilityManifest,
} from '../src/index';

describe('Simulator capability manifest', () => {
  const SOURCE_COMMIT = 'a'.repeat(40);

  it('全 provider の実装 capability を identity 順に一意化する', () => {
    const manifest = simulatorCapabilityManifest(SOURCE_COMMIT);
    expect(
      new Set(manifest.capabilities.map((entry) => entry.provider))
    ).toEqual(new Set(['aws', 'azure', 'gcp', 'sakura']));
    const identities = manifest.capabilities.map((entry) =>
      [
        entry.provider,
        entry.engine,
        entry.service,
        entry.resourceType,
        entry.operation,
      ].join('|')
    );
    expect(identities).toEqual(
      [...identities].sort((left, right) => left.localeCompare(right))
    );
    expect(new Set(identities).size).toBe(identities.length);
    for (const provider of ['aws', 'azure', 'gcp', 'sakura']) {
      expect(
        manifest.capabilities.find(
          (entry) =>
            entry.provider === provider &&
            entry.service === 'runtime' &&
            entry.resourceType === 'Runtime::Workload' &&
            entry.operation === 'Materialize'
        )?.fidelity
      ).toEqual(['L4']);
    }
  });

  it('複数 fidelity は canonical set のまま保持し重複 identity は拒否する', () => {
    const capability: ProviderCapability = {
      capabilityId: 'fixture.read',
      provider: 'fixture',
      engine: 'declarative',
      service: 'resources',
      resourceType: 'Fixture::Resource',
      operation: 'read',
      fidelity: ['L0', 'L1', 'L2'],
    };
    const manifest = createCapabilityManifest([capability], 'fixture-version');
    expect(manifest.capabilities[0]?.fidelity).toEqual(['L0', 'L1', 'L2']);
    expect(() =>
      createCapabilityManifest(
        [capability],
        'fixture-version',
        manifest.capabilities
      )
    ).toThrow('duplicate identity');
  });

  it('CLI は stdout、file、usage error を決定的に扱う', async () => {
    const help = await runCapabilityCommand(['--help']);
    expect(help).toMatchObject({ exitCode: 0, stderr: '' });
    expect(help.stdout).toContain('Usage:');
    const invalid = await runCapabilityCommand(['--unknown']);
    expect(invalid).toMatchObject({ exitCode: 2, stdout: '' });
    const missingCommit = await runCapabilityCommand([]);
    expect(missingCommit).toMatchObject({ exitCode: 2, stdout: '' });
    const printed = await runCapabilityCommand([
      '--source-commit',
      SOURCE_COMMIT,
    ]);
    expect(JSON.parse(printed.stdout)).toEqual(
      simulatorCapabilityManifest(SOURCE_COMMIT)
    );

    const directory = await mkdtemp(join(tmpdir(), 'capability-manifest-'));
    const outputPath = join(directory, 'capabilities.json');
    try {
      const written = await runCapabilityCommand([
        '--source-commit',
        SOURCE_COMMIT,
        '--output',
        outputPath,
      ]);
      expect(written).toEqual({ exitCode: 0, stdout: '', stderr: '' });
      expect(JSON.parse(await readFile(outputPath, 'utf8'))).toEqual(
        simulatorCapabilityManifest(SOURCE_COMMIT)
      );
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });

  it('manifest version を source commit に結び付ける', () => {
    expect(simulatorCapabilityManifest(SOURCE_COMMIT).version).toBe(
      `tenkacloud-simulator-0.1.0+git.${SOURCE_COMMIT}`
    );
    expect(() => simulatorCapabilityManifest('main')).toThrow(
      'immutable 40-character Git SHA'
    );
  });
});
