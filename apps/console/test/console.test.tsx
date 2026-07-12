import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { PROTOCOL_HEADER } from '@tenkacloud/simulator-api';
import {
  assertSimulatorDeploymentResponse,
  assertSimulatorWorldResponse,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorDeploymentResponse,
  type SimulatorResourceProjection,
  type SimulatorWorldResponse,
} from '@tenkacloud/simulator-contracts';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { GcpProvider } from '@tenkacloud/simulator-provider-gcp';
import { SakuraProvider } from '@tenkacloud/simulator-provider-sakura';
import {
  createAuthenticatedSimulatorApp,
  LaunchTokenAuthority,
} from '@tenkacloud/simulator-server';
import { renderToStaticMarkup } from 'react-dom/server';
import {
  ConsoleClientError,
  parseEventStream,
  SimulatorConsoleClient,
} from '../src/client';
import {
  ConsoleLaunchTokenError,
  consumeLaunchToken,
  simulatorLaunchToken,
} from '../src/launch-token';
import { loadConsoleData, parseConsoleRoute } from '../src/loader';
import {
  diagnostics,
  displayValue,
  groupResources,
  mergeEvents,
  propertyCategories,
} from '../src/model';
import { WorldConsoleView } from '../src/view';

const GCP_TEMPLATE = readFileSync(
  new URL(
    '../../../providers/gcp/test/fixtures/hello-multicloud/main.tf',
    import.meta.url
  ),
  'utf8'
);
const SAKURA_TEMPLATE = readFileSync(
  new URL('./fixtures/sakura-app.json', import.meta.url),
  'utf8'
);
const TOKEN_SECRET = 'console-test-secret-0123456789abcdef';

interface TestRuntime {
  readonly authority: LaunchTokenAuthority;
  readonly baseUrl: string;
  readonly directory: string;
  readonly server: Bun.Server<undefined>;
  readonly store: SimulationStore;
}

let runtime: TestRuntime;

interface LaunchedWorld {
  readonly client: SimulatorConsoleClient;
  readonly deploymentId: string;
  readonly token: string;
  readonly world: SimulatorWorldResponse;
}

function namespace(deploymentId: string) {
  return {
    tenantId: 'console-tenant',
    eventId: 'console-event',
    teamId: `team-${deploymentId}`,
    deploymentId,
  };
}

function protocolHeaders(token: string): HeadersInit {
  return {
    authorization: `Bearer ${token}`,
    'content-type': 'application/json',
    [PROTOCOL_HEADER]: SIMULATOR_PROTOCOL_VERSION,
  };
}

async function createWorld(deploymentId: string): Promise<LaunchedWorld> {
  const token = runtime.authority.issue(namespace(deploymentId));
  const client = new SimulatorConsoleClient(runtime.baseUrl, token);
  const response = await fetch(`${runtime.baseUrl}/v1/worlds`, {
    method: 'POST',
    headers: protocolHeaders(token),
    body: JSON.stringify({
      ...namespace(deploymentId),
      seed: `seed-${deploymentId}`,
      virtualClock: '2026-07-12T00:00:00.000Z',
    }),
  });
  expect(response.status).toBe(201);
  const body: unknown = await response.json();
  assertSimulatorWorldResponse(body);
  return { client, deploymentId, token, world: body };
}

async function deploy(
  worldId: string,
  token: string,
  provider: string,
  engine: string,
  entry: string,
  templateBody: string
): Promise<Response> {
  return fetch(`${runtime.baseUrl}/v1/worlds/${worldId}/deployments`, {
    method: 'POST',
    headers: protocolHeaders(token),
    body: JSON.stringify({
      problemId: `console-${provider}`,
      runtime: { provider, engine, entry },
      templateBody,
    }),
  });
}

async function readyWorld(): Promise<{
  readonly client: SimulatorConsoleClient;
  readonly world: SimulatorWorldResponse;
  readonly deployment: SimulatorDeploymentResponse;
  readonly token: string;
}> {
  const deploymentId = 'deployment-gcp';
  const launch = await createWorld(deploymentId);
  const response = await deploy(
    launch.world.worldId,
    launch.token,
    'gcp',
    'infra-manager',
    'main.tf',
    GCP_TEMPLATE
  );
  expect(response.status).toBe(201);
  const deployment: unknown = await response.json();
  assertSimulatorDeploymentResponse(deployment);
  return {
    client: launch.client,
    world: launch.world,
    deployment,
    token: launch.token,
  };
}

async function sakuraResources(): Promise<SimulatorResourceProjection> {
  const deploymentId = 'deployment-sakura-console';
  const launch = await createWorld(deploymentId);
  const response = await deploy(
    launch.world.worldId,
    launch.token,
    'sakura',
    'apprun',
    'application.json',
    SAKURA_TEMPLATE
  );
  expect(response.status).toBe(201);
  return launch.client.resources(launch.world.worldId);
}

beforeEach(() => {
  const directory = mkdtempSync(path.join(tmpdir(), 'simulator-console-'));
  const store = new SimulationStore(path.join(directory, 'simulation.sqlite'));
  const registry = new ProviderRegistry([
    new GcpProvider(),
    new SakuraProvider(),
  ]);
  const core = new SimulationCore(store, registry);
  const authority = new LaunchTokenAuthority(TOKEN_SECRET);
  const app = createAuthenticatedSimulatorApp({
    core,
    registry,
    consoleBaseUrl: 'http://127.0.0.1:4173/console',
    launchTokens: authority,
  });
  const server = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch: app.fetch,
  });
  runtime = {
    authority,
    baseUrl: server.url.origin,
    directory,
    server,
    store,
  };
});

afterEach(async () => {
  await runtime.server.stop(true);
  runtime.store.close();
  rmSync(runtime.directory, { recursive: true, force: true });
});

describe('Console の API client と route', () => {
  it('fragment token を URL から即時除去し、memory 内の simulator token だけを返す', async () => {
    const launch = await createWorld('deployment-fragment');
    const url = new URL(
      `http://console.local/console/${launch.world.worldId}?deploymentId=${launch.deploymentId}#token=${encodeURIComponent(launch.token)}`
    );
    let replacedPath = '';
    const consumed = consumeLaunchToken(url, (cleanPath) => {
      replacedPath = cleanPath;
    });

    expect(consumed.token).toBe(launch.token);
    expect(consumed.cleanPath).toBe(
      `/console/${launch.world.worldId}?deploymentId=${launch.deploymentId}`
    );
    expect(replacedPath).toBe(consumed.cleanPath);
    expect(url.hash).toBe('');
    expect(replacedPath).not.toContain(launch.token);
    expect(simulatorLaunchToken(launch.token)).toBe(launch.token);
  });

  it('token がない link、重複 token、非 simulator token を accessible error にする', () => {
    for (const value of [null, '', 'eyJhbGciOiJSUzI1NiJ9.real.jwt']) {
      expect(() => simulatorLaunchToken(value)).toThrow(
        ConsoleLaunchTokenError
      );
    }
    expect(() =>
      simulatorLaunchToken(`tc_sim_v1.${'a'.repeat(4096)}.signature`)
    ).toThrow('valid simulator token');

    const duplicate = new URL(
      'http://console.local/console/world-a#token=tc_sim_v1.a.b&token=tc_sim_v1.c.d'
    );
    let cleaned = '';
    let tokenError: unknown;
    try {
      consumeLaunchToken(duplicate, (cleanPath) => {
        cleaned = cleanPath;
      });
    } catch (error) {
      tokenError = error;
    }
    expect(cleaned).toBe('/console/world-a');
    expect(duplicate.hash).toBe('');
    expect(tokenError).toBeInstanceOf(ConsoleLaunchTokenError);
    const message =
      tokenError instanceof Error ? tokenError.message : 'Launch denied';
    const errorView = renderToStaticMarkup(
      <WorldConsoleView
        state={{ kind: 'error', worldId: 'world-a', message }}
        onRefresh={() => undefined}
      />
    );
    expect(errorView).toContain('role="alert"');
    expect(errorView).toContain('simulator launch token');
    expect(errorView).not.toContain('tc_sim_v1');
  });

  it('実 HTTP と SQLite の world を route から読み、SSE cursor で replay する', async () => {
    const { client, world, deployment } = await readyWorld();
    const route = parseConsoleRoute(
      new URL(
        `http://console.local/console/${encodeURIComponent(world.worldId)}?deploymentId=${deployment.deploymentId}`
      )
    );
    const data = await loadConsoleData(client, route);

    expect(data.worldId).toBe(world.worldId);
    expect(data.deployment?.status).toBe('running');
    expect(data.resources.resources).toHaveLength(2);
    expect(data.events.length).toBeGreaterThan(3);
    expect(data.cursor).toBe(data.events.at(-1)?.sequence);

    const replay = await client.events(world.worldId, 1);
    expect(replay.events.every((event) => event.sequence > 1)).toBe(true);
    const stream = await client.stream(world.worldId, 0);
    expect(stream.events).toEqual(data.events);
    expect(stream.nextCursor).toBe(data.cursor);
    const caughtUp = await client.stream(world.worldId, data.cursor);
    expect(caughtUp).toEqual({ events: [], nextCursor: data.cursor });

    const resources = await client.resources(world.worldId);
    const fetchedDeployment = await client.deployment(
      world.worldId,
      deployment.deploymentId
    );
    expect(resources).toEqual(data.resources);
    expect(fetchedDeployment).toEqual(deployment);
  });

  it('deployment 指定なし route、誤った route、未存在 world を明示的に扱う', async () => {
    const launch = await createWorld('deployment-route-only');
    const route = parseConsoleRoute(
      new URL(
        `http://console.local/console/${encodeURIComponent(launch.world.worldId)}/`
      )
    );
    const data = await loadConsoleData(launch.client, route);
    expect(data.deployment).toBeUndefined();
    expect(() =>
      parseConsoleRoute(new URL('http://console.local/world/nope'))
    ).toThrow('Expected /console/:worldId');
    expect(() =>
      parseConsoleRoute(new URL('http://console.local/console/%'))
    ).toThrow();
    expect(
      () => new SimulatorConsoleClient('file:///tmp/simulator', launch.token)
    ).toThrow('must use HTTP or HTTPS');

    try {
      await launch.client.resources('world-does-not-exist');
      throw new Error('ConsoleClientError が発生しませんでした');
    } catch (error) {
      expect(error).toBeInstanceOf(ConsoleClientError);
      if (!(error instanceof ConsoleClientError)) throw error;
      expect(error.status).toBe(404);
      expect(error.envelope?.error.code).toBe('NotFound');
    }

    await expect(
      launch.client.stream('world-does-not-exist', 0)
    ).rejects.toBeInstanceOf(ConsoleClientError);

    const untrusted = new SimulatorConsoleClient(
      runtime.baseUrl,
      'tc_sim_v1.payload.signature'
    );
    await expect(
      untrusted.resources(launch.world.worldId)
    ).rejects.toMatchObject({ status: 401 });
  });

  it('SSE comment と空 block を無視し、壊れた event JSON を拒否する', () => {
    expect(parseEventStream(': keepalive\n\n\n')).toEqual([]);
    expect(() =>
      parseEventStream('event: broken\ndata: {not-json}\n\n')
    ).toThrow('Event stream contains invalid JSON');
  });
});

describe('Console の provider-neutral projection model', () => {
  it('実 provider の resource を provider と native type の順に整理する', async () => {
    const { client, world } = await readyWorld();
    const gcp = await client.resources(world.worldId);
    const sakura = await sakuraResources();
    const groups = groupResources({
      resources: [...sakura.resources, ...gcp.resources],
    });
    expect(groups.map((group) => group.provider)).toEqual(['gcp', 'sakura']);
    expect(
      groups[0]?.resources.map((resource) => resource.resourceType)
    ).toEqual([
      'google_cloud_run_v2_service',
      'google_cloud_run_v2_service_iam_member',
    ]);

    const categories = gcp.resources.flatMap(propertyCategories);
    expect(categories.map((category) => category.label)).toContain('Policy');
    expect(categories.map((category) => category.label)).toContain(
      'Reachability'
    );
    expect(categories.map((category) => category.label)).toContain(
      'Properties'
    );
    expect(displayValue('ready')).toBe('ready');
    expect(displayValue({ nested: true })).toBe('{\n  "nested": true\n}');
  });

  it('event replay は sequence で重複排除し、deployment 診断を集約する', async () => {
    const { client, world } = await readyWorld();
    const page = await client.events(world.worldId);
    expect(mergeEvents(page.events, page.events)).toEqual(page.events);
    expect(diagnostics(undefined)).toEqual([]);

    const rejectedId = 'deployment-rejected';
    const rejectedLaunch = await createWorld(rejectedId);
    const response = await deploy(
      rejectedLaunch.world.worldId,
      rejectedLaunch.token,
      'unavailable-provider',
      'declarative',
      'unavailable.json',
      '{}'
    );
    expect(response.status).toBe(422);
    const rejected = await rejectedLaunch.client.deployment(
      rejectedLaunch.world.worldId,
      rejectedId
    );
    expect(rejected.status).toBe('failed');
    const rejectedDiagnostics = diagnostics(rejected);
    expect(rejectedDiagnostics).toHaveLength(1);
    const diagnostic = rejectedDiagnostics[0];
    if (!diagnostic) throw new Error('deployment diagnostic がありません');
    expect(
      diagnostics({
        ...rejected,
        diagnostics: [],
        targets: [
          {
            id: 'unavailable-target',
            provider: 'unavailable-provider',
            engine: 'declarative',
            status: 'failed',
            outputs: {},
            diagnostics: [diagnostic],
          },
        ],
      })
    ).toEqual([diagnostic]);
    const rejectedEvents = await rejectedLaunch.client.events(
      rejectedLaunch.world.worldId
    );
    const rendered = renderToStaticMarkup(
      <WorldConsoleView
        state={{
          kind: 'ready',
          data: {
            worldId: rejectedLaunch.world.worldId,
            deployment: rejected,
            resources: await rejectedLaunch.client.resources(
              rejectedLaunch.world.worldId
            ),
            events: rejectedEvents.events,
            cursor: rejectedEvents.nextCursor,
          },
        }}
        onRefresh={() => undefined}
      />
    );
    expect(rendered).toContain('MissingProvider');
    expect(rendered).toContain('unavailable.json');
  });
});

describe('Console view の状態表示', () => {
  it('実 world の resource、policy、reachability、output、event を表示する', async () => {
    const { client, world, deployment } = await readyWorld();
    const data = await loadConsoleData(client, {
      worldId: world.worldId,
      deploymentId: deployment.deploymentId,
    });
    const html = renderToStaticMarkup(
      <WorldConsoleView
        state={{ kind: 'ready', data }}
        onRefresh={() => undefined}
      />
    );
    expect(html).toContain('Provider projections');
    expect(html).toContain('google_cloud_run_v2_service');
    expect(html).toContain('Policy');
    expect(html).toContain('Reachability');
    expect(html).toContain('GcpHelloUrl');
    expect(html).toContain('DeploymentReady');
    expect(html).toContain('SSE replay');
  });

  it('loading、error、空 projection の状態を accessible text で表示する', async () => {
    const loading = renderToStaticMarkup(
      <WorldConsoleView
        state={{ kind: 'loading', worldId: 'world-loading' }}
        onRefresh={() => undefined}
      />
    );
    const error = renderToStaticMarkup(
      <WorldConsoleView
        state={{
          kind: 'error',
          worldId: 'world-error',
          message: 'World was deleted',
        }}
        onRefresh={() => undefined}
      />
    );
    const launch = await createWorld('deployment-empty');
    const realData = await loadConsoleData(launch.client, {
      worldId: launch.world.worldId,
    });
    const empty = renderToStaticMarkup(
      <WorldConsoleView
        state={{
          kind: 'ready',
          data: { ...realData, events: [], cursor: 0 },
        }}
        onRefresh={() => undefined}
      />
    );
    expect(loading).toContain('Reading the event-sourced world');
    expect(loading).toContain('aria-busy="true"');
    expect(error).toContain('role="alert"');
    expect(error).toContain('World was deleted');
    expect(empty).toContain('No resources have been projected yet.');
    expect(empty).toContain('No events exist after this cursor.');
    expect(empty).toContain('No deployment outputs.');
    expect(empty).toContain('No deployment diagnostics.');
  });
});
