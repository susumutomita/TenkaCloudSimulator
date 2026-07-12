import { mkdir, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import { errorMessage } from './errors.ts';
import {
  compareInventory,
  readCapabilityManifest,
  serializeReport,
} from './manifest.ts';
import type { CommandResult } from './model.ts';
import { collectCatalog } from './scanner.ts';

const HELP = `Usage: tenkacloud-catalog-scan --catalog <root> --capabilities <file> [--output <file>]

Options:
  --catalog <root>       TenkaCloudChallenge catalog root
  --capabilities <file>  Simulator capability manifest
  --output <file>        Write deterministic JSON report to a file
  --help                 Show this help
`;

interface CommandArguments {
  catalog: string;
  capabilities: string;
  output: string | null;
}

type ParsedArguments =
  | { status: 'error'; message: string }
  | { status: 'ok'; value: CommandArguments };

function optionValue(args: string[], index: number): string | undefined {
  const value = args[index + 1];
  return value === undefined || value.startsWith('--') ? undefined : value;
}

function parseArguments(args: string[]): ParsedArguments {
  let catalog: string | undefined;
  let capabilities: string | undefined;
  let output: string | null = null;
  for (let index = 0; index < args.length; index += 1) {
    const option = args[index];
    if (
      option !== '--catalog' &&
      option !== '--capabilities' &&
      option !== '--output'
    ) {
      return { status: 'error', message: `unknown argument: ${option ?? ''}` };
    }
    const value = optionValue(args, index);
    if (value === undefined) {
      return { status: 'error', message: `${option} requires a value` };
    }
    if (option === '--catalog') catalog = value;
    if (option === '--capabilities') capabilities = value;
    if (option === '--output') output = value;
    index += 1;
  }
  if (catalog === undefined || capabilities === undefined) {
    return {
      status: 'error',
      message: '--catalog and --capabilities are required',
    };
  }
  return { status: 'ok', value: { catalog, capabilities, output } };
}

function summaryLine(summary: {
  covered: number;
  missing: number;
  insufficient: number;
  authorizationInventory: {
    covered: number;
    missing: number;
    insufficient: number;
  };
  invalid: number;
}): string {
  const authorization = summary.authorizationInventory;
  return `coverage: covered=${summary.covered} missing=${summary.missing} insufficient=${summary.insufficient} authorization-covered=${authorization.covered} authorization-missing=${authorization.missing} authorization-insufficient=${authorization.insufficient} invalid=${summary.invalid}\n`;
}

async function execute(arguments_: CommandArguments): Promise<CommandResult> {
  const [inventory, manifest] = await Promise.all([
    collectCatalog(arguments_.catalog),
    readCapabilityManifest(arguments_.capabilities),
  ]);
  const report = compareInventory(inventory, manifest);
  const serialized = serializeReport(report);
  if (arguments_.output !== null) {
    await mkdir(dirname(arguments_.output), { recursive: true });
    await writeFile(arguments_.output, serialized, 'utf8');
  }
  return {
    exitCode: report.status === 'covered' ? 0 : 1,
    stdout: arguments_.output === null ? serialized : '',
    stderr: summaryLine(report.summary),
  };
}

export async function runCommand(args: string[]): Promise<CommandResult> {
  if (args.includes('--help')) return { exitCode: 0, stdout: HELP, stderr: '' };
  const parsed = parseArguments(args);
  if (parsed.status === 'error') {
    return { exitCode: 2, stdout: '', stderr: `${parsed.message}\n${HELP}` };
  }
  try {
    return await execute(parsed.value);
  } catch (error) {
    return { exitCode: 2, stdout: '', stderr: `${errorMessage(error)}\n` };
  }
}
