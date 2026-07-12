import type { CapabilityDiagnostic } from './domain';

export type CoreErrorCode =
  | 'Conflict'
  | 'IdempotencyConflict'
  | 'NotFound'
  | 'QuotaExceeded'
  | 'SnapshotIncompatible'
  | 'UnsupportedCapability'
  | 'ValidationFailed'
  | 'WorkloadEffectFailed';

export class CoreError extends Error {
  readonly diagnostics: readonly CapabilityDiagnostic[];

  constructor(
    readonly code: CoreErrorCode,
    message: string,
    diagnostics: readonly CapabilityDiagnostic[] = []
  ) {
    super(message);
    this.name = 'CoreError';
    this.diagnostics = diagnostics;
  }
}
