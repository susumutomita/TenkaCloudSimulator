import { describe, expect, it } from 'bun:test';
import {
  advanceClockRequestSchema,
  advanceClockResponseSchema,
  assertCapabilityCoverageReport,
  assertCapabilityRequirement,
  assertProblemRuntimeDescriptor,
  assertSimulatorCapabilities,
  assertSimulatorClockAdvanceRequest,
  assertSimulatorClockAdvanceResponse,
  assertSimulatorDeploymentRequest,
  assertSimulatorDeploymentResponse,
  assertSimulatorErrorEnvelope,
  assertSimulatorEvent,
  assertSimulatorEventPage,
  assertSimulatorMaterializeWorkloadsRequest,
  assertSimulatorResourceProjection,
  assertSimulatorSimulationOverlay,
  assertSimulatorSnapshot,
  assertSimulatorWorldRequest,
  assertSimulatorWorldResponse,
  ContractValidationError,
  capabilitiesSchema,
  capabilityReportSchema,
  capabilityRequirementSchema,
  commonSchema,
  createDeploymentRequestSchema,
  createWorldRequestSchema,
  createWorldResponseSchema,
  deploymentResponseSchema,
  errorEnvelopeSchema,
  eventPageSchema,
  eventSchema,
  isCapabilityCoverageReport,
  isCapabilityRequirement,
  isProblemRuntimeDescriptor,
  isSimulatorCapabilities,
  isSimulatorClockAdvanceRequest,
  isSimulatorClockAdvanceResponse,
  isSimulatorDeploymentRequest,
  isSimulatorDeploymentResponse,
  isSimulatorErrorEnvelope,
  isSimulatorEvent,
  isSimulatorEventPage,
  isSimulatorMaterializeWorkloadsRequest,
  isSimulatorResourceProjection,
  isSimulatorSimulationOverlay,
  isSimulatorSnapshot,
  isSimulatorWorldRequest,
  isSimulatorWorldResponse,
  materializeWorkloadsRequestSchema,
  resourceProjectionSchema,
  runtimeSchema,
  SIMULATOR_PROTOCOL_VERSION,
  SIMULATOR_SNAPSHOT_VERSION,
  simulationOverlaySchema,
  snapshotSchema,
} from '../src/index.js';

const HASH = 'a'.repeat(64);

const singleRuntime = {
  provider: 'aws',
  engine: 'cloudformation',
  entry: 'template.yaml',
};

const compositeRuntime = {
  kind: 'composite',
  targets: [
    {
      id: 'aws-main',
      provider: 'aws',
      engine: 'cloudformation',
      entry: 'template.yaml',
    },
    {
      id: 'gcp-edge',
      provider: 'gcp',
      engine: 'infra-manager',
      entry: 'main.yaml',
    },
  ],
};

const capabilities = {
  protocolVersion: SIMULATOR_PROTOCOL_VERSION,
  simulatorVersion: '0.1.0',
  providers: {
    aws: {
      engines: {
        cloudformation: {
          operations: ['deploy', 'delete', 'get'],
          resources: ['AWS::IAM::Role'],
          fidelity: ['contract', 'control', 'security'],
          constraints: { maxResources: 200 },
        },
      },
    },
  },
  capabilities: [
    {
      provider: 'aws',
      engine: 'cloudformation',
      service: 'iam',
      resourceType: 'AWS::IAM::Role',
      operation: 'CreateRole',
      fidelity: ['contract', 'control', 'security'],
    },
  ],
  constraints: { maxWorlds: 32 },
};

const worldRequest = {
  tenantId: 'local',
  eventId: 'event-1',
  teamId: 'team-1',
  deploymentId: 'deployment-1',
  seed: 'stable-seed',
  virtualClock: '2026-07-11T00:00:00.000Z',
};

const worldResponse = {
  worldId: 'world-1',
  consoleUrl: 'http://127.0.0.1:7777/console/world-1',
};

const clockAdvanceRequest = { milliseconds: 1_500 };

const clockAdvanceResponse = {
  clock: '2026-07-11T00:00:01.500Z',
  appliedTransitions: [
    { provider: 'aws', transitionId: 'transition-revert-command-1' },
  ],
};

const deploymentRequest = {
  problemId: 'hello-multicloud',
  runtime: compositeRuntime,
  templateBody: 'resources: []\n',
  metadata: { locale: 'ja', attempt: 1 },
  simulationOverlay: {
    schemaVersion: '1',
    requirements: [
      {
        targetId: 'aws-main',
        service: 'http',
        resourceType: 'HTTP::Endpoint',
        operation: 'Probe',
        fidelity: 'L4',
        plane: 'scoring',
      },
    ],
  },
};

const deploymentResponse = {
  deploymentId: 'deployment-1',
  status: 'running',
  outputs: {
    'aws-main.RoleArn': 'arn:aws:iam::000000000000:role/simulated',
    'gcp-edge.Url': 'http://127.0.0.1:7777/workloads/gcp-edge',
  },
  targets: [
    {
      id: 'aws-main',
      provider: 'aws',
      engine: 'cloudformation',
      status: 'running',
      outputs: { RoleArn: 'arn:aws:iam::000000000000:role/simulated' },
    },
    {
      id: 'gcp-edge',
      provider: 'gcp',
      engine: 'infra-manager',
      status: 'running',
      outputs: { Url: 'http://127.0.0.1:7777/workloads/gcp-edge' },
    },
  ],
  diagnostics: [],
};

const errorEnvelope = {
  error: {
    code: 'UnsupportedCapability',
    message: 'deployment requires unavailable simulator capabilities',
    requestId: 'request-1',
    retryable: false,
    diagnostics: [
      {
        code: 'CAPABILITY_MISSING',
        message: 'CreateRole is unavailable',
        provider: 'aws',
        engine: 'cloudformation',
        service: 'iam',
        resourceType: 'AWS::IAM::Role',
        operation: 'CreateRole',
        requiredFidelity: ['contract', 'control', 'security'],
        availableFidelity: ['contract', 'control'],
        source: { file: 'template.yaml', line: 4, column: 5 },
      },
    ],
  },
};

const simulatorEvent = {
  worldId: 'world-1',
  sequence: 1,
  virtualTimestamp: '2026-07-11T00:00:00.000Z',
  command: {
    id: 'command-1',
    deploymentId: 'deployment-1',
    provider: 'aws',
    operation: 'CreateRole',
    idempotencyKey: 'deployment-1:create-role',
  },
  type: 'ResourceCreated',
  schemaVersion: '1',
  payloadHash: HASH,
  payload: { resourceId: 'role-1', enabled: true },
};

const eventPage = {
  events: [simulatorEvent],
  nextCursor: 1,
};

const resourceProjection = {
  resources: [
    {
      worldId: 'world-1',
      deploymentId: 'deployment-1',
      targetId: 'default',
      provider: 'aws',
      resourceType: 'AWS::IAM::Role',
      resourceId: 'role-1',
      properties: { name: 'simulated-role', enabled: true },
      status: 'ready',
    },
  ],
};

const simulatorSnapshot = {
  snapshotVersion: SIMULATOR_SNAPSHOT_VERSION,
  protocolVersion: SIMULATOR_PROTOCOL_VERSION,
  worldId: 'world-1',
  namespace: {
    tenantId: 'local',
    eventId: 'event-1',
    teamId: 'team-1',
  },
  seed: 'stable-seed',
  clock: '2026-07-11T00:00:00.000Z',
  lastSequence: 1,
  resourceGraph: { nodes: [{ id: 'role-1' }], edges: [] },
  providerProjections: { aws: { roles: [{ id: 'role-1' }] } },
  scheduledTransitions: [{ at: '2026-07-11T00:01:00.000Z' }],
  hash: `sha256:${HASH}`,
};

const requirement = {
  problemId: 'aws-iam',
  targetId: 'aws-main',
  provider: 'aws',
  engine: 'cloudformation',
  entry: 'template.yaml',
  service: 'iam',
  resourceType: 'AWS::IAM::Role',
  operation: 'CreateRole',
  requiredFidelity: ['contract', 'control', 'security'],
  plane: 'deploy',
  source: {
    kind: 'iac-resource',
    location: { file: 'template.yaml', line: 4 },
  },
};

const coverageReport = {
  protocolVersion: SIMULATOR_PROTOCOL_VERSION,
  simulatorVersion: '0.1.0',
  catalogCommit: 'a'.repeat(40),
  reportHash: HASH,
  supported: true,
  summary: {
    total: 1,
    covered: 1,
    missing: 0,
    insufficient: 0,
    invalid: 0,
  },
  requirements: [
    {
      requirement,
      status: 'covered',
      implementedFidelity: ['contract', 'control', 'security'],
      diagnostics: [],
    },
  ],
};

describe('公開 JSON Schema', () => {
  it('すべての schema を安定した v1 ID で公開する', () => {
    expect(
      [
        commonSchema,
        runtimeSchema,
        simulationOverlaySchema,
        capabilitiesSchema,
        createWorldRequestSchema,
        createWorldResponseSchema,
        advanceClockRequestSchema,
        advanceClockResponseSchema,
        createDeploymentRequestSchema,
        deploymentResponseSchema,
        errorEnvelopeSchema,
        eventSchema,
        eventPageSchema,
        materializeWorkloadsRequestSchema,
        resourceProjectionSchema,
        snapshotSchema,
        capabilityRequirementSchema,
        capabilityReportSchema,
      ].map((schema) => schema.$id)
    ).toEqual([
      'https://schemas.tenkacloud.dev/simulator/v1/common.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/runtime.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/simulation-overlay.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/capabilities.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/create-world-request.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/create-world-response.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/advance-clock-request.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/advance-clock-response.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/create-deployment-request.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/deployment-response.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/error-envelope.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/event.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/event-page.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/materialize-workloads-request.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/resource-projection.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/snapshot.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/capability-requirement.schema.json',
      'https://schemas.tenkacloud.dev/simulator/v1/capability-report.schema.json',
    ]);
  });

  it('OpenAPI が lifecycle と共有 projection API を参照する', async () => {
    const text = await Bun.file(
      new URL('../openapi.yaml', import.meta.url)
    ).text();
    const document = Bun.YAML.parse(text);

    expect(document).toMatchObject({
      openapi: '3.1.0',
      paths: {
        '/v1/capabilities': { get: { operationId: 'getCapabilities' } },
        '/v1/worlds': { post: { operationId: 'createWorld' } },
        '/v1/worlds/{worldId}/deployments': {
          post: { operationId: 'createDeployment' },
        },
        '/v1/worlds/{worldId}/deployments/{deploymentId}': {
          get: { operationId: 'getDeployment' },
        },
        '/v1/worlds/{worldId}/workloads/materialize': {
          post: { operationId: 'materializeWorkloads' },
        },
        '/v1/worlds/{worldId}/data-plane/{provider}/{targetId}/{path}': {
          get: {
            operationId: 'proxyDataPlaneRequest',
            security: [{ LaunchToken: [] }],
          },
          'x-tenkacloud-all-methods': true,
        },
        '/v1/worlds/{worldId}': {
          delete: { operationId: 'deleteWorld' },
        },
        '/v1/worlds/{worldId}/clock/advance': {
          post: { operationId: 'advanceWorldClock' },
        },
        '/v1/worlds/{worldId}/resources': {
          get: { operationId: 'listResources' },
        },
        '/v1/worlds/{worldId}/events': {
          get: { operationId: 'listEvents' },
        },
        '/v1/worlds/{worldId}/events/stream': {
          get: { operationId: 'streamEvents' },
        },
        '/v1/worlds/{worldId}/snapshots': {
          get: { operationId: 'exportSnapshot' },
          post: { operationId: 'restoreSnapshot' },
        },
        '/v1/worlds/{worldId}/providers/{provider}/operations/{operation}': {
          post: { operationId: 'executeProviderCommand' },
        },
      },
      components: {
        securitySchemes: {
          LaunchToken: { type: 'http', scheme: 'bearer' },
        },
      },
    });
    expect(text.match(/operationId:/g)).toHaveLength(14);
    expect(text).toContain('./schemas/error-envelope.schema.json');
    expect(text).toContain('./schemas/event-page.schema.json');
    expect(text).toContain('./schemas/resource-projection.schema.json');
  });
});

describe('runtime と lifecycle 契約', () => {
  it('single runtime と Composite の 2 件から 8 件を受理する', () => {
    const eightTargets = {
      kind: 'composite',
      targets: Array.from({ length: 8 }, (_, index) => ({
        id: `target-${index}`,
        provider: 'aws',
        engine: 'cloudformation',
        entry: `target-${index}.yaml`,
      })),
    };

    expect(isProblemRuntimeDescriptor(singleRuntime)).toBe(true);
    expect(isProblemRuntimeDescriptor(compositeRuntime)).toBe(true);
    expect(isProblemRuntimeDescriptor(eightTargets)).toBe(true);
    assertProblemRuntimeDescriptor(singleRuntime);
  });

  it('Composite の件数超過、重複 ID、不正 target を拒否する', () => {
    const duplicateIds = {
      kind: 'composite',
      targets: [
        compositeRuntime.targets[0],
        { ...compositeRuntime.targets[1], id: 'aws-main' },
      ],
    };
    const nestedTarget = {
      kind: 'composite',
      targets: [null, compositeRuntime.targets[1]],
    };
    const nineTargets = {
      kind: 'composite',
      targets: Array.from({ length: 9 }, (_, index) => ({
        id: `target-${index}`,
        provider: 'aws',
        engine: 'cloudformation',
        entry: `target-${index}.yaml`,
      })),
    };

    expect(isProblemRuntimeDescriptor({ kind: 'composite', targets: [] })).toBe(
      false
    );
    expect(isProblemRuntimeDescriptor(duplicateIds)).toBe(false);
    expect(isProblemRuntimeDescriptor(nestedTarget)).toBe(false);
    expect(isProblemRuntimeDescriptor(nineTargets)).toBe(false);
  });

  it('capability の互換 shape と詳細 entry を検証する', () => {
    expect(isSimulatorCapabilities(capabilities)).toBe(true);
    expect(
      isSimulatorCapabilities({ ...capabilities, futureResponseField: true })
    ).toBe(true);
    expect(
      isSimulatorCapabilities({
        ...capabilities,
        protocolVersion: '2026-07-10',
      })
    ).toBe(false);
    expect(
      isSimulatorCapabilities({
        ...capabilities,
        capabilities: capabilities.capabilities.map(
          ({ engine: _engine, ...capability }) => capability
        ),
      })
    ).toBe(false);
    expect(
      isSimulatorCapabilities({
        ...capabilities,
        providers: {
          aws: {
            engines: {
              cloudformation: { operations: ['deploy', 'deploy'] },
            },
          },
        },
      })
    ).toBe(false);
    assertSimulatorCapabilities(capabilities);
  });

  it('world request と response の必須値と形式を検証する', () => {
    expect(isSimulatorWorldRequest(worldRequest)).toBe(true);
    expect(isSimulatorWorldResponse(worldResponse)).toBe(true);
    expect(isSimulatorWorldRequest({ ...worldRequest, teamId: '' })).toBe(
      false
    );
    expect(
      isSimulatorWorldRequest({ ...worldRequest, virtualClock: 'not-a-date' })
    ).toBe(false);
    expect(
      isSimulatorWorldRequest({ ...worldRequest, unexpected: 'field' })
    ).toBe(false);
    expect(
      isSimulatorWorldResponse({ ...worldResponse, consoleUrl: 'relative' })
    ).toBe(false);
    assertSimulatorWorldRequest(worldRequest);
    assertSimulatorWorldResponse(worldResponse);
  });

  it('clock advance の正の安全整数と適用済み transition を検証する', () => {
    expect(isSimulatorClockAdvanceRequest(clockAdvanceRequest)).toBe(true);
    expect(isSimulatorClockAdvanceResponse(clockAdvanceResponse)).toBe(true);
    for (const milliseconds of [0, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      expect(isSimulatorClockAdvanceRequest({ milliseconds })).toBe(false);
    }
    expect(
      isSimulatorClockAdvanceRequest({ ...clockAdvanceRequest, extra: true })
    ).toBe(false);
    expect(
      isSimulatorClockAdvanceResponse({
        ...clockAdvanceResponse,
        appliedTransitions: [{ provider: '', transitionId: 'transition-1' }],
      })
    ).toBe(false);
    expect(
      isSimulatorClockAdvanceResponse({
        ...clockAdvanceResponse,
        clock: 'not-a-date',
      })
    ).toBe(false);
    assertSimulatorClockAdvanceRequest(clockAdvanceRequest);
    assertSimulatorClockAdvanceResponse(clockAdvanceResponse);
  });

  it('single と Composite の deployment request を検証する', () => {
    const singleRequest = {
      ...deploymentRequest,
      runtime: singleRuntime,
      metadata: null,
    };

    expect(isSimulatorDeploymentRequest(singleRequest)).toBe(true);
    expect(isSimulatorDeploymentRequest(deploymentRequest)).toBe(true);
    expect(
      isSimulatorDeploymentRequest({ ...deploymentRequest, templateBody: '' })
    ).toBe(false);
    expect(
      isSimulatorDeploymentRequest({ ...deploymentRequest, unexpected: true })
    ).toBe(false);
    expect(
      isSimulatorDeploymentRequest({
        ...deploymentRequest,
        simulationOverlay: { invalid: undefined },
      })
    ).toBe(false);
    assertSimulatorDeploymentRequest(deploymentRequest);
  });

  it('workload materialize の再試行対象を deployment ID だけで指定する', () => {
    const request = { deploymentId: 'deployment-1' };

    expect(isSimulatorMaterializeWorkloadsRequest(request)).toBe(true);
    expect(
      isSimulatorMaterializeWorkloadsRequest({ ...request, extra: true })
    ).toBe(false);
    expect(isSimulatorMaterializeWorkloadsRequest({ deploymentId: '' })).toBe(
      false
    );
    assertSimulatorMaterializeWorkloadsRequest(request);
  });

  it('simulation overlay は capability と digest固定workloadだけを表現する', () => {
    const overlay = deploymentRequest.simulationOverlay;
    const workloadOverlay = {
      schemaVersion: '1',
      workloads: [
        {
          id: 'api',
          targetId: 'aws-main',
          resourceRef: 'ApiFunction',
          image: `ghcr.io/tenkacloud/api@sha256:${HASH}`,
          command: ['bun', 'run', 'start'],
          containerPort: 3000,
          healthPath: '/healthz',
          artifact: { path: 'services/api/Dockerfile', sha256: HASH },
        },
      ],
    };

    expect(isSimulatorSimulationOverlay(overlay)).toBe(true);
    expect(isSimulatorSimulationOverlay(workloadOverlay)).toBe(true);
    expect(
      isSimulatorSimulationOverlay({
        ...workloadOverlay,
        workloads: [
          { ...workloadOverlay.workloads[0], image: 'ghcr.io/latest' },
        ],
      })
    ).toBe(false);
    for (const forbidden of ['scoring', 'answer', 'secret', 'environment']) {
      expect(
        isSimulatorSimulationOverlay({ ...overlay, [forbidden]: true })
      ).toBe(false);
    }
    expect(
      isSimulatorSimulationOverlay({ schemaVersion: '2', requirements: [] })
    ).toBe(false);
    assertSimulatorSimulationOverlay(overlay);
    assertSimulatorSimulationOverlay(workloadOverlay);
  });

  it('single と target 別 status を持つ Composite response を検証する', () => {
    const singleResponse = {
      deploymentId: 'deployment-1',
      status: 'accepted',
      outputs: {},
    };

    expect(isSimulatorDeploymentResponse(singleResponse)).toBe(true);
    expect(isSimulatorDeploymentResponse(deploymentResponse)).toBe(true);
    expect(
      isSimulatorDeploymentResponse({
        ...deploymentResponse,
        targets: [deploymentResponse.targets[0]],
      })
    ).toBe(false);
    expect(
      isSimulatorDeploymentResponse({
        ...deploymentResponse,
        status: 'unknown',
      })
    ).toBe(false);
    expect(
      isSimulatorDeploymentResponse({
        ...deploymentResponse,
        targets: [
          deploymentResponse.targets[0],
          { ...deploymentResponse.targets[1], id: 'aws-main' },
        ],
      })
    ).toBe(false);
    assertSimulatorDeploymentResponse(deploymentResponse);
  });
});

describe('error、event、snapshot 契約', () => {
  it('共通 error envelope と capability diagnostic を受理する', () => {
    expect(isSimulatorErrorEnvelope(errorEnvelope)).toBe(true);
    expect(
      isSimulatorErrorEnvelope({
        error: { ...errorEnvelope.error, code: 'UnknownFailure' },
      })
    ).toBe(false);
    expect(
      isSimulatorErrorEnvelope({
        error: {
          ...errorEnvelope.error,
          diagnostics: [{ message: 'code がない' }],
        },
      })
    ).toBe(false);
    assertSimulatorErrorEnvelope(errorEnvelope);
  });

  it('event の sequence、時刻、payload hash を検証する', () => {
    expect(isSimulatorEvent(simulatorEvent)).toBe(true);
    expect(isSimulatorEvent({ ...simulatorEvent, sequence: 0 })).toBe(false);
    expect(
      isSimulatorEvent({ ...simulatorEvent, virtualTimestamp: 'today' })
    ).toBe(false);
    expect(isSimulatorEvent({ ...simulatorEvent, payloadHash: 'abc' })).toBe(
      false
    );
    assertSimulatorEvent(simulatorEvent);
  });

  it('event replay page の cursor と上限 100 件を検証する', () => {
    expect(isSimulatorEventPage(eventPage)).toBe(true);
    expect(isSimulatorEventPage({ ...eventPage, nextCursor: -1 })).toBe(false);
    expect(
      isSimulatorEventPage({
        ...eventPage,
        nextCursor: Number.MAX_SAFE_INTEGER + 1,
      })
    ).toBe(false);
    expect(
      isSimulatorEventPage({
        events: Array.from({ length: 101 }, () => simulatorEvent),
        nextCursor: 101,
      })
    ).toBe(false);
    assertSimulatorEventPage(eventPage);
  });

  it('resource projection の JSON property と状態を検証する', () => {
    const resource = resourceProjection.resources[0];
    if (!resource) throw new Error('resource fixture がありません');
    const { targetId: _targetId, ...targetlessResource } = resource;
    expect(isSimulatorResourceProjection(resourceProjection)).toBe(true);
    expect(
      isSimulatorResourceProjection({ resources: [targetlessResource] })
    ).toBe(false);
    expect(
      isSimulatorResourceProjection({
        resources: [{ ...resourceProjection.resources[0], properties: [] }],
      })
    ).toBe(false);
    expect(
      isSimulatorResourceProjection({
        resources: [{ ...resourceProjection.resources[0], status: 'unknown' }],
      })
    ).toBe(false);
    assertSimulatorResourceProjection(resourceProjection);
  });

  it('snapshot の独立 version、namespace、projection を検証する', () => {
    expect(isSimulatorSnapshot(simulatorSnapshot)).toBe(true);
    expect(
      isSimulatorSnapshot({ ...simulatorSnapshot, snapshotVersion: '2' })
    ).toBe(false);
    expect(
      isSimulatorSnapshot({ ...simulatorSnapshot, lastSequence: -1 })
    ).toBe(false);
    expect(
      isSimulatorSnapshot({
        ...simulatorSnapshot,
        namespace: { ...simulatorSnapshot.namespace, deploymentId: 'no' },
      })
    ).toBe(false);
    assertSimulatorSnapshot(simulatorSnapshot);
  });
});

describe('capability coverage 契約', () => {
  it('source location と fidelity 集合を持つ requirement を受理する', () => {
    expect(isCapabilityRequirement(requirement)).toBe(true);
    expect(
      isCapabilityRequirement({
        ...requirement,
        requiredFidelity: ['contract', 'contract'],
      })
    ).toBe(false);
    expect(isCapabilityRequirement({ ...requirement, plane: 'unknown' })).toBe(
      false
    );
    const { engine: _engine, ...enginelessRequirement } = requirement;
    expect(isCapabilityRequirement(enginelessRequirement)).toBe(false);
    assertCapabilityRequirement(requirement);
  });

  it('covered、missing、insufficient、invalid を区別する report を受理する', () => {
    const allStatusesReport = {
      ...coverageReport,
      supported: false,
      summary: {
        total: 4,
        covered: 1,
        missing: 1,
        insufficient: 1,
        invalid: 1,
      },
      requirements: [
        coverageReport.requirements[0],
        {
          ...coverageReport.requirements[0],
          status: 'missing',
          implementedFidelity: [],
        },
        {
          ...coverageReport.requirements[0],
          status: 'insufficient',
          implementedFidelity: ['contract'],
        },
        {
          ...coverageReport.requirements[0],
          status: 'invalid',
          implementedFidelity: [],
        },
      ],
    };

    expect(isCapabilityCoverageReport(coverageReport)).toBe(true);
    expect(isCapabilityCoverageReport(allStatusesReport)).toBe(true);
    expect(
      isCapabilityCoverageReport({ ...coverageReport, catalogCommit: 'main' })
    ).toBe(false);
    expect(
      isCapabilityCoverageReport({
        ...coverageReport,
        summary: { ...coverageReport.summary, missing: -1 },
      })
    ).toBe(false);
    assertCapabilityCoverageReport(coverageReport);
  });
});

describe('assertion validator', () => {
  it('不正な入力を schema 名と Ajv 診断付きの型エラーにする', () => {
    expect(() => assertSimulatorWorldRequest({ teamId: '' })).toThrow(
      ContractValidationError
    );

    let caught: unknown;
    try {
      assertSimulatorWorldRequest({ teamId: '' });
    } catch (error) {
      caught = error;
    }
    expect(caught).toMatchObject({
      name: 'ContractValidationError',
      contractName: 'SimulatorWorldRequest',
    });
    expect(caught).toHaveProperty('validationErrors');
  });
});
