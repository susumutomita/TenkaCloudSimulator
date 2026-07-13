import { afterEach, describe, expect, it } from 'bun:test';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  CoreError,
  type ExecuteCommandInput,
  type ProviderCommandInput,
  type ProviderCompileInput,
  ProviderRegistry,
  type ProviderTargetPlan,
  type ProviderWorldView,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  APPLICATION_RESOURCE,
  parseApplicationInput,
  storedApplication,
  VERSION_RESOURCE,
} from './application';
import { HTTP_ENDPOINT, SakuraProvider } from './provider';

const DEFAULT_TARGET_ID = 'default';
const APPLICATION_ID_OUTPUT = 'ApplicationId';
const APPLICATION_IMAGE =
  'ghcr.io/example/catalog-app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';

const APPLICATION = {
  name: 'catalog-app',
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
          image: APPLICATION_IMAGE,
          server: 'ghcr.io',
          username: 'participant',
          password: 'registry-secret',
        },
      },
      env: [{ key: 'MODE', value: 'simulation' }],
      secret: [{ key: 'TOKEN', value: 'workload-secret' }],
      probe: { http_get: { path: '/healthz', port: 8080 } },
    },
  ],
};

interface TestContext {
  readonly directory: string;
  readonly store: SimulationStore;
  readonly core: SimulationCore;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly applicationId: string;
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

async function context(): Promise<TestContext> {
  const directory = await mkdtemp(join(tmpdir(), 'sakura-provider-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new SakuraProvider()])
  );
  const deploymentId = 'deployment-sakura';
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
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'sakura-sample',
      runtime: {
        provider: 'sakura',
        engine: 'apprun',
        entry: 'sakura/application.json',
      },
      templateBody: JSON.stringify(APPLICATION),
    },
    'deployment-key'
  );
  const applicationId =
    deployment.outputs[DEFAULT_TARGET_ID]?.[APPLICATION_ID_OUTPUT];
  if (!applicationId) throw new Error('application output is missing');
  const result = {
    directory,
    store,
    core,
    worldId: world.worldId,
    deploymentId,
    applicationId,
  };
  contexts.push(result);
  return result;
}

function command(
  testContext: TestContext,
  operation: string,
  input: Readonly<Record<string, unknown>>,
  resourceType = APPLICATION_RESOURCE
): ExecuteCommandInput {
  return {
    deploymentId: testContext.deploymentId,
    targetId: 'default',
    provider: 'sakura',
    engine: 'apprun',
    service:
      operation === 'Request' || operation === 'Probe' ? 'http' : 'apprun',
    operation,
    resourceType,
    input,
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

async function asyncCoreError(
  operation: () => Promise<unknown>
): Promise<CoreError> {
  try {
    await operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('CoreError が発生しませんでした');
}

function saveMaterializedWorkload(
  testContext: TestContext,
  endpoint: string,
  declarationOverrides: Readonly<Record<string, unknown>> = {}
): void {
  testContext.store.saveResource({
    worldId: testContext.worldId,
    deploymentId: testContext.deploymentId,
    targetId: DEFAULT_TARGET_ID,
    provider: 'runtime',
    resourceType: 'Runtime::Workload',
    resourceId: 'workload-sakura-hello',
    status: 'ready',
    properties: {
      declaration: {
        id: 'sakura-hello',
        targetId: DEFAULT_TARGET_ID,
        resourceRef: 'BaseUrl',
        image: APPLICATION_IMAGE,
        containerPort: 8080,
        healthPath: '/healthz',
        ...declarationOverrides,
      },
      materialization: { endpoint },
    },
  });
}

function providerView(testContext: TestContext): ProviderWorldView {
  return {
    world: testContext.core.world(testContext.worldId),
    resources: testContext.store.resources(testContext.worldId),
  };
}

describe('Sakura AppRun provider の振る舞い', () => {
  it('公式 create schema を配備し secret を event state へ残さない', async () => {
    const testContext = await context();
    const resources = testContext.store.resources(testContext.worldId);
    expect(resources).toHaveLength(1);
    const resource = resources[0];
    if (!resource) throw new Error('application resource is missing');
    const application = storedApplication(resource.properties);
    expect(application.id).toBe(testContext.applicationId);
    expect(application.public_url).toContain('.apprun.sakura.local');
    expect(
      application.components[0]?.deploy_source.container_registry.password
    ).toBe('[REDACTED]');
    expect(application.components[0]?.secret?.[0]?.value).toBe('[REDACTED]');
    expect(
      JSON.stringify(testContext.core.events(testContext.worldId))
    ).not.toContain('workload-secret');
  });

  it('list と get が同じ application state を返す', async () => {
    const testContext = await context();
    const listed = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'listApplications', {}),
      'list-key'
    );
    expect(Reflect.get(listed, 'total')).toBe(1);
    const fetched = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'getApplication', { id: testContext.applicationId }),
      'get-key'
    );
    expect(Reflect.get(fetched, 'id')).toBe(testContext.applicationId);
  });

  it('patch で version を追加し traffic と packet filter を更新する', async () => {
    const testContext = await context();
    const patched = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'patchApplication', {
        id: testContext.applicationId,
        application: { components: APPLICATION.components, max_scale: 3 },
      }),
      'patch-key'
    );
    const versions = Reflect.get(patched, 'versions');
    if (!Array.isArray(versions)) throw new Error('versions are missing');
    expect(versions).toHaveLength(2);
    const latest = versions[1];
    if (latest === null || typeof latest !== 'object') {
      throw new Error('latest version is missing');
    }
    const latestId = Reflect.get(latest, 'id');
    const latestName = Reflect.get(latest, 'name');
    expect(typeof latestId).toBe('string');
    expect(typeof latestName).toBe('string');
    const fetchedVersion = testContext.core.executeCommand(
      testContext.worldId,
      command(
        testContext,
        'getVersion',
        { id: testContext.applicationId, versionId: latestId },
        VERSION_RESOURCE
      ),
      'version-key'
    );
    expect(Reflect.get(fetchedVersion, 'id')).toBe(latestId);
    const traffic = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'putTraffics', {
        id: testContext.applicationId,
        traffics: [{ version_name: latestName, percent: 100 }],
      }),
      'traffic-key'
    );
    expect(Reflect.get(traffic, 'traffics')).toEqual([
      { version_name: latestName, percent: 100 },
    ]);
    const fetchedTraffic = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'getTraffics', { id: testContext.applicationId }),
      'get-traffic-key'
    );
    expect(Reflect.get(fetchedTraffic, 'traffics')).toEqual(
      Reflect.get(traffic, 'traffics')
    );
    const filter = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'patchPacketFilter', {
        id: testContext.applicationId,
        packet_filter: {
          is_enabled: true,
          settings: [{ from_ip: '198.51.100.0', from_ip_prefix_length: 24 }],
        },
      }),
      'filter-key'
    );
    expect(Reflect.get(filter, 'is_enabled')).toBe(true);
    const fetchedFilter = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'getPacketFilter', {
        id: testContext.applicationId,
      }),
      'get-filter-key'
    );
    expect(fetchedFilter).toEqual(filter);
    const deletedVersion = testContext.core.executeCommand(
      testContext.worldId,
      command(
        testContext,
        'deleteVersion',
        { id: testContext.applicationId, versionId: latestId },
        VERSION_RESOURCE
      ),
      'delete-version-key'
    );
    expect(Reflect.get(deletedVersion, 'deleted')).toBe(true);
  });

  it('API command で別 application を作成して削除する', async () => {
    const testContext = await context();
    const created = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'postApplication', {
        application: { ...APPLICATION, name: 'second-app' },
      }),
      'create-second-key'
    );
    const id = Reflect.get(created, 'id');
    expect(typeof id).toBe('string');
    const deleted = testContext.core.executeCommand(
      testContext.worldId,
      command(testContext, 'deleteApplication', { id }),
      'delete-second-key'
    );
    expect(Reflect.get(deleted, 'deleted')).toBe(true);
    expect(() =>
      testContext.core.executeCommand(
        testContext.worldId,
        command(testContext, 'getApplication', { id }),
        'get-deleted-key'
      )
    ).toThrow('application does not exist');
  });

  it('AppRun Request と Probe は ready な materialized workload の実 HTTP 応答を転送する', async () => {
    const testContext = await context();
    const seen: Array<{ readonly method: string; readonly path: string }> = [];
    const server = Bun.serve({
      hostname: '127.0.0.1',
      port: 0,
      fetch(request) {
        const url = new URL(request.url);
        seen.push({
          method: request.method,
          path: `${url.pathname}${url.search}`,
        });
        if (url.pathname !== '/healthz') {
          return new Response('not found', { status: 404 });
        }
        return new Response(
          JSON.stringify({
            hello: 'sakura',
            language: url.searchParams.get('language'),
          }),
          {
            status: 202,
            headers: { 'content-type': 'application/json; charset=utf-8' },
          }
        );
      },
    });
    saveMaterializedWorkload(testContext, `http://127.0.0.1:${server.port}`);
    const input = {
      Method: 'GET',
      Path: '/healthz?language=ja',
      Headers: { Accept: 'application/json' },
      Body: '',
    };
    try {
      const get = await testContext.core.executeCommandAsync(
        testContext.worldId,
        command(testContext, 'Request', input, HTTP_ENDPOINT),
        'sakura-http-get'
      );
      const head = await testContext.core.executeCommandAsync(
        testContext.worldId,
        command(
          testContext,
          'Request',
          { ...input, Method: 'HEAD' },
          HTTP_ENDPOINT
        ),
        'sakura-http-head'
      );
      const probe = await testContext.core.executeCommandAsync(
        testContext.worldId,
        command(testContext, 'Probe', input, HTTP_ENDPOINT),
        'sakura-http-probe'
      );
      const schemeLikePath = `/http://127.0.0.1:${server.port}/authority-escape`;
      const schemeLike = await testContext.core.executeCommandAsync(
        testContext.worldId,
        command(
          testContext,
          'Request',
          { ...input, Path: schemeLikePath },
          HTTP_ENDPOINT
        ),
        'sakura-http-scheme-like-path'
      );
      const backslashEscape = await asyncCoreError(() =>
        testContext.core.executeCommandAsync(
          testContext.worldId,
          command(
            testContext,
            'Request',
            {
              ...input,
              Path: `/\\127.0.0.1:${server.port}/authority-escape`,
            },
            HTTP_ENDPOINT
          ),
          'sakura-http-backslash-authority'
        )
      );
      const post = await asyncCoreError(() =>
        testContext.core.executeCommandAsync(
          testContext.worldId,
          command(
            testContext,
            'Request',
            { ...input, Method: 'POST' },
            HTTP_ENDPOINT
          ),
          'sakura-http-post'
        )
      );
      const body = Reflect.get(get, 'Body');
      if (typeof body !== 'string') throw new Error('AppRun body がありません');

      expect(get).toMatchObject({
        StatusCode: 202,
        Headers: { 'content-type': 'application/json; charset=utf-8' },
      });
      expect(JSON.parse(body)).toEqual({ hello: 'sakura', language: 'ja' });
      expect(head).toMatchObject({ StatusCode: 202, Body: '' });
      expect(probe).toMatchObject({ StatusCode: 202 });
      expect(schemeLike).toMatchObject({ StatusCode: 404, Body: 'not found' });
      expect(backslashEscape.code).toBe('ValidationFailed');
      expect(post.code).toBe('UnsupportedCapability');
      expect(seen).toEqual([
        { method: 'GET', path: '/healthz?language=ja' },
        { method: 'HEAD', path: '/healthz?language=ja' },
        { method: 'GET', path: '/healthz?language=ja' },
        { method: 'GET', path: schemeLikePath },
      ]);
      expect(
        testContext.core
          .events(testContext.worldId)
          .filter((event) => event.type === 'SakuraApplicationRequestExecuted')
      ).toHaveLength(4);
    } finally {
      await server.stop(true);
    }
  });

  it('AppRun endpoint は request 境界と resource cardinality、ready projection を loud に扱う', async () => {
    const testContext = await context();
    const provider = new SakuraProvider();
    const view = providerView(testContext);
    const request: ProviderCommandInput = {
      worldId: testContext.worldId,
      deploymentId: testContext.deploymentId,
      service: 'http',
      operation: 'Request',
      resourceType: HTTP_ENDPOINT,
      input: { Method: 'GET', Path: '/', Headers: {}, Body: '' },
    };
    expect(coreError(() => provider.reduce(request, view)).code).toBe(
      'UnsupportedCapability'
    );
    expect(
      coreError(() => provider.reduce({ ...request, service: 'apprun' }, view))
        .code
    ).toBe('UnsupportedCapability');
    expect(
      (
        await asyncCoreError(() =>
          provider.reduceAsync(
            { ...request, input: { ...request.input, Body: 1 } },
            view
          )
        )
      ).code
    ).toBe('ValidationFailed');
    expect(
      (await asyncCoreError(() => provider.reduceAsync(request, view))).code
    ).toBe('NotFound');

    saveMaterializedWorkload(testContext, 'http://127.0.0.1:65535', {
      image: `ghcr.io/example/other@sha256:${'b'.repeat(64)}`,
    });
    expect(
      (
        await asyncCoreError(() =>
          provider.reduceAsync(request, providerView(testContext))
        )
      ).code
    ).toBe('Conflict');
  });

  it('invalid application と unsupported operation を loud に拒否する', async () => {
    expect(() => parseApplicationInput({ ...APPLICATION, port: 8012 })).toThrow(
      'port is reserved'
    );
    expect(() =>
      parseApplicationInput({ ...APPLICATION, min_scale: 3, max_scale: 2 })
    ).toThrow('min_scale must not exceed max_scale');
    const testContext = await context();
    expect(() =>
      testContext.core.executeCommand(
        testContext.worldId,
        command(testContext, 'unknownOperation', {}),
        'unknown-key'
      )
    ).toThrow(CoreError);
  });

  it('provider contract は capability を公開し不正 JSON、空 plan、未知 operation を直接拒否する', async () => {
    const provider = new SakuraProvider();
    expect(provider.provider).toBe('sakura');
    expect(provider.engines).toEqual(['apprun']);
    expect(provider.capabilities).toHaveLength(14);
    expect(
      provider.capabilities.map((capability) => capability.operation)
    ).toContain('patchPacketFilter');
    expect(
      provider.capabilities.find(
        (capability) => capability.operation === 'Request'
      )
    ).toMatchObject({
      service: 'http',
      resourceType: HTTP_ENDPOINT,
      fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
    });
    expect(
      provider.capabilities.find(
        (capability) => capability.operation === 'Probe'
      )
    ).toMatchObject({ service: 'http', resourceType: HTTP_ENDPOINT });

    const compileInput: ProviderCompileInput = {
      target: {
        provider: 'sakura',
        engine: 'apprun',
        entry: 'sakura/application.json',
      },
      targetId: 'default',
      problemId: 'invalid-json',
      templateBody: '{invalid',
      artifacts: [],
    };
    expect(coreError(() => provider.compile(compileInput)).code).toBe(
      'ValidationFailed'
    );

    const validPlan = provider.compile({
      ...compileInput,
      problemId: 'sakura-sample',
      templateBody: JSON.stringify(APPLICATION),
    });
    expect(
      validPlan.requirements.some(
        (requirement) => requirement.resourceType === HTTP_ENDPOINT
      )
    ).toBe(false);

    const workloadPlan = provider.compile({
      ...compileInput,
      problemId: 'sakura-sample',
      templateBody: JSON.stringify(APPLICATION),
      simulationOverlay: {
        schemaVersion: '1',
        workloads: [
          {
            id: 'sakura-hello',
            targetId: 'default',
            resourceRef: 'BaseUrl',
            image: APPLICATION_IMAGE,
            containerPort: 8080,
            healthPath: '/healthz',
          },
        ],
      },
    });
    expect(
      workloadPlan.requirements
        .filter((requirement) => requirement.resourceType === HTTP_ENDPOINT)
        .map((requirement) => requirement.operation)
    ).toEqual([]);
    expect(() =>
      provider.compile({
        ...compileInput,
        templateBody: JSON.stringify(APPLICATION),
        simulationOverlay: {
          schemaVersion: '1',
          workloads: [
            {
              id: 'sakura-hello',
              targetId: 'default',
              resourceRef: 'WrongOutput',
              image: APPLICATION_IMAGE,
              containerPort: 8080,
              healthPath: '/healthz',
            },
          ],
        },
      })
    ).toThrow('does not match the application descriptor');
    expect(() =>
      provider.compile({
        ...compileInput,
        templateBody: JSON.stringify(APPLICATION),
        simulationOverlay: {
          schemaVersion: '1',
          workloads: [
            {
              id: 'sakura-hello',
              targetId: 'default',
              resourceRef: 'BaseUrl',
              image: `ghcr.io/example/other@sha256:${'b'.repeat(64)}`,
              containerPort: 8080,
              healthPath: '/healthz',
            },
          ],
        },
      })
    ).toThrow('does not match the application descriptor');

    const testContext = await context();
    const emptyPlan: ProviderTargetPlan = {
      targetId: 'default',
      provider: 'sakura',
      engine: 'apprun',
      requirements: [],
      resources: [],
    };
    expect(
      coreError(() => provider.deploy(emptyPlan, providerView(testContext)))
        .code
    ).toBe('ValidationFailed');

    const unsupported: ProviderCommandInput = {
      worldId: testContext.worldId,
      deploymentId: testContext.deploymentId,
      service: 'apprun',
      operation: 'provider-direct-unknown',
      resourceType: APPLICATION_RESOURCE,
      input: {},
    };
    expect(
      coreError(() => provider.reduce(unsupported, providerView(testContext)))
        .code
    ).toBe('UnsupportedCapability');
  });

  it('application identity、patch、version の validation と conflict を区別する', async () => {
    const testContext = await context();
    const cases: Array<{
      readonly operation: string;
      readonly input: Readonly<Record<string, unknown>>;
      readonly resourceType?: string;
      readonly code: CoreError['code'];
    }> = [
      {
        operation: 'getApplication',
        input: { id: '   ' },
        code: 'ValidationFailed',
      },
      {
        operation: 'patchApplication',
        input: { id: testContext.applicationId, application: null },
        code: 'ValidationFailed',
      },
      {
        operation: 'getVersion',
        input: { id: testContext.applicationId, versionId: 1 },
        resourceType: VERSION_RESOURCE,
        code: 'ValidationFailed',
      },
      {
        operation: 'getVersion',
        input: { id: testContext.applicationId, versionId: 'missing-version' },
        resourceType: VERSION_RESOURCE,
        code: 'NotFound',
      },
      {
        operation: 'deleteVersion',
        input: {
          id: testContext.applicationId,
          versionId: storedApplication(
            testContext.store.resources(testContext.worldId)[0]?.properties
          ).versions[0]?.id,
        },
        resourceType: VERSION_RESOURCE,
        code: 'Conflict',
      },
      {
        operation: 'postApplication',
        input: { application: APPLICATION },
        code: 'Conflict',
      },
    ];

    cases.forEach((testCase, index) => {
      const error = coreError(() =>
        testContext.core.executeCommand(
          testContext.worldId,
          command(
            testContext,
            testCase.operation,
            testCase.input,
            testCase.resourceType
          ),
          `identity-error-${index}`
        )
      );
      expect(error.code).toBe(testCase.code);
    });
  });

  it('traffic payload の container、entry、合計、version 参照を検証する', async () => {
    const testContext = await context();
    const invalidTraffics: readonly unknown[] = [
      { value: 'not-an-array' },
      [null],
      [{ version_name: 'version', percent: 1.5 }],
      [{ version_name: 'version', percent: 50 }],
      [{ version_name: 'unknown-version', percent: 100 }],
    ];

    invalidTraffics.forEach((traffics, index) => {
      const error = coreError(() =>
        testContext.core.executeCommand(
          testContext.worldId,
          command(testContext, 'putTraffics', {
            id: testContext.applicationId,
            traffics,
          }),
          `traffic-error-${index}`
        )
      );
      expect(error.code).toBe('ValidationFailed');
    });
  });

  it('packet filter payload の container、shape、setting、prefix を検証する', async () => {
    const testContext = await context();
    const invalidFilters: readonly unknown[] = [
      null,
      { is_enabled: 'yes', settings: [] },
      { is_enabled: true, settings: [null] },
      {
        is_enabled: true,
        settings: [{ from_ip: '198.51.100.0', from_ip_prefix_length: 33 }],
      },
    ];

    invalidFilters.forEach((packetFilter, index) => {
      const error = coreError(() =>
        testContext.core.executeCommand(
          testContext.worldId,
          command(testContext, 'patchPacketFilter', {
            id: testContext.applicationId,
            packet_filter: packetFilter,
          }),
          `packet-filter-error-${index}`
        )
      );
      expect(error.code).toBe('ValidationFailed');
    });
  });
});
