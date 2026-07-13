import { errorMessage } from './errors.ts';
import type {
  Diagnostic,
  Fidelity,
  NormalizedTarget,
  Requirement,
} from './model.ts';
import { createRequirement } from './requirements.ts';

export interface BicepParseResult {
  requirements: Requirement[];
  diagnostics: Diagnostic[];
}

interface BicepResource {
  readonly symbol: string;
  readonly type: string;
  readonly body: string;
  readonly line: number;
}

type ScanMode =
  | 'code'
  | 'single-quote'
  | 'double-quote'
  | 'line-comment'
  | 'block-comment';

const CONTAINER_APP = 'Microsoft.App/containerApps';
const MANAGED_ENVIRONMENT = 'Microsoft.App/managedEnvironments';
const ROLE_ASSIGNMENT = 'Microsoft.Authorization/roleAssignments';
const ADAPTER_PARAMETERS = [
  'tenkacloudNamePrefix',
  'tenkacloudProblemId',
  'tenkacloudTeam',
] as const;

function lineAt(source: string, offset: number): number {
  return source.slice(0, offset).split('\n').length;
}

function nextMode(
  mode: ScanMode,
  character: string,
  next: string,
  previous: string
): { readonly mode: ScanMode; readonly consumeNext: boolean } {
  if (mode === 'line-comment') return lineCommentMode(character);
  if (mode === 'block-comment') return blockCommentMode(character, next);
  if (mode === 'single-quote' || mode === 'double-quote')
    return quoteMode(mode, character, previous);
  return codeMode(character, next);
}

function lineCommentMode(character: string): {
  readonly mode: ScanMode;
  readonly consumeNext: boolean;
} {
  return {
    mode: character === '\n' ? 'code' : 'line-comment',
    consumeNext: false,
  };
}

function blockCommentMode(
  character: string,
  next: string
): { readonly mode: ScanMode; readonly consumeNext: boolean } {
  const ended = character === '*' && next === '/';
  return { mode: ended ? 'code' : 'block-comment', consumeNext: ended };
}

function quoteMode(
  mode: 'single-quote' | 'double-quote',
  character: string,
  previous: string
): { readonly mode: ScanMode; readonly consumeNext: boolean } {
  const delimiter = mode === 'single-quote' ? "'" : '"';
  return {
    mode: character === delimiter && previous !== '\\' ? 'code' : mode,
    consumeNext: false,
  };
}

function codeMode(
  character: string,
  next: string
): { readonly mode: ScanMode; readonly consumeNext: boolean } {
  if (character === '/' && next === '/') {
    return { mode: 'line-comment', consumeNext: true };
  }
  if (character === '/' && next === '*') {
    return { mode: 'block-comment', consumeNext: true };
  }
  if (character === "'") {
    return { mode: 'single-quote', consumeNext: false };
  }
  if (character === '"') {
    return { mode: 'double-quote', consumeNext: false };
  }
  return { mode: 'code', consumeNext: false };
}

function isStructural(mode: ScanMode): boolean {
  return mode === 'code';
}

function commentProjection(source: string): string {
  let mode: ScanMode = 'code';
  let result = '';
  for (let index = 0; index < source.length; index++) {
    const character = source[index] ?? '';
    const step = nextMode(
      mode,
      character,
      source[index + 1] ?? '',
      source[index - 1] ?? ''
    );
    const comment =
      mode === 'line-comment' ||
      mode === 'block-comment' ||
      step.mode === 'line-comment' ||
      step.mode === 'block-comment';
    result += comment && character !== '\n' ? ' ' : character;
    mode = step.mode;
    if (step.consumeNext) {
      result += ' ';
      index++;
    }
  }
  return result;
}

function blockEnd(source: string, start: number): number {
  let depth = 0;
  let mode: ScanMode = 'code';
  for (let index = start; index < source.length; index++) {
    const character = source[index] ?? '';
    const structural = isStructural(mode);
    const step = nextMode(
      mode,
      character,
      source[index + 1] ?? '',
      source[index - 1] ?? ''
    );
    mode = step.mode;
    if (step.consumeNext) index++;
    if (!structural) continue;
    if (character === '{') depth++;
    if (character === '}') {
      depth--;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function topLevelProjection(source: string): string {
  let depth = 0;
  let mode: ScanMode = 'code';
  let result = '';
  for (let index = 0; index < source.length; index++) {
    const character = source[index] ?? '';
    const structural = isStructural(mode);
    const step = nextMode(
      mode,
      character,
      source[index + 1] ?? '',
      source[index - 1] ?? ''
    );
    const comment = isComment(mode) || isComment(step.mode);
    if (structural && character === '{') depth++;
    result += projectedCharacter(comment, depth, character);
    if (structural && character === '}') depth--;
    mode = step.mode;
    if (step.consumeNext) {
      result += ' ';
      index++;
    }
  }
  return result;
}

function isComment(mode: ScanMode): boolean {
  return mode === 'line-comment' || mode === 'block-comment';
}

function projectedCharacter(
  comment: boolean,
  depth: number,
  character: string
): string {
  if (!comment && depth === 0) return character;
  return character === '\n' ? '\n' : ' ';
}

function adapterParameters(source: string): boolean {
  const projected = topLevelProjection(source);
  const count = Array.from(projected.matchAll(/(?:^|\n)\s*param\b/g)).length;
  if (count === 0) return false;
  const declarations = Array.from(
    projected.matchAll(
      /(?:^|\n)\s*param\s+([A-Za-z_][\w]*)\s+([A-Za-z_][\w]*)([^\n]*)/g
    )
  );
  if (declarations.length !== count) {
    throw new Error('Bicep parameter declaration syntax is not supported');
  }
  const names = new Set<string>();
  for (const declaration of declarations) {
    const name = declaration[1] ?? '';
    const type = declaration[2] ?? '';
    const remainder = (declaration[3] ?? '').trim();
    if (
      !ADAPTER_PARAMETERS.includes(
        name as (typeof ADAPTER_PARAMETERS)[number]
      ) ||
      type !== 'string' ||
      remainder ||
      names.has(name)
    ) {
      throw new Error(
        `Bicep parameter ${name || '<unknown>'} declaration is not supported`
      );
    }
    names.add(name);
  }
  if (ADAPTER_PARAMETERS.some((name) => !names.has(name))) {
    throw new Error(
      'Bicep TenkaCloud adapter parameters must be declared as one complete set'
    );
  }
  return true;
}

function resources(source: string): readonly BicepResource[] {
  const searchable = commentProjection(source);
  const projected = topLevelProjection(source);
  if (/(?:^|\n)\s*module\b/.test(projected)) {
    throw new Error('Bicep module declarations are not supported');
  }
  const declarationCount = Array.from(
    projected.matchAll(/(?:^|\n)\s*resource\b/g)
  ).length;
  const pattern =
    /(?:^|\n)\s*resource\s+([A-Za-z_][\w]*)\s+(["'])([^"'\n]+)\2\s*=\s*\{/g;
  const result: BicepResource[] = [];
  const symbols = new Set<string>();
  for (const match of searchable.matchAll(pattern)) {
    result.push(resourceFromMatch(source, match, symbols));
  }
  if (result.length === 0) {
    if (declarationCount > 0) {
      throw new Error('Bicep resource declaration syntax is not supported');
    }
    throw new Error('Bicep entry has no resource declarations');
  }
  if (result.length !== declarationCount) {
    throw new Error('Bicep resource declaration syntax is not supported');
  }
  return result;
}

function resourceFromMatch(
  source: string,
  match: RegExpMatchArray,
  symbols: Set<string>
): BicepResource {
  const symbol = match[1] ?? '';
  if (symbols.has(symbol)) {
    throw new Error(`Bicep resource symbol ${symbol} is duplicated`);
  }
  symbols.add(symbol);
  const type = resourceType(match[3] ?? '');
  const start = (match.index ?? 0) + match[0].lastIndexOf('{');
  const end = blockEnd(source, start);
  if (end < 0) throw new Error(`Bicep resource ${symbol} block is not closed`);
  return {
    symbol,
    type,
    body: source.slice(start + 1, end),
    line: lineAt(source, source.indexOf('resource', match.index ?? 0)),
  };
}

function resourceType(declaration: string): string {
  const separator = declaration.lastIndexOf('@');
  if (separator <= 0 || separator === declaration.length - 1) {
    throw new Error(
      `Bicep resource type ${declaration} must include an API version`
    );
  }
  const type = declaration.slice(0, separator);
  if (![CONTAINER_APP, MANAGED_ENVIRONMENT, ROLE_ASSIGNMENT].includes(type)) {
    throw new Error(`Bicep resource ${type} is not supported`);
  }
  return type;
}

function expression(body: string, property: string): string | undefined {
  const match = new RegExp(
    `(?:^|\\n)\\s*${property}\\s*:\\s*([^\\n]+)`,
    'm'
  ).exec(body);
  return match?.[1]?.replace(/,\s*$/, '').trim();
}

function stringExpression(body: string, property: string): string {
  const raw = expression(body, property);
  if (raw === undefined) {
    throw new Error(`Bicep resource requires ${property}`);
  }
  const match = /^(?:'([^']*)'|"([^"]*)")$/.exec(raw);
  const value = match?.[1] ?? match?.[2];
  if (value === undefined || !value) {
    throw new Error(
      `Bicep property ${property} expression ${raw} is not supported`
    );
  }
  return value;
}

function integerExpression(
  body: string,
  property: string,
  minimum: number,
  maximum: number,
  defaultValue: number
): number {
  const raw = expression(body, property);
  if (raw === undefined) return defaultValue;
  if (!/^-?\d+$/.test(raw)) {
    throw new Error(
      `Bicep property ${property} expression ${raw} is not supported`
    );
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) {
    throw new Error(
      `Bicep property ${property} is outside the supported range`
    );
  }
  return value;
}

function resourceName(resource: BicepResource, hasAdapter: boolean): void {
  const name = stringExpression(topLevelProjection(resource.body), 'name');
  if (!name?.includes('${')) return;
  if (
    !hasAdapter ||
    !/^[a-z0-9-]*\$\{uniqueString\(\s*tenkacloudNamePrefix\s*,\s*tenkacloudProblemId\s*,\s*tenkacloudTeam\s*\)\}[a-z0-9-]*$/.test(
      name
    )
  ) {
    throw new Error(
      `Bicep resource ${resource.symbol} name expression is not supported`
    );
  }
}

function validateResources(
  values: readonly BicepResource[],
  hasAdapter: boolean
): { readonly hasContainer: boolean; readonly hasExternalContainer: boolean } {
  const bySymbol = new Map(
    values.map((resource) => [resource.symbol, resource])
  );
  let hasContainer = false;
  let hasExternalContainer = false;
  for (const resource of values) {
    resourceName(resource, hasAdapter);
    validateDependencies(resource, bySymbol);
    if (resource.type === MANAGED_ENVIRONMENT) continue;
    if (resource.type === ROLE_ASSIGNMENT) {
      validateRoleAssignment(resource, bySymbol);
      continue;
    }
    hasContainer = true;
    hasExternalContainer ||= validateContainerApp(resource, bySymbol);
  }
  return { hasContainer, hasExternalContainer };
}

function validateDependencies(
  resource: BicepResource,
  bySymbol: ReadonlyMap<string, BicepResource>
): void {
  const projected = commentProjection(resource.body);
  const match = /(?:^|\n)\s*dependsOn\s*:\s*\[([\s\S]*?)\]/m.exec(projected);
  if (!match) return;
  const raw = match[1] ?? '';
  const dependencies = raw
    .split(/[\s,]+/)
    .map((value) => value.trim())
    .filter(Boolean);
  if (
    dependencies.some(
      (dependency) =>
        !/^[A-Za-z_][\w]*$/.test(dependency) || !bySymbol.has(dependency)
    )
  ) {
    throw new Error(
      `Bicep resource ${resource.symbol} depends on an unknown resource`
    );
  }
}

function validateRoleAssignment(
  resource: BicepResource,
  bySymbol: ReadonlyMap<string, BicepResource>
): void {
  const scope = expression(topLevelProjection(resource.body), 'scope');
  if (!scope || bySymbol.get(scope)?.type !== CONTAINER_APP) {
    throw new Error(
      `Bicep role assignment ${resource.symbol} scopes an unknown Container App ${scope ?? ''}`
    );
  }
  stringExpression(resource.body, 'roleDefinitionId');
  stringExpression(resource.body, 'principalId');
}

function validateContainerApp(
  resource: BicepResource,
  bySymbol: ReadonlyMap<string, BicepResource>
): boolean {
  validateEnvironmentReference(resource, bySymbol);
  stringExpression(resource.body, 'image');
  integerExpression(resource.body, 'targetPort', 1, 65_535, 80);
  const minimum = integerExpression(resource.body, 'minReplicas', 0, 100, 0);
  const maximum = integerExpression(resource.body, 'maxReplicas', 1, 100, 1);
  if (minimum > maximum)
    throw new Error('minReplicas must not exceed maxReplicas');
  const external = expression(resource.body, 'external');
  if (external !== undefined && external !== 'true' && external !== 'false') {
    throw new Error(
      `Bicep property external expression ${external} is not supported`
    );
  }
  return external === 'true';
}

function validateEnvironmentReference(
  resource: BicepResource,
  bySymbol: ReadonlyMap<string, BicepResource>
): void {
  const environment = expression(resource.body, 'environmentId');
  if (environment === undefined) return;
  const match = /^([A-Za-z_][\w]*)\.id$/.exec(environment);
  if (!match || bySymbol.get(match[1] ?? '')?.type !== MANAGED_ENVIRONMENT) {
    throw new Error(
      `Bicep Container App ${resource.symbol} references an unknown Managed Environment`
    );
  }
}

function validateOutputs(
  source: string,
  values: readonly BicepResource[]
): void {
  const projected = topLevelProjection(source);
  const count = Array.from(projected.matchAll(/(?:^|\n)\s*output\b/g)).length;
  const declarations = Array.from(
    projected.matchAll(
      /(?:^|\n)\s*output\s+([A-Za-z_][\w]*)\s+([A-Za-z_][\w]*)\s*=\s*([^\n]+)/g
    )
  );
  if (declarations.length !== count) {
    throw new Error('Bicep output declaration syntax is not supported');
  }
  const names = new Set<string>();
  const bySymbol = new Map(
    values.map((resource) => [resource.symbol, resource])
  );
  for (const declaration of declarations) {
    const name = declaration[1] ?? '';
    const type = declaration[2] ?? '';
    const value = (declaration[3] ?? '').trim();
    if (type !== 'string' || names.has(name)) {
      throw new Error(`Bicep output ${name || '<unknown>'} is not supported`);
    }
    names.add(name);
    validateOutputValue(name, value, bySymbol);
  }
}

function validateOutputValue(
  name: string,
  value: string,
  bySymbol: ReadonlyMap<string, BicepResource>
): void {
  if (validateLiteralOutput(value, bySymbol)) return;
  const reference = /^([A-Za-z_][\w]*)\.(.+)$/.exec(value);
  const resource = reference ? bySymbol.get(reference[1] ?? '') : undefined;
  if (!reference || !resource) {
    throw new Error(`Bicep output ${name} references an unknown resource`);
  }
  const path = reference[2] ?? '';
  if (path === 'id' || path === 'name') return;
  if (
    path === 'properties.configuration.ingress.fqdn' &&
    resource.type === CONTAINER_APP
  ) {
    return;
  }
  throw new Error(`Bicep output expression ${value} is not supported`);
}

function validateLiteralOutput(
  value: string,
  bySymbol: ReadonlyMap<string, BicepResource>
): boolean {
  const literal = /^(?:'([^']*)'|"([^"]*)")$/.exec(value);
  const literalValue = literal?.[1] ?? literal?.[2];
  if (literalValue === undefined) return false;
  const interpolated =
    /^https:\/\/\$\{([A-Za-z_][\w]*)\.properties\.configuration\.ingress\.fqdn\}$/.exec(
      literalValue
    );
  if (!literalValue.includes('${')) return true;
  if (
    interpolated &&
    bySymbol.get(interpolated[1] ?? '')?.type === CONTAINER_APP
  ) {
    return true;
  }
  throw new Error(`Bicep output expression ${value} is not supported`);
}

function requirement(
  target: NormalizedTarget,
  problemId: string,
  sourcePath: string,
  service: string,
  resourceType: string,
  operation: string,
  fidelity: readonly Fidelity[],
  line: number,
  plane: 'deploy' | 'scoring' | 'workload'
): Requirement {
  return createRequirement({
    problemId,
    targetId: target.targetId,
    provider: target.provider,
    engine: target.engine,
    service,
    resourceType,
    operation,
    fidelity,
    plane,
    origin: 'iac-resource',
    classification: 'binding',
    source: { path: sourcePath, line, jsonPointer: null },
  });
}

function scanRequirements(
  contents: string,
  sourcePath: string,
  target: NormalizedTarget,
  problemId: string
): Requirement[] {
  const hasAdapter = adapterParameters(contents);
  const parsedResources = resources(contents);
  const state = validateResources(parsedResources, hasAdapter);
  validateOutputs(contents, parsedResources);
  const result = parsedResources.map((resource) =>
    requirement(
      target,
      problemId,
      sourcePath,
      resource.type === ROLE_ASSIGNMENT ? 'authorization' : 'containerapps',
      resource.type,
      'lifecycle',
      resource.type === CONTAINER_APP
        ? ['L0', 'L1', 'L2', 'L3', 'L4']
        : ['L0', 'L1', 'L2'],
      resource.line,
      'deploy'
    )
  );
  if (state.hasContainer) {
    result.push(
      requirement(
        target,
        problemId,
        sourcePath,
        'http',
        'HTTP::Endpoint',
        'Probe',
        ['L0', 'L1', 'L2', 'L3', 'L4'],
        1,
        'scoring'
      )
    );
  }
  if (state.hasExternalContainer) {
    result.push(
      requirement(
        target,
        problemId,
        sourcePath,
        'http',
        'HTTP::Endpoint',
        'Request',
        ['L0', 'L1', 'L2', 'L3', 'L4'],
        1,
        'workload'
      )
    );
  }
  return result;
}

export function parseBicep(
  contents: string,
  sourcePath: string,
  target: NormalizedTarget,
  problemId: string
): BicepParseResult {
  try {
    return {
      requirements: scanRequirements(contents, sourcePath, target, problemId),
      diagnostics: [],
    };
  } catch (error) {
    return {
      requirements: [],
      diagnostics: [
        {
          code: 'INVALID_BICEP',
          message: `Bicep entry is not supported: ${errorMessage(error)}`,
          problemId,
          targetId: target.targetId,
          source: { path: sourcePath, line: 1, jsonPointer: null },
        },
      ],
    };
  }
}
