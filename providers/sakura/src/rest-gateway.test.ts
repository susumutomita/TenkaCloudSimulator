import { afterEach, describe, expect, it } from 'bun:test';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { APPLICATION_RESOURCE, VERSION_RESOURCE } from './application';
import { SakuraProvider } from './provider';
import {
  SAKURA_APPRUN_API_BASE_PATH,
  SAKURA_APPRUN_DEPLOYMENT_HEADER,
  SAKURA_APPRUN_TARGET_HEADER,
  SAKURA_APPRUN_WORLD_HEADER,
  SakuraAppRunGateway,
  SakuraAppRunGatewayError,
} from './rest-gateway';

const ORIGIN = 'https://sakura.simulator.test';
const CREDENTIAL = 'tcsim_0123456789abcdef:tcsim_fedcba9876543210';
const APPLICATIONS_PATH = `${SAKURA_APPRUN_API_BASE_PATH}/applications`;

const APPLICATION = {
  name: 'base-app',
  timeout_seconds: 60,
  port: 8080,
  min_scale: 0,
  max_scale: 2,
  scale_target_concurrency: 100,
  components: [
    {
      name: 'web',
      max_cpu: '0.5',
      max_memory: '1Gi',
      deploy_source: {
        container_registry: {
          image: 'registry.example/app:1',
          server: 'registry.example',
          username: 'participant',
          password: 'registry-secret',
        },
      },
      env: [{ key: 'MODE', value: 'simulation' }],
      secret: [{ key: 'TOKEN', value: 'workload-secret' }],
      probe: {
        http_get: {
          path: '/healthz',
          port: 8080,
          headers: [{ name: 'X-Health', value: 'ready' }],
        },
      },
    },
  ],
};

interface TestRuntime {
  readonly directory: string;
  readonly store: SimulationStore;
  readonly core: SimulationCore;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly applicationId: string;
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

function basicCredential(value = CREDENTIAL): string {
  return `Basic ${btoa(value)}`;
}

function gateway(maxBodyBytes?: number): SakuraAppRunGateway {
  return new SakuraAppRunGateway({
    simulatorOrigin: ORIGIN,
    simulatorCredential: CREDENTIAL,
    ...(maxBodyBytes === undefined ? {} : { maxBodyBytes }),
  });
}

function appRunHeaders(): Headers {
  return new Headers({
    authorization: basicCredential(),
    [SAKURA_APPRUN_WORLD_HEADER]: 'world-route',
    [SAKURA_APPRUN_DEPLOYMENT_HEADER]: 'deployment-route',
  });
}

function appRunRequest(
  path: string,
  method = 'GET',
  body?: BodyInit,
  mutateHeaders?: (headers: Headers) => void
): Request {
  const headers = appRunHeaders();
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
): Promise<SakuraAppRunGatewayError> {
  try {
    await operation();
  } catch (error) {
    if (error instanceof SakuraAppRunGatewayError) return error;
    throw error;
  }
  throw new Error('SakuraAppRunGatewayError が発生しませんでした');
}

async function runtime(): Promise<TestRuntime> {
  const directory = await mkdtemp(join(tmpdir(), 'sakura-rest-gateway-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new SakuraProvider()])
  );
  const deploymentId = 'sakura-rest-deployment';
  const world = core.createWorld(
    {
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId,
      seed: 'seed',
      virtualTime: '2026-07-12T00:00:00.000Z',
    },
    'sakura-rest-world-key'
  );
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'sakura-rest-conformance',
      runtime: {
        provider: 'sakura',
        engine: 'apprun',
        entry: 'sakura/application.json',
      },
      templateBody: JSON.stringify(APPLICATION),
    },
    'sakura-rest-deployment-key'
  );
  const applicationId = deployment.outputs['default']?.['ApplicationId'];
  if (!applicationId) throw new Error('AppRun application がありません');
  const result = {
    directory,
    store,
    core,
    worldId: world.worldId,
    deploymentId,
    applicationId,
  };
  runtimes.push(result);
  return result;
}

function routedRequest(
  testRuntime: TestRuntime,
  path: string,
  method = 'GET',
  body?: BodyInit,
  mutateHeaders?: (headers: Headers) => void
): Request {
  return appRunRequest(path, method, body, (headers) => {
    headers.set(SAKURA_APPRUN_WORLD_HEADER, testRuntime.worldId);
    headers.set(SAKURA_APPRUN_DEPLOYMENT_HEADER, testRuntime.deploymentId);
    mutateHeaders?.(headers);
  });
}

function responseApplicationId(
  response: Readonly<Record<string, unknown>>
): string {
  const applicationId = Reflect.get(response, 'id');
  if (typeof applicationId !== 'string') {
    throw new Error('作成した application id がありません');
  }
  return applicationId;
}

function expectProbeHeaders(response: Readonly<Record<string, unknown>>): void {
  const components = Reflect.get(response, 'components');
  if (!Array.isArray(components)) {
    throw new Error('作成した components がありません');
  }
  const component = components[0];
  if (component === null || typeof component !== 'object') {
    throw new Error('作成した component がありません');
  }
  const probe = Reflect.get(component, 'probe');
  if (probe === null || typeof probe !== 'object') {
    throw new Error('作成した probe がありません');
  }
  const httpGet = Reflect.get(probe, 'http_get');
  if (httpGet === null || typeof httpGet !== 'object') {
    throw new Error('作成した http_get がありません');
  }
  expect(Reflect.get(httpGet, 'headers')).toEqual([
    { name: 'X-Health', value: 'ready' },
  ]);
}

function versionIdentities(response: Readonly<Record<string, unknown>>): {
  readonly firstId: string;
  readonly latestId: string;
  readonly latestName: string;
} {
  const versions = Reflect.get(response, 'versions');
  if (!Array.isArray(versions) || versions.length !== 2) {
    throw new Error('version がありません');
  }
  const latest = versions.at(-1);
  const first = versions[0];
  if (
    latest === null ||
    typeof latest !== 'object' ||
    first === null ||
    typeof first !== 'object'
  ) {
    throw new Error('version detail がありません');
  }
  const latestId = Reflect.get(latest, 'id');
  const firstId = Reflect.get(first, 'id');
  const latestName = Reflect.get(latest, 'name');
  if (
    typeof latestId !== 'string' ||
    typeof firstId !== 'string' ||
    typeof latestName !== 'string'
  ) {
    throw new Error('version identity がありません');
  }
  return { firstId, latestId, latestName };
}

describe('Sakura AppRun native REST gateway の振る舞い', () => {
  it('公式 application CRUD と list query を同じ SQLite world へ接続して secret を伏せる', async () => {
    const testRuntime = await runtime();
    const nativeGateway = gateway();
    const create = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        APPLICATIONS_PATH,
        'POST',
        JSON.stringify({ ...APPLICATION, name: 'native-app' }),
        (headers) => headers.set(SAKURA_APPRUN_TARGET_HEADER, 'sakura-target')
      )
    );

    expect(create.worldId).toBe(testRuntime.worldId);
    expect(create.command).toMatchObject({
      deploymentId: testRuntime.deploymentId,
      targetId: 'sakura-target',
      provider: 'sakura',
      engine: 'apprun',
      service: 'apprun',
      operation: 'postApplication',
      resourceType: APPLICATION_RESOURCE,
    });
    expect(JSON.stringify(create.command)).not.toContain('registry-secret');
    expect(JSON.stringify(create.command)).not.toContain('workload-secret');
    expect(JSON.stringify(create.command)).toContain('[REDACTED]');
    const created = testRuntime.core.executeCommand(
      create.worldId,
      create.command,
      'sakura-rest-create'
    );
    const applicationId = responseApplicationId(created);
    expectProbeHeaders(created);

    const list = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        `${APPLICATIONS_PATH}?page_num=1&page_size=1&sort_field=name&sort_order=asc`
      )
    );
    expect(list.command.input).toEqual({
      page_num: 1,
      page_size: 1,
      sort_field: 'name',
      sort_order: 'asc',
    });
    const listed = testRuntime.core.executeCommand(
      list.worldId,
      list.command,
      'sakura-rest-list'
    );
    expect(Reflect.get(listed, 'applications')).toHaveLength(1);
    expect(Reflect.get(listed, 'total')).toBe(2);

    const path = `${APPLICATIONS_PATH}/${applicationId}`;
    const get = await nativeGateway.translate(routedRequest(testRuntime, path));
    expect(
      Reflect.get(
        testRuntime.core.executeCommand(
          get.worldId,
          get.command,
          'sakura-rest-get'
        ),
        'id'
      )
    ).toBe(applicationId);

    const patch = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        path,
        'PATCH',
        JSON.stringify({
          timeout_seconds: 61,
          port: 8081,
          min_scale: 1,
          max_scale: 3,
          scale_target_concurrency: 120,
          components: APPLICATION.components,
          all_traffic_available: true,
        })
      )
    );
    expect(JSON.stringify(patch.command)).not.toContain('registry-secret');
    const patched = testRuntime.core.executeCommand(
      patch.worldId,
      patch.command,
      'sakura-rest-patch'
    );
    const { firstId, latestId, latestName } = versionIdentities(patched);
    expect(Reflect.get(patched, 'traffics')).toEqual([
      { version_name: latestName, percent: 100 },
    ]);

    const version = await nativeGateway.translate(
      routedRequest(testRuntime, `${path}/versions/${latestId}`)
    );
    expect(version.command).toMatchObject({
      operation: 'getVersion',
      resourceType: VERSION_RESOURCE,
      input: { id: applicationId, versionId: latestId },
    });
    expect(
      Reflect.get(
        testRuntime.core.executeCommand(
          version.worldId,
          version.command,
          'sakura-rest-version-get'
        ),
        'id'
      )
    ).toBe(latestId);

    const trafficPut = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        `${path}/traffics`,
        'PUT',
        JSON.stringify([{ is_latest_version: true, percent: 100 }])
      )
    );
    expect(trafficPut.command.input).toEqual({
      id: applicationId,
      traffics: [{ is_latest_version: true, percent: 100 }],
    });
    testRuntime.core.executeCommand(
      trafficPut.worldId,
      trafficPut.command,
      'sakura-rest-traffic-put'
    );
    const trafficGet = await nativeGateway.translate(
      routedRequest(testRuntime, `${path}/traffics`)
    );
    expect(
      Reflect.get(
        testRuntime.core.executeCommand(
          trafficGet.worldId,
          trafficGet.command,
          'sakura-rest-traffic-get'
        ),
        'traffics'
      )
    ).toEqual([{ version_name: latestName, percent: 100 }]);

    const filterSettings = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        `${path}/packet_filter`,
        'PATCH',
        JSON.stringify({
          settings: [{ from_ip: '198.51.100.0', from_ip_prefix_length: 24 }],
        })
      )
    );
    testRuntime.core.executeCommand(
      filterSettings.worldId,
      filterSettings.command,
      'sakura-rest-filter-settings'
    );
    const filterEnabled = await nativeGateway.translate(
      routedRequest(
        testRuntime,
        `${path}/packet_filter`,
        'PATCH',
        JSON.stringify({ is_enabled: true })
      )
    );
    testRuntime.core.executeCommand(
      filterEnabled.worldId,
      filterEnabled.command,
      'sakura-rest-filter-enabled'
    );
    const filterGet = await nativeGateway.translate(
      routedRequest(testRuntime, `${path}/packet_filter`)
    );
    expect(
      testRuntime.core.executeCommand(
        filterGet.worldId,
        filterGet.command,
        'sakura-rest-filter-get'
      )
    ).toEqual({
      is_enabled: true,
      settings: [{ from_ip: '198.51.100.0', from_ip_prefix_length: 24 }],
    });

    const versionDelete = await nativeGateway.translate(
      routedRequest(testRuntime, `${path}/versions/${firstId}`, 'DELETE')
    );
    expect(versionDelete.command.operation).toBe('deleteVersion');
    testRuntime.core.executeCommand(
      versionDelete.worldId,
      versionDelete.command,
      'sakura-rest-version-delete'
    );

    const deletion = await nativeGateway.translate(
      routedRequest(testRuntime, path, 'DELETE')
    );
    expect(deletion.command.operation).toBe('deleteApplication');
    testRuntime.core.executeCommand(
      deletion.worldId,
      deletion.command,
      'sakura-rest-delete'
    );
  });

  it('simulator Basic credential と world/deployment routing header を必須にする', async () => {
    const nativeGateway = gateway();
    const malformedUtf8 = btoa(String.fromCharCode(255, 58, 97));
    const requests = {
      missing: appRunRequest(APPLICATIONS_PATH, 'GET', undefined, (headers) =>
        headers.delete('authorization')
      ),
      malformed: appRunRequest(APPLICATIONS_PATH, 'GET', undefined, (headers) =>
        headers.set('authorization', 'Basic !!!')
      ),
      malformedUtf8: appRunRequest(
        APPLICATIONS_PATH,
        'GET',
        undefined,
        (headers) => headers.set('authorization', `Basic ${malformedUtf8}`)
      ),
      missingSeparator: appRunRequest(
        APPLICATIONS_PATH,
        'GET',
        undefined,
        (headers) =>
          headers.set('authorization', basicCredential('tcsim_no_separator'))
      ),
      real: appRunRequest(APPLICATIONS_PATH, 'GET', undefined, (headers) =>
        headers.set(
          'authorization',
          basicCredential(
            '01234567-89ab-cdef-0123-456789abcdef:REALSAKURASECRET'
          )
        )
      ),
      wrongSimulator: appRunRequest(
        APPLICATIONS_PATH,
        'GET',
        undefined,
        (headers) =>
          headers.set(
            'authorization',
            basicCredential('tcsim_aaaaaaaaaaaaaaaa:tcsim_bbbbbbbbbbbbbbbb')
          )
      ),
      missingWorld: appRunRequest(
        APPLICATIONS_PATH,
        'GET',
        undefined,
        (headers) => headers.delete(SAKURA_APPRUN_WORLD_HEADER)
      ),
      missingDeployment: appRunRequest(
        APPLICATIONS_PATH,
        'GET',
        undefined,
        (headers) => headers.delete(SAKURA_APPRUN_DEPLOYMENT_HEADER)
      ),
      invalidTarget: appRunRequest(
        APPLICATIONS_PATH,
        'GET',
        undefined,
        (headers) => headers.set(SAKURA_APPRUN_TARGET_HEADER, '../target')
      ),
    };

    for (const request of [
      requests.missing,
      requests.malformed,
      requests.malformedUtf8,
      requests.missingSeparator,
    ]) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect(error).toMatchObject({
        code: 'UnauthorizedOperation',
        status: 401,
      });
    }
    expect(
      (await capturedError(() => nativeGateway.translate(requests.real)))
        .message
    ).toContain('real Sakura');
    expect(
      (
        await capturedError(() =>
          nativeGateway.translate(requests.wrongSimulator)
        )
      ).message
    ).toContain('credential is invalid');
    for (const request of [
      requests.missingWorld,
      requests.missingDeployment,
      requests.invalidTarget,
    ]) {
      expect(
        (await capturedError(() => nativeGateway.translate(request))).code
      ).toBe('ValidationFailed');
    }
  });

  it('gateway option、origin、credential query、URL 長を境界で拒否する', async () => {
    const invalidOptions = [
      { simulatorOrigin: 'not-a-url', simulatorCredential: CREDENTIAL },
      {
        simulatorOrigin: `${ORIGIN}/apprun`,
        simulatorCredential: CREDENTIAL,
      },
      {
        simulatorOrigin: ORIGIN,
        simulatorCredential: '01234567:real-secret',
      },
      {
        simulatorOrigin: ORIGIN,
        simulatorCredential: CREDENTIAL,
        maxBodyBytes: 0,
      },
    ];
    for (const options of invalidOptions) {
      expect(() => new SakuraAppRunGateway(options)).toThrow(
        SakuraAppRunGatewayError
      );
    }

    const foreign = new Request(
      `https://secure.sakura.ad.jp${APPLICATIONS_PATH}`,
      { headers: appRunHeaders() }
    );
    expect((await capturedError(() => gateway().translate(foreign))).code).toBe(
      'UnauthorizedOperation'
    );
    expect(
      (
        await capturedError(() =>
          gateway().translate(
            appRunRequest(`${APPLICATIONS_PATH}?access_token=real`)
          )
        )
      ).message
    ).toContain('query credentials');
    const longPath = `${APPLICATIONS_PATH}/${'a'.repeat(4100)}`;
    expect(
      (await capturedError(() => gateway().translate(appRunRequest(longPath))))
        .code
    ).toBe('ValidationFailed');
  });

  it('unknown path、segment、method、query を loud に拒否する', async () => {
    const applicationPath = `${APPLICATIONS_PATH}/app_valid`;
    const versionPath = `${applicationPath}/versions/version_valid`;
    const trafficPath = `${applicationPath}/traffics`;
    const filterPath = `${applicationPath}/packet_filter`;
    const invalidRequests = [
      appRunRequest(`${SAKURA_APPRUN_API_BASE_PATH}/user`),
      appRunRequest(`${APPLICATIONS_PATH}/%ZZ`),
      appRunRequest(`${APPLICATIONS_PATH}/%2Fescape`),
      appRunRequest(`${APPLICATIONS_PATH}/!invalid`),
      appRunRequest(APPLICATIONS_PATH, 'DELETE'),
      appRunRequest(applicationPath, 'POST'),
      appRunRequest(versionPath, 'PATCH'),
      appRunRequest(trafficPath, 'POST'),
      appRunRequest(filterPath, 'PUT'),
      appRunRequest(`${applicationPath}?view=full`),
      appRunRequest(`${APPLICATIONS_PATH}?unknown=value`),
      appRunRequest(`${APPLICATIONS_PATH}?page_num=1&page_num=2`),
      appRunRequest(`${APPLICATIONS_PATH}?page_num=0`),
      appRunRequest(`${APPLICATIONS_PATH}?page_num=${'9'.repeat(30)}`),
      appRunRequest(`${APPLICATIONS_PATH}?page_size=101`),
      appRunRequest(`${APPLICATIONS_PATH}?sort_field=unknown`),
      appRunRequest(`${APPLICATIONS_PATH}?sort_order=random`),
      appRunRequest(`${APPLICATIONS_PATH}?view=full`, 'POST', '{}'),
      appRunRequest(APPLICATIONS_PATH, 'GET', '{}'),
      appRunRequest(versionPath, 'DELETE', '{}'),
    ];

    for (const request of invalidRequests) {
      const error = await capturedError(() => gateway().translate(request));
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }

    const defaults = await gateway().translate(
      appRunRequest(APPLICATIONS_PATH)
    );
    expect(defaults.command.input).toEqual({
      page_num: 1,
      page_size: 50,
      sort_field: 'created_at',
      sort_order: 'desc',
    });
  });

  it('JSON、UTF-8、content-length、byte limit を Request 境界で検証する', async () => {
    const nativeGateway = gateway(96);
    const invalidRequests = [
      appRunRequest(APPLICATIONS_PATH, 'POST'),
      appRunRequest(APPLICATIONS_PATH, 'POST', '{}', (headers) =>
        headers.set('content-type', 'text/plain')
      ),
      appRunRequest(APPLICATIONS_PATH, 'POST', new Uint8Array([255])),
      appRunRequest(APPLICATIONS_PATH, 'POST', '{'),
      appRunRequest(APPLICATIONS_PATH, 'POST', 'x'.repeat(97)),
      appRunRequest(APPLICATIONS_PATH, 'POST', '{}', (headers) =>
        headers.set('content-length', 'invalid')
      ),
      appRunRequest(APPLICATIONS_PATH, 'POST', '{}', (headers) =>
        headers.set('content-length', '97')
      ),
      appRunRequest(APPLICATIONS_PATH, 'POST', '{}', (headers) =>
        headers.set('content-length', '1')
      ),
    ];

    for (const request of invalidRequests) {
      const error = await capturedError(() => nativeGateway.translate(request));
      expect(['ValidationFailed', 'QuotaExceeded']).toContain(error.code);
    }

    const consumed = appRunRequest(APPLICATIONS_PATH, 'POST', '{}');
    await consumed.text();
    expect(
      (await capturedError(() => nativeGateway.translate(consumed))).message
    ).toContain('cannot be read');
  });

  it('application create/patch body の supported subset と secret shape を厳密に検証する', async () => {
    const applicationPath = `${APPLICATIONS_PATH}/app_valid`;
    const invalidCreateBodies: readonly unknown[] = [
      [],
      {},
      { ...APPLICATION, unknown: true },
      { ...APPLICATION, timeout_seconds: 0 },
      { ...APPLICATION, components: [null] },
      {
        ...APPLICATION,
        components: [
          { ...APPLICATION.components[0], unsupported_component: true },
        ],
      },
      {
        ...APPLICATION,
        components: [
          {
            ...APPLICATION.components[0],
            deploy_source: { unsupported_source: {} },
          },
        ],
      },
      {
        ...APPLICATION,
        components: [
          {
            ...APPLICATION.components[0],
            deploy_source: {
              container_registry: { image: 'image', action: 'keep' },
            },
          },
        ],
      },
      {
        ...APPLICATION,
        components: [
          { ...APPLICATION.components[0], env: [{ key: 'A', unknown: true }] },
        ],
      },
      {
        ...APPLICATION,
        components: [
          {
            ...APPLICATION.components[0],
            secret: [{ key: 'A', unknown: true }],
          },
        ],
      },
      {
        ...APPLICATION,
        components: [
          {
            ...APPLICATION.components[0],
            probe: { unknown: true },
          },
        ],
      },
      {
        ...APPLICATION,
        components: [
          {
            ...APPLICATION.components[0],
            probe: { http_get: { path: '/', port: 8080, unknown: true } },
          },
        ],
      },
      {
        ...APPLICATION,
        components: [
          {
            ...APPLICATION.components[0],
            probe: {
              http_get: {
                path: '/',
                port: 8080,
                headers: [{ name: 'X-Test', unknown: true }],
              },
            },
          },
        ],
      },
    ];
    for (const body of invalidCreateBodies) {
      const error = await capturedError(() =>
        gateway().translate(
          appRunRequest(APPLICATIONS_PATH, 'POST', JSON.stringify(body))
        )
      );
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }

    const invalidPatches: readonly unknown[] = [
      [],
      {},
      { name: 'rename-is-not-supported' },
      { timeout_seconds: 0 },
      { port: 8012 },
      { min_scale: 4, max_scale: 2 },
      { scale_target_concurrency: 201 },
      { all_traffic_available: 'yes' },
      { components: [] },
      {
        components: [
          {
            ...APPLICATION.components[0],
            deploy_source: { container_registry: { image: '' } },
          },
        ],
      },
    ];
    for (const patch of invalidPatches) {
      const error = await capturedError(() =>
        gateway().translate(
          appRunRequest(applicationPath, 'PATCH', JSON.stringify(patch))
        )
      );
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }
  });

  it('traffic と packet_filter の body shape、件数、値域を厳密に検証する', async () => {
    const applicationPath = `${APPLICATIONS_PATH}/app_valid`;
    const trafficPath = `${applicationPath}/traffics`;
    const filterPath = `${applicationPath}/packet_filter`;
    const invalidTraffics: readonly unknown[] = [
      {},
      [],
      Array.from({ length: 5 }, () => ({
        is_latest_version: true,
        percent: 20,
      })),
      [null],
      [{ is_latest_version: true, percent: 100, unknown: true }],
      [{ version_name: 'v1', is_latest_version: true, percent: 100 }],
      [{ percent: 100 }],
      [{ is_latest_version: false, percent: 100 }],
      [{ is_latest_version: true }],
      [{ is_latest_version: true, percent: 101 }],
      [{ version_name: ' ', percent: 100 }],
      [{ version_name: 'v1', percent: 50 }],
    ];
    for (const body of invalidTraffics) {
      const error = await capturedError(() =>
        gateway().translate(
          appRunRequest(trafficPath, 'PUT', JSON.stringify(body))
        )
      );
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }

    const invalidFilters: readonly unknown[] = [
      [],
      {},
      { unknown: true },
      { is_enabled: 'yes' },
      { settings: {} },
      {
        settings: Array.from({ length: 11 }, () => ({
          from_ip: '192.0.2.1',
          from_ip_prefix_length: 32,
        })),
      },
      { settings: [null] },
      { settings: [{ from_ip: '999.0.2.1', from_ip_prefix_length: 24 }] },
      { settings: [{ from_ip: '192.0.2.1' }] },
      {
        settings: [{ from_ip: '192.0.2.1', from_ip_prefix_length: 33 }],
      },
      {
        settings: [
          {
            from_ip: '192.0.2.1',
            from_ip_prefix_length: 24,
            unknown: true,
          },
        ],
      },
    ];
    for (const body of invalidFilters) {
      const error = await capturedError(() =>
        gateway().translate(
          appRunRequest(filterPath, 'PATCH', JSON.stringify(body))
        )
      );
      expect(['ValidationFailed', 'UnsupportedCapability']).toContain(
        error.code
      );
    }
  });
});
