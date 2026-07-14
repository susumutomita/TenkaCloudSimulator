import {
  type JsonValue,
  SIMULATOR_SNAPSHOT_INTEGRITY_VERSION,
  type SimulatorSnapshotEnvelope,
} from './types.js';
import { assertSimulatorSnapshotEnvelope } from './validators.js';

export const SIMULATOR_SNAPSHOT_INTEGRITY_DOMAIN =
  'tenkacloud-simulator.snapshot-integrity' as const;

function canonicalJsonValue(value: JsonValue): string {
  if (value === null || typeof value !== 'object') {
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) {
    return `[${value.map(canonicalJsonValue).join(',')}]`;
  }
  return `{${Object.entries(value)
    .sort(([left], [right]) => Number(left > right) - Number(left < right))
    .map(
      ([key, child]) => `${JSON.stringify(key)}:${canonicalJsonValue(child)}`
    )
    .join(',')}}`;
}

export function canonicalSimulatorSnapshotIntegrityPayload(
  envelope: SimulatorSnapshotEnvelope
): string {
  assertSimulatorSnapshotEnvelope(envelope);
  const payload: JsonValue = JSON.parse(
    JSON.stringify({
      domain: SIMULATOR_SNAPSHOT_INTEGRITY_DOMAIN,
      version: SIMULATOR_SNAPSHOT_INTEGRITY_VERSION,
      envelope,
    })
  );
  return canonicalJsonValue(payload);
}
