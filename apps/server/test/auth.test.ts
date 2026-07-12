import { describe, expect, it } from 'bun:test';
import { createHmac } from 'node:crypto';
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
