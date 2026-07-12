import { CoreError } from '@tenkacloud/simulator-core';

export interface TerraformResource {
  readonly type: string;
  readonly name: string;
  readonly body: string;
  readonly line: number;
}

function lineAt(source: string, offset: number): number {
  return source.slice(0, offset).split('\n').length;
}

function nextQuote(quote: string, character: string, previous: string): string {
  if ((character !== '"' && character !== "'") || previous === '\\')
    return quote;
  if (!quote) return character;
  return quote === character ? '' : quote;
}

function nextDepth(depth: number, character: string): number {
  if (character === '{') return depth + 1;
  if (character === '}') return depth - 1;
  return depth;
}

function blockEnd(source: string, start: number): number {
  let depth = 0;
  let quote = '';
  let comment = false;
  for (let index = start; index < source.length; index++) {
    const character = source[index] ?? '';
    if (comment) {
      comment = character !== '\n';
      continue;
    }
    if (!quote && character === '#') {
      comment = true;
      continue;
    }
    const updatedQuote = nextQuote(quote, character, source[index - 1] ?? '');
    if (updatedQuote !== quote) {
      quote = updatedQuote;
      continue;
    }
    if (quote) continue;
    depth = nextDepth(depth, character);
    if (character === '}' && depth === 0) return index;
  }
  return -1;
}

export function terraformResources(
  source: string
): readonly TerraformResource[] {
  const resources: TerraformResource[] = [];
  const pattern = /resource\s+"([^"]+)"\s+"([^"]+)"\s*\{/g;
  for (const match of source.matchAll(pattern)) {
    const offset = match.index;
    const start = offset + match[0].lastIndexOf('{');
    const end = blockEnd(source, start);
    if (end === -1) {
      throw new CoreError(
        'ValidationFailed',
        'Terraform resource block is not closed'
      );
    }
    resources.push({
      type: match[1] ?? '',
      name: match[2] ?? '',
      body: source.slice(start + 1, end),
      line: lineAt(source, offset),
    });
  }
  if (resources.length === 0) {
    throw new CoreError(
      'ValidationFailed',
      'Terraform entry has no resource blocks'
    );
  }
  return resources;
}

export function terraformString(
  body: string,
  property: string
): string | undefined {
  const match = new RegExp(`(?:^|\\n)\\s*${property}\\s*=\\s*"([^"]+)"`).exec(
    body
  );
  return match?.[1];
}

export function terraformNumber(
  body: string,
  property: string
): number | undefined {
  const match = new RegExp(`(?:^|\\n)\\s*${property}\\s*=\\s*(\\d+)`).exec(
    body
  );
  return match?.[1] === undefined ? undefined : Number(match[1]);
}
