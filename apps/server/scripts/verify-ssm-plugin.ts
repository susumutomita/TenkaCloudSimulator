#!/usr/bin/env bun
import { lstat, mkdtemp, readFile, realpath, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { AwsProvider } from '@tenkacloud/simulator-provider-aws';
import { createNativeGatewayHandler } from '../src/native-app';
import {
  type SsmSessionSocketData,
  SsmSessionStreamGateway,
} from '../src/ssm-session-stream';

const ACCESS_KEY_ID = 'TCSIMLOCALACCESS01';
const PLUGIN_ENV = 'SSM_SESSION_PLUGIN_BINARY';

async function releasedPlugin(): Promise<string> {
  const configured = process.env[PLUGIN_ENV];
  if (!configured?.startsWith('/')) {
    throw new TypeError(`${PLUGIN_ENV} must be an absolute path`);
  }
  const canonical = await realpath(configured);
  const stat = await lstat(canonical);
  if (!stat.isFile()) throw new TypeError(`${PLUGIN_ENV} must be a file`);
  return canonical;
}

function requiredExecutable(name: string): string {
  const executable = Bun.which(name);
  if (!executable) throw new TypeError(`${name} is required`);
  return executable;
}

async function main(): Promise<void> {
  const plugin = await releasedPlugin();
  const aws = requiredExecutable('aws');
  const expect = requiredExecutable('expect');
  const directory = await mkdtemp(join(tmpdir(), 'ssm-plugin-conformance-'));
  const store = new SimulationStore(join(directory, 'simulation.sqlite'));
  const core = new SimulationCore(
    store,
    new ProviderRegistry([new AwsProvider()])
  );
  const deploymentId = 'ssm-plugin-conformance';
  const world = core.createWorld(
    {
      tenantId: 'conformance-tenant',
      eventId: 'conformance-event',
      teamId: 'conformance-team',
      deploymentId,
      virtualTime: '2026-07-12T00:00:00.000Z',
    },
    'ssm-plugin-conformance-world'
  );
  const templateBody = await readFile(
    new URL(
      '../../../providers/aws/tests/fixtures/catalog-stack.yaml',
      import.meta.url
    ),
    'utf8'
  );
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'ssm-plugin-conformance',
      runtime: {
        provider: 'aws',
        engine: 'cloudformation',
        entry: 'template.yaml',
      },
      templateBody,
      metadata: { cfnParameters: { FlagSeed: 'conformance-seed' } },
    },
    'ssm-plugin-conformance-deployment'
  );
  const instanceId = deployment.outputs['default']?.['InstanceId'];
  if (!instanceId) throw new Error('conformance EC2 instance is missing');

  const stream = new SsmSessionStreamGateway({ core });
  let nativeGateway: ReturnType<typeof createNativeGatewayHandler> | undefined;
  const server = Bun.serve<SsmSessionSocketData>({
    hostname: '127.0.0.1',
    port: 0,
    fetch: async (request, bunServer) => {
      if (stream.handles(request)) return stream.upgrade(request, bunServer);
      return (
        (await nativeGateway?.(request)) ??
        new Response('Not Found', { status: 404 })
      );
    },
    websocket: stream.websocket,
  });
  nativeGateway = createNativeGatewayHandler({
    core,
    credentials: {
      awsAccessKeyId: ACCESS_KEY_ID,
      azureCredential: 'tcsim_azure_conformance_credential',
      gcpCredential: 'tcsim_google_conformance_credential',
      sakuraCredential:
        'tcsim_sakura_conformance_token:tcsim_sakura_conformance_secret',
    },
    simulatorOrigin: server.url.origin,
    beforeAwsCommand: (command) => stream.beforeAwsCommand(command),
    onAwsCommandSuccess: (command, response) =>
      stream.onAwsCommandSuccess(command, response),
  });

  try {
    const endpoint = `${server.url.origin}/v1/native/aws/${world.worldId}/${deploymentId}`;
    const expectProgram = String.raw`
set timeout 10
set aws $env(TCSIM_AWS_EXECUTABLE)
set target $env(TCSIM_SSM_TARGET)
set endpoint $env(TCSIM_SSM_ENDPOINT)
spawn $aws ssm start-session --target $target --endpoint-url $endpoint --no-cli-pager
expect {
  -exact "$ " {}
  timeout { exit 124 }
  eof { exit 125 }
}
send -- "systemctl stop nginx\r"
expect {
  -exact "$ " {}
  timeout { exit 126 }
  eof { exit 127 }
}
send -- "~."
expect eof
set result [wait]
exit [lindex $result 3]
`;
    const child = Bun.spawn([expect, '-c', expectProgram], {
      env: {
        ...process.env,
        PATH: `${dirname(plugin)}:${process.env['PATH'] ?? ''}`,
        AWS_ACCESS_KEY_ID: ACCESS_KEY_ID,
        AWS_SECRET_ACCESS_KEY: 'simulator-conformance-secret',
        AWS_DEFAULT_REGION: 'us-east-1',
        AWS_EC2_METADATA_DISABLED: 'true',
        TCSIM_AWS_EXECUTABLE: aws,
        TCSIM_SSM_TARGET: instanceId,
        TCSIM_SSM_ENDPOINT: endpoint,
      },
      stdout: 'pipe',
      stderr: 'pipe',
    });
    const stdout = new Response(child.stdout).text();
    const stderr = new Response(child.stderr).text();
    const exitCode = await child.exited;
    const [output, errorOutput] = await Promise.all([stdout, stderr]);
    if (exitCode !== 0) {
      throw new Error(
        `AWS CLI Session Manager exited ${exitCode}: ${errorOutput || output}`
      );
    }
    if (!output.includes('Starting session with SessionId:')) {
      throw new Error(
        `released plugin did not establish a session: stdout=${output} stderr=${errorOutput}`
      );
    }
    const instance = store
      .resources(world.worldId)
      .find((resource) => resource.properties['refValue'] === instanceId);
    const state = instance?.properties['state'];
    const services =
      state && typeof state === 'object' && !Array.isArray(state)
        ? (state as Record<string, unknown>)['services']
        : undefined;
    if (
      !services ||
      typeof services !== 'object' ||
      Array.isArray(services) ||
      (services as Record<string, unknown>)['nginx'] !== 'stopped'
    ) {
      throw new Error('released plugin command did not mutate EC2 state');
    }
    process.stdout.write(
      JSON.stringify(
        {
          awsCli: new TextDecoder()
            .decode(Bun.spawnSync([aws, '--version']).stdout)
            .trim(),
          plugin: new TextDecoder()
            .decode(Bun.spawnSync([plugin, '--version']).stdout)
            .trim(),
          sessionEstablished: true,
          catalogCommandApplied: true,
          hostShellExecuted: false,
        },
        null,
        2
      ) + '\n'
    );
  } finally {
    void server.stop(true);
    store.close();
    await rm(directory, { recursive: true, force: true });
  }
}

await main();
