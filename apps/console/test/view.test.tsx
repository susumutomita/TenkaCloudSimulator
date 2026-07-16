import './dom-setup';
import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import Alert from '@cloudscape-design/components/alert';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { PROTOCOL_HEADER } from '@tenkacloud/simulator-api';
import {
  assertSimulatorDeploymentResponse,
  assertSimulatorWorldResponse,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorDeploymentResponse,
  type SimulatorWorldResponse,
} from '@tenkacloud/simulator-contracts';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  CLOUD_RUN_SERVICE,
  GcpProvider,
} from '@tenkacloud/simulator-provider-gcp';
import {
  createAuthenticatedSimulatorApp,
  LaunchTokenAuthority,
} from '@tenkacloud/simulator-server';
import { cleanup, fireEvent, render } from '@testing-library/react';
import { SimulatorConsoleClient } from '../src/client';
import {
  ConsoleLaunchTokenError,
  consumeLaunchToken,
} from '../src/launch-token';
import { createConsoleOperationAction, loadConsoleData } from '../src/loader';
import type { ConsoleWorldData } from '../src/model';
import {
  ConsoleOperationResult,
  statusIndicatorType,
  WorldConsoleView,
} from '../src/view';

const GCP_TEMPLATE = readFileSync(
  new URL(
    '../../../providers/gcp/test/fixtures/hello-multicloud/main.tf',
    import.meta.url
  ),
  'utf8'
);
const TOKEN_SECRET = 'console-view-secret-0123456789abcdef';

interface TestRuntime {
  readonly authority: LaunchTokenAuthority;
  readonly baseUrl: string;
  readonly directory: string;
  readonly server: Bun.Server<undefined>;
  readonly store: SimulationStore;
}

let runtime: TestRuntime;

function namespace(deploymentId: string) {
  return {
    tenantId: 'console-view-tenant',
    eventId: 'console-view-event',
    teamId: `team-${deploymentId}`,
    deploymentId,
  };
}

async function createWorld(deploymentId: string): Promise<{
  readonly client: SimulatorConsoleClient;
  readonly token: string;
  readonly world: SimulatorWorldResponse;
}> {
  const token = runtime.authority.issue(namespace(deploymentId));
  const client = new SimulatorConsoleClient(runtime.baseUrl, token);
  const response = await fetch(`${runtime.baseUrl}/v1/worlds`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${token}`,
      'content-type': 'application/json',
      [PROTOCOL_HEADER]: SIMULATOR_PROTOCOL_VERSION,
    },
    body: JSON.stringify({
      ...namespace(deploymentId),
      seed: `seed-${deploymentId}`,
      virtualClock: '2026-07-16T00:00:00.000Z',
    }),
  });
  expect(response.status).toBe(201);
  const body: unknown = await response.json();
  assertSimulatorWorldResponse(body);
  return { client, token, world: body };
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
    headers: {
      authorization: `Bearer ${token}`,
      'content-type': 'application/json',
      [PROTOCOL_HEADER]: SIMULATOR_PROTOCOL_VERSION,
    },
    body: JSON.stringify({
      problemId: `console-view-${provider}`,
      runtime: { provider, engine, entry },
      templateBody,
    }),
  });
}

async function readyWorldData(): Promise<{
  readonly client: SimulatorConsoleClient;
  readonly data: ConsoleWorldData;
  readonly deployment: SimulatorDeploymentResponse;
}> {
  const deploymentId = 'deployment-view-gcp';
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
  const data = await loadConsoleData(launch.client, {
    worldId: launch.world.worldId,
    deploymentId,
  });
  return { client: launch.client, data, deployment };
}

const noRefresh: () => void = () => undefined;

const noOperation: () => Promise<void> = () => Promise.resolve(undefined);

beforeEach(() => {
  const directory = mkdtempSync(path.join(tmpdir(), 'simulator-console-view-'));
  const store = new SimulationStore(path.join(directory, 'simulation.sqlite'));
  const registry = new ProviderRegistry([new GcpProvider()]);
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
  cleanup();
  await runtime.server.stop(true);
  runtime.store.close();
  rmSync(runtime.directory, { recursive: true, force: true });
});

describe('Cloudscape client rendering spike', () => {
  it('代表 component の Alert と StatusIndicator と Container を DOM 環境で描画する', () => {
    const view = render(
      <Container header={<Header variant="h2">spike container</Header>}>
        <Alert type="error">spike alert body</Alert>
        <StatusIndicator type="success">spike status</StatusIndicator>
      </Container>
    );
    expect(view.getByText('spike container')).toBeTruthy();
    expect(view.getByText('spike alert body')).toBeTruthy();
    expect(view.getByText('spike status')).toBeTruthy();
  });
});

describe('Console shell の状態表示', () => {
  it('loading 状態を aria-busy な領域と world ID の accessible text で表示する', () => {
    const view = render(
      <WorldConsoleView
        state={{ kind: 'loading', worldId: 'world-loading' }}
        onRefresh={noRefresh}
        onOperation={noOperation}
      />
    );
    expect(view.container.querySelector('[aria-busy="true"]')).toBeTruthy();
    expect(view.getByText('Reading the event-sourced world')).toBeTruthy();
    expect(view.getByText('World world-loading')).toBeTruthy();
    expect(view.getAllByText('TenkaCloud Simulator').length).toBeGreaterThan(0);
  });

  it('error 状態を role=alert と再試行 Button で表示する', () => {
    let refreshes = 0;
    const view = render(
      <WorldConsoleView
        state={{
          kind: 'error',
          worldId: 'world-error',
          message: 'World was deleted',
        }}
        onRefresh={() => {
          refreshes += 1;
        }}
        onOperation={noOperation}
      />
    );
    const alert = view.getByRole('alert');
    expect(alert.textContent).toContain('World unavailable');
    expect(alert.textContent).toContain('World was deleted');
    expect(alert.textContent).toContain('world-error');
    fireEvent.click(view.getByText('Try again'));
    expect(refreshes).toBe(1);
  });

  it('launch token エラーを表示しても token 値を DOM に露出しない', () => {
    const duplicate = new URL(
      'http://console.local/console/world-a#token=tc_sim_v1.a.b&token=tc_sim_v1.c.d'
    );
    let tokenError: unknown;
    try {
      consumeLaunchToken(duplicate, () => undefined);
    } catch (error) {
      tokenError = error;
    }
    if (!(tokenError instanceof ConsoleLaunchTokenError)) {
      throw new Error('ConsoleLaunchTokenError が発生しませんでした');
    }
    const view = render(
      <WorldConsoleView
        state={{
          kind: 'error',
          worldId: 'world-a',
          message: tokenError.message,
        }}
        onRefresh={noRefresh}
        onOperation={noOperation}
      />
    );
    expect(view.getByRole('alert').textContent).toContain(
      'simulator launch token'
    );
    expect(view.baseElement.innerHTML).not.toContain('tc_sim_v1');
  });
});

describe('Console ready 表示', () => {
  it('実 world の provider projection と output と event を表示する', async () => {
    const { data } = await readyWorldData();
    let refreshes = 0;
    const view = render(
      <WorldConsoleView
        state={{ kind: 'ready', data }}
        onRefresh={() => {
          refreshes += 1;
        }}
        onOperation={noOperation}
      />
    );
    expect(view.getByText('Provider projections')).toBeTruthy();
    expect(view.getAllByText('gcp').length).toBeGreaterThan(0);
    expect(view.getAllByText(CLOUD_RUN_SERVICE).length).toBeGreaterThan(0);
    expect(view.getAllByText('Target').length).toBeGreaterThan(0);
    expect(view.getAllByText('default').length).toBeGreaterThan(0);
    expect(view.getAllByText('Policy').length).toBeGreaterThan(0);
    expect(view.getAllByText('Reachability').length).toBeGreaterThan(0);
    expect(view.getAllByText('Properties').length).toBeGreaterThan(0);
    expect(view.getByText('GcpHelloUrl')).toBeTruthy();
    expect(view.getByText('DeploymentReady')).toBeTruthy();
    expect(view.getByText('SSE replay')).toBeTruthy();
    expect(view.getByText(`cursor ${data.cursor}`)).toBeTruthy();
    expect(view.getByText('Provider operation')).toBeTruthy();
    expect(view.getByText('Execute command')).toBeTruthy();
    expect(view.getAllByText('running').length).toBeGreaterThan(0);
    expect(view.getAllByText('ready').length).toBeGreaterThan(0);
    const idempotency = view.getByLabelText('Idempotency key');
    if (!(idempotency instanceof HTMLInputElement)) {
      throw new Error('Idempotency key input がありません');
    }
    expect(idempotency.value).toMatch(/^console-[a-f0-9-]{36}$/);
    fireEvent.click(view.getByText('Refresh projection'));
    expect(refreshes).toBe(1);
  });

  it('空 projection と deployment 未選択の状態を empty text で表示する', async () => {
    const launch = await createWorld('deployment-view-empty');
    const realData = await loadConsoleData(launch.client, {
      worldId: launch.world.worldId,
    });
    const view = render(
      <WorldConsoleView
        state={{
          kind: 'ready',
          data: { ...realData, events: [], cursor: 0 },
        }}
        onRefresh={noRefresh}
        onOperation={noOperation}
      />
    );
    expect(
      view.getByText('No resources have been projected yet.')
    ).toBeTruthy();
    expect(view.getByText('No events exist after this cursor.')).toBeTruthy();
    expect(view.getByText('No deployment outputs.')).toBeTruthy();
    expect(view.getByText('No deployment diagnostics.')).toBeTruthy();
    expect(view.getByText('not selected')).toBeTruthy();
    expect(
      view.getByText(
        'Select a deployment in the Console URL before executing a command.'
      )
    ).toBeTruthy();
  });

  it('未実装 provider の diagnostics に MissingProvider と source を表示する', async () => {
    const rejectedId = 'deployment-view-rejected';
    const launch = await createWorld(rejectedId);
    const response = await deploy(
      launch.world.worldId,
      launch.token,
      'unavailable-provider',
      'declarative',
      'unavailable.json',
      '{}'
    );
    expect(response.status).toBe(422);
    const data = await loadConsoleData(launch.client, {
      worldId: launch.world.worldId,
      deploymentId: rejectedId,
    });
    const view = render(
      <WorldConsoleView
        state={{ kind: 'ready', data }}
        onRefresh={noRefresh}
        onOperation={noOperation}
      />
    );
    expect(view.getByText('MissingProvider')).toBeTruthy();
    expect(view.getAllByText(/unavailable\.json/).length).toBeGreaterThan(0);
    expect(view.getAllByText('failed').length).toBeGreaterThan(0);
  });

  it('未知 status を StatusIndicator の pending 表示へフォールバックする', () => {
    expect(statusIndicatorType('mystery-status')).toBe('pending');
    expect(statusIndicatorType('running')).toBe('success');
    expect(statusIndicatorType('ready')).toBe('success');
    expect(statusIndicatorType('failed')).toBe('error');
    expect(statusIndicatorType('deploying')).toBe('in-progress');
  });
});

describe('Console operation form', () => {
  it('送信中は Executing… と disabled を表示し、成功で role=status を出す', async () => {
    const { client, data, deployment } = await readyWorldData();
    const service = data.resources.resources.find(
      (resource) => resource.resourceType === CLOUD_RUN_SERVICE
    );
    if (!service) throw new Error('Cloud Run service projection がありません');
    let release = (): void => undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let refreshes = 0;
    const realAction = createConsoleOperationAction(
      client,
      { worldId: data.worldId, deploymentId: deployment.deploymentId },
      () => {
        refreshes += 1;
      }
    );
    const view = render(
      <WorldConsoleView
        state={{ kind: 'ready', data }}
        onRefresh={noRefresh}
        onOperation={async (formData) => {
          await realAction(formData);
          await gate;
        }}
      />
    );
    fireEvent.input(view.getByLabelText('Provider'), {
      target: { value: 'gcp' },
    });
    fireEvent.input(view.getByLabelText('Engine'), {
      target: { value: 'infra-manager' },
    });
    fireEvent.input(view.getByLabelText('Service'), {
      target: { value: 'run' },
    });
    fireEvent.input(view.getByLabelText('Resource type'), {
      target: { value: CLOUD_RUN_SERVICE },
    });
    fireEvent.input(view.getByLabelText('Operation'), {
      target: { value: 'UpdateService' },
    });
    fireEvent.input(view.getByLabelText('Input JSON object'), {
      target: {
        value: JSON.stringify({
          id: service.resourceId,
          patch: { minInstanceCount: 1, maxInstanceCount: 3 },
        }),
      },
    });
    fireEvent.input(view.getByLabelText('Idempotency key'), {
      target: { value: 'console-view-update-service' },
    });
    const submit = view.getByText('Execute command').closest('button');
    if (!submit) throw new Error('Execute command button がありません');
    fireEvent.click(submit);
    await view.findByText('Executing…');
    const pendingButton = view.getByText('Executing…').closest('button');
    expect(pendingButton?.disabled).toBe(true);
    release();
    const result = await view.findByText(
      'Command accepted. Waiting for the shared projection.'
    );
    expect(result.closest('[role="status"]')).toBeTruthy();
    expect(refreshes).toBe(1);
    expect(view.getByText('Execute command')).toBeTruthy();
  });

  it('ConsoleOperationResult が success を role=status、error を role=alert、idle を非表示にする', () => {
    const success = render(
      <ConsoleOperationResult
        state={{ kind: 'success', message: 'Command accepted.' }}
      />
    );
    expect(success.getByRole('status').textContent).toContain(
      'Command accepted.'
    );
    success.unmount();
    const error = render(
      <ConsoleOperationResult
        state={{ kind: 'error', message: 'Command rejected.' }}
      />
    );
    expect(error.getByRole('alert').textContent).toContain('Command rejected.');
    error.unmount();
    const idle = render(<ConsoleOperationResult state={{ kind: 'idle' }} />);
    expect(idle.container.innerHTML).toBe('');
  });
});
