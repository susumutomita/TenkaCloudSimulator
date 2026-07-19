import { afterEach, describe, expect, it } from 'bun:test';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { isPinnedAppRunImage, parseAppRun } from '../src/apprun.ts';
import { parseBicep } from '../src/bicep.ts';
import { compareInventory } from '../src/manifest.ts';
import type { CapabilityManifest, NormalizedTarget } from '../src/model.ts';
import { collectCatalog } from '../src/scanner.ts';

const temporaryDirectories: string[] = [];
const AZURE_TARGET: NormalizedTarget = {
  targetId: 'azure-app',
  provider: 'azure',
  engine: 'bicep',
  entry: 'main.bicep',
  delivery: 'cloud',
};
const SAKURA_TARGET: NormalizedTarget = {
  targetId: 'sakura-app',
  provider: 'sakura',
  engine: 'apprun',
  entry: 'application.json',
  delivery: 'cloud',
};

async function temporaryDirectory(): Promise<string> {
  const directory = await mkdtemp(
    join(tmpdir(), 'tenkacloud-provider-formats-')
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

async function fixture(name: string): Promise<string> {
  return readFile(new URL(`./fixtures/${name}`, import.meta.url), 'utf8');
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories
      .splice(0)
      .map((directory) => rm(directory, { recursive: true, force: true }))
  );
});

describe('Azure Bicep と Sakura AppRun catalog を走査するとき', () => {
  it('scanner 実装は provider runtime package を import しない', async () => {
    const sources = await Promise.all(
      ['bicep.ts', 'apprun.ts'].map((name) =>
        readFile(new URL(`../src/${name}`, import.meta.url), 'utf8')
      )
    );

    expect(sources.join('\n')).not.toContain('@tenkacloud/simulator-provider-');
  });

  it('digest-pinned AppRun image を metadata から binding requirement へ変換する', async () => {
    const root = await temporaryDirectory();
    const image = `ghcr.io/example/hello@sha256:${'a'.repeat(64)}`;
    await writeText(
      root,
      'challenges/sakura-image/metadata.json',
      `${JSON.stringify({
        id: 'sakura-image',
        category: 'Challenge',
        status: 'ready',
        runtime: { provider: 'sakura', engine: 'apprun', entry: image },
      })}\n`
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toEqual([]);
    expect(inventory.requirements).toHaveLength(1);
    expect(
      inventory.requirements.map(({ service, operation, fidelity }) => ({
        service,
        operation,
        fidelity,
      }))
    ).toEqual([
      {
        service: 'apprun',
        operation: 'deploy',
        fidelity: ['L0', 'L1', 'L2'],
      },
    ]);

    const manifest: CapabilityManifest = {
      schemaVersion: '1',
      version: 'provider-format-fixture',
      capabilities: inventory.requirements.map((requirement) => ({
        provider: requirement.provider,
        engine: requirement.engine,
        service: requirement.service,
        resourceType: requirement.resourceType,
        operation: requirement.operation,
        fidelity: requirement.fidelity,
      })),
    };
    const report = compareInventory(inventory, manifest, {
      catalogCommit: 'a'.repeat(40),
      simulatorVersion: manifest.version,
    });
    const deploy = report.requirements.find(
      (entry) => entry.requirement.operation === 'deploy'
    );
    expect(deploy?.requirement.requiredFidelity).toEqual([
      'contract',
      'control',
      'security',
    ]);
    expect(deploy?.implementedFidelity).toEqual([
      'contract',
      'control',
      'security',
    ]);

    await writeText(
      root,
      'challenges/sakura-image/metadata.json',
      `${JSON.stringify({
        id: 'sakura-image',
        category: 'Challenge',
        status: 'ready',
        runtime: { provider: 'sakura', engine: 'apprun', entry: image },
        simulationOverlay: { schemaVersion: '1', entry: 'simulation.json' },
      })}\n`
    );
    await writeText(
      root,
      'challenges/sakura-image/simulation.json',
      `${JSON.stringify({
        schemaVersion: '1',
        requirements: [
          {
            targetId: 'default',
            service: 'http',
            resourceType: 'HTTP::Endpoint',
            operation: 'Request',
            fidelity: 'L4',
            plane: 'workload',
          },
          {
            targetId: 'default',
            service: 'http',
            resourceType: 'HTTP::Endpoint',
            operation: 'Probe',
            fidelity: 'L4',
            plane: 'scoring',
          },
        ],
        workloads: [
          {
            id: 'sakura-image',
            targetId: 'default',
            resourceRef: 'BaseUrl',
            image,
            containerPort: 8080,
            healthPath: '/healthz',
          },
        ],
      })}\n`
    );
    const workloadInventory = await collectCatalog(root);
    expect(workloadInventory.diagnostics).toEqual([]);
    expect(
      new Set(
        workloadInventory.requirements.map(
          (requirement) =>
            `${requirement.service}/${requirement.resourceType}/${requirement.operation}`
        )
      )
    ).toEqual(
      new Set([
        'apprun/sakura.apprun.Application/deploy',
        'http/HTTP::Endpoint/Request',
        'http/HTTP::Endpoint/Probe',
        'runtime/Runtime::Workload/Materialize',
      ])
    );

    await writeText(
      root,
      'challenges/sakura-image/metadata.json',
      `${JSON.stringify({
        id: 'sakura-image',
        category: 'Challenge',
        status: 'ready',
        runtime: {
          provider: 'sakura',
          engine: 'apprun',
          entry: 'ghcr.io/example/hello:latest',
        },
        simulationOverlay: { schemaVersion: '1', entry: 'simulation.json' },
      })}\n`
    );
    const invalid = await collectCatalog(root);
    expect(
      invalid.requirements.map((requirement) => requirement.operation).sort()
    ).toEqual(['Materialize', 'Probe', 'Request']);
    expect(
      invalid.requirements.some(
        (requirement) => requirement.operation === 'deploy'
      )
    ).toBe(false);
    expect(
      invalid.diagnostics.some((diagnostic) =>
        diagnostic.message.includes('digest-pinned image')
      )
    ).toBe(true);
  });

  it('direct AppRun image は lowercase OCI segment の受理集合を runtime と一致させる', () => {
    const digest = 'a'.repeat(64);

    expect(isPinnedAppRunImage(`ghcr.io/example/hello@sha256:${digest}`)).toBe(
      true
    );
    for (const image of [
      `ghcr.io//hello@sha256:${digest}`,
      `ghcr.io/Team/hello@sha256:${digest}`,
      `ghcr.io/example/@sha256:${digest}`,
    ]) {
      expect(isPinnedAppRunImage(image)).toBe(false);
    }
  });

  it('provider compile contract fixture を完全 identity の requirement にする', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/provider-formats/metadata.json',
      `${JSON.stringify({
        id: 'provider-formats',
        category: 'Challenge',
        status: 'ready',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'azure-app',
              provider: 'azure',
              engine: 'bicep',
              entry: 'azure/main.bicep',
            },
            {
              id: 'sakura-app',
              provider: 'sakura',
              engine: 'apprun',
              entry: 'sakura/application.json',
            },
          ],
        },
      })}\n`
    );
    await writeText(
      root,
      'challenges/provider-formats/azure/main.bicep',
      await fixture('azure-container-app.bicep')
    );
    await writeText(
      root,
      'challenges/provider-formats/sakura/application.json',
      await fixture('sakura-application.json')
    );

    const inventory = await collectCatalog(root);

    expect(inventory.diagnostics).toEqual([]);
    expect(inventory.problems[0]?.targets).toEqual([
      {
        targetId: 'azure-app',
        provider: 'azure',
        engine: 'bicep',
        entry: 'azure/main.bicep',
        delivery: 'cloud',
      },
      {
        targetId: 'sakura-app',
        provider: 'sakura',
        engine: 'apprun',
        entry: 'sakura/application.json',
        delivery: 'cloud',
      },
    ]);
    expect(
      inventory.requirements.map(
        ({
          targetId,
          provider,
          engine,
          service,
          resourceType,
          operation,
          fidelity,
          plane,
        }) => ({
          targetId,
          provider,
          engine,
          service,
          resourceType,
          operation,
          fidelity,
          plane,
        })
      )
    ).toEqual([
      {
        targetId: 'azure-app',
        provider: 'azure',
        engine: 'bicep',
        service: 'authorization',
        resourceType: 'Microsoft.Authorization/roleAssignments',
        operation: 'lifecycle',
        fidelity: ['L0', 'L1', 'L2'],
        plane: 'deploy',
      },
      {
        targetId: 'azure-app',
        provider: 'azure',
        engine: 'bicep',
        service: 'containerapps',
        resourceType: 'Microsoft.App/containerApps',
        operation: 'lifecycle',
        fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
        plane: 'deploy',
      },
      {
        targetId: 'azure-app',
        provider: 'azure',
        engine: 'bicep',
        service: 'containerapps',
        resourceType: 'Microsoft.App/managedEnvironments',
        operation: 'lifecycle',
        fidelity: ['L0', 'L1', 'L2'],
        plane: 'deploy',
      },
      {
        targetId: 'azure-app',
        provider: 'azure',
        engine: 'bicep',
        service: 'http',
        resourceType: 'HTTP::Endpoint',
        operation: 'Probe',
        fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
        plane: 'scoring',
      },
      {
        targetId: 'azure-app',
        provider: 'azure',
        engine: 'bicep',
        service: 'http',
        resourceType: 'HTTP::Endpoint',
        operation: 'Request',
        fidelity: ['L0', 'L1', 'L2', 'L3', 'L4'],
        plane: 'workload',
      },
      {
        targetId: 'sakura-app',
        provider: 'sakura',
        engine: 'apprun',
        service: 'apprun',
        resourceType: 'sakura.apprun.Application',
        operation: 'deploy',
        fidelity: ['L0', 'L1', 'L2'],
        plane: 'deploy',
      },
    ]);
  });

  it('unsupported Bicep と壊れた AppRun descriptor を invalid にする', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/invalid-provider-formats/metadata.json',
      `${JSON.stringify({
        id: 'invalid-provider-formats',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          kind: 'composite',
          targets: [
            {
              id: 'azure-unknown',
              provider: 'azure',
              engine: 'bicep',
              entry: 'azure/main.bicep',
            },
            {
              id: 'sakura-invalid',
              provider: 'sakura',
              engine: 'apprun',
              entry: 'sakura/application.json',
            },
          ],
        },
      })}\n`
    );
    await writeText(
      root,
      'challenges/invalid-provider-formats/azure/main.bicep',
      `resource storage 'Microsoft.Storage/storageAccounts@2023-05-01' = {\n  name: 'unsupported'\n}\n`
    );
    await writeText(
      root,
      'challenges/invalid-provider-formats/sakura/application.json',
      '{"name":"broken","components":[]}'
    );

    const inventory = await collectCatalog(root);

    expect(inventory.requirements).toEqual([]);
    expect(
      inventory.diagnostics.map(({ code, targetId, message }) => ({
        code,
        targetId,
        message,
      }))
    ).toEqual([
      {
        code: 'INVALID_BICEP',
        targetId: 'azure-unknown',
        message: expect.stringContaining(
          'Microsoft.Storage/storageAccounts is not supported'
        ),
      },
      {
        code: 'INVALID_APPRUN',
        targetId: 'sakura-invalid',
        message: expect.stringContaining(
          'components must be a non-empty array'
        ),
      },
    ]);
  });

  it('Azure adapter parameter の未知な型と name 式を invalid にする', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/invalid-azure-expression/metadata.json',
      `${JSON.stringify({
        id: 'invalid-azure-expression',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          kind: 'cloud',
          provider: 'azure',
          engine: 'bicep',
          entry: 'azure/main.bicep',
        },
      })}\n`
    );
    await writeText(
      root,
      'challenges/invalid-azure-expression/azure/main.bicep',
      (await fixture('azure-container-app.bicep')).replace(
        'param tenkacloudTeam string',
        'param tenkacloudTeam object'
      )
    );

    const inventory = await collectCatalog(root);

    expect(inventory.requirements).toEqual([]);
    expect(inventory.diagnostics).toEqual([
      expect.objectContaining({
        code: 'INVALID_BICEP',
        problemId: 'invalid-azure-expression',
        targetId: 'default',
        message: expect.stringContaining('parameter'),
      }),
    ]);
  });

  it('AppRun component の provider compile contract 違反を invalid にする', async () => {
    const root = await temporaryDirectory();
    await writeText(
      root,
      'challenges/invalid-apprun-component/metadata.json',
      `${JSON.stringify({
        id: 'invalid-apprun-component',
        category: 'Challenge',
        status: 'draft',
        runtime: {
          kind: 'cloud',
          provider: 'sakura',
          engine: 'apprun',
          entry: 'application.json',
        },
      })}\n`
    );
    await writeText(
      root,
      'challenges/invalid-apprun-component/application.json',
      (await fixture('sakura-application.json')).replace('"0.5"', '"4"')
    );

    const inventory = await collectCatalog(root);

    expect(inventory.requirements).toEqual([]);
    expect(inventory.diagnostics).toEqual([
      expect.objectContaining({
        code: 'INVALID_APPRUN',
        problemId: 'invalid-apprun-component',
        targetId: 'default',
        message: expect.stringContaining('CPU or memory'),
      }),
    ]);
  });

  it('Bicep lexical boundary と unsupported subset を個別に fail loud にする', () => {
    const environment = (body: string, quote = "'") =>
      `resource environment ${quote}Microsoft.App/managedEnvironments@2024-03-01${quote} = {\n${body}\n}\n`;
    const validEnvironment = environment(
      `  name: 'environment'\n  properties: {}`
    );
    const validWithComments = `// leading comment\n/* block comment */\n${environment(
      `  name: "environment"\n  properties: {\n    note: '}' /* nested block */\n  }`,
      '"'
    )}`;
    expect(
      parseBicep(validWithComments, 'main.bicep', AZURE_TARGET, 'fixture')
        .diagnostics
    ).toEqual([]);

    const container = (body: string) =>
      `resource app 'Microsoft.App/containerApps@2024-03-01' = {\n  name: 'app'\n${body}\n}\n`;
    const containerProperties = `  properties: {
    template: {
      containers: [
        {
          image: 'image'
        }
      ]
    }
  }`;
    expect(
      parseBicep(
        `${container(containerProperties)}output id string = app.id\noutput name string = app.name\noutput fqdn string = app.properties.configuration.ingress.fqdn`,
        'main.bicep',
        AZURE_TARGET,
        'fixture'
      ).diagnostics
    ).toEqual([]);
    const invalidSources = [
      `param ?\n${validEnvironment}`,
      `param tenkacloudNamePrefix string\n${validEnvironment}`,
      `module nested 'module.bicep' = {}\n${validEnvironment}`,
      '',
      'resource broken = {}',
      `${validEnvironment}\nresource broken = {}`,
      `${validEnvironment}${validEnvironment}`,
      `${validEnvironment}output unsupported object = environment.id`,
      `${validEnvironment}output unknown string = missing.id`,
      `${validEnvironment}output broken`,
      `${validEnvironment}output invalid string = environment.properties.configuration.ingress.fqdn`,
      `${validEnvironment}output invalid string = '$${'{unknown.value}'}'`,
      environment(
        "  name: 'environment'\n  dependsOn: [\n    unknown\n  ]\n  properties: {}"
      ),
      "resource environment 'Microsoft.App/managedEnvironments' = { name: 'environment' }",
      environment('  properties: {}'),
      environment('  name: environment\n  properties: {}'),
      "resource environment 'Microsoft.App/managedEnvironments@2024-03-01' = {\n  name: 'environment'",
      environment(`  name: '$${'{unknown()}'}'\n  properties: {}`),
      container(`${containerProperties}\n  targetPort: dynamicPort`),
      container(`${containerProperties}\n  targetPort: 0`),
      container(`${containerProperties}\n  minReplicas: 2\n  maxReplicas: 1`),
      container(`${containerProperties}\n  external: enabled`),
      container(`  environmentId: unknown.id\n${containerProperties}`),
      `${container(containerProperties)}resource role 'Microsoft.Authorization/roleAssignments@2022-04-01' = {\n  name: 'role'\n  scope: unknown\n  properties: {\n    roleDefinitionId: 'reader'\n    principalId: 'participant'\n  }\n}`,
    ];

    for (const source of invalidSources) {
      const parsed = parseBicep(source, 'main.bicep', AZURE_TARGET, 'fixture');
      expect(parsed.requirements).toEqual([]);
      expect(parsed.diagnostics).toEqual([
        expect.objectContaining({ code: 'INVALID_BICEP' }),
      ]);
    }
  });

  it('AppRun scalar nested array と probe header の境界を個別に fail loud にする', async () => {
    const valid = JSON.parse(
      await fixture('sakura-application.json')
    ) as Record<string, unknown>;
    const component = (
      valid['components'] as Array<Record<string, unknown>>
    )[0] as Record<string, unknown>;
    const deploySource = component['deploy_source'] as Record<string, unknown>;
    const registry = deploySource['container_registry'] as Record<
      string,
      unknown
    >;
    registry['server'] = 'registry.example';
    registry['username'] = 'simulator';
    registry['password'] = 'redacted';
    component['env'] = [{ key: 'MODE', value: 'test' }];
    component['secret'] = [{ key: 'TOKEN', value: 'test-only' }];
    const probe = component['probe'] as Record<string, unknown>;
    const httpGet = probe['http_get'] as Record<string, unknown>;
    httpGet['headers'] = [{ name: 'x-probe', value: 'ready' }];
    expect(
      parseAppRun(
        JSON.stringify(valid),
        'application.json',
        SAKURA_TARGET,
        'fixture'
      ).diagnostics
    ).toEqual([]);
    expect(
      parseAppRun('{', 'application.json', SAKURA_TARGET, 'fixture').diagnostics
    ).toEqual([expect.objectContaining({ code: 'INVALID_APPRUN' })]);

    const invalidValues = [
      null,
      { ...valid, name: '' },
      { ...valid, timeout_seconds: 1.5 },
      { ...valid, port: 8008 },
      {
        ...valid,
        components: [{ ...component, env: {} }],
      },
      {
        ...valid,
        components: [{ ...component, env: [null] }],
      },
      {
        ...valid,
        components: [
          {
            ...component,
            probe: { http_get: { ...httpGet, headers: {} } },
          },
        ],
      },
    ];
    for (const value of invalidValues) {
      const parsed = parseAppRun(
        JSON.stringify(value),
        'application.json',
        SAKURA_TARGET,
        'fixture'
      );
      expect(parsed.requirements).toEqual([]);
      expect(parsed.diagnostics).toEqual([
        expect.objectContaining({ code: 'INVALID_APPRUN' }),
      ]);
    }
  });

  it('entry の拡張子とファイル種別が provider format に一致しなければ invalid にする', async () => {
    const root = await temporaryDirectory();
    const cases = [
      {
        id: 'azure-directory',
        provider: 'azure',
        engine: 'bicep',
        entry: 'main.bicep',
        nestedFile: 'main.bicep/source.txt',
        code: 'INVALID_BICEP',
        message: 'Bicep entry must be a .bicep file',
      },
      {
        id: 'azure-extension',
        provider: 'azure',
        engine: 'bicep',
        entry: 'main.txt',
        nestedFile: 'main.txt',
        code: 'INVALID_BICEP',
        message: 'Bicep entry must be a .bicep file',
      },
      {
        id: 'sakura-directory',
        provider: 'sakura',
        engine: 'apprun',
        entry: 'application.json',
        nestedFile: 'application.json/source.txt',
        code: 'INVALID_APPRUN',
        message: 'AppRun entry must be a JSON file',
      },
      {
        id: 'sakura-extension',
        provider: 'sakura',
        engine: 'apprun',
        entry: 'application.yaml',
        nestedFile: 'application.yaml',
        code: 'INVALID_APPRUN',
        message: 'AppRun entry must be a JSON file',
      },
    ] as const;

    for (const testCase of cases) {
      await writeText(
        root,
        `challenges/${testCase.id}/metadata.json`,
        `${JSON.stringify({
          id: testCase.id,
          category: 'Challenge',
          status: 'draft',
          runtime: {
            kind: 'cloud',
            provider: testCase.provider,
            engine: testCase.engine,
            entry: testCase.entry,
          },
        })}\n`
      );
      await writeText(
        root,
        `challenges/${testCase.id}/${testCase.nestedFile}`,
        'invalid entry shape\n'
      );
    }

    const inventory = await collectCatalog(root);

    expect(inventory.requirements).toEqual([]);
    expect(
      inventory.diagnostics.map(({ code, problemId, message }) => ({
        code,
        problemId,
        message,
      }))
    ).toEqual(
      cases.map(({ id, code, message }) => ({
        code,
        problemId: id,
        message,
      }))
    );
  });
});
