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
  it('全 provider の実装 capability を identity 順に一意化する', () => {
    const manifest = simulatorCapabilityManifest();
    expect(
      new Set(manifest.capabilities.map((entry) => entry.provider))
    ).toEqual(new Set(['aws', 'azure', 'gcp', 'sakura']));
    const identities = manifest.capabilities.map((entry) =>
      [entry.provider, entry.service, entry.resourceType, entry.operation].join(
        '|'
      )
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
      ).toBe('L4');
    }
  });

  it('複数 fidelity は最高 level へ要約し重複 identity は拒否する', () => {
    const capability: ProviderCapability = {
      capabilityId: 'fixture.read',
      provider: 'fixture',
      engine: 'declarative',
      service: 'resources',
      resourceType: 'Fixture::Resource',
      operation: 'read',
      fidelity: ['L0', 'L2', 'L1'],
    };
    const manifest = createCapabilityManifest([capability], 'fixture-version');
    expect(manifest.capabilities[0]?.fidelity).toBe('L2');
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
    const printed = await runCapabilityCommand([]);
    expect(JSON.parse(printed.stdout)).toEqual(simulatorCapabilityManifest());

    const directory = await mkdtemp(join(tmpdir(), 'capability-manifest-'));
    const outputPath = join(directory, 'capabilities.json');
    try {
      const written = await runCapabilityCommand(['--output', outputPath]);
      expect(written).toEqual({ exitCode: 0, stdout: '', stderr: '' });
      expect(JSON.parse(await readFile(outputPath, 'utf8'))).toEqual(
        simulatorCapabilityManifest()
      );
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
});
