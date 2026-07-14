import { createHmac, randomUUID, timingSafeEqual } from 'node:crypto';
import {
  assertSimulatorSnapshotEnvelope,
  canonicalSimulatorSnapshotIntegrityPayload,
  isSimulatorSnapshot,
  SIMULATOR_SNAPSHOT_INTEGRITY_ALGORITHM,
  SIMULATOR_SNAPSHOT_INTEGRITY_VERSION,
  type SimulatorSnapshotEnvelope,
  type SimulatorSnapshotIntegrityProof,
} from '@tenkacloud/simulator-contracts';

const TOKEN_PREFIX = 'tc_sim_v1';
const MAX_TOKEN_LENGTH = 4096;
const MAX_TTL_SECONDS = 86_400;

export interface LaunchTokenNamespace {
  readonly tenantId: string;
  readonly eventId: string;
  readonly teamId: string;
  readonly deploymentId: string;
}

export interface LaunchTokenClaims extends LaunchTokenNamespace {
  readonly expiresAt: number;
  readonly issuedAt: number;
  readonly nonce: string;
}

export class LaunchTokenError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'LaunchTokenError';
  }
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function textClaim(
  record: Readonly<Record<string, unknown>>,
  key: string
): string {
  const value = record[key];
  if (typeof value !== 'string' || !value.trim() || value.length > 256) {
    throw new LaunchTokenError(`launch token ${key} is invalid`);
  }
  return value;
}

function integerClaim(
  record: Readonly<Record<string, unknown>>,
  key: string
): number {
  const value = record[key];
  if (typeof value !== 'number' || !Number.isSafeInteger(value)) {
    throw new LaunchTokenError(`launch token ${key} is invalid`);
  }
  return value;
}

function claims(value: unknown): LaunchTokenClaims {
  if (!isRecord(value) || Object.keys(value).length !== 7) {
    throw new LaunchTokenError('launch token claims are invalid');
  }
  return {
    tenantId: textClaim(value, 'tenantId'),
    eventId: textClaim(value, 'eventId'),
    teamId: textClaim(value, 'teamId'),
    deploymentId: textClaim(value, 'deploymentId'),
    issuedAt: integerClaim(value, 'issuedAt'),
    expiresAt: integerClaim(value, 'expiresAt'),
    nonce: textClaim(value, 'nonce'),
  };
}

function base64Url(value: string | Uint8Array): string {
  return Buffer.from(value).toString('base64url');
}

function decodedJson(value: string): unknown {
  try {
    return JSON.parse(Buffer.from(value, 'base64url').toString('utf8'));
  } catch {
    throw new LaunchTokenError('launch token payload is invalid');
  }
}

export function bearerLaunchToken(header: string | undefined): string {
  if (!header?.startsWith('Bearer ')) {
    throw new LaunchTokenError('simulator bearer token is required');
  }
  const token = header.slice('Bearer '.length);
  if (
    !token.startsWith(`${TOKEN_PREFIX}.`) ||
    token.length > MAX_TOKEN_LENGTH
  ) {
    throw new LaunchTokenError('authorization is not a simulator launch token');
  }
  return token;
}

export class LaunchTokenAuthority {
  readonly #secret: Uint8Array;

  constructor(secret: string | Uint8Array) {
    this.#secret =
      typeof secret === 'string' ? new TextEncoder().encode(secret) : secret;
    if (this.#secret.byteLength < 32) {
      throw new LaunchTokenError(
        'launch token secret must contain at least 32 bytes'
      );
    }
  }

  #signature(payload: string): string {
    return base64Url(
      createHmac('sha256', this.#secret)
        .update(`${TOKEN_PREFIX}.${payload}`)
        .digest()
    );
  }

  #snapshotSignature(envelope: SimulatorSnapshotEnvelope): string {
    return base64Url(
      createHmac('sha256', this.#secret)
        .update(canonicalSimulatorSnapshotIntegrityPayload(envelope))
        .digest()
    );
  }

  signSnapshot(
    envelope: SimulatorSnapshotEnvelope
  ): SimulatorSnapshotIntegrityProof {
    assertSimulatorSnapshotEnvelope(envelope);
    return {
      version: SIMULATOR_SNAPSHOT_INTEGRITY_VERSION,
      algorithm: SIMULATOR_SNAPSHOT_INTEGRITY_ALGORITHM,
      value: this.#snapshotSignature(envelope),
    };
  }

  verifySnapshot(value: unknown): boolean {
    if (!isSimulatorSnapshot(value)) return false;
    const { integrityProof, ...envelope } = value;
    const expected = Buffer.from(this.#snapshotSignature(envelope), 'ascii');
    const provided = Buffer.from(integrityProof.value, 'ascii');
    return timingSafeEqual(expected, provided);
  }

  issue(
    namespace: LaunchTokenNamespace,
    ttlSeconds = 3600,
    now = Date.now()
  ): string {
    const validated = claims({
      ...namespace,
      issuedAt: now,
      expiresAt: now + ttlSeconds * 1000,
      nonce: randomUUID(),
    });
    if (
      !Number.isSafeInteger(ttlSeconds) ||
      ttlSeconds < 1 ||
      ttlSeconds > MAX_TTL_SECONDS
    ) {
      throw new LaunchTokenError(
        `launch token TTL must be between 1 and ${MAX_TTL_SECONDS} seconds`
      );
    }
    const payload = base64Url(JSON.stringify(validated));
    return `${TOKEN_PREFIX}.${payload}.${this.#signature(payload)}`;
  }

  verify(token: string, now = Date.now()): LaunchTokenClaims {
    if (token.length > MAX_TOKEN_LENGTH) {
      throw new LaunchTokenError('launch token is too large');
    }
    const parts = token.split('.');
    if (
      parts.length !== 3 ||
      parts[0] !== TOKEN_PREFIX ||
      !parts[1] ||
      !parts[2]
    ) {
      throw new LaunchTokenError('launch token format is invalid');
    }
    const expected = Buffer.from(this.#signature(parts[1]), 'base64url');
    const provided = Buffer.from(parts[2], 'base64url');
    if (
      expected.byteLength !== provided.byteLength ||
      !timingSafeEqual(expected, provided)
    ) {
      throw new LaunchTokenError('launch token signature is invalid');
    }
    const verified = claims(decodedJson(parts[1]));
    if (verified.issuedAt > now || verified.expiresAt <= now) {
      throw new LaunchTokenError('launch token is expired or not active');
    }
    return verified;
  }
}
