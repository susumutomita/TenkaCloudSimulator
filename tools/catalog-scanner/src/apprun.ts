import {
  isLowercaseDigestPinnedImage,
  sha256DigestPinnedImageName,
} from '@tenkacloud/simulator-contracts/image-reference';
import { errorMessage } from './errors.ts';
import type { Diagnostic, NormalizedTarget, Requirement } from './model.ts';
import { createRequirement } from './requirements.ts';

export interface AppRunParseResult {
  requirements: Requirement[];
  diagnostics: Diagnostic[];
}

const CPU_VALUES = new Set(['0.5', '1', '2']);
const MEMORY_VALUES = new Set(['1Gi', '2Gi', '4Gi']);
const RESERVED_PORTS = new Set([8008, 8012, 8013, 8022, 9090, 9091]);
const SUPPORTED_IMAGE_PREFIXES = [
  'ghcr.io/',
  'docker.io/',
  'index.docker.io/',
  'registry.sakura.ad.jp/',
] as const;

function supportedImagePrefix(value: string): string | undefined {
  return SUPPORTED_IMAGE_PREFIXES.find((prefix) => value.startsWith(prefix));
}

export function isPinnedAppRunImage(value: string): boolean {
  const imageName = sha256DigestPinnedImageName(value);
  return (
    imageName !== undefined &&
    supportedImagePrefix(imageName) !== undefined &&
    isLowercaseDigestPinnedImage(value)
  );
}

export function isAppRunImageReference(value: string): boolean {
  return supportedImagePrefix(value) !== undefined;
}

function objectValue(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} must be an object`);
  }
  return value as Readonly<Record<string, unknown>>;
}

function stringValue(value: unknown, label: string, maximum: number): string {
  if (typeof value !== 'string' || value.length < 1 || value.length > maximum) {
    throw new Error(
      `${label} must contain between 1 and ${maximum} characters`
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
    throw new Error(
      `${label} must be an integer between ${minimum} and ${maximum}`
    );
  }
  return value;
}

function optionalString(
  value: Readonly<Record<string, unknown>>,
  key: string,
  maximum: number
): void {
  if (value[key] !== undefined) stringValue(value[key], key, maximum);
}

function keyValueEntries(value: unknown, label: string): void {
  if (value === undefined) return;
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
  for (const [index, entry] of value.entries()) {
    const item = objectValue(entry, `${label}[${index}]`);
    stringValue(item['key'], `${label}[${index}].key`, 255);
    stringValue(item['value'], `${label}[${index}].value`, 4_096);
  }
}

function headerEntries(value: unknown, label: string): void {
  if (value === undefined) return;
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
  for (const [index, entry] of value.entries()) {
    const item = objectValue(entry, `${label}[${index}]`);
    stringValue(item['name'], `${label}[${index}].name`, 255);
    stringValue(item['value'], `${label}[${index}].value`, 4_096);
  }
}

function applicationPort(value: unknown, label: string): number {
  const port = integerValue(value, label, 1, 65_535);
  if (RESERVED_PORTS.has(port)) throw new Error(`${label} is reserved`);
  return port;
}

function component(value: unknown, index: number): void {
  const item = objectValue(value, `components[${index}]`);
  stringValue(item['name'], `components[${index}].name`, 255);
  const cpu = stringValue(item['max_cpu'], `components[${index}].max_cpu`, 3);
  const memory = stringValue(
    item['max_memory'],
    `components[${index}].max_memory`,
    3
  );
  if (!CPU_VALUES.has(cpu) || !MEMORY_VALUES.has(memory)) {
    throw new Error('component CPU or memory is unsupported');
  }
  const deploySource = objectValue(
    item['deploy_source'],
    `components[${index}].deploy_source`
  );
  const registry = objectValue(
    deploySource['container_registry'],
    `components[${index}].deploy_source.container_registry`
  );
  stringValue(
    registry['image'],
    `components[${index}].deploy_source.container_registry.image`,
    512
  );
  optionalString(registry, 'server', 128);
  optionalString(registry, 'username', 63);
  optionalString(registry, 'password', 63);
  keyValueEntries(item['env'], `components[${index}].env`);
  keyValueEntries(item['secret'], `components[${index}].secret`);
  if (item['probe'] === undefined) return;
  const probe = objectValue(item['probe'], `components[${index}].probe`);
  const httpGet = objectValue(
    probe['http_get'],
    `components[${index}].probe.http_get`
  );
  stringValue(
    httpGet['path'],
    `components[${index}].probe.http_get.path`,
    2_048
  );
  applicationPort(httpGet['port'], `components[${index}].probe.http_get.port`);
  headerEntries(
    httpGet['headers'],
    `components[${index}].probe.http_get.headers`
  );
}

function validateApplication(value: unknown): void {
  const application = objectValue(value, 'application');
  const components = application['components'];
  if (!Array.isArray(components) || components.length === 0) {
    throw new Error('components must be a non-empty array');
  }
  stringValue(application['name'], 'name', 255);
  integerValue(application['timeout_seconds'], 'timeout_seconds', 1, 300);
  applicationPort(application['port'], 'port');
  const minimum = integerValue(application['min_scale'], 'min_scale', 0, 10);
  const maximum = integerValue(application['max_scale'], 'max_scale', 1, 10);
  if (minimum > maximum) throw new Error('min_scale must not exceed max_scale');
  if (application['scale_target_concurrency'] !== undefined) {
    integerValue(
      application['scale_target_concurrency'],
      'scale_target_concurrency',
      50,
      200
    );
  }
  components.forEach(component);
}

function deployRequirement(
  sourcePath: string,
  target: NormalizedTarget,
  problemId: string
): Requirement {
  return createRequirement({
    problemId,
    targetId: target.targetId,
    provider: target.provider,
    engine: target.engine,
    service: 'apprun',
    resourceType: 'sakura.apprun.Application',
    operation: 'deploy',
    fidelity: ['L0', 'L1', 'L2'],
    plane: 'deploy',
    origin: 'iac-resource',
    classification: 'binding',
    source: { path: sourcePath, line: 1, jsonPointer: null },
  });
}

export function parseAppRunImage(
  image: string,
  sourcePath: string,
  target: NormalizedTarget,
  problemId: string
): AppRunParseResult {
  if (!isPinnedAppRunImage(image)) {
    return {
      requirements: [],
      diagnostics: [
        {
          code: 'INVALID_APPRUN',
          message:
            'AppRun runtime entry must be a digest-pinned image from a supported registry',
          problemId,
          targetId: target.targetId,
          source: { path: sourcePath, line: 1, jsonPointer: null },
        },
      ],
    };
  }
  return {
    requirements: [deployRequirement(sourcePath, target, problemId)],
    diagnostics: [],
  };
}

export function parseAppRun(
  contents: string,
  sourcePath: string,
  target: NormalizedTarget,
  problemId: string
): AppRunParseResult {
  try {
    let parsed: unknown;
    try {
      parsed = JSON.parse(contents) as unknown;
    } catch {
      throw new Error('AppRun entry must contain valid JSON');
    }
    validateApplication(parsed);
    return {
      requirements: [deployRequirement(sourcePath, target, problemId)],
      diagnostics: [],
    };
  } catch (error) {
    return {
      requirements: [],
      diagnostics: [
        {
          code: 'INVALID_APPRUN',
          message: `AppRun entry is not supported: ${errorMessage(error)}`,
          problemId,
          targetId: target.targetId,
          source: { path: sourcePath, line: 1, jsonPointer: null },
        },
      ],
    };
  }
}
