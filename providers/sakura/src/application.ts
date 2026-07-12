import { CoreError, deterministicId } from '@tenkacloud/simulator-core';

export const APPLICATION_RESOURCE = 'sakura.apprun.Application';
export const VERSION_RESOURCE = 'sakura.apprun.ApplicationVersion';

const RESERVED_PORTS = new Set([8008, 8012, 8013, 8022, 9090, 9091]);
const CPU_VALUES = new Set(['0.5', '1', '2']);
const MEMORY_VALUES = new Set(['1Gi', '2Gi', '4Gi']);

export interface ContainerRegistrySource {
  readonly image: string;
  readonly server?: string;
  readonly username?: string;
  readonly password?: string;
}

export interface ApplicationComponent {
  readonly name: string;
  readonly max_cpu: string;
  readonly max_memory: string;
  readonly deploy_source: {
    readonly container_registry: ContainerRegistrySource;
  };
  readonly env?: readonly { readonly key: string; readonly value: string }[];
  readonly secret?: readonly { readonly key: string; readonly value: string }[];
  readonly probe?: {
    readonly http_get: {
      readonly path: string;
      readonly port: number;
      readonly headers?: readonly {
        readonly name: string;
        readonly value: string;
      }[];
    };
  };
}

export interface ApplicationInput {
  readonly [key: string]: unknown;
  readonly name: string;
  readonly timeout_seconds: number;
  readonly port: number;
  readonly min_scale: number;
  readonly max_scale: number;
  readonly scale_target_concurrency?: number;
  readonly components: readonly ApplicationComponent[];
}

export interface StoredApplication extends ApplicationInput {
  readonly id: string;
  readonly resource_id: string;
  readonly status: 'Healthy' | 'UnHealthy' | 'Deploying';
  readonly public_url: string;
  readonly created_at: string;
  readonly versions: readonly {
    readonly id: string;
    readonly name: string;
    readonly created_at: string;
  }[];
  readonly traffics: readonly {
    readonly version_name: string;
    readonly percent: number;
  }[];
  readonly packet_filter: {
    readonly is_enabled: boolean;
    readonly settings: readonly {
      readonly from_ip: string;
      readonly from_ip_prefix_length: number;
    }[];
  };
}

function objectValue(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an object`);
  }
  return value as Readonly<Record<string, unknown>>;
}

function stringValue(value: unknown, label: string, maxLength: number): string {
  if (
    typeof value !== 'string' ||
    value.length < 1 ||
    value.length > maxLength
  ) {
    throw new CoreError(
      'ValidationFailed',
      `${label} must contain between 1 and ${maxLength} characters`
    );
  }
  return value;
}

function integerValue(
  value: unknown,
  label: string,
  minimum: number,
  maximum: number
): number {
  if (
    typeof value !== 'number' ||
    !Number.isInteger(value) ||
    value < minimum ||
    value > maximum
  ) {
    throw new CoreError(
      'ValidationFailed',
      `${label} must be an integer between ${minimum} and ${maximum}`
    );
  }
  return value;
}

function optionalString(
  object: Readonly<Record<string, unknown>>,
  key: string,
  maxLength: number
): string | undefined {
  const value = object[key];
  return value === undefined ? undefined : stringValue(value, key, maxLength);
}

function keyValues(
  value: unknown,
  label: string
): readonly { readonly key: string; readonly value: string }[] | undefined {
  if (value === undefined) return undefined;
  if (!Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an array`);
  }
  return value.map((entry, index) => {
    const object = objectValue(entry, `${label}[${index}]`);
    return {
      key: stringValue(object['key'], `${label}[${index}].key`, 255),
      value: stringValue(object['value'], `${label}[${index}].value`, 4_096),
    };
  });
}

function headerValues(
  value: unknown,
  label: string
): readonly { readonly name: string; readonly value: string }[] | undefined {
  if (value === undefined) return undefined;
  if (!Array.isArray(value)) {
    throw new CoreError('ValidationFailed', `${label} must be an array`);
  }
  return value.map((entry, index) => {
    const object = objectValue(entry, `${label}[${index}]`);
    return {
      name: stringValue(object['name'], `${label}[${index}].name`, 255),
      value: stringValue(object['value'], `${label}[${index}].value`, 4_096),
    };
  });
}

function componentValue(value: unknown, index: number): ApplicationComponent {
  const component = objectValue(value, `components[${index}]`);
  const deploySource = objectValue(
    component['deploy_source'],
    `components[${index}].deploy_source`
  );
  const registry = objectValue(
    deploySource['container_registry'],
    `components[${index}].deploy_source.container_registry`
  );
  const maxCpu = stringValue(
    component['max_cpu'],
    `components[${index}].max_cpu`,
    3
  );
  const maxMemory = stringValue(
    component['max_memory'],
    `components[${index}].max_memory`,
    3
  );
  if (!CPU_VALUES.has(maxCpu) || !MEMORY_VALUES.has(maxMemory)) {
    throw new CoreError(
      'ValidationFailed',
      'component CPU or memory is unsupported'
    );
  }
  const probeObject =
    component['probe'] === undefined
      ? undefined
      : objectValue(component['probe'], `components[${index}].probe`);
  const httpGet =
    probeObject?.['http_get'] === undefined
      ? undefined
      : objectValue(
          probeObject['http_get'],
          `components[${index}].probe.http_get`
        );
  const port =
    httpGet === undefined
      ? undefined
      : applicationPort(
          httpGet['port'],
          `components[${index}].probe.http_get.port`
        );
  const server = optionalString(registry, 'server', 128);
  const username = optionalString(registry, 'username', 63);
  const password = optionalString(registry, 'password', 63);
  const env = keyValues(component['env'], `components[${index}].env`);
  const secret = keyValues(component['secret'], `components[${index}].secret`);
  const headers =
    httpGet === undefined
      ? undefined
      : headerValues(
          httpGet['headers'],
          `components[${index}].probe.http_get.headers`
        );
  return {
    name: stringValue(component['name'], `components[${index}].name`, 255),
    max_cpu: maxCpu,
    max_memory: maxMemory,
    deploy_source: {
      container_registry: {
        image: stringValue(
          registry['image'],
          `components[${index}].deploy_source.container_registry.image`,
          128
        ),
        ...(server === undefined ? {} : { server }),
        ...(username === undefined ? {} : { username }),
        ...(password === undefined ? {} : { password: '[REDACTED]' }),
      },
    },
    ...(env === undefined ? {} : { env }),
    ...(secret === undefined
      ? {}
      : {
          secret: secret.map((entry) => ({
            ...entry,
            value: '[REDACTED]',
          })),
        }),
    ...(httpGet === undefined || port === undefined
      ? {}
      : {
          probe: {
            http_get: {
              path: stringValue(
                httpGet['path'],
                `components[${index}].probe.http_get.path`,
                2_048
              ),
              port,
              ...(headers === undefined ? {} : { headers }),
            },
          },
        }),
  };
}

function applicationPort(value: unknown, label = 'port'): number {
  const port = integerValue(value, label, 1, 65_535);
  if (RESERVED_PORTS.has(port)) {
    throw new CoreError('ValidationFailed', `${label} is reserved`);
  }
  return port;
}

export function parseApplicationInput(value: unknown): ApplicationInput {
  const object = objectValue(value, 'application');
  if (
    !Array.isArray(object['components']) ||
    object['components'].length === 0
  ) {
    throw new CoreError(
      'ValidationFailed',
      'components must be a non-empty array'
    );
  }
  const minScale = integerValue(object['min_scale'], 'min_scale', 0, 10);
  const maxScale = integerValue(object['max_scale'], 'max_scale', 1, 10);
  if (minScale > maxScale) {
    throw new CoreError(
      'ValidationFailed',
      'min_scale must not exceed max_scale'
    );
  }
  const concurrency =
    object['scale_target_concurrency'] === undefined
      ? undefined
      : integerValue(
          object['scale_target_concurrency'],
          'scale_target_concurrency',
          50,
          200
        );
  return {
    name: stringValue(object['name'], 'name', 255),
    timeout_seconds: integerValue(
      object['timeout_seconds'],
      'timeout_seconds',
      1,
      300
    ),
    port: applicationPort(object['port']),
    min_scale: minScale,
    max_scale: maxScale,
    ...(concurrency === undefined
      ? {}
      : { scale_target_concurrency: concurrency }),
    components: object['components'].map(componentValue),
  };
}

export function createStoredApplication(
  input: ApplicationInput,
  identity: Readonly<Record<string, unknown>>,
  virtualTime: string
): StoredApplication {
  const id = deterministicId('app', identity);
  const versionName = `${input.name}-${deterministicId('version', {
    identity,
    virtualTime,
  })}`;
  return {
    ...input,
    id,
    resource_id: deterministicId('resource', identity),
    status: 'Healthy',
    public_url: `https://${id}.apprun.sakura.local`,
    created_at: virtualTime,
    versions: [
      {
        id: deterministicId('version', { identity, index: 1 }),
        name: versionName,
        created_at: virtualTime,
      },
    ],
    traffics: [{ version_name: versionName, percent: 100 }],
    packet_filter: { is_enabled: false, settings: [] },
  };
}

export function storedApplication(value: unknown): StoredApplication {
  const object = objectValue(value, 'stored application');
  const parsed = parseApplicationInput(object);
  if (
    typeof object['id'] !== 'string' ||
    typeof object['resource_id'] !== 'string' ||
    typeof object['status'] !== 'string' ||
    typeof object['public_url'] !== 'string' ||
    typeof object['created_at'] !== 'string' ||
    !Array.isArray(object['versions']) ||
    !Array.isArray(object['traffics']) ||
    object['packet_filter'] === null ||
    typeof object['packet_filter'] !== 'object'
  ) {
    throw new CoreError('ValidationFailed', 'stored application is invalid');
  }
  return {
    ...parsed,
    id: object['id'],
    resource_id: object['resource_id'],
    status:
      object['status'] === 'UnHealthy'
        ? 'UnHealthy'
        : object['status'] === 'Deploying'
          ? 'Deploying'
          : 'Healthy',
    public_url: object['public_url'],
    created_at: object['created_at'],
    versions: object['versions'] as StoredApplication['versions'],
    traffics: object['traffics'] as StoredApplication['traffics'],
    packet_filter: object[
      'packet_filter'
    ] as StoredApplication['packet_filter'],
  };
}
