import { describe, expect, it } from 'bun:test';
import type {
  ProviderCommandInput,
  ProviderWorldView,
  ResourceRecord,
} from '@tenkacloud/simulator-core';
import { reduceElb } from '../src/elb';
import {
  isDeployedFunctionUrl,
  reduceHttp,
  validatedLambdaHttpResponse,
} from '../src/http';

const SQLI_AUTH_PROBE = 'redteam/probes/sqli-auth-bypass.sh';
const SQLI_UNION_PROBE = 'redteam/probes/sqli-data-exfil.sh';
const AVAILABILITY_PROBE = 'redteam/probes/availability-flood.sh';
const ANONYMOUS_SPAM_PROBE = 'redteam/probes/anon-spam.sh';

function endpoint(
  slot: string,
  problemId: string,
  overrideUrl: string | null = 'https://participant.example.test/',
  targetId = 'default'
): ResourceRecord {
  return {
    worldId: 'world',
    deploymentId: 'deployment',
    targetId,
    provider: 'aws',
    resourceType: 'Runtime::Endpoint',
    resourceId: `endpoint-${slot}-${problemId}`,
    status: 'ready',
    properties: {
      logicalId: `RuntimeEndpoint.${slot}`,
      physicalId: `stack:${slot}`,
      refValue: slot,
      dependsOn: [],
      attributes: {},
      templateProperties: {},
      status: 'CREATE_COMPLETE',
      Slot: slot,
      ProblemId: problemId,
      TargetId: targetId,
      state: { overrideUrl },
    },
  };
}

function instance(
  state: Readonly<Record<string, unknown>> = {}
): ResourceRecord {
  return {
    worldId: 'world',
    deploymentId: 'deployment',
    targetId: 'default',
    provider: 'aws',
    resourceType: 'AWS::EC2::Instance',
    resourceId: `instance-${JSON.stringify(state)}`,
    status: 'ready',
    properties: {
      logicalId: 'Instance',
      physicalId: 'i-fixture',
      refValue: 'i-fixture',
      dependsOn: [],
      attributes: {},
      templateProperties: {},
      status: 'CREATE_COMPLETE',
      state: {
        instanceState: 'running',
        loadActive: false,
        sqliParameterized: false,
        rateLimitEnabled: false,
        authRequired: false,
        authTokenConfigured: false,
        services: { nginx: 'running', 'tenkacloud-vibe': 'running' },
        ...state,
      },
    },
  };
}

function world(resources: readonly ResourceRecord[]): ProviderWorldView {
  return {
    world: {
      worldId: 'world',
      tenantId: 'tenant',
      eventId: 'event',
      teamId: 'team',
      deploymentId: 'deployment',
      seed: 'seed',
      virtualTime: '2026-07-12T00:00:00.000Z',
      status: 'active',
    },
    resources,
  };
}

function command(
  input: Readonly<Record<string, unknown>>,
  operation = 'AttackProbe'
): ProviderCommandInput {
  return {
    worldId: 'world',
    deploymentId: 'deployment',
    service: 'http',
    operation,
    resourceType: 'HTTP::Endpoint',
    input: { TargetId: 'default', ...input },
  };
}

function securityResources(
  state: Readonly<Record<string, unknown>> = {}
): readonly ResourceRecord[] {
  return [
    endpoint('frontend', 'security-battle-royale'),
    endpoint('api', 'security-battle-royale'),
    instance(state),
  ];
}

function awsResource(
  resourceType: string,
  logicalId: string,
  refValue: string,
  templateProperties: Readonly<Record<string, unknown>>,
  attributes: Readonly<Record<string, unknown>> = {}
): ResourceRecord {
  return {
    worldId: 'world',
    deploymentId: 'deployment',
    targetId: 'default',
    provider: 'aws',
    resourceType,
    resourceId: `resource-${logicalId}`,
    status: 'ready',
    properties: {
      logicalId,
      physicalId: refValue,
      refValue,
      dependsOn: [],
      attributes,
      templateProperties,
      status: 'CREATE_COMPLETE',
      state:
        resourceType === 'AWS::Lambda::Function' ? { invocationCount: 0 } : {},
    },
  };
}

function requestResources(): readonly ResourceRecord[] {
  const functionArn =
    'arn:aws:lambda:us-east-1:123456789012:function:tc-query-search';
  const targetGroupArn =
    'arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/search';
  const listenerArn =
    'arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/search';
  return [
    awsResource(
      'AWS::Lambda::Function',
      'SearchFunction',
      'tc-query-search',
      {
        Runtime: 'nodejs22.x',
        Handler: 'index.handler',
        Role: 'arn:aws:iam::123456789012:role/query',
        Environment: { Variables: { FLAG: 'TC{query-fixture}' } },
      },
      { Arn: functionArn }
    ),
    awsResource(
      'AWS::ElasticLoadBalancingV2::TargetGroup',
      'SearchTargetGroup',
      targetGroupArn,
      { TargetType: 'lambda', Targets: [{ Id: functionArn }] },
      { Arn: targetGroupArn }
    ),
    awsResource(
      'AWS::ElasticLoadBalancingV2::Listener',
      'AlbListener',
      listenerArn,
      {
        Port: 80,
        Protocol: 'HTTP',
        DefaultActions: [
          {
            Type: 'fixed-response',
            FixedResponseConfig: {
              StatusCode: '405',
              ContentType: 'text/plain',
              MessageBody: 'QUERY is blocked at the edge',
            },
          },
        ],
      },
      { Arn: listenerArn }
    ),
    awsResource(
      'AWS::ElasticLoadBalancingV2::ListenerRule',
      'AllowedMethodsRule',
      `${listenerArn}/rule/allowed`,
      {
        ListenerArn: listenerArn,
        Priority: 10,
        Conditions: [
          {
            Field: 'http-request-method',
            HttpRequestMethodConfig: {
              Values: ['GET', 'HEAD', 'POST', 'OPTIONS'],
            },
          },
          {
            Field: 'path-pattern',
            PathPatternConfig: { Values: ['/search'] },
          },
        ],
        Actions: [{ Type: 'forward', TargetGroupArn: targetGroupArn }],
      }
    ),
  ];
}

const FUNCTION_URL = 'https://hello-fixture.lambda-url.us-east-1.on.aws/';

function functionUrlResources(): readonly ResourceRecord[] {
  const functionArn = 'arn:aws:lambda:us-east-1:123456789012:function:tc-hello';
  return [
    awsResource(
      'AWS::Lambda::Function',
      'HelloFunction',
      'tc-hello',
      {
        Runtime: 'python3.12',
        Handler: 'index.handler',
        Role: 'arn:aws:iam::123456789012:role/hello',
      },
      { Arn: functionArn }
    ),
    awsResource(
      'AWS::Lambda::Url',
      'HelloFunctionUrl',
      'function-url-fixture',
      { TargetFunctionArn: functionArn, AuthType: 'NONE' },
      { FunctionUrl: FUNCTION_URL }
    ),
    awsResource(
      'AWS::Lambda::Permission',
      'HelloFunctionUrlPublicAccess',
      'function-url-permission-fixture',
      {
        FunctionName: functionArn,
        Action: 'lambda:InvokeFunctionUrl',
        Principal: '*',
        FunctionUrlAuthType: 'NONE',
      }
    ),
    awsResource(
      'AWS::Lambda::Permission',
      'HelloFunctionPublicInvoke',
      'function-invoke-permission-fixture',
      {
        FunctionName: functionArn,
        Action: 'lambda:InvokeFunction',
        Principal: '*',
        InvokedViaFunctionUrl: true,
      }
    ),
  ];
}

function resourceByLogicalId(
  resources: readonly ResourceRecord[],
  logicalId: string
): ResourceRecord {
  const resource = resources.find(
    (candidate) => candidate.properties['logicalId'] === logicalId
  );
  if (!resource) throw new Error(`resource ${logicalId} is missing`);
  return resource;
}

function templateOf(
  resource: ResourceRecord
): Readonly<Record<string, unknown>> {
  const value = resource.properties['templateProperties'];
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('templateProperties is not an object');
  }
  return Object.fromEntries(Object.entries(value));
}

function patchTemplate(
  resources: readonly ResourceRecord[],
  logicalId: string,
  patch: Readonly<Record<string, unknown>>
): readonly ResourceRecord[] {
  return resources.map((resource) =>
    resource.properties['logicalId'] === logicalId
      ? {
          ...resource,
          properties: {
            ...resource.properties,
            templateProperties: { ...templateOf(resource), ...patch },
          },
        }
      : resource
  );
}

function cloneResource(
  resources: readonly ResourceRecord[],
  logicalId: string,
  resourceId: string,
  patch: Readonly<Record<string, unknown>> = {}
): ResourceRecord {
  const resource = resourceByLogicalId(resources, logicalId);
  return {
    ...resource,
    resourceId,
    properties: {
      ...resource.properties,
      templateProperties: { ...templateOf(resource), ...patch },
    },
  };
}

function validRequest(
  patch: Readonly<Record<string, unknown>> = {}
): Readonly<Record<string, unknown>> {
  return {
    Method: 'GET',
    Path: '/search',
    Headers: { 'content-type': 'application/json' },
    Body: '',
    ...patch,
  };
}

describe('AWS HTTP attack probe reducer', () => {
  it('catalog SQLi script 2種を実際のpayloadへ展開する', () => {
    for (const Probe of [SQLI_AUTH_PROBE, SQLI_UNION_PROBE]) {
      expect(
        reduceHttp(command({ Probe }), world(securityResources())).response
      ).toMatchObject({
        Slot: 'api',
        StatusCode: 200,
        Vulnerable: true,
      });
    }
  });

  it('SQLi probeのbody method path slotと稼働状態を厳密に検証する', () => {
    const validBody = JSON.stringify({
      username: "' OR '1'='1' -- ",
      password: 'x',
    });
    const invalidInputs = [
      { Slot: 'frontend', Body: validBody },
      { Slot: 'api', Method: 'GET', Path: '/api/v1/auth', Body: validBody },
      { Slot: 'api', Method: 'POST', Path: '/other', Body: validBody },
      { Slot: 'api', Body: '{' },
      { Slot: 'api', Body: 'x'.repeat(64 * 1024 + 1) },
      {
        Slot: 'api',
        Body: JSON.stringify({ username: 'admin', password: 'x' }),
      },
    ];
    for (const input of invalidInputs) {
      expect(() =>
        reduceHttp(command(input), world(securityResources()))
      ).toThrow();
    }
    expect(() =>
      reduceHttp(
        command({ Slot: 'api', Body: validBody }),
        world(securityResources({ loadActive: true }))
      )
    ).toThrow('saturated');
    expect(() =>
      reduceHttp(
        command({ Slot: 'api', Body: validBody }),
        world([
          endpoint('api', 'security-battle-royale'),
          instance({ instanceState: 'stopped' }),
        ])
      )
    ).toThrow('EC2 workload is unavailable');
  });

  it('endpoint登録 target EC2 cardinalityを曖昧なまま成功にしない', () => {
    const input = { Probe: SQLI_AUTH_PROBE };
    const cases: readonly [readonly ResourceRecord[], string][] = [
      [
        [endpoint('api', 'security-battle-royale', null), instance()],
        'unavailable',
      ],
      [
        [
          endpoint('api', 'security-battle-royale', undefined, 'other'),
          instance(),
        ],
        'does not exist',
      ],
      [
        [endpoint('api', 'security-battle-royale')],
        'EC2 workload does not exist',
      ],
      [
        [
          endpoint('api', 'security-battle-royale'),
          instance(),
          { ...instance(), resourceId: 'instance-second' },
        ],
        'exactly one EC2 workload',
      ],
    ];
    for (const [resources, message] of cases) {
      expect(() => reduceHttp(command(input), world(resources))).toThrow(
        message
      );
    }
  });

  it('availability probeは2slotを要求し停止中nginxを成功扱いしない', () => {
    expect(() =>
      reduceHttp(
        command({ Probe: AVAILABILITY_PROBE, Slot: 'api' }),
        world(securityResources())
      )
    ).toThrow('covers frontend and api');
    expect(() =>
      reduceHttp(
        command({ Probe: AVAILABILITY_PROBE }),
        world(
          securityResources({
            services: { nginx: 'stopped', 'tenkacloud-vibe': 'running' },
          })
        )
      )
    ).toThrow('frontend workload is unavailable');
  });

  it('anonymous spamは停止中appを成功扱いしない', () => {
    expect(() =>
      reduceHttp(
        command({ Probe: ANONYMOUS_SPAM_PROBE }),
        world([
          endpoint('app', 'stackstack'),
          instance({
            services: {
              nginx: 'running',
              'tenkacloud-vibe': 'stopped',
            },
          }),
        ])
      )
    ).toThrow('app workload is unavailable');
  });

  it('availabilityとanonymous spamの防御前後をstate更新まで返す', () => {
    expect(
      reduceHttp(
        command({ Probe: AVAILABILITY_PROBE }),
        world(securityResources())
      ).response
    ).toMatchObject({ StatusCode: 503, Vulnerable: true, Landed: true });
    expect(
      reduceHttp(
        command({ Probe: AVAILABILITY_PROBE }),
        world(securityResources({ rateLimitEnabled: true }))
      ).response
    ).toMatchObject({ StatusCode: 200, Vulnerable: false, Landed: false });

    const vulnerable = reduceHttp(
      command({ Probe: ANONYMOUS_SPAM_PROBE }),
      world([endpoint('app', 'stackstack'), instance()])
    );
    expect(vulnerable.response).toMatchObject({
      StatusCode: 201,
      Vulnerable: true,
      LandedPosts: 5,
    });
    expect(vulnerable.resources).toHaveLength(1);
    expect(vulnerable.resources[0]?.properties['state']).toMatchObject({
      boardClean: false,
    });

    const defended = reduceHttp(
      command({ Probe: ANONYMOUS_SPAM_PROBE }),
      world([
        endpoint('app', 'stackstack'),
        instance({ authRequired: true, authTokenConfigured: true }),
      ])
    );
    expect(defended.response).toMatchObject({
      StatusCode: 401,
      Vulnerable: false,
      LandedPosts: 0,
    });
    expect(defended.resources).toHaveLength(0);
  });

  it('HTTP Requestがlistener rule変更前後のedgeとLambda data planeを再現する', () => {
    const input = {
      Method: 'QUERY',
      Path: '/search',
      Headers: { 'content-type': 'application/json' },
      Body: JSON.stringify({ query: { match: 'tenka' } }),
    };
    const initial = requestResources();
    expect(
      reduceHttp(command(input, 'Request'), world(initial)).response
    ).toEqual({
      StatusCode: 405,
      Headers: { 'content-type': 'text/plain' },
      Body: 'QUERY is blocked at the edge',
    });

    const rule = initial.find(
      (resource) => resource.properties['logicalId'] === 'AllowedMethodsRule'
    );
    if (!rule) throw new Error('listener rule fixture is missing');
    const modified = reduceElb(
      {
        ...command({}, 'ModifyRule'),
        service: 'elasticloadbalancing',
        input: {
          RuleArn: rule.properties['refValue'],
          Conditions: [
            {
              Field: 'http-request-method',
              HttpRequestMethodConfig: {
                Values: ['GET', 'HEAD', 'POST', 'OPTIONS', 'QUERY'],
              },
            },
            {
              Field: 'path-pattern',
              PathPatternConfig: { Values: ['/search'] },
            },
          ],
        },
      },
      world(initial)
    ).resources[0];
    if (!modified) throw new Error('modified listener rule is missing');
    const next = initial.map((resource) =>
      resource.resourceId === modified.resourceId
        ? { ...resource, properties: modified.properties }
        : resource
    );
    const response = reduceHttp(command(input, 'Request'), world(next));
    expect(response.response['StatusCode']).toBe(200);
    expect(response.response['Body']).toContain('TC{query-fixture}');
    expect(response.resources).toHaveLength(1);
    expect(response.events.map((event) => event.type)).toEqual([
      'AwsLambdaFunctionInvoked',
      'AwsHttpRequestExecuted',
    ]);
  });

  it('Lambda Function URLをALBなしで解決しRequestとProbeをhandlerへdispatchする', () => {
    const resources = functionUrlResources();
    expect(
      isDeployedFunctionUrl(
        command({ Url: FUNCTION_URL }, 'Probe'),
        world(resources)
      )
    ).toBe(true);
    expect(isDeployedFunctionUrl(command({}, 'Probe'), world(resources))).toBe(
      false
    );
    expect(
      isDeployedFunctionUrl(
        command({ Url: 'not-a-url' }, 'Probe'),
        world(resources)
      )
    ).toBe(false);
    expect(
      isDeployedFunctionUrl(
        command({ Url: FUNCTION_URL }, 'Probe'),
        world(
          resources.map((resource) =>
            resource.properties['logicalId'] === 'HelloFunctionUrl'
              ? {
                  ...resource,
                  properties: { ...resource.properties, attributes: {} },
                }
              : resource
          )
        )
      )
    ).toBe(false);
    const request = reduceHttp(
      command({ ...validRequest({ Path: '/' }), Url: FUNCTION_URL }, 'Request'),
      world(resources)
    );
    const defaultRequest = reduceHttp(
      command(
        {
          Url: `${FUNCTION_URL}hello?source=request&space=%20&plus=+&slash=%2f%2F`,
        },
        'Request'
      ),
      world(resources)
    );
    const implicit = reduceHttp(
      command(validRequest({ Path: '/' }), 'Request'),
      world(resources)
    );
    const probe = reduceHttp(
      command({ Url: `${FUNCTION_URL}hello?source=scorer` }, 'Probe'),
      world(resources)
    );

    expect(request.response).toMatchObject({ StatusCode: 200 });
    expect(defaultRequest.response).toMatchObject({ StatusCode: 200 });
    expect(defaultRequest.events.at(-1)?.payload).toMatchObject({
      method: 'GET',
      path: '/hello',
      rawQueryString: 'source=request&space=%20&plus=+&slash=%2f%2F',
    });
    expect(implicit.response).toMatchObject({ StatusCode: 200 });
    expect(probe.response).toMatchObject({
      Ok: true,
      StatusCode: 200,
      Truncated: false,
      ResponseTimeMilliseconds: 0,
    });
    expect(probe.events.at(-1)?.payload).toMatchObject({
      endpointType: 'lambda-function-url',
      path: '/hello',
      rawQueryString: 'source=scorer',
      url: FUNCTION_URL,
    });
  });

  it('ALBとFunction URLが共存するときURLなしRequestは従来のlistener経路を維持する', () => {
    const resources = [...requestResources(), ...functionUrlResources()];
    const alb = reduceHttp(
      command(
        validRequest({
          Method: 'QUERY',
          Body: JSON.stringify({ query: { match: 'tenkacloud' } }),
        }),
        'Request'
      ),
      world(resources)
    );
    const functionUrl = reduceHttp(
      command({ ...validRequest({ Path: '/' }), Url: FUNCTION_URL }, 'Request'),
      world(resources)
    );

    expect(alb.response).toMatchObject({
      StatusCode: 405,
      Body: 'QUERY is blocked at the edge',
    });
    expect(functionUrl.response).toMatchObject({
      StatusCode: 200,
      Body: expect.stringContaining('hello-multicloud'),
    });
  });

  it('Lambda Function URLのURL形式とresource graph境界を曖昧なまま成功にしない', () => {
    const resources = functionUrlResources();
    for (const Url of [
      `https://example.test/${'x'.repeat(2048)}`,
      'not-a-url',
      'http://hello-fixture.lambda-url.us-east-1.on.aws/',
      'https://user:secret@hello-fixture.lambda-url.us-east-1.on.aws/',
      `${FUNCTION_URL}#fragment`,
    ]) {
      expect(() =>
        reduceHttp(
          command({ ...validRequest({ Path: '/' }), Url }, 'Request'),
          world(resources)
        )
      ).toThrow();
    }
    expect(() =>
      reduceHttp(
        command(
          { ...validRequest({ Path: '/' }), Url: 'https://unknown.example/' },
          'Request'
        ),
        world(resources)
      )
    ).toThrow('does not exist');
    expect(() =>
      reduceHttp(
        command(validRequest({ Path: '/' }), 'Request'),
        world([
          ...resources,
          cloneResource(resources, 'HelloFunctionUrl', 'second-function-url'),
        ])
      )
    ).toThrow('ambiguous');
  });

  it('Lambda Function URLの認証、target、permission、URL projectionを検証する', () => {
    const resources = functionUrlResources();
    const request = command(validRequest({ Path: '/' }), 'Request');
    const cases: readonly [readonly ResourceRecord[], string][] = [
      [
        patchTemplate(resources, 'HelloFunctionUrl', { AuthType: 'AWS_IAM' }),
        'unsupported AWS authentication',
      ],
      [
        resources.filter(
          (resource) => resource.properties['logicalId'] !== 'HelloFunction'
        ),
        'target is missing',
      ],
      [
        [
          ...resources,
          cloneResource(resources, 'HelloFunction', 'second-hello-function'),
        ],
        'target is missing or ambiguous',
      ],
      [
        resources.filter(
          (resource) =>
            resource.properties['logicalId'] !== 'HelloFunctionUrlPublicAccess'
        ),
        'public invoke permissions are missing',
      ],
      [
        resources.filter(
          (resource) =>
            resource.properties['logicalId'] !== 'HelloFunctionPublicInvoke'
        ),
        'public invoke permissions are missing',
      ],
      [
        [
          ...resources,
          cloneResource(
            resources,
            'HelloFunctionUrlPublicAccess',
            'second-function-url-permission'
          ),
        ],
        'public invoke permissions are missing or ambiguous',
      ],
      [
        [
          ...resources,
          cloneResource(
            resources,
            'HelloFunctionPublicInvoke',
            'second-function-invoke-permission'
          ),
        ],
        'public invoke permissions are missing or ambiguous',
      ],
      [
        patchTemplate(resources, 'HelloFunctionPublicInvoke', {
          InvokedViaFunctionUrl: false,
        }),
        'public invoke permissions are missing',
      ],
      [
        resources.map((resource) =>
          resource.properties['logicalId'] === 'HelloFunctionUrl'
            ? {
                ...resource,
                properties: { ...resource.properties, attributes: {} },
              }
            : resource
        ),
        'projection is invalid',
      ],
    ];
    for (const [caseResources, message] of cases) {
      expect(() => reduceHttp(request, world(caseResources))).toThrow(message);
    }
  });

  it('HTTP Requestのmethod path header body境界をstrictに拒否する', () => {
    const resources = requestResources();
    const tooManyHeaders = Object.fromEntries(
      Array.from({ length: 65 }, (_, index) => [`x-${index}`, 'value'])
    );
    const invalidInputs: readonly [
      Readonly<Record<string, unknown>>,
      string,
    ][] = [
      [validRequest({ Headers: tooManyHeaders }), 'too many headers'],
      [validRequest({ Headers: { 'bad header': 'value' } }), 'is invalid'],
      [validRequest({ Headers: { valid: 1 } }), 'is invalid'],
      [validRequest({ Headers: { valid: 'x'.repeat(8193) } }), 'is invalid'],
      [validRequest({ Headers: { valid: 'line\rbreak' } }), 'is invalid'],
      [validRequest({ Headers: { valid: 'line\nbreak' } }), 'is invalid'],
      [validRequest({ Method: 'BAD METHOD' }), 'Method is invalid'],
      [validRequest({ Path: 'relative' }), 'bounded origin-relative path'],
      [validRequest({ Path: '//authority' }), 'bounded origin-relative path'],
      [
        validRequest({ Path: `/${'x'.repeat(2048)}` }),
        'bounded origin-relative path',
      ],
      [validRequest({ Path: '/has space' }), 'bounded origin-relative path'],
      [
        validRequest({ Path: '/fragment#part' }),
        'bounded origin-relative path',
      ],
      [validRequest({ Body: 1 }), 'Body must be a string'],
      [validRequest({ Body: 'x'.repeat(64 * 1024 + 1) }), 'body is too long'],
    ];
    for (const [input, message] of invalidInputs) {
      expect(() =>
        reduceHttp(command(input, 'Request'), world(resources))
      ).toThrow(message);
    }

    const normalized = reduceHttp(
      command(
        validRequest({
          Method: 'query',
          Headers: { 'Content-Type': 'application/json' },
          Body: JSON.stringify({ query: { match: 'case-insensitive' } }),
        }),
        'Request'
      ),
      world(
        patchTemplate(resources, 'AllowedMethodsRule', {
          Conditions: [
            {
              Field: 'http-request-method',
              HttpRequestMethodConfig: { Values: ['QUERY'] },
            },
            {
              Field: 'path-pattern',
              PathPatternConfig: { Values: ['/se*'] },
            },
          ],
        })
      )
    );
    expect(normalized.response['StatusCode']).toBe(200);
  });

  it('Lambda HTTP responseのstatusとbody境界を独立に検証する', () => {
    expect(
      validatedLambdaHttpResponse({
        statusCode: 201,
        headers: { 'content-type': 'application/json' },
        body: '{}',
      })
    ).toEqual({
      statusCode: 201,
      headers: { 'content-type': 'application/json' },
      body: '{}',
    });
    for (const statusCode of [undefined, 'invalid', 99, 600]) {
      expect(() =>
        validatedLambdaHttpResponse({ statusCode, body: '' })
      ).toThrow('Lambda HTTP status is invalid');
    }
  });

  it('listener ruleのcondition action priority境界を曖昧なまま実行しない', () => {
    const resources = requestResources();
    const conditionCases: readonly [readonly ResourceRecord[], string][] = [
      [
        patchTemplate(resources, 'AllowedMethodsRule', { Conditions: {} }),
        'Conditions must be an array',
      ],
      [
        patchTemplate(resources, 'AllowedMethodsRule', {
          Conditions: [{ Field: 'host-header', Values: ['example.test'] }],
        }),
        'condition host-header is not implemented',
      ],
      [
        patchTemplate(resources, 'AllowedMethodsRule', {
          Conditions: [
            {
              Field: 'http-request-method',
              HttpRequestMethodConfig: { Values: [] },
            },
          ],
        }),
        'values are invalid',
      ],
      [
        patchTemplate(resources, 'AllowedMethodsRule', { Actions: [] }),
        'must contain exactly one action',
      ],
      [
        patchTemplate(resources, 'AllowedMethodsRule', { Actions: {} }),
        'Actions must be an array',
      ],
    ];
    for (const [candidate, message] of conditionCases) {
      expect(() =>
        reduceHttp(command(validRequest(), 'Request'), world(candidate))
      ).toThrow(message);
    }

    const invalidPriority = cloneResource(
      resources,
      'AllowedMethodsRule',
      'rule-invalid-priority',
      { Priority: 'not-a-number' }
    );
    for (const candidate of [
      [invalidPriority, ...resources],
      [...resources, invalidPriority],
    ]) {
      expect(() =>
        reduceHttp(command(validRequest(), 'Request'), world(candidate))
      ).toThrow('Priority is invalid');
    }

    const lowerPriority = cloneResource(
      resources,
      'AllowedMethodsRule',
      'rule-lower-priority',
      { Priority: 5 }
    );
    expect(
      reduceHttp(
        command(validRequest(), 'Request'),
        world([...resources, lowerPriority])
      ).response['StatusCode']
    ).toBe(200);
  });

  it('listener default actionとtarget projectionの不正cardinalityを拒否する', () => {
    const resources = requestResources();
    const defaultRequest = validRequest({ Method: 'QUERY' });
    const listener = resourceByLogicalId(resources, 'AlbListener');
    const defaultCases: readonly [readonly ResourceRecord[], string][] = [
      [
        resources.filter(
          (resource) => resource.properties['logicalId'] !== 'AlbListener'
        ),
        'exactly one listener',
      ],
      [
        [...resources, { ...listener, resourceId: 'listener-second' }],
        'exactly one listener',
      ],
      [
        patchTemplate(resources, 'AlbListener', { DefaultActions: [] }),
        'exactly one action',
      ],
      [
        patchTemplate(resources, 'AlbListener', {
          DefaultActions: [
            {
              Type: 'fixed-response',
              FixedResponseConfig: { StatusCode: '99' },
            },
          ],
        }),
        'fixed response status is invalid',
      ],
      [
        patchTemplate(resources, 'AlbListener', {
          DefaultActions: [{ Type: 'redirect' }],
        }),
        'action redirect is not implemented',
      ],
    ];
    for (const [candidate, message] of defaultCases) {
      expect(() =>
        reduceHttp(command(defaultRequest, 'Request'), world(candidate))
      ).toThrow(message);
    }
    expect(
      reduceHttp(
        command(defaultRequest, 'Request'),
        world(
          patchTemplate(resources, 'AlbListener', {
            DefaultActions: [
              {
                Type: 'fixed-response',
                FixedResponseConfig: { StatusCode: '204' },
              },
            ],
          })
        )
      ).response
    ).toEqual({
      StatusCode: 204,
      Headers: { 'content-type': 'text/plain' },
      Body: '',
    });

    const targetGroup = resourceByLogicalId(resources, 'SearchTargetGroup');
    const lambda = resourceByLogicalId(resources, 'SearchFunction');
    const forwardCases: readonly [readonly ResourceRecord[], string][] = [
      [
        patchTemplate(resources, 'AllowedMethodsRule', {
          Actions: [{ Type: 'forward', TargetGroupArn: 'missing' }],
        }),
        'target group is missing or ambiguous',
      ],
      [
        [...resources, { ...targetGroup, resourceId: 'target-group-second' }],
        'target group is missing or ambiguous',
      ],
      [
        patchTemplate(resources, 'SearchTargetGroup', {
          TargetType: 'instance',
        }),
        'not a Lambda target',
      ],
      [
        patchTemplate(resources, 'SearchTargetGroup', { Targets: [] }),
        'exactly one Lambda target',
      ],
      [
        patchTemplate(resources, 'SearchTargetGroup', {
          Targets: [{ Id: 'one' }, { Id: 'two' }],
        }),
        'exactly one Lambda target',
      ],
      [
        resources.filter(
          (resource) => resource.properties['logicalId'] !== 'SearchFunction'
        ),
        'Lambda target is missing or ambiguous',
      ],
      [
        [...resources, { ...lambda, resourceId: 'lambda-second' }],
        'Lambda target is missing or ambiguous',
      ],
    ];
    for (const [candidate, message] of forwardCases) {
      expect(() =>
        reduceHttp(command(validRequest(), 'Request'), world(candidate))
      ).toThrow(message);
    }

    const queryForward = patchTemplate(resources, 'AllowedMethodsRule', {
      Conditions: [
        {
          Field: 'http-request-method',
          HttpRequestMethodConfig: { Values: ['QUERY'] },
        },
        {
          Field: 'path-pattern',
          PathPatternConfig: { Values: ['/search'] },
        },
      ],
    });
    const oversizedLambdaBody = patchTemplate(queryForward, 'SearchFunction', {
      Environment: { Variables: { FLAG: 'x'.repeat(64 * 1024) } },
    });
    expect(() =>
      reduceHttp(
        command(
          validRequest({
            Method: 'QUERY',
            Body: JSON.stringify({ query: { match: 'oversized' } }),
          }),
          'Request'
        ),
        world(oversizedLambdaBody)
      )
    ).toThrow('Lambda HTTP body is invalid');
  });

  it('未知operationとprobeをloudに拒否する', () => {
    expect(() => reduceHttp(command({}, 'Unknown'), world([]))).toThrow(
      'HTTP operation Unknown'
    );
    expect(() =>
      reduceHttp(command({ Probe: 'unknown.sh' }), world([]))
    ).toThrow('HTTP attack probe unknown.sh');
  });
});
