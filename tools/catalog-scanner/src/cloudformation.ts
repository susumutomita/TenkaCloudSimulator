import { isMap, isScalar, isSeq, LineCounter, parseDocument } from 'yaml';
import type {
  Diagnostic,
  Fidelity,
  NormalizedTarget,
  Plane,
  Requirement,
} from './model.ts';
import { createRequirement } from './requirements.ts';

interface ResourceBlock {
  logicalId: string;
  resourceType: string;
  typeLine: number;
  node: unknown;
}

export interface IaCParseResult {
  requirements: Requirement[];
  diagnostics: Diagnostic[];
}

const NETWORK_RESOURCE_MARKERS = [
  'AWS::EC2::VPC',
  'AWS::EC2::Subnet',
  'AWS::EC2::InternetGateway',
  'AWS::EC2::VPCGatewayAttachment',
  'AWS::EC2::Route',
  'AWS::EC2::RouteTable',
  'AWS::EC2::SubnetRouteTableAssociation',
  'AWS::EC2::SecurityGroup',
  'AWS::ElasticLoadBalancingV2::',
  'AWS::WAFv2::',
] as const;

function cloudFormationService(resourceType: string): string {
  if (
    resourceType.startsWith('Custom::') ||
    resourceType === 'AWS::CloudFormation::CustomResource'
  ) {
    return 'cloudformation';
  }
  const segment =
    resourceType.split('::')[1]?.toLowerCase() ?? 'cloudformation';
  if (segment === 'elasticloadbalancingv2') return 'elasticloadbalancing';
  return segment;
}

function resourceFidelity(resourceType: string): Fidelity {
  if (
    resourceType.startsWith('Custom::') ||
    resourceType === 'AWS::CloudFormation::CustomResource'
  ) {
    return 'L4';
  }
  if (resourceType.startsWith('AWS::IAM::')) return 'L2';
  if (
    NETWORK_RESOURCE_MARKERS.some((marker) => resourceType.startsWith(marker))
  )
    return 'L3';
  return 'L1';
}

function actionFidelity(service: string, operation: string): Fidelity {
  if (service === 'cloudshell') return 'L0';
  if (service === 'iam' || service === 'sts') return 'L2';
  if (service === 'elasticloadbalancing' || service === 'wafv2') return 'L3';
  if (
    service === 'ec2' &&
    /(SecurityGroup|Route|Network|Subnet|Vpc|VPC)/.test(operation)
  ) {
    return 'L3';
  }
  if (service === 'lambda' && operation.startsWith('Invoke')) return 'L4';
  if (service === 's3' && /(Object|Bucket)/.test(operation)) return 'L4';
  if (service === 'ssm' && /(Session|Command)/.test(operation)) return 'L4';
  if (service === 'ssm') return 'L2';
  return 'L1';
}

function resourcePlane(resource: ResourceBlock, service: string): Plane {
  if (service === 'cloudshell' || service === 'sts') return 'access';
  if (resource.logicalId === 'ParticipantViewerRole') return 'participant';
  if (resource.resourceType === 'AWS::Lambda::Permission') return 'deploy';
  return 'workload';
}

function scalarString(value: unknown): string | undefined {
  return isScalar(value) && typeof value.value === 'string'
    ? value.value
    : undefined;
}

function mapValue(value: unknown, key: string): unknown {
  if (!isMap(value)) return undefined;
  for (const pair of value.items) {
    if (scalarString(pair.key) === key) return pair.value;
  }
  return undefined;
}

function nodeLine(value: unknown, lineCounter: LineCounter): number {
  const offset =
    isScalar(value) || isMap(value) || isSeq(value)
      ? value.range?.[0]
      : undefined;
  return lineCounter.linePos(offset ?? 0).line;
}

function structuredResources(
  contents: string,
  sourcePath: string,
  target: NormalizedTarget,
  problemId: string
): {
  resources: ResourceBlock[];
  diagnostics: Diagnostic[];
  lineCounter: LineCounter;
} {
  const lineCounter = new LineCounter();
  const document = parseDocument(contents, { lineCounter });
  const parseError = document.errors[0];
  if (parseError !== undefined) {
    return {
      resources: [],
      diagnostics: [
        {
          code: 'INVALID_CLOUDFORMATION',
          message: `CloudFormation YAML is invalid: ${parseError.message}`,
          problemId,
          targetId: target.targetId,
          source: { path: sourcePath, line: 1, jsonPointer: null },
        },
      ],
      lineCounter,
    };
  }
  const resourcesNode = mapValue(document.contents, 'Resources');
  if (!isMap(resourcesNode)) {
    return {
      resources: [],
      diagnostics: [
        {
          code: 'INVALID_CLOUDFORMATION',
          message:
            'CloudFormation template is missing a top-level Resources section',
          problemId,
          targetId: target.targetId,
          source: { path: sourcePath, line: 1, jsonPointer: null },
        },
      ],
      lineCounter,
    };
  }
  const resources: ResourceBlock[] = [];
  for (const pair of resourcesNode.items) {
    const logicalId = scalarString(pair.key);
    const resourceTypeNode = mapValue(pair.value, 'Type');
    const resourceType = scalarString(resourceTypeNode);
    if (logicalId === undefined || resourceType === undefined) continue;
    resources.push({
      logicalId,
      resourceType,
      typeLine: nodeLine(resourceTypeNode, lineCounter),
      node: pair.value,
    });
  }
  return { resources, diagnostics: [], lineCounter };
}

interface ActionLiteral {
  service: string;
  operation: string;
  line: number;
}

function actionLiterals(
  value: unknown,
  lineCounter: LineCounter
): ActionLiteral[] {
  const candidates = isSeq(value) ? value.items : [value];
  return candidates.flatMap((candidate) => {
    const action = scalarString(candidate);
    const match = /^([a-z][a-z0-9-]*):(\*|[A-Z][A-Za-z0-9*]*)$/.exec(
      action ?? ''
    );
    const service = match?.[1];
    const operation = match?.[2];
    return service === undefined || operation === undefined
      ? []
      : [{ service, operation, line: nodeLine(candidate, lineCounter) }];
  });
}

function policyActionLiterals(
  value: unknown,
  lineCounter: LineCounter
): ActionLiteral[] {
  if (isSeq(value)) {
    return value.items.flatMap((item) =>
      policyActionLiterals(item, lineCounter)
    );
  }
  if (!isMap(value)) return [];
  return value.items.flatMap((pair) => {
    const statements =
      scalarString(pair.key) === 'Statement'
        ? isSeq(pair.value)
          ? pair.value.items
          : [pair.value]
        : [];
    return [
      ...statements.flatMap((statement) =>
        actionLiterals(mapValue(statement, 'Action'), lineCounter)
      ),
      ...policyActionLiterals(pair.value, lineCounter),
    ];
  });
}

function resourceActionLiterals(
  resource: ResourceBlock,
  lineCounter: LineCounter
): ActionLiteral[] {
  const actions = policyActionLiterals(resource.node, lineCounter);
  if (resource.resourceType === 'AWS::Lambda::Permission') {
    actions.push(
      ...actionLiterals(
        mapValue(mapValue(resource.node, 'Properties'), 'Action'),
        lineCounter
      )
    );
  }
  return actions;
}

function actionRequirements(
  resources: ResourceBlock[],
  lineCounter: LineCounter,
  target: NormalizedTarget,
  problemId: string,
  sourcePath: string
): Requirement[] {
  return resources.flatMap((resource) =>
    resourceActionLiterals(resource, lineCounter).map((action) =>
      createRequirement({
        problemId,
        targetId: target.targetId,
        provider: target.provider,
        engine: target.engine,
        service: action.service,
        resourceType: '*',
        operation: action.operation,
        fidelity: actionFidelity(action.service, action.operation),
        plane: resourcePlane(resource, action.service),
        origin: 'iam-policy',
        classification: 'authorization-inventory',
        source: { path: sourcePath, line: action.line, jsonPointer: null },
      })
    )
  );
}

export function parseCloudFormation(
  contents: string,
  sourcePath: string,
  target: NormalizedTarget,
  problemId: string
): IaCParseResult {
  const structure = structuredResources(
    contents,
    sourcePath,
    target,
    problemId
  );
  if (structure.diagnostics.length > 0) {
    return { requirements: [], diagnostics: structure.diagnostics };
  }
  const requirements = structure.resources.map((resource) =>
    createRequirement({
      problemId,
      targetId: target.targetId,
      provider: target.provider,
      engine: target.engine,
      service: cloudFormationService(resource.resourceType),
      resourceType: resource.resourceType,
      operation: 'lifecycle',
      fidelity: resourceFidelity(resource.resourceType),
      plane: 'deploy',
      origin: 'iac-resource',
      classification: 'binding',
      source: { path: sourcePath, line: resource.typeLine, jsonPointer: null },
    })
  );
  requirements.push(
    ...actionRequirements(
      structure.resources,
      structure.lineCounter,
      target,
      problemId,
      sourcePath
    )
  );
  return { requirements, diagnostics: [] };
}
