export const FIDELITY_LEVELS = ['L0', 'L1', 'L2', 'L3', 'L4'] as const;

export type FidelityLevel = (typeof FIDELITY_LEVELS)[number];

export interface WorldNamespace {
  readonly tenantId: string;
  readonly eventId: string;
  readonly teamId: string;
}

export interface CreateWorldInput extends WorldNamespace {
  readonly deploymentId: string;
  readonly seed?: string;
  readonly virtualTime?: string;
}

export interface WorldRecord extends WorldNamespace {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly seed: string;
  readonly virtualTime: string;
  readonly status: 'active' | 'deleted';
}

export interface SingleRuntimeTarget {
  readonly id?: string;
  readonly provider: string;
  readonly engine: string;
  readonly entry: string;
}

export interface CompositeRuntime {
  readonly kind: 'composite';
  readonly targets: readonly SingleRuntimeTarget[];
}

export type RuntimeDescriptor = SingleRuntimeTarget | CompositeRuntime;

export interface DeploymentInput {
  readonly deploymentId: string;
  readonly problemId: string;
  readonly runtime: RuntimeDescriptor;
  readonly templateBody: string;
  readonly metadata?: unknown;
  readonly simulationOverlay?: unknown;
}

export interface CapabilityRequirement {
  readonly provider: string;
  readonly engine: string;
  readonly service: string;
  readonly resourceType: string;
  readonly operation: string;
  readonly fidelity: readonly FidelityLevel[];
  readonly source?: {
    readonly path: string;
    readonly line?: number;
  };
}

export interface ProviderCapability extends CapabilityRequirement {
  readonly capabilityId: string;
}

export interface CapabilityDiagnostic extends CapabilityRequirement {
  readonly code:
    | 'MissingProvider'
    | 'MissingEngine'
    | 'MissingCapability'
    | 'InsufficientFidelity';
  readonly availableFidelity: readonly FidelityLevel[];
}

export interface ResourceDeclaration {
  readonly provider: string;
  readonly resourceType: string;
  readonly resourceId: string;
  readonly properties: Readonly<Record<string, unknown>>;
}

export interface ResourceRecord extends ResourceDeclaration {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
  readonly status: 'pending' | 'ready' | 'failed' | 'deleted';
}

export interface WorkloadDeclaration {
  readonly id: string;
  readonly targetId: string;
  readonly resourceRef: string;
  readonly image: string;
  readonly command?: readonly string[];
  readonly containerPort: number;
  readonly healthPath?: string;
}

export interface MaterializedWorkload {
  readonly worldId: string;
  readonly workloadId: string;
  readonly targetId: string;
  readonly resourceRef: string;
  readonly image: string;
  readonly healthPath: string;
  readonly endpoint?: string;
}

export interface WorkloadEffectPort {
  materialize(
    worldId: string,
    declarations: readonly WorkloadDeclaration[]
  ): Promise<readonly MaterializedWorkload[]>;
  cleanup(worldId: string): Promise<void>;
}

export interface ProviderEvent {
  readonly type: string;
  readonly payload: Readonly<Record<string, unknown>>;
}

export interface ProviderTargetPlan {
  readonly targetId: string;
  readonly provider: string;
  readonly engine: string;
  readonly requirements: readonly CapabilityRequirement[];
  readonly resources: readonly ResourceDeclaration[];
}

export interface ProviderSourceArtifact {
  readonly path: string;
  readonly content: string;
}

export interface ProviderDeploymentResult {
  readonly events: readonly ProviderEvent[];
  readonly resources: readonly ResourceDeclaration[];
  readonly outputs: Readonly<Record<string, string>>;
}

export interface ProviderCompileInput {
  readonly target: SingleRuntimeTarget;
  readonly targetId: string;
  readonly problemId: string;
  readonly templateBody: string;
  readonly artifacts: readonly ProviderSourceArtifact[];
  readonly metadata?: unknown;
  readonly simulationOverlay?: unknown;
}

export interface ProviderCommandInput {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly service: string;
  readonly operation: string;
  readonly resourceType: string;
  readonly input: Readonly<Record<string, unknown>>;
}

export interface ProviderCommandResult {
  readonly events: readonly ProviderEvent[];
  readonly resources: readonly ResourceDeclaration[];
  readonly deletedResourceIds: readonly string[];
  readonly outputs: Readonly<Record<string, string>>;
  readonly response: Readonly<Record<string, unknown>>;
}

export interface ProviderWorldView {
  readonly world: WorldRecord;
  readonly resources: readonly ResourceRecord[];
}

export interface ProviderClockInput {
  readonly previousVirtualTime: string;
  readonly virtualTime: string;
}

export interface ProviderClockResourceRef {
  readonly deploymentId: string;
  readonly targetId: string;
  readonly resourceId: string;
}

export interface ProviderClockResult {
  readonly events: readonly ProviderEvent[];
  readonly resources: readonly ResourceRecord[];
  readonly deletedResourceRefs: readonly ProviderClockResourceRef[];
  readonly appliedTransitionIds: readonly string[];
}

export interface AppliedTransition {
  readonly provider: string;
  readonly transitionId: string;
}

export interface ClockAdvanceResult extends WorldRecord {
  readonly appliedTransitions: readonly AppliedTransition[];
}

export interface ProviderModule {
  readonly provider: string;
  readonly engines: readonly string[];
  readonly capabilities: readonly ProviderCapability[];
  compile(input: ProviderCompileInput): ProviderTargetPlan;
  deploy(
    plan: ProviderTargetPlan,
    world: ProviderWorldView
  ): ProviderDeploymentResult;
  reduce(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): ProviderCommandResult;
  reduceAsync?(
    command: ProviderCommandInput,
    world: ProviderWorldView
  ): Promise<ProviderCommandResult>;
  advanceClock?(
    input: ProviderClockInput,
    world: ProviderWorldView
  ): ProviderClockResult;
}

export interface DeploymentRecord {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly problemId: string;
  readonly status: 'deploying' | 'ready' | 'failed' | 'rejected' | 'deleted';
  readonly targets: readonly DeploymentTargetIdentity[];
  readonly outputs: Readonly<Record<string, Readonly<Record<string, string>>>>;
  readonly diagnostics: readonly CapabilityDiagnostic[];
}

export interface DeploymentTargetIdentity {
  readonly id: string;
  readonly provider: string;
  readonly engine: string;
}

export interface EventRecord {
  readonly worldId: string;
  readonly sequence: number;
  readonly type: string;
  readonly virtualTime: string;
  readonly commandId: string;
  readonly payload: Readonly<Record<string, unknown>>;
  readonly payloadHash: string;
}

export interface ExecuteCommandInput {
  readonly deploymentId: string;
  readonly targetId: string;
  readonly provider: string;
  readonly engine: string;
  readonly service: string;
  readonly operation: string;
  readonly resourceType: string;
  readonly input: Readonly<Record<string, unknown>>;
}

export interface SnapshotPayload {
  readonly snapshotVersion: '1';
  readonly world: WorldRecord;
  readonly events: readonly EventRecord[];
  readonly deployments: readonly DeploymentRecord[];
  readonly resources: readonly ResourceRecord[];
}

export interface WorldSnapshot {
  readonly payload: SnapshotPayload;
  readonly hash: string;
}
