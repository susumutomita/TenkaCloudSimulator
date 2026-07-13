import { describe, expect, it } from 'bun:test';
import { createHmac } from 'node:crypto';
import {
  SIMULATOR_PROTOCOL_VERSION,
  SIMULATOR_SNAPSHOT_VERSION,
  type SimulatorSnapshotEnvelope,
} from '@tenkacloud/simulator-contracts';
import {
  bearerLaunchToken,
  LaunchTokenAuthority,
  LaunchTokenError,
} from '../src/auth';

const SECRET = '0123456789abcdef0123456789abcdef';
const NOW = 1_800_000_000_000;
const NAMESPACE = {
  tenantId: 'tenant-auth',
  eventId: 'event-auth',
  teamId: 'team-auth',
  deploymentId: 'deployment-auth',
};

const SNAPSHOT_PROJECTED_WORLD = {
  worldId: 'world-auth',
  tenantId: NAMESPACE.tenantId,
  eventId: NAMESPACE.eventId,
  teamId: NAMESPACE.teamId,
  deploymentId: NAMESPACE.deploymentId,
};

const SNAPSHOT_ENVELOPE: SimulatorSnapshotEnvelope = {
  snapshotVersion: SIMULATOR_SNAPSHOT_VERSION,
  protocolVersion: SIMULATOR_PROTOCOL_VERSION,
  worldId: 'world-auth',
  namespace: {
    tenantId: NAMESPACE.tenantId,
    eventId: NAMESPACE.eventId,
    teamId: NAMESPACE.teamId,
  },
  seed: 'seed-auth',
  clock: '2027-01-15T08:00:00.000Z',
  lastSequence: 1,
  resourceGraph: {
    world: SNAPSHOT_PROJECTED_WORLD,
    events: [],
    deployments: [],
    resources: [],
  },
  providerProjections: {},
  hash: 'a'.repeat(64),
};

function signedToken(payloadValue: unknown): string {
  const payload = Buffer.from(JSON.stringify(payloadValue)).toString(
    'base64url'
  );
  const signature = createHmac('sha256', SECRET)
    .update(`tc_sim_v1.${payload}`)
    .digest('base64url');
  return `tc_sim_v1.${payload}.${signature}`;
}

describe('Simulator launch token', () => {
  it('namespace と期限を HMAC 署名し Bearer header から検証する', () => {
    const authority = new LaunchTokenAuthority(SECRET);
    const token = authority.issue(NAMESPACE, 60, NOW);
    expect(bearerLaunchToken(`Bearer ${token}`)).toBe(token);
    expect(authority.verify(token, NOW + 30_000)).toMatchObject({
      ...NAMESPACE,
      issuedAt: NOW,
      expiresAt: NOW + 60_000,
    });
    const byteAuthority = new LaunchTokenAuthority(
      new TextEncoder().encode(SECRET)
    );
    expect(byteAuthority.verify(token, NOW + 1)).toMatchObject(NAMESPACE);
  });

  it('実 credential、改ざん、期限外、過剰 TTL を loud に拒否する', () => {
    const authority = new LaunchTokenAuthority(SECRET);
    expect(() => new LaunchTokenAuthority('short')).toThrow(
      'at least 32 bytes'
    );
    expect(() => bearerLaunchToken(undefined)).toThrow('required');
    expect(() => bearerLaunchToken('Basic dXNlcjpwYXNz')).toThrow('required');
    const nonSimulatorToken = [
      'eyJhbGciOiJSUzI1NiJ9',
      'test-payload',
      'test-signature',
    ].join('.');
    expect(() => bearerLaunchToken(`Bearer ${nonSimulatorToken}`)).toThrow(
      'not a simulator'
    );
    expect(() =>
      bearerLaunchToken(`Bearer tc_sim_v1.${'x'.repeat(5000)}`)
    ).toThrow('not a simulator');
    expect(() => authority.issue(NAMESPACE, 0, NOW)).toThrow('between 1 and');
    expect(() => authority.issue(NAMESPACE, 86_401, NOW)).toThrow(
      'between 1 and'
    );
    expect(() => authority.issue(NAMESPACE, 1.5, NOW)).toThrow('between 1 and');

    const token = authority.issue(NAMESPACE, 60, NOW);
    expect(() => authority.verify(`${token}x`, NOW)).toThrow('signature');
    expect(() => authority.verify(token, NOW - 1)).toThrow('not active');
    expect(() => authority.verify(token, NOW + 60_000)).toThrow('expired');
    expect(() => authority.verify('tc_sim_v1.payload', NOW)).toThrow('format');
    expect(() =>
      authority.verify(`tc_sim_v1.${'x'.repeat(5000)}`, NOW)
    ).toThrow('too large');
    expect(() => authority.verify('tc_sim_v1.e30.short', NOW)).toThrow(
      'signature'
    );
  });

  it('署名済みでも壊れた claim と payload を拒否する', () => {
    const authority = new LaunchTokenAuthority(SECRET);
    const validClaims = {
      ...NAMESPACE,
      issuedAt: NOW,
      expiresAt: NOW + 60_000,
      nonce: 'nonce',
    };
    expect(() => authority.verify(signedToken(null), NOW)).toThrow('claims');
    expect(() =>
      authority.verify(signedToken({ ...validClaims, extra: true }), NOW)
    ).toThrow('claims');
    expect(() =>
      authority.verify(signedToken({ ...validClaims, tenantId: '' }), NOW)
    ).toThrow('tenantId');
    expect(() =>
      authority.verify(signedToken({ ...validClaims, issuedAt: 'now' }), NOW)
    ).toThrow('issuedAt');

    const payload = Buffer.from('{invalid').toString('base64url');
    const signature = createHmac('sha256', SECRET)
      .update(`tc_sim_v1.${payload}`)
      .digest('base64url');
    expect(() =>
      authority.verify(`tc_sim_v1.${payload}.${signature}`, NOW)
    ).toThrow('payload');
    expect(() =>
      authority.issue({ ...NAMESPACE, tenantId: '' }, 60, NOW)
    ).toThrow(LaunchTokenError);
  });
});

describe('Simulator snapshot integrity proof の振る舞い', () => {
  it('proof を別 domain の canonical envelope に HMAC 署名して検証する', () => {
    const authority = new LaunchTokenAuthority(SECRET);
    const integrityProof = authority.signSnapshot(SNAPSHOT_ENVELOPE);
    expect(integrityProof).toEqual({
      version: '1',
      algorithm: 'HMAC-SHA256',
      value: expect.stringMatching(/^[A-Za-z0-9_-]{43}$/),
    });
    expect(
      authority.verifySnapshot({ ...SNAPSHOT_ENVELOPE, integrityProof })
    ).toBe(true);
    const alphabet =
      'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
    const lastCharacter = integrityProof.value.at(-1);
    const lastIndex = lastCharacter ? alphabet.indexOf(lastCharacter) : -1;
    if (lastIndex < 0) throw new Error('proof suffix is invalid');
    const nonCanonicalValue = `${integrityProof.value.slice(0, -1)}${alphabet[lastIndex ^ 1]}`;
    expect(
      Buffer.from(nonCanonicalValue, 'base64url').equals(
        Buffer.from(integrityProof.value, 'base64url')
      )
    ).toBe(true);
    expect(
      authority.verifySnapshot({
        ...SNAPSHOT_ENVELOPE,
        integrityProof: { ...integrityProof, value: nonCanonicalValue },
      })
    ).toBe(false);

    const reordered: SimulatorSnapshotEnvelope = {
      hash: SNAPSHOT_ENVELOPE.hash,
      providerProjections: SNAPSHOT_ENVELOPE.providerProjections,
      resourceGraph: SNAPSHOT_ENVELOPE.resourceGraph,
      lastSequence: SNAPSHOT_ENVELOPE.lastSequence,
      clock: SNAPSHOT_ENVELOPE.clock,
      seed: SNAPSHOT_ENVELOPE.seed,
      namespace: SNAPSHOT_ENVELOPE.namespace,
      worldId: SNAPSHOT_ENVELOPE.worldId,
      protocolVersion: SNAPSHOT_ENVELOPE.protocolVersion,
      snapshotVersion: SNAPSHOT_ENVELOPE.snapshotVersion,
    };
    expect(authority.signSnapshot(reordered)).toEqual(integrityProof);
    const unicodeProjectionLeft = {
      é: 'precomposed',
      é: 'decomposed',
    };
    const unicodeProjectionRight = {
      é: 'decomposed',
      é: 'precomposed',
    };
    const unicodeLeft: SimulatorSnapshotEnvelope = {
      ...SNAPSHOT_ENVELOPE,
      providerProjections: { unicode: unicodeProjectionLeft },
    };
    const unicodeRight: SimulatorSnapshotEnvelope = {
      ...SNAPSHOT_ENVELOPE,
      providerProjections: { unicode: unicodeProjectionRight },
    };
    const unicodeProof = authority.signSnapshot(unicodeLeft);
    expect(authority.signSnapshot(unicodeRight)).toEqual(unicodeProof);
    expect(
      authority.verifySnapshot({
        ...unicodeRight,
        integrityProof: unicodeProof,
      })
    ).toBe(true);

    const tokenSignature = authority
      .issue(NAMESPACE, 60, NOW)
      .split('.')
      .at(-1);
    expect(
      authority.verifySnapshot({
        ...SNAPSHOT_ENVELOPE,
        integrityProof: { ...integrityProof, value: tokenSignature },
      })
    ).toBe(false);
  });

  it('別 source、deployment、namespace、authority の proof を拒否する', () => {
    const authority = new LaunchTokenAuthority(SECRET);
    const integrityProof = authority.signSnapshot(SNAPSHOT_ENVELOPE);
    const signed = { ...SNAPSHOT_ENVELOPE, integrityProof };
    expect(
      authority.verifySnapshot({ ...signed, worldId: 'world-other' })
    ).toBe(false);
    expect(
      authority.verifySnapshot({
        ...signed,
        namespace: { ...signed.namespace, teamId: 'team-other' },
      })
    ).toBe(false);
    expect(
      authority.verifySnapshot({
        ...signed,
        resourceGraph: {
          ...signed.resourceGraph,
          world: {
            ...SNAPSHOT_PROJECTED_WORLD,
            deploymentId: 'deployment-other',
          },
        },
      })
    ).toBe(false);
    expect(
      new LaunchTokenAuthority(
        'other-secret-0123456789abcdef01234'
      ).verifySnapshot(signed)
    ).toBe(false);
  });

  it('unsigned、malformed、追加 field を strict に拒否して secret を露出しない', () => {
    const authority = new LaunchTokenAuthority(SECRET);
    const integrityProof = authority.signSnapshot(SNAPSHOT_ENVELOPE);
    expect(authority.verifySnapshot(SNAPSHOT_ENVELOPE)).toBe(false);
    expect(
      authority.verifySnapshot({
        ...SNAPSHOT_ENVELOPE,
        integrityProof: {
          ...integrityProof,
          value: `${integrityProof.value}=`,
        },
      })
    ).toBe(false);
    expect(
      authority.verifySnapshot({
        ...SNAPSHOT_ENVELOPE,
        integrityProof: { ...integrityProof, source: 'caller' },
      })
    ).toBe(false);
    expect(JSON.stringify(integrityProof)).not.toContain(SECRET);
  });
});
