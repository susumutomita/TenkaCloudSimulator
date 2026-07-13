import { Database } from 'bun:sqlite';
import { randomUUID } from 'node:crypto';
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
  type: string;
  notnull: number;
  dflt_value: string | null;
  pk: number;
}

interface TableCountRow {
  count: number;
}

interface TableDefinitionRow {
  sql: string | null;
}

interface SchemaVersionRow {
  user_version: number;
}

interface ForeignKeyRow {
  id: number;
  seq: number;
  table: string;
  from: string;
  to: string;
  on_update: string;
  on_delete: string;
  match: string;
}

interface SchemaObjectRow {
  type: string;
  name: string;
  tbl_name: string;
  sql: string | null;
}

interface ForeignKeyViolationRow {
  table: string;
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

interface EventReservationRow {
  event_count: number;
  operation_kind: EventReservationKind;
  owner_id: string;
}

interface EventReservationOwnerRow {
  owner_id: string;
}

interface WorldIdRow {
  world_id: string;
}

interface EventReservationOwnerIdentity {
  readonly version: 1;
  readonly pid: number;
  readonly startIdentity: string | null;
  readonly nonce: string;
}

export type EventReservationKind = 'materialization' | 'deletion';

function readProcessStartIdentity(pid: number): string | undefined {
  try {
    const result = Bun.spawnSync({
      cmd: ['/bin/ps', '-o', 'lstart=', '-p', String(pid)],
      env: { ...process.env, LC_ALL: 'C' },
      stderr: 'ignore',
      stdout: 'pipe',
    });
    if (result.exitCode !== 0) return undefined;
    const identity = new TextDecoder().decode(result.stdout).trim();
    return identity.length > 0 ? identity : undefined;
  } catch {
    return undefined;
  }
}

const PROCESS_START_IDENTITY = readProcessStartIdentity(process.pid) ?? null;
const ACTIVE_EVENT_RESERVATION_OWNERS = new Set<string>();

function eventReservationOwner(): string {
  return JSON.stringify({
    version: 1,
    pid: process.pid,
    startIdentity: PROCESS_START_IDENTITY,
    nonce: randomUUID(),
  } satisfies EventReservationOwnerIdentity);
}
const CURRENT_SCHEMA_VERSION = 2;
const CURRENT_TABLE_NAMES = [
  'worlds',
  'events',
  'deployments',
  'idempotency',
  'resources',
  'event_reservations',
] as const;
type CurrentTableName = (typeof CURRENT_TABLE_NAMES)[number];
const PRE_RESERVATION_TABLE_NAMES = [
  'worlds',
  'events',
  'deployments',
  'idempotency',
  'resources',
] as const satisfies readonly CurrentTableName[];

const CURRENT_TABLE_SQL = {
  worlds: `CREATE TABLE worlds (
    world_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    team_id TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    seed TEXT NOT NULL,
    virtual_time TEXT NOT NULL,
    status TEXT NOT NULL,
    next_sequence INTEGER NOT NULL
  )`,
  events: `CREATE TABLE events (
    world_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    type TEXT NOT NULL,
    virtual_time TEXT NOT NULL,
    command_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    PRIMARY KEY (world_id, sequence),
    FOREIGN KEY (world_id) REFERENCES worlds(world_id)
  )`,
  deployments: `CREATE TABLE deployments (
    world_id TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    problem_id TEXT NOT NULL,
    status TEXT NOT NULL,
    targets TEXT NOT NULL,
    outputs TEXT NOT NULL,
    diagnostics TEXT NOT NULL,
    PRIMARY KEY (world_id, deployment_id),
    FOREIGN KEY (world_id) REFERENCES worlds(world_id)
  )`,
  idempotency: `CREATE TABLE idempotency (
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response TEXT NOT NULL,
    PRIMARY KEY (scope, key)
  )`,
  resources: `CREATE TABLE resources (
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
  )`,
  event_reservations: `CREATE TABLE event_reservations (
    world_id TEXT NOT NULL,
    reservation_id TEXT NOT NULL,
    operation_kind TEXT NOT NULL CHECK (operation_kind IN ('materialization', 'deletion')),
    owner_id TEXT NOT NULL,
    event_count INTEGER NOT NULL CHECK (event_count > 0),
    PRIMARY KEY (world_id, reservation_id),
    FOREIGN KEY (world_id) REFERENCES worlds(world_id)
  )`,
} as const satisfies Readonly<Record<CurrentTableName, string>>;

function createTableIfMissing(tableSql: string): string {
  return `${tableSql.replace('CREATE TABLE', 'CREATE TABLE IF NOT EXISTS')};`;
}

const BASE_TABLES_SQL = [
  CURRENT_TABLE_SQL.worlds,
  CURRENT_TABLE_SQL.events,
  CURRENT_TABLE_SQL.deployments,
  CURRENT_TABLE_SQL.idempotency,
  CURRENT_TABLE_SQL.event_reservations,
]
  .map(createTableIfMissing)
  .join('\n');

const RESOURCE_TABLE_SQL = createTableIfMissing(CURRENT_TABLE_SQL.resources);
const CURRENT_TABLE_COLUMNS = {
  worlds: [
    'world_id',
    'tenant_id',
    'event_id',
    'team_id',
    'deployment_id',
    'seed',
    'virtual_time',
    'status',
    'next_sequence',
  ],
  events: [
    'world_id',
    'sequence',
    'type',
    'virtual_time',
    'command_id',
    'payload',
    'payload_hash',
  ],
  deployments: [
    'world_id',
    'deployment_id',
    'problem_id',
    'status',
    'targets',
    'outputs',
    'diagnostics',
  ],
  idempotency: ['scope', 'key', 'request_hash', 'response'],
  resources: [
    'world_id',
    'deployment_id',
    'target_id',
    'provider',
    'resource_type',
    'resource_id',
    'properties',
    'status',
  ],
  event_reservations: [
    'world_id',
    'reservation_id',
    'operation_kind',
    'owner_id',
    'event_count',
  ],
} as const;

const LEGACY_DEPLOYMENT_COLUMNS = [
  'world_id',
  'deployment_id',
  'problem_id',
  'status',
  'outputs',
  'diagnostics',
] as const;

const LEGACY_RESOURCE_COLUMNS = [
  'world_id',
  'deployment_id',
  'provider',
  'resource_type',
  'resource_id',
  'properties',
  'status',
] as const;

const PREVIOUSLY_MIGRATED_DEPLOYMENT_COLUMNS = [
  ...LEGACY_DEPLOYMENT_COLUMNS,
  'targets',
] as const;

const LEGACY_DEPLOYMENT_TABLE_SQL = `CREATE TABLE deployments (
  world_id TEXT NOT NULL,
  deployment_id TEXT NOT NULL,
  problem_id TEXT NOT NULL,
  status TEXT NOT NULL,
  outputs TEXT NOT NULL,
  diagnostics TEXT NOT NULL,
  PRIMARY KEY (world_id, deployment_id),
  FOREIGN KEY (world_id) REFERENCES worlds(world_id)
)`;

const LEGACY_RESOURCE_TABLE_SQL = `CREATE TABLE resources (
  world_id TEXT NOT NULL,
  deployment_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  properties TEXT NOT NULL,
  status TEXT NOT NULL,
  PRIMARY KEY (world_id, provider, resource_id),
  FOREIGN KEY (world_id) REFERENCES worlds(world_id)
)`;

const HISTORICAL_DEPLOYMENT_TABLE_SQL = `CREATE TABLE deployments (
  world_id TEXT NOT NULL,
  deployment_id TEXT NOT NULL,
  problem_id TEXT NOT NULL,
  status TEXT NOT NULL,
  outputs TEXT NOT NULL,
  diagnostics TEXT NOT NULL,
  targets TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (world_id, deployment_id),
  FOREIGN KEY (world_id) REFERENCES worlds(world_id)
)`;

const CURRENT_PRIMARY_KEYS = {
  worlds: ['world_id'],
  events: ['world_id', 'sequence'],
  deployments: ['world_id', 'deployment_id'],
  idempotency: ['scope', 'key'],
  resources: [
    'world_id',
    'deployment_id',
    'target_id',
    'provider',
    'resource_id',
  ],
  event_reservations: ['world_id', 'reservation_id'],
} as const;

interface LegacyTableShape {
  readonly columns: readonly string[];
  readonly primaryKey: readonly string[];
  readonly defaults?: Readonly<Record<string, string>>;
  readonly tableSql: string;
}

const LEGACY_DEPLOYMENT_SHAPE: LegacyTableShape = {
  columns: LEGACY_DEPLOYMENT_COLUMNS,
  primaryKey: ['world_id', 'deployment_id'],
  tableSql: LEGACY_DEPLOYMENT_TABLE_SQL,
};

const LEGACY_RESOURCE_SHAPE: LegacyTableShape = {
  columns: LEGACY_RESOURCE_COLUMNS,
  primaryKey: ['world_id', 'provider', 'resource_id'],
  tableSql: LEGACY_RESOURCE_TABLE_SQL,
};

const HISTORICAL_DEPLOYMENT_SHAPE: LegacyTableShape = {
  columns: PREVIOUSLY_MIGRATED_DEPLOYMENT_COLUMNS,
  primaryKey: ['world_id', 'deployment_id'],
  defaults: { targets: "'[]'" },
  tableSql: HISTORICAL_DEPLOYMENT_TABLE_SQL,
};

type SchemaShape = 'missing' | 'current' | 'historical' | 'legacy';
type SchemaShapes = Readonly<Record<CurrentTableName, SchemaShape>>;

const SCHEMA_SQL_TOKEN =
  /'(?:''|[^'])*'|[A-Za-z_][A-Za-z0-9_$]*|\d+(?:\.\d+)?|[^\s]/g;

function normalizedSchemaSql(sql: string | null): string {
  return Array.from(String(sql).matchAll(SCHEMA_SQL_TOKEN), (match) => {
    const token = match[0];
    return token.startsWith("'") ? token : token.toLowerCase();
  }).join('\u001f');
}

function sameColumns(
  actual: readonly string[],
  expected: readonly string[]
): boolean {
  return (
    actual.length === expected.length &&
    actual.every((column, index) => column === expected[index])
  );
}

function primaryKey(columns: readonly TableColumnRow[]): readonly string[] {
  return columns
    .filter((column) => column.pk > 0)
    .sort((left, right) => left.pk - right.pk)
    .map((column) => column.name);
}

function columnDefinitionsMatch(
  tableName: CurrentTableName,
  columns: readonly TableColumnRow[],
  defaults: Readonly<Record<string, string>> = {}
): boolean {
  return columns.every((column) => {
    const expectedType = ['sequence', 'next_sequence', 'event_count'].includes(
      column.name
    )
      ? 'INTEGER'
      : 'TEXT';
    const expectedNotNull =
      tableName === 'worlds' && column.name === 'world_id' ? 0 : 1;
    return (
      column.type === expectedType &&
      column.notnull === expectedNotNull &&
      column.dflt_value === (defaults[column.name] ?? null)
    );
  });
}

function foreignKeysMatch(
  database: Database,
  tableName: CurrentTableName
): boolean {
  const foreignKeys = database
    .query<ForeignKeyRow, []>(`PRAGMA foreign_key_list(${tableName})`)
    .all();
  const expectsWorld = [
    'events',
    'deployments',
    'resources',
    'event_reservations',
  ].includes(tableName);
  if (!expectsWorld) return foreignKeys.length === 0;
  const foreignKey = foreignKeys[0];
  return (
    foreignKeys.length === 1 &&
    foreignKey?.id === 0 &&
    foreignKey.seq === 0 &&
    foreignKey.table === 'worlds' &&
    foreignKey.from === 'world_id' &&
    foreignKey.to === 'world_id' &&
    foreignKey.on_update === 'NO ACTION' &&
    foreignKey.on_delete === 'NO ACTION' &&
    foreignKey.match === 'NONE'
  );
}

function matchesShape(
  database: Database,
  tableName: CurrentTableName,
  columns: readonly TableColumnRow[],
  expectedColumns: readonly string[],
  expectedPrimaryKey: readonly string[],
  actualTableSql: string | null,
  expectedTableSql: string,
  defaults: Readonly<Record<string, string>> = {}
): boolean {
  return (
    sameColumns(
      columns.map((column) => column.name),
      expectedColumns
    ) &&
    sameColumns(primaryKey(columns), expectedPrimaryKey) &&
    columnDefinitionsMatch(tableName, columns, defaults) &&
    foreignKeysMatch(database, tableName) &&
    normalizedSchemaSql(actualTableSql) ===
      normalizedSchemaSql(expectedTableSql)
  );
}

function tableShape(
  database: Database,
  tableName: CurrentTableName,
  legacyShape?: LegacyTableShape
): SchemaShape {
  const table = database
    .query<TableDefinitionRow, [string]>(
      `SELECT sql FROM sqlite_master
       WHERE type = 'table' AND name = ?`
    )
    .get(tableName);
  if (!table) return 'missing';
  const columns = database
    .query<TableColumnRow, []>(`PRAGMA table_info(${tableName})`)
    .all();
  const currentPrimaryKey = CURRENT_PRIMARY_KEYS[tableName];
  if (
    matchesShape(
      database,
      tableName,
      columns,
      CURRENT_TABLE_COLUMNS[tableName],
      currentPrimaryKey,
      table.sql,
      CURRENT_TABLE_SQL[tableName]
    )
  ) {
    return 'current';
  }
  if (
    tableName === 'deployments' &&
    matchesShape(
      database,
      tableName,
      columns,
      HISTORICAL_DEPLOYMENT_SHAPE.columns,
      HISTORICAL_DEPLOYMENT_SHAPE.primaryKey,
      table.sql,
      HISTORICAL_DEPLOYMENT_SHAPE.tableSql,
      HISTORICAL_DEPLOYMENT_SHAPE.defaults
    )
  ) {
    return 'historical';
  }
  if (
    legacyShape &&
    matchesShape(
      database,
      tableName,
      columns,
      legacyShape.columns,
      legacyShape.primaryKey,
      table.sql,
      legacyShape.tableSql,
      legacyShape.defaults
    )
  ) {
    return 'legacy';
  }
  throw new CoreError(
    'ValidationFailed',
    `${tableName} schema is incompatible with version ${CURRENT_SCHEMA_VERSION}`
  );
}

function assertLegacyTableIsEmpty(
  database: Database,
  tableName: 'deployments' | 'resources',
  message: string
): void {
  const row = database
    .query<TableCountRow, []>(`SELECT COUNT(*) AS count FROM ${tableName}`)
    .get();
  if ((row?.count ?? 0) > 0) {
    throw new CoreError('ValidationFailed', message);
  }
}

function schemaVersion(database: Database): number {
  const version = database
    .query<SchemaVersionRow, []>('PRAGMA user_version')
    .get()?.user_version;
  if (
    version === undefined ||
    !Number.isSafeInteger(version) ||
    version < 0 ||
    version > CURRENT_SCHEMA_VERSION
  ) {
    throw new CoreError(
      'ValidationFailed',
      `SQLite schema version ${String(version)} is not supported`
    );
  }
  return version;
}

function assertCurrentTable(
  database: Database,
  tableName: CurrentTableName
): void {
  if (tableShape(database, tableName) !== 'current') {
    throw new CoreError(
      'ValidationFailed',
      `${tableName} schema is missing from version ${CURRENT_SCHEMA_VERSION}`
    );
  }
}

function assertKnownSchemaObjects(database: Database): void {
  const unexpected = database
    .query<SchemaObjectRow, []>(
      `SELECT type, name, tbl_name, sql FROM sqlite_master
       ORDER BY type, name`
    )
    .all()
    .filter((object) => {
      const tableName = object.tbl_name as CurrentTableName;
      if (!CURRENT_TABLE_NAMES.includes(tableName)) return true;
      if (object.type === 'table') return object.name !== tableName;
      return (
        object.type !== 'index' ||
        object.name !== `sqlite_autoindex_${tableName}_1` ||
        object.sql !== null
      );
    });
  if (unexpected.length > 0) {
    throw new CoreError(
      'ValidationFailed',
      `simulator state contains unknown schema object ${unexpected[0]?.name ?? '<unknown>'}`
    );
  }
}

function assertNoForeignKeyViolations(database: Database): void {
  const violation = database
    .query<ForeignKeyViolationRow, []>('PRAGMA foreign_key_check')
    .get();
  if (violation) {
    throw new CoreError(
      'ValidationFailed',
      `simulator state has a foreign key violation in ${violation.table}`
    );
  }
}

function inspectSchema(database: Database, allowLegacy: boolean): SchemaShapes {
  return {
    worlds: tableShape(database, 'worlds'),
    events: tableShape(database, 'events'),
    deployments: tableShape(
      database,
      'deployments',
      allowLegacy ? LEGACY_DEPLOYMENT_SHAPE : undefined
    ),
    idempotency: tableShape(database, 'idempotency'),
    resources: tableShape(
      database,
      'resources',
      allowLegacy ? LEGACY_RESOURCE_SHAPE : undefined
    ),
    event_reservations: tableShape(database, 'event_reservations'),
  };
}

function hasExactlyKnownTables(
  shapes: SchemaShapes,
  expected: readonly CurrentTableName[]
): boolean {
  const expectedNames = new Set(expected);
  return CURRENT_TABLE_NAMES.every(
    (tableName) =>
      (shapes[tableName] !== 'missing') === expectedNames.has(tableName)
  );
}

function assertUnversionedSchemaSet(shapes: SchemaShapes): void {
  const shapesPresent = Object.values(shapes).filter(
    (shape) => shape !== 'missing'
  );
  if (
    shapesPresent.length > 0 &&
    !hasExactlyKnownTables(shapes, PRE_RESERVATION_TABLE_NAMES) &&
    !hasExactlyKnownTables(shapes, CURRENT_TABLE_NAMES)
  ) {
    throw new CoreError(
      'ValidationFailed',
      'simulator state schema is partial or incompatible'
    );
  }
  if (
    shapesPresent.length > 0 &&
    (shapes.deployments === 'legacy') !== (shapes.resources === 'legacy')
  ) {
    throw new CoreError(
      'ValidationFailed',
      'simulator state schema is partially migrated or incompatible'
    );
  }
}

function assertVersionOneSchemaSet(shapes: SchemaShapes): void {
  for (const tableName of PRE_RESERVATION_TABLE_NAMES) {
    if (shapes[tableName] !== 'current') {
      throw new CoreError(
        'ValidationFailed',
        `${tableName} schema is incompatible with version 1`
      );
    }
  }
  if (shapes.event_reservations !== 'missing') {
    throw new CoreError(
      'ValidationFailed',
      'event_reservations schema is incompatible with version 1'
    );
  }
}

function assertCurrentSchemaSet(shapes: SchemaShapes): void {
  for (const [tableName, shape] of Object.entries(shapes)) {
    if (shape !== 'current') {
      throw new CoreError(
        'ValidationFailed',
        `${tableName} schema is missing from version ${CURRENT_SCHEMA_VERSION}`
      );
    }
  }
}

function assertCompatibleSchemaSet(
  version: number,
  shapes: SchemaShapes
): void {
  if (version === 0) {
    assertUnversionedSchemaSet(shapes);
    return;
  }
  if (version === 1) {
    assertVersionOneSchemaSet(shapes);
    return;
  }
  assertCurrentSchemaSet(shapes);
}

function assertLegacyTablesAreEmpty(
  database: Database,
  shapes: SchemaShapes
): void {
  if (shapes.deployments === 'legacy') {
    assertLegacyTableIsEmpty(
      database,
      'deployments',
      'stored deployments have no target identity'
    );
  }
  if (shapes.resources === 'legacy') {
    assertLegacyTableIsEmpty(
      database,
      'resources',
      'stored resources have no target identity'
    );
  }
}

function migrateLegacyTables(database: Database, shapes: SchemaShapes): void {
  database.exec(BASE_TABLES_SQL);
  database.exec(RESOURCE_TABLE_SQL);
  if (shapes.deployments === 'legacy') {
    database.exec('DROP TABLE deployments');
    database.exec(BASE_TABLES_SQL);
  }
  if (shapes.deployments === 'historical') {
    database.exec('ALTER TABLE deployments RENAME TO deployments_historical');
    database.exec(BASE_TABLES_SQL);
    database.exec(`
      INSERT INTO deployments (
        world_id, deployment_id, problem_id, status, targets, outputs, diagnostics
      )
      SELECT
        world_id, deployment_id, problem_id, status, targets, outputs, diagnostics
      FROM deployments_historical
    `);
    database.exec('DROP TABLE deployments_historical');
  }
  if (shapes.resources === 'legacy') {
    database.exec('DROP TABLE resources');
    database.exec(RESOURCE_TABLE_SQL);
  }
}

function migrateSchema(database: Database): void {
  database.exec('BEGIN IMMEDIATE');
  try {
    const version = schemaVersion(database);
    assertKnownSchemaObjects(database);
    const shapes = inspectSchema(database, version === 0);
    assertCompatibleSchemaSet(version, shapes);
    assertLegacyTablesAreEmpty(database, shapes);
    migrateLegacyTables(database, shapes);
    assertKnownSchemaObjects(database);
    for (const tableName of CURRENT_TABLE_NAMES) {
      assertCurrentTable(database, tableName);
    }
    assertNoForeignKeyViolations(database);
    database.exec(`PRAGMA user_version = ${CURRENT_SCHEMA_VERSION}`);
    database.exec('COMMIT');
  } catch (error) {
    if (database.inTransaction) database.exec('ROLLBACK');
    throw error;
  }
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function parseEventReservationOwner(
  ownerId: string
): EventReservationOwnerIdentity | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(ownerId);
  } catch {
    return undefined;
  }
  if (!isRecord(parsed)) return undefined;
  const version = parsed['version'];
  const pid = parsed['pid'];
  const startIdentity = parsed['startIdentity'];
  const nonce = parsed['nonce'];
  if (
    version !== 1 ||
    typeof pid !== 'number' ||
    !Number.isSafeInteger(pid) ||
    pid < 1 ||
    (startIdentity !== null && typeof startIdentity !== 'string') ||
    typeof nonce !== 'string' ||
    nonce.length === 0
  ) {
    return undefined;
  }
  return { version, pid, startIdentity, nonce };
}

function isEventReservationOwnerProvablyDead(ownerId: string): boolean {
  const owner = parseEventReservationOwner(ownerId);
  if (!owner) return false;
  if (owner.pid === process.pid) {
    return !ACTIVE_EVENT_RESERVATION_OWNERS.has(ownerId);
  }
  try {
    process.kill(owner.pid, 0);
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === 'ESRCH';
  }
  if (owner.startIdentity === null) return false;
  const currentIdentity = readProcessStartIdentity(owner.pid);
  return (
    currentIdentity !== undefined && currentIdentity !== owner.startIdentity
  );
}

function recoverDeadEventReservations(database: Database): void {
  const owners = database
    .query<EventReservationOwnerRow, []>(
      `SELECT DISTINCT owner_id FROM event_reservations
       WHERE operation_kind = 'materialization'`
    )
    .all();
  const removeOwner = database.query(
    `DELETE FROM event_reservations
     WHERE owner_id = ? AND operation_kind = 'materialization'`
  );
  for (const { owner_id: ownerId } of owners) {
    if (isEventReservationOwnerProvablyDead(ownerId)) {
      removeOwner.run(ownerId);
    }
  }
}

function isSqliteContention(error: unknown): boolean {
  if (!isRecord(error)) return false;
  const code = error['code'];
  const errno = error['errno'];
  return (
    errno === 5 ||
    code === 'SQLITE_BUSY' ||
    code === 'SQLITE_BUSY_SNAPSHOT' ||
    code === 'SQLITE_LOCKED'
  );
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
  readonly #eventReservationOwner = eventReservationOwner();
  #closed = false;

  constructor(path: string) {
    ACTIVE_EVENT_RESERVATION_OWNERS.add(this.#eventReservationOwner);
    try {
      this.database = new Database(path, { create: true, strict: true });
    } catch (error) {
      ACTIVE_EVENT_RESERVATION_OWNERS.delete(this.#eventReservationOwner);
      throw error;
    }
    try {
      this.database.exec('PRAGMA busy_timeout = 5000');
      this.database.exec('PRAGMA foreign_keys = ON');
      migrateSchema(this.database);
      this.database
        .transaction(() => recoverDeadEventReservations(this.database))
        .immediate();
      this.database.exec('PRAGMA journal_mode = WAL');
    } catch (error) {
      ACTIVE_EVENT_RESERVATION_OWNERS.delete(this.#eventReservationOwner);
      this.database.close();
      throw error;
    }
  }

  close(): void {
    if (this.#closed) return;
    try {
      this.transaction(() => {
        this.database
          .query(
            `DELETE FROM event_reservations
             WHERE owner_id = ? AND operation_kind = 'materialization'`
          )
          .run(this.#eventReservationOwner);
      });
    } finally {
      this.#closed = true;
      ACTIVE_EVENT_RESERVATION_OWNERS.delete(this.#eventReservationOwner);
      this.database.close();
    }
  }

  transaction<T>(operation: () => T): T {
    const transaction = this.database.transaction(operation);
    try {
      return this.database.inTransaction
        ? transaction()
        : transaction.immediate();
    } catch (error) {
      if (isSqliteContention(error)) {
        throw new CoreError(
          'Conflict',
          'simulation store is busy; retry the operation'
        );
      }
      throw error;
    }
  }

  recoverDeadEventReservations(): void {
    if (!this.database.inTransaction) {
      this.transaction(() => recoverDeadEventReservations(this.database));
      return;
    }
    recoverDeadEventReservations(this.database);
  }

  reservedEventCount(worldId: string): number {
    return (
      this.database
        .query<TableCountRow, [string]>(
          'SELECT COALESCE(SUM(event_count), 0) AS count FROM event_reservations WHERE world_id = ?'
        )
        .get(worldId)?.count ?? 0
    );
  }

  reserveEvents(
    worldId: string,
    reservationId: string,
    eventCount: number,
    operationKind: EventReservationKind = 'materialization'
  ): void {
    if (!this.database.inTransaction) {
      this.transaction(() =>
        this.reserveEvents(worldId, reservationId, eventCount, operationKind)
      );
      return;
    }
    if (!Number.isSafeInteger(eventCount) || eventCount < 1) {
      throw new CoreError(
        'ValidationFailed',
        'event reservation count is invalid'
      );
    }
    this.recoverDeadEventReservations();
    const existing = this.database
      .query<EventReservationRow, [string, string]>(
        `SELECT operation_kind, owner_id, event_count FROM event_reservations
         WHERE world_id = ? AND reservation_id = ?`
      )
      .get(worldId, reservationId);
    if (existing) {
      if (
        existing.event_count !== eventCount ||
        existing.operation_kind !== operationKind
      ) {
        throw new CoreError(
          'Conflict',
          'event reservation identity is inconsistent'
        );
      }
      if (existing.owner_id === this.#eventReservationOwner) return;
      if (
        operationKind === 'deletion' &&
        isEventReservationOwnerProvablyDead(existing.owner_id)
      ) {
        const adopted = this.database
          .query(
            `UPDATE event_reservations SET owner_id = ?
             WHERE world_id = ? AND reservation_id = ? AND owner_id = ?
               AND operation_kind = 'deletion'`
          )
          .run(
            this.#eventReservationOwner,
            worldId,
            reservationId,
            existing.owner_id
          );
        if (adopted.changes === 1) return;
      }
      throw new CoreError(
        'Conflict',
        'event reservation identity is inconsistent'
      );
    }
    this.database
      .query(
        `INSERT INTO event_reservations(
           world_id, reservation_id, operation_kind, owner_id, event_count
         ) VALUES (?, ?, ?, ?, ?)`
      )
      .run(
        worldId,
        reservationId,
        operationKind,
        this.#eventReservationOwner,
        eventCount
      );
  }

  hasOtherEventReservation(worldId: string, reservationId: string): boolean {
    const row = this.database
      .query<{ present: number }, [string, string]>(
        `SELECT 1 AS present FROM event_reservations
         WHERE world_id = ? AND reservation_id <> ? LIMIT 1`
      )
      .get(worldId, reservationId);
    return Boolean(row);
  }

  pendingDeletionWorldIds(): readonly string[] {
    return this.database
      .query<WorldIdRow, []>(
        `SELECT DISTINCT world_id FROM event_reservations
         WHERE operation_kind = 'deletion' ORDER BY world_id`
      )
      .all()
      .map((row) => row.world_id);
  }

  releaseEvents(worldId: string, reservationId: string): void {
    if (!this.database.inTransaction) {
      this.transaction(() => this.releaseEvents(worldId, reservationId));
      return;
    }
    this.database
      .query(
        'DELETE FROM event_reservations WHERE world_id = ? AND reservation_id = ? AND owner_id = ?'
      )
      .run(worldId, reservationId, this.#eventReservationOwner);
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

  idempotentResponse<T = unknown>(scope: string, key: string): T | undefined {
    const row = this.database
      .query<Pick<IdempotencyRow, 'response'>, [string, string]>(
        'SELECT response FROM idempotency WHERE scope = ? AND key = ?'
      )
      .get(scope, key);
    return row ? (JSON.parse(row.response) as T) : undefined;
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
