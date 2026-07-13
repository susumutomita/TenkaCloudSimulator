import { createHash } from 'node:crypto';
import { readdir, realpath, stat } from 'node:fs/promises';
import { isAbsolute, relative, resolve, sep } from 'node:path';
import { CatalogScannerError } from './errors.ts';
import type { SourceLocation } from './model.ts';

export interface SourceDigest {
  path: string;
  digest: string;
}

export function portableRelativePath(root: string, path: string): string {
  return relative(root, path).split(sep).join('/');
}

export function lineForToken(contents: string, token: string): number {
  const index = contents.indexOf(token);
  if (index < 0) return 1;
  return contents.slice(0, index).split(/\r?\n/).length;
}

export function sourceLocation(
  path: string,
  contents: string,
  token: string,
  jsonPointer: string | null
): SourceLocation {
  return {
    path,
    line: lineForToken(contents, token),
    jsonPointer,
  };
}

export function contentDigest(
  path: string,
  contents: string | Uint8Array
): SourceDigest {
  return {
    path,
    digest: createHash('sha256').update(contents).digest('hex'),
  };
}

export function combinedDigest(sources: SourceDigest[]): string {
  const canonical = [...sources]
    .sort((left, right) => left.path.localeCompare(right.path))
    .map((source) => `${source.path}\u0000${source.digest}`)
    .join('\u0000');
  return createHash('sha256').update(canonical).digest('hex');
}

export async function walkFiles(root: string): Promise<string[]> {
  const rootStats = await stat(root).catch(() => undefined);
  if (rootStats === undefined || !rootStats.isDirectory()) {
    throw new CatalogScannerError(
      'INVALID_CATALOG_ROOT',
      `catalog root is not a directory: ${root}`
    );
  }
  return walkDirectory(root);
}

async function walkDirectory(directory: string): Promise<string[]> {
  const entries = await readdir(directory, { withFileTypes: true });
  const files: string[] = [];
  for (const entry of entries.sort((left, right) =>
    left.name.localeCompare(right.name)
  )) {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await walkDirectory(path)));
    } else if (entry.isFile()) {
      files.push(path);
    }
  }
  return files;
}

export async function resolveProblemEntry(
  problemDirectory: string,
  entry: string
): Promise<
  { status: 'ok'; path: string } | { status: 'outside' } | { status: 'missing' }
> {
  const candidate = resolve(problemDirectory, entry);
  const lexicalRelative = relative(problemDirectory, candidate);
  if (
    isAbsolute(lexicalRelative) ||
    lexicalRelative === '..' ||
    lexicalRelative.startsWith(`..${sep}`)
  ) {
    return { status: 'outside' };
  }
  const candidateStats = await stat(candidate).catch(() => undefined);
  if (candidateStats === undefined) return { status: 'missing' };
  const [problemRealPath, candidateRealPath] = await Promise.all([
    realpath(problemDirectory),
    realpath(candidate),
  ]);
  const physicalRelative = relative(problemRealPath, candidateRealPath);
  if (
    isAbsolute(physicalRelative) ||
    physicalRelative === '..' ||
    physicalRelative.startsWith(`..${sep}`)
  ) {
    return { status: 'outside' };
  }
  return { status: 'ok', path: candidate };
}
