import { CoreError } from '@tenkacloud/simulator-core';

export function objectValue(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an object`);
  }
  return value as Readonly<Record<string, unknown>>;
}

export function optionalObject(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> | undefined {
  return value === undefined ? undefined : objectValue(value, label);
}

export function stringValue(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new CoreError('ValidationFailed', `${label} must not be empty`);
  }
  return value;
}

export function optionalString(
  value: unknown,
  label: string
): string | undefined {
  return value === undefined ? undefined : stringValue(value, label);
}

export function stringArray(value: unknown, label: string): readonly string[] {
  if (!Array.isArray(value) || value.some((item) => typeof item !== 'string')) {
    throw new CoreError('ValidationFailed', `${label} must be a string array`);
  }
  return value;
}

export function optionalStringArray(
  value: unknown,
  label: string
): readonly string[] | undefined {
  return value === undefined ? undefined : stringArray(value, label);
}

export function numberValue(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    throw new CoreError('ValidationFailed', `${label} must be a number`);
  }
  return value;
}

export function booleanValue(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') {
    throw new CoreError('ValidationFailed', `${label} must be a boolean`);
  }
  return value;
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
