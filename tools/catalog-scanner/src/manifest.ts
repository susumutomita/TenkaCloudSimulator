import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { CatalogScannerError, errorMessage } from './errors.ts';
import type {
  CapabilityEntry,
  CapabilityManifest,
  CatalogInventory,
  CoverageReport,
  CoveredRequirement,
  Fidelity,
  RequirementCoverage,
} from './model.ts';
import {
  isFidelity,
  isRecord,
  recordValue,
  stringValue,
  unexpectedKeys,
} from './value.ts';

const FIDELITY_RANK: Record<Fidelity, number> = {
  L0: 0,
  L1: 1,
  L2: 2,
  L3: 3,
  L4: 4,
};

export function capabilityIdentity(entry: {
  provider: string;
  service: string;
  resourceType: string;
  operation: string;
}): string {
  return [
    entry.provider,
    entry.service,
    entry.resourceType,
    entry.operation,
  ].join('|');
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
  const service = stringValue(value, 'service');
  const resourceType = stringValue(value, 'resourceType');
  const operation = stringValue(value, 'operation');
  const fidelity = recordValue(value, 'fidelity');
  if (
    provider === undefined ||
    service === undefined ||
    resourceType === undefined ||
    operation === undefined ||
    !isFidelity(fidelity)
  ) {
    throw new CatalogScannerError(
      'INVALID_CAPABILITY_MANIFEST',
      `capability manifest capabilities[${index}] has missing fields or invalid fidelity`
    );
  }
  return { provider, service, resourceType, operation, fidelity };
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
  requiredFidelity: Fidelity,
  capability: CapabilityEntry | undefined
): RequirementCoverage {
  if (capability === undefined) return { status: 'missing' };
  if (FIDELITY_RANK[capability.fidelity] < FIDELITY_RANK[requiredFidelity]) {
    return { status: 'insufficient', availableFidelity: capability.fidelity };
  }
  return { status: 'covered', availableFidelity: capability.fidelity };
}

function manifestHash(manifest: CapabilityManifest): string {
  return createHash('sha256').update(JSON.stringify(manifest)).digest('hex');
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
  manifestInput: CapabilityManifest
): CoverageReport {
  const manifest = validateCapabilityManifest(manifestInput);
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
  const binding = coverageCounts(
    requirements.filter((entry) => entry.classification === 'binding')
  );
  const authorizationInventory = coverageCounts(
    requirements.filter(
      (entry) => entry.classification === 'authorization-inventory'
    )
  );
  const invalid = inventory.diagnostics.length;
  const summary = {
    problems: inventory.problems.length,
    targets: inventory.problems.reduce(
      (sum, problem) => sum + problem.targets.length,
      0
    ),
    ...binding,
    authorizationInventory,
    invalid,
  };
  return {
    schemaVersion: '1',
    status:
      binding.missing === 0 && binding.insufficient === 0 && invalid === 0
        ? 'covered'
        : 'failed',
    catalogHash: inventory.catalogHash,
    capabilityManifest: {
      version: manifest.version,
      hash: manifestHash(manifest),
    },
    problems: inventory.problems,
    requirements,
    diagnostics: inventory.diagnostics,
    summary,
  };
}

export function serializeReport(report: CoverageReport): string {
  return `${JSON.stringify(report, null, 2)}\n`;
}
