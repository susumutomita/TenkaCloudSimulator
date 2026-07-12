import { afterEach, describe, expect, it } from 'bun:test';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
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

    const result = await runCommand([
      '--catalog',
      catalog,
      '--capabilities',
      manifest,
      '--output',
      output,
    ]);

    expect(result.exitCode).toBe(1);
    expect(result.stdout).toBe('');
    expect(result.stderr).toContain('missing=1');
    expect(JSON.parse(await readFile(output, 'utf8'))).toEqual(
      expect.objectContaining({ status: 'failed' })
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

    const result = await runCommand([
      '--catalog',
      catalog,
      '--capabilities',
      manifest,
    ]);

    expect(result.exitCode).toBe(0);
    expect(JSON.parse(result.stdout)).toEqual(
      expect.objectContaining({ status: 'covered' })
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
            service: 'iam',
            resourceType: 'AWS::IAM::Role',
            operation: 'lifecycle',
            fidelity: 'L2',
          },
        ],
      })}\n`,
      'utf8'
    );

    const result = await runCommand([
      '--catalog',
      catalog,
      '--capabilities',
      manifest,
    ]);
    const report = JSON.parse(result.stdout);

    expect(result.exitCode).toBe(0);
    expect(result.stderr).toContain('missing=0');
    expect(result.stderr).toContain('authorization-missing=1');
    expect(report.status).toBe('covered');
    expect(report.summary.authorizationInventory.missing).toBe(1);
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
      '--catalog and --capabilities are required'
    );

    const unknown = await runCommand(['--unknown']);
    expect(unknown.exitCode).toBe(2);
    expect(unknown.stderr).toContain('unknown argument');
  });

  it('manifest 読み取り失敗を終了コード 2 で返す', async () => {
    const root = await temporaryDirectory();
    const result = await runCommand([
      '--catalog',
      root,
      '--capabilities',
      join(root, 'missing.json'),
    ]);
    expect(result.exitCode).toBe(2);
    expect(result.stderr).toContain('capability manifest');
  });
});
