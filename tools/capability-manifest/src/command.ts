import { execFile } from 'node:child_process';
import { realpath, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { simulatorCapabilityManifest } from './index';

const HELP = `Usage: tenkacloud-simulator-capabilities --source-commit <sha> [--output <file>]

Writes the deterministic provider capability manifest bound to an immutable source commit.
`;

export interface CapabilityCommandResult {
  readonly exitCode: 0 | 1 | 2;
  readonly stderr: string;
  readonly stdout: string;
}

const SIMULATOR_REPOSITORY_ROOT = resolve(import.meta.dir, '../../..');
const GIT_TIMEOUT_MILLISECONDS = 5000;
const GIT_OUTPUT_LIMIT_BYTES = 64 * 1024;

interface GitResult {
  readonly exitCode: number;
  readonly stdout: string;
}

async function git(
  repositoryRoot: string,
  arguments_: readonly string[]
): Promise<GitResult> {
  return new Promise((resolveResult) => {
    execFile(
      'git',
      ['-C', repositoryRoot, ...arguments_],
      {
        encoding: 'utf8',
        maxBuffer: GIT_OUTPUT_LIMIT_BYTES,
        timeout: GIT_TIMEOUT_MILLISECONDS,
        windowsHide: true,
      },
      (error, stdout) => {
        if (error === null) {
          resolveResult({
            exitCode: 0,
            stdout: stdout.trim(),
          });
          return;
        }
        if (typeof error.code === 'number') {
          resolveResult({
            exitCode: error.code,
            stdout: stdout.trim(),
          });
          return;
        }
        resolveResult({
          exitCode: -1,
          stdout: '',
        });
      }
    );
  });
}

async function validatedRepositoryRoot(
  repositoryRoot: string
): Promise<{ readonly error: string } | { readonly root: string }> {
  let canonicalRoot: string;
  try {
    canonicalRoot = await realpath(repositoryRoot);
  } catch {
    return { error: 'repository root is not a readable Git checkout' };
  }
  const topLevel = await git(canonicalRoot, ['rev-parse', '--show-toplevel']);
  if (topLevel.exitCode !== 0) {
    return { error: 'repository root is not a readable Git checkout' };
  }
  const canonicalTopLevel = resolve(topLevel.stdout);
  if (canonicalTopLevel !== canonicalRoot) {
    return { error: 'the command source anchor must be the Git top level' };
  }
  return { root: canonicalRoot };
}

async function validateSimulatorCheckout(
  repositoryRoot: string,
  sourceCommit: string
): Promise<string | undefined> {
  const repository = await validatedRepositoryRoot(repositoryRoot);
  if ('error' in repository) return repository.error;
  const sourceObject = await git(repository.root, [
    'cat-file',
    '-e',
    `${sourceCommit}^{commit}`,
  ]);
  if (sourceObject.exitCode !== 0) {
    return `--source-commit ${sourceCommit} does not exist as a commit`;
  }
  const head = await git(repository.root, [
    'rev-parse',
    '--verify',
    'HEAD^{commit}',
  ]);
  if (head.exitCode !== 0 || head.stdout !== sourceCommit) {
    return `HEAD ${head.stdout || '(unavailable)'} does not match --source-commit ${sourceCommit}`;
  }
  const status = await git(repository.root, [
    'status',
    '--porcelain=v1',
    '--untracked-files=all',
    '--ignore-submodules=none',
  ]);
  if (status.exitCode !== 0) {
    return 'Git status inspection failed';
  }
  if (status.stdout !== '') {
    return 'checkout must be clean, including staged, untracked, and submodule changes';
  }
  return undefined;
}

export async function runCapabilityCommand(
  args: readonly string[],
  repositoryRoot = SIMULATOR_REPOSITORY_ROOT
): Promise<CapabilityCommandResult> {
  if (args.length === 1 && args[0] === '--help') {
    return { exitCode: 0, stdout: HELP, stderr: '' };
  }
  const values = new Map<string, string>();
  for (let index = 0; index < args.length; index += 2) {
    const option = args[index];
    const value = args[index + 1];
    if (
      (option !== '--source-commit' && option !== '--output') ||
      value === undefined ||
      value.startsWith('--') ||
      values.has(option)
    ) {
      return { exitCode: 2, stdout: '', stderr: HELP };
    }
    values.set(option, value);
  }
  const sourceCommit = values.get('--source-commit');
  if (sourceCommit === undefined || !/^[0-9a-f]{40}$/.test(sourceCommit)) {
    return { exitCode: 2, stdout: '', stderr: HELP };
  }
  const provenanceError = await validateSimulatorCheckout(
    repositoryRoot,
    sourceCommit
  );
  if (provenanceError !== undefined) {
    return {
      exitCode: 1,
      stdout: '',
      stderr: `simulator repository provenance check failed: ${provenanceError}\n`,
    };
  }
  const output = `${JSON.stringify(simulatorCapabilityManifest(sourceCommit), null, 2)}\n`;
  const outputPath = values.get('--output');
  if (outputPath) {
    await writeFile(outputPath, output);
    return { exitCode: 0, stdout: '', stderr: '' };
  }
  return { exitCode: 0, stdout: output, stderr: '' };
}
