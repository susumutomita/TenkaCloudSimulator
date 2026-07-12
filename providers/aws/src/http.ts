import {
  CoreError,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceRecord,
} from '@tenkacloud/simulator-core';
import { reduceLambda } from './lambda';
import { RUNTIME_ENDPOINT_RESOURCE } from './model';
import {
  resourcesForDeployment,
  result,
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import { objectValue, optionalString, stringValue } from './value';

const INSTANCE_RESOURCE = 'AWS::EC2::Instance';
const LISTENER_RESOURCE = 'AWS::ElasticLoadBalancingV2::Listener';
const RULE_RESOURCE = 'AWS::ElasticLoadBalancingV2::ListenerRule';
const TARGET_GROUP_RESOURCE = 'AWS::ElasticLoadBalancingV2::TargetGroup';
const LAMBDA_RESOURCE = 'AWS::Lambda::Function';
const SQLI_AUTH_PROBE = 'redteam/probes/sqli-auth-bypass.sh';
const SQLI_UNION_PROBE = 'redteam/probes/sqli-data-exfil.sh';
const AVAILABILITY_PROBE = 'redteam/probes/availability-flood.sh';
const ANONYMOUS_SPAM_PROBE = 'redteam/probes/anon-spam.sh';
const MAX_PROBE_BODY_LENGTH = 64 * 1024;

function endpointFor(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  slot: string,
  targetId: string
): void {
  const resource = resourcesForDeployment(
    world,
    command.deploymentId,
    RUNTIME_ENDPOINT_RESOURCE
  ).find(
    (candidate) =>
      candidate.properties['Slot'] === slot &&
      candidate.properties['TargetId'] === targetId
  );
  if (!resource) {
    throw new CoreError(
      'NotFound',
      `HTTP endpoint slot ${slot} does not exist`
    );
  }
  const properties = storedProperties(resource);
  if (typeof stateObject(properties)['overrideUrl'] !== 'string') {
    throw new CoreError(
      'Conflict',
      `HTTP endpoint slot ${slot} workload is unavailable`
    );
  }
}

function instanceFor(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ResourceRecord {
  const instances = resourcesForDeployment(
    world,
    command.deploymentId,
    INSTANCE_RESOURCE
  );
  let instance: ResourceRecord | undefined;
  for (const candidate of instances) {
    if (instance) {
      throw new CoreError(
        'Conflict',
        'HTTP probe requires exactly one EC2 workload'
      );
    }
    instance = candidate;
  }
  if (!instance) {
    throw new CoreError('NotFound', 'HTTP probe EC2 workload does not exist');
  }
  const state = stateObject(storedProperties(instance));
  if (state['instanceState'] !== 'running') {
    throw new CoreError('Conflict', 'HTTP probe EC2 workload is unavailable');
  }
  return instance;
}

function requestedSlot(
  command: ProviderCommandInput,
  expected: string
): string {
  const supplied = optionalString(command.input['Slot'], 'Slot');
  if (supplied !== undefined && supplied !== expected) {
    throw new CoreError(
      'ValidationFailed',
      `HTTP probe requires endpoint slot ${expected}`
    );
  }
  return expected;
}

function sqlProbeBody(probe: string): string {
  return probe === SQLI_UNION_PROBE
    ? JSON.stringify({
        username: "admin' UNION SELECT username FROM username -- ",
        password: 'x',
      })
    : JSON.stringify({
        username: "admin' OR '1'='1' -- ",
        password: 'x',
      });
}

function sqlVariant(body: string): 'auth-bypass' | 'union-select' {
  if (body.length > MAX_PROBE_BODY_LENGTH) {
    throw new CoreError('QuotaExceeded', 'HTTP attack probe body is too long');
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    throw new CoreError(
      'ValidationFailed',
      'HTTP SQLi probe body must be valid JSON'
    );
  }
  const input = objectValue(parsed, 'HTTP SQLi probe body');
  const username = stringValue(input['username'], 'HTTP SQLi username');
  stringValue(input['password'], 'HTTP SQLi password');
  if (/\bUNION\s+SELECT\b/i.test(username)) return 'union-select';
  if (/['"]\s+OR\s+['"]?1['"]?\s*=\s*['"]?1/i.test(username)) {
    return 'auth-bypass';
  }
  throw new CoreError(
    'UnsupportedCapability',
    'HTTP auth request is not a catalog SQLi attack probe'
  );
}

function sqlAttackProbe(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  targetId: string,
  probe?: string
): ProviderCommandResult {
  const slot = requestedSlot(command, 'api');
  endpointFor(command, world, slot, targetId);
  const method = optionalString(command.input['Method'], 'Method') ?? 'POST';
  const path = optionalString(command.input['Path'], 'Path') ?? '/api/v1/auth';
  if (method !== 'POST' || path !== '/api/v1/auth') {
    throw new CoreError(
      'UnsupportedCapability',
      `HTTP SQLi probe ${method} ${path} is not supported`
    );
  }
  const body =
    probe === undefined
      ? stringValue(command.input['Body'], 'Body')
      : sqlProbeBody(probe);
  const variant = sqlVariant(body);
  const instance = instanceFor(command, world);
  const state = stateObject(storedProperties(instance));
  if (state['loadActive'] === true) {
    throw new CoreError('Conflict', 'HTTP SQLi workload is saturated');
  }
  const vulnerable = state['sqliParameterized'] !== true;
  return result('AwsHttpAttackProbeExecuted', {
    Slot: slot,
    Method: method,
    Path: path,
    Probe: probe ?? variant,
    StatusCode: vulnerable ? 200 : 403,
    Vulnerable: vulnerable,
    Landed: vulnerable,
  });
}

function availabilityAttackProbe(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  targetId: string
): ProviderCommandResult {
  const suppliedSlot = optionalString(command.input['Slot'], 'Slot');
  if (suppliedSlot !== undefined) {
    throw new CoreError(
      'ValidationFailed',
      'availability probe covers frontend and api slots together'
    );
  }
  endpointFor(command, world, 'frontend', targetId);
  endpointFor(command, world, 'api', targetId);
  const instance = instanceFor(command, world);
  const state = stateObject(storedProperties(instance));
  const services = objectValue(state['services'] ?? {}, 'instance services');
  if (services['nginx'] === 'stopped') {
    throw new CoreError('Conflict', 'HTTP frontend workload is unavailable');
  }
  const vulnerable = state['rateLimitEnabled'] !== true;
  return result('AwsHttpAttackProbeExecuted', {
    Slots: ['frontend', 'api'],
    Method: 'GET',
    Probe: AVAILABILITY_PROBE,
    StatusCode: vulnerable ? 503 : 200,
    Vulnerable: vulnerable,
    Landed: vulnerable,
  });
}

function anonymousSpamAttackProbe(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  targetId: string
): ProviderCommandResult {
  const slot = requestedSlot(command, 'app');
  endpointFor(command, world, slot, targetId);
  const instance = instanceFor(command, world);
  const properties = storedProperties(instance);
  const state = stateObject(properties);
  const services = objectValue(state['services'] ?? {}, 'instance services');
  if (services['tenkacloud-vibe'] === 'stopped') {
    throw new CoreError('Conflict', 'HTTP app workload is unavailable');
  }
  const defended =
    state['authRequired'] === true && state['authTokenConfigured'] === true;
  const nextResources = defended
    ? []
    : [
        updateStoredResource(instance, {
          state: { ...state, boardClean: false },
        }),
      ];
  return result(
    'AwsHttpAttackProbeExecuted',
    {
      Slot: slot,
      Method: 'POST',
      Path: '/submit',
      Probe: ANONYMOUS_SPAM_PROBE,
      StatusCode: defended ? 401 : 201,
      Vulnerable: !defended,
      Landed: !defended,
      LandedPosts: defended ? 0 : 5,
    },
    nextResources
  );
}

interface HttpRequest {
  readonly method: string;
  readonly path: string;
  readonly headers: Readonly<Record<string, string>>;
  readonly body: string;
}

function requestHeaders(value: unknown): Readonly<Record<string, string>> {
  const headers = objectValue(value ?? {}, 'Headers');
  if (Object.keys(headers).length > 64) {
    throw new CoreError('QuotaExceeded', 'HTTP request has too many headers');
  }
  return Object.fromEntries(
    Object.entries(headers).map(([name, entry]) => {
      if (
        !/^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$/.test(name) ||
        typeof entry !== 'string' ||
        entry.length > 8192 ||
        entry.includes('\r') ||
        entry.includes('\n')
      ) {
        throw new CoreError(
          'ValidationFailed',
          `HTTP request header ${name} is invalid`
        );
      }
      return [name.toLowerCase(), entry];
    })
  );
}

function httpRequest(command: ProviderCommandInput): HttpRequest {
  const method = stringValue(command.input['Method'], 'Method').toUpperCase();
  if (!/^[A-Z][A-Z0-9-]{0,31}$/.test(method)) {
    throw new CoreError('ValidationFailed', 'HTTP Method is invalid');
  }
  const path = stringValue(command.input['Path'], 'Path');
  if (
    !path.startsWith('/') ||
    path.startsWith('//') ||
    path.length > 2048 ||
    /[\s#]/.test(path)
  ) {
    throw new CoreError(
      'ValidationFailed',
      'HTTP Path must be a bounded origin-relative path'
    );
  }
  const body = command.input['Body'] ?? '';
  if (typeof body !== 'string') {
    throw new CoreError('ValidationFailed', 'HTTP Body must be a string');
  }
  if (body.length > MAX_PROBE_BODY_LENGTH) {
    throw new CoreError('QuotaExceeded', 'HTTP request body is too long');
  }
  return {
    method,
    path,
    headers: requestHeaders(command.input['Headers']),
    body,
  };
}

function objectArray(
  value: unknown,
  label: string
): readonly Readonly<Record<string, unknown>>[] {
  if (!Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an array`);
  }
  return value.map((entry, index) => objectValue(entry, `${label}[${index}]`));
}

function isSingleton<T>(values: readonly T[]): values is readonly [T] {
  return values.length === 1;
}

function wildcardPattern(pattern: string): RegExp {
  const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(`^${escaped.replaceAll('*', '.*')}$`);
}

function conditionMatches(
  condition: Readonly<Record<string, unknown>>,
  request: HttpRequest
): boolean {
  const field = stringValue(condition['Field'], 'listener rule Field');
  let configuration: Readonly<Record<string, unknown>>;
  let candidate: string;
  if (field === 'http-request-method') {
    configuration = objectValue(
      condition['HttpRequestMethodConfig'],
      'HttpRequestMethodConfig'
    );
    candidate = request.method;
  } else if (field === 'path-pattern') {
    configuration = objectValue(
      condition['PathPatternConfig'],
      'PathPatternConfig'
    );
    candidate = new URL(request.path, 'http://simulator.invalid').pathname;
  } else {
    throw new CoreError(
      'UnsupportedCapability',
      `listener rule condition ${field} is not implemented`
    );
  }
  const values = configuration['Values'];
  if (!Array.isArray(values) || values.length < 1) {
    throw new CoreError(
      'ValidationFailed',
      `listener rule ${field} values are invalid`
    );
  }
  return values.some(
    (value) =>
      typeof value === 'string' && wildcardPattern(value).test(candidate)
  );
}

function matchingRule(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  request: HttpRequest
): ResourceRecord | undefined {
  return resourcesForDeployment(world, command.deploymentId, RULE_RESOURCE)
    .filter((resource) => {
      const conditions = objectArray(
        storedProperties(resource).templateProperties['Conditions'] ?? [],
        'listener rule Conditions'
      );
      return conditions.every((condition) =>
        conditionMatches(condition, request)
      );
    })
    .sort((left, right) => {
      const leftPriority = Number(
        storedProperties(left).templateProperties['Priority']
      );
      const rightPriority = Number(
        storedProperties(right).templateProperties['Priority']
      );
      if (!Number.isSafeInteger(leftPriority)) {
        throw new CoreError(
          'ValidationFailed',
          'listener rule Priority is invalid'
        );
      }
      if (!Number.isSafeInteger(rightPriority)) {
        throw new CoreError(
          'ValidationFailed',
          'listener rule Priority is invalid'
        );
      }
      return leftPriority - rightPriority;
    })[0];
}

function singleAction(value: unknown, label: string) {
  const actions = objectArray(value, label);
  if (!isSingleton(actions)) {
    throw new CoreError(
      'UnsupportedCapability',
      `${label} must contain exactly one action`
    );
  }
  return actions[0];
}

function listenerDefaultAction(
  command: ProviderCommandInput,
  world: ProviderWorldView
) {
  const listeners = resourcesForDeployment(
    world,
    command.deploymentId,
    LISTENER_RESOURCE
  );
  if (!isSingleton(listeners)) {
    throw new CoreError(
      'Conflict',
      'HTTP request requires exactly one listener'
    );
  }
  const listener = listeners[0];
  return singleAction(
    storedProperties(listener).templateProperties['DefaultActions'],
    'listener DefaultActions'
  );
}

function fixedResponse(action: Readonly<Record<string, unknown>>) {
  const config = objectValue(
    action['FixedResponseConfig'],
    'FixedResponseConfig'
  );
  const status = Number(stringValue(config['StatusCode'], 'StatusCode'));
  if (!Number.isSafeInteger(status) || status < 100 || status > 599) {
    throw new CoreError('ValidationFailed', 'fixed response status is invalid');
  }
  return {
    StatusCode: status,
    Headers: {
      'content-type':
        optionalString(config['ContentType'], 'ContentType') ?? 'text/plain',
    },
    Body: optionalString(config['MessageBody'], 'MessageBody') ?? '',
  };
}

function findTargetFunction(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  action: Readonly<Record<string, unknown>>
): ResourceRecord {
  const targetGroupArn = stringValue(
    action['TargetGroupArn'],
    'TargetGroupArn'
  );
  const targetGroups = resourcesForDeployment(
    world,
    command.deploymentId,
    TARGET_GROUP_RESOURCE
  ).filter(
    (resource) => storedProperties(resource).refValue === targetGroupArn
  );
  if (!isSingleton(targetGroups)) {
    throw new CoreError(
      'Conflict',
      'HTTP request target group is missing or ambiguous'
    );
  }
  const targetGroupResource = targetGroups[0];
  const targetGroup = storedProperties(targetGroupResource);
  if (targetGroup.templateProperties['TargetType'] !== 'lambda') {
    throw new CoreError(
      'UnsupportedCapability',
      'HTTP request target group is not a Lambda target'
    );
  }
  const targets = objectArray(
    targetGroup.templateProperties['Targets'],
    'target group Targets'
  );
  if (!isSingleton(targets)) {
    throw new CoreError(
      'Conflict',
      'HTTP request requires exactly one Lambda target'
    );
  }
  const target = targets[0];
  const targetId = stringValue(target['Id'], 'target Id');
  const functions = resourcesForDeployment(
    world,
    command.deploymentId,
    LAMBDA_RESOURCE
  ).filter((resource) => {
    const stored = storedProperties(resource);
    return (
      stored.refValue === targetId || stored.attributes['Arn'] === targetId
    );
  });
  if (!isSingleton(functions)) {
    throw new CoreError(
      'Conflict',
      'HTTP request Lambda target is missing or ambiguous'
    );
  }
  return functions[0];
}

export function validatedLambdaHttpResponse(value: unknown): {
  readonly statusCode: number;
  readonly headers: Readonly<Record<string, string>>;
  readonly body: string;
} {
  const payload = objectValue(value, 'Lambda response');
  const statusCode = Number(payload['statusCode']);
  if (
    !Number.isSafeInteger(statusCode) ||
    statusCode < 100 ||
    statusCode > 599
  ) {
    throw new CoreError('ValidationFailed', 'Lambda HTTP status is invalid');
  }
  const headers = requestHeaders(payload['headers']);
  const body = payload['body'];
  if (typeof body !== 'string' || body.length > MAX_PROBE_BODY_LENGTH) {
    throw new CoreError('ValidationFailed', 'Lambda HTTP body is invalid');
  }
  return { statusCode, headers, body };
}

function forwardedResponse(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  action: Readonly<Record<string, unknown>>,
  request: HttpRequest
): ProviderCommandResult {
  const functionResource = findTargetFunction(command, world, action);
  const functionName = storedProperties(functionResource).refValue;
  const invoked = reduceLambda(
    {
      ...command,
      service: 'lambda',
      operation: 'InvokeFunction',
      resourceType: '*',
      input: {
        FunctionName: functionName,
        Payload: {
          httpMethod: request.method,
          path: request.path,
          headers: request.headers,
          body: request.body,
          isBase64Encoded: false,
        },
      },
    },
    world
  );
  const { statusCode, headers, body } = validatedLambdaHttpResponse(
    invoked.response['Payload']
  );
  return {
    ...invoked,
    events: [
      ...invoked.events,
      {
        type: 'AwsHttpRequestExecuted',
        payload: {
          method: request.method,
          path: request.path,
          functionName,
          statusCode,
        },
      },
    ],
    response: { StatusCode: statusCode, Headers: headers, Body: body },
  };
}

function executeRequest(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const request = httpRequest(command);
  const rule = matchingRule(command, world, request);
  const action = rule
    ? singleAction(
        storedProperties(rule).templateProperties['Actions'],
        'listener rule Actions'
      )
    : listenerDefaultAction(command, world);
  const type = stringValue(action['Type'], 'listener action Type');
  if (type === 'fixed-response') {
    return result('AwsHttpRequestExecuted', fixedResponse(action));
  }
  if (type === 'forward') {
    return forwardedResponse(command, world, action, request);
  }
  throw new CoreError(
    'UnsupportedCapability',
    `listener action ${type} is not implemented`
  );
}

export function reduceHttp(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  if (command.operation === 'Request') {
    return executeRequest(command, world);
  }
  if (command.operation !== 'AttackProbe') {
    throw new CoreError(
      'UnsupportedCapability',
      `HTTP operation ${command.operation} is not supported`
    );
  }
  const targetId = stringValue(command.input['TargetId'], 'TargetId');
  const probe = optionalString(command.input['Probe'], 'Probe');
  switch (probe) {
    case SQLI_AUTH_PROBE:
    case SQLI_UNION_PROBE:
      return sqlAttackProbe(command, world, targetId, probe);
    case AVAILABILITY_PROBE:
      return availabilityAttackProbe(command, world, targetId);
    case ANONYMOUS_SPAM_PROBE:
      return anonymousSpamAttackProbe(command, world, targetId);
    case undefined:
      return sqlAttackProbe(command, world, targetId);
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `HTTP attack probe ${probe} is not supported`
      );
  }
}
