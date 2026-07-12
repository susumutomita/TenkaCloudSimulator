import { afterEach, describe, expect, it } from 'bun:test';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  CoreError,
  type ExecuteCommandInput,
  type ProviderCommandInput,
  ProviderRegistry,
  type ProviderTargetPlan,
  type ProviderWorldView,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  AZURE_CONTAINER_APP,
  AZURE_ROLE_ASSIGNMENT,
  AzureProvider,
  HTTP_ENDPOINT,
} from './provider';

const DEFAULT_TARGET_ID = 'default';
const APP_ID_OUTPUT = 'containerAppId';
const ROLE_ID_OUTPUT = 'roleAssignmentId';

interface TestContext {
  readonly directory: string;
  readonly store: SimulationStore;
  readonly core: SimulationCore;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly appId: string;
  readonly roleId: string;
}

const contexts: TestContext[] = [];

afterEach(async () => {
  while (contexts.length > 0) {
    const testContext = contexts.pop();
    if (!testContext) continue;
    testContext.store.close();
    await rm(testContext.directory, { recursive: true, force: true });
  }
});

async function fixture(): Promise<string> {
  return readFile(
    new URL('./fixtures/container-app.bicep', import.meta.url),
    'utf8'
  );
}

async function context(): Promise<TestContext> {
  const directory = await mkdtemp(join(tmpdir(), 'azure-provider-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new AzureProvider()])
  );
  const deploymentId = 'azure-deployment';
  const world = core.createWorld(
    {
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId,
      seed: 'azure-seed',
      virtualTime: '2026-07-12T00:00:00.000Z',
    },
    'azure-world-key'
  );
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'azure-conformance',
      runtime: {
        provider: 'azure',
        engine: 'bicep',
        entry: 'fixtures/container-app.bicep',
      },
      templateBody: await fixture(),
    },
    'azure-deployment-key'
  );
  const targetOutputs = deployment.outputs[DEFAULT_TARGET_ID];
  const appId = targetOutputs?.[APP_ID_OUTPUT];
  const roleId = targetOutputs?.[ROLE_ID_OUTPUT];
  if (!appId || !roleId)
    throw new Error('Azure deployment output がありません');
  const result = {
    directory,
    store,
    core,
    worldId: world.worldId,
    deploymentId,
    appId,
    roleId,
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
  const service =
    operation === 'Probe' || operation === 'Request'
      ? 'http'
      : resourceType === AZURE_ROLE_ASSIGNMENT
        ? 'authorization'
        : 'containerapps';
  return {
    deploymentId: testContext.deploymentId,
    targetId: DEFAULT_TARGET_ID,
    provider: 'azure',
    engine: 'bicep',
    service,
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

describe('Azure Bicep provider の振る舞い', () => {
  it('Container App と Role Assignment を dependency 付きで同じ world へ配備する', async () => {
    const testContext = await context();
    const resources = testContext.store.resources(testContext.worldId);
    const app = resources.find(
      (resource) => resource.resourceType === AZURE_CONTAINER_APP
    );
    const role = resources.find(
      (resource) => resource.resourceType === AZURE_ROLE_ASSIGNMENT
    );
    if (!app || !role) throw new Error('Azure resources がありません');

    expect(resources).toHaveLength(2);
    expect(app.resourceId).toBe(testContext.appId);
    expect(app.properties['status']).toBe('Running');
    expect(app.properties['external']).toBe(true);
    expect(app.properties['targetPort']).toBe(8080);
    expect(role.resourceId).toBe(testContext.roleId);
    expect(role.properties['dependencies']).toEqual([app.resourceId]);
    expect(role.properties['scopeId']).toBe(app.resourceId);
    expect(
      resources.some((resource) => '$bicepOutputs' in resource.properties)
    ).toBe(false);
    expect(
      testContext.core
        .events(testContext.worldId)
        .filter((event) => event.type === 'AzureResourceCreated')
    ).toHaveLength(2);
  });

  it('Container App を read、update、HTTP probe、delete して state を共有する', async () => {
    const testContext = await context();
    const fetched = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'GetContainerApp', AZURE_CONTAINER_APP, {
        id: testContext.appId,
      }),
      'get-container-app'
    );
    expect(Reflect.get(fetched, 'name')).toBe('hello-container-app');

    const updated = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'UpdateContainerApp', AZURE_CONTAINER_APP, {
        id: testContext.appId,
        patch: {
          image: 'mcr.microsoft.com/updated:2',
          minReplicas: 1,
          maxReplicas: 5,
          targetPort: 9090,
          responseStatus: 201,
          responseBody: 'updated response',
        },
      }),
      'update-container-app'
    );
    expect(Reflect.get(updated, 'image')).toContain('updated:2');
    expect(Reflect.get(updated, 'maxReplicas')).toBe(5);

    const probed = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Probe', HTTP_ENDPOINT, {
        id: testContext.appId,
      }),
      'probe-container-app'
    );
    expect(probed).toMatchObject({
      status: 201,
      body: 'updated response',
    });
    expect(Reflect.get(probed, 'url')).toContain('.azurecontainerapps.local');

    const deleted = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'DeleteContainerApp', AZURE_CONTAINER_APP, {
        id: testContext.appId,
      }),
      'delete-container-app'
    );
    expect(deleted).toEqual({ id: testContext.appId, deleted: true });
    expect(
      testContext.store
        .resources(testContext.worldId)
        .find((resource) => resource.resourceId === testContext.appId)?.status
    ).toBe('deleted');
  });

  it('Container App endpoint は GET と HEAD を state から返し未対応 method を 405 にする', async () => {
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
      'azure-http-get'
    );
    const head = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Request', HTTP_ENDPOINT, {
        ...input,
        Method: 'HEAD',
      }),
      'azure-http-head'
    );
    const post = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Request', HTTP_ENDPOINT, {
        ...input,
        Method: 'POST',
      }),
      'azure-http-post'
    );
    testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'UpdateContainerApp', AZURE_CONTAINER_APP, {
        id: testContext.appId,
        patch: { responseStatus: 204, responseBody: '' },
      }),
      'azure-http-no-body-update'
    );
    const noBody = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'Request', HTTP_ENDPOINT, input),
      'azure-http-no-body-get'
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
    expect(noBody).toMatchObject({ StatusCode: 204, Body: '' });
    expect(
      testContext.core
        .events(testContext.worldId)
        .filter((event) => event.type === 'AzureContainerAppRequestExecuted')
    ).toHaveLength(4);
  });

  it('Container App endpoint は request 境界と resource cardinality、ready projection を loud に扱う', async () => {
    const testContext = await context();
    const provider = new AzureProvider();
    const view = providerView(testContext);
    const app = view.resources.find(
      (resource) => resource.resourceType === AZURE_CONTAINER_APP
    );
    if (!app) throw new Error('Container App がありません');
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
          { ...request, input: { ...request.input, Path: 'relative' } },
          view
        )
      ).code
    ).toBe('ValidationFailed');

    const withoutApp = view.resources.filter(
      (resource) => resource.resourceType !== AZURE_CONTAINER_APP
    );
    const duplicate = { ...app, resourceId: `${app.resourceId}-duplicate` };
    const cases = [
      { resources: withoutApp, code: 'NotFound' },
      { resources: [...view.resources, duplicate], code: 'Conflict' },
      {
        resources: view.resources.map((resource) =>
          resource.resourceId === app.resourceId
            ? { ...resource, status: 'pending' as const }
            : resource
        ),
        code: 'Conflict',
      },
      {
        resources: view.resources.map((resource) =>
          resource.resourceId === app.resourceId
            ? {
                ...resource,
                properties: { ...resource.properties, status: 'Stopped' },
              }
            : resource
        ),
        code: 'Conflict',
      },
      {
        resources: view.resources.map((resource) =>
          resource.resourceId === app.resourceId
            ? {
                ...resource,
                properties: { ...resource.properties, external: false },
              }
            : resource
        ),
        code: 'NotFound',
      },
      {
        resources: view.resources.map((resource) =>
          resource.resourceId === app.resourceId
            ? {
                ...resource,
                properties: { ...resource.properties, fqdn: '' },
              }
            : resource
        ),
        code: 'ValidationFailed',
      },
    ] as const;
    for (const [index, testCase] of cases.entries()) {
      const found = coreError(() =>
        provider.reduce(request, { ...view, resources: testCase.resources })
      );
      expect(found.code, `endpoint case ${index}`).toBe(testCase.code);
    }
  });

  it('Role Assignment を取得し、同じ Container App scope へ新規 assignment を設定する', async () => {
    const testContext = await context();
    const fetched = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'GetRoleAssignment', AZURE_ROLE_ASSIGNMENT, {
        id: testContext.roleId,
      }),
      'get-role-assignment'
    );
    expect(Reflect.get(fetched, 'principalId')).toBe(
      'participant@example.test'
    );

    const created = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'SetRoleAssignment', AZURE_ROLE_ASSIGNMENT, {
        scopeId: testContext.appId,
        roleDefinitionId: 'ContainerAppContributor',
        principalId: 'operator@example.test',
      }),
      'set-role-assignment'
    );
    const newId = Reflect.get(created, 'id');
    if (typeof newId !== 'string') {
      throw new Error('Role Assignment ID がありません');
    }
    expect(newId).toStartWith(
      `${testContext.appId}/providers/${AZURE_ROLE_ASSIGNMENT}/`
    );
    expect(Reflect.get(created, 'scopeId')).toBe(testContext.appId);
    expect(
      testContext.store
        .resources(testContext.worldId)
        .filter((resource) => resource.resourceType === AZURE_ROLE_ASSIGNMENT)
    ).toHaveLength(2);
    expect(
      testContext.core.deployment(testContext.worldId, testContext.deploymentId)
        .outputs[DEFAULT_TARGET_ID]?.['RoleAssignmentId']
    ).toBe(newId);
  });

  it('manifest identity と compile 結果を固定し、未知 operation を直接拒否する', async () => {
    const provider = new AzureProvider();
    const source = await fixture();
    const input = {
      target: {
        provider: 'azure',
        engine: 'bicep',
        entry: 'fixtures/container-app.bicep',
      },
      targetId: 'azure',
      problemId: 'azure-conformance',
      templateBody: source,
      artifacts: [],
    };
    expect(provider.provider).toBe('azure');
    expect(provider.engines).toEqual(['bicep']);
    expect(provider.capabilities).toHaveLength(10);
    const plan = provider.compile(input);
    expect(plan).toEqual(provider.compile(input));
    expect(
      plan.requirements.some(
        (requirement) =>
          requirement.service === 'http' &&
          requirement.resourceType === HTTP_ENDPOINT &&
          requirement.operation === 'Request' &&
          requirement.fidelity.includes('L4')
      )
    ).toBe(true);
    expect(
      provider
        .compile({
          ...input,
          templateBody: source.replace('external: true', 'external: false'),
        })
        .requirements.some((requirement) => requirement.operation === 'Request')
    ).toBe(false);

    const testContext = await context();
    const unsupported: ProviderCommandInput = {
      worldId: testContext.worldId,
      deploymentId: testContext.deploymentId,
      service: 'containerapps',
      operation: 'UnknownOperation',
      resourceType: AZURE_CONTAINER_APP,
      input: {},
    };
    expect(
      coreError(() => provider.reduce(unsupported, providerView(testContext)))
        .code
    ).toBe('UnsupportedCapability');

    const wrongResource = {
      ...unsupported,
      operation: 'GetContainerApp',
      resourceType: AZURE_ROLE_ASSIGNMENT,
    };
    expect(
      coreError(() => provider.reduce(wrongResource, providerView(testContext)))
        .code
    ).toBe('UnsupportedCapability');
  });

  it('空 plan と壊れた compile output metadata を deploy 前に拒否する', async () => {
    const provider = new AzureProvider();
    const testContext = await context();
    const empty: ProviderTargetPlan = {
      targetId: 'empty',
      provider: 'azure',
      engine: 'bicep',
      requirements: [],
      resources: [],
    };
    expect(
      coreError(() => provider.deploy(empty, providerView(testContext))).code
    ).toBe('ValidationFailed');

    const invalidOutputs: ProviderTargetPlan = {
      ...empty,
      resources: [
        {
          provider: 'azure',
          resourceType: AZURE_CONTAINER_APP,
          resourceId: 'invalid-output-resource',
          properties: { $bicepOutputs: { invalid: 42 } },
        },
      ],
    };
    expect(
      coreError(() =>
        provider.deploy(invalidOutputs, providerView(testContext))
      ).code
    ).toBe('ValidationFailed');

    const noOutputs: ProviderTargetPlan = {
      ...invalidOutputs,
      resources: [
        {
          provider: 'azure',
          resourceType: AZURE_CONTAINER_APP,
          resourceId: 'no-output-resource',
          properties: { status: 'Running' },
        },
      ],
    };
    expect(
      provider.deploy(noOutputs, providerView(testContext)).outputs
    ).toEqual({});
  });

  it('Container App の ID、patch object、field、range、replica 順序を検証する', async () => {
    const testContext = await context();
    const invalidCommands: readonly Readonly<Record<string, unknown>>[] = [
      { id: 1, patch: {} },
      { id: 'missing', patch: {} },
      { id: testContext.appId, patch: null },
      { id: testContext.appId, patch: { unknown: true } },
      { id: testContext.appId, patch: { image: '' } },
      { id: testContext.appId, patch: { targetPort: 0 } },
      { id: testContext.appId, patch: { responseStatus: 99 } },
      { id: testContext.appId, patch: { responseStatus: 199 } },
      { id: testContext.appId, patch: { responseBody: 1 } },
      {
        id: testContext.appId,
        patch: { responseBody: 'x'.repeat(64 * 1024 + 1) },
      },
      { id: testContext.appId, patch: { responseStatus: 204 } },
      {
        id: testContext.appId,
        patch: { minReplicas: 5, maxReplicas: 2 },
      },
    ];

    invalidCommands.forEach((input, index) => {
      const error = coreError(() =>
        testContext.core.executeCommand(
          testContext.worldId,
          command(
            testContext,
            'UpdateContainerApp',
            AZURE_CONTAINER_APP,
            input
          ),
          `invalid-app-${index}`
        )
      );
      expect(['ValidationFailed', 'NotFound']).toContain(error.code);
    });
  });

  it('Role Assignment の ID、scope、role、principal を検証する', async () => {
    const testContext = await context();
    const missing = coreError(() =>
      testContext.core.executeCommand(
        testContext.worldId,
        command(testContext, 'GetRoleAssignment', AZURE_ROLE_ASSIGNMENT, {
          id: 'missing-role',
        }),
        'missing-role'
      )
    );
    expect(missing.code).toBe('NotFound');

    const invalidInputs = [
      {
        scopeId: 'missing-app',
        roleDefinitionId: 'reader',
        principalId: 'participant',
      },
      {
        scopeId: testContext.appId,
        roleDefinitionId: '',
        principalId: 'participant',
      },
      {
        scopeId: testContext.appId,
        roleDefinitionId: 'reader',
        principalId: 1,
      },
      {
        id: 'not-a-child-resource-id',
        scopeId: testContext.appId,
        roleDefinitionId: 'reader',
        principalId: 'participant',
      },
    ];
    invalidInputs.forEach((input, index) => {
      const error = coreError(() =>
        testContext.core.executeCommand(
          testContext.worldId,
          command(
            testContext,
            'SetRoleAssignment',
            AZURE_ROLE_ASSIGNMENT,
            input
          ),
          `invalid-role-${index}`
        )
      );
      expect(['ValidationFailed', 'NotFound']).toContain(error.code);
    });
  });
});
