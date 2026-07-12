import {
  assertSimulatorCapabilities,
  type FidelityDimension,
  SIMULATOR_PROTOCOL_VERSION,
  type SimulatorCapabilities,
  type SimulatorCapability,
  type SimulatorEngineCapabilities,
  type SimulatorOperation,
} from '@tenkacloud/simulator-contracts';
import type {
  ProviderCapability,
  ProviderRegistry,
} from '@tenkacloud/simulator-core';
import { MAX_REQUEST_BODY_BYTES } from './errors.js';
import { fidelityDimensions } from './fidelity.js';

export const SIMULATOR_VERSION = '0.1.0';

interface EngineAccumulator {
  readonly operations: SimulatorOperation[];
  readonly resources: string[];
  readonly fidelity: FidelityDimension[];
}

interface ProviderAccumulator {
  readonly engines: Record<string, EngineAccumulator>;
}

function engineFor(
  providers: Record<string, ProviderAccumulator>,
  capability: ProviderCapability
): EngineAccumulator {
  let provider = providers[capability.provider];
  if (!provider) {
    provider = { engines: {} };
    providers[capability.provider] = provider;
  }
  let engine = provider.engines[capability.engine];
  if (!engine) {
    engine = { operations: ['deploy'], resources: [], fidelity: [] };
    provider.engines[capability.engine] = engine;
  }
  return engine;
}

function addUnique<T>(values: T[], additions: readonly T[]): void {
  for (const addition of additions) {
    if (!values.includes(addition)) values.push(addition);
  }
}

function detailedCapability(
  capability: ProviderCapability
): SimulatorCapability {
  return {
    provider: capability.provider,
    service: capability.service,
    resourceType: capability.resourceType,
    operation: capability.operation,
    fidelity: fidelityDimensions(capability.fidelity),
  };
}

export function simulatorCapabilities(
  registry: ProviderRegistry,
  workloadEffectsAvailable = false
): SimulatorCapabilities {
  const providerAccumulators: Record<string, ProviderAccumulator> = {};
  const providerCapabilities = registry.capabilities();
  const workloadCapabilities: ProviderCapability[] = workloadEffectsAvailable
    ? Array.from(
        new Map(
          providerCapabilities.map((capability) => [
            `${capability.provider}\u0000${capability.engine}`,
            capability,
          ])
        ).values()
      ).map((capability) => ({
        capabilityId: `runtime-workload-${capability.provider}-${capability.engine}`,
        provider: capability.provider,
        engine: capability.engine,
        service: 'runtime',
        resourceType: 'Runtime::Workload',
        operation: 'Materialize',
        fidelity: ['L4'],
      }))
    : [];
  const capabilities = [...providerCapabilities, ...workloadCapabilities];
  for (const capability of capabilities) {
    const engine = engineFor(providerAccumulators, capability);
    addUnique(engine.resources, [capability.resourceType]);
    addUnique(engine.fidelity, fidelityDimensions(capability.fidelity));
  }
  const providers = Object.fromEntries(
    Object.entries(providerAccumulators).map(([provider, value]) => [
      provider,
      {
        engines: Object.fromEntries(
          Object.entries(value.engines).map(([engine, accumulated]) => {
            const advertised: SimulatorEngineCapabilities = accumulated;
            return [engine, advertised];
          })
        ),
      },
    ])
  );
  const response: SimulatorCapabilities = {
    protocolVersion: SIMULATOR_PROTOCOL_VERSION,
    simulatorVersion: SIMULATOR_VERSION,
    providers,
    capabilities: capabilities.map(detailedCapability),
    constraints: { maxRequestBodyBytes: MAX_REQUEST_BODY_BYTES },
  };
  assertSimulatorCapabilities(response);
  return response;
}
