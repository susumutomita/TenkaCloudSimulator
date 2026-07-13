import { afterEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  HTTP_ENDPOINT_RESOURCE,
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { AwsProvider } from '@tenkacloud/simulator-provider-aws';
import {
  CLOUD_RUN_SERVICE,
  GcpProvider,
} from '@tenkacloud/simulator-provider-gcp';

const contexts: Array<{
  readonly directory: string;
  readonly store: SimulationStore;
}> = [];

const AWS_TEMPLATE_SHA256 =
  'e231363a8af30b98ddd7985740af611b3c2e31b6b62f6abd072e8243129381d0';
const GCP_TERRAFORM_SHA256 =
  'fa63e59f577edb0ea0e6bd7b78cd3cb41ef8032a85ec5118db3d77d25af7f20b';

afterEach(async () => {
  while (contexts.length > 0) {
    const context = contexts.pop();
    if (!context) continue;
    context.store.close();
    await rm(context.directory, { recursive: true, force: true });
  }
});

async function pinnedFixture(
  path: string,
  expectedSha256: string
): Promise<string> {
  const contents = await readFile(new URL(path, import.meta.url), 'utf8');
  const actualSha256 = createHash('sha256').update(contents).digest('hex');
  if (actualSha256 !== expectedSha256) {
    throw new Error(
      `${path} drifted from the reviewed composite regression fixture: ${actualSha256}`
    );
  }
  return contents;
}

describe('hello-multicloud AWS/GCP composite regression', () => {
  it('AWS Function URL と GCP Cloud Run を同じ world へ配備して両 scoring probe を200にする', async () => {
    const directory = await mkdtemp(join(tmpdir(), 'hello-multicloud-'));
    const store = new SimulationStore(join(directory, 'simulation.sqlite'));
    contexts.push({ directory, store });
    const core = new SimulationCore(
      store,
      new ProviderRegistry([new AwsProvider(), new GcpProvider()])
    );
    const deploymentId = 'hello-multicloud-deployment';
    const world = core.createWorld(
      {
        tenantId: 'tenant',
        eventId: 'event',
        teamId: 'team',
        deploymentId,
      },
      'hello-multicloud-world'
    );
    const awsTemplate = await pinnedFixture(
      '../../../providers/aws/tests/fixtures/hello-multicloud.yaml',
      AWS_TEMPLATE_SHA256
    );
    const gcpTerraform = await pinnedFixture(
      '../../../providers/gcp/test/fixtures/hello-multicloud/main.tf',
      GCP_TERRAFORM_SHA256
    );
    const runtime = {
      kind: 'composite' as const,
      targets: [
        {
          id: 'aws-hello',
          provider: 'aws',
          engine: 'cloudformation',
          entry: 'template.yaml',
        },
        {
          id: 'gcp-hello',
          provider: 'gcp',
          engine: 'infra-manager',
          entry: 'gcp/terraform',
        },
      ],
    };
    const deployment = core.createDeployment(
      world.worldId,
      {
        deploymentId,
        problemId: 'hello-multicloud',
        runtime,
        metadata: {
          scoring: {
            kind: 'composite-probe',
            success: 'all',
            pointsAllOk: 100,
            targets: [
              {
                targetId: 'aws-hello',
                probe: 'https',
                outputKey: 'AwsHelloUrl',
                expectStatus: [200],
              },
              {
                targetId: 'gcp-hello',
                probe: 'https',
                outputKey: 'GcpHelloUrl',
                expectStatus: [200],
              },
            ],
          },
        },
        templateBody: JSON.stringify({
          format: 'tenkacloud.simulator.artifacts.v1',
          targets: [
            {
              id: 'aws-hello',
              provider: 'aws',
              engine: 'cloudformation',
              entry: 'template.yaml',
              artifacts: [{ path: 'template.yaml', content: awsTemplate }],
            },
            {
              id: 'gcp-hello',
              provider: 'gcp',
              engine: 'infra-manager',
              entry: 'gcp/terraform',
              artifacts: [
                {
                  path: 'gcp/terraform/main.tf',
                  content: gcpTerraform,
                },
              ],
            },
          ],
        }),
      },
      'hello-multicloud-deployment-key'
    );

    const awsUrl = Reflect.get(
      deployment.outputs['aws-hello'] ?? {},
      'AwsHelloUrl'
    );
    const gcpUrl = Reflect.get(
      deployment.outputs['gcp-hello'] ?? {},
      'GcpHelloUrl'
    );
    if (typeof awsUrl !== 'string' || typeof gcpUrl !== 'string') {
      throw new Error('composite hello outputs are missing');
    }
    expect(awsUrl).toContain('.lambda-url.us-east-1.on.aws/');
    expect(gcpUrl).toContain('.run.gcp.local');

    const awsRequest = core.executeCommand(
      world.worldId,
      {
        deploymentId,
        targetId: 'aws-hello',
        provider: 'aws',
        engine: 'cloudformation',
        service: 'http',
        operation: 'Request',
        resourceType: HTTP_ENDPOINT_RESOURCE,
        input: {
          Url: awsUrl,
          Method: 'GET',
          Path: '/',
          Headers: {},
          Body: '',
        },
      },
      'aws-function-url-request'
    );
    const awsProbe = await core.executeCommandAsync(
      world.worldId,
      {
        deploymentId,
        targetId: 'aws-hello',
        provider: 'aws',
        engine: 'cloudformation',
        service: 'http',
        operation: 'Probe',
        resourceType: HTTP_ENDPOINT_RESOURCE,
        input: { Url: awsUrl },
      },
      'aws-function-url-probe'
    );

    const gcpService = store
      .resources(world.worldId)
      .find((resource) => resource.resourceType === CLOUD_RUN_SERVICE);
    if (!gcpService) throw new Error('GCP Cloud Run service is missing');
    const gcpRequest = core.executeCommand(
      world.worldId,
      {
        deploymentId,
        targetId: 'gcp-hello',
        provider: 'gcp',
        engine: 'infra-manager',
        service: 'http',
        operation: 'Request',
        resourceType: HTTP_ENDPOINT_RESOURCE,
        input: { Method: 'GET', Path: '/', Headers: {}, Body: '' },
      },
      'gcp-cloud-run-request'
    );
    const gcpProbe = core.executeCommand(
      world.worldId,
      {
        deploymentId,
        targetId: 'gcp-hello',
        provider: 'gcp',
        engine: 'infra-manager',
        service: 'http',
        operation: 'Probe',
        resourceType: HTTP_ENDPOINT_RESOURCE,
        input: { id: gcpService.resourceId },
      },
      'gcp-cloud-run-probe'
    );

    expect(awsRequest).toMatchObject({
      StatusCode: 200,
      Body: expect.stringContaining('hello-multicloud'),
    });
    expect(awsProbe).toMatchObject({ Ok: true, StatusCode: 200 });
    expect(gcpRequest).toMatchObject({ StatusCode: 200 });
    expect(gcpProbe).toMatchObject({ status: 200 });
    const statuses = [
      Reflect.get(awsProbe, 'StatusCode'),
      Reflect.get(gcpProbe, 'status'),
    ];
    const points = statuses.every((status) => status === 200) ? 100 : 0;
    expect(points).toBe(100);
  });
});
