import { afterEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  HTTP_ENDPOINT_RESOURCE,
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { AwsProvider } from '@tenkacloud/simulator-provider-aws';
import { AzureProvider } from '@tenkacloud/simulator-provider-azure';
import { GcpProvider } from '@tenkacloud/simulator-provider-gcp';
import { SakuraProvider } from '@tenkacloud/simulator-provider-sakura';
import { DockerWorkloadRunner } from '@tenkacloud/simulator-workload-runner';

interface RuntimeTarget {
  readonly id: string;
  readonly provider: string;
  readonly engine: string;
  readonly entry: string;
}

interface ScoringTarget {
  readonly targetId: string;
  readonly probe: 'https';
  readonly outputKey: string;
  readonly path?: string;
  readonly expectStatus: readonly number[];
}

interface CatalogMetadata {
  readonly id: string;
  readonly runtime: {
    readonly kind: 'composite';
    readonly targets: readonly RuntimeTarget[];
  };
  readonly scoring: {
    readonly kind: 'composite-probe';
    readonly success: 'all';
    readonly pointsAllOk: number;
    readonly targets: readonly ScoringTarget[];
  };
}

interface TestContext {
  readonly core: SimulationCore;
  readonly directory: string;
  readonly store: SimulationStore;
  readonly worldId: string;
}

interface ReviewedCatalogFixture {
  readonly metadata: CatalogMetadata;
  readonly simulationOverlay: unknown;
  readonly sourceByTarget: ReadonlyMap<string, string>;
}

const CATALOG_COMMIT = '488ed4a2d103cbe596295c940620d68d8f420c99';
const WORKLOAD_IMAGE =
  'ghcr.io/susumutomita/tenkacloud-challenge-microservice-migration@sha256:96c7ca29de82b7d0c041e98f9cd9494de283102509134e5fb524d6e89da27cf2';
const PROXY_IMAGE =
  'busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662';
const FIXTURES = {
  metadata: {
    path: './fixtures/hello-multicloud/metadata.json.fixture',
    sha256: 'e179875594123f0c063a732f4e263a0279f3a59c813d085de9053a388b048246',
  },
  overlay: {
    path: './fixtures/hello-multicloud/simulation.json',
    sha256: '21f0ff18fa5bacd5430e2859193957ef307e4f9c65c7fe177000678043439188',
  },
  aws: {
    path: '../../../providers/aws/tests/fixtures/hello-multicloud.yaml',
    sha256: '1dc0dba2f1c1ad5bae88bf2736debb67d6d7b9024ba117abd0fb769eabd523e6',
  },
  gcp: {
    path: '../../../providers/gcp/test/fixtures/hello-multicloud/main.tf',
    sha256: '9d205c898bf977b344db1c737ebf9c497a04f2718cf7262c43ffeafc460be1bf',
  },
  azure: {
    path: '../../../providers/azure/src/fixtures/hello-multicloud.bicep',
    sha256: 'b342cc2281ec113d7c73953f87566b2a0e43c2dcb5fba0d87531d1f19b6abe74',
  },
  sakura: {
    path: '../../../providers/sakura/src/fixtures/hello-multicloud.json',
    sha256: 'f8d2e2279be29f6698bd5d228d880bbaa0ecbca1f5369cdb20b4f36e26985ad2',
  },
} as const;

const contexts: TestContext[] = [];
const runner = new DockerWorkloadRunner({
  allowedImages: new Set([PROXY_IMAGE, WORKLOAD_IMAGE]),
  proxyImage: PROXY_IMAGE,
  maxMemoryBytes: 134_217_728,
  maxMilliCpu: 500,
  maxPids: 64,
});

afterEach(async () => {
  while (contexts.length > 0) {
    const context = contexts.pop();
    if (!context) continue;
    await context.core.deleteWorld(context.worldId);
    context.store.close();
    await rm(context.directory, { recursive: true, force: true });
  }
}, 60_000);

async function reviewedFixture(fixture: {
  readonly path: string;
  readonly sha256: string;
}): Promise<string> {
  const contents = await readFile(
    new URL(fixture.path, import.meta.url),
    'utf8'
  );
  const actualSha256 = createHash('sha256').update(contents).digest('hex');
  if (actualSha256 !== fixture.sha256) {
    throw new Error(
      `${fixture.path} drifted from TenkaCloudChallenge ${CATALOG_COMMIT}: ${actualSha256}`
    );
  }
  return contents;
}

async function reviewedCatalogFixture(): Promise<ReviewedCatalogFixture> {
  const [metadataSource, overlaySource, aws, gcp, azure, sakura] =
    await Promise.all([
      reviewedFixture(FIXTURES.metadata),
      reviewedFixture(FIXTURES.overlay),
      reviewedFixture(FIXTURES.aws),
      reviewedFixture(FIXTURES.gcp),
      reviewedFixture(FIXTURES.azure),
      reviewedFixture(FIXTURES.sakura),
    ]);
  return {
    metadata: JSON.parse(metadataSource) as CatalogMetadata,
    simulationOverlay: JSON.parse(overlaySource) as unknown,
    sourceByTarget: new Map([
      ['aws-hello', aws],
      ['gcp-hello', gcp],
      ['azure-hello', azure],
      ['sakura-hello', sakura],
    ]),
  };
}

function expectProductionScoringContract(metadata: CatalogMetadata): void {
  expect(metadata.id).toBe('hello-multicloud');
  expect(metadata.runtime.targets.map((target) => target.id)).toEqual([
    'aws-hello',
    'gcp-hello',
    'azure-hello',
    'sakura-hello',
  ]);
  expect(metadata.scoring).toMatchObject({
    kind: 'composite-probe',
    success: 'all',
    pointsAllOk: 100,
  });
  expect(metadata.scoring.targets).toEqual([
    {
      targetId: 'aws-hello',
      probe: 'https',
      outputKey: 'AwsHelloUrl',
      expectStatus: [200],
    },
    {
      targetId: 'gcp-hello',
      probe: 'https',
      outputKey: 'GcpHelloUrl',
      expectStatus: [200],
    },
    {
      targetId: 'azure-hello',
      probe: 'https',
      outputKey: 'AzureHelloUrl',
      path: '/healthz',
      expectStatus: [200],
    },
    {
      targetId: 'sakura-hello',
      probe: 'https',
      outputKey: 'BaseUrl',
      path: '/healthz',
      expectStatus: [200],
    },
  ]);
}

async function createTestCore(): Promise<{
  readonly core: SimulationCore;
  readonly deploymentId: string;
  readonly worldId: string;
}> {
  const directory = await mkdtemp(join(tmpdir(), 'hello-multicloud-final-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([
      new AwsProvider(),
      new GcpProvider(),
      new AzureProvider(),
      new SakuraProvider(),
    ]),
    { workloadEffects: runner }
  );
  const deploymentId = 'hello-multicloud-deployment';
  const world = core.createWorld(
    {
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId,
    },
    'hello-multicloud-final-world'
  );
  contexts.push({ core, directory, store, worldId: world.worldId });
  return { core, deploymentId, worldId: world.worldId };
}

const ARTIFACT_PATH_BY_TARGET = new Map([
  ['aws-hello', 'template.yaml'],
  ['gcp-hello', 'gcp/terraform/main.tf'],
  ['azure-hello', 'azure/main.bicep'],
  ['sakura-hello', 'sakura/application.json'],
]);

function artifactBundle(fixture: ReviewedCatalogFixture): string {
  return JSON.stringify({
    format: 'tenkacloud.simulator.artifacts.v1',
    targets: [...fixture.metadata.runtime.targets]
      .sort((left, right) => left.id.localeCompare(right.id))
      .map((target) => {
        const path = ARTIFACT_PATH_BY_TARGET.get(target.id);
        const content = fixture.sourceByTarget.get(target.id);
        if (!path || !content) {
          throw new Error(`reviewed artifact is missing for ${target.id}`);
        }
        return { ...target, artifacts: [{ path, content }] };
      }),
  });
}

type ReadyDeployment = Awaited<
  ReturnType<SimulationCore['materializeWorkloads']>
>;

async function deployFixture(
  fixture: ReviewedCatalogFixture,
  core: SimulationCore,
  worldId: string,
  deploymentId: string
): Promise<ReadyDeployment> {
  const deployment = core.createDeployment(
    worldId,
    {
      deploymentId,
      problemId: fixture.metadata.id,
      runtime: fixture.metadata.runtime,
      metadata: { scoring: fixture.metadata.scoring },
      simulationOverlay: fixture.simulationOverlay,
      templateBody: artifactBundle(fixture),
    },
    'hello-multicloud-deployment-key'
  );
  expect(deployment.status).toBe('deploying');
  const ready = await core.materializeWorkloads(worldId, deploymentId);
  expect(ready.status).toBe('ready');
  expect(ready.targets.map((target) => target.id)).toEqual(
    fixture.metadata.runtime.targets.map((target) => target.id)
  );
  return ready;
}

function scoringCommand(
  target: RuntimeTarget,
  scoring: ScoringTarget,
  output: string,
  deploymentId: string
) {
  const input = {
    Method: 'GET',
    Path: scoring.path ?? '/',
    Headers: {},
    Body: '',
  };
  return {
    deploymentId,
    targetId: target.id,
    provider: target.provider,
    engine: target.engine,
    service: 'http',
    operation:
      target.provider === 'gcp' || target.provider === 'azure'
        ? 'Request'
        : 'Probe',
    resourceType: HTTP_ENDPOINT_RESOURCE,
    input:
      target.provider === 'aws'
        ? { Url: new URL(scoring.path ?? '/', output).toString() }
        : input,
  };
}

async function probeScoringTargets(
  metadata: CatalogMetadata,
  ready: ReadyDeployment,
  core: SimulationCore,
  worldId: string,
  deploymentId: string
): Promise<ReadonlyMap<string, number>> {
  const statusByTarget = new Map<string, number>();
  for (const scoring of metadata.scoring.targets) {
    const target = metadata.runtime.targets.find(
      (candidate) => candidate.id === scoring.targetId
    );
    const output = ready.outputs[scoring.targetId]?.[scoring.outputKey];
    if (!target || typeof output !== 'string') {
      throw new Error(`scoring binding is missing for ${scoring.targetId}`);
    }
    expect(output).toStartWith('https://');
    const command = scoringCommand(target, scoring, output, deploymentId);
    const response =
      target.provider === 'aws' || target.provider === 'sakura'
        ? await core.executeCommandAsync(worldId, command, `score-${target.id}`)
        : core.executeCommand(worldId, command, `score-${target.id}`);
    statusByTarget.set(target.id, statusCode(response));
  }
  return statusByTarget;
}

function scoredPoints(
  metadata: CatalogMetadata,
  statusByTarget: ReadonlyMap<string, number>
): number {
  return metadata.scoring.targets.every((target) =>
    target.expectStatus.includes(statusByTarget.get(target.targetId) ?? 0)
  )
    ? metadata.scoring.pointsAllOk
    : 0;
}

function statusCode(response: Readonly<Record<string, unknown>>): number {
  const value = Reflect.get(response, 'StatusCode');
  if (typeof value !== 'number') {
    throw new Error('HTTP data-plane response has no StatusCode');
  }
  return value;
}

describe('final hello-multicloud catalog regression', () => {
  it.serial(
    'Challenge の4 targetを同じworldへ配備してproduction scoring pathをすべて実probeする',
    async () => {
      expect(await runner.available()).toBe(true);
      const fixture = await reviewedCatalogFixture();
      expectProductionScoringContract(fixture.metadata);
      const { core, deploymentId, worldId } = await createTestCore();
      const ready = await deployFixture(fixture, core, worldId, deploymentId);
      const statusByTarget = await probeScoringTargets(
        fixture.metadata,
        ready,
        core,
        worldId,
        deploymentId
      );

      expect(Object.fromEntries(statusByTarget)).toEqual({
        'aws-hello': 200,
        'gcp-hello': 200,
        'azure-hello': 200,
        'sakura-hello': 200,
      });
      expect(scoredPoints(fixture.metadata, statusByTarget)).toBe(100);
    },
    120_000
  );
});
