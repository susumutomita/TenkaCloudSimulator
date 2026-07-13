import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import {
  chmod,
  lstat,
  mkdir,
  mkdtemp,
  realpath,
  rm,
  symlink,
  writeFile,
} from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  assertSimulatorCapabilities,
  assertSimulatorDeploymentResponse,
  assertSimulatorWorldResponse,
  SIMULATOR_PROTOCOL_VERSION,
} from '@tenkacloud/simulator-contracts';
import {
  deterministicId,
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  AWS_NATIVE_DEPLOYMENT_HEADER,
  AWS_NATIVE_TARGET_HEADER,
  AWS_NATIVE_WORLD_HEADER,
} from '@tenkacloud/simulator-provider-aws';
import { LaunchTokenAuthority } from '../src/auth';
import {
  createSimulatorRuntime,
  needsPrivateDirectoryModeCorrection,
  type SimulatorRuntime,
  type SimulatorRuntimeEnvironment,
  workloadPolicy,
} from '../src/runtime';

const WORKLOAD_IMAGE = `busybox@sha256:${'a'.repeat(64)}`;

function workloadEnvironment(): Readonly<Partial<SimulatorRuntimeEnvironment>> {
  return {
    TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES: JSON.stringify([
      WORKLOAD_IMAGE,
    ]),
    TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES: '134217728',
    TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU: '500',
    TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS: '64',
    TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE: WORKLOAD_IMAGE,
  };
}

const SECRET_BYTES = new Uint8Array(32).fill(7);
const SECRET = Buffer.from(SECRET_BYTES).toString('base64url');
const NAMESPACE = {
  tenantId: 'tenant-runtime',
  eventId: 'event-runtime',
  teamId: 'team-runtime',
  deploymentId: 'deployment-runtime',
};

const DATA_PLANE_TEMPLATE = JSON.stringify({
  Resources: {
    SearchFunction: {
      Type: 'AWS::Lambda::Function',
      Properties: {
        FunctionName: 'runtime-data-plane-search',
        Runtime: 'nodejs22.x',
        Handler: 'index.handler',
        Role: 'arn:aws:iam::000000000000:role/runtime-data-plane',
        Environment: { Variables: { FLAG: 'TC{query-app-server}' } },
        Code: { ZipFile: 'exports.handler = async (event) => event;' },
      },
    },
    SearchTargetGroup: {
      Type: 'AWS::ElasticLoadBalancingV2::TargetGroup',
      Properties: {
        TargetType: 'lambda',
        Targets: [{ Id: { 'Fn::GetAtt': ['SearchFunction', 'Arn'] } }],
      },
    },
    AlbListener: {
      Type: 'AWS::ElasticLoadBalancingV2::Listener',
      Properties: {
        Port: 80,
        Protocol: 'HTTP',
        DefaultActions: [
          {
            Type: 'fixed-response',
            FixedResponseConfig: {
              StatusCode: '405',
              ContentType: 'text/plain',
              MessageBody: 'QUERY is blocked at the edge',
            },
          },
        ],
      },
    },
    AllowedMethodsRule: {
      Type: 'AWS::ElasticLoadBalancingV2::ListenerRule',
      Properties: {
        ListenerArn: { Ref: 'AlbListener' },
        Priority: 10,
        Conditions: [
          {
            Field: 'http-request-method',
            HttpRequestMethodConfig: {
              Values: ['GET', 'HEAD', 'POST', 'OPTIONS'],
            },
          },
          {
            Field: 'path-pattern',
            PathPatternConfig: { Values: ['/search'] },
          },
        ],
        Actions: [
          { Type: 'forward', TargetGroupArn: { Ref: 'SearchTargetGroup' } },
        ],
      },
    },
  },
  Outputs: { RuleArn: { Value: { Ref: 'AllowedMethodsRule' } } },
});

let directory: string;
let consoleDirectory: string;
let sequence: number;
const runtimes: SimulatorRuntime[] = [];
const servers: Array<{
  stop(closeActiveConnections?: boolean): Promise<void> | void;
}> = [];

function environment(
  overrides: Readonly<Partial<SimulatorRuntimeEnvironment>> = {}
): SimulatorRuntimeEnvironment {
  sequence += 1;
  return {
    TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID: 'TCSIMABCDEFGHIJKLMNO',
    TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL: 'tcsim_azure_runtime_credential',
    TENKACLOUD_SIMULATOR_CONSOLE_DIR: consoleDirectory,
    TENKACLOUD_SIMULATOR_GCP_CREDENTIAL: 'tcsim_gcp_runtime_credential',
    TENKACLOUD_SIMULATOR_LAUNCH_SECRET: SECRET,
    TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL:
      'tcsim_sakura_runtime_token:tcsim_sakura_runtime_secret',
    TENKACLOUD_SIMULATOR_STATE_DIR: join(directory, `state-${sequence}`),
    ...overrides,
  };
}

async function openRuntime(
  overrides: Readonly<Partial<SimulatorRuntimeEnvironment>> = {}
): Promise<SimulatorRuntime> {
  const runtime = await createSimulatorRuntime(environment(overrides));
  runtimes.push(runtime);
  return runtime;
}

function serve(runtime: SimulatorRuntime): Bun.Server<undefined> {
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch: runtime.app.fetch,
  });
  servers.push(server);
  return server;
}

function serveEntrypoint(runtime: SimulatorRuntime) {
  const server = Bun.serve({
    hostname: runtime.host,
    port: runtime.port,
    fetch: runtime.fetch,
    websocket: runtime.websocket,
  });
  servers.push(server);
  return server;
}

function authenticatedHeaders(token: string, json = true): Headers {
  const headers = new Headers({ authorization: `Bearer ${token}` });
  if (json) {
    headers.set('content-type', 'application/json');
    headers.set('x-tenkacloud-simulator-protocol', SIMULATOR_PROTOCOL_VERSION);
  }
  return headers;
}

function awsModifyRuleRequest(
  origin: string,
  worldId: string,
  ruleArn: string
): Request {
  const body = new URLSearchParams({
    Action: 'ModifyRule',
    Version: '2015-12-01',
    RuleArn: ruleArn,
    'Conditions.member.1.Field': 'http-request-method',
    'Conditions.member.1.HttpRequestMethodConfig.Values.member.1': 'GET',
    'Conditions.member.1.HttpRequestMethodConfig.Values.member.2': 'HEAD',
    'Conditions.member.1.HttpRequestMethodConfig.Values.member.3': 'POST',
    'Conditions.member.1.HttpRequestMethodConfig.Values.member.4': 'OPTIONS',
    'Conditions.member.1.HttpRequestMethodConfig.Values.member.5': 'QUERY',
    'Conditions.member.2.Field': 'path-pattern',
    'Conditions.member.2.PathPatternConfig.Values.member.1': '/search',
  }).toString();
  const headers = new Headers({
    'content-type': 'application/x-www-form-urlencoded',
    'x-amz-date': '20260712T010203Z',
    [AWS_NATIVE_WORLD_HEADER]: worldId,
    [AWS_NATIVE_DEPLOYMENT_HEADER]: NAMESPACE.deploymentId,
    [AWS_NATIVE_TARGET_HEADER]: 'default',
  });
  const signedHeaders = ['host', ...headers.keys()].sort();
  const signature = createHash('sha256').update(body).digest('hex');
  headers.set(
    'authorization',
    `AWS4-HMAC-SHA256 Credential=TCSIMABCDEFGHIJKLMNO/20260712/us-east-1/elasticloadbalancing/aws4_request, SignedHeaders=${signedHeaders.join(';')}, Signature=${signature}`
  );
  return new Request(origin, { method: 'POST', headers, body });
}

beforeEach(async () => {
  sequence = 0;
  directory = await realpath(
    await mkdtemp(join(tmpdir(), 'simulator-runtime-'))
  );
  consoleDirectory = join(directory, 'console');
  await mkdir(join(consoleDirectory, 'assets'), { recursive: true });
  await Promise.all([
    writeFile(join(consoleDirectory, 'index.html'), '<main>Simulator</main>'),
    writeFile(join(consoleDirectory, 'route.js'), 'export const route = true;'),
    writeFile(join(consoleDirectory, 'manifest.json'), '{"name":"Simulator"}'),
    writeFile(join(consoleDirectory, 'logo.svg'), '<svg></svg>'),
    writeFile(join(consoleDirectory, 'assets', 'style.css'), 'body{}'),
    writeFile(
      join(consoleDirectory, 'assets', 'data.bin'),
      new Uint8Array([1, 2, 3])
    ),
  ]);
  const outside = join(directory, 'outside.css');
  await writeFile(outside, 'outside');
  await symlink(outside, join(consoleDirectory, 'assets', 'link.css'));
});

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => server.stop(true)));
  for (const runtime of runtimes.splice(0)) runtime.close();
  await rm(directory, { recursive: true, force: true });
});

describe('Simulator runtime entrypoint', () => {
  it('mode 0700 の state directory では bind mount 非互換の chmod を省略する', () => {
    expect(needsPrivateDirectoryModeCorrection(0o40_700)).toBe(false);
    expect(needsPrivateDirectoryModeCorrection(0o40_755)).toBe(true);
    expect(needsPrivateDirectoryModeCorrection(0o40_600)).toBe(true);
    expect(needsPrivateDirectoryModeCorrection(0o42_700)).toBe(true);
  });

  it('実 HTTP で Console asset と認証済み lifecycle API を同じ origin から提供する', async () => {
    const runtime = await openRuntime();
    const server = serve(runtime);
    const origin = server.url.origin;

    const capabilities = await fetch(`${origin}/v1/capabilities`);
    expect(capabilities.status).toBe(200);
    expect((await capabilities.json()).providers).toHaveProperty('aws');

    const consoleIndex = await fetch(`${origin}/console/world-id`);
    expect(consoleIndex.status).toBe(200);
    expect(consoleIndex.headers.get('content-type')).toBe(
      'text/html; charset=utf-8'
    );
    expect(await consoleIndex.text()).toContain('Simulator');
    expect((await fetch(`${origin}/console`)).status).toBe(200);

    const expectedTypes = new Map([
      ['/assets/style.css', 'text/css; charset=utf-8'],
      ['/console/route.js', 'text/javascript; charset=utf-8'],
      ['/console/manifest.json', 'application/json; charset=utf-8'],
      ['/console/logo.svg', 'image/svg+xml'],
      ['/assets/data.bin', 'application/octet-stream'],
    ]);
    for (const [path, contentType] of expectedTypes) {
      const response = await fetch(`${origin}${path}`);
      expect(response.status).toBe(200);
      expect(response.headers.get('content-type')).toBe(contentType);
      expect(response.headers.get('x-content-type-options')).toBe('nosniff');
    }
    for (const path of [
      '/assets/missing.css',
      '/assets/link.css',
      '/assets/%2e%2e%2foutside.css',
      '/assets/%',
      '/console/missing.js',
      '/missing',
    ]) {
      expect((await fetch(`${origin}${path}`)).status).toBe(404);
    }

    const authority = new LaunchTokenAuthority(SECRET_BYTES);
    const token = authority.issue(NAMESPACE);
    const worldResponse = await fetch(`${origin}/v1/worlds`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${token}`,
        'content-type': 'application/json',
        'x-tenkacloud-simulator-protocol': SIMULATOR_PROTOCOL_VERSION,
      },
      body: JSON.stringify(NAMESPACE),
    });
    expect(worldResponse.status).toBe(201);
    const world: unknown = await worldResponse.json();
    assertSimulatorWorldResponse(world);
    expect(world.consoleUrl).toBe(
      `http://127.0.0.1:7777/console/${world.worldId}`
    );
  });

  it('認証済み raw data-plane が AWS listener rule の QUERY 405→ModifyRule→200 を実 HTTP で再現する', async () => {
    const reservation = Bun.serve({
      hostname: '127.0.0.1',
      port: 0,
      fetch: () => new Response('reserved'),
    });
    const port = reservation.port;
    await reservation.stop(true);
    const runtime = await openRuntime({
      TENKACLOUD_SIMULATOR_PORT: String(port),
    });
    const server = serveEntrypoint(runtime);
    const origin = server.url.origin;
    const authority = new LaunchTokenAuthority(SECRET_BYTES);
    const token = authority.issue(NAMESPACE);

    const worldResponse = await fetch(`${origin}/v1/worlds`, {
      method: 'POST',
      headers: authenticatedHeaders(token),
      body: JSON.stringify(NAMESPACE),
    });
    expect(worldResponse.status).toBe(201);
    const world: unknown = await worldResponse.json();
    assertSimulatorWorldResponse(world);

    const deploymentResponse = await fetch(
      `${origin}/v1/worlds/${world.worldId}/deployments`,
      {
        method: 'POST',
        headers: authenticatedHeaders(token),
        body: JSON.stringify({
          problemId: 'runtime-data-plane',
          runtime: {
            provider: 'aws',
            engine: 'cloudformation',
            entry: 'template.json',
          },
          templateBody: DATA_PLANE_TEMPLATE,
        }),
      }
    );
    expect(deploymentResponse.status).toBe(201);
    const deployment: unknown = await deploymentResponse.json();
    assertSimulatorDeploymentResponse(deployment);
    const { RuleArn: ruleArn } = deployment.outputs;
    if (!ruleArn) throw new Error('listener rule output がありません');

    const dataPlaneUrl = `${origin}/v1/worlds/${world.worldId}/data-plane/aws/default/search?source=runtime`;
    const queryBody = JSON.stringify({ query: { match: 'tenka' } });
    const query = () =>
      fetch(dataPlaneUrl, {
        method: 'QUERY',
        headers: {
          authorization: `Bearer ${token}`,
          'content-type': 'application/json',
        },
        body: queryBody,
      });

    const before = await query();
    expect(before.status).toBe(405);
    expect(before.headers.get('content-type')).toBe('text/plain');
    expect(await before.text()).toBe('QUERY is blocked at the edge');

    const modified = await fetch(
      awsModifyRuleRequest(origin, world.worldId, ruleArn)
    );
    expect(modified.status).toBe(200);
    expect(modified.headers.get('content-type')).toContain('xml');
    expect(await modified.text()).toContain('<member>QUERY</member>');

    const after = await query();
    expect(after.status).toBe(200);
    expect(await after.text()).toContain('TC{query-app-server}');

    const otherToken = authority.issue({ ...NAMESPACE, teamId: 'other-team' });
    expect(
      (
        await fetch(dataPlaneUrl, {
          headers: { authorization: `Bearer ${otherToken}` },
        })
      ).status
    ).toBe(404);
    expect((await fetch(dataPlaneUrl)).status).toBe(401);
    expect(
      (
        await fetch(dataPlaneUrl, {
          headers: {
            authorization: `Bearer ${token}`,
            te: 'trailers',
          },
        })
      ).status
    ).toBe(400);
  });

  it('process、IPv6、container の bind と public origin を明示的に分離する', async () => {
    const defaults = await openRuntime();
    expect({ host: defaults.host, port: defaults.port }).toEqual({
      host: '127.0.0.1',
      port: 7777,
    });

    const ipv6 = await openRuntime({
      TENKACLOUD_SIMULATOR_HOST: '::1',
      TENKACLOUD_SIMULATOR_PORT: '9444',
    });
    expect({ host: ipv6.host, port: ipv6.port }).toEqual({
      host: '::1',
      port: 9444,
    });

    const container = await openRuntime({
      TENKACLOUD_SIMULATOR_CONTAINER_MODE: '1',
      TENKACLOUD_SIMULATOR_HOST: '0.0.0.0',
      TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'https://codespace.example.test/',
    });
    expect(container.host).toBe('0.0.0.0');
    const server = serve(container);
    const token = new LaunchTokenAuthority(SECRET_BYTES).issue(NAMESPACE);
    const response = await fetch(`${server.url.origin}/v1/worlds`, {
      method: 'POST',
      headers: {
        authorization: `Bearer ${token}`,
        'content-type': 'application/json',
        'x-tenkacloud-simulator-protocol': SIMULATOR_PROTOCOL_VERSION,
      },
      body: JSON.stringify(NAMESPACE),
    });
    const world: unknown = await response.json();
    assertSimulatorWorldResponse(world);
    expect(world.consoleUrl).toStartWith(
      'https://codespace.example.test/console/'
    );
  });

  it('host、container mode、port、public origin の不正値を起動前に拒否する', async () => {
    const invalidOverrides: readonly Readonly<
      Partial<SimulatorRuntimeEnvironment>
    >[] = [
      { TENKACLOUD_SIMULATOR_HOST: '0.0.0.0' },
      {
        TENKACLOUD_SIMULATOR_CONTAINER_MODE: '0',
        TENKACLOUD_SIMULATOR_HOST: '127.0.0.1',
      },
      {
        TENKACLOUD_SIMULATOR_CONTAINER_MODE: '1',
        TENKACLOUD_SIMULATOR_HOST: '0.0.0.0',
      },
      { TENKACLOUD_SIMULATOR_HOST: '192.0.2.1' },
      { TENKACLOUD_SIMULATOR_PORT: '0' },
      { TENKACLOUD_SIMULATOR_PORT: '65536' },
      { TENKACLOUD_SIMULATOR_PORT: '1.5' },
      { TENKACLOUD_SIMULATOR_PORT: 'not-a-port' },
      { TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'relative' },
      { TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'ftp://example.test' },
      { TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'http://example.test' },
      { TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'https://user:pass@example.test' },
      { TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'https://example.test/path' },
      { TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'https://example.test?query=1' },
      { TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN: 'https://example.test/#fragment' },
    ];
    for (const overrides of invalidOverrides) {
      await expect(
        createSimulatorRuntime(environment(overrides))
      ).rejects.toThrow();
    }
  });

  it('secret、state directory、Console build の security boundary を検証する', async () => {
    const {
      TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID: _awsCredential,
      ...withoutNativeCredential
    } = environment();
    await expect(
      createSimulatorRuntime(withoutNativeCredential)
    ).rejects.toThrow('native gateway credentials');

    const complete = environment();
    const { TENKACLOUD_SIMULATOR_LAUNCH_SECRET: _secret, ...withoutSecret } =
      complete;
    await expect(createSimulatorRuntime(withoutSecret)).rejects.toThrow(
      'required'
    );

    const { TENKACLOUD_SIMULATOR_STATE_DIR: _state, ...withoutState } =
      environment();
    await expect(createSimulatorRuntime(withoutState)).rejects.toThrow(
      'required'
    );
    await expect(
      createSimulatorRuntime(
        environment({ TENKACLOUD_SIMULATOR_LAUNCH_SECRET: 'not%base64url' })
      )
    ).rejects.toThrow('base64url');
    await expect(
      createSimulatorRuntime(
        environment({
          TENKACLOUD_SIMULATOR_LAUNCH_SECRET:
            Buffer.alloc(31).toString('base64url'),
        })
      )
    ).rejects.toThrow('at least 32 bytes');

    const missingConsole = join(directory, 'missing-console');
    await expect(
      createSimulatorRuntime(
        environment({ TENKACLOUD_SIMULATOR_CONSOLE_DIR: missingConsole })
      )
    ).rejects.toThrow('does not exist');
    const consoleFile = join(directory, 'console-file');
    await writeFile(consoleFile, 'not a directory');
    await expect(
      createSimulatorRuntime(
        environment({ TENKACLOUD_SIMULATOR_CONSOLE_DIR: consoleFile })
      )
    ).rejects.toThrow('does not exist');

    const realState = join(directory, 'real-state');
    await mkdir(realState);
    const linkedState = join(directory, 'linked-state');
    await symlink(realState, linkedState, 'dir');
    await expect(
      createSimulatorRuntime(
        environment({ TENKACLOUD_SIMULATOR_STATE_DIR: linkedState })
      )
    ).rejects.toThrow('directory');

    const realParent = join(directory, 'real-parent');
    await mkdir(realParent);
    const linkedParent = join(directory, 'linked-parent');
    await symlink(realParent, linkedParent, 'dir');
    await expect(
      createSimulatorRuntime(
        environment({
          TENKACLOUD_SIMULATOR_STATE_DIR: join(linkedParent, 'nested-state'),
        })
      )
    ).rejects.toThrow('symbolic link');

    const permissionState = join(directory, 'permission-state');
    await mkdir(permissionState);
    await chmod(permissionState, 0o755);
    const runtime = await createSimulatorRuntime(
      environment({ TENKACLOUD_SIMULATOR_STATE_DIR: permissionState })
    );
    runtimes.push(runtime);
    expect((await lstat(permissionState)).mode & 0o777).toBe(0o700);
  });

  it('should reconcile a persisted delete intent before returning a runtime', async () => {
    const configured = environment();
    const stateDirectory = configured.TENKACLOUD_SIMULATOR_STATE_DIR ?? '';
    await mkdir(stateDirectory, { recursive: true });
    const seededStore = new SimulationStore(
      join(stateDirectory, 'simulator.sqlite')
    );
    const seededCore = new SimulationCore(seededStore, new ProviderRegistry());
    const world = seededCore.createWorld(
      {
        tenantId: 'startup-reconcile-tenant',
        eventId: 'startup-reconcile-event',
        teamId: 'startup-reconcile-team',
        deploymentId: 'startup-reconcile-deployment',
      },
      'startup-reconcile-world'
    );
    seededStore.reserveEvents(
      world.worldId,
      deterministicId('command', {
        worldId: world.worldId,
        operation: 'delete',
      }),
      1,
      'deletion'
    );
    await expect(createSimulatorRuntime(configured)).rejects.toMatchObject({
      code: 'Conflict',
    });
    expect(seededStore.pendingDeletionWorldIds()).toEqual([world.worldId]);
    seededStore.close();

    const runtime = await createSimulatorRuntime(configured);
    runtimes.push(runtime);
    expect(runtime.store.world(world.worldId)?.status).toBe('deleted');
    expect(runtime.store.pendingDeletionWorldIds()).toEqual([]);
  });

  it('workload policy は全項目の厳密設定時だけ runner capability を広告する', async () => {
    expect(workloadPolicy(environment())).toBeUndefined();
    const policyEnvironment = environment(workloadEnvironment());
    expect(workloadPolicy(policyEnvironment)).toEqual({
      allowedImages: new Set([WORKLOAD_IMAGE]),
      proxyImage: WORKLOAD_IMAGE,
      maxMemoryBytes: 134_217_728,
      maxMilliCpu: 500,
      maxPids: 64,
    });
    expect(
      workloadPolicy(
        environment({
          ...workloadEnvironment(),
          TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER:
            'tenkacloud-simulator-control',
        })
      )
    ).toEqual({
      allowedImages: new Set([WORKLOAD_IMAGE]),
      proxyImage: WORKLOAD_IMAGE,
      maxMemoryBytes: 134_217_728,
      maxMilliCpu: 500,
      maxPids: 64,
      controlContainer: 'tenkacloud-simulator-control',
    });
    const runtime = await createSimulatorRuntime(policyEnvironment);
    runtimes.push(runtime);
    const server = serve(runtime);
    const response = await fetch(`${server.url.origin}/v1/capabilities`);
    const capabilities: unknown = await response.json();
    assertSimulatorCapabilities(capabilities);
    expect(
      capabilities.capabilities?.some(
        (capability) =>
          capability.resourceType === 'Runtime::Workload' &&
          capability.operation === 'Materialize'
      )
    ).toBe(true);
  });

  it('workload allowlist の不完全値、tag、重複、policy 上限超過を起動前に拒否する', () => {
    const valid = workloadEnvironment();
    const tooManyImages = Array.from(
      { length: 65 },
      (_, index) => `image-${index}@sha256:${'a'.repeat(64)}`
    );
    const invalid: readonly Readonly<Partial<SimulatorRuntimeEnvironment>>[] = [
      { TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE: WORKLOAD_IMAGE },
      {
        TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER:
          'tenkacloud-simulator-control',
      },
      { ...valid, TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES: '[' },
      { ...valid, TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES: '{}' },
      { ...valid, TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES: '[]' },
      {
        ...valid,
        TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES:
          JSON.stringify(tooManyImages),
      },
      {
        ...valid,
        TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES: JSON.stringify([
          'busybox:latest',
        ]),
        TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE: 'busybox:latest',
      },
      {
        ...valid,
        TENKACLOUD_SIMULATOR_WORKLOAD_ALLOWED_IMAGES: JSON.stringify([
          WORKLOAD_IMAGE,
          WORKLOAD_IMAGE,
        ]),
      },
      {
        ...valid,
        TENKACLOUD_SIMULATOR_WORKLOAD_PROXY_IMAGE: `proxy@sha256:${'b'.repeat(64)}`,
      },
      {
        ...valid,
        TENKACLOUD_SIMULATOR_WORKLOAD_CONTROL_CONTAINER: 'invalid/name',
      },
      { ...valid, TENKACLOUD_SIMULATOR_CONTAINER_MODE: '1' },
      { ...valid, TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES: '0' },
      {
        ...valid,
        TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MEMORY_BYTES: '8589934593',
      },
      { ...valid, TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU: '1.5' },
      { ...valid, TENKACLOUD_SIMULATOR_WORKLOAD_MAX_MILLI_CPU: '8001' },
      { ...valid, TENKACLOUD_SIMULATOR_WORKLOAD_MAX_PIDS: '4097' },
    ];
    for (const overrides of invalid) {
      expect(() => workloadPolicy(environment(overrides))).toThrow();
    }
  });
});
