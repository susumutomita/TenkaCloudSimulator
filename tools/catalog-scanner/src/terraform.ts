import type {
  Diagnostic,
  Fidelity,
  NormalizedTarget,
  Requirement,
} from './model.ts';
import { createRequirement } from './requirements.ts';

export interface TerraformSource {
  path: string;
  contents: string;
}

export interface TerraformParseResult {
  requirements: Requirement[];
  diagnostics: Diagnostic[];
}

function terraformService(resourceType: string): string {
  if (resourceType.startsWith('google_cloud_run_')) return 'run';
  const parts = resourceType.split('_');
  return parts[1] ?? resourceType;
}

function terraformFidelity(resourceType: string): Fidelity {
  if (resourceType.includes('_iam_')) return 'L2';
  if (/(network|subnet|firewall|load_balancer|web_acl)/.test(resourceType))
    return 'L3';
  if (/(cloud_run|function|instance|container)/.test(resourceType)) return 'L4';
  return 'L1';
}

function mismatchDiagnostic(
  target: NormalizedTarget,
  problemId: string,
  source: TerraformSource,
  line: number,
  resourceType: string
): Diagnostic | undefined {
  if (resourceType.startsWith('google_')) return undefined;
  return {
    code: 'TERRAFORM_PROVIDER_MISMATCH',
    message: `Terraform resource ${resourceType} does not match target provider ${target.provider}`,
    problemId,
    targetId: target.targetId,
    source: { path: source.path, line, jsonPointer: null },
  };
}

export function parseTerraform(
  sources: TerraformSource[],
  target: NormalizedTarget,
  problemId: string
): TerraformParseResult {
  if (sources.length === 0) {
    return {
      requirements: [],
      diagnostics: [
        {
          code: 'INVALID_TERRAFORM',
          message: 'Terraform entry does not contain any .tf files',
          problemId,
          targetId: target.targetId,
          source: { path: target.entry, line: 1, jsonPointer: null },
        },
      ],
    };
  }
  const requirements: Requirement[] = [];
  const diagnostics: Diagnostic[] = [];
  for (const source of sources) {
    const lines = source.contents.split(/\r?\n/);
    for (let index = 0; index < lines.length; index += 1) {
      const resourceType = lines[index]?.match(
        /^\s*resource\s+"([^"]+)"\s+"[^"]+"\s*\{/
      )?.[1];
      if (resourceType === undefined) continue;
      const mismatch = mismatchDiagnostic(
        target,
        problemId,
        source,
        index + 1,
        resourceType
      );
      if (mismatch !== undefined) diagnostics.push(mismatch);
      requirements.push(
        createRequirement({
          problemId,
          targetId: target.targetId,
          provider: target.provider,
          engine: target.engine,
          service: terraformService(resourceType),
          resourceType,
          operation: 'lifecycle',
          fidelity: [terraformFidelity(resourceType)],
          plane: 'deploy',
          origin: 'iac-resource',
          classification: 'binding',
          source: { path: source.path, line: index + 1, jsonPointer: null },
        })
      );
    }
  }
  return { requirements, diagnostics };
}
