import { describe, expect, it } from 'bun:test';
import { readFile } from 'node:fs/promises';
import { CoreError } from '@tenkacloud/simulator-core';
import {
  BICEP_CONTAINER_APP,
  BICEP_MANAGED_ENVIRONMENT,
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

    expect(resources).toHaveLength(3);
    expect(
      resources.map(({ symbol, type, apiVersion }) => ({
        symbol,
        type,
        apiVersion,
      }))
    ).toEqual([
      {
        symbol: 'helloEnvironment',
        type: BICEP_MANAGED_ENVIRONMENT,
        apiVersion: '2024-03-01',
      },
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
    expect(resources[1]?.body).toContain("image: 'mcr.microsoft.com");
    expect(resources[0]?.line).toBeGreaterThan(1);
    expect(resources[2]?.line).toBeGreaterThan(resources[1]?.line ?? 0);
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

  it('line と block comment 内の ghost output と dependency を compile 対象にしない', async () => {
    const source = (await fixture())
      .replace(
        'resource helloEnvironment',
        `/*
output ghostOutput string = missing.id
*/
// output lineGhost string = missing.id
resource helloEnvironment`
      )
      .replace(
        `dependsOn: [
    helloApp
  ]`,
        `/*
  dependsOn: [missing]
  */
  // dependsOn: [lineMissing]
  dependsOn: [
    helloApp
  ]`
      );

    const outputs = bicepOutputs(source);
    const compiled = compileBicep(source, CONTEXT);
    const role = compiled.resources.find(
      (resource) => resource.type === BICEP_ROLE_ASSIGNMENT
    );
    const app = compiled.resources.find(
      (resource) => resource.type === BICEP_CONTAINER_APP
    );
    if (!role || !app) {
      throw new Error('comment 除外後の compiled resource がありません');
    }

    expect(outputs.map((output) => output.name)).toEqual([
      'containerAppId',
      'containerAppFqdn',
      'roleAssignmentId',
    ]);
    expect(role.dependencies).toEqual([app.resourceId]);
  });

  it('resource ID、dependency ID、output を決定論的に compile する', async () => {
    const source = await fixture();
    const first = compileBicep(source, CONTEXT);
    const repeated = compileBicep(source, CONTEXT);
    const app = first.resources.find(
      (resource) => resource.type === BICEP_CONTAINER_APP
    );
    const environment = first.resources.find(
      (resource) => resource.type === BICEP_MANAGED_ENVIRONMENT
    );
    const role = first.resources.find(
      (resource) => resource.type === BICEP_ROLE_ASSIGNMENT
    );
    if (!environment || !app || !role)
      throw new Error('compiled Azure resources がありません');
    const fqdn = app.properties['fqdn'];
    if (typeof fqdn !== 'string')
      throw new Error('Container App FQDN がありません');

    expect(repeated).toEqual(first);
    expect(environment.name).toMatch(/^tc-[a-f0-9]{13}-env$/);
    expect(app.name).toMatch(/^tc-[a-f0-9]{13}-app$/);
    expect(app.name.slice(3, 16)).toBe(environment.name.slice(3, 16));
    expect(app.dependencies).toEqual([environment.resourceId]);
    expect(app.properties['environmentId']).toBe(environment.resourceId);
    expect(environment.properties['status']).toBe('Ready');
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

  it('adapter parameter 3個とその uniqueString 名だけを組として受理する', async () => {
    const source = await fixture();
    const accepted = compileBicep(source, CONTEXT);
    expect(accepted.resources).toHaveLength(3);

    const invalidSources = [
      source.replace('param tenkacloudTeam string\n', ''),
      source.replace(
        'param tenkacloudTeam string',
        'param tenkacloudTeam object'
      ),
      source.replace(
        'param tenkacloudTeam string',
        "param tenkacloudTeam string = 'team'"
      ),
      source.replace('param tenkacloudTeam string', 'param unsupported string'),
      source.replace('param tenkacloudTeam string', 'param 1bad string'),
      source.replace(
        'tenkacloudNamePrefix, tenkacloudProblemId, tenkacloudTeam',
        'tenkacloudTeam, tenkacloudProblemId, tenkacloudNamePrefix'
      ),
      source.replace('uniqueString(', 'toLower('),
      source.replace("-app'", "-other'"),
    ];

    invalidSources.forEach((invalid) => {
      const error = coreError(() => compileBicep(invalid, CONTEXT));
      expect(error.code).toBe('UnsupportedCapability');
    });
  });

  it('Container App の environmentId を暗黙 dependency にし未知または複雑な式を拒否する', async () => {
    const source = await fixture();
    const missing = coreError(() =>
      compileBicep(
        source.replace(
          'environmentId: helloEnvironment.id',
          'environmentId: missing.id'
        ),
        CONTEXT
      )
    );
    const expression = coreError(() =>
      compileBicep(
        source.replace(
          'environmentId: helloEnvironment.id',
          "environmentId: resourceId('Microsoft.App/managedEnvironments', 'env')"
        ),
        CONTEXT
      )
    );

    expect(missing.message).toContain('unknown Managed Environment missing');
    expect(expression.code).toBe('UnsupportedCapability');
  });

  it('comment 内の environmentId ではなく実際の property 式だけを評価する', async () => {
    const source = await fixture();
    const commentedUnsupported = source.replace(
      'environmentId: helloEnvironment.id',
      `/*
      environmentId: resourceId('Microsoft.App/managedEnvironments', 'ignored')
    */
    // environmentId: ignored.id
    environmentId: helloEnvironment.id`
    );
    const actualUnsupported = source.replace(
      'environmentId: helloEnvironment.id',
      `/*
      environmentId: helloEnvironment.id
    */
    // environmentId: helloEnvironment.id
    environmentId: resourceId('Microsoft.App/managedEnvironments', 'actual')`
    );

    const compiled = compileBicep(commentedUnsupported, CONTEXT);
    const app = compiled.resources.find(
      (resource) => resource.type === BICEP_CONTAINER_APP
    );
    const environment = compiled.resources.find(
      (resource) => resource.type === BICEP_MANAGED_ENVIRONMENT
    );
    const error = coreError(() => compileBicep(actualUnsupported, CONTEXT));

    expect(app?.properties['environmentId']).toBe(environment?.resourceId);
    expect(error.code).toBe('UnsupportedCapability');
    expect(error.message).toContain('actual');
  });

  it('別 symbol が同じ generated resource ID へ解決される場合は決定論的に拒否する', () => {
    const duplicate = containerSource().replace(
      'resource app ',
      'resource peer '
    );

    const error = coreError(() =>
      compileBicep(`${containerSource()}\n${duplicate}`, CONTEXT)
    );

    expect(error.code).toBe('Conflict');
    expect(error.message).toContain('symbols app and peer');
    expect(error.message).toContain('duplicate resource ID');
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
    const emptyName = coreError(() =>
      compileBicep(
        containerSource().replace("name: 'app'", "name: ''"),
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
    expect(emptyName.message).toContain('requires string property name');
    expect(invalidPort.message).toContain('targetPort must be an integer');
    expect(invalidReplicas.message).toBe(
      'minReplicas must not exceed maxReplicas'
    );
  });

  it('未対応の property 式を default 値へ置換せず loud に拒否する', () => {
    const expressions = [
      ["name: 'app'", 'name: appName'],
      ["name: 'app'", `name: 'app-\${suffix}'`],
      ["image: 'image:latest'", `image: '\${containerImage}'`],
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

  it('Container App ingress FQDN の HTTPS output だけを補間する', async () => {
    const compiled = compileBicep(
      containerSource(
        `output AzureHelloUrl string = 'https://\${app.properties.configuration.ingress.fqdn}'`
      ),
      CONTEXT
    );
    const app = compiled.resources[0];
    if (!app) throw new Error('compiled Container App がありません');

    expect(compiled.outputs['AzureHelloUrl']).toBe(
      `https://${app.properties['fqdn']}`
    );

    const invalidOutputs = [
      `output url string = 'http://\${app.properties.configuration.ingress.fqdn}'`,
      `output url string = 'prefix-\${app.properties.configuration.ingress.fqdn}'`,
      `output url string = 'https://\${app.name}'`,
      `output url string = 'https://\${unknown.properties.configuration.ingress.fqdn}'`,
    ];
    invalidOutputs.forEach((output) => {
      expect(() => compileBicep(containerSource(output), CONTEXT)).toThrow(
        CoreError
      );
    });
    const roleOutput = `${await fixture()}
output url string = 'https://\${participantRole.properties.configuration.ingress.fqdn}'`;
    expect(() => compileBicep(roleOutput, CONTEXT)).toThrow(CoreError);
  });
});
