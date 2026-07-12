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
  CLOUD_RUN_IAM_MEMBER,
  CLOUD_RUN_SERVICE,
  GcpProvider,
  HTTP_ENDPOINT,
} from './provider';
import {
  GCP_REST_DEPLOYMENT_HEADER,
  GCP_REST_TARGET_HEADER,
  GCP_REST_WORLD_HEADER,
  GcpRestGateway,
  GcpRestGatewayError,
} from './rest-gateway';

const ORIGIN = 'https://gcp.simulator.test';
const CREDENTIAL = 'tcsim_0123456789abcdef';
const SERVICE_PATH =
  '/v2/projects/tenka-project/locations/asia-northeast1/services/run-service';

interface TestRuntime {
  readonly directory: string;
  readonly store: SimulationStore;
  readonly core: SimulationCore;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly serviceId: string;
}

const runtimes: TestRuntime[] = [];

afterEach(async () => {
  while (runtimes.length > 0) {
    const testRuntime = runtimes.pop();
    if (!testRuntime) continue;
    testRuntime.store.close();
    await rm(testRuntime.directory, { recursive: true, force: true });
  }
});

function gateway(maxBodyBytes?: number): GcpRestGateway {
  return new GcpRestGateway({
    simulatorOrigin: ORIGIN,
    simulatorCredential: CREDENTIAL,
    ...(maxBodyBytes === undefined ? {} : { maxBodyBytes }),
  });
}

function restHeaders(): Headers {
  return new Headers({
    authorization: `Bearer ${CREDENTIAL}`,
    [GCP_REST_WORLD_HEADER]: 'world-route',
    [GCP_REST_DEPLOYMENT_HEADER]: 'deployment-route',
  });
}

function restRequest(
  path: string,
  method = 'GET',
  body?: BodyInit,
  mutateHeaders?: (headers: Headers) => void
): Request {
  const headers = restHeaders();
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
): Promise<GcpRestGatewayError> {
  try {
    await operation();
  } catch (error) {
    if (error instanceof GcpRestGatewayError) return error;
    throw error;
  }
  throw new Error('GcpRestGatewayError が発生しませんでした');
}

async function fixture(): Promise<string> {
  return readFile(
    new URL('../test/fixtures/hello-multicloud/main.tf', import.meta.url),
    'utf8'
  );
}

async function runtime(): Promise<TestRuntime> {
  const directory = await mkdtemp(join(tmpdir(), 'gcp-rest-gateway-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new GcpProvider()])
  );
  const deploymentId = 'gcp-rest-deployment';
  const world = core.createWorld(
    {
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId,
    },
    'gcp-rest-world-key'
  );
  core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'gcp-rest-conformance',
      runtime: {
        provider: 'gcp',
        engine: 'infra-manager',
        entry: 'test/fixtures/hello-multicloud/main.tf',
      },
      templateBody: await fixture(),
    },
    'gcp-rest-deployment-key'
  );
  const serviceId = store
    .resources(world.worldId)
    .find(
      (resource) => resource.resourceType === CLOUD_RUN_SERVICE
    )?.resourceId;
  if (!serviceId) throw new Error('Cloud Run service がありません');
  const result = {
    directory,
    store,
    core,
    worldId: world.worldId,
    deploymentId,
    serviceId,
  };
  runtimes.push(result);
  return result;
}

function servicePath(testRuntime: TestRuntime): string {
  return `/v2/projects/tenka-project/locations/asia-northeast1/services/${testRuntime.serviceId}`;
}

function routedRequest(
  testRuntime: TestRuntime,
  path: string,
  method = 'GET',
  body?: BodyInit
): Request {
  return restRequest(path, method, body, (headers) => {
    headers.set(GCP_REST_WORLD_HEADER, testRuntime.worldId);
    headers.set(GCP_REST_DEPLOYMENT_HEADER, testRuntime.deploymentId);
  });
}

describe('GCP native REST gateway の振る舞い', () => {
  it('Cloud Run v2 GET/PATCH/DELETE と probe を同じ SQLite world へ接続する', async () => {
    const testRuntime = await runtime();
    const nativeGateway = gateway();
    const path = servicePath(testRuntime);
    const fullName = path.slice('/v2/'.length);
    const updateMask = [
      'template.containers',
      'template.scaling.minInstanceCount',
      'template.scaling.maxInstanceCount',
    ].join(',');
    const patch = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        `${path}?updateMask=${updateMask}`,
        'PATCH',
        JSON.stringify({
          name: fullName,
          template: {
            containers: [{ name: 'web', image: 'us-docker.pkg.dev/app:2' }],
            scaling: { minInstanceCount: 1, maxInstanceCount: 4 },
          },
        })
      )
    );

    expect(patch.worldId).toBe(testRuntime.worldId);
    expect(patch.command).toMatchObject({
      deploymentId: testRuntime.deploymentId,
      targetId: 'default',
      provider: 'gcp',
      engine: 'infra-manager',
      service: 'run',
      operation: 'UpdateService',
      resourceType: CLOUD_RUN_SERVICE,
      input: {
        id: testRuntime.serviceId,
        patch: {
          image: 'us-docker.pkg.dev/app:2',
          minInstanceCount: 1,
          maxInstanceCount: 4,
        },
      },
    });
    testRuntime.core.executeCommand(
      patch.worldId,
      patch.command,
      'gcp-rest-patch'
    );

    const get = await nativeGateway.translate(routedRequest(testRuntime, path));
    const fetched = testRuntime.core.executeCommand(
      get.worldId,
      get.command,
      'gcp-rest-get'
    );
    expect(Reflect.get(fetched, 'image')).toBe('us-docker.pkg.dev/app:2');

    const probe = await nativeGateway.translate(
      routedRequest(testRuntime, `${path}:probe`)
    );
    expect(probe.command).toMatchObject({
      service: 'http',
      operation: 'Probe',
      resourceType: HTTP_ENDPOINT,
    });
    expect(
      Reflect.get(
        testRuntime.core.executeCommand(
          probe.worldId,
          probe.command,
          'gcp-rest-probe'
        ),
        'status'
      )
    ).toBe(200);

    const deletion = await nativeGateway.translate(
      routedRequest(testRuntime, path, 'DELETE')
    );
    expect(deletion.command.operation).toBe('DeleteService');
    testRuntime.core.executeCommand(
      deletion.worldId,
      deletion.command,
      'gcp-rest-delete'
    );
  });

  it('Cloud Run IAM get/set REST action を単一 binding command に変換する', async () => {
    const testRuntime = await runtime();
    const nativeGateway = gateway();
    const path = servicePath(testRuntime);
    const setPolicy = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        `${path}:setIamPolicy`,
        'POST',
        JSON.stringify({
          policy: {
            bindings: [
              {
                role: 'roles/run.invoker',
                members: ['serviceAccount:participant@example.test'],
              },
            ],
          },
        })
      )
    );

    expect(setPolicy.command).toMatchObject({
      service: 'run',
      operation: 'SetIamPolicy',
      resourceType: CLOUD_RUN_IAM_MEMBER,
      input: {
        serviceId: testRuntime.serviceId,
        role: 'roles/run.invoker',
        member: 'serviceAccount:participant@example.test',
      },
    });
    testRuntime.core.executeCommand(
      setPolicy.worldId,
      setPolicy.command,
      'gcp-rest-set-iam'
    );

    const getPolicy = await nativeGateway.translate(
      routedRequest(testRuntime, `${path}:getIamPolicy`, 'POST', '{}')
    );
    const policy = testRuntime.core.executeCommand(
      getPolicy.worldId,
      getPolicy.command,
      'gcp-rest-get-iam'
    );
    expect(Reflect.get(policy, 'bindings')).toHaveLength(2);

    const getWithoutBody = await nativeGateway.translate(
      routedRequest(testRuntime, `${path}:getIamPolicy`, 'POST')
    );
    expect(getWithoutBody.command.operation).toBe('GetIamPolicy');
  });

  it('simulator credential と world/deployment routing header を必須にする', async () => {
    const nativeGateway = gateway();
    const requests = {
      missing: restRequest(SERVICE_PATH, 'GET', undefined, (headers) =>
        headers.delete('authorization')
      ),
      oauth: restRequest(SERVICE_PATH, 'GET', undefined, (headers) =>
        headers.set('authorization', 'Bearer ya29.real-google-oauth-token')
      ),
      wrongSimulator: restRequest(SERVICE_PATH, 'GET', undefined, (headers) =>
        headers.set('authorization', 'Bearer tcsim_aaaaaaaaaaaaaaaa')
      ),
      apiKey: restRequest(SERVICE_PATH, 'GET', undefined, (headers) =>
        headers.set('x-goog-api-key', 'real-api-key')
      ),
      missingWorld: restRequest(SERVICE_PATH, 'GET', undefined, (headers) =>
        headers.delete(GCP_REST_WORLD_HEADER)
      ),
      missingDeployment: restRequest(
        SERVICE_PATH,
        'GET',
        undefined,
        (headers) => headers.delete(GCP_REST_DEPLOYMENT_HEADER)
      ),
      invalidTarget: restRequest(SERVICE_PATH, 'GET', undefined, (headers) =>
        headers.set(GCP_REST_TARGET_HEADER, '../target')
      ),
    };

    const missing = await capturedError(() =>
      nativeGateway.translate(requests.missing)
    );
    const oauth = await capturedError(() =>
      nativeGateway.translate(requests.oauth)
    );
    const wrong = await capturedError(() =>
      nativeGateway.translate(requests.wrongSimulator)
    );
    const apiKey = await capturedError(() =>
      nativeGateway.translate(requests.apiKey)
    );
    expect(missing).toMatchObject({
      code: 'UnauthorizedOperation',
      status: 401,
    });
    expect(oauth.message).toContain('OAuth credentials');
    expect(wrong.message).toContain('credential is invalid');
    expect(apiKey.message).toContain('real Google credentials');

    for (const request of [
      requests.missingWorld,
      requests.missingDeployment,
      requests.invalidTarget,
    ]) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect(error).toMatchObject({ code: 'ValidationFailed', status: 400 });
    }
  });

  it('gateway option、origin、credential query、URL 長を境界で拒否する', async () => {
    const invalidOptions = [
      { simulatorOrigin: 'not-a-url', simulatorCredential: CREDENTIAL },
      {
        simulatorOrigin: `${ORIGIN}/google`,
        simulatorCredential: CREDENTIAL,
      },
      { simulatorOrigin: ORIGIN, simulatorCredential: 'ya29.oauth' },
      {
        simulatorOrigin: ORIGIN,
        simulatorCredential: CREDENTIAL,
        maxBodyBytes: 0,
      },
    ];
    for (const options of invalidOptions) {
      expect(() => new GcpRestGateway(options)).toThrow(GcpRestGatewayError);
    }

    const foreign = new Request(`https://run.googleapis.com${SERVICE_PATH}`, {
      headers: restHeaders(),
    });
    expect((await capturedError(() => gateway().translate(foreign))).code).toBe(
      'UnauthorizedOperation'
    );
    expect(
      (
        await capturedError(() =>
          gateway().translate(restRequest(`${SERVICE_PATH}?access_token=real`))
        )
      ).message
    ).toContain('query credentials');
    const longPath = `/v2/projects/${'a'.repeat(4100)}/locations/region/services/service`;
    expect(
      (await capturedError(() => gateway().translate(restRequest(longPath))))
        .code
    ).toBe('ValidationFailed');
  });

  it('unknown path、segment、method、query を loud に拒否する', async () => {
    const nativeGateway = gateway();
    const invalidRequests = [
      restRequest('/v2/projects/project/locations/location/jobs/job'),
      restRequest('/v2/projects/%ZZ/locations/location/services/service'),
      restRequest('/v2/projects/UPPER/locations/location/services/service'),
      restRequest('/v2/projects/project/locations/location/services/1service'),
      restRequest(SERVICE_PATH, 'POST'),
      restRequest(`${SERVICE_PATH}?view=FULL`),
      restRequest(`${SERVICE_PATH}:getIamPolicy`, 'GET'),
      restRequest(`${SERVICE_PATH}:setIamPolicy`, 'GET'),
      restRequest(`${SERVICE_PATH}:probe`, 'POST'),
      restRequest(`${SERVICE_PATH}:probe?view=FULL`),
      restRequest(`${SERVICE_PATH}:getIamPolicy?view=FULL`, 'POST'),
    ];

    for (const request of invalidRequests) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }

    const explicitTarget = restRequest(
      SERVICE_PATH,
      'GET',
      undefined,
      (headers) => headers.set(GCP_REST_TARGET_HEADER, 'gcp-target')
    );
    expect(
      (await nativeGateway.translate(explicitTarget)).command.targetId
    ).toBe('gcp-target');
  });

  it('Cloud Run PATCH の JSON、byte limit、shape、updateMask を検証する', async () => {
    const nativeGateway = gateway(160);
    const validImageBody = JSON.stringify({
      template: { containers: [{ image: 'registry.test/app:2' }] },
    });
    const invalidRequests = [
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        '{}',
        (headers) => headers.set('content-type', 'text/plain')
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        ''
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        new Uint8Array([255])
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        '{'
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        '[]'
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        JSON.stringify({
          type: 'service_account',
          private_key: 'real-private-key',
        })
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        JSON.stringify({ unknown: true })
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        JSON.stringify({
          name: 'projects/other/locations/other/services/other',
          template: {},
        })
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        JSON.stringify({ template: null })
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        JSON.stringify({ template: { unknown: true } })
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        JSON.stringify({ template: { containers: [] } })
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        JSON.stringify({ template: { containers: [{ image: '' }] } })
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.scaling.minInstanceCount,template.scaling.maxInstanceCount`,
        'PATCH',
        JSON.stringify({
          template: {
            scaling: { minInstanceCount: 5, maxInstanceCount: 2 },
          },
        })
      ),
      restRequest(`${SERVICE_PATH}`, 'PATCH', validImageBody),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers&updateMask=template.containers`,
        'PATCH',
        validImageBody
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers&view=FULL`,
        'PATCH',
        validImageBody
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.unknown`,
        'PATCH',
        validImageBody
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers,template.containers`,
        'PATCH',
        validImageBody
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.scaling.maxInstanceCount`,
        'PATCH',
        validImageBody
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        'x'.repeat(161)
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        validImageBody,
        (headers) => headers.set('content-length', 'invalid')
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        validImageBody,
        (headers) => headers.set('content-length', '161')
      ),
      restRequest(
        `${SERVICE_PATH}?updateMask=template.containers`,
        'PATCH',
        validImageBody,
        (headers) => headers.set('content-length', '1')
      ),
      restRequest(SERVICE_PATH, 'DELETE', '{}'),
    ];

    for (const request of invalidRequests) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect([
        'ValidationFailed',
        'QuotaExceeded',
        'UnauthorizedOperation',
        'UnsupportedCapability',
      ]).toContain(error.code);
    }

    const consumed = restRequest(
      `${SERVICE_PATH}?updateMask=template.containers`,
      'PATCH',
      validImageBody
    );
    await consumed.text();
    expect(
      (await capturedError(() => nativeGateway.translate(consumed))).message
    ).toContain('cannot be read');
  });

  it('IAM policy body は単一 binding と単一 member だけを許可する', async () => {
    const nativeGateway = gateway();
    const path = `${SERVICE_PATH}:setIamPolicy`;
    const invalidBodies: readonly unknown[] = [
      {},
      { policy: null },
      { policy: { etag: 'etag' } },
      { policy: { bindings: [] } },
      { policy: { bindings: [null] } },
      { policy: { bindings: [{ unknown: true }] } },
      {
        policy: {
          bindings: [{ role: 'roles/run.invoker', members: [] }],
        },
      },
      {
        policy: {
          bindings: [{ role: '', members: ['allUsers'] }],
        },
      },
    ];

    for (const body of invalidBodies) {
      const error = await capturedError(() =>
        nativeGateway.translate(restRequest(path, 'POST', JSON.stringify(body)))
      );
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }

    const getBodyError = await capturedError(() =>
      nativeGateway.translate(
        restRequest(`${SERVICE_PATH}:getIamPolicy`, 'POST', '{"options":{}}')
      )
    );
    expect(getBodyError.code).toBe('UnsupportedCapability');
  });
});
