import {
  type CapabilityEntry,
  type CapabilityManifest,
  validateCapabilityManifest,
} from '@tenkacloud/catalog-scanner';
import type {
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

function manifestCapability(capability: ProviderCapability): CapabilityEntry {
  return {
    provider: capability.provider,
    engine: capability.engine,
    service: capability.service,
    resourceType: capability.resourceType,
    operation: capability.operation,
    fidelity: [...capability.fidelity],
  };
}

function workloadCapabilities(
  modules: readonly ProviderModule[]
): readonly CapabilityEntry[] {
  return modules.flatMap((module) =>
    module.engines.map((engine) => ({
      provider: module.provider,
      engine,
      service: 'runtime',
      resourceType: 'Runtime::Workload',
      operation: 'Materialize',
      fidelity: ['L4'],
    }))
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

export function simulatorCapabilityManifest(
  sourceCommit: string
): CapabilityManifest {
  if (!/^[0-9a-f]{40}$/.test(sourceCommit)) {
    throw new Error('sourceCommit must be an immutable 40-character Git SHA');
  }
  const modules = [
    new AwsProvider(),
    new AzureProvider(),
    new GcpProvider(),
    new SakuraProvider(),
  ];
  return createCapabilityManifest(
    modules.slice(1).flatMap((module) => module.capabilities),
    `tenkacloud-simulator-0.1.0+git.${sourceCommit}`,
    [
      ...AWS_CATALOG_CAPABILITY_MANIFEST.capabilities,
      ...workloadCapabilities(modules),
    ]
  );
}
