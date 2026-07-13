import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import {
  assertCapabilityCoverageReport,
  type CapabilityCoverageEntry,
  type CapabilityCoverageReport,
  type CapabilityRequirement,
  type FidelityDimension,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorDiagnostic,
} from '@tenkacloud/simulator-contracts';
import { CatalogScannerError, errorMessage } from './errors.ts';
import type {
  CapabilityEntry,
  CapabilityManifest,
  CatalogInventory,
  CoverageReport,
  CoverageReportIdentity,
  CoveredRequirement,
  Fidelity,
  Requirement,
  RequirementCoverage,
} from './model.ts';
import { FIDELITIES } from './model.ts';
import {
  isFidelity,
  isRecord,
  recordValue,
  stringValue,
  unexpectedKeys,
} from './value.ts';

const FIDELITY_DIMENSION_BY_LEVEL: Readonly<
  Record<Fidelity, FidelityDimension>
> = {
  L0: 'contract',
  L1: 'control',
  L2: 'security',
  L3: 'network',
  L4: 'data-plane',
};

const CATALOG_COMMIT = /^[a-f0-9]{40}$/;

export function capabilityIdentity(entry: {
  provider: string;
  engine: string;
  service: string;
  resourceType: string;
  operation: string;
}): string {
  return [
    entry.provider,
    entry.engine,
    entry.service,
    entry.resourceType,
    entry.operation,
  ].join('|');
}

function fidelitySet(value: unknown, index: number): readonly Fidelity[] {
  if (!Array.isArray(value)) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] fidelity must be an array`
    );
  }
  if (value.length === 0) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] fidelity must be non-empty`
    );
  }
  if (!value.every(isFidelity)) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] has invalid fidelity`
    );
  }
  if (new Set(value).size !== value.length) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] fidelity must be unique`
    );
  }
  const canonical = FIDELITIES.filter((level) => value.includes(level));
  if (canonical.some((level, position) => level !== value[position])) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] fidelity must be in canonical L0..L4 order`
    );
  }
  return [...value];
}

function capabilityEntry(value: unknown, index: number): CapabilityEntry {
  if (!isRecord(value)) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] must be an object`
    );
  }
  const unknown = unexpectedKeys(value, [
    'provider',
    'engine',
    'service',
    'resourceType',
    'operation',
    'fidelity',
  ]);
  if (unknown.length > 0) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] has unknown fields: ${unknown.join(', ')}`
    );
  }
  const provider = stringValue(value, 'provider');
  const engine = stringValue(value, 'engine');
  const service = stringValue(value, 'service');
  const resourceType = stringValue(value, 'resourceType');
  const operation = stringValue(value, 'operation');
  const rawFidelity = recordValue(value, 'fidelity');
  if (
    provider === undefined ||
    engine === undefined ||
    service === undefined ||
    resourceType === undefined ||
    operation === undefined ||
    rawFidelity === undefined
  ) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] has missing fields or invalid fidelity`
    );
  }
  const fidelity = fidelitySet(rawFidelity, index);
  return { provider, engine, service, resourceType, operation, fidelity };
}

export function validateCapabilityManifest(value: unknown): CapabilityManifest {
  if (!isRecord(value)) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      'capability manifest root must be an object'
    );
  }
  const unknown = unexpectedKeys(value, [
    'schemaVersion',
    'version',
    'capabilities',
  ]);
  if (unknown.length > 0) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest has unknown fields: ${unknown.join(', ')}`
    );
  }
  if (recordValue(value, 'schemaVersion') !== '1') {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      'capability manifest schemaVersion must be "1"'
    );
  }
  const version = stringValue(value, 'version');
  const rawCapabilities = recordValue(value, 'capabilities');
  if (version === undefined || !Array.isArray(rawCapabilities)) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      'capability manifest requires version and capabilities'
    );
  }
  const capabilities = rawCapabilities.map(capabilityEntry);
  const seen = new Set<string>();
  for (const capability of capabilities) {
    const identity = capabilityIdentity(capability);
    if (seen.has(identity)) {
      throw new CatalogScannerError(
        'INVALID_CAPABILITY_MANIFEST',
        `capability manifest has duplicate identity: ${identity}`
      );
    }
    seen.add(identity);
  }
  capabilities.sort((left, right) =>
    capabilityIdentity(left).localeCompare(capabilityIdentity(right))
  );
  return { schemaVersion: '1', version, capabilities };
}

export async function readCapabilityManifest(
  path: string
): Promise<CapabilityManifest> {
  let contents: string;
  try {
    contents = await readFile(path, 'utf8');
  } catch (error) {
    throw new CatalogScannerError(
      'CAPABILITY_MANIFEST_READ_FAILED',
      `capability manifest could not be read: ${errorMessage(error)}`
    );
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(contents);
  } catch (error) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest JSON is invalid: ${errorMessage(error)}`
    );
  }
  return validateCapabilityManifest(parsed);
}

function requirementCoverage(
  requiredFidelity: readonly Fidelity[],
  capability: CapabilityEntry | undefined
): RequirementCoverage {
  if (capability === undefined) return { status: 'missing' };
  if (
    !requiredFidelity.every((fidelity) =>
      capability.fidelity.includes(fidelity)
    )
  ) {
    return { status: 'insufficient', availableFidelity: capability.fidelity };
  }
  return { status: 'covered', availableFidelity: capability.fidelity };
}

function manifestHash(manifest: CapabilityManifest): string {
  return createHash('sha256').update(JSON.stringify(manifest)).digest('hex');
}

function validateReportIdentity(
  identity: CoverageReportIdentity
): CoverageReportIdentity {
  if (!CATALOG_COMMIT.test(identity.catalogCommit)) {
    throw new CatalogScannerError(
      'INVALID_REPORT_IDENTITY',
      'catalog commit must be an immutable 40-character lowercase Git SHA'
    );
  }
  if (!identity.simulatorVersion.trim()) {
    throw new CatalogScannerError(
      'INVALID_REPORT_IDENTITY',
      'simulator version must not be empty'
    );
  }
  return identity;
}

function fidelityDimensions(
  fidelity: readonly Fidelity[]
): readonly FidelityDimension[] {
  return fidelity.map((level) => FIDELITY_DIMENSION_BY_LEVEL[level]);
}

function publicRequirement(requirement: Requirement): CapabilityRequirement {
  return {
    problemId: requirement.problemId,
    targetId: requirement.targetId,
    provider: requirement.provider,
    engine: requirement.engine,
    service: requirement.service,
    resourceType: requirement.resourceType,
    operation: requirement.operation,
    requiredFidelity: fidelityDimensions(requirement.fidelity),
    plane: requirement.plane,
    source: {
      kind: requirement.origin,
      location: {
        file: requirement.source.path,
        line: requirement.source.line,
      },
    },
  };
}

function coverageDiagnostic(
  requirement: Requirement,
  coverage: RequirementCoverage
): readonly SimulatorDiagnostic[] {
  if (coverage.status === 'covered') return [];
  const requiredFidelity = fidelityDimensions(requirement.fidelity);
  const availableFidelity =
    coverage.status === 'missing'
      ? []
      : fidelityDimensions(coverage.availableFidelity);
  return [
    {
      code:
        coverage.status === 'missing'
          ? 'MissingCapability'
          : 'InsufficientFidelity',
      message: `${capabilityIdentity(requirement)} is ${coverage.status}`,
      provider: requirement.provider,
      engine: requirement.engine,
      service: requirement.service,
      resourceType: requirement.resourceType,
      operation: requirement.operation,
      requiredFidelity,
      availableFidelity,
      source: {
        file: requirement.source.path,
        line: requirement.source.line,
      },
    },
  ];
}

function publicCoverageEntry(
  requirement: CoveredRequirement
): CapabilityCoverageEntry {
  return {
    requirement: publicRequirement(requirement),
    status: requirement.coverage.status,
    implementedFidelity:
      requirement.coverage.status === 'missing'
        ? []
        : fidelityDimensions(requirement.coverage.availableFidelity),
    diagnostics: coverageDiagnostic(requirement, requirement.coverage),
  };
}

type ReportWithoutHash = Omit<CoverageReport, 'reportHash'>;

function canonicalValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value)
        .sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0))
        .map(([key, child]) => [key, canonicalValue(child)])
    );
  }
  return value;
}

export function coverageReportHash(
  report: CapabilityCoverageReport | ReportWithoutHash
): string {
  const payload = Object.fromEntries(
    Object.entries(report).filter(([key]) => key !== 'reportHash')
  );
  return createHash('sha256')
    .update(JSON.stringify(canonicalValue(payload)))
    .digest('hex');
}

function coverageCounts(requirements: readonly CoveredRequirement[]): {
  requirements: number;
  covered: number;
  missing: number;
  insufficient: number;
} {
  return {
    requirements: requirements.length,
    covered: requirements.filter((entry) => entry.coverage.status === 'covered')
      .length,
    missing: requirements.filter((entry) => entry.coverage.status === 'missing')
      .length,
    insufficient: requirements.filter(
      (entry) => entry.coverage.status === 'insufficient'
    ).length,
  };
}

export function compareInventory(
  inventory: CatalogInventory,
  manifestInput: CapabilityManifest,
  reportIdentity: CoverageReportIdentity
): CoverageReport {
  const identity = validateReportIdentity(reportIdentity);
  const manifest = validateCapabilityManifest(manifestInput);
  if (identity.simulatorVersion !== manifest.version) {
    throw new CatalogScannerError(
      'INVALID_REPORT_IDENTITY',
      'simulator version must exactly match the capability manifest version'
    );
  }
  const capabilityByIdentity = new Map(
    manifest.capabilities.map((capability) => [
      capabilityIdentity(capability),
      capability,
    ])
  );
  const requirements: CoveredRequirement[] = inventory.requirements.map(
    (requirement) => ({
      ...requirement,
      coverage: requirementCoverage(
        requirement.fidelity,
        capabilityByIdentity.get(capabilityIdentity(requirement))
      ),
    })
  );
  const bindingRequirements = requirements.filter(
    (entry) => entry.classification === 'binding'
  );
  const authorizationRequirements = requirements.filter(
    (entry) => entry.classification === 'authorization-inventory'
  );
  const binding = coverageCounts(bindingRequirements);
  const authorizationInventory = coverageCounts(authorizationRequirements);
  const invalid = inventory.diagnostics.length;
  const supported =
    binding.missing === 0 && binding.insufficient === 0 && invalid === 0;
  const reportWithoutHash: ReportWithoutHash = {
    protocolVersion: SIMULATOR_PROTOCOL_VERSION,
    simulatorVersion: identity.simulatorVersion,
    catalogCommit: identity.catalogCommit,
    supported,
    summary: {
      total: binding.requirements,
      covered: binding.covered,
      missing: binding.missing,
      insufficient: binding.insufficient,
      invalid,
    },
    requirements: bindingRequirements.map(publicCoverageEntry),
    inventory: {
      catalogHash: inventory.catalogHash,
      capabilityManifest: {
        version: manifest.version,
        hash: manifestHash(manifest),
      },
      problems: inventory.problems,
      diagnostics: inventory.diagnostics,
      authorizationInventory: {
        summary: authorizationInventory,
        requirements: authorizationRequirements,
      },
    },
  };
  const report: CoverageReport = {
    ...reportWithoutHash,
    reportHash: coverageReportHash(reportWithoutHash),
  };
  assertCapabilityCoverageReport(report);
  return report;
}

export function serializeReport(report: CoverageReport): string {
  assertCapabilityCoverageReport(report);
  if (coverageReportHash(report) !== report.reportHash) {
    throw new CatalogScannerError(
      'INVALID_COVERAGE_REPORT',
      'coverage report hash does not match its canonical payload'
    );
  }
  return `${JSON.stringify(report, null, 2)}\n`;
}
