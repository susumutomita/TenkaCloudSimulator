import { deterministicId } from '@tenkacloud/simulator-core';
import type { Context } from 'hono';
import { RequestValidationError } from './errors.js';

export interface ProviderCommandRequest {
  readonly deploymentId: string;
  readonly targetId: string;
  readonly engine: string;
  readonly service: string;
  readonly resourceType: string;
  readonly input: Readonly<Record<string, unknown>>;
}

interface RequestRecord extends Record<string, unknown> {
  readonly input?: unknown;
}

function isRecord(value: unknown): value is RequestRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function requiredText(value: Record<string, unknown>, key: string): string {
  const candidate = value[key];
  if (typeof candidate !== 'string' || !candidate.trim()) {
    throw new RequestValidationError(`${key} must be a non-empty string`);
  }
  return candidate;
}

export async function readJson(c: Context): Promise<unknown> {
  return c.req.json<unknown>();
}

export function parseProviderCommand(value: unknown): ProviderCommandRequest {
  if (!isRecord(value) || !isRecord(value.input)) {
    throw new RequestValidationError(
      'provider command must be an object with an input object'
    );
  }
  return {
    deploymentId: requiredText(value, 'deploymentId'),
    targetId: requiredText(value, 'targetId'),
    engine: requiredText(value, 'engine'),
    service: requiredText(value, 'service'),
    resourceType: requiredText(value, 'resourceType'),
    input: value.input,
  };
}

export function idempotencyKey(
  c: Context,
  deploymentId: string,
  operation: string
): string {
  return (
    c.req.header('idempotency-key')?.trim() ||
    deterministicId('idempotency', { deploymentId, operation })
  );
}
