import { afterEach, describe, expect, it } from 'bun:test';
import { mkdir, mkdtemp, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { runCommand } from '../src/command.ts';
import {
  collectCatalog,
  readCapabilityManifest,
  validateCapabilityManifest,
} from '../src/index.ts';
import { recordValue } from '../src/value.ts';

const temporaryDirectories: string[] = [];

async function temporaryDirectory(): Promise<string> {
  const directory = await mkdtemp(join(tmpdir(), 'tenkacloud-scanner-edge-'));
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

async function writeAwsProblem(
  root: string,
  directoryName: string,
  metadata: Record<string, unknown>,
  template = 'Resources:\n  Parameter:\n    Type: AWS::SSM::Parameter\n'
): Promise<void> {
  await writeText(
    root,
    `challenges/${directoryName}/metadata.json`,
    `${JSON.stringify(metadata, null, 2)}\n`
  );
  if (typeof recordValue(metadata, 'cfnTemplate') === 'string') {
    await writeText(
      root,
      `challenges/${directoryName}/template.yaml`,
      template
    );
  }
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories
      .splice(0)
      .map((directory) => rm(directory, { recursive: true, force: true }))
  );
});

describe('CloudFormation の多様な resource と action を走査するとき', () => {
  it('custom、network、permission と全 fidelity plane を分類する', async () => {
    const root = await temporaryDirectory();
    await writeAwsProblem(
      root,
      'fidelity-map',
      {
        id: 'fidelity-map',
        category: 'Challenge',
        status: 'ready',
        cfnTemplate: 'template.yaml',
      },
      `Resources:
  Hook:
    Type: Custom::LifecycleHook
  Network:
    Type: AWS::EC2::SecurityGroup
  InvokePermission:
    Type: AWS::Lambda::Permission
    Properties:
      Action: lambda:InvokeFunction
  ParticipantViewerRole:
    Type: AWS::IAM::Role
    Properties:
      Policies:
        - PolicyName: Operations
          PolicyDocument:
            Statement:
              - Action: [cloudshell:CreateSession, sts:GetCallerIdentity, ec2:CreateSecurityGroup, elasticloadbalancing:ModifyRule, wafv2:AssociateWebACL, s3:GetObject, ssm:StartSession, logs:FilterLogEvents]
  WorkloadRole:
    Type: AWS::IAM::Role
    Properties:
      Policies:
        - PolicyName: Runtime
          PolicyDocument:
            Statement:
              - Action: iam:PassRole
  MissingType:
    Properties: {}
Outputs:
  Done:
    Value: yes
`
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toEqual([]);
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        resourceType: 'Custom::LifecycleHook',
        service: 'cloudformation',
        fidelity: 'L4',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        resourceType: 'AWS::EC2::SecurityGroup',
        fidelity: 'L3',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        operation: 'InvokeFunction',
        fidelity: 'L4',
        plane: 'deploy',
        classification: 'authorization-inventory',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        operation: 'CreateSession',
        fidelity: 'L0',
        plane: 'access',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        operation: 'CreateSecurityGroup',
        fidelity: 'L3',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({ operation: 'FilterLogEvents', fidelity: 'L1' })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        operation: 'PassRole',
        fidelity: 'L2',
        plane: 'workload',
        classification: 'authorization-inventory',
      })
    );
  });

  it('Resources 欠損と YAML 以外の entry を invalid にする', async () => {
    const root = await temporaryDirectory();
    await writeAwsProblem(
      root,
      'no-resources',
      {
        id: 'no-resources',
        category: 'Challenge',
        status: 'draft',
        cfnTemplate: 'template.yaml',
      },
      'Parameters: {}\n'
    );
    await writeText(
      root,
      'challenges/json-template/metadata.json',
      `${JSON.stringify({
        id: 'json-template',
        category: 'Challenge',
        status: 'draft',
        cfnTemplate: 'template.json',
      })}\n`
    );
    await writeText(
      root,
      'challenges/json-template/template.json',
      '{"Resources":{}}\n'
    );
    await writeAwsProblem(
      root,
      'broken-yaml',
      {
        id: 'broken-yaml',
        category: 'Challenge',
        status: 'draft',
        cfnTemplate: 'template.yaml',
      },
      'Resources:\n  Broken: [\n'
    );

    const inventory = await collectCatalog(root);

    expect(
      inventory.diagnostics.filter(
        (diagnostic) => diagnostic.code === 'INVALID_CLOUDFORMATION'
      )
    ).toHaveLength(3);
  });
});

describe('metadata の全 probe と disruption 分岐を走査するとき', () => {
  it('phased polling、attack probe、Lambda、stack update を requirement にする', async () => {
    const root = await temporaryDirectory();
    await writeAwsProblem(root, 'operator-paths', {
      id: 'operator-paths',
      category: 'Battle',
      status: 'ready',
      cfnTemplate: 'template.yaml',
      scoring: {
        kind: 'phased-polling',
        probe: { metaPath: '/meta', scorePath: '/score' },
        attackProbes: [{ path: '/attack' }],
      },
      disruptions: [
        {
          id: 'lambda',
          eventDetailType: 'LambdaFired',
          parameters: { probe: 'redteam/check.sh' },
          action: {
            kind: 'lambda-invoke',
            targetRef: 'FunctionName',
            revert: { afterSeconds: 1 },
          },
        },
        {
          id: 'stack',
          eventDetailType: 'StackFired',
          action: {
            kind: 'cfn-stack-update',
            targetRef: 'StackName',
            revert: { afterSeconds: 1 },
          },
        },
      ],
    });

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toEqual([]);
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({ operation: 'Poll', origin: 'metadata-probe' })
    );
    expect(
      inventory.requirements.filter(
        (requirement) => requirement.operation === 'AttackProbe'
      )
    ).toHaveLength(2);
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        service: 'lambda',
        operation: 'InvokeFunction',
        origin: 'metadata-disruption',
      })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({
        service: 'cloudformation',
        operation: 'UpdateStack',
      })
    );
  });

  it('壊れた endpoint、scoring、probe、disruption を個別 diagnostic にする', async () => {
    const root = await temporaryDirectory();
    await writeAwsProblem(root, 'bad-collections', {
      id: 'bad-collections',
      category: 'Challenge',
      status: 'draft',
      cfnTemplate: 'template.yaml',
      endpoints: 'invalid',
      scoring: 'invalid',
      disruptions: 'invalid',
    });
    await writeAwsProblem(root, 'bad-elements', {
      id: 'bad-elements',
      category: 'Challenge',
      status: 'draft',
      cfnTemplate: 'template.yaml',
      endpoints: [null],
      scoring: { kind: 'unknown-kind' },
    });
    await writeAwsProblem(root, 'bad-probes', {
      id: 'bad-probes',
      category: 'Battle',
      status: 'draft',
      cfnTemplate: 'template.yaml',
      scoring: {
        kind: 'uptime-multi',
        probedSlots: 'invalid',
        attackProbes: 'invalid',
      },
    });
    await writeAwsProblem(root, 'bad-action-shape', {
      id: 'bad-action-shape',
      category: 'Battle',
      status: 'draft',
      cfnTemplate: 'template.yaml',
      disruptions: [
        null,
        { id: 'bad', eventDetailType: 'Bad', action: 'invalid' },
      ],
    });

    const inventory = await collectCatalog(root);
    const codes = inventory.diagnostics.map((diagnostic) => diagnostic.code);

    expect(codes.filter((code) => code === 'INVALID_METADATA')).toHaveLength(6);
    expect(codes).toContain('UNKNOWN_SCORING_KIND');
    expect(codes).toContain('UNKNOWN_DISRUPTION_ACTION');
  });

  it('Composite の共有 field と不正な scoring target を invalid にする', async () => {
    const root = await temporaryDirectory();
    const problemRoot = 'challenges/composite-errors';
    await writeText(
      root,
      `${problemRoot}/metadata.json`,
      `${JSON.stringify({
        id: 'composite-errors',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'aws-target',
              provider: 'aws',
              engine: 'cloudformation',
              entry: 'template.yaml',
            },
            {
              id: 'gcp-target',
              provider: 'gcp',
              engine: 'infra-manager',
              entry: 'gcp',
            },
          ],
        },
        endpoints: [{ slot: 'shared' }],
        scoring: {
          kind: 'composite-probe',
          targets: [{ targetId: 'missing-target' }],
        },
        disruptions: [],
      })}\n`
    );
    await writeText(root, `${problemRoot}/template.yaml`, 'Resources: {}\n');
    await writeText(
      root,
      `${problemRoot}/gcp/main.tf`,
      'resource "google_storage_bucket" "data" {}\n'
    );

    const inventory = await collectCatalog(root);

    expect(
      inventory.diagnostics.filter(
        (diagnostic) => diagnostic.code === 'INVALID_METADATA'
      )
    ).toHaveLength(3);
  });

  it('composite-probe の targets 欠損を invalid にする', async () => {
    const root = await temporaryDirectory();
    const problemRoot = 'challenges/composite-no-probes';
    await writeText(
      root,
      `${problemRoot}/metadata.json`,
      `${JSON.stringify({
        id: 'composite-no-probes',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'aws-target',
              provider: 'aws',
              engine: 'cloudformation',
              entry: 'template.yaml',
            },
            {
              id: 'gcp-target',
              provider: 'gcp',
              engine: 'infra-manager',
              entry: 'gcp',
            },
          ],
        },
        scoring: { kind: 'composite-probe' },
      })}\n`
    );
    await writeText(root, `${problemRoot}/template.yaml`, 'Resources: {}\n');
    await writeText(
      root,
      `${problemRoot}/gcp/main.tf`,
      'resource "google_storage_bucket" "data" {}\n'
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toContainEqual(
      expect.objectContaining({ code: 'INVALID_METADATA' })
    );
  });
});

describe('runtime の境界を正規化するとき', () => {
  it('欠損、型不正、ID drift、entry drift、composite 制約を全て loud にする', async () => {
    const root = await temporaryDirectory();
    await writeText(root, 'challenges/non-object/metadata.json', '[]\n');
    await writeAwsProblem(root, 'missing-id', {
      category: 'Challenge',
      status: 'draft',
      cfnTemplate: 'template.yaml',
    });
    await writeAwsProblem(root, 'directory-name', {
      id: 'different-id',
      category: 'Challenge',
      status: 'draft',
      cfnTemplate: 'template.yaml',
    });
    await writeAwsProblem(root, 'no-runtime', {
      id: 'no-runtime',
      category: 'Challenge',
      status: 'draft',
    });
    await writeAwsProblem(root, 'runtime-scalar', {
      id: 'runtime-scalar',
      category: 'Challenge',
      status: 'draft',
      runtime: 'invalid',
    });
    await writeAwsProblem(root, 'runtime-incomplete', {
      id: 'runtime-incomplete',
      category: 'Challenge',
      status: 'draft',
      runtime: { provider: 'aws' },
    });
    await writeAwsProblem(root, 'entry-drift', {
      id: 'entry-drift',
      category: 'Challenge',
      status: 'draft',
      cfnTemplate: 'template.yaml',
      runtime: {
        provider: 'aws',
        engine: 'cloudformation',
        entry: 'other.yaml',
      },
    });
    await writeText(
      root,
      'challenges/entry-drift/other.yaml',
      'Resources: {}\n'
    );
    await writeAwsProblem(root, 'composite-count', {
      id: 'composite-count',
      category: 'Challenge',
      status: 'draft',
      runtime: { kind: 'composite', targets: [] },
    });
    await writeAwsProblem(root, 'composite-invalid-target', {
      id: 'composite-invalid-target',
      category: 'Challenge',
      status: 'draft',
      runtime: {
        kind: 'composite',
        targets: [
          {
            id: 'same',
            provider: 'aws',
            engine: 'cloudformation',
            entry: 'template.yaml',
          },
          {
            id: 'same',
            provider: 'docker',
            engine: 'compose',
            entry: 'compose.yml',
          },
        ],
      },
    });
    await writeText(
      root,
      'challenges/composite-invalid-target/template.yaml',
      'Resources: {}\n'
    );
    await writeAwsProblem(root, 'composite-missing-entry', {
      id: 'composite-missing-entry',
      category: 'Challenge',
      status: 'draft',
      runtime: {
        kind: 'composite',
        targets: [
          {
            id: 'aws-target',
            provider: 'aws',
            engine: 'cloudformation',
            entry: 'missing.yaml',
          },
          {
            id: 'gcp-target',
            provider: 'gcp',
            engine: 'infra-manager',
            entry: 'gcp',
          },
        ],
      },
    });
    await writeText(
      root,
      'challenges/composite-missing-entry/gcp/main.tf',
      'resource "google_storage_bucket" "data" {}\n'
    );

    const inventory = await collectCatalog(root);
    const codes = inventory.diagnostics.map((diagnostic) => diagnostic.code);

    expect(codes).toContain('INVALID_METADATA');
    expect(codes).toContain('ID_DIRECTORY_MISMATCH');
    expect(codes).toContain('INVALID_RUNTIME');
    expect(codes).toContain('MISSING_ENTRY');
    expect(
      inventory.diagnostics.some((diagnostic) =>
        diagnostic.message.includes('single runtime requires')
      )
    ).toBeTrue();
    expect(
      inventory.diagnostics.some((diagnostic) =>
        diagnostic.message.includes('does not match cfnTemplate')
      )
    ).toBeTrue();
    expect(
      inventory.diagnostics.some((diagnostic) =>
        diagnostic.message.includes('2 to 8 targets')
      )
    ).toBeTrue();
  });
});

describe('Terraform entry を走査するとき', () => {
  it('file entry の service と fidelity を分類し provider drift を検出する', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/gcp-resources/metadata.json',
      `${JSON.stringify({
        id: 'gcp-resources',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          provider: 'gcp',
          engine: 'terraform',
          entry: 'main.tf',
        },
      })}\n`
    );
    await writeText(
      root,
      'challenges/gcp-resources/main.tf',
      `resource "google_compute_network" "main" {}
resource "google_storage_bucket" "data" {}
resource "aws_instance" "wrong" {}
`
    );

    const inventory = await collectCatalog(root);

    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({ service: 'compute', fidelity: 'L3' })
    );
    expect(inventory.requirements).toContainEqual(
      expect.objectContaining({ service: 'storage', fidelity: 'L1' })
    );
    expect(inventory.diagnostics).toContainEqual(
      expect.objectContaining({ code: 'TERRAFORM_PROVIDER_MISMATCH' })
    );
  });

  it('Terraform file が無い entry directory を invalid にする', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/no-terraform/metadata.json',
      `${JSON.stringify({
        id: 'no-terraform',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          provider: 'gcp',
          engine: 'infra-manager',
          entry: 'gcp',
        },
      })}\n`
    );
    await writeText(root, 'challenges/no-terraform/gcp/README.md', 'empty\n');

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toContainEqual(
      expect.objectContaining({ code: 'INVALID_TERRAFORM' })
    );
  });
});

describe('filesystem boundary を走査するとき', () => {
  it('存在しない catalog root と symlink escape を拒否する', async () => {
    const root = await temporaryDirectory();
    await expect(collectCatalog(join(root, 'missing'))).rejects.toThrow(
      /catalog root is not a directory/
    );
    await writeText(root, 'outside.yaml', 'Resources: {}\n');
    await writeText(
      root,
      'challenges/symlink-entry/metadata.json',
      `${JSON.stringify({
        id: 'symlink-entry',
        category: 'Challenge',
        status: 'draft',
        cfnTemplate: 'template.yaml',
      })}\n`
    );
    await symlink(
      join(root, 'outside.yaml'),
      join(root, 'challenges/symlink-entry/template.yaml')
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toContainEqual(
      expect.objectContaining({ code: 'ENTRY_OUTSIDE_PROBLEM' })
    );
  });
});

describe('capability manifest の各不正 shape を読むとき', () => {
  it('root、schema、required field、entry shape、entry field、duplicate を別々に拒否する', () => {
    expect(() => validateCapabilityManifest([])).toThrow(
      /root must be an object/
    );
    expect(() =>
      validateCapabilityManifest({
        schemaVersion: '2',
        version: 'v',
        capabilities: [],
      })
    ).toThrow(/schemaVersion/);
    expect(() =>
      validateCapabilityManifest({ schemaVersion: '1', capabilities: [] })
    ).toThrow(/requires version/);
    expect(() =>
      validateCapabilityManifest({
        schemaVersion: '1',
        version: 'v',
        capabilities: [null],
      })
    ).toThrow(/must be an object/);
    expect(() =>
      validateCapabilityManifest({
        schemaVersion: '1',
        version: 'v',
        capabilities: [
          {
            provider: 'aws',
            service: 'ssm',
            resourceType: '*',
            operation: 'GetParameter',
            fidelity: 'L2',
            extra: true,
          },
        ],
      })
    ).toThrow(/unknown fields/);
    expect(() =>
      validateCapabilityManifest({
        schemaVersion: '1',
        version: 'v',
        capabilities: [{}],
      })
    ).toThrow(/missing fields/);
    const duplicate = {
      provider: 'aws',
      service: 'ssm',
      resourceType: '*',
      operation: 'GetParameter',
      fidelity: 'L2',
    };
    expect(() =>
      validateCapabilityManifest({
        schemaVersion: '1',
        version: 'v',
        capabilities: [duplicate, duplicate],
      })
    ).toThrow(/duplicate identity/);
  });

  it('壊れた manifest JSON を読み取りエラーにする', async () => {
    const root = await temporaryDirectory();
    const path = join(root, 'capabilities.json');
    await writeFile(path, '{broken\n', 'utf8');

    await expect(readCapabilityManifest(path)).rejects.toThrow(
      /JSON is invalid/
    );
  });
});

describe('CLI option の値が欠損するとき', () => {
  it('usage error として終了コード 2 を返す', async () => {
    const result = await runCommand(['--catalog']);
    expect(result.exitCode).toBe(2);
    expect(result.stderr).toContain('--catalog requires a value');
  });
});
