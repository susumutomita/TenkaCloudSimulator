import { mkdir, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import { CatalogScannerError, errorMessage } from './errors.ts';
import {
  compareInventory,
  readCapabilityManifest,
  serializeReport,
} from './manifest.ts';
import type { CommandResult } from './model.ts';
import { collectCatalogWithSources } from './scanner.ts';
import { contentDigest, type SourceDigest } from './source.ts';

const HELP = `Usage: tenkacloud-catalog-scan --catalog <root> --catalog-commit <sha> --capabilities <file> --simulator-version <version> [--output <file>]

Options:
  --catalog <root>       TenkaCloudChallenge catalog root
  --catalog-commit <sha> Immutable 40-character catalog commit
  --capabilities <file>  Simulator capability manifest
  --simulator-version <version> Exact version from the capability manifest
  --output <file>        Write deterministic JSON report to a file
  --help                 Show this help
`;

const VALUE_OPTIONS = new Set([
  '--catalog',
  '--catalog-commit',
  '--capabilities',
  '--simulator-version',
  '--output',
]);

interface CommandArguments {
  catalog: string;
  catalogCommit: string;
  capabilities: string;
  output: string | null;
  simulatorVersion: string;
}

type ParsedArguments =
  | { status: 'error'; message: string }
  | { status: 'ok'; value: CommandArguments };

function optionValue(args: string[], index: number): string | undefined {
  const value = args[index + 1];
  return value === undefined || value.startsWith('--') ? undefined : value;
}

function parseArguments(args: string[]): ParsedArguments {
  const values = new Map<string, string>();
  for (let index = 0; index < args.length; index += 1) {
    const option = args[index];
    if (option === undefined || !VALUE_OPTIONS.has(option)) {
      return { status: 'error', message: `unknown argument: ${option ?? ''}` };
    }
    const value = optionValue(args, index);
    if (value === undefined) {
      return { status: 'error', message: `${option} requires a value` };
    }
    values.set(option, value);
    index += 1;
  }
  const catalog = values.get('--catalog');
  const catalogCommit = values.get('--catalog-commit');
  const capabilities = values.get('--capabilities');
  const simulatorVersion = values.get('--simulator-version');
  if (
    catalog === undefined ||
    catalogCommit === undefined ||
    capabilities === undefined ||
    simulatorVersion === undefined
  ) {
    return {
      status: 'error',
      message:
        '--catalog, --catalog-commit, --capabilities, and --simulator-version are required',
    };
  }
  return {
    status: 'ok',
    value: {
      catalog,
      catalogCommit,
      capabilities,
      output: values.get('--output') ?? null,
      simulatorVersion,
    },
  };
}

function summaryLine(
  summary: {
    covered: number;
    missing: number;
    insufficient: number;
    invalid: number;
  },
  authorization: {
    covered: number;
    missing: number;
    insufficient: number;
  }
): string {
  return `coverage: covered=${summary.covered} missing=${summary.missing} insufficient=${summary.insufficient} authorization-covered=${authorization.covered} authorization-missing=${authorization.missing} authorization-insufficient=${authorization.insufficient} invalid=${summary.invalid}\n`;
}

async function gitOutput(
  catalog: string,
  arguments_: readonly string[]
): Promise<string> {
  const subprocess = Bun.spawn(['git', '-C', catalog, ...arguments_], {
    stdout: 'pipe',
    stderr: 'pipe',
  });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(subprocess.stdout).text(),
    new Response(subprocess.stderr).text(),
    subprocess.exited,
  ]);
  if (exitCode !== 0) {
    throw new CatalogScannerError(
      'INVALID_CATALOG_SOURCE',
      `catalog must be a readable Git checkout: ${stderr.trim() || 'Git inspection failed'}`
    );
  }
  return stdout.trim();
}

async function validateCatalogCheckout(
  catalog: string,
  expectedCommit: string
): Promise<void> {
  const head = await gitOutput(catalog, ['rev-parse', '--verify', 'HEAD']);
  if (head !== expectedCommit) {
    throw new CatalogScannerError(
      'INVALID_CATALOG_SOURCE',
      `catalog HEAD ${head} does not match --catalog-commit ${expectedCommit}`
    );
  }
  const status = await gitOutput(catalog, [
    'status',
    '--porcelain=v1',
    '--untracked-files=all',
    '--ignore-submodules=none',
    '--',
    '.',
  ]);
  if (status) {
    throw new CatalogScannerError(
      'INVALID_CATALOG_SOURCE',
      'catalog scope must be clean at the reported commit'
    );
  }
}

async function committedSourceContents(
  catalog: string,
  expectedCommit: string,
  path: string
): Promise<Uint8Array> {
  const subprocess = Bun.spawn(
    ['git', '-C', catalog, 'cat-file', 'blob', `${expectedCommit}:${path}`],
    { stdout: 'pipe', stderr: 'pipe' }
  );
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(subprocess.stdout).arrayBuffer(),
    new Response(subprocess.stderr).text(),
    subprocess.exited,
  ]);
  if (exitCode !== 0) {
    throw new CatalogScannerError(
      'INVALID_CATALOG_SOURCE',
      `catalog source is not a tracked blob at ${expectedCommit}: ${path} (${stderr.trim() || 'missing blob'})`
    );
  }
  return new Uint8Array(stdout);
}

async function validateCatalogSources(
  catalog: string,
  expectedCommit: string,
  sources: readonly SourceDigest[]
): Promise<void> {
  const byPath = new Map<string, string>();
  for (const source of sources) {
    byPath.set(source.path, source.digest);
  }
  await Promise.all(
    [...byPath.entries()].map(async ([path, digest]) => {
      const committed = await committedSourceContents(
        catalog,
        expectedCommit,
        path
      );
      if (contentDigest(path, committed).digest !== digest) {
        throw new CatalogScannerError(
          'INVALID_CATALOG_SOURCE',
          `catalog source does not match the tracked blob at ${expectedCommit}: ${path}`
        );
      }
    })
  );
}

async function execute(arguments_: CommandArguments): Promise<CommandResult> {
  await validateCatalogCheckout(arguments_.catalog, arguments_.catalogCommit);
  const [collection, manifest] = await Promise.all([
    collectCatalogWithSources(arguments_.catalog),
    readCapabilityManifest(arguments_.capabilities),
  ]);
  await validateCatalogCheckout(arguments_.catalog, arguments_.catalogCommit);
  await validateCatalogSources(
    arguments_.catalog,
    arguments_.catalogCommit,
    collection.sources
  );
  const report = compareInventory(collection.inventory, manifest, {
    catalogCommit: arguments_.catalogCommit,
    simulatorVersion: arguments_.simulatorVersion,
  });
  const serialized = serializeReport(report);
  if (arguments_.output !== null) {
    await mkdir(dirname(arguments_.output), { recursive: true });
    await writeFile(arguments_.output, serialized, 'utf8');
  }
  return {
    exitCode: report.supported ? 0 : 1,
    stdout: arguments_.output === null ? serialized : '',
    stderr: summaryLine(
      report.summary,
      report.inventory.authorizationInventory.summary
    ),
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
