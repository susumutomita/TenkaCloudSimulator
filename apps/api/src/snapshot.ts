import {
  assertSimulatorSnapshotEnvelope,
  SIMULATOR_PROTOCOL_VERSION,
  SIMULATOR_SNAPSHOT_VERSION,
  type SimulatorSnapshot,
  type SimulatorSnapshotEnvelope,
} from '@tenkacloud/simulator-contracts';
import type {
  CapabilityDiagnostic,
  DeploymentRecord,
  EventRecord,
  FidelityLevel,
  ResourceRecord,
  WorldRecord,
  WorldSnapshot,
} from '@tenkacloud/simulator-core';
import { RequestValidationError } from './errors.js';

interface SnapshotRecord extends Record<string, unknown> {
  readonly availableFidelity?: unknown;
  readonly code?: unknown;
  readonly commandId?: unknown;
  readonly deploymentId?: unknown;
  readonly deployments?: unknown;
  readonly diagnostics?: unknown;
  readonly engine?: unknown;
  readonly eventId?: unknown;
  readonly events?: unknown;
  readonly fidelity?: unknown;
  readonly hash?: unknown;
  readonly line?: unknown;
  readonly operation?: unknown;
  readonly outputs?: unknown;
  readonly path?: unknown;
  readonly payload?: unknown;
  readonly payloadHash?: unknown;
  readonly problemId?: unknown;
  readonly properties?: unknown;
  readonly provider?: unknown;
  readonly resourceId?: unknown;
  readonly resources?: unknown;
  readonly resourceType?: unknown;
  readonly seed?: unknown;
  readonly sequence?: unknown;
  readonly service?: unknown;
  readonly snapshotVersion?: unknown;
  readonly source?: unknown;
  readonly status?: unknown;
  readonly teamId?: unknown;
  readonly targetId?: unknown;
  readonly tenantId?: unknown;
  readonly type?: unknown;
  readonly virtualTime?: unknown;
  readonly world?: unknown;
  readonly worldId?: unknown;
}

function isRecord(value: unknown): value is SnapshotRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isFidelityLevel(value: unknown): value is FidelityLevel {
  return (
    value === 'L0' ||
    value === 'L1' ||
    value === 'L2' ||
    value === 'L3' ||
    value === 'L4'
  );
}

function isDiagnosticCode(
  value: unknown
): value is CapabilityDiagnostic['code'] {
  return (
    value === 'MissingProvider' ||
    value === 'MissingEngine' ||
    value === 'MissingCapability' ||
    value === 'InsufficientFidelity'
  );
}

function isSource(value: unknown): boolean {
  return (
    isRecord(value) &&
    typeof value.path === 'string' &&
    (value.line === undefined ||
      (typeof value.line === 'number' && Number.isInteger(value.line)))
  );
}

function isCapabilityDiagnostic(value: unknown): value is CapabilityDiagnostic {
  return (
    isRecord(value) &&
    typeof value.provider === 'string' &&
    typeof value.engine === 'string' &&
    typeof value.service === 'string' &&
    typeof value.resourceType === 'string' &&
    typeof value.operation === 'string' &&
    Array.isArray(value.fidelity) &&
    value.fidelity.every(isFidelityLevel) &&
    isDiagnosticCode(value.code) &&
    Array.isArray(value.availableFidelity) &&
    value.availableFidelity.every(isFidelityLevel) &&
    (value.source === undefined || isSource(value.source))
  );
}

function isWorldRecord(value: unknown): value is WorldRecord {
  return (
    isRecord(value) &&
    typeof value.worldId === 'string' &&
    typeof value.tenantId === 'string' &&
    typeof value.eventId === 'string' &&
    typeof value.teamId === 'string' &&
    typeof value.deploymentId === 'string' &&
    typeof value.seed === 'string' &&
    typeof value.virtualTime === 'string' &&
    (value.status === 'active' || value.status === 'deleted')
  );
}

function isEventRecord(value: unknown): value is EventRecord {
  return (
    isRecord(value) &&
    typeof value.worldId === 'string' &&
    Number.isSafeInteger(value.sequence) &&
    typeof value.type === 'string' &&
    typeof value.virtualTime === 'string' &&
    typeof value.commandId === 'string' &&
    isRecord(value.payload) &&
    typeof value.payloadHash === 'string'
  );
}

function isStringMap(
  value: unknown
): value is Readonly<Record<string, string>> {
  return (
    isRecord(value) &&
    Object.values(value).every((item) => typeof item === 'string')
  );
}

function isOutputMap(
  value: unknown
): value is Readonly<Record<string, Readonly<Record<string, string>>>> {
  return isRecord(value) && Object.values(value).every(isStringMap);
}

function isDeploymentTarget(value: unknown): boolean {
  return (
    isRecord(value) &&
    typeof value['id'] === 'string' &&
    typeof value.provider === 'string' &&
    typeof value.engine === 'string'
  );
}

function isDeploymentRecord(value: unknown): value is DeploymentRecord {
  return (
    isRecord(value) &&
    typeof value.worldId === 'string' &&
    typeof value.deploymentId === 'string' &&
    typeof value.problemId === 'string' &&
    (value.status === 'deploying' ||
      value.status === 'ready' ||
      value.status === 'failed' ||
      value.status === 'rejected' ||
      value.status === 'deleted') &&
    Array.isArray(value['targets']) &&
    value['targets'].every(isDeploymentTarget) &&
    isOutputMap(value.outputs) &&
    Array.isArray(value.diagnostics) &&
    value.diagnostics.every(isCapabilityDiagnostic)
  );
}

function isResourceRecord(value: unknown): value is ResourceRecord {
  return (
    isRecord(value) &&
    typeof value.worldId === 'string' &&
    typeof value.deploymentId === 'string' &&
    typeof value.targetId === 'string' &&
    typeof value.provider === 'string' &&
    typeof value.resourceType === 'string' &&
    typeof value.resourceId === 'string' &&
    isRecord(value.properties) &&
    (value.status === 'pending' ||
      value.status === 'ready' ||
      value.status === 'failed' ||
      value.status === 'deleted')
  );
}

function isWorldSnapshot(value: unknown): value is WorldSnapshot {
  if (!isRecord(value) || typeof value.hash !== 'string') return false;
  const payload = value.payload;
  return (
    isRecord(payload) &&
    payload.snapshotVersion === '1' &&
    isWorldRecord(payload.world) &&
    Array.isArray(payload.events) &&
    payload.events.every(isEventRecord) &&
    Array.isArray(payload.deployments) &&
    payload.deployments.every(isDeploymentRecord) &&
    Array.isArray(payload.resources) &&
    payload.resources.every(isResourceRecord)
  );
}

function hasCoreGraphFields(graph: Readonly<Record<string, unknown>>): boolean {
  const keys = Object.keys(graph).sort();
  return (
    keys.length === 4 &&
    keys[0] === 'deployments' &&
    keys[1] === 'events' &&
    keys[2] === 'resources' &&
    keys[3] === 'world'
  );
}

function lastSequence(events: readonly EventRecord[]): number {
  return events.reduce(
    (highest, event) => Math.max(highest, event.sequence),
    0
  );
}

export function simulatorSnapshot(
  snapshot: WorldSnapshot
): SimulatorSnapshotEnvelope {
  const { world } = snapshot.payload;
  const response: unknown = {
    snapshotVersion: SIMULATOR_SNAPSHOT_VERSION,
    protocolVersion: SIMULATOR_PROTOCOL_VERSION,
    worldId: world.worldId,
    namespace: {
      tenantId: world.tenantId,
      eventId: world.eventId,
      teamId: world.teamId,
    },
    seed: world.seed,
    clock: world.virtualTime,
    lastSequence: lastSequence(snapshot.payload.events),
    resourceGraph: {
      world,
      events: snapshot.payload.events,
      deployments: snapshot.payload.deployments,
      resources: snapshot.payload.resources,
    },
    providerProjections: {},
    hash: snapshot.hash,
  };
  assertSimulatorSnapshotEnvelope(response);
  return response;
}

export function coreSnapshot(snapshot: SimulatorSnapshot): WorldSnapshot {
  const graph = snapshot.resourceGraph;
  const { deployments, events, resources, world: projectedWorld } = graph;
  const candidate: unknown = {
    payload: {
      snapshotVersion: '1',
      world: projectedWorld,
      events,
      deployments,
      resources,
    },
    hash: snapshot.hash,
  };
  if (
    !hasCoreGraphFields(graph) ||
    Object.keys(snapshot.providerProjections).length !== 0 ||
    !isWorldSnapshot(candidate)
  ) {
    throw new RequestValidationError(
      'snapshot resourceGraph does not contain a valid core projection'
    );
  }
  const world = candidate.payload.world;
  if (
    snapshot.worldId !== world.worldId ||
    snapshot.namespace.tenantId !== world.tenantId ||
    snapshot.namespace.eventId !== world.eventId ||
    snapshot.namespace.teamId !== world.teamId ||
    snapshot.seed !== world.seed ||
    snapshot.clock !== world.virtualTime ||
    snapshot.lastSequence !== lastSequence(candidate.payload.events)
  ) {
    throw new RequestValidationError(
      'snapshot envelope does not match its core projection'
    );
  }
  return candidate;
}
