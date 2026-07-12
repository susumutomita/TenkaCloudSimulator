import {
  ContractValidationError,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorDiagnostic,
  type SimulatorErrorCode,
  type SimulatorErrorEnvelope,
} from '@tenkacloud/simulator-contracts';
import {
  type CapabilityDiagnostic,
  CoreError,
  type CoreErrorCode,
} from '@tenkacloud/simulator-core';
import type { Context } from 'hono';
import { fidelityDimensions } from './fidelity.js';

export const PROTOCOL_HEADER = 'x-tenkacloud-simulator-protocol';
export const MAX_REQUEST_BODY_BYTES = 1024 * 1024;

type ErrorStatus = 400 | 404 | 409 | 413 | 422 | 429 | 500;

const CORE_ERROR_STATUS: Readonly<Record<CoreErrorCode, ErrorStatus>> = {
  Conflict: 409,
  IdempotencyConflict: 409,
  NotFound: 404,
  QuotaExceeded: 429,
  SnapshotIncompatible: 422,
  UnsupportedCapability: 422,
  ValidationFailed: 400,
  WorkloadEffectFailed: 500,
};

export class RequestValidationError extends TypeError {
  public constructor(message: string) {
    super(message);
    this.name = 'RequestValidationError';
  }
}

function requestId(c: Context): string {
  return c.req.header('x-request-id')?.trim() || crypto.randomUUID();
}

export function coreDiagnostic(
  diagnostic: CapabilityDiagnostic
): SimulatorDiagnostic {
  return {
    code: diagnostic.code,
    message: `${diagnostic.provider}/${diagnostic.service}/${diagnostic.resourceType}/${diagnostic.operation}`,
    provider: diagnostic.provider,
    service: diagnostic.service,
    resourceType: diagnostic.resourceType,
    operation: diagnostic.operation,
    requiredFidelity: fidelityDimensions(diagnostic.fidelity),
    availableFidelity: fidelityDimensions(diagnostic.availableFidelity),
    ...(diagnostic.source
      ? {
          source: {
            file: diagnostic.source.path,
            ...(diagnostic.source.line === undefined
              ? {}
              : { line: diagnostic.source.line }),
          },
        }
      : {}),
  };
}

function contractDiagnostics(
  error: ContractValidationError
): readonly SimulatorDiagnostic[] {
  return (error.validationErrors ?? []).map((validationError) => ({
    code: validationError.keyword,
    message: `${validationError.instancePath || '/'} ${validationError.message || 'is invalid'}`,
  }));
}

export function errorResponse(
  c: Context,
  code: SimulatorErrorCode,
  message: string,
  status: ErrorStatus,
  retryable = false,
  diagnostics: readonly SimulatorDiagnostic[] = []
): Response {
  c.header(PROTOCOL_HEADER, SIMULATOR_PROTOCOL_VERSION);
  const envelope: SimulatorErrorEnvelope = {
    error: {
      code,
      message,
      requestId: requestId(c),
      retryable,
      diagnostics,
    },
  };
  return c.json(envelope, status);
}

export function protocolMismatchResponse(c: Context): Response {
  return errorResponse(
    c,
    'ProtocolVersionMismatch',
    `request must use simulator protocol ${SIMULATOR_PROTOCOL_VERSION}`,
    400
  );
}

export function bodyLimitResponse(c: Context): Response {
  return errorResponse(
    c,
    'QuotaExceeded',
    `request body exceeds ${MAX_REQUEST_BODY_BYTES} bytes`,
    413
  );
}

export function handleAppError(error: Error, c: Context): Response {
  if (error instanceof ContractValidationError) {
    return errorResponse(
      c,
      'ValidationFailed',
      error.message,
      400,
      false,
      contractDiagnostics(error)
    );
  }
  if (error instanceof RequestValidationError || error instanceof SyntaxError) {
    return errorResponse(c, 'ValidationFailed', error.message, 400);
  }
  if (error instanceof CoreError) {
    return errorResponse(
      c,
      error.code,
      error.message,
      CORE_ERROR_STATUS[error.code],
      error.code === 'WorkloadEffectFailed',
      error.diagnostics.map(coreDiagnostic)
    );
  }
  return errorResponse(
    c,
    'InternalError',
    'simulator request failed',
    500,
    true
  );
}
