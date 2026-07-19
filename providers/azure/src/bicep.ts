import { CoreError, deterministicId } from '@tenkacloud/simulator-core';

export const BICEP_CONTAINER_APP = 'Microsoft.App/containerApps';
export const BICEP_MANAGED_ENVIRONMENT = 'Microsoft.App/managedEnvironments';
export const BICEP_ROLE_ASSIGNMENT = 'Microsoft.Authorization/roleAssignments';

const ADAPTER_PARAMETERS = [
  'tenkacloudNamePrefix',
  'tenkacloudProblemId',
  'tenkacloudTeam',
] as const;

const BICEP_MAX_SOURCE_BYTES = 5 * 1024 * 1024;
const BICEP_MAX_SOURCE_LINES = 100_000;
const BICEP_MAX_LINE_BYTES = 64 * 1024;
const BICEP_MAX_RESOURCES = 256;

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

interface SourceScan {
  readonly character: string;
  readonly step: ScanStep;
  readonly comment: boolean;
}

function scanSourceCharacter(
  source: string,
  index: number,
  mode: ScanMode
): SourceScan {
  const character = source[index] ?? '';
  const step = scanStep(
    mode,
    character,
    source[index + 1] ?? '',
    source[index - 1] ?? ''
  );
  return {
    character,
    step,
    comment: isComment(mode) || isComment(step.mode),
  };
}

function commentProjection(source: string): string {
  let mode: ScanMode = 'code';
  let result = '';
  for (let index = 0; index < source.length; index++) {
    const { character, step, comment } = scanSourceCharacter(
      source,
      index,
      mode
    );
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
    const { character, step } = scanSourceCharacter(source, index, mode);
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

interface ProjectedSourceLine {
  readonly topLevel: string;
  readonly commentless: string;
  readonly offset: number;
  readonly number: number;
}

interface TopLevelDeclarations {
  readonly resources: readonly ProjectedSourceLine[];
  readonly outputs: readonly ProjectedSourceLine[];
  readonly parameters: readonly ProjectedSourceLine[];
  readonly modules: readonly ProjectedSourceLine[];
}

interface IdentifierToken {
  readonly value: string;
  readonly end: number;
}

interface ResourceDeclaration {
  readonly symbol: string;
  readonly typeAndVersion: string;
  readonly blockStart: number;
}

interface OutputDeclaration {
  readonly name: string;
  readonly type: string;
  readonly expression: string;
}

interface ParameterDeclaration {
  readonly name: string;
  readonly type: string;
  readonly remainder: string;
}

function isHorizontalWhitespace(character: string): boolean {
  return (
    character === ' ' ||
    character === '\t' ||
    character === '\r' ||
    character === '\f' ||
    character === '\v'
  );
}

function skipHorizontalWhitespace(value: string, start: number): number {
  let index = start;
  while (index < value.length && isHorizontalWhitespace(value[index] ?? '')) {
    index++;
  }
  return index;
}

function isAsciiLetter(character: string): boolean {
  return (
    (character >= 'A' && character <= 'Z') ||
    (character >= 'a' && character <= 'z')
  );
}

function isIdentifierStart(character: string): boolean {
  return character === '_' || isAsciiLetter(character);
}

function isIdentifierPart(character: string): boolean {
  return isIdentifierStart(character) || (character >= '0' && character <= '9');
}

function identifierAt(value: string, start: number): IdentifierToken | null {
  if (!isIdentifierStart(value[start] ?? '')) return null;
  let end = start + 1;
  while (end < value.length && isIdentifierPart(value[end] ?? '')) end++;
  return { value: value.slice(start, end), end };
}

function requiredIdentifierAfter(
  value: string,
  start: number
): IdentifierToken | null {
  const tokenStart = skipHorizontalWhitespace(value, start);
  if (tokenStart === start) return null;
  return identifierAt(value, tokenStart);
}

function quotedResourceTypeAt(
  value: string,
  start: number
): IdentifierToken | null {
  const quote = value[start] ?? '';
  if (quote !== "'" && quote !== '"') return null;
  const typeStart = start + 1;
  let end = typeStart;
  while (end < value.length && value[end] !== quote) {
    const character = value[end] ?? '';
    if (character === "'" || character === '"') return null;
    end++;
  }
  if (end === typeStart || end >= value.length) return null;
  return { value: value.slice(typeStart, end), end: end + 1 };
}

function parseResourceDeclaration(
  line: ProjectedSourceLine
): ResourceDeclaration | null {
  const text = line.commentless;
  const keyword = identifierAt(text, skipHorizontalWhitespace(text, 0));
  if (keyword?.value !== 'resource') return null;
  const symbol = requiredIdentifierAfter(text, keyword.end);
  if (!symbol) return null;
  let index = skipHorizontalWhitespace(text, symbol.end);
  if (index === symbol.end) return null;
  const resourceType = quotedResourceTypeAt(text, index);
  if (!resourceType) return null;
  index = skipHorizontalWhitespace(text, resourceType.end);
  if (text[index] !== '=') return null;
  index = skipHorizontalWhitespace(text, index + 1);
  if (text[index] !== '{') return null;
  return {
    symbol: symbol.value,
    typeAndVersion: resourceType.value,
    blockStart: line.offset + index,
  };
}

function parseOutputDeclaration(
  line: ProjectedSourceLine
): OutputDeclaration | null {
  const text = line.topLevel;
  const keyword = identifierAt(text, skipHorizontalWhitespace(text, 0));
  if (keyword?.value !== 'output') return null;
  const name = requiredIdentifierAfter(text, keyword.end);
  if (!name) return null;
  const type = requiredIdentifierAfter(text, name.end);
  if (!type) return null;
  let index = skipHorizontalWhitespace(text, type.end);
  if (text[index] !== '=') return null;
  index = skipHorizontalWhitespace(text, index + 1);
  const expression = text.slice(index).trim();
  if (!expression) return null;
  return { name: name.value, type: type.value, expression };
}

function parseParameterDeclaration(
  line: ProjectedSourceLine
): ParameterDeclaration | null {
  const text = line.topLevel;
  const keyword = identifierAt(text, skipHorizontalWhitespace(text, 0));
  if (keyword?.value !== 'param') return null;
  const name = requiredIdentifierAfter(text, keyword.end);
  if (!name) return null;
  const type = requiredIdentifierAfter(text, name.end);
  if (!type) return null;
  return {
    name: name.value,
    type: type.value,
    remainder: text.slice(type.end).trim(),
  };
}

function parseBicepResources(
  source: string,
  declarations: TopLevelDeclarations
): readonly BicepResource[] {
  if (declarations.resources.length === 0) {
    throw new CoreError(
      'ValidationFailed',
      'Bicep entry has no resource declarations'
    );
  }
  const resources: BicepResource[] = [];
  const symbols = new Set<string>();
  for (const line of declarations.resources) {
    const declaration = parseResourceDeclaration(line);
    if (!declaration) {
      throw new CoreError(
        'UnsupportedCapability',
        'Bicep resource declaration syntax is not supported'
      );
    }
    const { symbol, blockStart } = declaration;
    if (symbols.has(symbol)) {
      throw new CoreError(
        'Conflict',
        `Bicep resource symbol ${symbol} is duplicated`
      );
    }
    symbols.add(symbol);
    const end = blockEnd(source, blockStart);
    if (end === -1) {
      throw new CoreError(
        'ValidationFailed',
        `Bicep resource ${symbol} block is not closed`
      );
    }
    resources.push({
      symbol,
      ...typeAndVersion(declaration.typeAndVersion),
      body: source.slice(blockStart + 1, end),
      line: line.number,
    });
  }
  return resources;
}

export function bicepResources(source: string): readonly BicepResource[] {
  return parseBicepResources(source, topLevelDeclarations(source));
}

function parseBicepOutputs(
  declarations: TopLevelDeclarations
): readonly BicepOutput[] {
  const outputs: BicepOutput[] = [];
  const names = new Set<string>();
  for (const line of declarations.outputs) {
    const declaration = parseOutputDeclaration(line);
    if (!declaration) {
      throw new CoreError(
        'UnsupportedCapability',
        'Bicep output declaration syntax is not supported'
      );
    }
    const { name } = declaration;
    if (names.has(name)) {
      throw new CoreError('Conflict', `Bicep output ${name} is duplicated`);
    }
    names.add(name);
    outputs.push({
      name,
      type: declaration.type,
      expression: declaration.expression,
      line: line.number,
    });
  }
  return outputs;
}

export function bicepOutputs(source: string): readonly BicepOutput[] {
  return parseBicepOutputs(topLevelDeclarations(source));
}

function propertyExpression(
  body: string,
  property: string
): string | undefined {
  const searchable = commentProjection(body);
  const location = lineProperty(searchable, property);
  if (!location) return undefined;
  let expression = searchable
    .slice(location.valueStart, location.lineEnd)
    .trim();
  if (expression.endsWith(',')) expression = expression.slice(0, -1).trim();
  return expression;
}

interface LineProperty {
  readonly valueStart: number;
  readonly lineEnd: number;
}

function malformedProperty(property: string, line: string): never {
  return unsupportedPropertyExpression(property, line.trim() || '<empty>');
}

function lineProperty(source: string, property: string): LineProperty | null {
  let lineStart = 0;
  while (lineStart <= source.length) {
    const newline = source.indexOf('\n', lineStart);
    const lineEnd = newline === -1 ? source.length : newline;
    const line = source.slice(lineStart, lineEnd);
    const tokenStart = skipHorizontalWhitespace(line, 0);
    const token = identifierAt(line, tokenStart);
    if (token?.value === property) {
      const separator = skipHorizontalWhitespace(line, token.end);
      if (line[separator] !== ':') malformedProperty(property, line);
      return {
        valueStart: lineStart + skipHorizontalWhitespace(line, separator + 1),
        lineEnd,
      };
    }
    if (newline === -1) break;
    lineStart = newline + 1;
  }
  return null;
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
    const { character, step, comment } = scanSourceCharacter(body, index, mode);
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

function utf8Width(source: string, index: number): number {
  const code = source.charCodeAt(index);
  if (code <= 0x7f) return 1;
  if (code <= 0x7ff) return 2;
  if (code >= 0xd800 && code <= 0xdbff) {
    const next = source.charCodeAt(index + 1);
    if (next >= 0xdc00 && next <= 0xdfff) return 4;
  }
  if (code >= 0xdc00 && code <= 0xdfff) {
    const previous = source.charCodeAt(index - 1);
    if (previous >= 0xd800 && previous <= 0xdbff) return 0;
  }
  return 3;
}

function sourceLimitExceeded(kind: string, limit: number): never {
  throw new CoreError(
    'ValidationFailed',
    `Bicep source exceeds ${limit} ${kind}`
  );
}

function validateBicepSourceBounds(source: string): void {
  let bytes = 0;
  let lines = 1;
  let lineBytes = 0;
  for (let index = 0; index < source.length; index++) {
    const width = utf8Width(source, index);
    bytes += width;
    if (source[index] === '\n') {
      lines++;
      lineBytes = 0;
    } else {
      lineBytes += width;
    }
    if (bytes > BICEP_MAX_SOURCE_BYTES) {
      sourceLimitExceeded('bytes', BICEP_MAX_SOURCE_BYTES);
    }
    if (lines > BICEP_MAX_SOURCE_LINES) {
      sourceLimitExceeded('lines', BICEP_MAX_SOURCE_LINES);
    }
    if (lineBytes > BICEP_MAX_LINE_BYTES) {
      sourceLimitExceeded('line bytes', BICEP_MAX_LINE_BYTES);
    }
  }
}

function* projectedSourceLines(
  source: string
): Generator<ProjectedSourceLine, void, undefined> {
  validateBicepSourceBounds(source);
  let depth = 0;
  let mode: ScanMode = 'code';
  let topLevel = '';
  let commentless = '';
  let offset = 0;
  let number = 1;
  for (let index = 0; index < source.length; index++) {
    const { character, step, comment } = scanSourceCharacter(
      source,
      index,
      mode
    );
    const brace = structuralBrace(step, character);
    depth = nextDepth(depth, brace, character);
    mode = step.mode;
    if (character === '\n') {
      yield { topLevel, commentless, offset, number };
      topLevel = '';
      commentless = '';
      offset = index + 1;
      number++;
      continue;
    }
    topLevel += projectedCharacter(character, comment, brace, depth);
    commentless += comment ? ' ' : character;
    if (step.consumeNext) {
      topLevel += ' ';
      commentless += ' ';
      index++;
    }
  }
  yield { topLevel, commentless, offset, number };
}

function topLevelDeclarations(source: string): TopLevelDeclarations {
  const resources: ProjectedSourceLine[] = [];
  const outputs: ProjectedSourceLine[] = [];
  const parameters: ProjectedSourceLine[] = [];
  const modules: ProjectedSourceLine[] = [];
  for (const line of projectedSourceLines(source)) {
    const start = skipHorizontalWhitespace(line.topLevel, 0);
    const keyword = identifierAt(line.topLevel, start)?.value;
    const commentlessStart = skipHorizontalWhitespace(line.commentless, 0);
    const commentlessKeyword = identifierAt(
      line.commentless,
      commentlessStart
    )?.value;
    if (commentlessKeyword === 'resource' && keyword !== 'resource') {
      throw new CoreError(
        'UnsupportedCapability',
        'Bicep nested resource declarations are not supported'
      );
    }
    switch (keyword) {
      case 'resource':
        resources.push(line);
        if (resources.length > BICEP_MAX_RESOURCES) {
          throw new CoreError(
            'ValidationFailed',
            `Bicep source exceeds ${BICEP_MAX_RESOURCES} resources`
          );
        }
        break;
      case 'output':
        outputs.push(line);
        break;
      case 'param':
        parameters.push(line);
        break;
      case 'module':
        modules.push(line);
        break;
    }
  }
  return { resources, outputs, parameters, modules };
}

function validateAdapterParameters(
  declarations: TopLevelDeclarations
): boolean {
  if (declarations.parameters.length === 0) return false;

  const names = new Set<string>();
  for (const line of declarations.parameters) {
    const declaration = parseParameterDeclaration(line);
    if (!declaration) {
      throw new CoreError(
        'UnsupportedCapability',
        'Bicep parameter declaration syntax is not supported'
      );
    }
    const { name, type, remainder } = declaration;
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
  const searchable = commentProjection(body);
  const location = lineProperty(searchable, 'dependsOn');
  if (!location) return [];
  if (searchable[location.valueStart] !== '[') {
    return malformedProperty(
      'dependsOn',
      searchable.slice(location.valueStart, location.lineEnd)
    );
  }
  const symbols: string[] = [];
  let index = location.valueStart + 1;
  while (index < searchable.length) {
    const character = searchable[index] ?? '';
    if (character === ']') return symbols;
    if (
      character === ' ' ||
      character === '\t' ||
      character === '\r' ||
      character === '\n' ||
      character === ','
    ) {
      index++;
      continue;
    }
    const token = identifierAt(searchable, index);
    if (token) {
      symbols.push(token.value);
      index = token.end;
      continue;
    }
    break;
  }
  throw new CoreError(
    'ValidationFailed',
    'Bicep dependency array is not closed'
  );
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

function validateTopLevelConstructs(declarations: TopLevelDeclarations): void {
  if (declarations.modules.length > 0) {
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
  const identified = named.map((item) =>
    identifyResource(item, context, environmentIds, containerIds)
  );
  const resourceSymbols = new Map<string, string>();
  for (const item of identified) {
    const previous = resourceSymbols.get(item.resourceId);
    if (previous) {
      throw new CoreError(
        'Conflict',
        `Bicep symbols ${previous} and ${item.resource.symbol} resolve to duplicate resource ID ${item.resourceId}`
      );
    }
    resourceSymbols.set(item.resourceId, item.resource.symbol);
  }
  return identified;
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
  declarations: TopLevelDeclarations,
  resources: ReadonlyMap<string, CompiledBicepResource>
): Readonly<Record<string, string>> {
  const outputs: Record<string, string> = {};
  for (const output of parseBicepOutputs(declarations)) {
    outputs[output.name] = outputValue(output, resources);
  }
  return outputs;
}

export function compileBicep(
  source: string,
  context: BicepCompileContext
): BicepCompilation {
  const declarations = topLevelDeclarations(source);
  validateTopLevelConstructs(declarations);
  const adapterParametersDeclared = validateAdapterParameters(declarations);
  const resources = parseBicepResources(source, declarations);
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
  return {
    resources: compiled,
    outputs: compileOutputs(declarations, bySymbol),
  };
}
