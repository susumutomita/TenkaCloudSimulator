import { writeFile } from 'node:fs/promises';
import { simulatorCapabilityManifest } from './index';

const HELP = `Usage: tenkacloud-simulator-capabilities [--output <file>]

Writes the deterministic provider capability manifest to stdout or a file.
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
  if (args.length !== 0 && (args.length !== 2 || args[0] !== '--output')) {
    return { exitCode: 2, stdout: '', stderr: HELP };
  }
  const output = `${JSON.stringify(simulatorCapabilityManifest(), null, 2)}\n`;
  const outputPath = args[1];
  if (outputPath) {
    await writeFile(outputPath, output);
    return { exitCode: 0, stdout: '', stderr: '' };
  }
  return { exitCode: 0, stdout: output, stderr: '' };
}
