import { afterEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import {
  collectCatalog,
  compareInventory,
  readCapabilityManifest,
  serializeReport,
} from '../src/index.ts';
import type {
  CapabilityEntry,
  CapabilityManifest,
  Requirement,
} from '../src/model.ts';

const temporaryDirectories: string[] = [];

async function temporaryDirectory(): Promise<string> {
  const directory = await mkdtemp(
    join(tmpdir(), 'tenkacloud-catalog-scanner-')
  );
  temporaryDirectories.push(directory);
  return directory;
}

async function writeText(
  root: string,
  path: string,
  contents: string
): Promise<void> {
  const absolutePath = join(root, path);
  await mkdir(dirname(absolutePath), { recursive: true });
  await writeFile(absolutePath, contents, 'utf8');
}

function capabilityIdentity(entry: CapabilityEntry): string {
  return [
    entry.provider,
    entry.service,
    entry.resourceType,
    entry.operation,
  ].join('|');
}

const fidelityRank = { L0: 0, L1: 1, L2: 2, L3: 3, L4: 4 } as const;

function coveringManifest(requirements: Requirement[]): CapabilityManifest {
  const capabilities = new Map<string, CapabilityEntry>();
  for (const requirement of requirements) {
    const entry: CapabilityEntry = {
      provider: requirement.provider,
      service: requirement.service,
      resourceType: requirement.resourceType,
      operation: requirement.operation,
      fidelity: requirement.fidelity,
    };
    const identity = capabilityIdentity(entry);
    const current = capabilities.get(identity);
    if (
      current === undefined ||
      fidelityRank[current.fidelity] < fidelityRank[entry.fidelity]
    ) {
      capabilities.set(identity, entry);
    }
  }
  return {
    schemaVersion: '1',
    version: 'test-capabilities',
    capabilities: [...capabilities.values()].sort((left, right) =>
      capabilityIdentity(left).localeCompare(capabilityIdentity(right))
    ),
  };
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories
      .splice(0)
      .map((directory) => rm(directory, { recursive: true, force: true }))
  );
});

describe('legacy CloudFormation catalog を走査するとき', () => {
  it('runtime を正規化し resource、IAM、endpoint、probe、disruption を根拠付きで返す', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/hello-world/metadata.json',
      `${JSON.stringify(
        {
          id: 'hello-world',
          category: 'Challenge',
          status: 'ready',
          cfnTemplate: 'template.yaml',
          endpoints: [
            {
              slot: 'api',
              default: { from: 'cfn-output', key: 'ApiUrl' },
              overridable: true,
            },
          ],
          scoring: {
            kind: 'uptime-flat',
            endpoints: [{ slot: 'api', path: '/healthz', expectStatus: [200] }],
          },
          disruptions: [
            {
              id: 'stop-api',
              eventDetailType: 'OutageFired',
              action: {
                kind: 'ssm-run-command',
                targetRef: 'InstanceId',
                revert: { afterSeconds: 60 },
              },
            },
          ],
        },
        null,
        2
      )}\n`
    );
    await writeText(
      root,
      'challenges/hello-world/template.yaml',
      `AWSTemplateFormatVersion: "2010-09-09"
Resources:
  HelloParameter:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /hello
      Type: String
      Value: hello
  ParticipantViewerRole:
    Type: AWS::IAM::Role
    Properties:
      Policies:
        - PolicyName: ReadParameter
          PolicyDocument:
            Statement:
              - Effect: Allow
                Action:
                  - ssm:GetParameter
                  - ssm:PutParameter
                Resource: "*"
Outputs:
  ApiUrl:
    Value: http://127.0.0.1
  InstanceId:
    Value: i-local
`
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toEqual([]);
    expect(inventory.problems).toHaveLength(1);
    expect(inventory.problems[0]?.targets).toEqual([
      {
        targetId: 'default',
        provider: 'aws',
        engine: 'cloudformation',
        entry: 'template.yaml',
        delivery: 'cloud',
      },
    ]);
    expect(
      inventory.requirements.map((requirement) => ({
        service: requirement.service,
        resourceType: requirement.resourceType,
        operation: requirement.operation,
        fidelity: requirement.fidelity,
        plane: requirement.plane,
        origin: requirement.origin,
        classification: requirement.classification,
      }))
    ).toContainEqual({
      service: 'ssm',
      resourceType: 'AWS::SSM::Parameter',
      operation: 'lifecycle',
      fidelity: 'L1',
      plane: 'deploy',
      origin: 'iac-resource',
      classification: 'binding',
    });
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        service: 'ssm',
        resourceType: '*',
        operation: 'GetParameter',
        fidelity: 'L2',
        plane: 'participant',
        origin: 'iam-policy',
        classification: 'authorization-inventory',
        source: expect.objectContaining({
          path: 'challenges/hello-world/template.yaml',
          line: 18,
        }),
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        service: 'runtime',
        operation: 'ResolveEndpoint',
        plane: 'scoring',
        origin: 'metadata-endpoint',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        service: 'http',
        operation: 'Probe',
        fidelity: 'L4',
        origin: 'metadata-probe',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        service: 'ssm',
        resourceType: 'AWS::EC2::Instance',
        operation: 'SendCommand',
        fidelity: 'L4',
        plane: 'operator',
        origin: 'metadata-disruption',
      })
    );

    const report = compareInventory(
      inventory,
      coveringManifest(inventory.requirements)
    );
    expect(report.status).toBe('covered');
    const binding = inventory.requirements.filter(
      (requirement) => requirement.classification === 'binding'
    );
    const authorizationInventory = inventory.requirements.filter(
      (requirement) => requirement.classification === 'authorization-inventory'
    );
    expect(report.summary).toEqual({
      problems: 1,
      targets: 1,
      requirements: binding.length,
      covered: binding.length,
      missing: 0,
      insufficient: 0,
      authorizationInventory: {
        requirements: authorizationInventory.length,
        covered: authorizationInventory.length,
        missing: 0,
        insufficient: 0,
      },
      invalid: 0,
    });
    expect(serializeReport(report)).toBe(serializeReport(report));
    expect(serializeReport(report)).not.toContain(root);
  });

  it('YAML Action field だけを authorization inventory にし token 文字列は無視する', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/structured-actions/metadata.json',
      `${JSON.stringify({
        id: 'structured-actions',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await writeText(
      root,
      'challenges/structured-actions/template.yaml',
      `Description: "NO lambda:GetFunction* and NO cloudformation:DescribeStacks"
Resources:
  ParticipantViewerRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "example lambda:DeleteFunction token"
      Policies:
        - PolicyName: Session
          PolicyDocument:
            Statement:
              - Effect: Allow
                Action: ssm:StartSession
                Resource: "*"
  Permission:
    Type: AWS::Lambda::Permission
    Properties:
      Action: lambda:InvokeFunction
      FunctionName: local
      Principal: elasticloadbalancing.amazonaws.com
  Script:
    Type: AWS::SSM::Parameter
    Properties:
      Type: String
      Value: |
        # Action: iam:PassRole
        aws lambda update-function-code
`
    );

    const inventory = await collectCatalog(root);
    const authorization = inventory.requirements.filter(
      (requirement) => requirement.classification === 'authorization-inventory'
    );

    expect(inventory.diagnostics).toEqual([]);
    expect(
      authorization.map((requirement) => requirement.operation).sort()
    ).toEqual(['InvokeFunction', 'StartSession']);
    expect(
      authorization.every((requirement) => requirement.origin === 'iam-policy')
    ).toBe(true);

    const manifest = coveringManifest(
      inventory.requirements.filter(
        (requirement) => requirement.classification === 'binding'
      )
    );
    manifest.capabilities.push({
      provider: 'aws',
      service: 'ssm',
      resourceType: '*',
      operation: 'StartSession',
      fidelity: 'L1',
    });
    const report = compareInventory(inventory, manifest);
    expect(report.status).toBe('covered');
    expect(report.summary.missing).toBe(0);
    expect(report.summary.authorizationInventory).toEqual({
      requirements: 2,
      covered: 0,
      missing: 1,
      insufficient: 1,
    });
  });
});

describe('Composite Runtime catalog を走査するとき', () => {
  it('AWS CloudFormation と GCP Terraform を target ごとに抽出する', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/hello-multicloud/metadata.json',
      `${JSON.stringify(
        {
          id: 'hello-multicloud',
          category: 'Challenge',
          status: 'draft',
          runtime: {
            kind: 'composite',
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
          },
          scoring: {
            kind: 'composite-probe',
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
        null,
        2
      )}\n`
    );
    await writeText(
      root,
      'challenges/hello-multicloud/template.yaml',
      `Resources:
  HelloFunction:
    Type: AWS::Lambda::Function
`
    );
    await writeText(
      root,
      'challenges/hello-multicloud/gcp/terraform/main.tf',
      `resource "google_cloud_run_v2_service" "hello" {
  name = "hello"
}

resource "google_cloud_run_v2_service_iam_member" "public" {
  role   = "roles/run.invoker"
  member = "allUsers"
}
`
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toEqual([]);
    expect(
      inventory.problems[0]?.targets.map((target) => target.targetId)
    ).toEqual(['aws-hello', 'gcp-hello']);
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        targetId: 'aws-hello',
        provider: 'aws',
        resourceType: 'AWS::Lambda::Function',
        operation: 'lifecycle',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        targetId: 'gcp-hello',
        provider: 'gcp',
        service: 'run',
        resourceType: 'google_cloud_run_v2_service',
        operation: 'lifecycle',
        source: expect.objectContaining({ line: 1 }),
      })
    );
    expect(
      inventory.requirements.filter(
        (requirement) =>
          requirement.origin === 'metadata-probe' &&
          requirement.operation === 'Probe'
      )
    ).toHaveLength(2);
  });
});

describe('Docker local-play catalog を走査するとき', () => {
  it('既存 container runtime を cloud capability 対象外として保持する', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/local-drill/metadata.json',
      `${JSON.stringify({
        id: 'local-drill',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          provider: 'docker',
          engine: 'compose',
          entry: 'local/docker-compose.yml',
        },
        scoring: { kind: 'verify' },
      })}\n`
    );
    await writeText(
      root,
      'challenges/local-drill/local/docker-compose.yml',
      'services: {}\n'
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toEqual([]);
    expect(inventory.requirements).toEqual([]);
    expect(inventory.problems[0]?.targets[0]?.delivery).toBe('container');
    const report = compareInventory(inventory, {
      schemaVersion: '1',
      version: 'empty',
      capabilities: [],
    });
    expect(report.status).toBe('covered');
  });
});

describe('versioned simulation overlay を走査するとき', () => {
  it('target identity を継承し requirement、workload、artifact digest を catalog hash に統合する', async () => {
    const root = await temporaryDirectory();
    const artifact = 'services:\n  api:\n    image: example.invalid/api\n';
    const artifactHash = createHash('sha256').update(artifact).digest('hex');
    await writeText(
      root,
      'challenges/overlay-problem/metadata.json',
      `${JSON.stringify(
        {
          id: 'overlay-problem',
          category: 'Challenge',
          status: 'ready',
          cfnTemplate: 'template.yaml',
          simulationOverlay: {
            schemaVersion: '1',
            entry: 'simulation.json',
          },
        },
        null,
        2
      )}\n`
    );
    await writeText(
      root,
      'challenges/overlay-problem/template.yaml',
      'Resources:\n  Parameter:\n    Type: AWS::SSM::Parameter\n'
    );
    await writeText(
      root,
      'challenges/overlay-problem/services/docker-compose.yml',
      artifact
    );
    await writeText(
      root,
      'challenges/overlay-problem/simulation.json',
      `${JSON.stringify(
        {
          $schema: '../../SIMULATION_SCHEMA.json',
          schemaVersion: '1',
          requirements: [
            {
              targetId: 'default',
              service: 'http',
              resourceType: 'HTTP::Endpoint',
              operation: 'Request',
              fidelity: 'L4',
              plane: 'participant',
              artifact: {
                path: 'services/docker-compose.yml',
                sha256: artifactHash,
              },
            },
          ],
          workloads: [
            {
              id: 'api',
              targetId: 'default',
              resourceRef: 'Parameter',
              image: `registry.example/api@sha256:${'a'.repeat(64)}`,
              command: ['bun', 'server.ts'],
              containerPort: 8080,
              healthPath: '/healthz',
              artifact: {
                path: 'services/docker-compose.yml',
                sha256: artifactHash,
              },
            },
          ],
        },
        null,
        2
      )}\n`
    );

    const inventory = await collectCatalog(root);
    const overlayRequirements = inventory.requirements.filter(
      (requirement) => requirement.origin === 'simulation-overlay'
    );

    expect(inventory.diagnostics).toEqual([]);
    expect(overlayRequirements).toHaveLength(2);
    expect(overlayRequirements).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          targetId: 'default',
          provider: 'aws',
          engine: 'cloudformation',
          operation: 'Request',
          source: expect.objectContaining({
            path: 'challenges/overlay-problem/simulation.json',
            jsonPointer: '/requirements/0',
          }),
        }),
        expect.objectContaining({
          service: 'runtime',
          resourceType: 'Runtime::Workload',
          operation: 'Materialize',
          fidelity: 'L4',
        }),
      ])
    );
    const initialHash = inventory.catalogHash;
    await writeText(
      root,
      'challenges/overlay-problem/services/docker-compose.yml',
      `${artifact}# changed\n`
    );
    const changed = await collectCatalog(root);
    expect(changed.catalogHash).not.toBe(initialHash);
    expect(changed.diagnostics).toContainEqual(
      expect.objectContaining({
        code: 'INVALID_SIMULATION_OVERLAY',
        message: expect.stringContaining('sha256 does not match'),
      })
    );
  });
});

describe('capability coverage を比較するとき', () => {
  it('未登録を missing、fidelity 不足を insufficient として区別する', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/minimal/metadata.json',
      `${JSON.stringify({
        id: 'minimal',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await writeText(
      root,
      'challenges/minimal/template.yaml',
      `Resources:
  Role:
    Type: AWS::IAM::Role
  Parameter:
    Type: AWS::SSM::Parameter
`
    );
    const inventory = await collectCatalog(root);
    const roleRequirement = inventory.requirements.find(
      (requirement) => requirement.resourceType === 'AWS::IAM::Role'
    );
    expect(roleRequirement).toBeDefined();
    const manifest: CapabilityManifest = {
      schemaVersion: '1',
      version: 'partial',
      capabilities: [
        {
          provider: 'aws',
          service: 'iam',
          resourceType: 'AWS::IAM::Role',
          operation: 'lifecycle',
          fidelity: 'L1',
        },
      ],
    };

    const report = compareInventory(inventory, manifest);

    expect(report.status).toBe('failed');
    expect(report.summary.insufficient).toBe(1);
    expect(report.summary.missing).toBe(1);
    expect(report.requirements).toContainEqual(
      expect.objectContaining({
        resourceType: 'AWS::IAM::Role',
        coverage: { status: 'insufficient', availableFidelity: 'L1' },
      })
    );
  });

  it('IAM と同一 operation の overlay だけを binding として失敗させる', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/overlay-promotion/metadata.json',
      `${JSON.stringify({
        id: 'overlay-promotion',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
        simulationOverlay: { schemaVersion: '1', entry: 'simulation.json' },
      })}\n`
    );
    await writeText(
      root,
      'challenges/overlay-promotion/template.yaml',
      `Resources:
  ParticipantViewerRole:
    Type: AWS::IAM::Role
    Properties:
      Policies:
        - PolicyName: Invoke
          PolicyDocument:
            Statement:
              - Effect: Allow
                Action: lambda:InvokeFunction
                Resource: "*"
`
    );
    await writeText(
      root,
      'challenges/overlay-promotion/simulation.json',
      `${JSON.stringify({
        schemaVersion: '1',
        requirements: [
          {
            targetId: 'default',
            service: 'lambda',
            resourceType: '*',
            operation: 'InvokeFunction',
            fidelity: 'L4',
            plane: 'participant',
          },
        ],
      })}\n`
    );

    const inventory = await collectCatalog(root);
    const invocationRequirements = inventory.requirements.filter(
      (requirement) => requirement.operation === 'InvokeFunction'
    );
    const manifest = coveringManifest(
      inventory.requirements.filter(
        (requirement) =>
          requirement.classification === 'binding' &&
          requirement.operation !== 'InvokeFunction'
      )
    );
    const report = compareInventory(inventory, manifest);

    expect(invocationRequirements).toHaveLength(2);
    expect(
      invocationRequirements.map((requirement) => requirement.classification)
    ).toEqual(['authorization-inventory', 'binding']);
    expect(report.status).toBe('failed');
    expect(report.summary.missing).toBe(1);
    expect(report.summary.authorizationInventory.missing).toBe(1);
  });
});

describe('catalog 入力が不正なとき', () => {
  it('壊れた JSON、未対応 engine、未知 disruption、欠損 entry を invalid にする', async () => {
    const root = await temporaryDirectory();
    await writeText(root, 'challenges/broken-json/metadata.json', '{broken\n');
    await writeText(
      root,
      'challenges/unknown-engine/metadata.json',
      `${JSON.stringify({
        id: 'unknown-engine',
        category: 'Challenge',
        status: 'draft',
        runtime: { provider: 'azure', engine: 'bicep', entry: 'main.bicep' },
      })}\n`
    );
    await writeText(
      root,
      'challenges/unknown-engine/main.bicep',
      'resource x {}\n'
    );
    await writeText(
      root,
      'challenges/unknown-action/metadata.json',
      `${JSON.stringify({
        id: 'unknown-action',
        category: 'Challenge',
        status: 'draft',
        cfnTemplate: 'template.yaml',
        disruptions: [
          {
            id: 'unknown',
            eventDetailType: 'Unknown',
            action: {
              kind: 'provider-magic',
              targetRef: 'Target',
              revert: { afterSeconds: 1 },
            },
          },
        ],
      })}\n`
    );
    await writeText(
      root,
      'challenges/unknown-action/template.yaml',
      'Resources:\n  Role:\n    Type: AWS::IAM::Role\n'
    );
    await writeText(
      root,
      'challenges/missing-entry/metadata.json',
      `${JSON.stringify({
        id: 'missing-entry',
        category: 'Challenge',
        status: 'draft',
        cfnTemplate: 'template.yaml',
      })}\n`
    );

    const inventory = await collectCatalog(root);
    const codes = inventory.diagnostics.map((diagnostic) => diagnostic.code);

    expect(codes).toContain('INVALID_METADATA_JSON');
    expect(codes).toContain('UNSUPPORTED_ENGINE');
    expect(codes).toContain('UNKNOWN_DISRUPTION_ACTION');
    expect(codes).toContain('MISSING_ENTRY');
    const report = compareInventory(
      inventory,
      coveringManifest(inventory.requirements)
    );
    expect(report.status).toBe('failed');
    expect(report.summary.invalid).toBe(4);
  });

  it('problem 外を指す entry と metadata が無い catalog を拒否する', async () => {
    const root = await temporaryDirectory();
    await writeText(root, 'outside.yaml', 'Resources: {}\n');
    await writeText(
      root,
      'challenges/escape/metadata.json',
      `${JSON.stringify({
        id: 'escape',
        category: 'Challenge',
        status: 'draft',
        cfnTemplate: '../../outside.yaml',
      })}\n`
    );
    const inventory = await collectCatalog(root);
    expect(
      inventory.diagnostics.map((diagnostic) => diagnostic.code)
    ).toContain('ENTRY_OUTSIDE_PROBLEM');

    const emptyRoot = await temporaryDirectory();
    const emptyInventory = await collectCatalog(emptyRoot);
    expect(emptyInventory.diagnostics).toEqual([
      expect.objectContaining({ code: 'NO_PROBLEMS' }),
    ]);
  });
});

describe('capability manifest を読むとき', () => {
  it('正しい manifest を key 順に正規化する', async () => {
    const root = await temporaryDirectory();
    const path = join(root, 'capabilities.json');
    await writeFile(
      path,
      `${JSON.stringify({
        version: 'v1',
        capabilities: [
          {
            operation: 'PutParameter',
            resourceType: '*',
            fidelity: 'L2',
            provider: 'aws',
            service: 'ssm',
          },
        ],
        schemaVersion: '1',
      })}\n`,
      'utf8'
    );

    const manifest = await readCapabilityManifest(path);

    expect(manifest).toEqual({
      schemaVersion: '1',
      version: 'v1',
      capabilities: [
        {
          provider: 'aws',
          service: 'ssm',
          resourceType: '*',
          operation: 'PutParameter',
          fidelity: 'L2',
        },
      ],
    });
  });

  it('未知 field、未知 fidelity、重複 identity を明示的に失敗させる', async () => {
    const root = await temporaryDirectory();
    const path = join(root, 'capabilities.json');
    await writeFile(
      path,
      `${JSON.stringify({
        schemaVersion: '1',
        version: 'bad',
        unknown: true,
        capabilities: [
          {
            provider: 'aws',
            service: 'ssm',
            resourceType: '*',
            operation: 'GetParameter',
            fidelity: 'L9',
          },
          {
            provider: 'aws',
            service: 'ssm',
            resourceType: '*',
            operation: 'GetParameter',
            fidelity: 'L2',
          },
        ],
      })}\n`,
      'utf8'
    );

    await expect(readCapabilityManifest(path)).rejects.toThrow(
      /unknown|fidelity|duplicate/i
    );
  });

  it('存在しない manifest を読み取りエラーにする', async () => {
    const root = await temporaryDirectory();
    await expect(
      readCapabilityManifest(join(root, 'missing.json'))
    ).rejects.toThrow(/capability manifest/i);
  });
});

describe('report serialization を繰り返すとき', () => {
  it('同じ catalog bytes から同じ hash と JSON を返す', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/repeatable/metadata.json',
      `${JSON.stringify({
        id: 'repeatable',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await writeText(
      root,
      'challenges/repeatable/template.yaml',
      'Resources:\n  Parameter:\n    Type: AWS::SSM::Parameter\n'
    );

    const first = await collectCatalog(root);
    const second = await collectCatalog(root);
    const manifest = coveringManifest(first.requirements);

    expect(first.catalogHash).toBe(second.catalogHash);
    expect(serializeReport(compareInventory(first, manifest))).toBe(
      serializeReport(compareInventory(second, manifest))
    );
    expect(
      await readFile(join(root, 'challenges/repeatable/metadata.json'), 'utf8')
    ).toContain('repeatable');
  });
});
