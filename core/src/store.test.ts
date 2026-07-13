import { Database } from 'bun:sqlite';
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
    targets: [{ id: 'default', provider: 'alpha', engine: 'engine-a' }],
    outputs: { default: { endpoint: `https://${deploymentId}.example` } },
    diagnostics,
  };
}

function resourceRecord(
  worldId: string,
  resourceId: string,
  provider = 'alpha',
  targetId = 'default'
): ResourceRecord {
  return {
    worldId,
    deploymentId: 'deployment-a',
    targetId,
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

function createLegacyTargetlessSchema(database: Database): void {
  database.exec(`
    CREATE TABLE worlds (
      world_id TEXT PRIMARY KEY,
      tenant_id TEXT NOT NULL,
      event_id TEXT NOT NULL,
      team_id TEXT NOT NULL,
      deployment_id TEXT NOT NULL,
      seed TEXT NOT NULL,
      virtual_time TEXT NOT NULL,
      status TEXT NOT NULL,
      next_sequence INTEGER NOT NULL
    );
    CREATE TABLE events (
      world_id TEXT NOT NULL,
      sequence INTEGER NOT NULL,
      type TEXT NOT NULL,
      virtual_time TEXT NOT NULL,
      command_id TEXT NOT NULL,
      payload TEXT NOT NULL,
      payload_hash TEXT NOT NULL,
      PRIMARY KEY (world_id, sequence),
      FOREIGN KEY (world_id) REFERENCES worlds(world_id)
    );
    CREATE TABLE deployments (
      world_id TEXT NOT NULL,
      deployment_id TEXT NOT NULL,
      problem_id TEXT NOT NULL,
      status TEXT NOT NULL,
      outputs TEXT NOT NULL,
      diagnostics TEXT NOT NULL,
      PRIMARY KEY (world_id, deployment_id),
      FOREIGN KEY (world_id) REFERENCES worlds(world_id)
    );
    CREATE TABLE resources (
      world_id TEXT NOT NULL,
      deployment_id TEXT NOT NULL,
      provider TEXT NOT NULL,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      properties TEXT NOT NULL,
      status TEXT NOT NULL,
      PRIMARY KEY (world_id, provider, resource_id),
      FOREIGN KEY (world_id) REFERENCES worlds(world_id)
    );
    CREATE TABLE idempotency (
      scope TEXT NOT NULL,
      key TEXT NOT NULL,
      request_hash TEXT NOT NULL,
      response TEXT NOT NULL,
      PRIMARY KEY (scope, key)
    )
  `);
}

function replayDeploymentTargetAlter(
  database: Database,
  defaultValue: '[]' | '{}'
): void {
  database.exec(`
    PRAGMA foreign_keys = OFF;
    DROP TABLE event_reservations;
    ALTER TABLE deployments RENAME TO deployments_canonical;
    CREATE TABLE deployments (
      world_id TEXT NOT NULL,
      deployment_id TEXT NOT NULL,
      problem_id TEXT NOT NULL,
      status TEXT NOT NULL,
      outputs TEXT NOT NULL,
      diagnostics TEXT NOT NULL,
      PRIMARY KEY (world_id, deployment_id),
      FOREIGN KEY (world_id) REFERENCES worlds(world_id)
    );
    INSERT INTO deployments (
      world_id, deployment_id, problem_id, status, outputs, diagnostics
    ) SELECT
      world_id, deployment_id, problem_id, status, outputs, diagnostics
    FROM deployments_canonical;
    DROP TABLE deployments_canonical;
    ALTER TABLE deployments
      ADD COLUMN targets TEXT NOT NULL DEFAULT '${defaultValue}';
    PRAGMA user_version = 0;
  `);
}

function createCurrentEventReservationsTable(database: Database): void {
  database.exec(`
    CREATE TABLE event_reservations (
      world_id TEXT NOT NULL,
      reservation_id TEXT NOT NULL,
      operation_kind TEXT NOT NULL CHECK (operation_kind IN ('materialization', 'deletion')),
      owner_id TEXT NOT NULL,
      event_count INTEGER NOT NULL CHECK (event_count > 0),
      PRIMARY KEY (world_id, reservation_id),
      FOREIGN KEY (world_id) REFERENCES worlds(world_id)
    )
  `);
}

interface SchemaSnapshotRow {
  readonly type: string;
  readonly name: string;
  readonly tbl_name: string;
  readonly sql: string | null;
}

interface IdempotencySnapshotRow {
  readonly scope: string;
  readonly key: string;
  readonly request_hash: string;
  readonly response: string;
}

interface IdempotencySchemaVariant {
  readonly keyDefinition?: string;
  readonly tableConstraint?: string;
  readonly tableOptions?: string;
}

function schemaSnapshot(database: Database): readonly SchemaSnapshotRow[] {
  return database
    .query<SchemaSnapshotRow, []>(
      `SELECT type, name, tbl_name, sql
       FROM sqlite_master
       ORDER BY type, name`
    )
    .all();
}

type PersistedSqliteRow = Readonly<Record<string, string | number | null>>;

function tableRowsSnapshot(
  database: Database,
  tableName:
    | 'worlds'
    | 'events'
    | 'deployments'
    | 'idempotency'
    | 'resources'
    | 'event_reservations'
): readonly PersistedSqliteRow[] {
  return database
    .query<PersistedSqliteRow, []>(`SELECT * FROM ${tableName} ORDER BY rowid`)
    .all();
}

function databaseStateSnapshot(database: Database) {
  return {
    schema: schemaSnapshot(database),
    rows: {
      worlds: tableRowsSnapshot(database, 'worlds'),
      events: tableRowsSnapshot(database, 'events'),
      deployments: tableRowsSnapshot(database, 'deployments'),
      idempotency: tableRowsSnapshot(database, 'idempotency'),
      resources: tableRowsSnapshot(database, 'resources'),
      eventReservations: tableRowsSnapshot(database, 'event_reservations'),
    },
    userVersion: database
      .query<{ user_version: number }, []>('PRAGMA user_version')
      .get()?.user_version,
  };
}

function expectUnversionedHybridRejectedUnchanged(databasePath: string): void {
  const beforeDatabase = new Database(databasePath, { readonly: true });
  const before = databaseStateSnapshot(beforeDatabase);
  beforeDatabase.close();

  const error = captureCoreError(() => {
    const accepted = new SimulationStore(databasePath);
    accepted.close();
  });

  expect(error.code).toBe('ValidationFailed');
  expect(error.message).toContain('partial or incompatible');
  const rejected = new Database(databasePath, { readonly: true });
  expect(databaseStateSnapshot(rejected)).toEqual(before);
  rejected.close();
}

function idempotencySnapshot(
  database: Database
): readonly IdempotencySnapshotRow[] {
  return database
    .query<IdempotencySnapshotRow, []>(
      `SELECT scope, key, request_hash, response
       FROM idempotency
       ORDER BY scope, key`
    )
    .all();
}

function replaceIdempotencySchema(
  database: Database,
  variant: IdempotencySchemaVariant
): void {
  const keyDefinition = variant.keyDefinition ?? 'TEXT NOT NULL';
  const tableConstraint = variant.tableConstraint
    ? `${variant.tableConstraint},`
    : '';
  const tableOptions = variant.tableOptions ? ` ${variant.tableOptions}` : '';
  database.exec(`
    ALTER TABLE idempotency RENAME TO idempotency_original;
    CREATE TABLE idempotency (
      scope TEXT NOT NULL,
      key ${keyDefinition},
      request_hash TEXT NOT NULL,
      response TEXT NOT NULL,
      ${tableConstraint}
      PRIMARY KEY (scope, key)
    )${tableOptions};
    INSERT INTO idempotency (scope, key, request_hash, response)
    SELECT scope, key, request_hash, response
    FROM idempotency_original;
    DROP TABLE idempotency_original;
    PRAGMA user_version = 2;
  `);
}

function expectIdempotencySchemaRejectedUnchanged(
  databasePath: string,
  variant: IdempotencySchemaVariant
): void {
  const seeded = new SimulationStore(databasePath);
  seeded.saveIdempotent(
    'scope-a',
    'key',
    { request: 'preserved' },
    {
      response: 'preserved',
    }
  );
  seeded.close();
  const invalid = new Database(databasePath);
  replaceIdempotencySchema(invalid, variant);
  const schemaBefore = schemaSnapshot(invalid);
  const rowsBefore = idempotencySnapshot(invalid);
  const versionBefore = invalid
    .query<{ user_version: number }, []>('PRAGMA user_version')
    .get()?.user_version;
  invalid.close();

  const error = captureCoreError(() => {
    const accepted = new SimulationStore(databasePath);
    accepted.close();
  });

  expect(error.code).toBe('ValidationFailed');
  expect(error.message).toContain('schema');
  expect(rowsBefore).toHaveLength(1);
  const rejected = new Database(databasePath, { readonly: true });
  expect(schemaSnapshot(rejected)).toEqual(schemaBefore);
  expect(idempotencySnapshot(rejected)).toEqual(rowsBefore);
  expect(
    rejected.query<{ user_version: number }, []>('PRAGMA user_version').get()
      ?.user_version
  ).toBe(versionBefore);
  rejected.close();
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

  it('immediate outer transaction 内で nested savepoint rollback を保持する', () => {
    const world = worldRecord('nested-savepoint-world');

    store.transaction(() => {
      store.insertWorld(world);
      expect(() =>
        store.transaction(() => {
          store.appendEvent(world.worldId, 'NestedEvent', 'nested-command', {});
          throw new Error('nested rollback');
        })
      ).toThrow('nested rollback');
      expect(store.events(world.worldId)).toEqual([]);
    });

    expect(store.world(world.worldId)).toEqual(world);
    expect(store.events(world.worldId)).toEqual([]);
  });

  it('bounded SQLite write contention を retryable domain conflict に変換する', () => {
    const locker = new SimulationStore(
      path.join(directory, 'simulation.sqlite')
    );
    locker.database.exec('BEGIN IMMEDIATE');
    store.database.exec('PRAGMA busy_timeout = 1');

    try {
      const error = captureCoreError(() =>
        store.transaction(() =>
          store.insertWorld(worldRecord('contended-world'))
        )
      );
      expect(error.code).toBe('Conflict');
      expect(error.message).toContain('busy');
      expect(store.world('contended-world')).toBeUndefined();
    } finally {
      locker.database.exec('ROLLBACK');
      locker.close();
    }
  });

  it('fresh schema を current user_version として一つの migration transaction で初期化する', () => {
    const version = store.database
      .query<{ user_version: number }, []>('PRAGMA user_version')
      .get()?.user_version;

    expect(version).toBe(2);
  });

  it('exact current 6-table schema は unversioned でも rows を保持して version 2 へ移行する', () => {
    const currentPath = path.join(directory, 'unversioned-current.sqlite');
    const seeded = new SimulationStore(currentPath);
    const world = worldRecord('unversioned-current-world');
    seeded.insertWorld(world);
    seeded.saveIdempotent(
      'unversioned-current-scope',
      'key',
      { request: 'preserved' },
      { response: 'preserved' }
    );
    seeded.close();
    const unversioned = new Database(currentPath);
    unversioned.exec('PRAGMA user_version = 0');
    const before = databaseStateSnapshot(unversioned);
    unversioned.close();

    const migrated = new SimulationStore(currentPath);

    const after = databaseStateSnapshot(migrated.database);
    expect(after.schema).toEqual(before.schema);
    expect(after.rows).toEqual(before.rows);
    expect(after.userVersion).toBe(2);
    expect(migrated.world(world.worldId)).toEqual(world);
    migrated.close();
  });

  it('unversioned legacy または historical 5-table と current reservation table の hybrid を完全不変で拒否する', () => {
    const legacyPath = path.join(directory, 'legacy-reservation-hybrid.sqlite');
    const legacy = new Database(legacyPath, { create: true });
    createLegacyTargetlessSchema(legacy);
    createCurrentEventReservationsTable(legacy);
    legacy.exec(`
      INSERT INTO worlds VALUES (
        'legacy-hybrid-world', 'tenant-a', 'event-a', 'team-a',
        'deployment-a', 'seed-a', '2026-07-12T00:00:00.000Z', 'active', 1
      );
      INSERT INTO idempotency VALUES (
        'legacy-hybrid-scope', 'key', 'request-hash', '{"preserved":true}'
      );
      INSERT INTO event_reservations VALUES (
        'legacy-hybrid-world', 'preserved-reservation', 'materialization',
        'preserved-owner', 2
      )
    `);
    legacy.close();

    expectUnversionedHybridRejectedUnchanged(legacyPath);

    const historicalPath = path.join(
      directory,
      'historical-reservation-hybrid.sqlite'
    );
    const seeded = new SimulationStore(historicalPath);
    const world = worldRecord('historical-hybrid-world');
    seeded.insertWorld(world);
    seeded.saveDeployment(
      deploymentRecord(world.worldId, 'historical-hybrid-deployment')
    );
    seeded.saveIdempotent(
      'historical-hybrid-scope',
      'key',
      { request: 'preserved' },
      { response: 'preserved' }
    );
    seeded.close();
    const historical = new Database(historicalPath);
    replayDeploymentTargetAlter(historical, '[]');
    createCurrentEventReservationsTable(historical);
    historical.exec(`
      INSERT INTO event_reservations VALUES (
        'historical-hybrid-world', 'preserved-reservation', 'deletion',
        'preserved-owner', 3
      )
    `);
    historical.close();

    expectUnversionedHybridRejectedUnchanged(historicalPath);
  });

  it('exact schema version 1 に event reservation table だけを追加して version 2 へ移行する', () => {
    const versionOnePath = path.join(directory, 'version-one.sqlite');
    const seeded = new SimulationStore(versionOnePath);
    const world = worldRecord('version-one-world');
    seeded.insertWorld(world);
    seeded.close();
    const versionOne = new Database(versionOnePath);
    versionOne.exec(`
      DROP TABLE event_reservations;
      PRAGMA user_version = 1;
    `);
    versionOne.close();

    const migrated = new SimulationStore(versionOnePath);

    expect(migrated.world(world.worldId)).toEqual(world);
    expect(
      migrated.database
        .query<{ user_version: number }, []>('PRAGMA user_version')
        .get()?.user_version
    ).toBe(2);
    expect(
      migrated.database
        .query<{ sql: string }, []>(
          "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'event_reservations'"
        )
        .get()?.sql
    ).toContain('CHECK (event_count > 0)');
    migrated.close();
  });

  it('schema version 1 に先行 reservation table が混在した状態を変更せず拒否する', () => {
    const pollutedPath = path.join(directory, 'polluted-version-one.sqlite');
    const seeded = new SimulationStore(pollutedPath);
    seeded.close();
    const polluted = new Database(pollutedPath);
    polluted.exec('PRAGMA user_version = 1');
    const schemaBefore = schemaSnapshot(polluted);
    polluted.close();

    const error = captureCoreError(() => new SimulationStore(pollutedPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('event_reservations');
    const rejected = new Database(pollutedPath, { readonly: true });
    expect(schemaSnapshot(rejected)).toEqual(schemaBefore);
    expect(
      rejected.query<{ user_version: number }, []>('PRAGMA user_version').get()
        ?.user_version
    ).toBe(1);
    rejected.close();
  });

  it('未来の user_version は table を追加せず起動時に拒否する', () => {
    const futurePath = path.join(directory, 'future.sqlite');
    const future = new Database(futurePath, { create: true });
    future.exec('PRAGMA user_version = 3');
    future.close();

    const error = captureCoreError(() => new SimulationStore(futurePath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('schema version');
    const rejected = new Database(futurePath, { readonly: true });
    expect(
      rejected.query<{ user_version: number }, []>('PRAGMA user_version').get()
        ?.user_version
    ).toBe(3);
    expect(
      rejected.query<{ journal_mode: string }, []>('PRAGMA journal_mode').get()
        ?.journal_mode
    ).toBe('delete');
    expect(
      rejected
        .query<{ count: number }, []>(
          "SELECT COUNT(*) AS count FROM sqlite_master WHERE type = 'table'"
        )
        .get()?.count
    ).toBe(0);
    rejected.close();
  });

  it('既知の legacy shape ではない部分 schema を変更せず拒否する', () => {
    const partialPath = path.join(directory, 'partial.sqlite');
    const partial = new Database(partialPath, { create: true });
    partial.exec(`
      CREATE TABLE resources (
        world_id TEXT NOT NULL,
        deployment_id TEXT NOT NULL,
        target_id TEXT NOT NULL,
        provider TEXT NOT NULL,
        resource_type TEXT NOT NULL,
        resource_id TEXT NOT NULL,
        status TEXT NOT NULL,
        PRIMARY KEY (world_id, deployment_id, target_id, provider, resource_id)
      )
    `);
    const schemaBefore = partial
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    partial.close();

    const error = captureCoreError(() => new SimulationStore(partialPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('resources schema');
    const rejected = new Database(partialPath, { readonly: true });
    const schemaAfter = rejected
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    expect(schemaAfter).toEqual(schemaBefore);
    expect(
      rejected.query<{ user_version: number }, []>('PRAGMA user_version').get()
        ?.user_version
    ).toBe(0);
    rejected.close();
  });

  it('current table の一部だけを持つ unversioned schema は不足 table を補完せず拒否する', () => {
    const partialPath = path.join(directory, 'partial-current.sqlite');
    const partial = new Database(partialPath, { create: true });
    partial.exec(`
      CREATE TABLE worlds (
        world_id TEXT PRIMARY KEY,
        tenant_id TEXT NOT NULL,
        event_id TEXT NOT NULL,
        team_id TEXT NOT NULL,
        deployment_id TEXT NOT NULL,
        seed TEXT NOT NULL,
        virtual_time TEXT NOT NULL,
        status TEXT NOT NULL,
        next_sequence INTEGER NOT NULL
      )
    `);
    partial.close();

    const error = captureCoreError(() => new SimulationStore(partialPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('partial');
    const rejected = new Database(partialPath, { readonly: true });
    expect(
      rejected
        .query<{ count: number }, []>(
          `SELECT COUNT(*) AS count FROM sqlite_master
           WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
        )
        .get()?.count
    ).toBe(1);
    rejected.close();
  });

  it('legacy table が一部だけ存在する unversioned schema も補完せず拒否する', () => {
    const partialPath = path.join(directory, 'partial-legacy.sqlite');
    const partial = new Database(partialPath, { create: true });
    partial.exec(`
      CREATE TABLE resources (
        world_id TEXT NOT NULL,
        deployment_id TEXT NOT NULL,
        provider TEXT NOT NULL,
        resource_type TEXT NOT NULL,
        resource_id TEXT NOT NULL,
        properties TEXT NOT NULL,
        status TEXT NOT NULL,
        PRIMARY KEY (world_id, provider, resource_id),
        FOREIGN KEY (world_id) REFERENCES worlds(world_id)
      )
    `);
    partial.close();

    const error = captureCoreError(() => new SimulationStore(partialPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('partial');
  });

  it('column が揃っていても resource primary key が違う schema を拒否する', () => {
    const invalidPath = path.join(directory, 'invalid-primary-key.sqlite');
    const invalid = new Database(invalidPath, { create: true });
    invalid.exec(`
      CREATE TABLE resources (
        world_id TEXT NOT NULL,
        deployment_id TEXT NOT NULL,
        target_id TEXT NOT NULL,
        provider TEXT NOT NULL,
        resource_type TEXT NOT NULL,
        resource_id TEXT NOT NULL,
        properties TEXT NOT NULL,
        status TEXT NOT NULL,
        PRIMARY KEY (world_id, provider, resource_id)
      )
    `);
    invalid.close();

    const error = captureCoreError(() => new SimulationStore(invalidPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('resources schema');
  });

  it('column type・NOT NULL・foreign key が current 定義と違う schema を拒否する', () => {
    const invalidPath = path.join(directory, 'invalid-column-shape.sqlite');
    const seeded = new SimulationStore(invalidPath);
    seeded.close();
    const invalid = new Database(invalidPath);
    invalid.exec(`
      PRAGMA foreign_keys = OFF;
      ALTER TABLE resources RENAME TO resources_original;
      CREATE TABLE resources (
        world_id TEXT NOT NULL,
        deployment_id TEXT NOT NULL,
        target_id TEXT NOT NULL,
        provider TEXT NOT NULL,
        resource_type TEXT NOT NULL,
        resource_id TEXT NOT NULL,
        properties BLOB,
        status TEXT NOT NULL,
        PRIMARY KEY (world_id, deployment_id, target_id, provider, resource_id)
      );
      DROP TABLE resources_original;
    `);
    invalid.close();

    const error = captureCoreError(() => new SimulationStore(invalidPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('resources schema');
  });

  it('同じ column と primary key でも CHECK 制約を加えた schema を変更せず拒否する', () => {
    const invalidPath = path.join(directory, 'unexpected-check.sqlite');
    expectIdempotencySchemaRejectedUnchanged(invalidPath, {
      keyDefinition: 'TEXT NOT NULL CHECK (length(key) < 5)',
    });
  });

  it('event reservation の必須 CHECK 制約が欠けた schema を変更せず拒否する', () => {
    const invalidPath = path.join(
      directory,
      'missing-reservation-check.sqlite'
    );
    const seeded = new SimulationStore(invalidPath);
    seeded.close();
    const invalid = new Database(invalidPath);
    invalid.exec(`
      ALTER TABLE event_reservations RENAME TO event_reservations_original;
      CREATE TABLE event_reservations (
        world_id TEXT NOT NULL,
        reservation_id TEXT NOT NULL,
        operation_kind TEXT NOT NULL,
        owner_id TEXT NOT NULL,
        event_count INTEGER NOT NULL,
        PRIMARY KEY (world_id, reservation_id),
        FOREIGN KEY (world_id) REFERENCES worlds(world_id)
      );
      DROP TABLE event_reservations_original;
    `);
    const schemaBefore = schemaSnapshot(invalid);
    invalid.close();

    const error = captureCoreError(() => new SimulationStore(invalidPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('event_reservations schema');
    const rejected = new Database(invalidPath, { readonly: true });
    expect(schemaSnapshot(rejected)).toEqual(schemaBefore);
    expect(
      rejected.query<{ user_version: number }, []>('PRAGMA user_version').get()
        ?.user_version
    ).toBe(2);
    rejected.close();
  });

  it('同じ column と primary key でも UNIQUE 制約を加えた schema を変更せず拒否する', () => {
    const invalidPath = path.join(directory, 'unexpected-unique.sqlite');
    expectIdempotencySchemaRejectedUnchanged(invalidPath, {
      tableConstraint: 'UNIQUE (request_hash)',
    });
  });

  it('COLLATE・STRICT・WITHOUT ROWID を加えた未知 DDL も変更せず拒否する', () => {
    const variants: readonly (IdempotencySchemaVariant & {
      readonly name: string;
    })[] = [
      { name: 'collate', keyDefinition: 'TEXT COLLATE NOCASE NOT NULL' },
      { name: 'strict', tableOptions: 'STRICT' },
      { name: 'without-rowid', tableOptions: 'WITHOUT ROWID' },
    ];

    for (const variant of variants) {
      const invalidPath = path.join(directory, `${variant.name}.sqlite`);
      expectIdempotencySchemaRejectedUnchanged(invalidPath, variant);
    }
  });

  it('current schema に追加された trigger や未知 schema object を拒否する', () => {
    const invalidPath = path.join(directory, 'unexpected-trigger.sqlite');
    const seeded = new SimulationStore(invalidPath);
    seeded.close();
    const invalid = new Database(invalidPath);
    invalid.exec(`
      CREATE TRIGGER unexpected_world_trigger
      AFTER INSERT ON worlds
      BEGIN
        SELECT 1;
      END
    `);
    invalid.close();

    const error = captureCoreError(() => new SimulationStore(invalidPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('schema object');
  });

  it('current schema に保存された foreign key 違反を起動時に拒否する', () => {
    const invalidPath = path.join(directory, 'orphan-resource.sqlite');
    const seeded = new SimulationStore(invalidPath);
    seeded.close();
    const invalid = new Database(invalidPath);
    invalid.exec('PRAGMA foreign_keys = OFF');
    invalid
      .query('INSERT INTO resources VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
      .run(
        'missing-world',
        'deployment-a',
        'default',
        'alpha',
        'Object',
        'orphan',
        '{}',
        'ready'
      );
    invalid.close();

    const error = captureCoreError(() => new SimulationStore(invalidPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('foreign key');
  });

  it('event の予約 rollback release を idempotent に処理する', () => {
    const world = worldRecord();
    store.insertWorld(world);

    store.reserveEvents(world.worldId, 'materialize-one', 2);
    store.reserveEvents(world.worldId, 'materialize-one', 2);
    expect(
      store.hasOtherEventReservation(world.worldId, 'materialize-one')
    ).toBe(false);
    store.reserveEvents(world.worldId, 'materialize-two', 1);
    expect(
      store.hasOtherEventReservation(world.worldId, 'materialize-one')
    ).toBe(true);
    store.releaseEvents(world.worldId, 'materialize-two');
    expect(store.reservedEventCount(world.worldId)).toBe(2);
    expect(
      captureCoreError(() =>
        store.reserveEvents(world.worldId, 'materialize-one', 3)
      ).code
    ).toBe('Conflict');
    expect(
      captureCoreError(() =>
        store.reserveEvents(world.worldId, 'invalid-reservation', 0)
      ).code
    ).toBe('ValidationFailed');
    expect(() =>
      store.transaction(() => {
        store.reserveEvents(world.worldId, 'rolled-back-reservation', 4);
        throw new Error('reservation rollback');
      })
    ).toThrow('reservation rollback');
    expect(store.reservedEventCount(world.worldId)).toBe(2);
    expect(() =>
      store.transaction(() => {
        store.releaseEvents(world.worldId, 'materialize-one');
        throw new Error('release rollback');
      })
    ).toThrow('release rollback');
    expect(store.reservedEventCount(world.worldId)).toBe(2);
    store.releaseEvents(world.worldId, 'materialize-one');
    expect(store.reservedEventCount(world.worldId)).toBe(0);
  });

  it('未知の owner identity を持つ event reservation を fail closed にする', () => {
    const world = worldRecord();
    store.insertWorld(world);

    store.database
      .query(
        `INSERT INTO event_reservations(
           world_id, reservation_id, operation_kind, owner_id, event_count
         ) VALUES (?, ?, ?, ?, ?)`
      )
      .run(
        world.worldId,
        'unknown-reservation',
        'materialization',
        'unknown-owner',
        5
      );
    expect(store.reservedEventCount(world.worldId)).toBe(5);
    const reopened = new SimulationStore(
      path.join(directory, 'simulation.sqlite')
    );
    reopened.releaseEvents(world.worldId, 'unknown-reservation');
    expect(reopened.reservedEventCount(world.worldId)).toBe(5);
    reopened.close();
  });

  it('同一 process の active owner を保持して close 時に reservation を解放する', () => {
    const world = worldRecord();
    store.insertWorld(world);
    const owner = new SimulationStore(
      path.join(directory, 'simulation.sqlite')
    );
    const observer = new SimulationStore(
      path.join(directory, 'simulation.sqlite')
    );

    owner.reserveEvents(world.worldId, 'same-process-owner', 2);
    observer.recoverDeadEventReservations();
    expect(observer.reservedEventCount(world.worldId)).toBe(2);
    owner.close();
    expect(observer.reservedEventCount(world.worldId)).toBe(0);
    observer.close();
  });

  it('close 時も delete intent を保持して dead owner だけ takeover を許可する', () => {
    const world = worldRecord('persistent-delete-intent-world');
    store.insertWorld(world);
    store.reserveEvents(
      world.worldId,
      'persistent-delete-intent',
      1,
      'deletion'
    );
    const survivor = new SimulationStore(
      path.join(directory, 'simulation.sqlite')
    );

    try {
      expect(() =>
        survivor.reserveEvents(
          world.worldId,
          'persistent-delete-intent',
          1,
          'deletion'
        )
      ).toThrow(CoreError);
      expect(survivor.pendingDeletionWorldIds()).toEqual([world.worldId]);

      store.close();
      expect(survivor.pendingDeletionWorldIds()).toEqual([world.worldId]);
      survivor.reserveEvents(
        world.worldId,
        'persistent-delete-intent',
        1,
        'deletion'
      );
      survivor.releaseEvents(world.worldId, 'persistent-delete-intent');
      expect(survivor.pendingDeletionWorldIds()).toEqual([]);
    } finally {
      survivor.close();
    }
  });

  it('再利用 PID に active store owner がない reservation を回収する', () => {
    const world = worldRecord();
    store.insertWorld(world);
    const reusedOwner = JSON.stringify({
      version: 1,
      pid: process.pid,
      startIdentity: 'a prior process with the same PID',
      nonce: 'reused-pid-owner',
    });
    store.database
      .query(
        `INSERT INTO event_reservations(
           world_id, reservation_id, operation_kind, owner_id, event_count
         ) VALUES (?, ?, ?, ?, ?)`
      )
      .run(
        world.worldId,
        'reused-pid-reservation',
        'materialization',
        reusedOwner,
        2
      );

    expect(store.reservedEventCount(world.worldId)).toBe(2);
    store.recoverDeadEventReservations();
    expect(store.reservedEventCount(world.worldId)).toBe(0);
  });

  it('live foreign reservation を保持して owner crash 後に open survivor が回収する', async () => {
    const world = worldRecord();
    store.insertWorld(world);
    const databasePath = path.join(directory, 'simulation.sqlite');
    const storeModulePath = path.join(import.meta.dir, 'store.ts');
    const child = Bun.spawn({
      cmd: [
        process.execPath,
        '--eval',
        `
          import { SimulationStore } from ${JSON.stringify(storeModulePath)};
          const childStore = new SimulationStore(${JSON.stringify(databasePath)});
          childStore.reserveEvents(${JSON.stringify(world.worldId)}, 'foreign-reservation', 3);
          console.log('READY');
          await new Promise(() => {});
        `,
      ],
      cwd: import.meta.dir,
      stdout: 'pipe',
      stderr: 'pipe',
    });

    try {
      const reader = child.stdout.getReader();
      const ready = await (async () => {
        let timeout: ReturnType<typeof setTimeout> | undefined;
        try {
          return await Promise.race([
            reader.read(),
            new Promise<never>((_, reject) => {
              timeout = setTimeout(
                () => reject(new Error('child READY timeout')),
                5_000
              );
            }),
          ]);
        } catch (error) {
          if (child.exitCode === null) child.kill('SIGKILL');
          await child.exited;
          const stderr = await new Response(child.stderr).text();
          throw new Error(
            `reservation child did not become ready: ${String(error)}; stderr: ${stderr}`
          );
        } finally {
          if (timeout) clearTimeout(timeout);
        }
      })();
      expect(new TextDecoder().decode(ready.value)).toContain('READY');

      const survivor = new SimulationStore(databasePath);
      survivor.releaseEvents(world.worldId, 'foreign-reservation');
      expect(survivor.reservedEventCount(world.worldId)).toBe(3);
      expect(
        captureCoreError(() =>
          survivor.reserveEvents(world.worldId, 'foreign-reservation', 3)
        ).code
      ).toBe('Conflict');

      child.kill('SIGKILL');
      await child.exited;

      survivor.reserveEvents(world.worldId, 'foreign-reservation', 3);
      expect(survivor.reservedEventCount(world.worldId)).toBe(3);
      survivor.releaseEvents(world.worldId, 'foreign-reservation');
      expect(survivor.reservedEventCount(world.worldId)).toBe(0);
      survivor.close();
    } finally {
      if (child.exitCode === null) child.kill('SIGKILL');
      await child.exited;
    }
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

  it('request 本文なしでも idempotency response を recovery pointer として読める', () => {
    const response = { worldId: 'world-one', deploymentId: 'deployment-one' };

    expect(store.idempotentResponse('scope', 'key')).toBeUndefined();
    store.saveIdempotent('scope', 'key', { seed: 'secret-seed' }, response);

    expect(store.idempotentResponse<typeof response>('scope', 'key')).toEqual(
      response
    );
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

  it('既存 deployment table に target identity column を fail-closed migration する', () => {
    const legacyPath = path.join(directory, 'legacy.sqlite');
    const legacy = new Database(legacyPath, { create: true });
    createLegacyTargetlessSchema(legacy);
    legacy.close();

    const migrated = new SimulationStore(legacyPath);
    const columns = migrated.database
      .query<{ name: string }, []>('PRAGMA table_info(deployments)')
      .all();
    expect(columns.map((column) => column.name)).toContain('targets');
    expect(
      migrated.database
        .query<{ user_version: number }, []>('PRAGMA user_version')
        .get()?.user_version
    ).toBe(2);
    migrated.close();
  });

  it("旧migrationの実 ALTER で DEFAULT '[]' が付いた populated deployment を正規化して保持する", () => {
    const migratedPath = path.join(directory, 'previously-migrated.sqlite');
    const seeded = new SimulationStore(migratedPath);
    const world = worldRecord('previously-migrated-world');
    const deployment = deploymentRecord(world.worldId, 'preserved-deployment');
    seeded.insertWorld(world);
    seeded.saveDeployment(deployment);
    seeded.close();
    const previous = new Database(migratedPath);
    replayDeploymentTargetAlter(previous, '[]');
    const historicalColumns = previous
      .query<{ name: string; dflt_value: string | null }, []>(
        'PRAGMA table_info(deployments)'
      )
      .all();
    expect(historicalColumns.map((column) => column.name)).toEqual([
      'world_id',
      'deployment_id',
      'problem_id',
      'status',
      'outputs',
      'diagnostics',
      'targets',
    ]);
    expect(
      historicalColumns.find((column) => column.name === 'targets')?.dflt_value
    ).toBe("'[]'");
    previous.close();

    const reopened = new SimulationStore(migratedPath);

    expect(reopened.deployment(world.worldId, deployment.deploymentId)).toEqual(
      { ...deployment, targets: [] }
    );
    const normalizedColumns = reopened.database
      .query<{ name: string; dflt_value: string | null }, []>(
        'PRAGMA table_info(deployments)'
      )
      .all();
    expect(normalizedColumns.map((column) => column.name)).toEqual([
      'world_id',
      'deployment_id',
      'problem_id',
      'status',
      'targets',
      'outputs',
      'diagnostics',
    ]);
    expect(
      normalizedColumns.find((column) => column.name === 'targets')?.dflt_value
    ).toBeNull();
    expect(
      reopened.database
        .query<{ user_version: number }, []>('PRAGMA user_version')
        .get()?.user_version
    ).toBe(2);
    reopened.close();
  });

  it('末尾 targets の未知の DEFAULT は schema と populated row を変更せず拒否する', () => {
    const invalidPath = path.join(directory, 'unknown-target-default.sqlite');
    const seeded = new SimulationStore(invalidPath);
    const world = worldRecord('unknown-default-world');
    const deployment = deploymentRecord(world.worldId, 'preserved-deployment');
    seeded.insertWorld(world);
    seeded.saveDeployment(deployment);
    seeded.close();
    const invalid = new Database(invalidPath);
    replayDeploymentTargetAlter(invalid, '{}');
    const schemaBefore = invalid
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    invalid.close();

    const error = captureCoreError(() => new SimulationStore(invalidPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('deployments schema');
    const rejected = new Database(invalidPath, { readonly: true });
    expect(
      rejected
        .query<{ name: string; sql: string }, []>(
          `SELECT name, sql FROM sqlite_master
           WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
           ORDER BY name`
        )
        .all()
    ).toEqual(schemaBefore);
    expect(
      rejected
        .query<{ count: number }, []>(
          'SELECT COUNT(*) AS count FROM deployments'
        )
        .get()?.count
    ).toBe(1);
    expect(
      rejected.query<{ user_version: number }, []>('PRAGMA user_version').get()
        ?.user_version
    ).toBe(0);
    rejected.close();
  });

  it('target identity のない既存 deployment row があれば schema 変更前に migration を拒否する', () => {
    const legacyPath = path.join(directory, 'legacy-deployments.sqlite');
    const legacy = new Database(legacyPath, { create: true });
    createLegacyTargetlessSchema(legacy);
    legacy.exec(`
      INSERT INTO deployments VALUES (
        'world-one', 'deployment-a', 'problem-a', 'ready', '{}', '[]'
      )
    `);
    const schemaBefore = legacy
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    legacy.close();

    const error = captureCoreError(() => new SimulationStore(legacyPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('target identity');
    const rejected = new Database(legacyPath, { readonly: true });
    const schemaAfter = rejected
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    rejected.close();
    expect(schemaAfter).toEqual(schemaBefore);
  });

  it('保存済み deployment target identity の不正 JSON を拒否する', () => {
    const world = worldRecord();
    store.insertWorld(world);
    const deployment = deploymentRecord(world.worldId, 'invalid-targets');
    store.saveDeployment(deployment);
    store.database
      .query(
        'UPDATE deployments SET targets = ? WHERE world_id = ? AND deployment_id = ?'
      )
      .run('[{}]', world.worldId, deployment.deploymentId);

    const error = captureCoreError(() =>
      store.deployment(world.worldId, deployment.deploymentId)
    );
    expect(error.code).toBe('ValidationFailed');
  });

  it('target identity のない既存 resource row があれば migration を拒否する', () => {
    const legacyPath = path.join(directory, 'legacy-resources.sqlite');
    const legacy = new Database(legacyPath, { create: true });
    createLegacyTargetlessSchema(legacy);
    legacy.exec(`
      INSERT INTO resources VALUES (
        'world-one', 'deployment-a', 'alpha', 'Object', 'shared', '{}', 'ready'
      )
    `);
    const schemaBefore = legacy
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    legacy.close();

    const error = captureCoreError(() => new SimulationStore(legacyPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('target identity');
    const rejected = new Database(legacyPath, { readonly: true });
    const schemaAfter = rejected
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    rejected.close();
    expect(schemaAfter).toEqual(schemaBefore);
  });

  it('空の完全な legacy schema を target identity 付き schema へ移行する', () => {
    const legacyPath = path.join(directory, 'empty-legacy-resources.sqlite');
    const legacy = new Database(legacyPath, { create: true });
    createLegacyTargetlessSchema(legacy);
    legacy.close();

    const migrated = new SimulationStore(legacyPath);

    const columns = migrated.database
      .query<{ name: string }, []>('PRAGMA table_info(resources)')
      .all();
    expect(columns.map((column) => column.name)).toEqual([
      'world_id',
      'deployment_id',
      'target_id',
      'provider',
      'resource_type',
      'resource_id',
      'properties',
      'status',
    ]);
    expect(
      migrated.database
        .query<{ user_version: number }, []>('PRAGMA user_version')
        .get()?.user_version
    ).toBe(2);
    migrated.close();
  });

  it('legacy DDL 後の整合性検証が失敗したら schema と version を rollback する', () => {
    const legacyPath = path.join(directory, 'rollback-legacy.sqlite');
    const legacy = new Database(legacyPath, { create: true });
    createLegacyTargetlessSchema(legacy);
    legacy.exec(`
      PRAGMA foreign_keys = OFF;
      INSERT INTO events VALUES (
        'missing-world', 1, 'OrphanEvent', '2026-07-12T00:00:00.000Z',
        'command', '{}', 'hash'
      )
    `);
    const schemaBefore = legacy
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    legacy.close();

    const error = captureCoreError(() => new SimulationStore(legacyPath));

    expect(error.code).toBe('ValidationFailed');
    expect(error.message).toContain('foreign key');
    const rejected = new Database(legacyPath, { readonly: true });
    const schemaAfter = rejected
      .query<{ name: string; sql: string }, []>(
        `SELECT name, sql FROM sqlite_master
         WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
         ORDER BY name`
      )
      .all();
    expect(schemaAfter).toEqual(schemaBefore);
    expect(
      rejected.query<{ user_version: number }, []>('PRAGMA user_version').get()
        ?.user_version
    ).toBe(0);
    rejected.close();
  });

  it('resource を target 単位で upsert、取得し、論理削除する', () => {
    const world = worldRecord();
    store.insertWorld(world);
    const alpha = resourceRecord(world.worldId, 'z-resource', 'alpha');
    const beta = resourceRecord(world.worldId, 'a-resource', 'beta');
    const sibling = resourceRecord(
      world.worldId,
      alpha.resourceId,
      alpha.provider,
      'secondary'
    );
    store.saveResource(beta);
    store.saveResource(alpha);
    store.saveResource(sibling);
    expect(
      captureCoreError(() => store.saveResource({ ...alpha, targetId: ' ' }))
        .code
    ).toBe('ValidationFailed');
    store.saveResource({
      ...alpha,
      resourceType: 'UpdatedObject',
      properties: { enabled: false },
    });

    expect(store.resources(world.worldId)).toEqual([
      {
        ...alpha,
        resourceType: 'UpdatedObject',
        properties: { enabled: false },
      },
      sibling,
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

    store.deleteResource(
      world.worldId,
      alpha.deploymentId,
      alpha.targetId,
      alpha.provider,
      alpha.resourceId
    );
    expect(store.resources(world.worldId)[0]?.status).toBe('deleted');
    expect(store.resources(world.worldId)[1]).toEqual(sibling);
  });

  it('resource の保存 JSON が object でなければ ValidationFailed にする', () => {
    const world = worldRecord();
    store.insertWorld(world);
    store.database
      .query('INSERT INTO resources VALUES (?, ?, ?, ?, ?, ?, ?, ?)')
      .run(
        world.worldId,
        'deployment-a',
        'default',
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
