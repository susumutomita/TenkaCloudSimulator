import { Database } from 'bun:sqlite';
import { canonicalJson, contentHash } from './canonical';
import type {
  DeploymentRecord,
  DeploymentTargetIdentity,
  EventRecord,
  ResourceRecord,
  WorldRecord,
} from './domain';
import { CoreError } from './errors';

interface WorldRow {
  world_id: string;
  tenant_id: string;
  event_id: string;
  team_id: string;
  deployment_id: string;
  seed: string;
  virtual_time: string;
  status: string;
  next_sequence: number;
}

interface EventRow {
  world_id: string;
  sequence: number;
  type: string;
  virtual_time: string;
  command_id: string;
  payload: string;
  payload_hash: string;
}

interface DeploymentRow {
  world_id: string;
  deployment_id: string;
  problem_id: string;
  status: string;
  targets: string;
  outputs: string;
  diagnostics: string;
}

interface TableColumnRow {
  name: string;
}

interface TableCountRow {
  count: number;
}

interface TableExistsRow {
  found: number;
}

interface ResourceRow {
  world_id: string;
  deployment_id: string;
  target_id: string;
  provider: string;
  resource_type: string;
  resource_id: string;
  properties: string;
  status: string;
}

interface IdempotencyRow {
  request_hash: string;
  response: string;
}

const RESOURCE_TABLE_SQL = `
  CREATE TABLE IF NOT EXISTS resources (
    world_id TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    properties TEXT NOT NULL,
    status TEXT NOT NULL,
    PRIMARY KEY (world_id, deployment_id, target_id, provider, resource_id),
    FOREIGN KEY (world_id) REFERENCES worlds(world_id)
  );
`;

function assertResourceMigrationCompatibility(database: Database): void {
  const table = database
    .query<TableExistsRow, []>(
      `SELECT 1 AS found FROM sqlite_master
       WHERE type = 'table' AND name = 'resources'`
    )
    .get();
  if (!table) return;
  const columns = database
    .query<TableColumnRow, []>('PRAGMA table_info(resources)')
    .all();
  if (columns.some((column) => column.name === 'target_id')) return;
  const row = database
    .query<TableCountRow, []>('SELECT COUNT(*) AS count FROM resources')
    .get();
  if ((row?.count ?? 0) > 0) {
    throw new CoreError(
      'ValidationFailed',
      'stored resources have no target identity'
    );
  }
}

function assertDeploymentMigrationCompatibility(database: Database): void {
  const table = database
    .query<TableExistsRow, []>(
      `SELECT 1 AS found FROM sqlite_master
       WHERE type = 'table' AND name = 'deployments'`
    )
    .get();
  if (!table) return;
  const columns = database
    .query<TableColumnRow, []>('PRAGMA table_info(deployments)')
    .all();
  if (columns.some((column) => column.name === 'targets')) return;
  const row = database
    .query<TableCountRow, []>('SELECT COUNT(*) AS count FROM deployments')
    .get();
  if ((row?.count ?? 0) > 0) {
    throw new CoreError(
      'ValidationFailed',
      'stored deployments have no target identity'
    );
  }
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function parseObject(value: string): Readonly<Record<string, unknown>> {
  const parsed: unknown = JSON.parse(value);
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new CoreError(
      'ValidationFailed',
      'stored JSON value is not an object'
    );
  }
  return parsed as Readonly<Record<string, unknown>>;
}

function parseStringMapMap(
  value: string
): Readonly<Record<string, Readonly<Record<string, string>>>> {
  return JSON.parse(value) as Readonly<
    Record<string, Readonly<Record<string, string>>>
  >;
}

function parseDeploymentTargets(
  value: string
): readonly DeploymentTargetIdentity[] {
  const parsed: unknown = JSON.parse(value);
  if (
    !Array.isArray(parsed) ||
    parsed.some(
      (target) =>
        !isRecord(target) ||
        typeof target['id'] !== 'string' ||
        typeof target['provider'] !== 'string' ||
        typeof target['engine'] !== 'string'
    )
  ) {
    throw new CoreError(
      'ValidationFailed',
      'stored deployment targets are invalid'
    );
  }
  return parsed;
}

function worldFromRow(row: WorldRow): WorldRecord {
  return {
    worldId: row.world_id,
    tenantId: row.tenant_id,
    eventId: row.event_id,
    teamId: row.team_id,
    deploymentId: row.deployment_id,
    seed: row.seed,
    virtualTime: row.virtual_time,
    status: row.status === 'deleted' ? 'deleted' : 'active',
  };
}

export class SimulationStore {
  readonly database: Database;

  constructor(path: string) {
    this.database = new Database(path, { create: true, strict: true });
    try {
      assertDeploymentMigrationCompatibility(this.database);
      assertResourceMigrationCompatibility(this.database);
    } catch (error) {
      this.database.close();
      throw error;
    }
    this.database.exec('PRAGMA foreign_keys = ON');
    this.database.exec('PRAGMA journal_mode = WAL');
    this.database.exec(`
      CREATE TABLE IF NOT EXISTS worlds (
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
      CREATE TABLE IF NOT EXISTS events (
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
      CREATE TABLE IF NOT EXISTS deployments (
        world_id TEXT NOT NULL,
        deployment_id TEXT NOT NULL,
        problem_id TEXT NOT NULL,
        status TEXT NOT NULL,
        targets TEXT NOT NULL,
        outputs TEXT NOT NULL,
        diagnostics TEXT NOT NULL,
        PRIMARY KEY (world_id, deployment_id),
        FOREIGN KEY (world_id) REFERENCES worlds(world_id)
      );
      CREATE TABLE IF NOT EXISTS idempotency (
        scope TEXT NOT NULL,
        key TEXT NOT NULL,
        request_hash TEXT NOT NULL,
        response TEXT NOT NULL,
        PRIMARY KEY (scope, key)
      );
    `);
    this.database.exec(RESOURCE_TABLE_SQL);
    const deploymentColumns = this.database
      .query<TableColumnRow, []>('PRAGMA table_info(deployments)')
      .all();
    if (!deploymentColumns.some((column) => column.name === 'targets')) {
      this.database.exec(
        "ALTER TABLE deployments ADD COLUMN targets TEXT NOT NULL DEFAULT '[]'"
      );
    }
    const resourceColumns = this.database
      .query<TableColumnRow, []>('PRAGMA table_info(resources)')
      .all();
    if (!resourceColumns.some((column) => column.name === 'target_id')) {
      this.database.exec('DROP TABLE resources');
      this.database.exec(RESOURCE_TABLE_SQL);
    }
  }

  close(): void {
    this.database.close();
  }

  transaction<T>(operation: () => T): T {
    return this.database.transaction(operation)();
  }

  idempotent<T>(scope: string, key: string, request: unknown): T | undefined {
    const row = this.database
      .query<IdempotencyRow, [string, string]>(
        'SELECT request_hash, response FROM idempotency WHERE scope = ? AND key = ?'
      )
      .get(scope, key);
    if (!row) return undefined;
    if (row.request_hash !== contentHash(request)) {
      throw new CoreError(
        'IdempotencyConflict',
        'idempotency key was reused with a different request'
      );
    }
    return JSON.parse(row.response) as T;
  }

  saveIdempotent(
    scope: string,
    key: string,
    request: unknown,
    response: unknown
  ): void {
    this.database
      .query(
        'INSERT INTO idempotency(scope, key, request_hash, response) VALUES (?, ?, ?, ?)'
      )
      .run(scope, key, contentHash(request), canonicalJson(response));
  }

  insertWorld(world: WorldRecord): void {
    this.database
      .query('INSERT INTO worlds VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)')
      .run(
        world.worldId,
        world.tenantId,
        world.eventId,
        world.teamId,
        world.deploymentId,
        world.seed,
        world.virtualTime,
        world.status,
        1
      );
  }

  world(worldId: string): WorldRecord | undefined {
    const row = this.database
      .query<WorldRow, [string]>('SELECT * FROM worlds WHERE world_id = ?')
      .get(worldId);
    return row ? worldFromRow(row) : undefined;
  }

  setWorldState(
    worldId: string,
    virtualTime: string,
    status: WorldRecord['status']
  ): void {
    this.database
      .query(
        'UPDATE worlds SET virtual_time = ?, status = ? WHERE world_id = ?'
      )
      .run(virtualTime, status, worldId);
  }

  appendEvent(
    worldId: string,
    type: string,
    commandId: string,
    payload: Readonly<Record<string, unknown>>
  ): EventRecord {
    const row = this.database
      .query<WorldRow, [string]>('SELECT * FROM worlds WHERE world_id = ?')
      .get(worldId);
    if (!row) throw new CoreError('NotFound', 'world does not exist');
    const sequence = row.next_sequence;
    const payloadHash = contentHash(payload);
    this.database
      .query('INSERT INTO events VALUES (?, ?, ?, ?, ?, ?, ?)')
      .run(
        worldId,
        sequence,
        type,
        row.virtual_time,
        commandId,
        canonicalJson(payload),
        payloadHash
      );
    this.database
      .query(
        'UPDATE worlds SET next_sequence = next_sequence + 1 WHERE world_id = ?'
      )
      .run(worldId);
    return {
      worldId,
      sequence,
      type,
      virtualTime: row.virtual_time,
      commandId,
      payload,
      payloadHash,
    };
  }

  events(worldId: string): readonly EventRecord[] {
    return this.database
      .query<EventRow, [string]>(
        'SELECT * FROM events WHERE world_id = ? ORDER BY sequence'
      )
      .all(worldId)
      .map((row) => ({
        worldId: row.world_id,
        sequence: row.sequence,
        type: row.type,
        virtualTime: row.virtual_time,
        commandId: row.command_id,
        payload: parseObject(row.payload),
        payloadHash: row.payload_hash,
      }));
  }

  saveDeployment(deployment: DeploymentRecord): void {
    this.database
      .query(
        `INSERT INTO deployments (
           world_id, deployment_id, problem_id, status, targets, outputs, diagnostics
         ) VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(world_id, deployment_id) DO UPDATE SET
           status = excluded.status,
           targets = excluded.targets,
           outputs = excluded.outputs,
           diagnostics = excluded.diagnostics`
      )
      .run(
        deployment.worldId,
        deployment.deploymentId,
        deployment.problemId,
        deployment.status,
        canonicalJson(deployment.targets),
        canonicalJson(deployment.outputs),
        canonicalJson(deployment.diagnostics)
      );
  }

  deployment(
    worldId: string,
    deploymentId: string
  ): DeploymentRecord | undefined {
    const row = this.database
      .query<DeploymentRow, [string, string]>(
        'SELECT * FROM deployments WHERE world_id = ? AND deployment_id = ?'
      )
      .get(worldId, deploymentId);
    if (!row) return undefined;
    return {
      worldId: row.world_id,
      deploymentId: row.deployment_id,
      problemId: row.problem_id,
      status:
        row.status === 'deploying'
          ? 'deploying'
          : row.status === 'failed'
            ? 'failed'
            : row.status === 'rejected'
              ? 'rejected'
              : row.status === 'deleted'
                ? 'deleted'
                : 'ready',
      targets: parseDeploymentTargets(row.targets),
      outputs: parseStringMapMap(row.outputs),
      diagnostics: JSON.parse(row.diagnostics),
    };
  }

  deployments(worldId: string): readonly DeploymentRecord[] {
    return this.database
      .query<DeploymentRow, [string]>(
        'SELECT * FROM deployments WHERE world_id = ? ORDER BY deployment_id'
      )
      .all(worldId)
      .map((row) => {
        const deployment = this.deployment(row.world_id, row.deployment_id);
        if (!deployment)
          throw new CoreError('NotFound', 'deployment disappeared');
        return deployment;
      });
  }

  saveResource(resource: ResourceRecord): void {
    if (typeof resource.targetId !== 'string' || !resource.targetId.trim()) {
      throw new CoreError(
        'ValidationFailed',
        'resource target identity must not be empty'
      );
    }
    this.database
      .query(
        `INSERT INTO resources (
           world_id, deployment_id, target_id, provider, resource_type,
           resource_id, properties, status
         ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(world_id, deployment_id, target_id, provider, resource_id)
         DO UPDATE SET
           resource_type = excluded.resource_type,
           properties = excluded.properties,
           status = excluded.status`
      )
      .run(
        resource.worldId,
        resource.deploymentId,
        resource.targetId,
        resource.provider,
        resource.resourceType,
        resource.resourceId,
        canonicalJson(resource.properties),
        resource.status
      );
  }

  deleteResource(
    worldId: string,
    deploymentId: string,
    targetId: string,
    provider: string,
    resourceId: string
  ): void {
    this.database
      .query(
        `UPDATE resources SET status = 'deleted'
         WHERE world_id = ? AND deployment_id = ? AND target_id = ?
           AND provider = ? AND resource_id = ?`
      )
      .run(worldId, deploymentId, targetId, provider, resourceId);
  }

  resources(worldId: string): readonly ResourceRecord[] {
    return this.database
      .query<ResourceRow, [string]>(
        `SELECT * FROM resources WHERE world_id = ?
         ORDER BY provider, deployment_id, target_id, resource_id`
      )
      .all(worldId)
      .map((row) => {
        if (!row.target_id.trim()) {
          throw new CoreError(
            'ValidationFailed',
            'stored resource target identity is invalid'
          );
        }
        return {
          worldId: row.world_id,
          deploymentId: row.deployment_id,
          targetId: row.target_id,
          provider: row.provider,
          resourceType: row.resource_type,
          resourceId: row.resource_id,
          properties: parseObject(row.properties),
          status:
            row.status === 'pending'
              ? 'pending'
              : row.status === 'failed'
                ? 'failed'
                : row.status === 'deleted'
                  ? 'deleted'
                  : 'ready',
        };
      });
  }
}
