import { describe, expect, it } from 'bun:test';
import {
  hasSha256DigestPin,
  isLowercaseDigestPinnedImage,
} from '../src/image-reference';

const DIGEST = 'a'.repeat(64);

describe('OCI image digest validator の振る舞い', () => {
  it('固定長の lowercase SHA-256 digest pin を受理する', () => {
    expect(hasSha256DigestPin(`GHCR.IO/team/image@sha256:${DIGEST}`)).toBe(
      true
    );
    expect(
      isLowercaseDigestPinnedImage(`ghcr.io/team/image@sha256:${DIGEST}`)
    ).toBe(true);
  });

  it('digest 長、大文字 hexadecimal、空の image 名を拒否する', () => {
    expect(hasSha256DigestPin(`image@sha256:${'a'.repeat(63)}`)).toBe(false);
    expect(hasSha256DigestPin(`image@sha256:${'a'.repeat(65)}`)).toBe(false);
    expect(hasSha256DigestPin(`image@sha256:${'A'.repeat(64)}`)).toBe(false);
    expect(hasSha256DigestPin(`@sha256:${DIGEST}`)).toBe(false);
  });

  it('lowercase repository 名の不正文字、空の最終 segment、上限超過を拒否する', () => {
    expect(
      isLowercaseDigestPinnedImage(`GHCR.IO/team/image@sha256:${DIGEST}`)
    ).toBe(false);
    expect(
      isLowercaseDigestPinnedImage(`ghcr.io/team image@sha256:${DIGEST}`)
    ).toBe(false);
    expect(isLowercaseDigestPinnedImage(`ghcr.io/team/@sha256:${DIGEST}`)).toBe(
      false
    );
    expect(
      isLowercaseDigestPinnedImage(`ghcr.io//image@sha256:${DIGEST}`)
    ).toBe(false);
    expect(
      isLowercaseDigestPinnedImage(`ghcr.io/.team/image@sha256:${DIGEST}`)
    ).toBe(false);
    expect(
      isLowercaseDigestPinnedImage(`${'a'.repeat(441)}@sha256:${DIGEST}`)
    ).toBe(false);
  });

  it('slash を大量に含む不正入力を fail closed で処理する', () => {
    const adversarial = `0${'/0'.repeat(100_000)}@sha256:${DIGEST}`;
    expect(isLowercaseDigestPinnedImage(adversarial)).toBe(false);
  });
});
