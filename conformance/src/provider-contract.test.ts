import { describe, expect, it } from 'bun:test';
import type {
  ProviderCommandInput,
  ProviderCommandResult,
  ProviderCompileInput,
  ProviderDeploymentResult,
  ProviderModule,
  ProviderTargetPlan,
  ProviderWorldView,
} from '@tenkacloud/simulator-core';
import {
  assertProviderContract,
  inspectProviderContract,
} from './provider-contract';

function contractModule(
  overrides: Partial<ProviderModule> = {}
): ProviderModule {
  return {
    provider: 'contract-provider',
    engines: ['contract-engine'],
    capabilities: [
      {
        capabilityId: 'contract.deploy',
        provider: 'contract-provider',
        engine: 'contract-engine',
        service: 'contract-engine',
        resourceType: '*',
        operation: 'deploy',
        fidelity: ['L0'],
      },
    ],
    compile(_input: ProviderCompileInput): ProviderTargetPlan {
      return {
        targetId: 'contract-target',
        provider: 'contract-provider',
        engine: 'contract-engine',
        requirements: [],
        resources: [],
      };
    },
    deploy(
      _plan: ProviderTargetPlan,
      _world: ProviderWorldView
    ): ProviderDeploymentResult {
      return { events: [], resources: [], outputs: {} };
    },
    reduce(
      _command: ProviderCommandInput,
      _world: ProviderWorldView
    ): ProviderCommandResult {
      return {
        events: [],
        resources: [],
        deletedResourceIds: [],
        outputs: {},
        response: {},
      };
    },
    ...overrides,
  };
}

describe('provider contract 検査', () => {
  it('有効な module のとき finding を返さない', () => {
    const module = contractModule();
    expect(inspectProviderContract(module)).toEqual([]);
    expect(() => assertProviderContract(module)).not.toThrow();
  });

  it('identity と fidelity が不正なとき全 finding を返す', () => {
    const capability = contractModule().capabilities[0];
    if (!capability) throw new Error('contract capability is missing');
    const invalidCapability = { ...capability, provider: 'other' };
    Reflect.set(invalidCapability, 'fidelity', ['invalid']);
    const module = contractModule({
      provider: '',
      engines: [''],
      capabilities: [invalidCapability, { ...capability, provider: 'other' }],
    });
    expect(
      inspectProviderContract(module).map((finding) => finding.code)
    ).toEqual([
      'EmptyProvider',
      'EmptyEngine',
      'CapabilityProviderMismatch',
      'InvalidFidelity',
      'DuplicateCapability',
      'CapabilityProviderMismatch',
    ]);
    expect(() => assertProviderContract(module)).toThrow(
      'provider must not be empty'
    );
  });
});
