import { createHash } from 'node:crypto';
import {
  CoreError,
  deterministicId,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { evaluateExternalWorker } from './external-evaluator';
import { declaration } from './model';
import {
  awsResources,
  findBy,
  result,
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import {
  booleanValue,
  numberValue,
  objectValue,
  optionalString,
  stringArray,
  stringValue,
} from './value';

const FUNCTION_RESOURCE = 'AWS::Lambda::Function';
const FUNCTION_NAME = /^[A-Za-z0-9-_]{1,64}$/;
const HANDLER = /^[A-Za-z0-9_./-]{1,128}$/;
const ROLE_ARN = /^arn:aws:iam::\d{12}:role\/[A-Za-z0-9+=,.@_/-]{1,512}$/;
const SUPPORTED_RUNTIMES = new Set(['nodejs20.x', 'nodejs22.x']);
const CREATE_FUNCTION_FIELDS = new Set([
  'Architectures',
  'Code',
  'Description',
  'Environment',
  'FunctionName',
  'Handler',
  'MemorySize',
  'PackageType',
  'Publish',
  'Role',
  'Runtime',
  'Tags',
  'Timeout',
]);
const MAX_ZIP_BYTES = 768 * 1024;

function findFunction(world: ProviderWorldView, name: string) {
  return findBy(
    world,
    FUNCTION_RESOURCE,
    (properties) =>
      properties.refValue === name || properties.attributes['Arn'] === name,
    'Lambda function'
  );
}

function payloadValue(value: unknown): Readonly<Record<string, unknown>> {
  if (typeof value !== 'string') return objectValue(value ?? {}, 'Payload');
  try {
    return objectValue(JSON.parse(value), 'Payload');
  } catch {
    throw new CoreError('ValidationFailed', 'Payload must contain valid JSON');
  }
}

function environment(
  properties: ReturnType<typeof storedProperties>
): Readonly<Record<string, string>> {
  const environmentValue = properties.templateProperties['Environment'];
  if (environmentValue === undefined) return {};
  const root = objectValue(environmentValue, 'Lambda Environment');
  const variables = objectValue(
    root['Variables'] ?? {},
    'Lambda Environment.Variables'
  );
  return Object.fromEntries(
    Object.entries(variables).map(([key, value]) => [
      key,
      stringValue(value, `Lambda environment ${key}`),
    ])
  );
}

function redactedEnvironment(
  properties: ReturnType<typeof storedProperties>
): Readonly<Record<string, string>> {
  return Object.fromEntries(
    Object.keys(environment(properties)).map((name) => [name, '[REDACTED]'])
  );
}

function boundedInteger(
  value: unknown,
  fallback: number,
  minimum: number,
  maximum: number,
  label: string
): number {
  if (value === undefined) return fallback;
  const number = numberValue(value, label);
  if (!Number.isSafeInteger(number) || number < minimum || number > maximum) {
    throw new CoreError(
      'ValidationFailed',
      `${label} must be an integer between ${minimum} and ${maximum}`
    );
  }
  return number;
}

function stringMap(
  value: unknown,
  label: string
): Readonly<Record<string, string>> {
  const record = objectValue(value ?? {}, label);
  if (Object.keys(record).length > 64) {
    throw new CoreError('ValidationFailed', `${label} has too many entries`);
  }
  return Object.fromEntries(
    Object.entries(record).map(([key, entry]) => {
      if (
        !/^[A-Za-z][A-Za-z0-9_]{0,127}$/.test(key) ||
        typeof entry !== 'string' ||
        entry.length > 4096 ||
        entry.includes('\u0000')
      ) {
        throw new CoreError(
          'ValidationFailed',
          `${label} entry ${key} is invalid`
        );
      }
      return [key, entry];
    })
  );
}

function zipDigest(value: unknown): string {
  const encoded = stringValue(value, 'Code.ZipFile');
  if (
    encoded.length % 4 !== 0 ||
    !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(
      encoded
    )
  ) {
    throw new CoreError('ValidationFailed', 'Code.ZipFile must be base64');
  }
  const bytes = Buffer.from(encoded, 'base64');
  if (bytes.byteLength < 1 || bytes.byteLength > MAX_ZIP_BYTES) {
    throw new CoreError(
      'ValidationFailed',
      `Code.ZipFile must contain between 1 and ${MAX_ZIP_BYTES} bytes`
    );
  }
  return createHash('sha256').update(bytes).digest('base64');
}

function lambdaConfiguration(
  stored: ReturnType<typeof storedProperties>
): Readonly<Record<string, unknown>> {
  const state = stateObject(stored);
  return {
    FunctionName: stored.refValue,
    FunctionArn: stored.attributes['Arn'],
    Runtime: stored.templateProperties['Runtime'],
    Handler: stored.templateProperties['Handler'],
    Role: stored.templateProperties['Role'],
    Description: stored.templateProperties['Description'] ?? '',
    Timeout: stored.templateProperties['Timeout'] ?? 3,
    MemorySize: stored.templateProperties['MemorySize'] ?? 128,
    Architectures: stored.templateProperties['Architectures'] ?? ['x86_64'],
    PackageType: 'Zip',
    CodeSha256: state['codeSha256'],
    Environment: { Variables: redactedEnvironment(stored) },
    State: 'Active',
  };
}

interface NewFunctionIdentity {
  readonly functionName: string;
  readonly runtime: string;
  readonly handler: string;
  readonly role: string;
}

function newFunctionIdentity(
  input: Readonly<Record<string, unknown>>
): NewFunctionIdentity {
  const functionName = stringValue(input['FunctionName'], 'FunctionName');
  if (!FUNCTION_NAME.test(functionName)) {
    throw new CoreError('ValidationFailed', 'FunctionName is invalid');
  }
  const runtime = stringValue(input['Runtime'], 'Runtime');
  if (!SUPPORTED_RUNTIMES.has(runtime)) {
    throw new CoreError(
      'UnsupportedCapability',
      `Lambda runtime ${runtime} is not supported`
    );
  }
  const handler = stringValue(input['Handler'], 'Handler');
  if (!HANDLER.test(handler)) {
    throw new CoreError('ValidationFailed', 'Handler is invalid');
  }
  const role = stringValue(input['Role'], 'Role');
  if (!ROLE_ARN.test(role)) {
    throw new CoreError('ValidationFailed', 'Role must be an IAM role ARN');
  }
  return { functionName, runtime, handler, role };
}

interface NewFunctionPackage {
  readonly architectures: readonly string[];
  readonly codeSha256: string;
}

function newFunctionPackage(
  input: Readonly<Record<string, unknown>>
): NewFunctionPackage {
  const packageType = optionalString(input['PackageType'], 'PackageType');
  if (packageType !== undefined && packageType !== 'Zip') {
    throw new CoreError(
      'UnsupportedCapability',
      'only Lambda Zip package type is supported'
    );
  }
  const architectures =
    input['Architectures'] === undefined
      ? ['x86_64']
      : stringArray(input['Architectures'], 'Architectures');
  if (architectures.length !== 1 || architectures[0] !== 'x86_64') {
    throw new CoreError(
      'UnsupportedCapability',
      'only Lambda x86_64 architecture is supported'
    );
  }
  if (
    input['Publish'] !== undefined &&
    booleanValue(input['Publish'], 'Publish')
  ) {
    throw new CoreError(
      'UnsupportedCapability',
      'published Lambda versions are not supported'
    );
  }
  const code = objectValue(input['Code'], 'Code');
  if (Object.keys(code).length !== 1 || code['ZipFile'] === undefined) {
    throw new CoreError(
      'UnsupportedCapability',
      'Lambda Code must contain only a bounded ZipFile payload'
    );
  }
  return { architectures, codeSha256: zipDigest(code['ZipFile']) };
}

function newFunctionEnvironment(
  input: Readonly<Record<string, unknown>>
): Readonly<Record<string, string>> {
  const environmentValue =
    input['Environment'] === undefined
      ? {}
      : objectValue(input['Environment'], 'Environment');
  const unknown = Object.keys(environmentValue).find(
    (field) => field !== 'Variables'
  );
  if (unknown) {
    throw new CoreError(
      'UnsupportedCapability',
      `Lambda Environment field ${unknown} is not supported`
    );
  }
  return stringMap(environmentValue['Variables'], 'Environment');
}

function optionalDescription(value: unknown): string {
  if (value === undefined) return '';
  if (typeof value !== 'string' || value.length > 256) {
    throw new CoreError('ValidationFailed', 'Description is too long');
  }
  return value;
}

function createFunction(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const unknown = Object.keys(command.input).find(
    (field) => !CREATE_FUNCTION_FIELDS.has(field)
  );
  if (unknown) {
    throw new CoreError(
      'UnsupportedCapability',
      `Lambda CreateFunction field ${unknown} is not supported`
    );
  }
  const { functionName, runtime, handler, role } = newFunctionIdentity(
    command.input
  );
  if (
    awsResources(world, FUNCTION_RESOURCE).some(
      (resource) => storedProperties(resource).refValue === functionName
    )
  ) {
    throw new CoreError('Conflict', 'Lambda function already exists');
  }
  const { architectures, codeSha256 } = newFunctionPackage(command.input);
  const variables = newFunctionEnvironment(command.input);
  const tags = stringMap(command.input['Tags'], 'Tags');
  const description = optionalDescription(command.input['Description']);
  const timeout = boundedInteger(
    command.input['Timeout'],
    3,
    1,
    900,
    'Timeout'
  );
  const memorySize = boundedInteger(
    command.input['MemorySize'],
    128,
    128,
    10_240,
    'MemorySize'
  );
  const arn = `arn:aws:lambda:us-east-1:123456789012:function:${functionName}`;
  const resource = declaration({
    resourceType: FUNCTION_RESOURCE,
    resourceId: deterministicId('aws-lambda-function', {
      worldId: command.worldId,
      functionName,
    }),
    properties: {
      logicalId: `ParticipantFunction.${functionName}`,
      physicalId: functionName,
      refValue: functionName,
      dependsOn: [],
      attributes: { Arn: arn },
      templateProperties: {
        FunctionName: functionName,
        Runtime: runtime,
        Handler: handler,
        Role: role,
        Description: description,
        Timeout: timeout,
        MemorySize: memorySize,
        Architectures: architectures,
        Environment: { Variables: variables },
        Tags: tags,
      },
      status: 'CREATE_COMPLETE',
      state: { invocationCount: 0, codeSha256 },
    },
  });
  return result(
    'AwsLambdaFunctionCreated',
    lambdaConfiguration(storedProperties(resource)),
    [resource]
  );
}

function reply(statusCode: number, body: Readonly<Record<string, unknown>>) {
  return {
    statusCode,
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  };
}

function ssmValue(
  world: ProviderWorldView,
  name: string,
  fallback = ''
): string {
  const parameter = world.resources.find(
    (resource) =>
      resource.provider === 'aws' &&
      resource.resourceType === 'AWS::SSM::Parameter' &&
      (resource.properties['refValue'] === name ||
        objectValue(
          resource.properties['templateProperties'],
          'templateProperties'
        )['Name'] === name)
  );
  if (!parameter) return fallback;
  const value = objectValue(
    parameter.properties['templateProperties'],
    'templateProperties'
  )['Value'];
  return typeof value === 'string' ? value.trim() : fallback;
}

function gateHandler(
  payload: Readonly<Record<string, unknown>>,
  env: Readonly<Record<string, string>>,
  world: ProviderWorldView
) {
  const prefix = env['NAME_PREFIX'] ?? '';
  const path =
    typeof payload['rawPath'] === 'string' ? payload['rawPath'] : '/';
  const headerValue = payload['headers'];
  const rawHeaders =
    headerValue === undefined
      ? {}
      : objectValue(headerValue, 'Payload.headers');
  const headers = Object.fromEntries(
    Object.entries(rawHeaders).map(([key, value]) => [
      key.toLowerCase(),
      typeof value === 'string' ? value : String(value),
    ])
  );
  const monetized = ssmValue(
    world,
    `/${prefix}/config/monetized_path`,
    '/content/*'
  );
  const payTo = ssmValue(world, `/${prefix}/config/pay_to_wallet`);
  const mode = ssmValue(world, `/${prefix}/config/currency_mode`, 'test');
  const price = ssmValue(world, `/${prefix}/config/price_usdc`, '10000');
  const network = mode === 'test' ? 'base-sepolia' : 'base-mainnet';
  const pattern = new RegExp(
    `^${monetized.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replaceAll('\\*', '.*')}$`
  );
  if (!pattern.test(path)) {
    return reply(200, { free: true, path, note: 'this path is not monetized' });
  }
  const manifest = {
    x402Version: 1,
    accepts: [
      {
        scheme: 'exact',
        network,
        asset: 'USDC',
        maxAmountRequired: price,
        payTo,
        resource: path,
        description: 'TenkaCloud premium content',
      },
    ],
  };
  const token = headers['x-payment'];
  if (!token) return reply(402, manifest);
  let payment: Readonly<Record<string, unknown>>;
  try {
    payment = objectValue(
      JSON.parse(Buffer.from(token, 'base64').toString('utf8')),
      'X-PAYMENT'
    );
  } catch {
    return reply(402, { ...manifest, error: 'invalid X-PAYMENT encoding' });
  }
  const amount = String(payment['amount'] ?? '0');
  const validWallet = /^0x[0-9a-fA-F]{40}$/.test(payTo);
  const paid =
    mode === 'test' &&
    validWallet &&
    payment['payTo'] === payTo &&
    payment['network'] === network &&
    /^\d+$/.test(amount) &&
    Number(amount) >= Number(price);
  return paid
    ? reply(200, {
        paid: true,
        content: "PREMIUM ARTICLE -- Kato-san's leftover notes",
        flag: env['FLAG'] ?? '',
      })
    : reply(402, { ...manifest, error: 'payment rejected' });
}

function searchReply(
  statusCode: number,
  statusDescription: string,
  body: string,
  headers: Readonly<Record<string, string>> = {}
) {
  return {
    statusCode,
    statusDescription,
    isBase64Encoded: false,
    headers: {
      'Content-Type': 'text/plain; charset=utf-8',
      'Access-Control-Allow-Origin': '*',
      'Access-Control-Allow-Methods': 'GET, HEAD, POST, QUERY, OPTIONS',
      'Access-Control-Allow-Headers': 'content-type',
      ...headers,
    },
    body,
  };
}

function searchQueryHandler(
  payload: Readonly<Record<string, unknown>>,
  env: Readonly<Record<string, string>>,
  headers: Readonly<Record<string, unknown>>
) {
  const contentType = String(
    headers['content-type'] ?? headers['Content-Type'] ?? ''
  )
    .split(';')[0]
    ?.trim()
    .toLowerCase();
  if (contentType !== 'application/json') {
    return searchReply(
      415,
      'Unsupported Media Type',
      '415 - QUERY requires Content-Type: application/json.\n'
    );
  }
  const rawBody = String(payload['body'] ?? '');
  let parsed: unknown;
  try {
    parsed = JSON.parse(
      payload['isBase64Encoded'] === true
        ? Buffer.from(rawBody, 'base64').toString('utf8')
        : rawBody
    );
  } catch {
    return searchReply(
      400,
      'Bad Request',
      '400 - request body is not valid JSON.\n'
    );
  }
  const query =
    parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)
      ? Reflect.get(parsed, 'query')
      : undefined;
  const match =
    query !== null && typeof query === 'object' && !Array.isArray(query)
      ? Reflect.get(query, 'match')
      : undefined;
  if (typeof match !== 'string' || !match) {
    return searchReply(
      422,
      'Unprocessable Entity',
      '422 - invalid search DSL.\n'
    );
  }
  return searchReply(
    200,
    'OK',
    `QUERY /search OK. Matched term: ${match}\nFlag: ${env['FLAG'] ?? ''}\n`,
    { 'Content-Location': `/search?match=${encodeURIComponent(match)}` }
  );
}

function searchHandler(
  payload: Readonly<Record<string, unknown>>,
  env: Readonly<Record<string, string>>
) {
  const method = String(payload['httpMethod'] ?? 'GET').toUpperCase();
  const headers = objectValue(payload['headers'] ?? {}, 'Payload.headers');
  if (method === 'OPTIONS') {
    return searchReply(204, 'No Content', '', {
      'Access-Control-Max-Age': '600',
    });
  }
  if (method === 'GET' || method === 'HEAD') {
    const body =
      method === 'HEAD'
        ? ''
        : 'This is a QUERY-based search API (RFC 10008).\n';
    return searchReply(200, 'OK', body);
  }
  if (method === 'POST') {
    return searchReply(200, 'OK', 'Use QUERY for retry-safe search.\n');
  }
  if (method !== 'QUERY') {
    return searchReply(
      405,
      'Method Not Allowed',
      `405 - method '${method}' is not supported.\n`,
      {
        Allow: 'GET, HEAD, POST, QUERY, OPTIONS',
      }
    );
  }
  return searchQueryHandler(payload, env, headers);
}

function invokeHandler(
  logicalId: string,
  payload: Readonly<Record<string, unknown>>,
  env: Readonly<Record<string, string>>,
  world: ProviderWorldView
): Readonly<Record<string, unknown>> {
  switch (logicalId) {
    case 'HelloFunction':
      return {
        statusCode: 200,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ hello: 'aws', problem: 'hello-multicloud' }),
      };
    case 'SearchFunction':
      return searchHandler(payload, env);
    case 'GateFunction':
      return gateHandler(payload, env, world);
    case 'EvaluatorFunction':
      throw new CoreError(
        'UnsupportedCapability',
        'EvaluatorFunction requires an external HTTP runtime and is not simulated'
      );
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `Lambda handler ${logicalId} is not implemented`
      );
  }
}

function invocationResult(
  resource: ReturnType<typeof findFunction>,
  stored: ReturnType<typeof storedProperties>,
  handlerResponse: Readonly<Record<string, unknown>>,
  world: ProviderWorldView
): ProviderCommandResult {
  const state = stateObject(stored);
  const count =
    typeof state['invocationCount'] === 'number'
      ? state['invocationCount'] + 1
      : 1;
  const updated = updateStoredResource(resource, {
    state: {
      ...state,
      invocationCount: count,
      lastInvokedAt: world.world.virtualTime,
    },
  });
  return result(
    'AwsLambdaFunctionInvoked',
    {
      StatusCode: 200,
      ExecutedVersion: '$LATEST',
      Payload: handlerResponse,
    },
    [updated]
  );
}

export function reduceLambda(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  if (command.operation === 'CreateFunction') {
    return createFunction(command, world);
  }
  const functionName = stringValue(
    command.input['FunctionName'],
    'FunctionName'
  );
  const resource = findFunction(world, functionName);
  const stored = storedProperties(resource);
  switch (command.operation) {
    case 'GetFunction':
      return result('AwsLambdaFunctionRead', {
        Configuration: lambdaConfiguration(stored),
        Code: { RepositoryType: 'S3' },
      });
    case 'InvokeFunction': {
      const payload = payloadValue(command.input['Payload']);
      const handlerResponse = invokeHandler(
        stored.logicalId,
        payload,
        environment(stored),
        world
      );
      return invocationResult(resource, stored, handlerResponse, world);
    }
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `Lambda operation ${command.operation} is not supported`
      );
  }
}

export async function reduceLambdaAsync(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  trustedWorkerOrigins: ReadonlySet<string>
): Promise<ProviderCommandResult> {
  if (command.operation !== 'InvokeFunction') {
    return reduceLambda(command, world);
  }
  const functionName = stringValue(
    command.input['FunctionName'],
    'FunctionName'
  );
  const resource = findFunction(world, functionName);
  const stored = storedProperties(resource);
  if (stored.logicalId !== 'EvaluatorFunction') {
    return reduceLambda(command, world);
  }
  const payload = payloadValue(command.input['Payload']);
  const workerUrl = payload['workerUrl'] ?? payload['WorkerUrl'];
  const handlerResponse = await evaluateExternalWorker(
    workerUrl,
    environment(stored)['FLAG'] ?? '',
    trustedWorkerOrigins
  );
  return invocationResult(resource, stored, handlerResponse, world);
}
