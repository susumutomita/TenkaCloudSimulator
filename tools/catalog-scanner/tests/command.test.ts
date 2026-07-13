import { afterEach, describe, expect, it } from 'bun:test';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { isCapabilityCoverageReport } from '@tenkacloud/simulator-contracts';
import { runCommand } from '../src/command.ts';

const temporaryDirectories: string[] = [];

async function temporaryDirectory(): Promise<string> {
  const directory = await mkdtemp(
    join(tmpdir(), 'tenkacloud-catalog-command-')
  );
  temporaryDirectories.push(directory);
  return directory;
}

async function writeText(
  root: string,
  path: string,
  contents: string
): Promise<void> {
  const absolutePath = join(root, path);
  await mkdir(dirname(absolutePath), { recursive: true });
  await writeFile(absolutePath, contents, 'utf8');
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

async function commitCatalog(catalog: string): Promise<string> {
  await runGit(catalog, ['init', '--quiet']);
  await runGit(catalog, ['add', '.']);
  await runGit(catalog, [
    '-c',
    'user.name=Catalog Scanner Test',
    '-c',
    'user.email=catalog-scanner@example.invalid',
    'commit',
    '--quiet',
    '--allow-empty',
    '-m',
    'catalog fixture',
  ]);
  return runGit(catalog, ['rev-parse', 'HEAD']);
}

function reportArguments(
  catalogCommit: string,
  simulatorVersion: string
): readonly string[] {
  return [
    '--catalog-commit',
    catalogCommit,
    '--simulator-version',
    simulatorVersion,
  ];
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories
      .splice(0)
      .map((directory) => rm(directory, { recursive: true, force: true }))
  );
});

describe('catalog scanner CLI command を実行するとき', () => {
  it('coverage 不足 report を file に書き終了コード 1 を返す', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    const output = join(root, 'coverage.json');
    await writeText(
      catalog,
      'challenges/minimal/metadata.json',
      `${JSON.stringify({
        id: 'minimal',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await writeText(
      catalog,
      'challenges/minimal/template.yaml',
      'Resources:\n  Parameter:\n    Type: AWS::SSM::Parameter\n'
    );
    await writeFile(
      manifest,
      `${JSON.stringify({ schemaVersion: '1', version: 'empty', capabilities: [] })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);

    const result = await runCommand([
      '--catalog',
      catalog,
      ...reportArguments(catalogCommit, 'empty'),
      '--capabilities',
      manifest,
      '--output',
      output,
    ]);

    expect(result.exitCode).toBe(1);
    expect(result.stdout).toBe('');
    expect(result.stderr).toContain('missing=1');
    expect(JSON.parse(await readFile(output, 'utf8'))).toEqual(
      expect.objectContaining({ supported: false })
    );
  });

  it('output 未指定なら covered report を標準出力文字列として返す', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    await writeText(
      catalog,
      'challenges/local/metadata.json',
      `${JSON.stringify({
        id: 'local',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          provider: 'docker',
          engine: 'compose',
          entry: 'local/docker-compose.yml',
        },
      })}\n`
    );
    await writeText(
      catalog,
      'challenges/local/local/docker-compose.yml',
      'services: {}\n'
    );
    await writeFile(
      manifest,
      `${JSON.stringify({ schemaVersion: '1', version: 'empty', capabilities: [] })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);

    const result = await runCommand([
      '--catalog',
      catalog,
      ...reportArguments(catalogCommit, 'empty'),
      '--capabilities',
      manifest,
    ]);

    expect(result.exitCode).toBe(0);
    expect(isCapabilityCoverageReport(JSON.parse(result.stdout))).toBe(true);
    expect(JSON.parse(result.stdout)).toEqual(
      expect.objectContaining({ supported: true })
    );
    expect(result.stderr).toContain('covered=0');
  });

  it('IAM authorization 不足だけなら inventory を残して終了コード 0 を返す', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    await writeText(
      catalog,
      'challenges/authorization-only/metadata.json',
      `${JSON.stringify({
        id: 'authorization-only',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await writeText(
      catalog,
      'challenges/authorization-only/template.yaml',
      `Resources:
  ParticipantViewerRole:
    Type: AWS::IAM::Role
    Properties:
      Policies:
        - PolicyName: OptionalSession
          PolicyDocument:
            Statement:
              - Effect: Allow
                Action: ssm:StartSession
                Resource: "*"
`
    );
    await writeFile(
      manifest,
      `${JSON.stringify({
        schemaVersion: '1',
        version: 'binding-only',
        capabilities: [
          {
            provider: 'aws',
            engine: 'cloudformation',
            service: 'iam',
            resourceType: 'AWS::IAM::Role',
            operation: 'lifecycle',
            fidelity: ['L2'],
          },
        ],
      })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);

    const result = await runCommand([
      '--catalog',
      catalog,
      ...reportArguments(catalogCommit, 'binding-only'),
      '--capabilities',
      manifest,
    ]);
    const report = JSON.parse(result.stdout);

    expect(result.exitCode).toBe(0);
    expect(result.stderr).toContain('missing=0');
    expect(result.stderr).toContain('authorization-missing=1');
    expect(report.supported).toBe(true);
    expect(report.inventory.authorizationInventory.summary.missing).toBe(1);
  });

  it('help、必須引数不足、未知引数を明示する', async () => {
    const help = await runCommand(['--help']);
    expect(help).toEqual(
      expect.objectContaining({
        exitCode: 0,
        stdout: expect.stringContaining('--catalog'),
      })
    );

    const missing = await runCommand([]);
    expect(missing.exitCode).toBe(2);
    expect(missing.stderr).toContain(
      '--catalog, --catalog-commit, --capabilities, and --simulator-version are required'
    );

    const unknown = await runCommand(['--unknown']);
    expect(unknown.exitCode).toBe(2);
    expect(unknown.stderr).toContain('unknown argument');
  });

  it('manifest 読み取り失敗を終了コード 2 で返す', async () => {
    const root = await temporaryDirectory();
    const catalogCommit = await commitCatalog(root);
    const result = await runCommand([
      '--catalog',
      root,
      ...reportArguments(catalogCommit, 'missing'),
      '--capabilities',
      join(root, 'missing.json'),
    ]);
    expect(result.exitCode).toBe(2);
    expect(result.stderr).toContain('capability manifest');
  });

  it('catalog commit mismatch、dirty scope、non-Git catalogを拒否する', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    await writeText(catalog, 'README.md', 'clean catalog\n');
    await writeFile(
      manifest,
      `${JSON.stringify({ schemaVersion: '1', version: 'fixture-version', capabilities: [] })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);
    const baseArguments = [
      '--catalog',
      catalog,
      '--capabilities',
      manifest,
      '--simulator-version',
      'fixture-version',
    ];

    const mismatch = await runCommand([
      ...baseArguments,
      '--catalog-commit',
      'f'.repeat(40),
    ]);
    expect(mismatch.exitCode).toBe(2);
    expect(mismatch.stderr).toContain('does not match --catalog-commit');

    await writeText(catalog, 'uncommitted.txt', 'dirty\n');
    const dirty = await runCommand([
      ...baseArguments,
      '--catalog-commit',
      catalogCommit,
    ]);
    expect(dirty.exitCode).toBe(2);
    expect(dirty.stderr).toContain('catalog scope must be clean');

    const nonGit = join(root, 'not-git');
    await mkdir(nonGit);
    const outside = await runCommand([
      '--catalog',
      nonGit,
      ...reportArguments(catalogCommit, 'fixture-version'),
      '--capabilities',
      manifest,
    ]);
    expect(outside.exitCode).toBe(2);
    expect(outside.stderr).toContain('Git checkout');
  });

  it('metadata が参照する ignored artifact を commit provenance なしで受理しない', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    await writeText(
      catalog,
      '.gitignore',
      'challenges/ignored/template.yaml\n'
    );
    await writeText(
      catalog,
      'challenges/ignored/metadata.json',
      `${JSON.stringify({
        id: 'ignored',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await writeText(
      catalog,
      'challenges/ignored/template.yaml',
      'Resources: {}\n'
    );
    await writeFile(
      manifest,
      `${JSON.stringify({ schemaVersion: '1', version: 'fixture-version', capabilities: [] })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);

    const result = await runCommand([
      '--catalog',
      catalog,
      ...reportArguments(catalogCommit, 'fixture-version'),
      '--capabilities',
      manifest,
    ]);

    expect(result.exitCode).toBe(2);
    expect(result.stderr).toContain('catalog source is not a tracked blob');
    expect(result.stderr).toContain('challenges/ignored/template.yaml');
  });

  it('assume-unchanged で status から隠された source byte drift を拒否する', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    const templatePath = 'challenges/hidden/template.yaml';
    await writeText(
      catalog,
      'challenges/hidden/metadata.json',
      `${JSON.stringify({
        id: 'hidden',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await writeText(catalog, templatePath, 'Resources: {}\n');
    await writeFile(
      manifest,
      `${JSON.stringify({ schemaVersion: '1', version: 'fixture-version', capabilities: [] })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);
    await runGit(catalog, ['update-index', '--assume-unchanged', templatePath]);
    await writeText(
      catalog,
      templatePath,
      'Resources:\n  Drifted:\n    Type: AWS::SSM::Parameter\n'
    );

    const result = await runCommand([
      '--catalog',
      catalog,
      ...reportArguments(catalogCommit, 'fixture-version'),
      '--capabilities',
      manifest,
    ]);

    expect(result.exitCode).toBe(2);
    expect(result.stderr).toContain('does not match the tracked blob');
    expect(result.stderr).toContain(templatePath);
  });

  it('UTF-8 replacement が同じ異なる raw byte を commit blob と同一扱いしない', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    const metadataPath = 'challenges/raw-byte/metadata.json';
    const absoluteMetadata = join(catalog, metadataPath);
    await mkdir(dirname(absoluteMetadata), { recursive: true });
    await writeFile(absoluteMetadata, Uint8Array.from([0x7b, 0x80, 0x7d]));
    await writeFile(
      manifest,
      `${JSON.stringify({ schemaVersion: '1', version: 'fixture-version', capabilities: [] })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);
    await runGit(catalog, ['update-index', '--assume-unchanged', metadataPath]);
    await writeFile(absoluteMetadata, Uint8Array.from([0x7b, 0x81, 0x7d]));

    const result = await runCommand([
      '--catalog',
      catalog,
      ...reportArguments(catalogCommit, 'fixture-version'),
      '--capabilities',
      manifest,
    ]);

    expect(result.exitCode).toBe(2);
    expect(result.stderr).toContain('does not match the tracked blob');
    expect(result.stderr).toContain(metadataPath);
  });

  it('simulator versionがcapability manifest versionと一致しないreportを拒否する', async () => {
    const root = await temporaryDirectory();
    const catalog = join(root, 'catalog');
    const manifest = join(root, 'capabilities.json');
    await writeText(catalog, 'README.md', 'versioned catalog\n');
    await writeFile(
      manifest,
      `${JSON.stringify({ schemaVersion: '1', version: 'manifest-version', capabilities: [] })}\n`,
      'utf8'
    );
    const catalogCommit = await commitCatalog(catalog);

    const result = await runCommand([
      '--catalog',
      catalog,
      ...reportArguments(catalogCommit, 'different-version'),
      '--capabilities',
      manifest,
    ]);

    expect(result.exitCode).toBe(2);
    expect(result.stderr).toContain(
      'simulator version must exactly match the capability manifest version'
    );
  });
});
