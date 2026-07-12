import {
  CoreError,
  contentHash,
  type ExecuteCommandInput,
  type SimulationCore,
} from '@tenkacloud/simulator-core';
import {
  AwsNativeGateway,
  type AwsNativeGatewayCommand,
  type AwsNativeGatewayOptions,
} from '@tenkacloud/simulator-provider-aws';
import {
  AzureArmGateway,
  AzureArmGatewayError,
} from '@tenkacloud/simulator-provider-azure';
import {
  GcpRestGateway,
  GcpRestGatewayError,
} from '@tenkacloud/simulator-provider-gcp';
import {
  SakuraAppRunGateway,
  SakuraAppRunGatewayError,
} from '@tenkacloud/simulator-provider-sakura';

export interface NativeGatewayCredentials {
  readonly awsAccessKeyId: string;
  readonly azureCredential: string;
  readonly gcpCredential: string;
  readonly sakuraCredential: string;
}

export interface NativeGatewayOptions {
  readonly core: SimulationCore;
  readonly credentials: NativeGatewayCredentials;
  readonly simulatorOrigin: string;
  readonly beforeAwsCommand?: (
    command: AwsNativeGatewayCommand
  ) => void | Promise<void>;
  readonly onAwsCommandSuccess?: (
    command: AwsNativeGatewayCommand,
    response: Readonly<Record<string, unknown>>
  ) => void | Promise<void>;
}

interface NativeCommand {
  readonly worldId: string;
  readonly command: ExecuteCommandInput;
}

type JsonGatewayError =
  | AzureArmGatewayError
  | GcpRestGatewayError
  | SakuraAppRunGatewayError
  | CoreError;

const CORE_ERROR_STATUS = {
  Conflict: 409,
  IdempotencyConflict: 409,
  NotFound: 404,
  QuotaExceeded: 413,
  SnapshotIncompatible: 400,
  UnsupportedCapability: 422,
  ValidationFailed: 400,
  WorkloadEffectFailed: 500,
} as const;

function coreErrorStatus(error: CoreError): number {
  return CORE_ERROR_STATUS[error.code];
}

function jsonGatewayError(error: JsonGatewayError): Response {
  const status =
    error instanceof CoreError ? coreErrorStatus(error) : error.status;
  return Response.json(
    { error: { code: error.code, message: error.message } },
    { status }
  );
}

function isJsonGatewayError(error: unknown): error is JsonGatewayError {
  return (
    error instanceof CoreError ||
    error instanceof AzureArmGatewayError ||
    error instanceof GcpRestGatewayError ||
    error instanceof SakuraAppRunGatewayError
  );
}

async function executeNativeCommand(
  core: SimulationCore,
  provider: string,
  translated: NativeCommand
): Promise<Readonly<Record<string, unknown>>> {
  return core.executeCommandAsync(
    translated.worldId,
    translated.command,
    `native:${provider}:${contentHash(translated)}`
  );
}

function isAwsRequest(request: Request): boolean {
  const authorization = request.headers.get('authorization') ?? '';
  return (
    authorization.startsWith('AWS4-HMAC-SHA256 ') ||
    request.headers.has('x-amz-target') ||
    request.headers.has('x-amz-content-sha256') ||
    new URL(request.url).searchParams.has('Action')
  );
}

export function createNativeGatewayHandler(options: NativeGatewayOptions) {
  const awsOptions: AwsNativeGatewayOptions = {
    simulatorOrigin: options.simulatorOrigin,
    simulatorAccessKeyId: options.credentials.awsAccessKeyId,
    ...(options.beforeAwsCommand === undefined
      ? {}
      : { beforeCommand: options.beforeAwsCommand }),
    ...(options.onAwsCommandSuccess === undefined
      ? {}
      : { onCommandSuccess: options.onAwsCommandSuccess }),
  };
  const aws = new AwsNativeGateway(awsOptions);
  const azure = new AzureArmGateway({
    simulatorOrigin: options.simulatorOrigin,
    simulatorCredential: options.credentials.azureCredential,
  });
  const gcp = new GcpRestGateway({
    simulatorOrigin: options.simulatorOrigin,
    simulatorCredential: options.credentials.gcpCredential,
  });
  const sakura = new SakuraAppRunGateway({
    simulatorOrigin: options.simulatorOrigin,
    simulatorCredential: options.credentials.sakuraCredential,
  });

  return async (request: Request): Promise<Response | undefined> => {
    if (isAwsRequest(request)) return aws.handle(request, options.core);
    const path = new URL(request.url).pathname;
    let provider: string;
    let translated: NativeCommand;
    try {
      if (path.startsWith('/subscriptions/')) {
        provider = 'azure';
        translated = await azure.translate(request);
      } else if (path.startsWith('/v2/projects/')) {
        provider = 'gcp';
        translated = await gcp.translate(request);
      } else if (path.startsWith('/cloud/api/apprun/')) {
        provider = 'sakura';
        translated = await sakura.translate(request);
      } else {
        return undefined;
      }
      return Response.json(
        await executeNativeCommand(options.core, provider, translated)
      );
    } catch (error) {
      if (!isJsonGatewayError(error)) throw error;
      return jsonGatewayError(error);
    }
  };
}
