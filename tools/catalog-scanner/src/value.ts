import { FIDELITIES, type Fidelity } from './model.ts';

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function stringValue(
  record: Record<string, unknown>,
  key: string
): string | undefined {
  const value = recordValue(record, key);
  return typeof value === 'string' && value.length > 0 ? value : undefined;
}

export function recordValue(
  record: Record<string, unknown>,
  key: string
): unknown {
  return record[key];
}

export function isFidelity(value: unknown): value is Fidelity {
  return typeof value === 'string' && FIDELITIES.some((item) => item === value);
}

export function unexpectedKeys(
  record: Record<string, unknown>,
  allowed: readonly string[]
): string[] {
  const allowedKeys = new Set(allowed);
  return Object.keys(record)
    .filter((key) => !allowedKeys.has(key))
    .sort();
}
