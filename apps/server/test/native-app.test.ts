import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import {
  AWS_NATIVE_DEPLOYMENT_HEADER,
  AWS_NATIVE_TARGET_HEADER,
  AWS_NATIVE_WORLD_HEADER,
  AwsProvider,
} from '@tenkacloud/simulator-provider-aws';
import {
  AZURE_ARM_CONTAINER_API_VERSION,
  AZURE_ARM_DEPLOYMENT_HEADER,
  AZURE_ARM_TARGET_HEADER,
  AZURE_ARM_WORLD_HEADER,
  AzureProvider,
} from '@tenkacloud/simulator-provider-azure';
import {
  GCP_REST_DEPLOYMENT_HEADER,
  GCP_REST_TARGET_HEADER,
  GCP_REST_WORLD_HEADER,
  GcpProvider,
} from '@tenkacloud/simulator-provider-gcp';
import {
  SAKURA_APPRUN_API_BASE_PATH,
  SAKURA_APPRUN_DEPLOYMENT_HEADER,
  SAKURA_APPRUN_TARGET_HEADER,
  SAKURA_APPRUN_WORLD_HEADER,
  SakuraProvider,
} from '@tenkacloud/simulator-provider-sakura';
import { createNativeGatewayHandler } from '../src/native-app';

const ORIGIN = 'https://simulator.example.test';
const CREDENTIALS = {
  awsAccessKeyId: 'TCSIMLOCALACCESS01',
  azureCredential: 'tcsim_azure_native_credential',
  gcpCredential: 'tcsim_google_native_credential',
  sakuraCredential: 'tcsim_sakura_native_token:tcsim_sakura_native_secret',
};

const SAKURA_APPLICATION = {
  name: 'native-app',
  timeout_seconds: 60,
  port: 8080,
  min_scale: 0,
  max_scale: 1,
  scale_target_concurrency: 100,
  components: [
    {
      name: 'web',
      max_cpu: '0.5',
      max_memory: '1Gi',
      deploy_source: {
        container_registry: {
          image:
            'registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
          server: 'registry.example',
          username: 'participant',
          password: 'registry-secret',
        },
      },
    },
  ],
};

interface DeploymentContext {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly outputs: Readonly<Record<string, string>>;
}

let directory: string;
let store: SimulationStore;
let core: SimulationCore;

beforeEach(async () => {
  directory = await mkdtemp(join(tmpdir(), 'simulator-native-server-'));
  store = new SimulationStore(join(directory, 'simulation.sqlite'));
  core = new SimulationCore(
    store,
    new ProviderRegistry([
      new AwsProvider(),
      new AzureProvider(),
      new GcpProvider(),
      new SakuraProvider(),
    ])
  );
});

afterEach(async () => {
  store.close();
  await rm(directory, { recursive: true, force: true });
});

async function deploy(
  provider: 'aws' | 'azure' | 'gcp' | 'sakura'
): Promise<DeploymentContext> {
  const deploymentId = `${provider}-native-deployment`;
  const world = core.createWorld(
    {
      tenantId: 'native-tenant',
      eventId: 'native-event',
      teamId: 'native-team',
      deploymentId,
    },
    `${provider}-native-world-key`
  );
  const definitions = {
    aws: {
      engine: 'cloudformation',
      entry: 'template.yaml',
      templateBody: await readFile(
        new URL(
          '../../../providers/aws/tests/fixtures/update-stack.yaml',
          import.meta.url
        ),
        'utf8'
      ),
    },
    azure: {
      engine: 'bicep',
      entry: 'container-app.bicep',
      templateBody: await readFile(
        new URL(
          '../../../providers/azure/src/fixtures/container-app.bicep',
          import.meta.url
        ),
        'utf8'
      ),
    },
    gcp: {
      engine: 'infra-manager',
      entry: 'terraform/main.tf',
      templateBody: await readFile(
        new URL(
          '../../../providers/gcp/test/fixtures/hello-multicloud/main.tf',
          import.meta.url
        ),
        'utf8'
      ),
    },
    sakura: {
      engine: 'apprun',
      entry: 'application.json',
      templateBody: JSON.stringify(SAKURA_APPLICATION),
    },
  } as const;
  const definition = definitions[provider];
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: `${provider}-native-problem`,
      runtime: {
        provider,
        engine: definition.engine,
        entry: definition.entry,
      },
      templateBody: definition.templateBody,
    },
    `${provider}-native-deployment-key`
  );
  return {
    worldId: world.worldId,
    deploymentId,
    outputs: deployment.outputs['default'] ?? {},
  };
}

function gateway() {
  return createNativeGatewayHandler({
    core,
    credentials: CREDENTIALS,
    simulatorOrigin: ORIGIN,
  });
}

function requiredResponse(response: Response | undefined): Response {
  if (!response) throw new Error('native gateway response がありません');
  return response;
}

function routedHeaders(
  authorization: string,
  context: DeploymentContext,
  names: readonly [string, string, string]
): Headers {
  return new Headers({
    authorization,
    [names[0]]: context.worldId,
    [names[1]]: context.deploymentId,
    [names[2]]: 'default',
  });
}

function awsRequest(context: DeploymentContext): Request {
  const body = new URLSearchParams({
    Action: 'GetCallerIdentity',
    Version: '2011-06-15',
  }).toString();
  const headers = routedHeaders('', context, [
    AWS_NATIVE_WORLD_HEADER,
    AWS_NATIVE_DEPLOYMENT_HEADER,
    AWS_NATIVE_TARGET_HEADER,
  ]);
  headers.delete('authorization');
  headers.set('content-type', 'application/x-www-form-urlencoded');
  headers.set('x-amz-date', '20260712T010203Z');
  const signedHeaders = ['host', ...headers.keys()].sort();
  const signature = createHash('sha256').update(body).digest('hex');
  headers.set(
    'authorization',
    `AWS4-HMAC-SHA256 Credential=${CREDENTIALS.awsAccessKeyId}/20260712/us-east-1/sts/aws4_request, SignedHeaders=${signedHeaders.join(';')}, Signature=${signature}`
  );
  return new Request(ORIGIN, { method: 'POST', headers, body });
}

describe('native provider gateway の server routing', () => {
  it('AWS Query を protocol response のまま実 SQLite world へ接続する', async () => {
    const context = await deploy('aws');
    const response = requiredResponse(await gateway()(awsRequest(context)));
    expect(response.status).toBe(200);
    expect(response.headers.get('content-type')).toContain('xml');
    expect(await response.text()).toContain('<Account>000000000000</Account>');
  });

  it('Azure、GCP、Sakura の REST request を同じ core command 境界で実行する', async () => {
    const azure = await deploy('azure');
    const azureId = azure.outputs['containerAppId'];
    if (!azureId) throw new Error('Azure Container App output がありません');
    const azureResponse = await gateway()(
      new Request(
        `${ORIGIN}${azureId}?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`,
        {
          headers: routedHeaders(
            `Bearer ${CREDENTIALS.azureCredential}`,
            azure,
            [
              AZURE_ARM_WORLD_HEADER,
              AZURE_ARM_DEPLOYMENT_HEADER,
              AZURE_ARM_TARGET_HEADER,
            ]
          ),
        }
      )
    );
    const requiredAzure = requiredResponse(azureResponse);
    expect(requiredAzure.status).toBe(200);
    expect(await requiredAzure.json()).toMatchObject({ id: azureId });

    const gcp = await deploy('gcp');
    const service = store
      .resources(gcp.worldId)
      .find(
        (resource) => resource.resourceType === 'google_cloud_run_v2_service'
      );
    if (!service) throw new Error('GCP Cloud Run service がありません');
    const gcpPath = `/v2/projects/tenka-project/locations/asia-northeast1/services/${service.resourceId}`;
    const gcpResponse = await gateway()(
      new Request(`${ORIGIN}${gcpPath}`, {
        headers: routedHeaders(`Bearer ${CREDENTIALS.gcpCredential}`, gcp, [
          GCP_REST_WORLD_HEADER,
          GCP_REST_DEPLOYMENT_HEADER,
          GCP_REST_TARGET_HEADER,
        ]),
      })
    );
    const requiredGcp = requiredResponse(gcpResponse);
    expect(requiredGcp.status).toBe(200);
    expect(await requiredGcp.json()).toMatchObject({
      name: expect.stringContaining(service.resourceId),
    });

    const sakura = await deploy('sakura');
    const applicationId = sakura.outputs['ApplicationId'];
    if (!applicationId) throw new Error('Sakura AppRun output がありません');
    const sakuraResponse = await gateway()(
      new Request(
        `${ORIGIN}${SAKURA_APPRUN_API_BASE_PATH}/applications/${applicationId}`,
        {
          headers: routedHeaders(
            `Basic ${btoa(CREDENTIALS.sakuraCredential)}`,
            sakura,
            [
              SAKURA_APPRUN_WORLD_HEADER,
              SAKURA_APPRUN_DEPLOYMENT_HEADER,
              SAKURA_APPRUN_TARGET_HEADER,
            ]
          ),
        }
      )
    );
    const requiredSakura = requiredResponse(sakuraResponse);
    expect(requiredSakura.status).toBe(200);
    expect(await requiredSakura.json()).toMatchObject({ id: applicationId });
  });

  it('unknown route は委譲し、gateway と core の error を JSON status へ変換する', async () => {
    expect(await gateway()(new Request(`${ORIGIN}/unknown`))).toBeUndefined();

    const azure = await deploy('azure');
    const unauthorized = await gateway()(
      new Request(
        `${ORIGIN}/subscriptions/sub/resourceGroups/group/providers/Microsoft.App/containerApps/app?api-version=${AZURE_ARM_CONTAINER_API_VERSION}`,
        {
          headers: routedHeaders('Bearer real-azure-token', azure, [
            AZURE_ARM_WORLD_HEADER,
            AZURE_ARM_DEPLOYMENT_HEADER,
            AZURE_ARM_TARGET_HEADER,
          ]),
        }
      )
    );
    const requiredUnauthorized = requiredResponse(unauthorized);
    expect(requiredUnauthorized.status).toBe(401);
    expect(await requiredUnauthorized.json()).toEqual({
      error: {
        code: 'UnauthorizedOperation',
        message: 'real Azure bearer credentials are not accepted',
      },
    });

    const missingWorldHeaders = new Headers({
      authorization: `Bearer ${CREDENTIALS.gcpCredential}`,
      [GCP_REST_WORLD_HEADER]: 'missing-world',
      [GCP_REST_DEPLOYMENT_HEADER]: 'missing-deployment',
    });
    const missing = await gateway()(
      new Request(
        `${ORIGIN}/v2/projects/tenka-project/locations/asia-northeast1/services/missing-service`,
        { headers: missingWorldHeaders }
      )
    );
    const requiredMissing = requiredResponse(missing);
    expect(requiredMissing.status).toBe(404);
    expect(await requiredMissing.json()).toMatchObject({
      error: { code: 'NotFound' },
    });
  });
});
