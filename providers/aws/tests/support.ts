import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  type ExecuteCommandInput,
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { AwsProvider } from '../src/provider';

export interface TestContext {
  readonly directory: string;
  readonly store: SimulationStore;
  readonly core: SimulationCore;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly templateBody: string;
  sequence: number;
}

const contexts: TestContext[] = [];

export async function cleanupContexts(): Promise<void> {
  while (contexts.length > 0) {
    const context = contexts.pop();
    if (!context) continue;
    context.store.close();
    await rm(context.directory, { recursive: true, force: true });
  }
}

export async function fixtureBody(
  name = 'catalog-stack.yaml'
): Promise<string> {
  return readFile(join(import.meta.dir, 'fixtures', name), 'utf8');
}

export async function createContext(
  metadataFixture?: string,
  problemId = 'aws-fixture',
  templateFixture = 'catalog-stack.yaml'
): Promise<TestContext> {
  const directory = await mkdtemp(join(tmpdir(), 'aws-provider-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new AwsProvider()])
  );
  const deploymentId = 'deployment-aws';
  const world = core.createWorld(
    {
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId,
      seed: 'seed',
      virtualTime: '2026-07-12T00:00:00.000Z',
    },
    'world-key'
  );
  const templateBody = await fixtureBody(templateFixture);
  const fixtureMetadata =
    metadataFixture === undefined
      ? {}
      : JSON.parse(await fixtureBody(metadataFixture));
  core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId,
      runtime: {
        provider: 'aws',
        engine: 'cloudformation',
        entry: `tests/fixtures/${templateFixture}`,
      },
      templateBody,
      metadata: {
        cfnParameters: { FlagSeed: 'fixture-seed' },
        ...fixtureMetadata,
      },
    },
    'deployment-key'
  );
  const context = {
    directory,
    store,
    core,
    worldId: world.worldId,
    deploymentId,
    templateBody,
    sequence: 0,
  };
  contexts.push(context);
  return context;
}

export function execute(
  context: TestContext,
  service: string,
  operation: string,
  input: Readonly<Record<string, unknown>>,
  resourceType = '*'
): Readonly<Record<string, unknown>> {
  context.sequence += 1;
  const command: ExecuteCommandInput = {
    deploymentId: context.deploymentId,
    targetId: 'default',
    provider: 'aws',
    engine: 'cloudformation',
    service,
    operation,
    resourceType,
    input,
  };
  return context.core.executeCommand(
    context.worldId,
    command,
    `${service}-${operation}-${context.sequence}`
  );
}

export function resourceByLogicalId(context: TestContext, logicalId: string) {
  const resource = context.store
    .resources(context.worldId)
    .find((candidate) => candidate.properties['logicalId'] === logicalId);
  if (!resource) throw new Error(`resource ${logicalId} is missing`);
  return resource;
}
