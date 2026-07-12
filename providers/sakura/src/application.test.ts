import { describe, expect, it } from 'bun:test';
import { CoreError } from '@tenkacloud/simulator-core';
import {
  createStoredApplication,
  parseApplicationInput,
  storedApplication,
} from './application';

const VALID_APPLICATION = {
  name: 'validation-app',
  timeout_seconds: 60,
  port: 8080,
  min_scale: 0,
  max_scale: 2,
  components: [
    {
      name: 'web',
      max_cpu: '0.5',
      max_memory: '1Gi',
      deploy_source: {
        container_registry: { image: 'registry.example/app:1' },
      },
    },
  ],
};

function componentWith(
  changes: Readonly<Record<string, unknown>>
): Readonly<Record<string, unknown>> {
  const component = VALID_APPLICATION.components[0];
  if (!component) throw new Error('validation component がありません');
  return { ...component, ...changes };
}

function validationError(operation: () => unknown): CoreError {
  try {
    operation();
  } catch (error) {
    if (error instanceof CoreError) return error;
    throw error;
  }
  throw new Error('ValidationFailed が発生しませんでした');
}

describe('Sakura Application parser の振る舞い', () => {
  it('application と nested entry が object でない場合を拒否する', () => {
    const rootError = validationError(() => parseApplicationInput(null));
    const entryError = validationError(() =>
      parseApplicationInput({
        ...VALID_APPLICATION,
        components: [null],
      })
    );

    expect(rootError.code).toBe('ValidationFailed');
    expect(rootError.message).toBe('application must be an object');
    expect(entryError.message).toBe('components[0] must be an object');
  });

  it('必須 string の空文字、型違い、最大長超過を拒否する', () => {
    for (const name of ['', 42, 'a'.repeat(256)]) {
      const error = validationError(() =>
        parseApplicationInput({ ...VALID_APPLICATION, name })
      );
      expect(error.code).toBe('ValidationFailed');
      expect(error.message).toContain('name must contain between 1 and 255');
    }
  });

  it('integer field の型違い、小数、範囲外を拒否する', () => {
    for (const timeout of ['60', 1.5, 0, 301]) {
      const error = validationError(() =>
        parseApplicationInput({
          ...VALID_APPLICATION,
          timeout_seconds: timeout,
        })
      );
      expect(error.code).toBe('ValidationFailed');
      expect(error.message).toContain(
        'timeout_seconds must be an integer between 1 and 300'
      );
    }
  });

  it('components は一件以上の array を必須にする', () => {
    for (const components of [[], { name: 'web' }]) {
      const error = validationError(() =>
        parseApplicationInput({ ...VALID_APPLICATION, components })
      );
      expect(error.code).toBe('ValidationFailed');
      expect(error.message).toBe('components must be a non-empty array');
    }
  });

  it('env と secret が array でなければ拒否する', () => {
    for (const field of ['env', 'secret'] as const) {
      const error = validationError(() =>
        parseApplicationInput({
          ...VALID_APPLICATION,
          components: [componentWith({ [field]: { key: 'MODE' } })],
        })
      );
      expect(error.code).toBe('ValidationFailed');
      expect(error.message).toBe(`components[0].${field} must be an array`);
    }
    const headerError = validationError(() =>
      parseApplicationInput({
        ...VALID_APPLICATION,
        components: [
          componentWith({
            probe: {
              http_get: { path: '/healthz', port: 8080, headers: {} },
            },
          }),
        ],
      })
    );
    expect(headerError.message).toBe(
      'components[0].probe.http_get.headers must be an array'
    );
  });

  it('未対応の CPU または memory 値を拒否する', () => {
    const cpuError = validationError(() =>
      parseApplicationInput({
        ...VALID_APPLICATION,
        components: [componentWith({ max_cpu: '3' })],
      })
    );
    const memoryError = validationError(() =>
      parseApplicationInput({
        ...VALID_APPLICATION,
        components: [componentWith({ max_memory: '8Gi' })],
      })
    );

    expect(cpuError.message).toBe('component CPU or memory is unsupported');
    expect(memoryError.message).toBe('component CPU or memory is unsupported');
  });

  it('optional field がない最小 application を値の追加なしで返す', () => {
    const parsed = parseApplicationInput(VALID_APPLICATION);
    const component = parsed.components[0];

    expect(parsed).toEqual(VALID_APPLICATION);
    expect('scale_target_concurrency' in parsed).toBe(false);
    expect(component && 'env' in component).toBe(false);
    expect(component && 'secret' in component).toBe(false);
    expect(component && 'probe' in component).toBe(false);
  });

  it('stored application の必須 projection field が欠けた場合を拒否する', () => {
    const parsed = parseApplicationInput(VALID_APPLICATION);
    const stored = createStoredApplication(
      parsed,
      { worldId: 'world-a', name: parsed.name },
      '2026-07-12T00:00:00.000Z'
    );
    for (const invalid of [
      { ...stored, id: 1 },
      { ...stored, versions: {} },
      { ...stored, packet_filter: null },
    ]) {
      const error = validationError(() => storedApplication(invalid));
      expect(error.code).toBe('ValidationFailed');
      expect(error.message).toBe('stored application is invalid');
    }
  });

  it('保存 status は UnHealthy と Deploying を維持し、未知値を Healthy に正規化する', () => {
    const parsed = parseApplicationInput(VALID_APPLICATION);
    const stored = createStoredApplication(
      parsed,
      { worldId: 'world-a', name: parsed.name },
      '2026-07-12T00:00:00.000Z'
    );

    expect(storedApplication({ ...stored, status: 'UnHealthy' }).status).toBe(
      'UnHealthy'
    );
    expect(storedApplication({ ...stored, status: 'Deploying' }).status).toBe(
      'Deploying'
    );
    expect(storedApplication({ ...stored, status: 'Unknown' }).status).toBe(
      'Healthy'
    );
  });

  it('同じ identity と clock から同じ ID、version、URL を生成する', () => {
    const parsed = parseApplicationInput(VALID_APPLICATION);
    const identity = { worldId: 'world-a', name: parsed.name };
    const first = createStoredApplication(
      parsed,
      identity,
      '2026-07-12T00:00:00.000Z'
    );
    const repeated = createStoredApplication(
      parsed,
      identity,
      '2026-07-12T00:00:00.000Z'
    );

    expect(repeated).toEqual(first);
    expect(first.public_url).toBe(`https://${first.id}.apprun.sakura.local`);
    const version = first.versions[0];
    if (!version) throw new Error('初期 application version がありません');
    expect(first.traffics).toEqual([
      { version_name: version.name, percent: 100 },
    ]);
  });
});
