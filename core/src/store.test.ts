import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import type {
  CapabilityDiagnostic,
  DeploymentRecord,
  ResourceRecord,
  WorldRecord,
} from './domain';
import { CoreError } from './errors';
import { SimulationStore } from './store';

function worldRecord(worldId = 'world-one'): WorldRecord {
  return {
    worldId,
    tenantId: 'tenant-a',
    eventId: 'event-a',
    teamId: 'team-a',
    deploymentId: 'deployment-a',
    seed: 'seed-a',
    virtualTime: '2026-07-12T00:00:00.000Z',
    status: 'active',
  };
}

function deploymentRecord(
  worldId: string,
  deploymentId: string,
  status: DeploymentRecord['status'] = 'ready',
  diagnostics: readonly CapabilityDiagnostic[] = []
): DeploymentRecord {
  return {
    worldId,
    deploymentId,
    problemId: `problem-${deploymentId}`,
    status,
    outputs: { default: { endpoint: `https://${deploymentId}.example` } },
    diagnostics,
  };
}

function resourceRecord(
  worldId: string,
  resourceId: string,
  provider = 'alpha'
): ResourceRecord {
  return {
    worldId,
    deploymentId: 'deployment-a',
    provider,
    resourceType: 'Object',
    resourceId,
    properties: { enabled: true, nested: { z: 2, a: 1 } },
    status: 'ready',
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

describe('SimulationStore の振る舞い', () => {
  let directory = '';
  let store: SimulationStore;

  beforeEach(() => {
    directory = mkdtempSync(path.join(tmpdir(), 'simulation-store-'));
    store = new SimulationStore(path.join(directory, 'simulation.sqlite'));
  });

  afterEach(() => {
    store.close();
    rmSync(directory, { recursive: true, force: true });
  });

  it('transaction が成功すると実 SQLite に commit し、失敗すると rollback する', () => {
    const committed = store.transaction(() => {
      const world = worldRecord('committed-world');
      store.insertWorld(world);
      return world.worldId;
    });

    expect(committed).toBe('committed-world');
    expect(store.world(committed)?.status).toBe('active');
    expect(() =>
      store.transaction(() => {
        store.insertWorld(worldRecord('rolled-back-world'));
        throw new Error('rollback');
      })
    ).toThrow('rollback');
    expect(store.world('rolled-back-world')).toBeUndefined();
  });

  it('idempotency response を canonical JSON で保存し、別 request の key 再利用を拒否する', () => {
    const request = { z: 2, a: 1 };
    const response = { worldId: 'world-one', nested: { b: 2, a: 1 } };

    expect(
      store.idempotent<typeof response>('scope', 'key', request)
    ).toBeUndefined();
    store.saveIdempotent('scope', 'key', request, response);
    expect(
      store.idempotent<typeof response>('scope', 'key', { a: 1, z: 2 })
    ).toEqual(response);

    const error = captureCoreError(() =>
      store.idempotent('scope', 'key', { a: 9, z: 2 })
    );
    expect(error.code).toBe('IdempotencyConflict');
  });

  it('world の clock と削除状態を永続化する', () => {
    const world = worldRecord();
    store.insertWorld(world);

    store.setWorldState(world.worldId, '2030-01-02T03:04:05.000Z', 'deleted');

    expect(store.world(world.worldId)).toEqual({
      ...world,
      virtualTime: '2030-01-02T03:04:05.000Z',
      status: 'deleted',
    });
    expect(store.world('missing-world')).toBeUndefined();
  });

  it('event sequence、virtual time、payload hash を world ごとに永続化する', () => {
    const firstWorld = worldRecord('world-one');
    const secondWorld = worldRecord('world-two');
    store.insertWorld(firstWorld);
    store.insertWorld(secondWorld);
    store.setWorldState(
      firstWorld.worldId,
      '2026-07-12T00:00:01.000Z',
      'active'
    );

    const first = store.appendEvent(
      firstWorld.worldId,
      'ObjectCreated',
      'command-one',
      { z: 2, a: 1 }
    );
    const second = store.appendEvent(
      firstWorld.worldId,
      'ObjectUpdated',
      'command-two',
      { value: 2 }
    );
    store.appendEvent(secondWorld.worldId, 'OtherWorldEvent', 'command-three', {
      isolated: true,
    });

    expect(first.sequence).toBe(1);
    expect(second.sequence).toBe(2);
    expect(first.virtualTime).toBe('2026-07-12T00:00:01.000Z');
    expect(first.payloadHash).toMatch(/^[a-f0-9]{64}$/);
    expect(store.events(firstWorld.worldId)).toEqual([first, second]);
    expect(store.events(secondWorld.worldId)).toHaveLength(1);
  });

  it('存在しない world への event append を NotFound にする', () => {
    const error = captureCoreError(() =>
      store.appendEvent('missing-world', 'Event', 'command', {})
    );

    expect(error.code).toBe('NotFound');
  });

  it('event の保存 JSON が object でなければ ValidationFailed にする', () => {
    const world = worldRecord();
    store.insertWorld(world);
    store.database
      .query('INSERT INTO events VALUES (?, ?, ?, ?, ?, ?, ?)')
      .run(
        world.worldId,
        1,
        'InvalidPayload',
        world.virtualTime,
        'command',
        '[]',
        'invalid-hash'
      );

    const error = captureCoreError(() => store.events(world.worldId));

    expect(error.code).toBe('ValidationFailed');
  });

  it('deployment を insert、update、sort し、全 status を復元する', () => {
    const world = worldRecord();
    store.insertWorld(world);
    const later = deploymentRecord(world.worldId, 'z-deployment');
    const earlier = deploymentRecord(world.worldId, 'a-deployment');
    store.saveDeployment(later);
    store.saveDeployment(earlier);

    expect(store.deployment(world.worldId, 'missing')).toBeUndefined();
    expect(
      store
        .deployments(world.worldId)
        .map((deployment) => deployment.deploymentId)
    ).toEqual(['a-deployment', 'z-deployment']);

    const diagnostic: CapabilityDiagnostic = {
      provider: 'alpha',
      engine: 'engine-a',
      service: 'objects',
      resourceType: 'Object',
      operation: 'read',
      fidelity: ['L2'],
      code: 'InsufficientFidelity',
      availableFidelity: ['L0'],
    };
    store.saveDeployment({
      ...earlier,
      status: 'rejected',
      diagnostics: [diagnostic],
    });
    expect(store.deployment(world.worldId, earlier.deploymentId)?.status).toBe(
      'rejected'
    );
    expect(
      store.deployment(world.worldId, earlier.deploymentId)?.diagnostics
    ).toEqual([diagnostic]);

    for (const status of ['deploying', 'failed'] as const) {
      store.saveDeployment({ ...earlier, status });
      expect(
        store.deployment(world.worldId, earlier.deploymentId)?.status
      ).toBe(status);
    }

    store.saveDeployment({ ...later, status: 'deleted' });
    expect(store.deployment(world.worldId, later.deploymentId)?.status).toBe(
      'deleted'
    );
  });

  it('resource を upsert、provider 順で取得し、論理削除する', () => {
    const world = worldRecord();
    store.insertWorld(world);
    const alpha = resourceRecord(world.worldId, 'z-resource', 'alpha');
    const beta = resourceRecord(world.worldId, 'a-resource', 'beta');
    store.saveResource(beta);
    store.saveResource(alpha);
    store.saveResource({
      ...alpha,
      deploymentId: 'deployment-updated',
      resourceType: 'UpdatedObject',
      properties: { enabled: false },
    });

    expect(store.resources(world.worldId)).toEqual([
      {
        ...alpha,
        deploymentId: 'deployment-updated',
        resourceType: 'UpdatedObject',
        properties: { enabled: false },
      },
      beta,
    ]);

    for (const status of ['pending', 'failed'] as const) {
      store.saveResource({ ...beta, status });
      expect(
        store
          .resources(world.worldId)
          .find((resource) => resource.resourceId === beta.resourceId)?.status
      ).toBe(status);
    }

    store.deleteResource(world.worldId, alpha.provider, alpha.resourceId);
    expect(store.resources(world.worldId)[0]?.status).toBe('deleted');
  });

  it('resource の保存 JSON が object でなければ ValidationFailed にする', () => {
    const world = worldRecord();
    store.insertWorld(world);
    store.database
      .query('INSERT INTO resources VALUES (?, ?, ?, ?, ?, ?, ?)')
      .run(
        world.worldId,
        'deployment-a',
        'alpha',
        'Object',
        'invalid-resource',
        'null',
        'ready'
      );

    const error = captureCoreError(() => store.resources(world.worldId));

    expect(error.code).toBe('ValidationFailed');
  });
});
