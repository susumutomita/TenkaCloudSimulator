#!/usr/bin/env bun
import { randomUUID } from 'node:crypto';
import { LaunchTokenAuthority } from '../apps/server/src/auth.ts';
import { SimulatorClient } from '../tools/cli/src/client.ts';

function requiredEnvironment(name: string): string {
  const value = process.env[name];
  if (!value) throw new TypeError(`${name} is required`);
  return value;
}

const baseUrl = requiredEnvironment('SIMULATOR_URL');
const encodedSecret = requiredEnvironment('SIMULATOR_LAUNCH_SECRET');
const workloadImage = requiredEnvironment('SIMULATOR_WORKLOAD_IMAGE');
const deploymentId = `container-workload-${randomUUID()}`;
const namespace = {
  tenantId: 'container-workload-tenant',
  eventId: 'container-workload-event',
  teamId: 'container-workload-team',
  deploymentId,
};
const authority = new LaunchTokenAuthority(
  Buffer.from(encodedSecret, 'base64url')
);
const client = new SimulatorClient(baseUrl, authority.issue(namespace));
let worldId: string | undefined;
let endpoint: string | undefined;

try {
  const capabilities = await client.capabilities();
  if (
    capabilities.capabilities?.some(
      (capability) =>
        capability.resourceType === 'Runtime::Workload' &&
        capability.operation === 'Materialize' &&
        capability.fidelity.includes('data-plane')
    ) !== true
  ) {
    throw new Error(
      'production image does not advertise workload materialization'
    );
  }

  const world = await client.createWorld({
    ...namespace,
    seed: 'container-workload-seed',
  });
  worldId = world.worldId;
  const deployment = await client.createDeployment(world.worldId, {
    problemId: 'container-workload-e2e',
    runtime: {
      provider: 'aws',
      engine: 'cloudformation',
      entry: 'template.json',
    },
    templateBody: JSON.stringify({
      Resources: { ArtifactBucket: { Type: 'AWS::S3::Bucket' } },
    }),
    simulationOverlay: {
      schemaVersion: '1',
      workloads: [
        {
          id: 'api',
          targetId: 'default',
          resourceRef: 'ArtifactBucket',
          image: workloadImage,
          command: [
            'sh',
            '-c',
            "printf 'ready' > /tmp/healthz; exec httpd -f -p 8080 -h /tmp",
          ],
          containerPort: 8080,
          healthPath: '/healthz',
        },
      ],
    },
  });
  if (deployment.status !== 'running') {
    throw new Error(`workload deployment is ${deployment.status}`);
  }
  const output = deployment.outputs['Workload.api.Endpoint'];
  if (typeof output !== 'string' || !output.startsWith('http://127.0.0.1:')) {
    throw new Error('workload endpoint is not host-loopback scoped');
  }
  endpoint = output;
  const health = await fetch(`${endpoint}/healthz`, {
    signal: AbortSignal.timeout(5_000),
  });
  const healthBody = await health.text();
  if (health.status !== 200 || healthBody !== 'ready') {
    throw new Error(`workload health failed with HTTP ${health.status}`);
  }
} finally {
  if (worldId !== undefined) await client.deleteWorld(worldId);
}

if (endpoint === undefined)
  throw new Error('workload endpoint was not produced');
const remaining = await fetch(`${endpoint}/healthz`, {
  signal: AbortSignal.timeout(1_000),
}).catch(() => undefined);
if (remaining !== undefined) {
  await remaining.body?.cancel();
  throw new Error('workload endpoint remained reachable after world cleanup');
}

process.stdout.write(
  `${JSON.stringify({ deploymentId, endpoint, health: 'ready', cleanup: 'complete' })}\n`
);
