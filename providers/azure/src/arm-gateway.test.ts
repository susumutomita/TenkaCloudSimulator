import { afterEach, describe, expect, it } from 'bun:test';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  AZURE_ARM_CONTAINER_API_VERSION,
  AZURE_ARM_DEPLOYMENT_HEADER,
  AZURE_ARM_ROLE_API_VERSION,
  AZURE_ARM_TARGET_HEADER,
  AZURE_ARM_WORLD_HEADER,
  AzureArmGateway,
  AzureArmGatewayError,
} from './arm-gateway';
import {
  AZURE_CONTAINER_APP,
  AZURE_ROLE_ASSIGNMENT,
  AzureProvider,
} from './provider';

const ORIGIN = 'https://azure.simulator.test';
const CREDENTIAL = 'tcsim_0123456789abcdef';
const CONTAINER_PATH =
  '/subscriptions/subscription-1/resourceGroups/group-1/providers/Microsoft.App/containerApps/hello-app';
const ROLE_PATH = `${CONTAINER_PATH}/providers/Microsoft.Authorization/roleAssignments/assignment-1`;

interface TestRuntime {
  readonly directory: string;
  readonly store: SimulationStore;
  readonly core: SimulationCore;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly appId: string;
}

const runtimes: TestRuntime[] = [];

afterEach(async () => {
  while (runtimes.length > 0) {
    const runtime = runtimes.pop();
    if (!runtime) continue;
    runtime.store.close();
    await rm(runtime.directory, { recursive: true, force: true });
  }
});

function gateway(maxBodyBytes?: number): AzureArmGateway {
  return new AzureArmGateway({
    simulatorOrigin: ORIGIN,
    simulatorCredential: CREDENTIAL,
    ...(maxBodyBytes === undefined ? {} : { maxBodyBytes }),
  });
}

function armHeaders(): Headers {
  return new Headers({
    authorization: `Bearer ${CREDENTIAL}`,
    [AZURE_ARM_WORLD_HEADER]: 'world-route',
    [AZURE_ARM_DEPLOYMENT_HEADER]: 'deployment-route',
  });
}

function armRequest(
  path: string,
  method = 'GET',
  body?: BodyInit,
  mutateHeaders?: (headers: Headers) => void
): Request {
  const headers = armHeaders();
  if (body !== undefined) headers.set('content-type', 'application/json');
  mutateHeaders?.(headers);
  return new Request(`${ORIGIN}${path}`, {
    method,
    headers,
    ...(body === undefined ? {} : { body }),
  });
}

async function capturedError(
  operation: () => Promise<unknown>
): Promise<AzureArmGatewayError> {
  try {
    await operation();
  } catch (error) {
    if (error instanceof AzureArmGatewayError) return error;
    throw error;
  }
  throw new Error('AzureArmGatewayError が発生しませんでした');
}

async function fixture(): Promise<string> {
  return readFile(
    new URL('./fixtures/container-app.bicep', import.meta.url),
    'utf8'
  );
}

async function runtime(): Promise<TestRuntime> {
  const directory = await mkdtemp(join(tmpdir(), 'azure-arm-gateway-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new AzureProvider()])
  );
  const deploymentId = 'azure-arm-deployment';
  const world = core.createWorld(
    {
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId,
    },
    'azure-arm-world-key'
  );
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'azure-arm-conformance',
      runtime: {
        provider: 'azure',
        engine: 'bicep',
        entry: 'fixtures/container-app.bicep',
      },
      templateBody: await fixture(),
    },
    'azure-arm-deployment-key'
  );
  const appId = deployment.outputs['default']?.['containerAppId'];
  if (!appId) throw new Error('Container App output がありません');
  const result = {
    directory,
    store,
    core,
    worldId: world.worldId,
    deploymentId,
    appId,
  };
  runtimes.push(result);
  return result;
}

function routedRequest(
  testRuntime: TestRuntime,
  path: string,
  method = 'GET',
  body?: BodyInit
): Request {
  return armRequest(path, method, body, (headers) => {
    headers.set(AZURE_ARM_WORLD_HEADER, testRuntime.worldId);
    headers.set(AZURE_ARM_DEPLOYMENT_HEADER, testRuntime.deploymentId);
  });
}

describe('Azure ARM native gateway の振る舞い', () => {
  it('実 Request を Container Apps command に変換して同じ SQLite world を更新する', async () => {
    const testRuntime = await runtime();
    const nativeGateway = gateway();
    const path = `${testRuntime.appId}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`;
    const patchBody = JSON.stringify({
      properties: {
        configuration: { ingress: { targetPort: 9090 } },
        template: {
          containers: [{ name: 'web', image: 'registry.test/app:2' }],
          scale: { minReplicas: 1, maxReplicas: 4 },
        },
      },
    });
    const translatedPatch = await nativeGateway.translate(
      routedRequest(testRuntime, path, 'PATCH', patchBody)
    );

    expect(translatedPatch.worldId).toBe(testRuntime.worldId);
    expect(translatedPatch.command).toMatchObject({
      deploymentId: testRuntime.deploymentId,
      targetId: 'default',
      provider: 'azure',
      engine: 'bicep',
      service: 'containerapps',
      operation: 'UpdateContainerApp',
      resourceType: AZURE_CONTAINER_APP,
      input: {
        id: testRuntime.appId,
        patch: {
          image: 'registry.test/app:2',
          minReplicas: 1,
          maxReplicas: 4,
          targetPort: 9090,
        },
      },
    });
    testRuntime.core.executeCommand(
      translatedPatch.worldId,
      translatedPatch.command,
      'arm-patch'
    );

    const translatedGet = await nativeGateway.translate(
      routedRequest(testRuntime, path)
    );
    const fetched = testRuntime.core.executeCommand(
      translatedGet.worldId,
      translatedGet.command,
      'arm-get'
    );
    expect(Reflect.get(fetched, 'image')).toBe('registry.test/app:2');

    const translatedDelete = await nativeGateway.translate(
      routedRequest(testRuntime, path, 'DELETE')
    );
    expect(translatedDelete.command.operation).toBe('DeleteContainerApp');
    testRuntime.core.executeCommand(
      translatedDelete.worldId,
      translatedDelete.command,
      'arm-delete'
    );
  });

  it('ARM Role Assignment PUT/GET を path identity の Set/Get command に変換する', async () => {
    const testRuntime = await runtime();
    const nativeGateway = gateway();
    const roleId = `${testRuntime.appId}/providers/${AZURE_ROLE_ASSIGNMENT}/native-assignment`;
    const path = `${roleId}?api-version=${AZURE_ARM_ROLE_API_VERSION}`;
    const translatedPut = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        path,
        'PUT',
        JSON.stringify({
          properties: {
            roleDefinitionId: 'ContainerAppContributor',
            principalId: 'participant@example.test',
          },
        })
      )
    );

    expect(translatedPut.command).toMatchObject({
      service: 'authorization',
      operation: 'SetRoleAssignment',
      resourceType: AZURE_ROLE_ASSIGNMENT,
      input: {
        id: roleId,
        scopeId: testRuntime.appId,
      },
    });
    const created = testRuntime.core.executeCommand(
      translatedPut.worldId,
      translatedPut.command,
      'arm-role-put'
    );
    expect(Reflect.get(created, 'id')).toBe(roleId);

    const translatedGet = await nativeGateway.translate(
      routedRequest(testRuntime, path)
    );
    const fetched = testRuntime.core.executeCommand(
      translatedGet.worldId,
      translatedGet.command,
      'arm-role-get'
    );
    expect(Reflect.get(fetched, 'principalId')).toBe(
      'participant@example.test'
    );
  });

  it('simulator credential と world/deployment routing header を必須にする', async () => {
    const nativeGateway = gateway();
    const path = `${CONTAINER_PATH}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`;
    const missingAuthorization = armRequest(path, 'GET', undefined, (headers) =>
      headers.delete('authorization')
    );
    const realBearer = armRequest(path, 'GET', undefined, (headers) =>
      headers.set('authorization', 'Bearer eyJhbGciOiJSUzI1NiJ9.real.azure.jwt')
    );
    const wrongSimulator = armRequest(path, 'GET', undefined, (headers) =>
      headers.set('authorization', 'Bearer tcsim_aaaaaaaaaaaaaaaa')
    );
    const basicAuthorization = armRequest(path, 'GET', undefined, (headers) =>
      headers.set('authorization', 'Basic dXNlcjpwYXNz')
    );
    const missingWorld = armRequest(path, 'GET', undefined, (headers) =>
      headers.delete(AZURE_ARM_WORLD_HEADER)
    );
    const missingDeployment = armRequest(path, 'GET', undefined, (headers) =>
      headers.delete(AZURE_ARM_DEPLOYMENT_HEADER)
    );
    const invalidTarget = armRequest(path, 'GET', undefined, (headers) =>
      headers.set(AZURE_ARM_TARGET_HEADER, '../target')
    );

    const missing = await capturedError(() =>
      nativeGateway.translate(missingAuthorization)
    );
    const real = await capturedError(() => nativeGateway.translate(realBearer));
    const wrong = await capturedError(() =>
      nativeGateway.translate(wrongSimulator)
    );
    const basic = await capturedError(() =>
      nativeGateway.translate(basicAuthorization)
    );
    expect(missing).toMatchObject({
      code: 'UnauthorizedOperation',
      status: 401,
    });
    expect(real.message).toContain('real Azure bearer credentials');
    expect(wrong.message).toContain('credential is invalid');
    expect(basic.code).toBe('UnauthorizedOperation');

    for (const request of [missingWorld, missingDeployment, invalidTarget]) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect(error).toMatchObject({ code: 'ValidationFailed', status: 400 });
    }
  });

  it('gateway option と simulator-owned origin を境界で検証する', async () => {
    const invalidOptions = [
      { simulatorOrigin: 'not-a-url', simulatorCredential: CREDENTIAL },
      {
        simulatorOrigin: `${ORIGIN}/arm`,
        simulatorCredential: CREDENTIAL,
      },
      { simulatorOrigin: ORIGIN, simulatorCredential: 'azure-token' },
      {
        simulatorOrigin: ORIGIN,
        simulatorCredential: CREDENTIAL,
        maxBodyBytes: 0,
      },
    ];
    for (const options of invalidOptions) {
      expect(() => new AzureArmGateway(options)).toThrow(AzureArmGatewayError);
    }

    const foreign = new Request(
      `https://management.azure.com${CONTAINER_PATH}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`,
      { headers: armHeaders() }
    );
    const foreignError = await capturedError(() =>
      gateway().translate(foreign)
    );
    expect(foreignError).toMatchObject({
      code: 'UnauthorizedOperation',
      status: 401,
    });
  });

  it('api-version、ARM path、method、URL 長を strict に検証する', async () => {
    const nativeGateway = gateway();
    const invalidRequests = [
      armRequest(CONTAINER_PATH),
      armRequest(`${CONTAINER_PATH}?api-version=2023-01-01`),
      armRequest(
        `${CONTAINER_PATH}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}&api-version=${AZURE_ARM_CONTAINER_API_VERSION}`
      ),
      armRequest(
        `${CONTAINER_PATH}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}&expand=true`
      ),
      armRequest(
        `/subscriptions/sub/resourceGroups/group/providers/unknown?api-version=1`
      ),
      armRequest(
        `${CONTAINER_PATH}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`,
        'POST'
      ),
      armRequest(
        `${ROLE_PATH}?api-version=${AZURE_ARM_ROLE_API_VERSION}`,
        'PATCH'
      ),
      armRequest(
        `/subscriptions/%ZZ/resourceGroups/group/providers/Microsoft.App/containerApps/app?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`
      ),
      armRequest(
        `/subscriptions/sub/resourceGroups/group/providers/Microsoft.App/containerApps/${'a'.repeat(40)}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`
      ),
      armRequest(
        `${ROLE_PATH.replace('assignment-1', '%2F')}?api-version=${AZURE_ARM_ROLE_API_VERSION}`
      ),
      armRequest(
        `/subscriptions/${'a'.repeat(4100)}/resourceGroups/group/providers/Microsoft.App/containerApps/app?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`
      ),
    ];

    for (const request of invalidRequests) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }

    const explicitTarget = armRequest(
      `${CONTAINER_PATH}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`,
      'GET',
      undefined,
      (headers) => headers.set(AZURE_ARM_TARGET_HEADER, 'azure-target')
    );
    expect(
      (await nativeGateway.translate(explicitTarget)).command.targetId
    ).toBe('azure-target');
  });

  it('JSON body の media type、byte limit、UTF-8、shape を検証する', async () => {
    const path = `${CONTAINER_PATH}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`;
    const nativeGateway = gateway(128);
    const invalidRequests = [
      armRequest(path, 'PATCH', '{}', (headers) =>
        headers.set('content-type', 'text/plain')
      ),
      armRequest(path, 'PATCH', ''),
      armRequest(path, 'PATCH', new Uint8Array([255])),
      armRequest(path, 'PATCH', '{'),
      armRequest(path, 'PATCH', '[]'),
      armRequest(path, 'PATCH', JSON.stringify({ unknown: true })),
      armRequest(path, 'PATCH', JSON.stringify({ properties: null })),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({ properties: { external: true } })
      ),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({ properties: { configuration: null } })
      ),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({
          properties: { configuration: { unsupported: true } },
        })
      ),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({
          properties: { configuration: { ingress: { targetPort: 0 } } },
        })
      ),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({
          properties: { template: { containers: [] } },
        })
      ),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({
          properties: { template: { containers: [{ unknown: true }] } },
        })
      ),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({
          properties: { template: { containers: [{ image: '' }] } },
        })
      ),
      armRequest(
        path,
        'PATCH',
        JSON.stringify({
          properties: {
            template: { scale: { minReplicas: 5, maxReplicas: 2 } },
          },
        })
      ),
      armRequest(path, 'PATCH', 'x'.repeat(129)),
      armRequest(path, 'PATCH', '{}', (headers) =>
        headers.set('content-length', 'invalid')
      ),
      armRequest(path, 'PATCH', '{}', (headers) =>
        headers.set('content-length', '129')
      ),
      armRequest(path, 'PATCH', '{}', (headers) =>
        headers.set('content-length', '1')
      ),
      armRequest(path, 'DELETE', '{}'),
    ];

    for (const request of invalidRequests) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect([
        'ValidationFailed',
        'QuotaExceeded',
        'UnsupportedCapability',
      ]).toContain(error.code);
    }

    const consumed = armRequest(
      path,
      'PATCH',
      JSON.stringify({
        properties: {
          template: { containers: [{ image: 'registry.test/app:3' }] },
        },
      })
    );
    await consumed.text();
    expect(
      (await capturedError(() => nativeGateway.translate(consumed))).message
    ).toContain('cannot be read');
  });

  it('Role Assignment body は対象 property と必須文字列だけを許可する', async () => {
    const path = `${ROLE_PATH}?api-version=${AZURE_ARM_ROLE_API_VERSION}`;
    const nativeGateway = gateway();
    const invalidBodies: readonly unknown[] = [
      {},
      { properties: null },
      { properties: { principalType: 'ServicePrincipal' } },
      { properties: { principalId: 'participant' } },
      {
        properties: {
          roleDefinitionId: 'reader',
          principalId: '',
        },
      },
    ];

    for (const body of invalidBodies) {
      const error = await capturedError(() =>
        nativeGateway.translate(armRequest(path, 'PUT', JSON.stringify(body)))
      );
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }
  });
});
