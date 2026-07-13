export const SIMULATOR_PROTOCOL_VERSION = '2026-07-11' as const;
export const SIMULATOR_SNAPSHOT_VERSION = '1' as const;
export const SIMULATOR_EVENT_PAGE_SIZE = 100 as const;
export const SIMULATOR_RUNTIME_TARGET_ID_PATTERN = /^[a-z][a-z0-9-]{0,31}$/;

export type JsonPrimitive = boolean | null | number | string;
export type JsonValue = JsonPrimitive | JsonObject | readonly JsonValue[];
export type JsonObject = { readonly [key: string]: JsonValue };

export type SimulatorOperation =
  | 'deploy'
  | 'delete'
  | 'get'
  | 'capabilities'
  | 'world';

export type FidelityDimension =
  | 'contract'
  | 'control'
  | 'security'
  | 'network'
  | 'data-plane';

export interface SimulatorEngineCapabilities {
  readonly operations: readonly SimulatorOperation[];
  readonly resources?: readonly string[];
  readonly fidelity?: readonly FidelityDimension[];
  readonly constraints?: JsonObject;
}

export interface SimulatorProviderCapabilities {
  readonly engines: Readonly<Record<string, SimulatorEngineCapabilities>>;
}

export interface SimulatorCapability {
  readonly provider: string;
  readonly engine?: string;
  readonly service: string;
  readonly resourceType: string;
  readonly operation: string;
  readonly fidelity: readonly FidelityDimension[];
  readonly constraints?: JsonObject;
}

export interface SimulatorCapabilities {
  readonly protocolVersion: typeof SIMULATOR_PROTOCOL_VERSION;
  readonly simulatorVersion: string;
  readonly providers: Readonly<Record<string, SimulatorProviderCapabilities>>;
  readonly capabilities?: readonly SimulatorCapability[];
  readonly constraints?: JsonObject;
}

export interface SingleRuntimeDescriptor {
  readonly provider: string;
  readonly engine: string;
  readonly entry: string;
}

export interface CompositeRuntimeTarget {
  readonly id: string;
  readonly provider: string;
  readonly engine: string;
  readonly entry: string;
}

export interface CompositeRuntimeDescriptor {
  readonly kind: 'composite';
  readonly targets: readonly CompositeRuntimeTarget[];
}

export type ProblemRuntimeDescriptor =
  | SingleRuntimeDescriptor
  | CompositeRuntimeDescriptor;

export interface SimulatorWorldRequest {
  readonly tenantId: string;
  readonly eventId: string;
  readonly teamId: string;
  readonly deploymentId: string;
  readonly seed?: string;
  readonly virtualClock?: string;
}

export interface SimulatorWorldResponse {
  readonly worldId: string;
  readonly consoleUrl: string;
}

export interface SimulatorClockAdvanceRequest {
  readonly milliseconds: number;
}

export interface SimulatorAppliedTransition {
  readonly provider: string;
  readonly transitionId: string;
}

export interface SimulatorClockAdvanceResponse {
  readonly clock: string;
  readonly appliedTransitions: readonly SimulatorAppliedTransition[];
}

export interface SimulatorDeploymentRequest {
  readonly problemId: string;
  readonly runtime: ProblemRuntimeDescriptor;
  readonly templateBody: string;
  readonly metadata?: JsonValue;
  readonly simulationOverlay?: SimulatorSimulationOverlay;
}

export interface SimulatorMaterializeWorkloadsRequest {
  readonly deploymentId: string;
}

export type SimulatorFidelityLevel = 'L0' | 'L1' | 'L2' | 'L3' | 'L4';

export interface SimulatorOverlayArtifact {
  readonly path: string;
  readonly sha256: string;
}

export interface SimulatorOverlayRequirement {
  readonly targetId: string;
  readonly service: string;
  readonly resourceType: string;
  readonly operation: string;
  readonly fidelity: SimulatorFidelityLevel;
  readonly plane: CapabilityPlane;
  readonly artifact?: SimulatorOverlayArtifact;
}

export interface SimulatorOverlayWorkload {
  readonly id: string;
  readonly targetId: string;
  readonly resourceRef: string;
  readonly image: string;
  readonly command?: readonly string[];
  readonly containerPort: number;
  readonly healthPath?: string;
  readonly artifact?: SimulatorOverlayArtifact;
}

export interface SimulatorSimulationOverlay {
  readonly $schema?: string;
  readonly schemaVersion: '1';
  readonly requirements?: readonly SimulatorOverlayRequirement[];
  readonly workloads?: readonly SimulatorOverlayWorkload[];
}

export type SimulatorDeploymentStatus =
  | 'accepted'
  | 'deploying'
  | 'running'
  | 'failed'
  | 'deleting'
  | 'deleted';

export interface SourceLocation {
  readonly file: string;
  readonly line?: number;
  readonly column?: number;
}

export interface SimulatorDiagnostic {
  readonly code: string;
  readonly message: string;
  readonly provider?: string;
  readonly engine?: string;
  readonly service?: string;
  readonly resourceType?: string;
  readonly operation?: string;
  readonly requiredFidelity?: readonly FidelityDimension[];
  readonly availableFidelity?: readonly FidelityDimension[];
  readonly source?: SourceLocation;
}

export interface SimulatorDeploymentTargetResponse {
  readonly id: string;
  readonly provider: string;
  readonly engine: string;
  readonly status: SimulatorDeploymentStatus;
  readonly outputs: Readonly<Record<string, string>>;
  readonly diagnostics?: readonly SimulatorDiagnostic[];
}

export interface SimulatorDeploymentResponse {
  readonly deploymentId: string;
  readonly status: SimulatorDeploymentStatus;
  readonly outputs: Readonly<Record<string, string>>;
  readonly targets?: readonly SimulatorDeploymentTargetResponse[];
  readonly diagnostics?: readonly SimulatorDiagnostic[];
}

export type SimulatorErrorCode =
  | 'ValidationFailed'
  | 'NotFound'
  | 'Conflict'
  | 'UnauthorizedOperation'
  | 'UnsupportedCapability'
  | 'NotImplemented'
  | 'IdempotencyConflict'
  | 'QuotaExceeded'
  | 'SnapshotIncompatible'
  | 'ProtocolVersionMismatch'
  | 'WorkloadEffectFailed'
  | 'InternalError';

export interface SimulatorErrorEnvelope {
  readonly error: {
    readonly code: SimulatorErrorCode;
    readonly message: string;
    readonly requestId: string;
    readonly retryable: boolean;
    readonly diagnostics: readonly SimulatorDiagnostic[];
  };
}

export interface SimulatorCommandIdentity {
  readonly id: string;
  readonly deploymentId?: string;
  readonly provider?: string;
  readonly operation: string;
  readonly idempotencyKey?: string;
}

export interface SimulatorEvent {
  readonly worldId: string;
  readonly sequence: number;
  readonly virtualTimestamp: string;
  readonly command: SimulatorCommandIdentity;
  readonly type: string;
  readonly schemaVersion: string;
  readonly payloadHash: string;
  readonly payload: JsonValue;
}

export interface SimulatorEventPage {
  readonly events: readonly SimulatorEvent[];
  readonly nextCursor: number;
}

export type SimulatorResourceStatus =
  | 'pending'
  | 'ready'
  | 'failed'
  | 'deleted';

export interface SimulatorResourceRecord {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
  readonly provider: string;
  readonly resourceType: string;
  readonly resourceId: string;
  readonly properties: JsonObject;
  readonly status: SimulatorResourceStatus;
}

export interface SimulatorResourceProjection {
  readonly resources: readonly SimulatorResourceRecord[];
}

export interface SimulatorNamespace {
  readonly tenantId: string;
  readonly eventId: string;
  readonly teamId: string;
}

export interface SimulatorSnapshot {
  readonly snapshotVersion: typeof SIMULATOR_SNAPSHOT_VERSION;
  readonly protocolVersion: typeof SIMULATOR_PROTOCOL_VERSION;
  readonly worldId: string;
  readonly namespace: SimulatorNamespace;
  readonly seed: string;
  readonly clock: string;
  readonly lastSequence: number;
  readonly resourceGraph: JsonObject;
  readonly providerProjections: Readonly<Record<string, JsonObject>>;
  readonly scheduledTransitions?: readonly JsonObject[];
  readonly hash: string;
}

export type CapabilityPlane =
  | 'deploy'
  | 'participant'
  | 'workload'
  | 'scoring'
  | 'operator'
  | 'access';

export type CapabilitySourceKind =
  | 'iac-resource'
  | 'iam-policy'
  | 'metadata-endpoint'
  | 'metadata-probe'
  | 'metadata-disruption'
  | 'code-analysis'
  | 'simulation-overlay';

export interface CapabilitySource {
  readonly kind: CapabilitySourceKind;
  readonly location: SourceLocation;
}

export interface CapabilityRequirement {
  readonly problemId: string;
  readonly targetId?: string;
  readonly provider: string;
  readonly engine: string;
  readonly entry?: string;
  readonly service: string;
  readonly resourceType: string;
  readonly operation: string;
  readonly requiredFidelity: readonly FidelityDimension[];
  readonly plane: CapabilityPlane;
  readonly source: CapabilitySource;
}

export type CapabilityCoverageStatus =
  | 'covered'
  | 'missing'
  | 'insufficient'
  | 'invalid';

export interface CapabilityCoverageEntry {
  readonly requirement: CapabilityRequirement;
  readonly status: CapabilityCoverageStatus;
  readonly implementedFidelity: readonly FidelityDimension[];
  readonly diagnostics: readonly SimulatorDiagnostic[];
}

export interface CapabilityCoverageSummary {
  readonly total: number;
  readonly covered: number;
  readonly missing: number;
  readonly insufficient: number;
  readonly invalid: number;
}

export interface CapabilityCoverageReport {
  readonly protocolVersion: typeof SIMULATOR_PROTOCOL_VERSION;
  readonly simulatorVersion: string;
  readonly catalogCommit: string;
  readonly reportHash: string;
  readonly supported: boolean;
  readonly summary: CapabilityCoverageSummary;
  readonly requirements: readonly CapabilityCoverageEntry[];
}

/** Compatibility projection consumed by TenkaCloud's initial local-play client. */
export interface SimulatorRequirementRow {
  readonly provider: string;
  readonly engine: string;
  readonly entry: string;
  readonly operation: 'deploy';
  readonly supported: boolean;
  readonly diagnostic?: string;
}

/** Compatibility projection consumed by TenkaCloud's initial local-play client. */
export interface SimulatorCapabilityReport {
  readonly protocolVersion: typeof SIMULATOR_PROTOCOL_VERSION;
  readonly supported: boolean;
  readonly requirements: readonly SimulatorRequirementRow[];
}
