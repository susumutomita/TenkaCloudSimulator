import { afterEach, describe, expect, it } from 'bun:test';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { ProviderCapability } from '@tenkacloud/simulator-core';
import { runCapabilityCommand } from '../src/command';
import {
  createCapabilityManifest,
  simulatorCapabilityManifest,
} from '../src/index';

const temporaryDirectories: string[] = [];

async function temporaryDirectory(): Promise<string> {
  const directory = await mkdtemp(join(tmpdir(), 'capability-manifest-'));
  temporaryDirectories.push(directory);
  return directory;
}

async function runGit(
  root: string,
  arguments_: readonly string[]
): Promise<string> {
  const subprocess = Bun.spawn(['git', '-C', root, ...arguments_], {
    stdout: 'pipe',
    stderr: 'pipe',
  });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(subprocess.stdout).text(),
    new Response(subprocess.stderr).text(),
    subprocess.exited,
  ]);
  if (exitCode !== 0) throw new Error(stderr || 'test Git command failed');
  return stdout.trim();
}

async function commitFixture(
  root: string,
  contents = 'first\n'
): Promise<string> {
  await runGit(root, ['init', '--quiet']);
  await writeFile(join(root, 'tracked.txt'), contents, 'utf8');
  await runGit(root, ['add', '.']);
  await runGit(root, [
    '-c',
    'user.name=Capability Manifest Test',
    '-c',
    'user.email=capability-manifest@example.invalid',
    'commit',
    '--quiet',
    '-m',
    'fixture',
  ]);
  return runGit(root, ['rev-parse', 'HEAD']);
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories
      .splice(0)
      .map((directory) => rm(directory, { recursive: true, force: true }))
  );
});

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

  it('CLI は usage error を決定的に扱う', async () => {
    const help = await runCapabilityCommand(['--help']);
    expect(help).toMatchObject({ exitCode: 0, stderr: '' });
    expect(help.stdout).toContain('Usage:');
    const invalid = await runCapabilityCommand(['--unknown']);
    expect(invalid).toMatchObject({ exitCode: 2, stdout: '' });
    const missingCommit = await runCapabilityCommand([]);
    expect(missingCommit).toMatchObject({ exitCode: 2, stdout: '' });
  });

  it('clean な実 Git HEAD と一致するときだけ manifest を書く', async () => {
    const directory = await temporaryDirectory();
    const sourceCommit = await commitFixture(directory);
    const outputPath = join(directory, 'capabilities.json');
    const written = await runCapabilityCommand(
      ['--source-commit', sourceCommit, '--output', outputPath],
      directory
    );
    expect(written).toEqual({ exitCode: 0, stdout: '', stderr: '' });
    expect(JSON.parse(await readFile(outputPath, 'utf8'))).toEqual(
      simulatorCapabilityManifest(sourceCommit)
    );
  });

  it('clean な実 Git HEAD と一致するとき manifest を stdout に返す', async () => {
    const directory = await temporaryDirectory();
    const sourceCommit = await commitFixture(directory);
    const result = await runCapabilityCommand(
      ['--source-commit', sourceCommit],
      directory
    );
    expect(result.exitCode).toBe(0);
    expect(result.stderr).toBe('');
    expect(JSON.parse(result.stdout)).toEqual(
      simulatorCapabilityManifest(sourceCommit)
    );
  });

  it('指定 commit が存在しても HEAD と異なるとき出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    const previousCommit = await commitFixture(directory);
    const outputPath = join(directory, 'must-not-exist.json');
    await writeFile(join(directory, 'tracked.txt'), 'second\n', 'utf8');
    await runGit(directory, ['add', '.']);
    await runGit(directory, [
      '-c',
      'user.name=Capability Manifest Test',
      '-c',
      'user.email=capability-manifest@example.invalid',
      'commit',
      '--quiet',
      '-m',
      'second fixture',
    ]);

    const result = await runCapabilityCommand(
      ['--source-commit', previousCommit, '--output', outputPath],
      directory
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('does not match --source-commit');
    expect(result.stdout).toBe('');
    expect(await Bun.file(outputPath).exists()).toBe(false);
  });

  it('指定 commit が存在しないとき出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    await commitFixture(directory);
    const result = await runCapabilityCommand(
      ['--source-commit', SOURCE_COMMIT],
      directory
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('does not exist');
  });

  it('tracked file が dirty のとき出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    const sourceCommit = await commitFixture(directory);
    await writeFile(join(directory, 'tracked.txt'), 'dirty\n', 'utf8');
    const result = await runCapabilityCommand(
      ['--source-commit', sourceCommit],
      directory
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('checkout must be clean');
  });

  it('index に staged change があるとき出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    const sourceCommit = await commitFixture(directory);
    await writeFile(join(directory, 'tracked.txt'), 'staged\n', 'utf8');
    await runGit(directory, ['add', 'tracked.txt']);
    const result = await runCapabilityCommand(
      ['--source-commit', sourceCommit],
      directory
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('checkout must be clean');
  });

  it('untracked file があるとき出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    const sourceCommit = await commitFixture(directory);
    await writeFile(join(directory, 'untracked.txt'), 'dirty\n', 'utf8');
    const result = await runCapabilityCommand(
      ['--source-commit', sourceCommit],
      directory
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('checkout must be clean');
  });

  it('Git repository ではない root のとき出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    const result = await runCapabilityCommand(
      ['--source-commit', SOURCE_COMMIT],
      directory
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('readable Git checkout');
  });

  it('存在しない repository root のとき出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    const result = await runCapabilityCommand(
      ['--source-commit', SOURCE_COMMIT],
      join(directory, 'missing')
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('readable Git checkout');
  });

  it('Git top level より内側を source anchor にすると出力を拒否する', async () => {
    const directory = await temporaryDirectory();
    const sourceCommit = await commitFixture(directory);
    const nested = join(directory, 'nested');
    await mkdir(nested);
    const result = await runCapabilityCommand(
      ['--source-commit', sourceCommit],
      nested
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('must be the Git top level');
  });

  it('Git status が出力上限を超えると fail closed にする', async () => {
    const directory = await temporaryDirectory();
    const sourceCommit = await commitFixture(directory);
    await Promise.all(
      Array.from({ length: 800 }, async (_, index) => {
        const suffix = `${index}`.padStart(4, '0');
        await writeFile(
          join(directory, `untracked-${suffix}-${'x'.repeat(80)}.txt`),
          'x',
          'utf8'
        );
      })
    );
    const result = await runCapabilityCommand(
      ['--source-commit', sourceCommit],
      directory
    );
    expect(result.exitCode).toBe(1);
    expect(result.stderr).toContain('Git status inspection failed');
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
