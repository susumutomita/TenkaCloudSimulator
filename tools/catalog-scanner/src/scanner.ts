import { readFile, stat } from 'node:fs/promises';
import { dirname, extname } from 'node:path';
import { parseCloudFormation } from './cloudformation.ts';
import { errorMessage } from './errors.ts';
import { parseMetadataRequirements } from './metadata.ts';
import type {
  CatalogInventory,
  Diagnostic,
  NormalizedTarget,
  ProblemInventory,
  Requirement,
} from './model.ts';
import { scanSimulationOverlay } from './overlay.ts';
import { compareRequirements } from './requirements.ts';
import { normalizeRuntime } from './runtime.ts';
import {
  combinedDigest,
  contentDigest,
  portableRelativePath,
  resolveProblemEntry,
  type SourceDigest,
  sourceLocation,
  walkFiles,
} from './source.ts';
import { parseTerraform, type TerraformSource } from './terraform.ts';
import { isRecord } from './value.ts';

interface ProblemScanResult {
  problem: ProblemInventory | null;
  requirements: Requirement[];
  diagnostics: Diagnostic[];
  sources: SourceDigest[];
}

interface ScanContext {
  catalogRoot: string;
  problemDirectory: string;
  problemId: string;
  metadataPath: string;
  metadataContents: string;
}

function diagnosticForEntry(
  context: ScanContext,
  target: NormalizedTarget,
  code: 'ENTRY_OUTSIDE_PROBLEM' | 'MISSING_ENTRY' | 'INVALID_CLOUDFORMATION',
  message: string
): Diagnostic {
  return {
    code,
    message,
    problemId: context.problemId,
    targetId: target.targetId,
    source: sourceLocation(
      context.metadataPath,
      context.metadataContents,
      `"entry"`,
      target.targetId === 'default'
        ? '/runtime/entry'
        : `/runtime/targets/${target.targetId}/entry`
    ),
  };
}

async function readSource(
  catalogRoot: string,
  absolutePath: string
): Promise<{ path: string; contents: string; digest: SourceDigest }> {
  const path = portableRelativePath(catalogRoot, absolutePath);
  const contents = await readFile(absolutePath, 'utf8');
  return { path, contents, digest: contentDigest(path, contents) };
}

async function scanCloudFormationTarget(
  context: ScanContext,
  target: NormalizedTarget,
  absoluteEntry: string
): Promise<{
  requirements: Requirement[];
  diagnostics: Diagnostic[];
  sources: SourceDigest[];
}> {
  const entryStats = await stat(absoluteEntry);
  if (
    !entryStats.isFile() ||
    !['.yaml', '.yml'].includes(extname(absoluteEntry).toLowerCase())
  ) {
    return {
      requirements: [],
      diagnostics: [
        diagnosticForEntry(
          context,
          target,
          'INVALID_CLOUDFORMATION',
          'CloudFormation entry must be a YAML file'
        ),
      ],
      sources: [],
    };
  }
  const source = await readSource(context.catalogRoot, absoluteEntry);
  const parsed = parseCloudFormation(
    source.contents,
    source.path,
    target,
    context.problemId
  );
  return { ...parsed, sources: [source.digest] };
}

async function terraformSources(
  catalogRoot: string,
  absoluteEntry: string
): Promise<{ parsedSources: TerraformSource[]; digests: SourceDigest[] }> {
  const entryStats = await stat(absoluteEntry);
  const files = entryStats.isDirectory()
    ? (await walkFiles(absoluteEntry)).filter((path) => extname(path) === '.tf')
    : extname(absoluteEntry) === '.tf'
      ? [absoluteEntry]
      : [];
  const parsedSources: TerraformSource[] = [];
  const digests: SourceDigest[] = [];
  for (const file of files.sort()) {
    const source = await readSource(catalogRoot, file);
    parsedSources.push({ path: source.path, contents: source.contents });
    digests.push(source.digest);
  }
  return { parsedSources, digests };
}

async function scanTerraformTarget(
  context: ScanContext,
  target: NormalizedTarget,
  absoluteEntry: string
): Promise<{
  requirements: Requirement[];
  diagnostics: Diagnostic[];
  sources: SourceDigest[];
}> {
  const sources = await terraformSources(context.catalogRoot, absoluteEntry);
  const parsed = parseTerraform(
    sources.parsedSources,
    target,
    context.problemId
  );
  return { ...parsed, sources: sources.digests };
}

async function scanContainerEntry(
  catalogRoot: string,
  absoluteEntry: string
): Promise<SourceDigest[]> {
  const entryStats = await stat(absoluteEntry);
  if (!entryStats.isFile()) return [];
  return [(await readSource(catalogRoot, absoluteEntry)).digest];
}

async function scanTarget(
  context: ScanContext,
  target: NormalizedTarget
): Promise<{
  requirements: Requirement[];
  diagnostics: Diagnostic[];
  sources: SourceDigest[];
}> {
  const resolved = await resolveProblemEntry(
    context.problemDirectory,
    target.entry
  );
  if (resolved.status === 'outside') {
    return {
      requirements: [],
      diagnostics: [
        diagnosticForEntry(
          context,
          target,
          'ENTRY_OUTSIDE_PROBLEM',
          `runtime entry escapes the problem directory: ${target.entry}`
        ),
      ],
      sources: [],
    };
  }
  if (resolved.status === 'missing') {
    return {
      requirements: [],
      diagnostics: [
        diagnosticForEntry(
          context,
          target,
          'MISSING_ENTRY',
          `runtime entry does not exist: ${target.entry}`
        ),
      ],
      sources: [],
    };
  }
  if (target.delivery === 'container') {
    return {
      requirements: [],
      diagnostics: [],
      sources: await scanContainerEntry(context.catalogRoot, resolved.path),
    };
  }
  if (target.provider === 'aws' && target.engine === 'cloudformation') {
    return scanCloudFormationTarget(context, target, resolved.path);
  }
  return scanTerraformTarget(context, target, resolved.path);
}

function invalidJsonDiagnostic(path: string, message: string): Diagnostic {
  return {
    code: 'INVALID_METADATA_JSON',
    message: `metadata JSON is invalid: ${message}`,
    problemId: null,
    targetId: null,
    source: { path, line: 1, jsonPointer: null },
  };
}

async function scanProblemMetadata(
  catalogRoot: string,
  absoluteMetadataPath: string
): Promise<ProblemScanResult> {
  const metadataPath = portableRelativePath(catalogRoot, absoluteMetadataPath);
  const metadataContents = await readFile(absoluteMetadataPath, 'utf8');
  const sources = [contentDigest(metadataPath, metadataContents)];
  let parsed: unknown;
  try {
    parsed = JSON.parse(metadataContents);
  } catch (error) {
    return {
      problem: null,
      requirements: [],
      diagnostics: [invalidJsonDiagnostic(metadataPath, errorMessage(error))],
      sources,
    };
  }
  if (!isRecord(parsed)) {
    return {
      problem: null,
      requirements: [],
      diagnostics: [
        {
          code: 'INVALID_METADATA',
          message: 'metadata root must be an object',
          problemId: null,
          targetId: null,
          source: { path: metadataPath, line: 1, jsonPointer: null },
        },
      ],
      sources,
    };
  }
  const problemDirectory = dirname(absoluteMetadataPath);
  const normalized = normalizeRuntime({
    metadata: parsed,
    metadataPath,
    metadataContents,
    problemDirectory,
  });
  if (normalized.problemId === null) {
    return {
      problem: null,
      requirements: [],
      diagnostics: normalized.diagnostics,
      sources,
    };
  }
  const context: ScanContext = {
    catalogRoot,
    problemDirectory,
    problemId: normalized.problemId,
    metadataPath,
    metadataContents,
  };
  const requirements: Requirement[] = [];
  const diagnostics = [...normalized.diagnostics];
  for (const target of normalized.targets) {
    const targetScan = await scanTarget(context, target);
    requirements.push(...targetScan.requirements);
    diagnostics.push(...targetScan.diagnostics);
    sources.push(...targetScan.sources);
  }
  const metadataScan = parseMetadataRequirements({
    metadata: parsed,
    contents: metadataContents,
    path: metadataPath,
    problemId: normalized.problemId,
    targets: normalized.targets,
  });
  requirements.push(...metadataScan.requirements);
  diagnostics.push(...metadataScan.diagnostics);
  const overlayScan = await scanSimulationOverlay({
    catalogRoot,
    problemDirectory,
    problemId: normalized.problemId,
    metadata: parsed,
    metadataContents,
    metadataPath,
    targets: normalized.targets,
  });
  requirements.push(...overlayScan.requirements);
  diagnostics.push(...overlayScan.diagnostics);
  sources.push(...overlayScan.sources);
  return {
    problem: {
      problemId: normalized.problemId,
      category: normalized.category,
      status: normalized.status,
      metadataPath,
      targets: normalized.targets,
    },
    requirements,
    diagnostics,
    sources,
  };
}

function compareDiagnostics(left: Diagnostic, right: Diagnostic): number {
  return [
    left.source.path,
    String(left.source.line).padStart(10, '0'),
    left.code,
    left.problemId ?? '',
    left.targetId ?? '',
    left.message,
  ]
    .join('|')
    .localeCompare(
      [
        right.source.path,
        String(right.source.line).padStart(10, '0'),
        right.code,
        right.problemId ?? '',
        right.targetId ?? '',
        right.message,
      ].join('|')
    );
}

function isProblemMetadata(catalogRoot: string, path: string): boolean {
  const relativePath = portableRelativePath(catalogRoot, path);
  return /^(battles|challenges)\/[^/]+\/metadata\.json$/.test(relativePath);
}

export async function collectCatalog(
  catalogRoot: string
): Promise<CatalogInventory> {
  const allFiles = await walkFiles(catalogRoot);
  const metadataFiles = allFiles
    .filter((path) => isProblemMetadata(catalogRoot, path))
    .sort();
  if (metadataFiles.length === 0) {
    return {
      schemaVersion: '1',
      catalogHash: combinedDigest([]),
      problems: [],
      requirements: [],
      diagnostics: [
        {
          code: 'NO_PROBLEMS',
          message:
            'catalog has no battles/*/metadata.json or challenges/*/metadata.json',
          problemId: null,
          targetId: null,
          source: { path: '.', line: 1, jsonPointer: null },
        },
      ],
    };
  }
  const results = await Promise.all(
    metadataFiles.map((metadataPath) =>
      scanProblemMetadata(catalogRoot, metadataPath)
    )
  );
  const problems = results
    .flatMap((result) => (result.problem === null ? [] : [result.problem]))
    .sort((left, right) => left.problemId.localeCompare(right.problemId));
  const requirements = results
    .flatMap((result) => result.requirements)
    .sort(compareRequirements);
  const diagnostics = results
    .flatMap((result) => result.diagnostics)
    .sort(compareDiagnostics);
  const sources = results.flatMap((result) => result.sources);
  return {
    schemaVersion: '1',
    catalogHash: combinedDigest(sources),
    problems,
    requirements,
    diagnostics,
  };
}
