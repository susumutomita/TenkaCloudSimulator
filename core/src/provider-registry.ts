import type {
  CapabilityDiagnostic,
  CapabilityRequirement,
  ProviderCapability,
  ProviderModule,
} from './domain';
import { CoreError } from './errors';

function capabilityMatches(
  requirement: CapabilityRequirement,
  capability: ProviderCapability
): boolean {
  return (
    requirement.provider === capability.provider &&
    requirement.engine === capability.engine &&
    requirement.service === capability.service &&
    requirement.resourceType === capability.resourceType &&
    requirement.operation === capability.operation
  );
}

function missingFidelity(
  requirement: CapabilityRequirement,
  capability: ProviderCapability
): readonly string[] {
  return requirement.fidelity.filter(
    (level) => !capability.fidelity.includes(level)
  );
}

export class ProviderRegistry {
  readonly #modules = new Map<string, ProviderModule>();

  constructor(modules: readonly ProviderModule[] = []) {
    for (const module of modules) this.register(module);
  }

  register(module: ProviderModule): void {
    if (this.#modules.has(module.provider)) {
      throw new CoreError(
        'Conflict',
        `provider ${module.provider} is already registered`
      );
    }
    const ids = new Set<string>();
    const identities = new Set<string>();
    for (const capability of module.capabilities) {
      if (ids.has(capability.capabilityId)) {
        throw new CoreError(
          'Conflict',
          `capability ${capability.capabilityId} is duplicated`
        );
      }
      ids.add(capability.capabilityId);
      const identity = [
        capability.provider,
        capability.engine,
        capability.service,
        capability.resourceType,
        capability.operation,
      ].join('\u0000');
      if (identities.has(identity)) {
        throw new CoreError(
          'Conflict',
          `capability identity for ${capability.operation} is duplicated`
        );
      }
      identities.add(identity);
    }
    this.#modules.set(module.provider, module);
  }

  get(provider: string): ProviderModule | undefined {
    return this.#modules.get(provider);
  }

  modules(): readonly ProviderModule[] {
    return Array.from(this.#modules.values()).sort((left, right) =>
      left.provider.localeCompare(right.provider)
    );
  }

  capabilities(): readonly ProviderCapability[] {
    return this.modules()
      .flatMap((module) => module.capabilities)
      .sort((left, right) =>
        left.capabilityId.localeCompare(right.capabilityId)
      );
  }

  diagnose(
    requirements: readonly CapabilityRequirement[]
  ): readonly CapabilityDiagnostic[] {
    const diagnostics: CapabilityDiagnostic[] = [];
    for (const requirement of requirements) {
      const module = this.get(requirement.provider);
      if (!module) {
        diagnostics.push({
          ...requirement,
          code: 'MissingProvider',
          availableFidelity: [],
        });
        continue;
      }
      if (!module.engines.includes(requirement.engine)) {
        diagnostics.push({
          ...requirement,
          code: 'MissingEngine',
          availableFidelity: [],
        });
        continue;
      }
      const capability = module.capabilities.find((candidate) =>
        capabilityMatches(requirement, candidate)
      );
      if (!capability) {
        diagnostics.push({
          ...requirement,
          code: 'MissingCapability',
          availableFidelity: [],
        });
        continue;
      }
      if (missingFidelity(requirement, capability).length > 0) {
        diagnostics.push({
          ...requirement,
          code: 'InsufficientFidelity',
          availableFidelity: capability.fidelity,
        });
      }
    }
    return diagnostics;
  }
}
