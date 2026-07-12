#!/usr/bin/env bun
import { readFile, writeFile } from 'node:fs/promises';
import {
  assertProblemRuntimeDescriptor,
  assertSimulatorSnapshot,
  type ProblemRuntimeDescriptor,
  type SimulatorDeploymentRequest,
  type SimulatorWorldRequest,
} from '@tenkacloud/simulator-contracts';
import { SimulatorClient } from './client';

const USAGE = `Usage:
  tenkacloud-simulator capabilities --url <url> [--token <launch-token>]
  tenkacloud-simulator world-create --url <url> --token <launch-token> --tenant <id> --event <id> --team <id> --deployment <id>
  tenkacloud-simulator deploy --url <url> --world <id> --problem <id> --runtime <json-file> --template <file>
  tenkacloud-simulator deployment-get --url <url> --world <id> --deployment <id>
  tenkacloud-simulator resources --url <url> --world <id>
  tenkacloud-simulator operation --url <url> --world <id> --provider <id> --operation <name> --deployment <id> --target <id> --engine <id> --service <id> --resource <type> --input <json-file> --idempotency <key>
  tenkacloud-simulator snapshot-export --url <url> --world <id> --output <json-file>
  tenkacloud-simulator snapshot-import --url <url> --input <json-file>
  tenkacloud-simulator world-delete --url <url> --world <id>
  tenkacloud-simulator events --url <url> --world <id> [--after <cursor>]
`;

interface Output {
  write(value: string): void;
}

function options(args: readonly string[]): Readonly<Record<string, string>> {
  const result: Record<string, string> = {};
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    const value = args[index + 1];
    if (!key?.startsWith('--') || value === undefined) {
      throw new TypeError('CLI options must use --name value pairs');
    }
    result[key.slice(2)] = value;
  }
  return result;
}

function required(
  values: Readonly<Record<string, string>>,
  key: string
): string {
  const value = values[key];
  if (!value) throw new TypeError(`--${key} is required`);
  return value;
}

async function runtime(path: string): Promise<ProblemRuntimeDescriptor> {
  const value: unknown = JSON.parse(await readFile(path, 'utf8'));
  assertProblemRuntimeDescriptor(value);
  return value;
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

async function jsonObject(
  path: string
): Promise<Readonly<Record<string, unknown>>> {
  const value: unknown = JSON.parse(await readFile(path, 'utf8'));
  if (!isRecord(value))
    throw new TypeError('input file must contain an object');
  return value;
}

function cursor(value: string | undefined): number {
  const parsed = value === undefined ? 0 : Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < 0) {
    throw new TypeError('--after must be a non-negative integer');
  }
  return parsed;
}

function print(output: Output, value: unknown): void {
  output.write(`${JSON.stringify(value, null, 2)}\n`);
}

async function executeLifecycle(
  command: string,
  values: Readonly<Record<string, string>>,
  client: SimulatorClient,
  stdout: Output
): Promise<boolean> {
  if (command === 'capabilities') {
    print(stdout, await client.capabilities());
    return true;
  }
  if (command === 'world-create') {
    const request: SimulatorWorldRequest = {
      tenantId: required(values, 'tenant'),
      eventId: required(values, 'event'),
      teamId: required(values, 'team'),
      deploymentId: required(values, 'deployment'),
    };
    print(stdout, await client.createWorld(request));
    return true;
  }
  if (command === 'deploy') {
    const request: SimulatorDeploymentRequest = {
      problemId: required(values, 'problem'),
      runtime: await runtime(required(values, 'runtime')),
      templateBody: await readFile(required(values, 'template'), 'utf8'),
    };
    print(
      stdout,
      await client.createDeployment(required(values, 'world'), request)
    );
    return true;
  }
  if (command === 'deployment-get') {
    print(
      stdout,
      await client.getDeployment(
        required(values, 'world'),
        required(values, 'deployment')
      )
    );
    return true;
  }
  if (command === 'world-delete') {
    await client.deleteWorld(required(values, 'world'));
    print(stdout, { deleted: true });
    return true;
  }
  return false;
}

async function executeInspection(
  command: string,
  values: Readonly<Record<string, string>>,
  client: SimulatorClient,
  stdout: Output
): Promise<boolean> {
  if (command === 'resources') {
    print(stdout, await client.resources(required(values, 'world')));
    return true;
  }
  if (command === 'events') {
    print(
      stdout,
      await client.events(required(values, 'world'), cursor(values['after']))
    );
    return true;
  }
  if (command === 'operation') {
    print(
      stdout,
      await client.operation(
        required(values, 'world'),
        required(values, 'provider'),
        required(values, 'operation'),
        {
          deploymentId: required(values, 'deployment'),
          targetId: required(values, 'target'),
          engine: required(values, 'engine'),
          service: required(values, 'service'),
          resourceType: required(values, 'resource'),
          input: await jsonObject(required(values, 'input')),
        },
        required(values, 'idempotency')
      )
    );
    return true;
  }
  return false;
}

async function executeSnapshot(
  command: string,
  values: Readonly<Record<string, string>>,
  client: SimulatorClient,
  stdout: Output
): Promise<boolean> {
  if (command === 'snapshot-export') {
    const outputPath = required(values, 'output');
    const snapshot = await client.snapshot(required(values, 'world'));
    await writeFile(outputPath, `${JSON.stringify(snapshot, null, 2)}\n`);
    print(stdout, { output: outputPath, hash: snapshot.hash });
    return true;
  }
  if (command === 'snapshot-import') {
    const snapshot: unknown = JSON.parse(
      await readFile(required(values, 'input'), 'utf8')
    );
    assertSimulatorSnapshot(snapshot);
    print(stdout, await client.restoreSnapshot(snapshot));
    return true;
  }
  return false;
}

export async function runCli(
  args: readonly string[],
  stdout: Output = process.stdout,
  stderr: Output = process.stderr
): Promise<0 | 1 | 2> {
  if (args.length === 0 || args[0] === '--help') {
    stdout.write(USAGE);
    return 0;
  }
  try {
    const command = args[0] ?? '';
    const values = options(args.slice(1));
    const client = new SimulatorClient(
      required(values, 'url'),
      values['token']
    );
    if (
      (await executeLifecycle(command, values, client, stdout)) ||
      (await executeInspection(command, values, client, stdout)) ||
      (await executeSnapshot(command, values, client, stdout))
    ) {
      return 0;
    }
    stderr.write(`Unknown command: ${command}\n${USAGE}`);
    return 2;
  } catch (error) {
    stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    return error instanceof TypeError ? 2 : 1;
  }
}
