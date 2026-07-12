import { describe, expect, it } from 'bun:test';
import { canonicalJson, contentHash, deterministicId } from './canonical';

describe('canonical helper の振る舞い', () => {
  it('object key を再帰的に整列し、array の順序と primitive を保持する', () => {
    const value = {
      z: [{ second: 2, first: 1 }, null, true],
      a: { d: 'four', b: 'two' },
    };

    expect(canonicalJson(value)).toBe(
      '{"a":{"b":"two","d":"four"},"z":[{"first":1,"second":2},null,true]}'
    );
    expect(canonicalJson(['z', 'a', 1])).toBe('["z","a",1]');
  });

  it('同じ内容の object は入力順に依存しない SHA-256 hash を返す', () => {
    const left = contentHash({ b: 2, a: { d: 4, c: 3 } });
    const right = contentHash({ a: { c: 3, d: 4 }, b: 2 });

    expect(left).toBe(right);
    expect(left).toMatch(/^[a-f0-9]{64}$/);
    expect(contentHash({ a: 2 })).not.toBe(left);
  });

  it('deterministic ID は prefix と hash の先頭24文字から構成する', () => {
    const value = { world: 'alpha', sequence: 1 };

    expect(deterministicId('world', value)).toBe(
      `world_${contentHash(value).slice(0, 24)}`
    );
    expect(deterministicId('command', value)).toHaveLength(32);
  });
});
