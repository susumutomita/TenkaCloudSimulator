import { writeFile } from 'node:fs/promises';
import { simulatorCapabilityManifest } from './index';

const HELP = `Usage: tenkacloud-simulator-capabilities --source-commit <sha> [--output <file>]

Writes the deterministic provider capability manifest bound to an immutable source commit.
`;

export interface CapabilityCommandResult {
  readonly exitCode: 0 | 2;
  readonly stderr: string;
  readonly stdout: string;
}

export async function runCapabilityCommand(
  args: readonly string[]
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
  const output = `${JSON.stringify(simulatorCapabilityManifest(sourceCommit), null, 2)}\n`;
  const outputPath = values.get('--output');
  if (outputPath) {
    await writeFile(outputPath, output);
    return { exitCode: 0, stdout: '', stderr: '' };
  }
  return { exitCode: 0, stdout: output, stderr: '' };
}
