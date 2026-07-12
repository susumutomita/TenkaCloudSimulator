import {
  type CapabilityEntry,
  type CapabilityManifest,
  type Fidelity,
  validateCapabilityManifest,
} from '@tenkacloud/catalog-scanner';
import type {
  FidelityLevel,
  ProviderCapability,
  ProviderModule,
} from '@tenkacloud/simulator-core';
import {
  AWS_CATALOG_CAPABILITY_MANIFEST,
  AwsProvider,
} from '@tenkacloud/simulator-provider-aws';
import { AzureProvider } from '@tenkacloud/simulator-provider-azure';
import { GcpProvider } from '@tenkacloud/simulator-provider-gcp';
import { SakuraProvider } from '@tenkacloud/simulator-provider-sakura';

const FIDELITY_RANK: Readonly<Record<FidelityLevel, number>> = {
  L0: 0,
  L1: 1,
  L2: 2,
  L3: 3,
  L4: 4,
};

function maximumFidelity(levels: readonly FidelityLevel[]): Fidelity {
  return (
    [...levels].sort(
      (left, right) => FIDELITY_RANK[right] - FIDELITY_RANK[left]
    )[0] ?? 'L0'
  );
}

function manifestCapability(capability: ProviderCapability): CapabilityEntry {
  return {
    provider: capability.provider,
    service: capability.service,
    resourceType: capability.resourceType,
    operation: capability.operation,
    fidelity: maximumFidelity(capability.fidelity),
  };
}

function workloadCapabilities(
  modules: readonly ProviderModule[]
): readonly CapabilityEntry[] {
  return [...new Set(modules.map((module) => module.provider))].map(
    (provider) => ({
      provider,
      service: 'runtime',
      resourceType: 'Runtime::Workload',
      operation: 'Materialize',
      fidelity: 'L4',
    })
  );
}

export function createCapabilityManifest(
  capabilities: readonly ProviderCapability[],
  version: string,
  baseCapabilities: readonly CapabilityEntry[] = []
): CapabilityManifest {
  return validateCapabilityManifest({
    schemaVersion: '1',
    version,
    capabilities: [
      ...baseCapabilities,
      ...capabilities.map(manifestCapability),
    ],
  });
}

export function simulatorCapabilityManifest(): CapabilityManifest {
  const modules = [
    new AwsProvider(),
    new AzureProvider(),
    new GcpProvider(),
    new SakuraProvider(),
  ];
  return createCapabilityManifest(
    modules.slice(1).flatMap((module) => module.capabilities),
    'tenkacloud-simulator-0.1.0',
    [
      ...AWS_CATALOG_CAPABILITY_MANIFEST.capabilities,
      ...workloadCapabilities(modules),
    ]
  );
}
