export const FIDELITIES = ['L0', 'L1', 'L2', 'L3', 'L4'] as const;

export type Fidelity = (typeof FIDELITIES)[number];

export type Plane =
  | 'deploy'
  | 'participant'
  | 'workload'
  | 'scoring'
  | 'operator'
  | 'access';

export type Origin =
  | 'iac-resource'
  | 'iam-policy'
  | 'metadata-endpoint'
  | 'metadata-probe'
  | 'metadata-disruption'
  | 'simulation-overlay';

export type RequirementClassification = 'binding' | 'authorization-inventory';

export interface SourceLocation {
  path: string;
  line: number;
  jsonPointer: string | null;
}

export interface Requirement {
  id: string;
  problemId: string;
  targetId: string;
  provider: string;
  engine: string;
  service: string;
  resourceType: string;
  operation: string;
  fidelity: Fidelity;
  plane: Plane;
  origin: Origin;
  classification: RequirementClassification;
  source: SourceLocation;
}

export type DiagnosticCode =
  | 'ENTRY_OUTSIDE_PROBLEM'
  | 'ID_DIRECTORY_MISMATCH'
  | 'INVALID_CLOUDFORMATION'
  | 'INVALID_METADATA'
  | 'INVALID_METADATA_JSON'
  | 'INVALID_RUNTIME'
  | 'INVALID_TERRAFORM'
  | 'MISSING_ENTRY'
  | 'INVALID_SIMULATION_OVERLAY'
  | 'NO_PROBLEMS'
  | 'TERRAFORM_PROVIDER_MISMATCH'
  | 'UNKNOWN_DISRUPTION_ACTION'
  | 'UNKNOWN_SCORING_KIND'
  | 'UNSUPPORTED_ENGINE';

export interface Diagnostic {
  code: DiagnosticCode;
  message: string;
  problemId: string | null;
  targetId: string | null;
  source: SourceLocation;
}

export interface NormalizedTarget {
  targetId: string;
  provider: string;
  engine: string;
  entry: string;
  delivery: 'cloud' | 'container';
}

export interface ProblemInventory {
  problemId: string;
  category: string;
  status: string;
  metadataPath: string;
  targets: NormalizedTarget[];
}

export interface CatalogInventory {
  schemaVersion: '1';
  catalogHash: string;
  problems: ProblemInventory[];
  requirements: Requirement[];
  diagnostics: Diagnostic[];
}

export interface CapabilityEntry {
  provider: string;
  service: string;
  resourceType: string;
  operation: string;
  fidelity: Fidelity;
}

export interface CapabilityManifest {
  schemaVersion: '1';
  version: string;
  capabilities: CapabilityEntry[];
}

export type RequirementCoverage =
  | { status: 'covered'; availableFidelity: Fidelity }
  | { status: 'missing' }
  | { status: 'insufficient'; availableFidelity: Fidelity };

export interface CoveredRequirement extends Requirement {
  coverage: RequirementCoverage;
}

export interface CoverageSummary {
  problems: number;
  targets: number;
  requirements: number;
  covered: number;
  missing: number;
  insufficient: number;
  authorizationInventory: {
    requirements: number;
    covered: number;
    missing: number;
    insufficient: number;
  };
  invalid: number;
}

export interface CoverageReport {
  schemaVersion: '1';
  status: 'covered' | 'failed';
  catalogHash: string;
  capabilityManifest: {
    version: string;
    hash: string;
  };
  problems: ProblemInventory[];
  requirements: CoveredRequirement[];
  diagnostics: Diagnostic[];
  summary: CoverageSummary;
}

export interface CommandResult {
  exitCode: 0 | 1 | 2;
  stdout: string;
  stderr: string;
}
