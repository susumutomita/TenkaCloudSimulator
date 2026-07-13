import type {
  CapabilityCoverageReport,
  CapabilityCoverageSummary,
} from '@tenkacloud/simulator-contracts';

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
  fidelity: readonly Fidelity[];
  plane: Plane;
  origin: Origin;
  classification: RequirementClassification;
  source: SourceLocation;
}

export type DiagnosticCode =
  | 'ENTRY_OUTSIDE_PROBLEM'
  | 'ID_DIRECTORY_MISMATCH'
  | 'INVALID_APPRUN'
  | 'INVALID_BICEP'
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
  engine: string;
  service: string;
  resourceType: string;
  operation: string;
  fidelity: readonly Fidelity[];
}

export interface CapabilityManifest {
  schemaVersion: '1';
  version: string;
  capabilities: CapabilityEntry[];
}

export type RequirementCoverage =
  | { status: 'covered'; availableFidelity: readonly Fidelity[] }
  | { status: 'missing' }
  | { status: 'insufficient'; availableFidelity: readonly Fidelity[] };

export interface CoveredRequirement extends Requirement {
  coverage: RequirementCoverage;
}

export interface CoverageCounts {
  requirements: number;
  covered: number;
  missing: number;
  insufficient: number;
}

export interface CoverageReportIdentity {
  catalogCommit: string;
  simulatorVersion: string;
}

export interface CoverageReportInventory {
  catalogHash: string;
  capabilityManifest: {
    version: string;
    hash: string;
  };
  problems: ProblemInventory[];
  diagnostics: Diagnostic[];
  authorizationInventory: {
    summary: CoverageCounts;
    requirements: CoveredRequirement[];
  };
}

export interface CoverageReport extends CapabilityCoverageReport {
  summary: CapabilityCoverageSummary;
  inventory: CoverageReportInventory;
}

export interface CommandResult {
  exitCode: 0 | 1 | 2;
  stdout: string;
  stderr: string;
}
