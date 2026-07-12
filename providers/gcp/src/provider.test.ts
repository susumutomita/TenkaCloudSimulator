import { afterEach, describe, expect, it } from 'bun:test';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  CoreError,
  type ExecuteCommandInput,
  type ProviderCommandInput,
  ProviderRegistry,
  type ProviderWorldView,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  CLOUD_RUN_IAM_MEMBER,
  CLOUD_RUN_SERVICE,
  GcpProvider,
  HTTP_ENDPOINT,
} from './provider';
import {
  terraformNumber,
  terraformResources,
  terraformString,
} from './terraform';

interface TestContext {
  readonly directory: string;
  readonly store: SimulationStore;
  readonly core: SimulationCore;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly serviceId: string;
}

const contexts: TestContext[] = [];

afterEach(async () => {
  while (contexts.length > 0) {
    const context = contexts.pop();
    if (!context) continue;
    context.store.close();
    await rm(context.directory, { recursive: true, force: true });
  }
});

async function helloTerraform(): Promise<string> {
  return readFile(
    join(import.meta.dir, '../test/fixtures/hello-multicloud/main.tf'),
    'utf8'
  );
}

async function context(): Promise<TestContext> {
  const directory = await mkdtemp(join(tmpdir(), 'gcp-provider-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new GcpProvider()])
  );
  const deploymentId = 'gcp-deployment';
  const world = core.createWorld(
    {
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId,
    },
    'world-key'
  );
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'hello-multicloud',
      runtime: {
        provider: 'gcp',
        engine: 'infra-manager',
        entry: 'gcp/terraform',
      },
      templateBody: await helloTerraform(),
    },
    'deployment-key'
  );
  const serviceId = store
    .resources(world.worldId)
    .find(
      (resource) => resource.resourceType === CLOUD_RUN_SERVICE
    )?.resourceId;
  if (!serviceId) throw new Error('Cloud Run service is missing');
  expect(deployment.outputs['default']?.['GcpHelloUrl']).toContain(
    '.run.gcp.local'
  );
  const result = {
    directory,
    store,
    core,
    worldId: world.worldId,
    deploymentId,
    serviceId,
  };
  contexts.push(result);
  return result;
}

function command(
  testContext: TestContext,
  operation: string,
  resourceType: string,
  input: Readonly<Record<string, unknown>>
): ExecuteCommandInput {
  return {
    deploymentId: testContext.deploymentId,
    targetId: 'default',
    provider: 'gcp',
    engine: 'infra-manager',
    service: operation === 'Probe' || operation === 'Request' ? 'http' : 'run',
    operation,
    resourceType,
    input,
  };
}

function providerView(testContext: TestContext): ProviderWorldView {
  return {
    world: testContext.core.world(testContext.worldId),
    resources: testContext.store.resources(testContext.worldId),
  };
}

function coreError(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('CoreError が発生しませんでした');
}

describe('GCP Infrastructure Manager provider', () => {
  it('実 catalog の Cloud Run と IAM member を同じ world へ配備する', async () => {
    const testContext = await context();
    expect(
      testContext.store
        .resources(testContext.worldId)
        .map((resource) => resource.resourceType)
        .sort()
    ).toEqual([CLOUD_RUN_IAM_MEMBER, CLOUD_RUN_SERVICE].sort());
    expect(
      testContext.core
        .events(testContext.worldId)
        .filter((event) => event.type === 'GcpResourceCreated')
    ).toHaveLength(2);
  });

  it('service の read、update、probe、delete を event として共有する', async () => {
    const testContext = await context();
    const fetched = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'GetService', CLOUD_RUN_SERVICE, {
        id: testContext.serviceId,
      }),
      'get-key'
    );
    expect(Reflect.get(fetched, 'status')).toBe('Ready');
    const updated = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'UpdateService', CLOUD_RUN_SERVICE, {
        id: testContext.serviceId,
        patch: { maxInstanceCount: 4 },
      }),
      'update-key'
    );
    expect(Reflect.get(updated, 'maxInstanceCount')).toBe(4);
    const probed = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Probe', HTTP_ENDPOINT, {
        id: testContext.serviceId,
      }),
      'probe-key'
    );
    expect(Reflect.get(probed, 'status')).toBe(200);
    const deleted = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'DeleteService', CLOUD_RUN_SERVICE, {
        id: testContext.serviceId,
      }),
      'delete-key'
    );
    expect(Reflect.get(deleted, 'deleted')).toBe(true);
  });

  it('Cloud Run endpoint は GET と HEAD を state から返し未対応 method を 405 にする', async () => {
    const testContext = await context();
    const input = {
      Method: 'GET',
      Path: '/hello?language=ja',
      Headers: { Accept: 'text/plain' },
      Body: '',
    };
    const get = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Request', HTTP_ENDPOINT, input),
      'gcp-http-get'
    );
    const head = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Request', HTTP_ENDPOINT, {
        ...input,
        Method: 'HEAD',
      }),
      'gcp-http-head'
    );
    const post = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Request', HTTP_ENDPOINT, {
        ...input,
        Method: 'POST',
      }),
      'gcp-http-post'
    );

    expect(get).toEqual({
      StatusCode: 200,
      Headers: { 'content-type': 'text/plain; charset=utf-8' },
      Body: 'Hello from TenkaCloud Simulator',
    });
    expect(head).toMatchObject({ StatusCode: 200, Body: '' });
    expect(post).toMatchObject({
      StatusCode: 405,
      Headers: { allow: 'GET, HEAD' },
    });
    expect(
      testContext.core
        .events(testContext.worldId)
        .filter((event) => event.type === 'GcpServiceRequestExecuted')
    ).toHaveLength(3);
  });

  it('Cloud Run endpoint は request 境界と resource cardinality、ready projection を loud に扱う', async () => {
    const testContext = await context();
    const provider = new GcpProvider();
    const view = providerView(testContext);
    const service = view.resources.find(
      (resource) => resource.resourceType === CLOUD_RUN_SERVICE
    );
    if (!service) throw new Error('Cloud Run service がありません');
    const request: ProviderCommandInput = {
      worldId: testContext.worldId,
      deploymentId: testContext.deploymentId,
      service: 'http',
      operation: 'Request',
      resourceType: HTTP_ENDPOINT,
      input: { Method: 'GET', Path: '/', Headers: {}, Body: '' },
    };
    expect(
      coreError(() =>
        provider.reduce(
          { ...request, input: { ...request.input, Headers: null } },
          view
        )
      ).code
    ).toBe('ValidationFailed');

    const withoutService = view.resources.filter(
      (resource) => resource.resourceType !== CLOUD_RUN_SERVICE
    );
    const duplicate = {
      ...service,
      resourceId: `${service.resourceId}-duplicate`,
    };
    const cases = [
      { resources: withoutService, code: 'NotFound' },
      { resources: [...view.resources, duplicate], code: 'Conflict' },
      {
        resources: view.resources.map((resource) =>
          resource.resourceId === service.resourceId
            ? { ...resource, status: 'failed' as const }
            : resource
        ),
        code: 'Conflict',
      },
      {
        resources: view.resources.map((resource) =>
          resource.resourceId === service.resourceId
            ? {
                ...resource,
                properties: { ...resource.properties, status: 'Deploying' },
              }
            : resource
        ),
        code: 'Conflict',
      },
      {
        resources: view.resources.map((resource) =>
          resource.resourceId === service.resourceId
            ? {
                ...resource,
                properties: { ...resource.properties, uri: '' },
              }
            : resource
        ),
        code: 'ValidationFailed',
      },
    ] as const;
    for (const testCase of cases) {
      expect(
        coreError(() =>
          provider.reduce(request, { ...view, resources: testCase.resources })
        ).code
      ).toBe(testCase.code);
    }
  });

  it('manifest と compile plan は Cloud Run HTTP Request L4 を宣言する', async () => {
    const provider = new GcpProvider();
    const plan = provider.compile({
      target: {
        provider: 'gcp',
        engine: 'infra-manager',
        entry: 'gcp/terraform/main.tf',
      },
      targetId: 'gcp',
      problemId: 'hello-multicloud',
      templateBody: await helloTerraform(),
      artifacts: [],
    });
    const requestCapability = provider.capabilities.find(
      (capability) => capability.operation === 'Request'
    );
    expect(provider.capabilities).toHaveLength(10);
    expect(requestCapability).toMatchObject({
      service: 'http',
      resourceType: HTTP_ENDPOINT,
      fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
    });
    expect(
      plan.requirements.some(
        (requirement) =>
          requirement.operation === 'Request' &&
          requirement.resourceType === HTTP_ENDPOINT &&
          requirement.fidelity.includes('L4')
      )
    ).toBe(true);
  });

  it('IAM policy を取得し新しい binding を追加する', async () => {
    const testContext = await context();
    const policy = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'GetIamPolicy', CLOUD_RUN_IAM_MEMBER, {}),
      'policy-key'
    );
    expect(Reflect.get(policy, 'bindings')).toHaveLength(1);
    const updated = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'SetIamPolicy', CLOUD_RUN_IAM_MEMBER, {
        role: 'roles/run.invoker',
        member: 'serviceAccount:participant@example.test',
      }),
      'set-policy-key'
    );
    expect(Reflect.get(updated, 'member')).toContain('participant');
  });

  it('未知 resource と壊れた HCL を loud に拒否する', async () => {
    const provider = new GcpProvider();
    expect(() =>
      provider.compile({
        target: {
          provider: 'gcp',
          engine: 'infra-manager',
          entry: 'main.tf',
        },
        targetId: 'gcp',
        problemId: 'invalid',
        templateBody: 'resource "google_unknown" "bad" {}',
        artifacts: [],
      })
    ).toThrow('not supported');
    expect(() => terraformResources('resource "broken" "block" {')).toThrow(
      'not closed'
    );
    expect(() => terraformResources('variable "only" {}')).toThrow(
      'no resource blocks'
    );
    expect(terraformString('name = "hello"', 'name')).toBe('hello');
    expect(terraformString('name = var.name', 'name')).toBeUndefined();
    expect(terraformNumber('max = 4', 'max')).toBe(4);
    expect(terraformNumber('max = var.max', 'max')).toBeUndefined();
    expect(
      terraformResources(`resource "google_cloud_run_v2_service" "commented" {
  # } brace in comment
  name = "brace-}-in-string"
}`)[0]?.body
    ).toContain('brace-}-in-string');
  });

  it('service patch、IAM input、command identity の不正値を拒否する', async () => {
    const testContext = await context();
    const invalidPatches: readonly unknown[] = [
      null,
      {},
      { unknown: true },
      { image: '' },
      { minInstanceCount: -1 },
      { maxInstanceCount: 101 },
      { minInstanceCount: 5, maxInstanceCount: 2 },
    ];
    invalidPatches.forEach((patch, index) => {
      expect(() =>
        testContext.core.executeCommand(
          testContext.worldId,
          command(testContext, 'UpdateService', CLOUD_RUN_SERVICE, {
            id: testContext.serviceId,
            patch,
          }),
          `invalid-gcp-patch-${index}`
        )
      ).toThrow();
    });

    expect(() =>
      testContext.core.executeCommand(
        testContext.worldId,
        command(testContext, 'SetIamPolicy', CLOUD_RUN_IAM_MEMBER, {
          serviceId: 'missing-service',
          role: '',
          member: 1,
        }),
        'invalid-gcp-iam'
      )
    ).toThrow();
    expect(() =>
      testContext.core.executeCommand(
        testContext.worldId,
        command(testContext, 'GetIamPolicy', CLOUD_RUN_IAM_MEMBER, {
          serviceId: 'missing-service',
        }),
        'missing-gcp-iam-service'
      )
    ).toThrow();

    const provider = new GcpProvider();
    expect(() =>
      provider.reduce(
        {
          worldId: testContext.worldId,
          deploymentId: testContext.deploymentId,
          service: 'run',
          operation: 'GetService',
          resourceType: CLOUD_RUN_IAM_MEMBER,
          input: { id: testContext.serviceId },
        },
        {
          world: testContext.core.world(testContext.worldId),
          resources: testContext.store.resources(testContext.worldId),
        }
      )
    ).toThrow('does not support');
  });
});
