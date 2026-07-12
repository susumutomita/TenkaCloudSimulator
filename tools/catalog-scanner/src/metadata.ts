import type {
  Diagnostic,
  NormalizedTarget,
  Requirement,
  SourceLocation,
} from './model.ts';
import { createRequirement } from './requirements.ts';
import { metadataSource } from './runtime.ts';
import { isRecord, recordValue, stringValue } from './value.ts';

interface MetadataContext {
  metadata: Record<string, unknown>;
  contents: string;
  path: string;
  problemId: string;
  targets: NormalizedTarget[];
}

interface MetadataParseResult {
  requirements: Requirement[];
  diagnostics: Diagnostic[];
}

const KNOWN_SCORING_KINDS = new Set([
  'flag',
  'multi-flag',
  'uptime',
  'uptime-flat',
  'uptime-multi',
  'phased-polling',
  'attack-detection',
  'composite-probe',
  'verify',
  'multi-verify',
]);

function cloudTargets(context: MetadataContext): NormalizedTarget[] {
  return context.targets.filter((target) => target.delivery === 'cloud');
}

function diagnostic(
  context: MetadataContext,
  code: Diagnostic['code'],
  message: string,
  source: SourceLocation,
  targetId: string | null = null
): Diagnostic {
  return { code, message, problemId: context.problemId, targetId, source };
}

function targetForSharedMetadata(
  context: MetadataContext,
  pointer: string,
  diagnostics: Diagnostic[]
): NormalizedTarget | undefined {
  const targets = cloudTargets(context);
  if (targets.length === 1) return targets[0];
  if (targets.length > 1) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_METADATA',
        `${pointer} requires an explicit target for a composite runtime`,
        metadataSource(
          context.path,
          context.contents,
          pointer.slice(1),
          pointer
        )
      )
    );
  }
  return undefined;
}

function endpointRequirements(
  context: MetadataContext,
  diagnostics: Diagnostic[]
): Requirement[] {
  const endpoints = recordValue(context.metadata, 'endpoints');
  if (endpoints === undefined || cloudTargets(context).length === 0) return [];
  if (!Array.isArray(endpoints)) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_METADATA',
        'metadata.endpoints must be an array',
        metadataSource(
          context.path,
          context.contents,
          '"endpoints"',
          '/endpoints'
        )
      )
    );
    return [];
  }
  const target = targetForSharedMetadata(context, '/endpoints', diagnostics);
  if (target === undefined) return [];
  const requirements: Requirement[] = [];
  endpoints.forEach((endpoint, index) => {
    if (!isRecord(endpoint) || stringValue(endpoint, 'slot') === undefined) {
      diagnostics.push(
        diagnostic(
          context,
          'INVALID_METADATA',
          `metadata.endpoints[${index}] requires slot`,
          metadataSource(
            context.path,
            context.contents,
            '"endpoints"',
            `/endpoints/${index}`
          ),
          target.targetId
        )
      );
      return;
    }
    requirements.push(
      createRequirement({
        problemId: context.problemId,
        targetId: target.targetId,
        provider: target.provider,
        engine: target.engine,
        service: 'runtime',
        resourceType: 'Runtime::Endpoint',
        operation: 'ResolveEndpoint',
        fidelity: 'L1',
        plane: 'scoring',
        origin: 'metadata-endpoint',
        classification: 'binding',
        source: metadataSource(
          context.path,
          context.contents,
          `"slot": "${stringValue(endpoint, 'slot')}"`,
          `/endpoints/${index}`
        ),
      })
    );
  });
  return requirements;
}

function httpRequirement(
  context: MetadataContext,
  target: NormalizedTarget,
  operation: 'Probe' | 'Poll' | 'AttackProbe',
  pointer: string
): Requirement {
  return createRequirement({
    problemId: context.problemId,
    targetId: target.targetId,
    provider: target.provider,
    engine: target.engine,
    service: 'http',
    resourceType: 'HTTP::Endpoint',
    operation,
    fidelity: 'L4',
    plane: operation === 'AttackProbe' ? 'operator' : 'scoring',
    origin:
      operation === 'AttackProbe' ? 'metadata-disruption' : 'metadata-probe',
    classification: 'binding',
    source: metadataSource(
      context.path,
      context.contents,
      pointer.slice(1),
      pointer
    ),
  });
}

function repeatedProbeRequirements(
  context: MetadataContext,
  scoring: Record<string, unknown>,
  field: string,
  diagnostics: Diagnostic[]
): Requirement[] {
  const values = scoring[field];
  if (values === undefined) return [];
  if (!Array.isArray(values)) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_METADATA',
        `scoring.${field} must be an array`,
        metadataSource(
          context.path,
          context.contents,
          `"${field}"`,
          `/scoring/${field}`
        )
      )
    );
    return [];
  }
  const target = targetForSharedMetadata(
    context,
    `/scoring/${field}`,
    diagnostics
  );
  if (target === undefined) return [];
  return values.map((_value, index) =>
    httpRequirement(
      context,
      target,
      field === 'attackProbes' ? 'AttackProbe' : 'Probe',
      `/scoring/${field}/${index}`
    )
  );
}

function compositeProbeRequirements(
  context: MetadataContext,
  scoring: Record<string, unknown>,
  diagnostics: Diagnostic[]
): Requirement[] {
  const targets = recordValue(scoring, 'targets');
  if (!Array.isArray(targets)) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_METADATA',
        'composite-probe scoring requires targets',
        metadataSource(
          context.path,
          context.contents,
          '"scoring"',
          '/scoring/targets'
        )
      )
    );
    return [];
  }
  const requirements: Requirement[] = [];
  targets.forEach((value, index) => {
    const targetId = isRecord(value)
      ? stringValue(value, 'targetId')
      : undefined;
    const target = context.targets.find(
      (candidate) => candidate.targetId === targetId
    );
    if (target === undefined) {
      diagnostics.push(
        diagnostic(
          context,
          'INVALID_METADATA',
          `scoring target ${targetId ?? '(missing)'} does not exist`,
          metadataSource(
            context.path,
            context.contents,
            '"targets"',
            `/scoring/targets/${index}`
          ),
          targetId ?? null
        )
      );
      return;
    }
    requirements.push(
      httpRequirement(context, target, 'Probe', `/scoring/targets/${index}`)
    );
  });
  return requirements;
}

function scoringRequirements(
  context: MetadataContext,
  diagnostics: Diagnostic[]
): Requirement[] {
  const value = recordValue(context.metadata, 'scoring');
  if (value === undefined || cloudTargets(context).length === 0) return [];
  if (!isRecord(value)) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_METADATA',
        'metadata.scoring must be an object',
        metadataSource(context.path, context.contents, '"scoring"', '/scoring')
      )
    );
    return [];
  }
  const kind = stringValue(value, 'kind');
  if (kind === undefined || !KNOWN_SCORING_KINDS.has(kind)) {
    diagnostics.push(
      diagnostic(
        context,
        'UNKNOWN_SCORING_KIND',
        `unknown scoring kind: ${kind ?? '(missing)'}`,
        metadataSource(
          context.path,
          context.contents,
          '"kind"',
          '/scoring/kind'
        )
      )
    );
    return [];
  }
  const requirements: Requirement[] = [];
  if (kind === 'composite-probe') {
    requirements.push(
      ...compositeProbeRequirements(context, value, diagnostics)
    );
  } else if (kind === 'uptime-flat' || kind === 'uptime') {
    requirements.push(
      ...repeatedProbeRequirements(context, value, 'endpoints', diagnostics)
    );
  } else if (kind === 'uptime-multi') {
    requirements.push(
      ...repeatedProbeRequirements(context, value, 'probedSlots', diagnostics)
    );
  } else if (kind === 'phased-polling') {
    const target = targetForSharedMetadata(
      context,
      '/scoring/probe',
      diagnostics
    );
    if (target !== undefined)
      requirements.push(
        httpRequirement(context, target, 'Poll', '/scoring/probe')
      );
  }
  requirements.push(
    ...repeatedProbeRequirements(context, value, 'attackProbes', diagnostics)
  );
  return requirements;
}

function disruptionActionRequirement(
  context: MetadataContext,
  target: NormalizedTarget,
  kind: string,
  index: number
): Requirement | undefined {
  const common = {
    problemId: context.problemId,
    targetId: target.targetId,
    provider: target.provider,
    engine: target.engine,
    plane: 'operator' as const,
    origin: 'metadata-disruption' as const,
    classification: 'binding' as const,
    source: metadataSource(
      context.path,
      context.contents,
      `"kind": "${kind}"`,
      `/disruptions/${index}/action`
    ),
  };
  if (kind === 'ssm-run-command') {
    return createRequirement({
      ...common,
      service: 'ssm',
      resourceType: 'AWS::EC2::Instance',
      operation: 'SendCommand',
      fidelity: 'L4',
    });
  }
  if (kind === 'lambda-invoke') {
    return createRequirement({
      ...common,
      service: 'lambda',
      resourceType: 'AWS::Lambda::Function',
      operation: 'InvokeFunction',
      fidelity: 'L4',
    });
  }
  if (kind === 'cfn-stack-update') {
    return createRequirement({
      ...common,
      service: 'cloudformation',
      resourceType: 'AWS::CloudFormation::Stack',
      operation: 'UpdateStack',
      fidelity: 'L1',
    });
  }
  return undefined;
}

function disruptionRequirements(
  context: MetadataContext,
  diagnostics: Diagnostic[]
): Requirement[] {
  const disruptions = recordValue(context.metadata, 'disruptions');
  if (disruptions === undefined || cloudTargets(context).length === 0)
    return [];
  if (!Array.isArray(disruptions)) {
    diagnostics.push(
      diagnostic(
        context,
        'INVALID_METADATA',
        'metadata.disruptions must be an array',
        metadataSource(
          context.path,
          context.contents,
          '"disruptions"',
          '/disruptions'
        )
      )
    );
    return [];
  }
  const target = targetForSharedMetadata(context, '/disruptions', diagnostics);
  if (target === undefined) return [];
  const requirements: Requirement[] = [];
  disruptions.forEach((value, index) => {
    if (!isRecord(value)) return;
    const parameters = recordValue(value, 'parameters');
    if (
      isRecord(parameters) &&
      typeof recordValue(parameters, 'probe') === 'string'
    ) {
      requirements.push(
        httpRequirement(
          context,
          target,
          'AttackProbe',
          `/disruptions/${index}/parameters/probe`
        )
      );
    }
    const action = recordValue(value, 'action');
    if (action === undefined) return;
    const kind = isRecord(action) ? stringValue(action, 'kind') : undefined;
    const requirement =
      kind === undefined
        ? undefined
        : disruptionActionRequirement(context, target, kind, index);
    if (requirement !== undefined) {
      requirements.push(requirement);
      return;
    }
    diagnostics.push(
      diagnostic(
        context,
        'UNKNOWN_DISRUPTION_ACTION',
        `unknown disruption action: ${kind ?? '(invalid)'}`,
        metadataSource(
          context.path,
          context.contents,
          '"action"',
          `/disruptions/${index}/action`
        ),
        target.targetId
      )
    );
  });
  return requirements;
}

export function parseMetadataRequirements(
  context: MetadataContext
): MetadataParseResult {
  const diagnostics: Diagnostic[] = [];
  const requirements = [
    ...endpointRequirements(context, diagnostics),
    ...scoringRequirements(context, diagnostics),
    ...disruptionRequirements(context, diagnostics),
  ];
  return { requirements, diagnostics };
}
