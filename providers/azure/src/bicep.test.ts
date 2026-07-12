import { describe, expect, it } from 'bun:test';
import { readFile } from 'node:fs/promises';
import { CoreError } from '@tenkacloud/simulator-core';
import {
  BICEP_CONTAINER_APP,
  BICEP_ROLE_ASSIGNMENT,
  bicepOutputs,
  bicepResources,
  compileBicep,
} from './bicep';

const CONTEXT = { problemId: 'azure-conformance', targetId: 'azure' };

async function fixture(): Promise<string> {
  return readFile(
    new URL('./fixtures/container-app.bicep', import.meta.url),
    'utf8'
  );
}

function coreError(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('CoreError が発生しませんでした');
}

function containerSource(outputs = ''): string {
  return `resource app 'Microsoft.App/containerApps@2024-03-01' = {
  name: 'app'
  properties: {
    configuration: {
      ingress: {
        targetPort: 8080
      }
    }
    template: {
      containers: [
        {
          name: 'web'
          image: 'image:latest'
        }
      ]
      scale: {
        minReplicas: 0
        maxReplicas: 1
      }
    }
  }
}
${outputs}`;
}

describe('targeted Bicep compiler の振る舞い', () => {
  it('resource 宣言の symbol、type、API version、body、source line を抽出する', async () => {
    const resources = bicepResources(await fixture());

    expect(resources).toHaveLength(2);
    expect(
      resources.map(({ symbol, type, apiVersion }) => ({
        symbol,
        type,
        apiVersion,
      }))
    ).toEqual([
      {
        symbol: 'helloApp',
        type: BICEP_CONTAINER_APP,
        apiVersion: '2024-03-01',
      },
      {
        symbol: 'participantRole',
        type: BICEP_ROLE_ASSIGNMENT,
        apiVersion: '2022-04-01',
      },
    ]);
    expect(resources[0]?.body).toContain("image: 'mcr.microsoft.com");
    expect(resources[0]?.line).toBe(1);
    expect(resources[1]?.line).toBeGreaterThan(1);
  });

  it('output 宣言を型、式、source line とともに抽出する', async () => {
    const outputs = bicepOutputs(await fixture());

    expect(
      outputs.map(({ name, type, expression }) => ({
        name,
        type,
        expression,
      }))
    ).toEqual([
      {
        name: 'containerAppId',
        type: 'string',
        expression: 'helloApp.id',
      },
      {
        name: 'containerAppFqdn',
        type: 'string',
        expression: 'helloApp.properties.configuration.ingress.fqdn',
      },
      {
        name: 'roleAssignmentId',
        type: 'string',
        expression: 'participantRole.id',
      },
    ]);
    expect(outputs.every((output) => output.line > 1)).toBe(true);
  });

  it('resource ID、dependency ID、output を決定論的に compile する', async () => {
    const source = await fixture();
    const first = compileBicep(source, CONTEXT);
    const repeated = compileBicep(source, CONTEXT);
    const app = first.resources.find(
      (resource) => resource.type === BICEP_CONTAINER_APP
    );
    const role = first.resources.find(
      (resource) => resource.type === BICEP_ROLE_ASSIGNMENT
    );
    if (!app || !role) throw new Error('compiled Azure resources がありません');
    const fqdn = app.properties['fqdn'];
    if (typeof fqdn !== 'string')
      throw new Error('Container App FQDN がありません');

    expect(repeated).toEqual(first);
    expect(app.resourceId).toContain(
      '/providers/Microsoft.App/containerApps/hello-container-app'
    );
    expect(role.resourceId).toBe(
      `${app.resourceId}/providers/Microsoft.Authorization/roleAssignments/participant-container-app-reader`
    );
    expect(role.dependencies).toEqual([app.resourceId]);
    expect(role.properties['scopeId']).toBe(app.resourceId);
    expect(first.outputs).toEqual({
      containerAppId: app.resourceId,
      containerAppFqdn: fqdn,
      roleAssignmentId: role.resourceId,
    });
  });

  it('quote 内の brace と line/block comment を block 終端と誤認しない', () => {
    const source = `// resource comment
resource app 'Microsoft.App/containerApps@2024-03-01' = {
  name: 'brace-} //-app'
  /* { ignored block } */
  properties: {
    configuration: { ingress: { targetPort: 8080 } }
    template: { containers: [{ image: 'image:latest' }] }
  }
}`;

    const resources = bicepResources(source);

    expect(resources).toHaveLength(1);
    expect(resources[0]?.body).toContain("name: 'brace-} //-app'");

    const compiled = compileBicep(
      containerSource().replace(
        "name: 'app'",
        `/* { top-level block comment } */
  name: "app"
  // } top-level line comment`
      ),
      CONTEXT
    );
    expect(compiled.resources[0]?.name).toBe('app');
  });

  it('resource block 不在、未閉鎖、API version 不在、symbol 重複を拒否する', () => {
    const cases: Array<[string, CoreError['code'], string]> = [
      ['param name string', 'ValidationFailed', 'no resource declarations'],
      [
        "resource app 'Microsoft.App/containerApps@2024-03-01' = {",
        'ValidationFailed',
        'not closed',
      ],
      [
        "resource app 'Microsoft.App/containerApps' = { name: 'app' }",
        'ValidationFailed',
        'must include an API version',
      ],
      [
        `${containerSource()}\n${containerSource().replace('app ', 'app ')}`,
        'Conflict',
        'symbol app is duplicated',
      ],
    ];

    for (const [source, code, message] of cases) {
      const error = coreError(() => bicepResources(source));
      expect(error.code).toBe(code);
      expect(error.message).toContain(message);
    }
  });

  it('module、condition、loop など対象外の top-level 構文を黙って無視しない', () => {
    const moduleError = coreError(() =>
      compileBicep(
        `${containerSource()}
module child './child.bicep' = { name: 'child' }`,
        CONTEXT
      )
    );
    const conditionError = coreError(() =>
      bicepResources(
        `${containerSource()}
resource conditional 'Microsoft.App/containerApps@2024-03-01' = if (true) { name: 'conditional' }`
      )
    );
    const loopError = coreError(() =>
      bicepResources(
        "resource looped 'Microsoft.App/containerApps@2024-03-01' = [for name in names: { name: name }]"
      )
    );

    expect(moduleError.message).toContain('module declarations');
    expect(conditionError.message).toContain('declaration syntax');
    expect(loopError.message).toContain('declaration syntax');
  });

  it('未知 resource、scope 不在、依存先不在を loud に拒否する', () => {
    const unknown = coreError(() =>
      compileBicep(
        "resource item 'Microsoft.Storage/storageAccounts@2024-01-01' = { name: 'storage' }",
        CONTEXT
      )
    );
    const missingScope = coreError(() =>
      compileBicep(
        `resource role 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: 'role'
  properties: { roleDefinitionId: 'reader', principalId: 'participant' }
}`,
        CONTEXT
      )
    );
    const missingDependency = coreError(() =>
      compileBicep(
        containerSource().replace(
          "name: 'app'",
          "name: 'app'\n  dependsOn: [missing]"
        ),
        CONTEXT
      )
    );
    const unknownScope = coreError(() =>
      compileBicep(
        `resource role 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: 'role'
  scope: missing
  properties: { roleDefinitionId: 'reader', principalId: 'participant' }
}`,
        CONTEXT
      )
    );

    expect(unknown.code).toBe('UnsupportedCapability');
    expect(missingScope.message).toContain('requires a scope reference');
    expect(missingDependency.message).toContain('unknown symbol missing');
    expect(unknownScope.message).toContain('unknown Container App missing');
  });

  it('必須 property、integer range、replica 順序を検証する', () => {
    const missingName = coreError(() =>
      compileBicep(containerSource().replace("name: 'app'", ''), CONTEXT)
    );
    const missingImage = coreError(() =>
      compileBicep(
        containerSource().replace("image: 'image:latest'", ''),
        CONTEXT
      )
    );
    const invalidPort = coreError(() =>
      compileBicep(
        containerSource().replace('targetPort: 8080', 'targetPort: 0'),
        CONTEXT
      )
    );
    const invalidReplicas = coreError(() =>
      compileBicep(
        containerSource()
          .replace('minReplicas: 0', 'minReplicas: 3')
          .replace('maxReplicas: 1', 'maxReplicas: 2'),
        CONTEXT
      )
    );

    expect(missingName.message).toContain('requires string property name');
    expect(missingImage.message).toContain('requires string property image');
    expect(invalidPort.message).toContain('targetPort must be an integer');
    expect(invalidReplicas.message).toBe(
      'minReplicas must not exceed maxReplicas'
    );
  });

  it('未対応の property 式を default 値へ置換せず loud に拒否する', () => {
    const expressions = [
      ["name: 'app'", 'name: appName'],
      ["name: 'app'", `name: 'app-\${suffix}'`],
      ['targetPort: 8080', 'targetPort: portValue'],
      ['targetPort: 8080', 'targetPort: 8080 + 1'],
      ['targetPort: 8080', 'external: publicIngress\n        targetPort: 8080'],
    ] as const;

    for (const [literal, expression] of expressions) {
      const error = coreError(() =>
        compileBicep(containerSource().replace(literal, expression), CONTEXT)
      );
      expect(error.code).toBe('UnsupportedCapability');
      expect(error.message).toContain('expression');
    }

    const scopeExpression = coreError(() =>
      compileBicep(
        `${containerSource()}
resource role 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: 'role'
  scope: resourceId('Microsoft.App/containerApps', 'app')
  properties: {
    roleDefinitionId: 'reader'
    principalId: 'participant'
  }
}`,
        CONTEXT
      )
    );
    expect(scopeExpression.code).toBe('UnsupportedCapability');
  });

  it('literal output を許可し、未知 type、resource、expression、name 重複を拒否する', () => {
    const literal = compileBicep(
      containerSource(
        "output greeting string = 'hello // simulator' // comment"
      ),
      CONTEXT
    );
    expect(literal.outputs['greeting']).toBe('hello // simulator');

    const invalidOutputs = [
      'output count int = 1',
      'output missing string = unknown.id',
      'output unsupported string = app.properties.unknown',
      `output interpolated string = 'hello \${app.name}'`,
      "output duplicate string = 'one'\noutput duplicate string = 'two'",
    ];
    invalidOutputs.forEach((output) => {
      expect(() => compileBicep(containerSource(output), CONTEXT)).toThrow(
        CoreError
      );
    });
  });
});
