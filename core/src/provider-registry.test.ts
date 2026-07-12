import { describe, expect, it } from 'bun:test';
import type {
  CapabilityRequirement,
  ProviderCapability,
  ProviderModule,
} from './domain';
import { CoreError } from './errors';
import { ProviderRegistry } from './provider-registry';

const ALPHA_CAPABILITY: ProviderCapability = {
  capabilityId: 'alpha-object-read',
  provider: 'alpha',
  engine: 'engine-a',
  service: 'objects',
  resourceType: 'Object',
  operation: 'read',
  fidelity: ['L0', 'L1'],
};

function createProviderModule(
  provider: string,
  engines: readonly string[],
  capabilities: readonly ProviderCapability[]
): ProviderModule {
  return {
    provider,
    engines,
    capabilities,
    compile: (input) => ({
      targetId: input.targetId,
      provider,
      engine: input.target.engine,
      requirements: [],
      resources: [],
    }),
    deploy: () => ({ events: [], resources: [], outputs: {} }),
    reduce: () => ({
      events: [],
      resources: [],
      deletedResourceIds: [],
      outputs: {},
      response: {},
    }),
  };
}

function captureCoreError(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('CoreError が発生しませんでした');
}

describe('ProviderRegistry の振る舞い', () => {
  it('登録済み module を取得し、capability ID 順で一覧を返す', () => {
    const later = {
      ...ALPHA_CAPABILITY,
      capabilityId: 'z-last',
      operation: 'write',
    };
    const earlier = { ...ALPHA_CAPABILITY, capabilityId: 'a-first' };
    const module = createProviderModule(
      'alpha',
      ['engine-a'],
      [later, earlier]
    );
    const beta = createProviderModule('beta', ['engine-b'], []);
    const registry = new ProviderRegistry([beta, module]);

    expect(registry.get('alpha')).toBe(module);
    expect(registry.get('missing')).toBeUndefined();
    expect(registry.modules()).toEqual([module, beta]);
    expect(registry.capabilities().map((item) => item.capabilityId)).toEqual([
      'a-first',
      'z-last',
    ]);
    expect(new ProviderRegistry().modules()).toEqual([]);
    expect(new ProviderRegistry().capabilities()).toEqual([]);
  });

  it('同じ provider を二重登録すると Conflict を返す', () => {
    const module = createProviderModule(
      'alpha',
      ['engine-a'],
      [ALPHA_CAPABILITY]
    );
    const registry = new ProviderRegistry([module]);

    const error = captureCoreError(() => registry.register(module));

    expect(error.code).toBe('Conflict');
    expect(error.name).toBe('CoreError');
    expect(error.diagnostics).toEqual([]);
  });

  it('同じ capability ID を module 内で二重登録すると Conflict を返す', () => {
    const duplicated = { ...ALPHA_CAPABILITY };

    const error = captureCoreError(
      () =>
        new ProviderRegistry([
          createProviderModule(
            'alpha',
            ['engine-a'],
            [ALPHA_CAPABILITY, duplicated]
          ),
        ])
    );

    expect(error.code).toBe('Conflict');
    expect(error.message).toContain(ALPHA_CAPABILITY.capabilityId);
  });

  it('同じ capability identity を別 ID で二重登録すると Conflict を返す', () => {
    const duplicatedIdentity = {
      ...ALPHA_CAPABILITY,
      capabilityId: 'different-id-for-the-same-identity',
    };

    const error = captureCoreError(
      () =>
        new ProviderRegistry([
          createProviderModule(
            'alpha',
            ['engine-a'],
            [ALPHA_CAPABILITY, duplicatedIdentity]
          ),
        ])
    );

    expect(error.code).toBe('Conflict');
  });

  it('provider、engine、capability、fidelity の不足を区別して返す', () => {
    const registry = new ProviderRegistry([
      createProviderModule('alpha', ['engine-a'], [ALPHA_CAPABILITY]),
    ]);
    const base: CapabilityRequirement = {
      provider: 'alpha',
      engine: 'engine-a',
      service: 'objects',
      resourceType: 'Object',
      operation: 'read',
      fidelity: ['L0'],
      source: { path: 'problem/template.yaml', line: 12 },
    };

    const diagnostics = registry.diagnose([
      { ...base, provider: 'missing' },
      { ...base, engine: 'missing-engine' },
      { ...base, operation: 'missing-operation' },
      { ...base, fidelity: ['L0', 'L2'] },
      base,
    ]);

    expect(diagnostics.map((item) => item.code)).toEqual([
      'MissingProvider',
      'MissingEngine',
      'MissingCapability',
      'InsufficientFidelity',
    ]);
    expect(diagnostics[0]?.availableFidelity).toEqual([]);
    expect(diagnostics[1]?.availableFidelity).toEqual([]);
    expect(diagnostics[2]?.availableFidelity).toEqual([]);
    expect(diagnostics[3]?.availableFidelity).toEqual(['L0', 'L1']);
    expect(diagnostics[3]?.source).toEqual({
      path: 'problem/template.yaml',
      line: 12,
    });
  });
});
