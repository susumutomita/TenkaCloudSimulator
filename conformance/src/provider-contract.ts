import type { FidelityLevel, ProviderModule } from '@tenkacloud/simulator-core';

export type ProviderContractFindingCode =
  | 'CapabilityProviderMismatch'
  | 'DuplicateCapability'
  | 'EmptyEngine'
  | 'EmptyProvider'
  | 'InvalidFidelity';

export interface ProviderContractFinding {
  readonly code: ProviderContractFindingCode;
  readonly message: string;
}

const VALID_FIDELITY = new Set<FidelityLevel>(['L0', 'L1', 'L2', 'L3', 'L4']);

export function inspectProviderContract(
  module: ProviderModule
): readonly ProviderContractFinding[] {
  const findings: ProviderContractFinding[] = [];
  if (!module.provider.trim()) {
    findings.push({
      code: 'EmptyProvider',
      message: 'provider must not be empty',
    });
  }
  if (module.engines.some((engine) => !engine.trim())) {
    findings.push({ code: 'EmptyEngine', message: 'engine must not be empty' });
  }
  const capabilityIds = new Set<string>();
  for (const capability of module.capabilities) {
    if (capabilityIds.has(capability.capabilityId)) {
      findings.push({
        code: 'DuplicateCapability',
        message: `capability ${capability.capabilityId} is duplicated`,
      });
    }
    capabilityIds.add(capability.capabilityId);
    if (capability.provider !== module.provider) {
      findings.push({
        code: 'CapabilityProviderMismatch',
        message: `capability ${capability.capabilityId} belongs to another provider`,
      });
    }
    if (capability.fidelity.some((level) => !VALID_FIDELITY.has(level))) {
      findings.push({
        code: 'InvalidFidelity',
        message: `capability ${capability.capabilityId} has invalid fidelity`,
      });
    }
  }
  return findings;
}

export function assertProviderContract(module: ProviderModule): void {
  const findings = inspectProviderContract(module);
  if (findings.length > 0) {
    throw new Error(findings.map((finding) => finding.message).join('; '));
  }
}
