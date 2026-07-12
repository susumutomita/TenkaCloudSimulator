import { afterEach, describe, expect, it } from 'bun:test';
import {
  CoreError,
  type ProviderCommandInput,
  ProviderRegistry,
  SimulationCore,
} from '@tenkacloud/simulator-core';
import { AwsProvider } from '../src/provider';
import {
  cleanupContexts,
  createContext,
  execute,
  resourceByLogicalId,
} from './support';

afterEach(cleanupContexts);

function arrayField(
  value: Readonly<Record<string, unknown>>,
  key: string
): readonly unknown[] {
  const field = value[key];
  if (!Array.isArray(field)) throw new Error(`${key} is not an array`);
  return field;
}

function objectField(
  value: Readonly<Record<string, unknown>>,
  key: string
): Readonly<Record<string, unknown>> {
  const field = value[key];
  if (field === null || typeof field !== 'object' || Array.isArray(field)) {
    throw new Error(`${key} is not an object`);
  }
  return field as Readonly<Record<string, unknown>>;
}

function externalWorker(request: Request): Response {
  const url = new URL(request.url);
  const authorization = request.headers.get('authorization');
  if (url.pathname === '/healthz') {
    return Response.json({ status: 'ok' });
  }
  if (url.pathname === '/api/profile') {
    if (authorization !== 'Bearer token-alice') {
      return Response.json({ error: 'unauthorized' }, { status: 401 });
    }
    return Response.json({ id: 'alice' });
  }
  if (url.pathname === '/api/profile/alice') {
    return Response.json({ id: 'alice' });
  }
  if (
    url.pathname === '/api/profile/bob' ||
    url.pathname === '/api/profile/charlie'
  ) {
    return Response.json({ error: 'forbidden' }, { status: 403 });
  }
  return Response.json({ error: 'invalid id' }, { status: 400 });
}

describe('AWS participant and operator reducers', () => {
  it('IAM role と inline policy を読み書きして削除する', async () => {
    const context = await createContext();
    expect(
      arrayField(execute(context, 'iam', 'ListRoles', {}), 'Roles')
    ).toHaveLength(1);
    expect(
      objectField(
        execute(context, 'iam', 'GetRole', { RoleName: 'tc-fixture-role' }),
        'Role'
      )['RoleName']
    ).toBe('tc-fixture-role');
    expect(
      arrayField(
        execute(context, 'iam', 'ListRolePolicies', {
          RoleName: 'tc-fixture-role',
        }),
        'PolicyNames'
      )
    ).toEqual(['ReadParameters']);
    expect(
      execute(context, 'iam', 'GetRolePolicy', {
        RoleName: 'tc-fixture-role',
        PolicyName: 'ReadParameters',
      })['PolicyName']
    ).toBe('ReadParameters');
    execute(context, 'iam', 'PutRolePolicy', {
      RoleName: 'tc-fixture-role',
      PolicyName: 'RuntimePolicy',
      PolicyDocument: { Version: '2012-10-17', Statement: [] },
    });
    expect(
      arrayField(
        execute(context, 'iam', 'ListRolePolicies', {
          RoleName: 'tc-fixture-role',
        }),
        'PolicyNames'
      )
    ).toEqual(['ReadParameters', 'RuntimePolicy']);
    execute(context, 'iam', 'DeleteRolePolicy', {
      RoleName: 'tc-fixture-role',
      PolicyName: 'RuntimePolicy',
    });
    expect(() =>
      execute(context, 'iam', 'DeleteRolePolicy', {
        RoleName: 'tc-fixture-role',
        PolicyName: 'RuntimePolicy',
      })
    ).toThrow('inline role policy does not exist');
    expect(() =>
      execute(context, 'iam', 'GetRole', { RoleName: 'missing' })
    ).toThrow('IAM role does not exist');
  });

  it('SSM parameter の単体 複数 path describe put を永続化する', async () => {
    const context = await createContext();
    const path = '/tc-fixture/config';
    expect(
      objectField(
        execute(context, 'ssm', 'GetParameter', {
          Name: `${path}/currency_mode`,
        }),
        'Parameter'
      )['Value']
    ).toBe('test');
    const multiple = execute(context, 'ssm', 'GetParameters', {
      Names: [`${path}/currency_mode`, '/missing'],
    });
    expect(arrayField(multiple, 'Parameters')).toHaveLength(1);
    expect(arrayField(multiple, 'InvalidParameters')).toEqual(['/missing']);
    expect(
      arrayField(
        execute(context, 'ssm', 'GetParametersByPath', {
          Path: `${path}/`,
          Recursive: true,
        }),
        'Parameters'
      )
    ).toHaveLength(4);
    expect(
      arrayField(
        execute(context, 'ssm', 'DescribeParameters', {}),
        'Parameters'
      ).length
    ).toBeGreaterThanOrEqual(4);
    expect(() =>
      execute(context, 'ssm', 'PutParameter', {
        Name: `${path}/currency_mode`,
        Value: 'real',
      })
    ).toThrow('already exists');
    expect(
      execute(context, 'ssm', 'PutParameter', {
        Name: `${path}/currency_mode`,
        Value: 'test-updated',
        Overwrite: true,
      })['Version']
    ).toBe(2);
    execute(context, 'ssm', 'PutParameter', {
      Name: `${path}/nested/new`,
      Value: 'new-value',
      Type: 'String',
    });
    expect(
      arrayField(
        execute(context, 'ssm', 'GetParametersByPath', {
          Path: path,
          Recursive: false,
        }),
        'Parameters'
      )
    ).toHaveLength(4);
    expect(() =>
      execute(context, 'ssm', 'GetParameter', { Name: '/missing' })
    ).toThrow('SSM parameter does not exist');
  });

  it('SSM SendCommand がcatalog disruptionをEC2 stateへ適用する', async () => {
    const context = await createContext();
    const instanceId = String(
      resourceByLogicalId(context, 'Instance').properties['refValue']
    );
    const response = execute(
      context,
      'ssm',
      'SendCommand',
      {
        InstanceIds: [instanceId],
        DocumentName: 'AWS-RunShellScript',
        Parameters: {
          commands: [
            'systemctl stop nginx || true',
            'systemctl start nginx || true',
            'command -v tc >/dev/null 2>&1 || dnf install -y iproute-tc',
            'DEV=$(ip route show default | awk x); tc qdisc add dev "$DEV" root netem delay 200ms || true',
            'tc qdisc del dev "$DEV" root || true',
            '/opt/tenkacloud/vibe/wipe_database.sh || true',
            'sqlite3 $SQLITE_DB < /tmp/seed-sqlite.sql || true',
            'cp config.json config.json.redteam-auth.bak || true',
            "jq --arg t smoke-token-42 '.auth_required=true|.auth_token=$t' config.json > tmp",
            "jq '.auth_required=false' config.json > tmp",
            'mv config.json.redteam-auth.bak config.json || true',
            'docker compose restart api',
            'systemctl stop tenkacloud-vibe || true',
            'systemctl start tenkacloud-vibe || true',
            '/opt/tenkacloud/vibe/deface_site.sh || true',
            '/opt/tenkacloud/vibe/restore_site.sh || true',
            '/opt/tenkacloud/vibe/install_backdoor.sh || true',
            '/opt/tenkacloud/vibe/remove_backdoor.sh || true',
            'END=$(( $(date +%s) + 30 ))',
            "pkill -f 'curl -s -o /dev/null' || true",
            'set -a; . runtime.env; set +a',
            'true',
          ],
        },
      },
      'AWS::EC2::Instance'
    );
    expect(objectField(response, 'Command')['Status']).toBe('Success');
    const state = objectField(
      resourceByLogicalId(context, 'Instance').properties,
      'state'
    );
    expect(state).toMatchObject({
      networkDelayMs: 0,
      databaseWiped: false,
      authRequired: false,
      siteDefaced: false,
      backdoorInstalled: false,
      loadActive: false,
    });
    expect(objectField(state, 'services')).toMatchObject({
      nginx: 'running',
      'tenkacloud-vibe': 'running',
    });
    expect(
      context.store
        .resources(context.worldId)
        .some(
          (resource) => resource.resourceType === 'AWS::SSM::CommandInvocation'
        )
    ).toBe(true);
  });

  it('declarative SSM commandを純粋 reducer で適用しclock到達時に一度だけrevertする', async () => {
    const context = await createContext();
    const instance = resourceByLogicalId(context, 'Instance');
    const instanceId = String(instance.properties['refValue']);
    const response = execute(
      context,
      'ssm',
      'SendCommand',
      {
        targetRef: 'InstanceId',
        targetResource: instanceId,
        documentName: 'AWS-RunShellScript',
        parameters: { commands: ['systemctl stop nginx || true'] },
        revert: {
          afterSeconds: 600,
          paramTemplate: { commands: ['systemctl start nginx || true'] },
        },
      },
      'AWS::SSM::Command'
    );
    expect(response).toEqual({
      commandId: expect.any(String),
      status: 'Success',
      scheduledRevertAt: '2026-07-12T00:10:00.000Z',
    });
    expect(String(response['commandId'])).toStartWith('command_');
    const commandResource = context.store
      .resources(context.worldId)
      .find((resource) => resource.resourceType === 'AWS::SSM::Command');
    expect(commandResource?.properties).toMatchObject({
      targetRef: 'InstanceId',
      targetResource: instanceId,
      documentName: 'AWS-RunShellScript',
      parameters: { commands: ['systemctl stop nginx || true'] },
      status: 'SUCCESS',
      state: {
        status: 'Success',
        requestedAt: '2026-07-12T00:00:00.000Z',
        transition: {
          status: 'Scheduled',
          scheduledAt: '2026-07-12T00:10:00.000Z',
          documentName: 'AWS-RunShellScript',
          parameters: { commands: ['systemctl start nginx || true'] },
        },
      },
    });
    expect(
      objectField(
        objectField(
          resourceByLogicalId(context, 'Instance').properties,
          'state'
        ),
        'services'
      )['nginx']
    ).toBe('stopped');
    expect(
      context.core
        .events(context.worldId)
        .some((event) => event.type === 'AwsSsmCommandScheduled')
    ).toBe(true);

    expect(context.core.advanceClock(context.worldId, 599_000)).toMatchObject({
      virtualTime: '2026-07-12T00:09:59.000Z',
      appliedTransitions: [],
    });
    const transitionId = String(
      objectField(commandResource?.properties ?? {}, 'attributes')[
        'TransitionId'
      ]
    );
    expect(context.core.advanceClock(context.worldId, 1_000)).toMatchObject({
      virtualTime: '2026-07-12T00:10:00.000Z',
      appliedTransitions: [{ provider: 'aws', transitionId }],
    });
    expect(
      objectField(
        objectField(
          resourceByLogicalId(context, 'Instance').properties,
          'state'
        ),
        'services'
      )['nginx']
    ).toBe('running');
    const reverted = context.store
      .resources(context.worldId)
      .find((resource) => resource.resourceId === response['commandId']);
    expect(reverted?.properties).toMatchObject({
      status: 'REVERTED',
      state: {
        status: 'Reverted',
        transition: {
          transitionId,
          status: 'Applied',
          appliedAt: '2026-07-12T00:10:00.000Z',
        },
      },
    });
    expect(context.core.events(context.worldId).at(-1)).toMatchObject({
      type: 'AwsSsmCommandReverted',
      payload: { transitionId },
    });
    expect(context.core.advanceClock(context.worldId, 1)).toMatchObject({
      appliedTransitions: [],
    });
  });

  it('同時刻の複数SSM revertをID順に同じtarget stateへ合成する', async () => {
    const context = await createContext();
    const instanceId = String(
      resourceByLogicalId(context, 'Instance').properties['refValue']
    );
    for (const [stop, start] of [
      ['systemctl stop nginx || true', 'systemctl start nginx || true'],
      [
        'systemctl stop tenkacloud-vibe || true',
        'systemctl start tenkacloud-vibe || true',
      ],
    ]) {
      execute(
        context,
        'ssm',
        'SendCommand',
        {
          targetRef: 'InstanceId',
          targetResource: instanceId,
          documentName: 'AWS-RunShellScript',
          parameters: { commands: [stop] },
          revert: {
            afterSeconds: 600,
            paramTemplate: { commands: [start] },
          },
        },
        'AWS::SSM::Command'
      );
    }
    const transitionIds = context.store
      .resources(context.worldId)
      .filter((resource) => resource.resourceType === 'AWS::SSM::Command')
      .map((resource) =>
        String(objectField(resource.properties, 'attributes')['TransitionId'])
      )
      .sort();

    const advanced = context.core.advanceClock(context.worldId, 600_000);
    const services = objectField(
      objectField(resourceByLogicalId(context, 'Instance').properties, 'state'),
      'services'
    );

    expect(advanced.appliedTransitions).toEqual(
      transitionIds.map((transitionId) => ({ provider: 'aws', transitionId }))
    );
    expect(services).toMatchObject({
      nginx: 'running',
      'tenkacloud-vibe': 'running',
    });
  });

  it('SSM clock projectionの不正時刻 document targetを成功扱いしない', async () => {
    const context = await createContext();
    const instanceId = String(
      resourceByLogicalId(context, 'Instance').properties['refValue']
    );
    execute(
      context,
      'ssm',
      'SendCommand',
      {
        targetRef: 'InstanceId',
        targetResource: instanceId,
        documentName: 'AWS-RunShellScript',
        parameters: { commands: ['systemctl stop nginx || true'] },
        revert: {
          afterSeconds: 600,
          paramTemplate: { commands: ['systemctl start nginx || true'] },
        },
      },
      'AWS::SSM::Command'
    );
    const provider = new AwsProvider();
    const view = {
      world: context.core.world(context.worldId),
      resources: context.store.resources(context.worldId),
    };
    const command = view.resources.find(
      (resource) => resource.resourceType === 'AWS::SSM::Command'
    );
    if (!command) throw new Error('SSM command resource がありません');
    const state = objectField(command.properties, 'state');
    const transition = objectField(state, 'transition');
    const withTransition = (patch: Readonly<Record<string, unknown>>) => ({
      ...view,
      resources: view.resources.map((resource) =>
        resource.resourceId === command.resourceId
          ? {
              ...resource,
              properties: {
                ...resource.properties,
                state: {
                  ...state,
                  transition: { ...transition, ...patch },
                },
              },
            }
          : resource
      ),
    });
    const due = {
      previousVirtualTime: view.world.virtualTime,
      virtualTime: '2026-07-12T00:10:00.000Z',
    };

    expect(() =>
      provider.advanceClock({ ...due, virtualTime: 'invalid' }, view)
    ).toThrow('clock target time is invalid');
    expect(() =>
      provider.advanceClock(due, withTransition({ scheduledAt: 'invalid' }))
    ).toThrow('scheduledAt is invalid');
    expect(() =>
      provider.advanceClock(
        due,
        withTransition({ documentName: 'AWS-RunPowerShellScript' })
      )
    ).toThrow('transition document AWS-RunPowerShellScript');
    expect(() =>
      provider.advanceClock(due, {
        ...view,
        resources: view.resources.filter(
          (resource) => resource.resourceType !== 'AWS::EC2::Instance'
        ),
      })
    ).toThrow('transition target');
  });

  it('declarative SSM commandのtarget document schedule size境界をloudに拒否する', async () => {
    const context = await createContext();
    const instanceId = String(
      resourceByLogicalId(context, 'Instance').properties['refValue']
    );
    const base = {
      targetRef: 'InstanceId',
      targetResource: instanceId,
      documentName: 'AWS-RunShellScript',
      parameters: { commands: ['true'] },
      revert: {
        afterSeconds: 600,
        paramTemplate: { commands: ['true'] },
      },
    };
    const invalidInputs = [
      { ...base, unknown: true },
      { ...base, targetRef: 'MissingOutput' },
      { ...base, targetResource: 'i-other' },
      { ...base, documentName: 'AWS-RunPowerShellScript' },
      {
        ...base,
        revert: { ...base.revert, unknown: true },
      },
      {
        ...base,
        revert: {
          ...base.revert,
          documentName: 'AWS-RunPowerShellScript',
        },
      },
      {
        ...base,
        parameters: { body: 'x'.repeat(64 * 1024) },
      },
      {
        ...base,
        revert: { ...base.revert, afterSeconds: 0 },
      },
      {
        ...base,
        revert: { ...base.revert, afterSeconds: 1.5 },
      },
      {
        ...base,
        revert: { ...base.revert, afterSeconds: 86_401 },
      },
    ];
    for (const input of invalidInputs) {
      expect(() =>
        execute(context, 'ssm', 'SendCommand', input, 'AWS::SSM::Command')
      ).toThrow(CoreError);
    }
    const provider = new AwsProvider();
    const circular: Record<string, unknown> = {};
    circular['self'] = circular;
    const directCommand: ProviderCommandInput = {
      worldId: context.worldId,
      deploymentId: context.deploymentId,
      service: 'ssm',
      operation: 'SendCommand',
      resourceType: 'AWS::SSM::Command',
      input: { ...base, parameters: circular },
    };
    const view = {
      world: context.core.world(context.worldId),
      resources: context.store.resources(context.worldId),
    };
    expect(() =>
      provider.reduce(
        { ...directCommand, resourceType: 'AWS::SSM::Other', input: base },
        view
      )
    ).toThrow('resource AWS::SSM::Other is not supported');
    expect(() => provider.reduce(directCommand, view)).toThrow(
      'must be JSON serializable'
    );
    expect(() =>
      provider.reduce(
        { ...directCommand, input: base },
        {
          ...view,
          world: { ...view.world, virtualTime: 'invalid' },
        }
      )
    ).toThrow('world virtual time is invalid');

    const missingId = 'i-missing';
    const resourcesWithoutTarget = view.resources
      .filter((resource) => resource.resourceType !== 'AWS::EC2::Instance')
      .map((resource) =>
        resource.resourceType === 'AWS::CloudFormation::Stack'
          ? {
              ...resource,
              properties: {
                ...resource.properties,
                outputs: {
                  ...objectField(resource.properties, 'outputs'),
                  InstanceId: missingId,
                },
              },
            }
          : resource
      );
    expect(() =>
      provider.reduce(
        {
          ...directCommand,
          input: { ...base, targetResource: missingId },
        },
        { ...view, resources: resourcesWithoutTarget }
      )
    ).toThrow('target resource does not exist');
  });

  it('SSM SendCommand は未知document target commandを成功扱いしない', async () => {
    const context = await createContext();
    const instanceId = String(
      resourceByLogicalId(context, 'Instance').properties['refValue']
    );
    const base = {
      InstanceIds: [instanceId],
      DocumentName: 'AWS-RunShellScript',
      Parameters: { commands: ['unknown command'] },
    };
    expect(() =>
      execute(context, 'ssm', 'SendCommand', base, 'AWS::EC2::Instance')
    ).toThrow('outside the catalog reducer');
    expect(() =>
      execute(
        context,
        'ssm',
        'SendCommand',
        { ...base, DocumentName: 'Unknown' },
        'AWS::EC2::Instance'
      )
    ).toThrow('document Unknown');
    expect(() =>
      execute(
        context,
        'ssm',
        'SendCommand',
        {
          Targets: [{ Key: 'tag:Name', Values: ['x'] }],
          DocumentName: 'AWS-RunShellScript',
          Parameters: { commands: ['true'] },
        },
        'AWS::EC2::Instance'
      )
    ).toThrow('only InstanceIds');
    expect(() =>
      execute(
        context,
        'ssm',
        'SendCommand',
        {
          DocumentName: 'AWS-RunShellScript',
          Parameters: { commands: ['true'] },
        },
        'AWS::EC2::Instance'
      )
    ).toThrow('requires InstanceIds or Targets');
    expect(
      objectField(
        execute(
          context,
          'ssm',
          'SendCommand',
          {
            Targets: [{ Key: 'InstanceIds', Values: [instanceId] }],
            DocumentName: 'AWS-RunShellScript',
            Parameters: { Commands: ['true'] },
          },
          'AWS::EC2::Instance'
        ),
        'Command'
      )['Status']
    ).toBe('Success');
  });

  it('S3 object bodyを保存 取得 一覧 削除する', async () => {
    const context = await createContext();
    const bucket = 'tc-fixture-bucket-000000000000';
    expect(
      execute(context, 's3', 'GetObject', { Bucket: bucket, Key: 'tool.sh' })[
        'Body'
      ]
    ).toContain('fixture');
    expect(
      execute(context, 's3', 'ListBucket', { Bucket: bucket })['KeyCount']
    ).toBe(1);
    execute(context, 's3', 'PutObject', {
      Bucket: bucket,
      Key: 'data.txt',
      Body: 'payload',
      ContentType: 'text/plain',
      Metadata: { owner: 'team' },
    });
    expect(
      execute(context, 's3', 'ListBucket', { Bucket: bucket })['KeyCount']
    ).toBe(2);
    expect(
      execute(context, 's3', 'GetObject', { Bucket: bucket, Key: 'data.txt' })[
        'ContentLength'
      ]
    ).toBe(7);
    expect(
      execute(context, 's3', 'GetBucketLocation', { Bucket: bucket })[
        'LocationConstraint'
      ]
    ).toBe('us-east-1');
    execute(context, 's3', 'DeleteObject', { Bucket: bucket, Key: 'data.txt' });
    expect(() =>
      execute(context, 's3', 'GetObject', { Bucket: bucket, Key: 'data.txt' })
    ).toThrow('object does not exist');
    expect(() =>
      execute(context, 's3', 'PutObject', {
        Bucket: bucket,
        Key: 'bad',
        Body: 3,
      })
    ).toThrow('Body must be a string');
  });

  it('Lambda CreateFunctionがbounded ZIPのcontrol planeを保存する', async () => {
    const context = await createContext();
    const request = {
      FunctionName: 'participant-users',
      Runtime: 'nodejs22.x',
      Role: 'arn:aws:iam::123456789012:role/tc-fixture-role',
      Handler: 'index.handler',
      Code: {
        ZipFile: Buffer.from('PK\u0003\u0004fixture').toString('base64'),
      },
      Description: 'users migration function',
      Timeout: 12,
      MemorySize: 256,
      Publish: false,
      PackageType: 'Zip',
      Architectures: ['x86_64'],
      Environment: { Variables: { PLATFORM: 'lambda' } },
      Tags: { challenge: 'migration' },
    };
    const created = execute(context, 'lambda', 'CreateFunction', request);
    expect(created).toMatchObject({
      FunctionName: 'participant-users',
      Runtime: 'nodejs22.x',
      Handler: 'index.handler',
      State: 'Active',
      CodeSha256: expect.any(String),
    });
    const read = objectField(
      execute(context, 'lambda', 'GetFunction', {
        FunctionName: 'participant-users',
      }),
      'Configuration'
    );
    expect(read).toMatchObject({
      FunctionName: 'participant-users',
      Timeout: 12,
      MemorySize: 256,
      Architectures: ['x86_64'],
      Environment: { Variables: { PLATFORM: '[REDACTED]' } },
    });
    expect(() =>
      execute(context, 'lambda', 'InvokeFunction', {
        FunctionName: 'participant-users',
        Payload: {},
      })
    ).toThrow('is not implemented');
    expect(() => execute(context, 'lambda', 'CreateFunction', request)).toThrow(
      'already exists'
    );
  });

  it('Lambda CreateFunctionが未再現の設定と不正payloadを成功扱いしない', async () => {
    const context = await createContext();
    const valid = {
      FunctionName: 'participant-orders',
      Runtime: 'nodejs20.x',
      Role: 'arn:aws:iam::123456789012:role/tc-fixture-role',
      Handler: 'index.handler',
      Code: { ZipFile: Buffer.from('zip').toString('base64') },
    };
    const invalid: readonly Readonly<Record<string, unknown>>[] = [
      { ...valid, Layers: [] },
      { ...valid, FunctionName: 'invalid name' },
      { ...valid, Runtime: 'python3.13' },
      { ...valid, Handler: '../bad handler' },
      { ...valid, Role: 'not-an-arn' },
      { ...valid, PackageType: 'Image' },
      { ...valid, Architectures: ['arm64'] },
      { ...valid, Architectures: ['x86_64', 'x86_64'] },
      { ...valid, Publish: true },
      { ...valid, Code: { S3Bucket: 'bucket', S3Key: 'code.zip' } },
      { ...valid, Code: { ZipFile: '%' } },
      { ...valid, Code: { ZipFile: '' } },
      {
        ...valid,
        Code: {
          ZipFile: Buffer.alloc(768 * 1024 + 1).toString('base64'),
        },
      },
      { ...valid, Environment: { Unknown: {} } },
      { ...valid, Environment: { Variables: { 'BAD-KEY': 'value' } } },
      { ...valid, Tags: { tag: 1 } },
      { ...valid, Description: 'x'.repeat(257) },
      { ...valid, Timeout: 0 },
      { ...valid, MemorySize: 127 },
    ];
    for (const request of invalid) {
      expect(() =>
        execute(context, 'lambda', 'CreateFunction', request)
      ).toThrow();
    }
  });

  it('Lambda Hello Search handlerを決定的にinvokeする', async () => {
    const context = await createContext();
    const hello = String(
      resourceByLogicalId(context, 'HelloFunction').properties['refValue']
    );
    const search = String(
      resourceByLogicalId(context, 'SearchFunction').properties['refValue']
    );
    const gate = String(
      resourceByLogicalId(context, 'GateFunction').properties['refValue']
    );
    const configuration = objectField(
      execute(context, 'lambda', 'GetFunction', { FunctionName: hello }),
      'Configuration'
    );
    expect(configuration['FunctionName']).toBe(hello);
    expect(JSON.stringify(configuration)).not.toContain('fixture-seed');
    const gateConfiguration = objectField(
      execute(context, 'lambda', 'GetFunction', { FunctionName: gate }),
      'Configuration'
    );
    expect(JSON.stringify(gateConfiguration)).toContain('[REDACTED]');
    expect(JSON.stringify(gateConfiguration)).not.toContain('fixture-seed');
    expect(
      objectField(
        execute(context, 'lambda', 'InvokeFunction', {
          FunctionName: hello,
          Payload: {},
        }),
        'Payload'
      )['statusCode']
    ).toBe(200);
    const invokeSearch = (payload: Readonly<Record<string, unknown>>) =>
      objectField(
        execute(context, 'lambda', 'InvokeFunction', {
          FunctionName: search,
          Payload: payload,
        }),
        'Payload'
      );
    expect(invokeSearch({ httpMethod: 'OPTIONS' })['statusCode']).toBe(204);
    expect(invokeSearch({ httpMethod: 'GET' })['statusCode']).toBe(200);
    expect(invokeSearch({ httpMethod: 'HEAD' })['body']).toBe('');
    expect(invokeSearch({ httpMethod: 'POST' })['statusCode']).toBe(200);
    expect(invokeSearch({ httpMethod: 'DELETE' })['statusCode']).toBe(405);
    expect(
      invokeSearch({ httpMethod: 'QUERY', headers: {} })['statusCode']
    ).toBe(415);
    expect(
      invokeSearch({
        httpMethod: 'QUERY',
        headers: { 'content-type': 'application/json' },
        body: '{',
      })['statusCode']
    ).toBe(400);
    expect(
      invokeSearch({
        httpMethod: 'QUERY',
        headers: { 'content-type': 'application/json' },
        body: '{}',
      })['statusCode']
    ).toBe(422);
    const validBody = Buffer.from(
      JSON.stringify({ query: { match: 'tenka' } })
    ).toString('base64');
    expect(
      invokeSearch({
        httpMethod: 'QUERY',
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
        isBase64Encoded: true,
        body: validBody,
      })['body']
    ).toContain('TC{fixture-seed}');
  });

  it('Lambda Gate handlerがSSM修正前後のx402支払いを再現する', async () => {
    const context = await createContext();
    const gate = String(
      resourceByLogicalId(context, 'GateFunction').properties['refValue']
    );
    const invokeGate = (payload: Readonly<Record<string, unknown>>) =>
      objectField(
        execute(context, 'lambda', 'InvokeFunction', {
          FunctionName: gate,
          Payload: JSON.stringify(payload),
        }),
        'Payload'
      );
    expect(invokeGate({ rawPath: '/free', headers: {} })['statusCode']).toBe(
      200
    );
    expect(
      invokeGate({ rawPath: '/content/article', headers: {} })['statusCode']
    ).toBe(402);
    expect(
      invokeGate({
        rawPath: '/content/article',
        headers: { 'x-payment': 'invalid' },
      })['statusCode']
    ).toBe(402);
    const wallet = '0x00000000000000000000000000000000deadc0de';
    execute(context, 'ssm', 'PutParameter', {
      Name: '/tc-fixture/config/pay_to_wallet',
      Value: wallet,
      Overwrite: true,
    });
    const rejected = Buffer.from(
      JSON.stringify({ payTo: wallet, network: 'wrong', amount: '10000' })
    ).toString('base64');
    expect(
      invokeGate({
        rawPath: '/content/article',
        headers: { 'x-payment': rejected },
      })['statusCode']
    ).toBe(402);
    const accepted = Buffer.from(
      JSON.stringify({
        payTo: wallet,
        network: 'base-sepolia',
        amount: '10000',
      })
    ).toString('base64');
    const response = invokeGate({
      rawPath: '/content/article',
      headers: { 'X-PAYMENT': accepted },
    });
    expect(response['statusCode']).toBe(200);
    expect(response['body']).toContain('TC{fixture-seed}');
    const evaluator = String(
      resourceByLogicalId(context, 'EvaluatorFunction').properties['refValue']
    );
    expect(() =>
      execute(context, 'lambda', 'InvokeFunction', {
        FunctionName: evaluator,
        Payload: { workerUrl: 'https://example.workers.dev' },
      })
    ).toThrow('external HTTP runtime');
  });

  it('Lambda Evaluatorをasync reducerから実HTTPへ接続して結果を冪等保存する', async () => {
    const context = await createContext();
    const server = Bun.serve({
      hostname: '127.0.0.1',
      port: 0,
      fetch: externalWorker,
    });
    try {
      const core = new SimulationCore(
        context.store,
        new ProviderRegistry([
          new AwsProvider({ trustedWorkerOrigins: [server.url.origin] }),
        ])
      );
      const evaluator = String(
        resourceByLogicalId(context, 'EvaluatorFunction').properties['refValue']
      );
      const command: ProviderCommandInput = {
        worldId: context.worldId,
        deploymentId: context.deploymentId,
        service: 'lambda',
        operation: 'InvokeFunction',
        resourceType: '*',
        input: {
          FunctionName: evaluator,
          Payload: { workerUrl: server.url.origin },
        },
      };
      const executeAsync = (
        providerCommand: ProviderCommandInput,
        key: string
      ) =>
        core.executeCommandAsync(
          context.worldId,
          {
            deploymentId: context.deploymentId,
            targetId: 'default',
            provider: 'aws',
            engine: 'cloudformation',
            service: providerCommand.service,
            operation: providerCommand.operation,
            resourceType: providerCommand.resourceType,
            input: providerCommand.input,
          },
          key
        );

      const first = await executeAsync(command, 'external-evaluator');
      const repeated = await executeAsync(command, 'external-evaluator');
      expect(first).toEqual(repeated);
      expect(objectField(first, 'Payload')).toMatchObject({
        passed: true,
        flag: 'TC{fixture-seed}',
      });
      expect(
        objectField(
          resourceByLogicalId(context, 'EvaluatorFunction').properties,
          'state'
        )['invocationCount']
      ).toBe(1);

      const hello = String(
        resourceByLogicalId(context, 'HelloFunction').properties['refValue']
      );
      expect(
        objectField(
          await executeAsync(
            {
              ...command,
              input: { FunctionName: hello, Payload: {} },
            },
            'async-hello'
          ),
          'Payload'
        )['statusCode']
      ).toBe(200);
      expect(
        await executeAsync(
          {
            ...command,
            operation: 'GetFunction',
            input: { FunctionName: hello },
          },
          'async-get-function'
        )
      ).toHaveProperty('Configuration');
      expect(
        await executeAsync(
          {
            ...command,
            service: 'ssm',
            operation: 'GetParameter',
            input: { Name: '/tc-fixture/config/currency_mode' },
          },
          'async-ssm'
        )
      ).toHaveProperty('Parameter');
    } finally {
      await server.stop(true);
    }
  });

  it('EC2 security group revoke と instance describeをstateから返す', async () => {
    const context = await createContext();
    const groupId = String(
      resourceByLogicalId(context, 'SecurityGroup').properties['refValue']
    );
    const groups = execute(context, 'ec2', 'DescribeSecurityGroups', {
      GroupIds: [groupId],
    });
    expect(arrayField(groups, 'SecurityGroups')).toHaveLength(1);
    execute(context, 'ec2', 'RevokeSecurityGroupIngress', {
      GroupId: groupId,
      IpPermissions: [
        {
          IpProtocol: 'tcp',
          FromPort: 80,
          ToPort: 80,
          CidrIp: '0.0.0.0/0',
        },
      ],
    });
    execute(context, 'ec2', 'RevokeSecurityGroupEgress', {
      GroupId: groupId,
      IpProtocol: '-1',
      CidrIp: '0.0.0.0/0',
    });
    const after = arrayField(
      execute(context, 'ec2', 'DescribeSecurityGroups', {
        GroupIds: [groupId],
      }),
      'SecurityGroups'
    )[0];
    if (after === null || typeof after !== 'object') {
      throw new Error('security group is missing');
    }
    expect(Reflect.get(after, 'IpPermissions')).toEqual([]);
    expect(
      arrayField(
        execute(context, 'ec2', 'DescribeInstances', {}),
        'Reservations'
      )
    ).toHaveLength(1);
    expect(() =>
      execute(context, 'ec2', 'RevokeSecurityGroupIngress', {
        GroupId: groupId,
      })
    ).toThrow('IpPermissions must not be empty');
  });

  it('ELB ruleをdescribeしてconditionsとactionsをmodifyする', async () => {
    const context = await createContext();
    const ruleArn = String(
      resourceByLogicalId(context, 'ListenerRule').properties['refValue']
    );
    expect(
      arrayField(
        execute(context, 'elasticloadbalancing', 'DescribeRules', {
          RuleArns: [ruleArn],
        }),
        'Rules'
      )
    ).toHaveLength(1);
    expect(() =>
      execute(context, 'elasticloadbalancing', 'ModifyRule', {
        RuleArn: ruleArn,
      })
    ).toThrow('requires Conditions or Actions');
    expect(() =>
      execute(context, 'elasticloadbalancing', 'ModifyRule', {
        RuleArn: ruleArn,
        Conditions: 'bad',
      })
    ).toThrow('Conditions must be an array');
    expect(() =>
      execute(context, 'elasticloadbalancing', 'ModifyRule', {
        RuleArn: ruleArn,
        Actions: 'bad',
      })
    ).toThrow('Actions must be an array');
    const modified = execute(context, 'elasticloadbalancing', 'ModifyRule', {
      RuleArn: ruleArn,
      Conditions: [
        {
          Field: 'http-request-method',
          HttpRequestMethodConfig: { Values: ['QUERY'] },
        },
      ],
      Actions: [
        { Type: 'fixed-response', FixedResponseConfig: { StatusCode: '200' } },
      ],
    });
    expect(arrayField(modified, 'Rules')).toHaveLength(1);
  });

  it('WAF association とRDS describeを実resource stateで扱う', async () => {
    const context = await createContext();
    const webAcl = resourceByLogicalId(context, 'WebAcl');
    const attributes = objectField(webAcl.properties, 'attributes');
    const webAclArn = String(attributes['Arn']);
    const webAclId = String(attributes['Id']);
    const loadBalancerArn = String(
      resourceByLogicalId(context, 'LoadBalancer').properties['refValue']
    );
    expect(
      execute(context, 'wafv2', 'GetWebACLForResource', {
        ResourceArn: loadBalancerArn,
      })
    ).toEqual({});
    execute(context, 'wafv2', 'AssociateWebACL', {
      WebACLArn: webAclArn,
      ResourceArn: loadBalancerArn,
    });
    expect(
      objectField(
        execute(context, 'wafv2', 'GetWebACLForResource', {
          ResourceArn: loadBalancerArn,
        }),
        'WebACL'
      )['ARN']
    ).toBe(webAclArn);
    expect(
      objectField(
        execute(context, 'wafv2', 'GetWebACL', {
          Id: webAclId,
          Name: 'tc-fixture-acl',
          Scope: 'REGIONAL',
        }),
        'WebACL'
      )['Name']
    ).toBe('tc-fixture-acl');
    expect(
      arrayField(execute(context, 'wafv2', 'ListWebACLs', {}), 'WebACLs')
    ).toHaveLength(1);
    execute(context, 'wafv2', 'DisassociateWebACL', {
      ResourceArn: loadBalancerArn,
    });
    expect(() =>
      execute(context, 'wafv2', 'DisassociateWebACL', {
        ResourceArn: loadBalancerArn,
      })
    ).toThrow('no Web ACL association');
    expect(
      arrayField(
        execute(context, 'rds', 'DescribeDBInstances', {}),
        'DBInstances'
      )
    ).toHaveLength(1);
    expect(() =>
      execute(context, 'rds', 'DescribeDBInstances', {
        DBInstanceIdentifier: 'missing',
      })
    ).toThrow('DB instance does not exist');
  });

  it('CloudWatch Logs group stream eventsを永続化しfilterする', async () => {
    const context = await createContext();
    expect(
      arrayField(
        execute(context, 'logs', 'DescribeLogGroups', {
          LogGroupNamePrefix: '/aws/lambda/',
        }),
        'logGroups'
      )
    ).toHaveLength(1);
    execute(context, 'logs', 'CreateLogGroup', {
      LogGroupName: '/app/runtime',
    });
    expect(() =>
      execute(context, 'logs', 'CreateLogGroup', {
        LogGroupName: '/app/runtime',
      })
    ).toThrow('already exists');
    execute(context, 'logs', 'CreateLogStream', {
      LogGroupName: '/app/runtime',
      LogStreamName: 'main',
    });
    expect(() =>
      execute(context, 'logs', 'CreateLogStream', {
        LogGroupName: '/app/runtime',
        LogStreamName: 'main',
      })
    ).toThrow('already exists');
    expect(() =>
      execute(context, 'logs', 'PutLogEvents', {
        LogGroupName: '/app/runtime',
        LogStreamName: 'main',
        LogEvents: [],
      })
    ).toThrow('non-empty array');
    expect(
      execute(context, 'logs', 'PutLogEvents', {
        LogGroupName: '/app/runtime',
        LogStreamName: 'main',
        LogEvents: [
          { timestamp: 2, message: 'error second' },
          { timestamp: 1, message: 'info first' },
        ],
      })['nextSequenceToken']
    ).toBe('2');
    expect(
      arrayField(
        execute(context, 'logs', 'DescribeLogStreams', {
          LogGroupName: '/app/runtime',
          LogStreamNamePrefix: 'ma',
        }),
        'logStreams'
      )
    ).toHaveLength(1);
    expect(
      arrayField(
        execute(context, 'logs', 'GetLogEvents', {
          LogGroupName: '/app/runtime',
          LogStreamName: 'main',
        }),
        'events'
      )
    ).toHaveLength(2);
    expect(
      arrayField(
        execute(context, 'logs', 'FilterLogEvents', {
          LogGroupName: '/app/runtime',
          FilterPattern: 'error',
        }),
        'events'
      )
    ).toHaveLength(1);
    expect(() =>
      execute(context, 'logs', 'GetLogEvents', {
        LogGroupName: '/app/runtime',
        LogStreamName: 'missing',
      })
    ).toThrow('log stream does not exist');
  });

  it('未公開 service と operation はprovider reducerで loudに失敗する', async () => {
    const context = await createContext();
    const provider = new AwsProvider();
    const world = {
      world: context.core.world(context.worldId),
      resources: context.store.resources(context.worldId),
    };
    const inputs: Readonly<Record<string, Readonly<Record<string, unknown>>>> =
      {
        iam: {},
        ssm: {},
        s3: { Bucket: 'tc-fixture-bucket-000000000000' },
        lambda: {
          FunctionName: resourceByLogicalId(context, 'HelloFunction')
            .properties['refValue'],
        },
        elasticloadbalancing: {},
        ec2: {},
        rds: {},
        wafv2: {},
        logs: {},
        cloudformation: {},
      };
    for (const [service, input] of Object.entries(inputs)) {
      const command: ProviderCommandInput = {
        worldId: context.worldId,
        deploymentId: context.deploymentId,
        service,
        operation: 'Unknown',
        resourceType: '*',
        input,
      };
      expect(() => provider.reduce(command, world)).toThrow(CoreError);
    }
    expect(() =>
      provider.reduce(
        {
          worldId: context.worldId,
          deploymentId: context.deploymentId,
          service: 'unknown',
          operation: 'Unknown',
          resourceType: '*',
          input: {},
        },
        world
      )
    ).toThrow('AWS service unknown');
  });
});
