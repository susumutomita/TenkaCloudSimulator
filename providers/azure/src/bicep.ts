import { CoreError, deterministicId } from '@tenkacloud/simulator-core';

export const BICEP_CONTAINER_APP = 'Microsoft.App/containerApps';
export const BICEP_MANAGED_ENVIRONMENT = 'Microsoft.App/managedEnvironments';
export const BICEP_ROLE_ASSIGNMENT = 'Microsoft.Authorization/roleAssignments';

const ADAPTER_PARAMETERS = [
  'tenkacloudNamePrefix',
  'tenkacloudProblemId',
  'tenkacloudTeam',
] as const;

export interface BicepResource {
  readonly symbol: string;
  readonly type: string;
  readonly apiVersion: string;
  readonly body: string;
  readonly line: number;
}

export interface BicepOutput {
  readonly name: string;
  readonly type: string;
  readonly expression: string;
  readonly line: number;
}

export interface BicepCompileContext {
  readonly problemId: string;
  readonly targetId: string;
}

export interface CompiledBicepResource {
  readonly symbol: string;
  readonly type: string;
  readonly apiVersion: string;
  readonly line: number;
  readonly name: string;
  readonly resourceId: string;
  readonly dependencies: readonly string[];
  readonly properties: Readonly<Record<string, unknown>>;
}

export interface BicepCompilation {
  readonly resources: readonly CompiledBicepResource[];
  readonly outputs: Readonly<Record<string, string>>;
}

function lineAt(source: string, offset: number): number {
  return source.slice(0, offset).split('\n').length;
}

type ScanMode =
  | 'code'
  | 'single-quote'
  | 'double-quote'
  | 'line-comment'
  | 'block-comment';

interface ScanStep {
  readonly mode: ScanMode;
  readonly consumeNext: boolean;
  readonly structural: boolean;
}

function lineCommentStep(character: string): ScanStep {
  return {
    mode: character === '\n' ? 'code' : 'line-comment',
    consumeNext: false,
    structural: false,
  };
}

function blockCommentStep(character: string, next: string): ScanStep {
  const ended = character === '*' && next === '/';
  return {
    mode: ended ? 'code' : 'block-comment',
    consumeNext: ended,
    structural: false,
  };
}

function quoteStep(
  mode: 'single-quote' | 'double-quote',
  character: string,
  previous: string
): ScanStep {
  const delimiter = mode === 'single-quote' ? "'" : '"';
  return {
    mode: character === delimiter && previous !== '\\' ? 'code' : mode,
    consumeNext: false,
    structural: false,
  };
}

function codeStep(character: string, next: string): ScanStep {
  if (character === '/' && next === '/') {
    return { mode: 'line-comment', consumeNext: true, structural: false };
  }
  if (character === '/' && next === '*') {
    return { mode: 'block-comment', consumeNext: true, structural: false };
  }
  if (character === "'") {
    return { mode: 'single-quote', consumeNext: false, structural: false };
  }
  if (character === '"') {
    return { mode: 'double-quote', consumeNext: false, structural: false };
  }
  return { mode: 'code', consumeNext: false, structural: true };
}

function scanStep(
  mode: ScanMode,
  character: string,
  next: string,
  previous: string
): ScanStep {
  switch (mode) {
    case 'line-comment':
      return lineCommentStep(character);
    case 'block-comment':
      return blockCommentStep(character, next);
    case 'single-quote':
    case 'double-quote':
      return quoteStep(mode, character, previous);
    case 'code':
      return codeStep(character, next);
  }
}

function isComment(mode: ScanMode): boolean {
  return mode === 'line-comment' || mode === 'block-comment';
}

function commentProjection(source: string): string {
  let mode: ScanMode = 'code';
  let result = '';
  for (let index = 0; index < source.length; index++) {
    const character = source[index] ?? '';
    const step = scanStep(
      mode,
      character,
      source[index + 1] ?? '',
      source[index - 1] ?? ''
    );
    const comment = isComment(mode) || isComment(step.mode);
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
    const step = scanStep(
      mode,
      character,
      source[index + 1] ?? '',
      source[index - 1] ?? ''
    );
    mode = step.mode;
    if (step.consumeNext) index++;
    if (!step.structural) continue;
    if (character === '{') depth++;
    if (character === '}') {
      depth--;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function typeAndVersion(value: string): {
  readonly type: string;
  readonly apiVersion: string;
} {
  const separator = value.lastIndexOf('@');
  if (separator <= 0 || separator === value.length - 1) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep resource type ${value} must include an API version`
    );
  }
  return {
    type: value.slice(0, separator),
    apiVersion: value.slice(separator + 1),
  };
}

export function bicepResources(source: string): readonly BicepResource[] {
  const resources: BicepResource[] = [];
  const symbols = new Set<string>();
  const searchable = commentProjection(source);
  const declarationCount = Array.from(
    topLevelProjection(source).matchAll(/(?:^|\n)\s*resource\b/g)
  ).length;
  const pattern =
    /(?:^|\n)\s*resource\s+([A-Za-z_][\w]*)\s+(["'])([^"'\n]+)\2\s*=\s*\{/g;
  for (const match of searchable.matchAll(pattern)) {
    const symbol = match[1] ?? '';
    if (symbols.has(symbol)) {
      throw new CoreError(
        'Conflict',
        `Bicep resource symbol ${symbol} is duplicated`
      );
    }
    symbols.add(symbol);
    const start = (match.index ?? 0) + match[0].lastIndexOf('{');
    const end = blockEnd(source, start);
    if (end === -1) {
      throw new CoreError(
        'ValidationFailed',
        `Bicep resource ${symbol} block is not closed`
      );
    }
    const parsedType = typeAndVersion(match[3] ?? '');
    const resourceOffset = source.indexOf('resource', match.index ?? 0);
    resources.push({
      symbol,
      ...parsedType,
      body: source.slice(start + 1, end),
      line: lineAt(source, resourceOffset),
    });
  }
  if (resources.length === 0) {
    if (declarationCount > 0) {
      throw new CoreError(
        'UnsupportedCapability',
        'Bicep resource declaration syntax is not supported'
      );
    }
    throw new CoreError(
      'ValidationFailed',
      'Bicep entry has no resource declarations'
    );
  }
  if (resources.length !== declarationCount) {
    throw new CoreError(
      'UnsupportedCapability',
      'Bicep resource declaration syntax is not supported'
    );
  }
  return resources;
}

function withoutLineComment(value: string): string {
  let mode: ScanMode = 'code';
  for (let index = 0; index < value.length - 1; index++) {
    const character = value[index] ?? '';
    const step = scanStep(
      mode,
      character,
      value[index + 1] ?? '',
      value[index - 1] ?? ''
    );
    if (mode === 'code' && step.mode === 'line-comment') {
      return value.slice(0, index).trim();
    }
    mode = step.mode;
    if (step.consumeNext) index++;
  }
  return value.trim();
}

export function bicepOutputs(source: string): readonly BicepOutput[] {
  const outputs: BicepOutput[] = [];
  const names = new Set<string>();
  const pattern =
    /(?:^|\n)\s*output\s+([A-Za-z_][\w]*)\s+([A-Za-z_][\w]*)\s*=\s*([^\n]+)/g;
  for (const match of source.matchAll(pattern)) {
    const name = match[1] ?? '';
    if (names.has(name)) {
      throw new CoreError('Conflict', `Bicep output ${name} is duplicated`);
    }
    names.add(name);
    const outputOffset = source.indexOf('output', match.index ?? 0);
    outputs.push({
      name,
      type: match[2] ?? '',
      expression: withoutLineComment(match[3] ?? ''),
      line: lineAt(source, outputOffset),
    });
  }
  return outputs;
}

function propertyExpression(
  body: string,
  property: string
): string | undefined {
  const match = new RegExp(
    `(?:^|\\n)\\s*${property}\\s*:\\s*([^\\n]+)`,
    'm'
  ).exec(body);
  if (!match?.[1]) return undefined;
  return withoutLineComment(match[1])
    .replace(/,\\s*$/, '')
    .trim();
}

function unsupportedPropertyExpression(
  property: string,
  expression: string
): never {
  throw new CoreError(
    'UnsupportedCapability',
    `Bicep property ${property} expression ${expression} is not supported`
  );
}

function stringProperty(body: string, property: string): string | undefined {
  const expression = propertyExpression(body, property);
  if (expression === undefined) return undefined;
  const match = /^(["'])(.*)\1$/.exec(expression);
  const value = match?.[2];
  if (value === undefined || value.includes('${')) {
    return unsupportedPropertyExpression(property, expression);
  }
  return value;
}

function structuralBrace(step: ScanStep, character: string): boolean {
  return step.structural && (character === '{' || character === '}');
}

function nextDepth(depth: number, brace: boolean, character: string): number {
  if (!brace) return depth;
  return character === '{' ? depth + 1 : depth - 1;
}

function projectedCharacter(
  character: string,
  comment: boolean,
  brace: boolean,
  depth: number
): string {
  if (!comment && !brace && depth === 0) return character;
  return character === '\n' ? '\n' : ' ';
}

function topLevelProjection(body: string): string {
  let depth = 0;
  let mode: ScanMode = 'code';
  let result = '';
  for (let index = 0; index < body.length; index++) {
    const character = body[index] ?? '';
    const step = scanStep(
      mode,
      character,
      body[index + 1] ?? '',
      body[index - 1] ?? ''
    );
    const comment = isComment(mode) || isComment(step.mode);
    const brace = structuralBrace(step, character);
    depth = nextDepth(depth, brace, character);
    result += projectedCharacter(character, comment, brace, depth);
    mode = step.mode;
    if (step.consumeNext) {
      result += ' ';
      index++;
    }
  }
  return result;
}

function validateAdapterParameters(source: string): boolean {
  const projected = topLevelProjection(source);
  const declarationCount = Array.from(
    projected.matchAll(/(?:^|\n)\s*param\b/g)
  ).length;
  if (declarationCount === 0) return false;

  const declarations = Array.from(
    projected.matchAll(
      /(?:^|\n)\s*param\s+([A-Za-z_][\w]*)\s+([A-Za-z_][\w]*)([^\n]*)/g
    )
  );
  if (declarations.length !== declarationCount) {
    throw new CoreError(
      'UnsupportedCapability',
      'Bicep parameter declaration syntax is not supported'
    );
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
      remainder.length > 0 ||
      names.has(name)
    ) {
      throw new CoreError(
        'UnsupportedCapability',
        `Bicep parameter ${name || '<unknown>'} declaration is not supported`
      );
    }
    names.add(name);
  }
  if (
    names.size !== ADAPTER_PARAMETERS.length ||
    ADAPTER_PARAMETERS.some((name) => !names.has(name))
  ) {
    throw new CoreError(
      'UnsupportedCapability',
      'Bicep TenkaCloud adapter parameters must be declared as one complete set'
    );
  }
  return true;
}

function topLevelStringProperty(
  body: string,
  property: string
): string | undefined {
  return stringProperty(topLevelProjection(body), property);
}

function numberProperty(body: string, property: string): number | undefined {
  const expression = propertyExpression(body, property);
  if (expression === undefined) return undefined;
  if (!/^-?\d+(?:\.\d+)?$/.test(expression)) {
    return unsupportedPropertyExpression(property, expression);
  }
  return Number(expression);
}

function booleanProperty(body: string, property: string): boolean | undefined {
  const expression = propertyExpression(body, property);
  if (expression === undefined) return undefined;
  if (expression !== 'true' && expression !== 'false') {
    return unsupportedPropertyExpression(property, expression);
  }
  return expression === 'true';
}

function referenceProperty(body: string, property: string): string | undefined {
  const expression = propertyExpression(body, property);
  if (expression === undefined) return undefined;
  if (!/^[A-Za-z_][\w]*$/.test(expression)) {
    return unsupportedPropertyExpression(property, expression);
  }
  return expression;
}

function resourceIdReferenceProperty(
  body: string,
  property: string
): string | undefined {
  const expression = propertyExpression(body, property);
  if (expression === undefined) return undefined;
  const reference = /^([A-Za-z_][\w]*)\.id$/.exec(expression);
  if (!reference?.[1]) {
    return unsupportedPropertyExpression(property, expression);
  }
  return reference[1];
}

function topLevelReferenceProperty(
  body: string,
  property: string
): string | undefined {
  return referenceProperty(topLevelProjection(body), property);
}

function dependencySymbols(body: string): readonly string[] {
  const match = /(?:^|\n)\s*dependsOn\s*:\s*\[([\s\S]*?)\]/m.exec(body);
  if (!match?.[1]) return [];
  const source = match[1].replace(/\/\/[^\n]*/g, ' ');
  return source.match(/[A-Za-z_][\w]*/g) ?? [];
}

function requiredString(
  body: string,
  property: string,
  symbol: string
): string {
  const value = stringProperty(body, property);
  if (!value?.trim()) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep resource ${symbol} requires string property ${property}`
    );
  }
  return value;
}

function adapterResourceName(
  expression: string,
  resource: BicepResource,
  context: BicepCompileContext,
  adapterParametersDeclared: boolean
): string {
  const literal = /^(?:'([^']*)'|"([^"]*)")$/.exec(expression);
  const value = literal?.[1] ?? literal?.[2];
  if (value === undefined) {
    return unsupportedPropertyExpression('name', expression);
  }
  if (!value.includes('${')) return value;

  const parameterized =
    /^([a-z0-9-]*)\$\{uniqueString\(\s*tenkacloudNamePrefix\s*,\s*tenkacloudProblemId\s*,\s*tenkacloudTeam\s*\)\}([a-z0-9-]*)$/.exec(
      value
    );
  if (
    !adapterParametersDeclared ||
    !parameterized ||
    (resource.type !== BICEP_MANAGED_ENVIRONMENT &&
      resource.type !== BICEP_CONTAINER_APP)
  ) {
    return unsupportedPropertyExpression('name', expression);
  }
  const expectedSuffix =
    resource.type === BICEP_CONTAINER_APP ? '-app' : '-env';
  if (parameterized[1] !== 'tc-' || parameterized[2] !== expectedSuffix) {
    return unsupportedPropertyExpression('name', expression);
  }
  const unique = deterministicId('azure-bicep-name', context).slice(-13);
  const name = `${parameterized[1] ?? ''}${unique}${parameterized[2] ?? ''}`;
  return name;
}

function requiredResourceName(
  resource: BicepResource,
  context: BicepCompileContext,
  adapterParametersDeclared: boolean
): string {
  const expression = propertyExpression(
    topLevelProjection(resource.body),
    'name'
  );
  if (expression === undefined) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep resource ${resource.symbol} requires string property name`
    );
  }
  const name = adapterResourceName(
    expression,
    resource,
    context,
    adapterParametersDeclared
  );
  if (!name.trim()) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep resource ${resource.symbol} requires string property name`
    );
  }
  return name;
}

function integerProperty(
  body: string,
  property: string,
  fallback: number,
  minimum: number,
  maximum: number
): number {
  const value = numberProperty(body, property) ?? fallback;
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new CoreError(
      'ValidationFailed',
      `${property} must be an integer between ${minimum} and ${maximum}`
    );
  }
  return value;
}

function baseResourceId(
  type: string,
  name: string,
  context: BicepCompileContext
): string {
  const subscription = deterministicId('subscription', context);
  const resourceGroup = deterministicId('resource-group', context);
  return `/subscriptions/${subscription}/resourceGroups/${resourceGroup}/providers/${type}/${name}`;
}

function resolveDependencies(
  symbols: readonly string[],
  ids: ReadonlyMap<string, string>,
  owner: string
): readonly string[] {
  return Array.from(new Set(symbols)).map((symbol) => {
    const id = ids.get(symbol);
    if (!id) {
      throw new CoreError(
        'ValidationFailed',
        `Bicep resource ${owner} depends on unknown symbol ${symbol}`
      );
    }
    return id;
  });
}

function containerAppProperties(
  resource: BicepResource,
  id: string,
  name: string,
  dependencies: readonly string[],
  context: BicepCompileContext,
  environmentId?: string
): Readonly<Record<string, unknown>> {
  const targetPort = integerProperty(
    resource.body,
    'targetPort',
    80,
    1,
    65_535
  );
  const minReplicas = integerProperty(resource.body, 'minReplicas', 0, 0, 100);
  const maxReplicas = integerProperty(resource.body, 'maxReplicas', 1, 1, 100);
  if (minReplicas > maxReplicas) {
    throw new CoreError(
      'ValidationFailed',
      'minReplicas must not exceed maxReplicas'
    );
  }
  return {
    id,
    symbol: resource.symbol,
    name,
    apiVersion: resource.apiVersion,
    location: topLevelStringProperty(resource.body, 'location') ?? 'japaneast',
    dependencies,
    ...(environmentId ? { environmentId } : {}),
    status: 'Running',
    external: booleanProperty(resource.body, 'external') ?? false,
    targetPort,
    minReplicas,
    maxReplicas,
    image: requiredString(resource.body, 'image', resource.symbol),
    fqdn: `${deterministicId('container-app', {
      ...context,
      name,
    })}.azurecontainerapps.local`,
    responseStatus: 200,
    responseBody: 'Hello from TenkaCloud Simulator',
    sourceLine: resource.line,
  };
}

function managedEnvironmentProperties(
  resource: BicepResource,
  id: string,
  name: string,
  dependencies: readonly string[]
): Readonly<Record<string, unknown>> {
  return {
    id,
    symbol: resource.symbol,
    name,
    apiVersion: resource.apiVersion,
    location: topLevelStringProperty(resource.body, 'location') ?? 'japaneast',
    dependencies,
    status: 'Ready',
    sourceLine: resource.line,
  };
}

function roleAssignmentProperties(
  resource: BicepResource,
  id: string,
  name: string,
  scopeId: string,
  dependencies: readonly string[]
): Readonly<Record<string, unknown>> {
  return {
    id,
    symbol: resource.symbol,
    name,
    apiVersion: resource.apiVersion,
    scopeId,
    dependencies,
    roleDefinitionId: requiredString(
      resource.body,
      'roleDefinitionId',
      resource.symbol
    ),
    principalId: requiredString(resource.body, 'principalId', resource.symbol),
    status: 'Assigned',
    sourceLine: resource.line,
  };
}

function literalOutputValue(
  output: BicepOutput,
  resources: ReadonlyMap<string, CompiledBicepResource>
): string | null {
  const literal = /^(["'])(.*?)\1$/.exec(output.expression);
  const expression = literal?.[2];
  if (expression === undefined) return null;
  const httpsFqdn =
    /^https:\/\/\$\{([A-Za-z_][\w]*)\.properties\.configuration\.ingress\.fqdn\}$/.exec(
      expression
    );
  if (!httpsFqdn) {
    if (expression.includes('${')) {
      throw new CoreError(
        'UnsupportedCapability',
        `Bicep output expression ${output.expression} is not supported`
      );
    }
    return expression;
  }
  const resource = resources.get(httpsFqdn[1] ?? '');
  if (!resource) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep output ${output.name} references an unknown resource`
    );
  }
  const fqdn = resource.properties['fqdn'];
  if (resource.type === BICEP_CONTAINER_APP && typeof fqdn === 'string') {
    return `https://${fqdn}`;
  }
  throw new CoreError(
    'UnsupportedCapability',
    `Bicep output expression ${output.expression} is not supported`
  );
}

function outputValue(
  output: BicepOutput,
  resources: ReadonlyMap<string, CompiledBicepResource>
): string {
  if (output.type !== 'string') {
    throw new CoreError(
      'UnsupportedCapability',
      `Bicep output type ${output.type} is not supported`
    );
  }
  const literal = literalOutputValue(output, resources);
  if (literal !== null) return literal;
  const reference = /^([A-Za-z_][\w]*)\.(.+)$/.exec(output.expression);
  const resource = reference ? resources.get(reference[1] ?? '') : undefined;
  if (!reference || !resource) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep output ${output.name} references an unknown resource`
    );
  }
  const path = reference[2];
  if (path === 'id') return resource.resourceId;
  if (path === 'name') return resource.name;
  if (path === 'properties.configuration.ingress.fqdn') {
    const fqdn = resource.properties['fqdn'];
    if (typeof fqdn === 'string') return fqdn;
  }
  throw new CoreError(
    'UnsupportedCapability',
    `Bicep output expression ${output.expression} is not supported`
  );
}

interface NamedBicepResource {
  readonly resource: BicepResource;
  readonly name: string;
}

interface IdentifiedContainerApp extends NamedBicepResource {
  readonly kind: 'container-app';
  readonly resourceId: string;
  readonly environmentSymbol?: string;
  readonly environmentId?: string;
}

interface IdentifiedManagedEnvironment extends NamedBicepResource {
  readonly kind: 'managed-environment';
  readonly resourceId: string;
}

interface IdentifiedRoleAssignment extends NamedBicepResource {
  readonly kind: 'role-assignment';
  readonly resourceId: string;
  readonly scopeSymbol: string;
  readonly scopeId: string;
}

type IdentifiedResource =
  | IdentifiedContainerApp
  | IdentifiedManagedEnvironment
  | IdentifiedRoleAssignment;

function validateResourceTypes(resources: readonly BicepResource[]): void {
  for (const resource of resources) {
    if (
      resource.type !== BICEP_CONTAINER_APP &&
      resource.type !== BICEP_MANAGED_ENVIRONMENT &&
      resource.type !== BICEP_ROLE_ASSIGNMENT
    ) {
      throw new CoreError(
        'UnsupportedCapability',
        `Bicep resource ${resource.type} is not supported`
      );
    }
  }
}

function validateTopLevelConstructs(source: string): void {
  if (/(?:^|\n)\s*module\b/.test(topLevelProjection(source))) {
    throw new CoreError(
      'UnsupportedCapability',
      'Bicep module declarations are not supported'
    );
  }
}

function requiredScope(resource: BicepResource): string {
  const scope = topLevelReferenceProperty(resource.body, 'scope');
  if (!scope) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep role assignment ${resource.symbol} requires a scope reference`
    );
  }
  return scope;
}

function identifyResource(
  item: NamedBicepResource,
  context: BicepCompileContext,
  environmentIds: ReadonlyMap<string, string>,
  containerIds: ReadonlyMap<string, string>
): IdentifiedResource {
  if (item.resource.type === BICEP_MANAGED_ENVIRONMENT) {
    return {
      ...item,
      kind: 'managed-environment',
      resourceId: baseResourceId(item.resource.type, item.name, context),
    };
  }
  if (item.resource.type === BICEP_CONTAINER_APP) {
    const environmentSymbol = resourceIdReferenceProperty(
      item.resource.body,
      'environmentId'
    );
    const environmentId = environmentSymbol
      ? environmentIds.get(environmentSymbol)
      : undefined;
    if (environmentSymbol && !environmentId) {
      throw new CoreError(
        'ValidationFailed',
        `Bicep Container App ${item.resource.symbol} references an unknown Managed Environment ${environmentSymbol}`
      );
    }
    return {
      ...item,
      kind: 'container-app',
      resourceId: baseResourceId(item.resource.type, item.name, context),
      ...(environmentSymbol ? { environmentSymbol } : {}),
      ...(environmentId ? { environmentId } : {}),
    };
  }
  const scopeSymbol = requiredScope(item.resource);
  const scopeId = containerIds.get(scopeSymbol);
  if (!scopeId) {
    throw new CoreError(
      'ValidationFailed',
      `Bicep role assignment ${item.resource.symbol} scopes an unknown Container App ${scopeSymbol}`
    );
  }
  return {
    ...item,
    kind: 'role-assignment',
    resourceId: `${scopeId}/providers/${BICEP_ROLE_ASSIGNMENT}/${item.name}`,
    scopeSymbol,
    scopeId,
  };
}

function identifyResources(
  resources: readonly BicepResource[],
  context: BicepCompileContext,
  adapterParametersDeclared: boolean
): readonly IdentifiedResource[] {
  const named = resources.map(
    (resource): NamedBicepResource => ({
      resource,
      name: requiredResourceName(resource, context, adapterParametersDeclared),
    })
  );
  const containerIds = new Map<string, string>();
  const environmentIds = new Map<string, string>();
  for (const item of named) {
    if (item.resource.type === BICEP_CONTAINER_APP) {
      containerIds.set(
        item.resource.symbol,
        baseResourceId(item.resource.type, item.name, context)
      );
    }
    if (item.resource.type === BICEP_MANAGED_ENVIRONMENT) {
      environmentIds.set(
        item.resource.symbol,
        baseResourceId(item.resource.type, item.name, context)
      );
    }
  }
  return named.map((item) =>
    identifyResource(item, context, environmentIds, containerIds)
  );
}

function compileResource(
  item: IdentifiedResource,
  ids: ReadonlyMap<string, string>,
  context: BicepCompileContext
): CompiledBicepResource {
  const dependencyNames = [
    ...dependencySymbols(item.resource.body),
    ...(item.kind === 'role-assignment' ? [item.scopeSymbol] : []),
    ...(item.kind === 'container-app' && item.environmentSymbol
      ? [item.environmentSymbol]
      : []),
  ];
  const dependencies = resolveDependencies(
    dependencyNames,
    ids,
    item.resource.symbol
  );
  let properties: Readonly<Record<string, unknown>>;
  switch (item.kind) {
    case 'container-app':
      properties = containerAppProperties(
        item.resource,
        item.resourceId,
        item.name,
        dependencies,
        context,
        item.environmentId
      );
      break;
    case 'managed-environment':
      properties = managedEnvironmentProperties(
        item.resource,
        item.resourceId,
        item.name,
        dependencies
      );
      break;
    case 'role-assignment':
      properties = roleAssignmentProperties(
        item.resource,
        item.resourceId,
        item.name,
        item.scopeId,
        dependencies
      );
      break;
  }
  return {
    symbol: item.resource.symbol,
    type: item.resource.type,
    apiVersion: item.resource.apiVersion,
    line: item.resource.line,
    name: item.name,
    resourceId: item.resourceId,
    dependencies,
    properties,
  };
}

function compileOutputs(
  source: string,
  resources: ReadonlyMap<string, CompiledBicepResource>
): Readonly<Record<string, string>> {
  const outputs: Record<string, string> = {};
  for (const output of bicepOutputs(source)) {
    outputs[output.name] = outputValue(output, resources);
  }
  return outputs;
}

export function compileBicep(
  source: string,
  context: BicepCompileContext
): BicepCompilation {
  validateTopLevelConstructs(source);
  const adapterParametersDeclared = validateAdapterParameters(source);
  const resources = bicepResources(source);
  validateResourceTypes(resources);
  const identified = identifyResources(
    resources,
    context,
    adapterParametersDeclared
  );
  const ids = new Map(
    identified.map((item) => [item.resource.symbol, item.resourceId])
  );
  const compiled = identified.map((item) =>
    compileResource(item, ids, context)
  );
  const bySymbol = new Map(
    compiled.map((resource) => [resource.symbol, resource])
  );
  return { resources: compiled, outputs: compileOutputs(source, bySymbol) };
}
