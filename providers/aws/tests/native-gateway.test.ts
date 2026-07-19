import { afterEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import {
  AWS_NATIVE_DEPLOYMENT_HEADER,
  AWS_NATIVE_TARGET_HEADER,
  AWS_NATIVE_WORLD_HEADER,
  AwsNativeGateway,
  AwsNativeGatewayError,
} from '../src/native-gateway';
import { AwsProvider } from '../src/provider';
import {
  cleanupContexts,
  createContext,
  execute,
  resourceByLogicalId,
  type TestContext,
} from './support';

const ORIGIN = 'https://aws.simulator.test';
const ACCESS_KEY_ID = 'TCSIMLOCALACCESS01';
let requestSequence = 0;

afterEach(cleanupContexts);

interface NativeRequestInput {
  readonly context: TestContext;
  readonly service: string;
  readonly path?: string;
  readonly method?: string;
  readonly body?: string;
  readonly contentType?: string;
  readonly target?: string;
  readonly origin?: string;
  readonly accessKeyId?: string;
  readonly region?: string;
  readonly targetId?: string;
  readonly mutateHeaders?: (headers: Headers) => void;
}

function gateway(maxBodyBytes?: number): AwsNativeGateway {
  return new AwsNativeGateway({
    simulatorOrigin: ORIGIN,
    simulatorAccessKeyId: ACCESS_KEY_ID,
    ...(maxBodyBytes === undefined ? {} : { maxBodyBytes }),
  });
}

function nativeRequest(input: NativeRequestInput): Request {
  requestSequence += 1;
  const headers = new Headers({
    [AWS_NATIVE_WORLD_HEADER]: input.context.worldId,
    [AWS_NATIVE_DEPLOYMENT_HEADER]: input.context.deploymentId,
    [AWS_NATIVE_TARGET_HEADER]: input.targetId ?? 'default',
    'x-amz-date': '20260712T010203Z',
  });
  if (input.contentType) headers.set('content-type', input.contentType);
  if (input.target) headers.set('x-amz-target', input.target);
  const signedHeaders = [
    'host',
    ...[...headers.keys()].map((name) => name.toLowerCase()),
  ].sort();
  const signature = createHash('sha256')
    .update(
      JSON.stringify({
        requestSequence,
        service: input.service,
        path: input.path,
        body: input.body,
      })
    )
    .digest('hex');
  headers.set(
    'authorization',
    `AWS4-HMAC-SHA256 Credential=${input.accessKeyId ?? ACCESS_KEY_ID}/20260712/${input.region ?? 'us-east-1'}/${input.service}/aws4_request, SignedHeaders=${signedHeaders.join(';')}, Signature=${signature}`
  );
  input.mutateHeaders?.(headers);
  return new Request(`${input.origin ?? ORIGIN}${input.path ?? '/'}`, {
    method: input.method ?? 'POST',
    headers,
    ...(input.body === undefined ? {} : { body: input.body }),
  });
}

function queryBody(
  action: string,
  version: string,
  entries: Readonly<Record<string, string>> = {}
): string {
  return new URLSearchParams({
    Action: action,
    Version: version,
    ...entries,
  }).toString();
}

async function responseText(response: Response): Promise<string> {
  const text = await response.text();
  expect(response.headers.get('x-amzn-requestid')).toStartWith('tcsim-');
  return text;
}

async function captureGatewayError(
  action: () => Promise<unknown>
): Promise<AwsNativeGatewayError> {
  try {
    await action();
  } catch (error) {
    if (error instanceof AwsNativeGatewayError) return error;
    throw error;
  }
  throw new Error('gateway error was not thrown');
}

describe('AWS native gateway', () => {
  it('Query request を core command に変換してサービス固有 XML を返す', async () => {
    const context = await createContext();
    const cases = [
      {
        service: 'cloudformation',
        action: 'DescribeStacks',
        version: '2010-05-15',
        entries: {},
        expected: '<StackName>aws-fixture-default</StackName>',
      },
      {
        service: 'iam',
        action: 'GetRole',
        version: '2010-05-08',
        entries: { RoleName: 'tc-fixture-role' },
        expected: '<RoleName>tc-fixture-role</RoleName>',
      },
      {
        service: 'ec2',
        action: 'DescribeInstances',
        version: '2016-11-15',
        entries: {},
        expected: '<reservationSet>',
      },
      {
        service: 'rds',
        action: 'DescribeDBInstances',
        version: '2014-10-31',
        entries: { DBInstanceIdentifier: 'tc-fixture-db' },
        expected: '<DBInstanceIdentifier>tc-fixture-db</DBInstanceIdentifier>',
      },
      {
        service: 'sts',
        action: 'GetCallerIdentity',
        version: '2011-06-15',
        entries: {},
        expected: '<Account>000000000000</Account>',
      },
    ];
    for (const item of cases) {
      const response = await gateway().handle(
        nativeRequest({
          context,
          service: item.service,
          body: queryBody(item.action, item.version, item.entries),
          contentType: 'application/x-www-form-urlencoded',
        }),
        context.core
      );
      expect(response.status).toBe(200);
      expect(response.headers.get('content-type')).toContain('xml');
      expect(await responseText(response)).toContain(item.expected);
    }
  });

  it('ELBv2 Query の member 構造を配列へ戻して listener rule を更新する', async () => {
    const context = await createContext();
    const rule = resourceByLogicalId(context, 'ListenerRule');
    const ruleArn = String(rule.properties['refValue']);
    const body = queryBody('ModifyRule', '2015-12-01', {
      RuleArn: ruleArn,
      'Conditions.member.1.Field': 'http-request-method',
      'Conditions.member.1.HttpRequestMethodConfig.Values.member.1': 'GET',
      'Conditions.member.1.HttpRequestMethodConfig.Values.member.2': 'QUERY',
    });
    const translated = await gateway().translate(
      nativeRequest({
        context,
        service: 'elasticloadbalancing',
        body,
        contentType: 'application/x-www-form-urlencoded',
      })
    );
    expect(translated).toMatchObject({
      worldId: context.worldId,
      protocol: 'query',
      service: 'elasticloadbalancing',
      operation: 'ModifyRule',
      command: {
        resourceType: '*',
        input: {
          Conditions: [
            {
              Field: 'http-request-method',
              HttpRequestMethodConfig: { Values: ['GET', 'QUERY'] },
            },
          ],
        },
      },
    });
    const response = await gateway().handle(
      nativeRequest({
        context,
        service: 'elasticloadbalancing',
        body,
        contentType: 'application/x-www-form-urlencoded',
      }),
      context.core
    );
    expect(response.status).toBe(200);
    expect(await responseText(response)).toContain('<member>QUERY</member>');
  });

  it('EC2 Query の IpRanges を security group rule に正規化する', async () => {
    const context = await createContext();
    const groupId = String(
      resourceByLogicalId(context, 'SecurityGroup').properties['refValue']
    );
    const described = await gateway().handle(
      nativeRequest({
        context,
        service: 'ec2',
        body: queryBody('DescribeSecurityGroups', '2016-11-15', {
          'GroupId.1': groupId,
        }),
        contentType: 'application/x-www-form-urlencoded',
      }),
      context.core
    );
    expect(await responseText(described)).toContain(
      '<ipRanges><item><cidrIp>0.0.0.0/0</cidrIp></item></ipRanges>'
    );

    const revoked = await gateway().handle(
      nativeRequest({
        context,
        service: 'ec2',
        body: queryBody('RevokeSecurityGroupIngress', '2016-11-15', {
          GroupId: groupId,
          'IpPermissions.1.IpProtocol': 'tcp',
          'IpPermissions.1.FromPort': '80',
          'IpPermissions.1.ToPort': '80',
          'IpPermissions.1.IpRanges.1.CidrIp': '0.0.0.0/0',
        }),
        contentType: 'application/x-www-form-urlencoded',
      }),
      context.core
    );
    expect(revoked.status).toBe(200);
    expect(await responseText(revoked)).toContain('<return>true</return>');
  });

  it('IAM Query の percent encoded policy document を object として永続化する', async () => {
    const context = await createContext();
    const policy = JSON.stringify({
      Version: '2012-10-17',
      Statement: [{ Effect: 'Allow', Action: 'ssm:GetParameter' }],
    });
    const put = await gateway().handle(
      nativeRequest({
        context,
        service: 'iam',
        body: queryBody('PutRolePolicy', '2010-05-08', {
          RoleName: 'tc-fixture-role',
          PolicyName: 'NativePolicy',
          PolicyDocument: policy,
        }),
        contentType: 'application/x-www-form-urlencoded',
      }),
      context.core
    );
    expect(put.status).toBe(200);
    const fetched = execute(context, 'iam', 'GetRolePolicy', {
      RoleName: 'tc-fixture-role',
      PolicyName: 'NativePolicy',
    });
    expect(Reflect.get(fetched, 'PolicyDocument')).toMatchObject({
      Version: '2012-10-17',
    });
    const role = await gateway().handle(
      nativeRequest({
        context,
        service: 'iam',
        body: queryBody('GetRole', '2010-05-08', {
          RoleName: 'tc-fixture-role',
        }),
        contentType: 'application/x-www-form-urlencoded',
      }),
      context.core
    );
    expect(await responseText(role)).toContain('<AssumeRolePolicyDocument>%7B');

    for (const document of ['{', '[]']) {
      const invalid = await gateway().handle(
        nativeRequest({
          context,
          service: 'iam',
          body: queryBody('PutRolePolicy', '2010-05-08', {
            RoleName: 'tc-fixture-role',
            PolicyName: 'InvalidPolicy',
            PolicyDocument: document,
          }),
          contentType: 'application/x-www-form-urlencoded',
        }),
        context.core
      );
      expect(invalid.status).toBe(400);
    }
  });

  it('AWS JSON 1.1 の target と Logs lowerCamel input を実 reducer に接続する', async () => {
    const context = await createContext();
    const requests = [
      {
        service: 'ssm',
        target: 'AmazonSSM.GetParameter',
        body: JSON.stringify({ Name: '/tc-fixture/config/currency_mode' }),
        expected: '"Value":"test"',
      },
      {
        service: 'logs',
        target: 'Logs_20140328.DescribeLogGroups',
        body: JSON.stringify({ logGroupNamePrefix: '/aws/lambda/' }),
        expected: '"logGroupName":"/aws/lambda/tc-fixture"',
      },
      {
        service: 'wafv2',
        target: 'AWSWAF_20190729.ListWebACLs',
        body: JSON.stringify({ Scope: 'REGIONAL' }),
        expected: '"Name":"tc-fixture-acl"',
      },
    ];
    for (const item of requests) {
      const response = await gateway().handle(
        nativeRequest({
          context,
          service: item.service,
          target: item.target,
          body: item.body,
          contentType: 'application/x-amz-json-1.1',
        }),
        context.core
      );
      expect(response.status).toBe(200);
      expect(response.headers.get('content-type')).toBe(
        'application/x-amz-json-1.1'
      );
      expect(await responseText(response)).toContain(item.expected);
    }
  });

  it('Runtime endpoint を CFn output から解決して override をSQLiteへ永続化する', async () => {
    const context = await createContext('runtime-metadata.json');
    const resolve = (body: Readonly<Record<string, unknown>>) =>
      gateway().handle(
        nativeRequest({
          context,
          service: 'runtime',
          target: 'TenkaCloudRuntime.ResolveEndpoint',
          body: JSON.stringify(body),
          contentType: 'application/x-amz-json-1.1',
        }),
        context.core
      );

    const initial = await resolve({ Slot: 'app' });
    expect(initial.status).toBe(409);
    expect(await initial.text()).toContain('workload is unavailable');

    const updated = await resolve({
      Slot: 'app',
      OverrideUrl: 'https://participant.example.test/service',
    });
    expect(await updated.json()).toEqual({
      Slot: 'app',
      Url: 'https://participant.example.test/service',
      Source: 'override',
      Overridable: true,
      OutputKey: 'FunctionUrl',
    });
    const stored = context.store
      .resources(context.worldId)
      .find(
        (resource) =>
          resource.resourceType === 'Runtime::Endpoint' &&
          resource.properties['Slot'] === 'app'
      );
    expect(stored?.properties['state']).toMatchObject({
      overrideUrl: 'https://participant.example.test/service',
    });

    expect(await (await resolve({ Slot: 'app' })).json()).toMatchObject({
      Source: 'override',
    });
    expect(
      (await resolve({ Slot: 'fixed', OverrideUrl: 'https://example.test' }))
        .status
    ).toBe(409);
    expect((await resolve({ Slot: 'missing' })).status).toBe(404);
    for (const OverrideUrl of [
      'https://user:secret@example.test/',
      'not-a-url',
      `https://example.test/${'x'.repeat(2048)}`,
    ]) {
      expect((await resolve({ Slot: 'app', OverrideUrl })).status).toBe(400);
    }
    const wrongTarget = await gateway().handle(
      nativeRequest({
        context,
        service: 'runtime',
        targetId: 'other',
        target: 'TenkaCloudRuntime.ResolveEndpoint',
        body: JSON.stringify({ Slot: 'app' }),
        contentType: 'application/x-amz-json-1.1',
      }),
      context.core
    );
    expect(wrongTarget.status).toBe(400);
    expect(await wrongTarget.text()).toContain(
      'command target does not belong to the deployment'
    );
  });

  it('HTTP AttackProbe が登録済みendpointの防御状態を実Requestから観測してSQLiteへ反映する', async () => {
    const security = await createContext(
      'security-probe-metadata.json',
      'security-battle-royale'
    );
    const jsonRequest = (
      context: TestContext,
      service: string,
      target: string,
      body: Readonly<Record<string, unknown>>
    ) =>
      gateway().handle(
        nativeRequest({
          context,
          service,
          target,
          body: JSON.stringify(body),
          contentType: 'application/x-amz-json-1.1',
        }),
        context.core
      );
    for (const [Slot, OverrideUrl] of [
      ['frontend', 'https://team.example.test/'],
      ['api', 'https://team.example.test:8080/'],
    ] as const) {
      expect(
        (
          await jsonRequest(
            security,
            'runtime',
            'TenkaCloudRuntime.ResolveEndpoint',
            { Slot, OverrideUrl }
          )
        ).status
      ).toBe(200);
    }

    const sqliInput = {
      Slot: 'api',
      Method: 'POST',
      Path: '/api/v1/auth',
      Body: JSON.stringify({
        username: "' OR '1'='1' -- ",
        password: 'x',
      }),
    };
    expect(
      await (
        await jsonRequest(
          security,
          'http',
          'TenkaCloudHTTP.AttackProbe',
          sqliInput
        )
      ).json()
    ).toMatchObject({
      Slot: 'api',
      StatusCode: 200,
      Vulnerable: true,
      Landed: true,
    });

    const instanceId = String(
      resourceByLogicalId(security, 'Instance').properties['refValue']
    );
    const patchCommand =
      'python3 -c \'from pathlib import Path; p=Path("/opt/tenkacloud/repo/battles/security-battle-royale/api/api.py"); p.write_text(p.read_text().replace("cur.execute(query)", "cur.execute(\\"SELECT username FROM username WHERE username=%s AND password=%s\\", (username, password))"))\'';
    expect(
      (
        await jsonRequest(security, 'ssm', 'AmazonSSM.SendCommand', {
          InstanceIds: [instanceId],
          DocumentName: 'AWS-RunShellScript',
          Parameters: { commands: [patchCommand] },
        })
      ).status
    ).toBe(200);
    expect(
      await (
        await jsonRequest(
          security,
          'http',
          'TenkaCloudHTTP.AttackProbe',
          sqliInput
        )
      ).json()
    ).toMatchObject({ StatusCode: 403, Vulnerable: false, Landed: false });

    expect(
      await (
        await jsonRequest(security, 'http', 'TenkaCloudHTTP.AttackProbe', {
          Probe: 'redteam/probes/availability-flood.sh',
        })
      ).json()
    ).toMatchObject({ StatusCode: 503, Vulnerable: true });
    await jsonRequest(security, 'ssm', 'AmazonSSM.SendCommand', {
      InstanceIds: [instanceId],
      DocumentName: 'AWS-RunShellScript',
      Parameters: {
        commands: [
          "printf 'limit_req_zone $binary_remote_addr zone=perip:10m rate=10r/s; limit_req zone=perip burst=20;' | sudo tee /etc/nginx/conf.d/rate-limit.conf",
          'systemctl restart nginx',
        ],
      },
    });
    expect(
      await (
        await jsonRequest(security, 'http', 'TenkaCloudHTTP.AttackProbe', {
          Probe: 'redteam/probes/availability-flood.sh',
        })
      ).json()
    ).toMatchObject({ StatusCode: 200, Vulnerable: false });

    const stackstack = await createContext(
      'stackstack-probe-metadata.json',
      'stackstack'
    );
    await jsonRequest(
      stackstack,
      'runtime',
      'TenkaCloudRuntime.ResolveEndpoint',
      { Slot: 'app', OverrideUrl: 'https://board.example.test/' }
    );
    const spamInput = { Probe: 'redteam/probes/anon-spam.sh' };
    expect(
      await (
        await jsonRequest(
          stackstack,
          'http',
          'TenkaCloudHTTP.AttackProbe',
          spamInput
        )
      ).json()
    ).toMatchObject({
      Slot: 'app',
      StatusCode: 201,
      Vulnerable: true,
      Landed: true,
      LandedPosts: 5,
    });
    expect(
      resourceByLogicalId(stackstack, 'Instance').properties['state']
    ).toMatchObject({ boardClean: false });

    const stackInstanceId = String(
      resourceByLogicalId(stackstack, 'Instance').properties['refValue']
    );
    await jsonRequest(stackstack, 'ssm', 'AmazonSSM.SendCommand', {
      InstanceIds: [stackInstanceId],
      DocumentName: 'AWS-RunShellScript',
      Parameters: {
        commands: [
          'tmp=$(mktemp); jq \'.auth_required=true|.auth_token="my-secret-42"\' /etc/tenkacloud-vibe/config.json > "$tmp" && sudo mv "$tmp" /etc/tenkacloud-vibe/config.json',
          'sqlite3 "$SQLITE_DB" "DELETE FROM posts WHERE author=\'redteam-spam\'"',
          'systemctl restart tenkacloud-vibe',
        ],
      },
    });
    expect(
      await (
        await jsonRequest(
          stackstack,
          'http',
          'TenkaCloudHTTP.AttackProbe',
          spamInput
        )
      ).json()
    ).toMatchObject({
      StatusCode: 401,
      Vulnerable: false,
      Landed: false,
      LandedPosts: 0,
    });
    expect(
      resourceByLogicalId(stackstack, 'Instance').properties['state']
    ).toMatchObject({
      authRequired: true,
      authTokenConfigured: true,
      boardClean: true,
    });
  });

  it('Lambda REST-JSON の Create Get Invoke を native payload で扱う', async () => {
    const context = await createContext();
    const createResponse = await gateway().handle(
      nativeRequest({
        context,
        service: 'lambda',
        path: '/2015-03-31/functions',
        body: JSON.stringify({
          FunctionName: 'native-participant',
          Runtime: 'nodejs22.x',
          Role: 'arn:aws:iam::123456789012:role/tc-fixture-role',
          Handler: 'index.handler',
          Code: { ZipFile: Buffer.from('native zip').toString('base64') },
        }),
        contentType: 'application/x-amz-json-1.1',
      }),
      context.core
    );
    expect(createResponse.status).toBe(200);
    expect(await createResponse.json()).toMatchObject({
      FunctionName: 'native-participant',
      Runtime: 'nodejs22.x',
      CodeSha256: expect.any(String),
    });

    const getResponse = await gateway().handle(
      nativeRequest({
        context,
        service: 'lambda',
        method: 'GET',
        path: '/2015-03-31/functions/tc-fixture-hello',
      }),
      context.core
    );
    expect(getResponse.status).toBe(200);
    expect(await getResponse.json()).toMatchObject({
      Configuration: { FunctionName: 'tc-fixture-hello' },
    });

    const invokeResponse = await gateway().handle(
      nativeRequest({
        context,
        service: 'lambda',
        path: '/2015-03-31/functions/tc-fixture-hello/invocations?Qualifier=%24LATEST',
        body: '{}',
        contentType: 'application/octet-stream',
      }),
      context.core
    );
    expect(invokeResponse.status).toBe(200);
    expect(invokeResponse.headers.get('x-amz-executed-version')).toBe(
      '$LATEST'
    );
    expect(await invokeResponse.json()).toMatchObject({ statusCode: 200 });

    const evaluatorResponse = await gateway().handle(
      nativeRequest({
        context,
        service: 'lambda',
        path: '/2015-03-31/functions/tc-fixture-evaluator/invocations',
        body: '{}',
        contentType: 'application/octet-stream',
      }),
      context.core
    );
    expect(evaluatorResponse.status).toBe(200);
    expect(await evaluatorResponse.json()).toEqual({
      passed: false,
      error: 'workerUrl is required',
    });
  });

  it('S3 REST-XML を path-style bucket と object body へ変換する', async () => {
    const context = await createContext();
    const bucket = 'tc-fixture-bucket-000000000000';
    const put = await gateway().handle(
      nativeRequest({
        context,
        service: 's3',
        method: 'PUT',
        path: `/${bucket}/native.txt`,
        body: 'native body',
        contentType: 'text/plain',
        mutateHeaders: (headers) =>
          headers.set('x-amz-meta-purpose', 'native-test'),
      }),
      context.core
    );
    expect(put.status).toBe(200);
    expect(put.headers.get('etag')).toBeTruthy();

    const get = await gateway().handle(
      nativeRequest({
        context,
        service: 's3',
        method: 'GET',
        path: `/${bucket}/native.txt`,
      }),
      context.core
    );
    expect(get.status).toBe(200);
    expect(get.headers.get('content-type')).toBe('text/plain');
    expect(get.headers.get('x-amz-meta-purpose')).toBe('native-test');
    expect(get.headers.get('etag')).toMatch(/^"[0-9a-f]+"$/);
    expect(get.headers.get('last-modified')).toBe(
      'Sun, 12 Jul 2026 00:00:00 GMT'
    );
    expect(await get.text()).toBe('native body');

    const head = await gateway().handle(
      nativeRequest({
        context,
        service: 's3',
        method: 'HEAD',
        path: `/${bucket}/native.txt`,
      }),
      context.core
    );
    expect(head.status).toBe(200);
    expect(head.headers.get('content-length')).toBe('11');
    expect(await head.text()).toBe('');

    const list = await gateway().handle(
      nativeRequest({
        context,
        service: 's3',
        method: 'GET',
        path: `/${bucket}?list-type=2&prefix=native`,
      }),
      context.core
    );
    expect(await responseText(list)).toContain('<Key>native.txt</Key>');

    const location = await gateway().handle(
      nativeRequest({
        context,
        service: 's3',
        method: 'GET',
        path: `/${bucket}?location`,
      }),
      context.core
    );
    expect(await responseText(location)).toContain('us-east-1');

    const deleted = await gateway().handle(
      nativeRequest({
        context,
        service: 's3',
        method: 'DELETE',
        path: `/${bucket}/native.txt`,
      }),
      context.core
    );
    expect(deleted.status).toBe(204);
  });

  it('実 AWS credential と署名されていない routing context を拒否する', async () => {
    const context = await createContext();
    const attempts = [
      nativeRequest({
        context,
        service: 'ssm',
        accessKeyId: 'AKIA',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'ssm',
        accessKeyId: 'ASIA',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'ssm',
        accessKeyId: 'TCSIMWRONGACCESS01',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'ssm',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
        mutateHeaders: (headers) => headers.set('x-amz-security-token', 'real'),
      }),
      nativeRequest({
        context,
        service: 'ssm',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
        mutateHeaders: (headers) => {
          headers.set(
            'authorization',
            String(headers.get('authorization')).replace(
              `;${AWS_NATIVE_WORLD_HEADER}`,
              ''
            )
          );
        },
      }),
      nativeRequest({
        context,
        service: 'ssm',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
        mutateHeaders: (headers) => {
          headers.set(
            'authorization',
            String(headers.get('authorization')).replace(';x-amz-target', '')
          );
        },
      }),
    ];
    for (const request of attempts) {
      const error = await captureGatewayError(() =>
        gateway().translate(request)
      );
      expect(error.code).toBe('UnauthorizedOperation');
      expect(error.status).toBe(403);
    }
  });

  it('origin option body size SigV4 scope と protocol 境界を検証する', async () => {
    const context = await createContext();
    expect(
      () =>
        new AwsNativeGateway({
          simulatorOrigin: 'not-a-url',
          simulatorAccessKeyId: ACCESS_KEY_ID,
        })
    ).toThrow('simulatorOrigin');
    expect(
      () =>
        new AwsNativeGateway({
          simulatorOrigin: `${ORIGIN}/path`,
          simulatorAccessKeyId: ACCESS_KEY_ID,
        })
    ).toThrow('only an origin');
    expect(
      () =>
        new AwsNativeGateway({
          simulatorOrigin: ORIGIN,
          simulatorAccessKeyId: 'AKIA',
        })
    ).toThrow('simulator-owned');
    expect(() => gateway(0)).toThrow('maxBodyBytes');
    expect(() => gateway(1024 * 1024 + 1)).toThrow('maxBodyBytes');

    const invalidRequests = [
      nativeRequest({
        context,
        service: 'ssm',
        origin: 'https://real.amazonaws.com',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'ssm',
        region: 'bad_region',
        target: 'AmazonSSM.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'ssm',
        target: 'Wrong.GetParameter',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'ssm',
        target: 'AmazonSSM.UnknownOperation',
        body: '{}',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'ssm',
        target: 'AmazonSSM.GetParameter',
        body: '{',
        contentType: 'application/x-amz-json-1.1',
      }),
      nativeRequest({
        context,
        service: 'iam',
        method: 'GET',
        body: queryBody('GetRole', '2010-05-08'),
        contentType: 'application/x-www-form-urlencoded',
      }),
    ];
    for (const request of invalidRequests) {
      const response = await gateway().handle(request, context.core);
      expect(response.status).toBeGreaterThanOrEqual(400);
    }

    const oversized = nativeRequest({
      context,
      service: 'ssm',
      target: 'AmazonSSM.GetParameter',
      body: JSON.stringify({ Name: 'x'.repeat(256) }),
      contentType: 'application/x-amz-json-1.1',
    });
    expect((await gateway(32).handle(oversized, context.core)).status).toBe(
      413
    );
  });

  it('各 protocol が reducer error を AWS 形式の失敗応答へ変換する', async () => {
    const context = await createContext();
    const cases = [
      {
        request: nativeRequest({
          context,
          service: 'ssm',
          target: 'AmazonSSM.GetParameter',
          body: JSON.stringify({ Name: '/missing' }),
          contentType: 'application/x-amz-json-1.1',
        }),
        expectedType: 'application/x-amz-json-1.1',
        expectedBody: '__type',
      },
      {
        request: nativeRequest({
          context,
          service: 'iam',
          body: queryBody('GetRole', '2010-05-08', { RoleName: 'missing' }),
          contentType: 'application/x-www-form-urlencoded',
        }),
        expectedType: 'xml',
        expectedBody:
          '<Response xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><Errors>',
      },
      {
        request: nativeRequest({
          context,
          service: 's3',
          method: 'GET',
          path: '/tc-fixture-bucket-000000000000/missing.txt',
        }),
        expectedType: 'xml',
        expectedBody: '<Code>NoSuchKey</Code>',
      },
    ];
    for (const item of cases) {
      const response = await gateway().handle(item.request, context.core);
      expect(response.status).toBeGreaterThanOrEqual(400);
      expect(response.headers.get('content-type')).toContain(item.expectedType);
      expect(await responseText(response)).toContain(item.expectedBody);
    }
  });

  it('STS reducer は simulator key だけを caller identity として扱う', async () => {
    const context = await createContext();
    expect(() =>
      execute(context, 'sts', 'GetCallerIdentity', {
        AccessKeyId: 'AKIA',
      })
    ).toThrow('simulator-owned access key');
    const provider = new AwsProvider();
    expect(() =>
      provider.reduce(
        {
          worldId: context.worldId,
          deploymentId: context.deploymentId,
          service: 'sts',
          operation: 'AssumeRole',
          resourceType: '*',
          input: {},
        },
        {
          world: context.core.world(context.worldId),
          resources: context.store.resources(context.worldId),
        }
      )
    ).toThrow('STS operation AssumeRole');
  });
});
