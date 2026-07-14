import Ajv2020, {
  type ErrorObject,
  type ValidateFunction,
} from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';
import {
  advanceClockRequestSchema,
  advanceClockResponseSchema,
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
  materializeWorkloadsRequestSchema,
  resourceProjectionSchema,
  runtimeSchema,
  simulationOverlaySchema,
  snapshotSchema,
} from './schemas.js';
import type {
  CapabilityCoverageReport,
  CapabilityRequirement,
  ProblemRuntimeDescriptor,
  SimulatorCapabilities,
  SimulatorClockAdvanceRequest,
  SimulatorClockAdvanceResponse,
  SimulatorDeploymentRequest,
  SimulatorDeploymentResponse,
  SimulatorErrorEnvelope,
  SimulatorEvent,
  SimulatorEventPage,
  SimulatorMaterializeWorkloadsRequest,
  SimulatorResourceProjection,
  SimulatorSimulationOverlay,
  SimulatorSnapshot,
  SimulatorSnapshotEnvelope,
  SimulatorWorldRequest,
  SimulatorWorldResponse,
} from './types.js';

const ajv = addFormats(new Ajv2020({ allErrors: true, strict: true }));

function hasUniqueProperty(property: string, entries: unknown[]): boolean {
  const seen = new Set<unknown>();
  for (const entry of entries) {
    if (typeof entry !== 'object' || entry === null) continue;
    const value = Reflect.get(entry, property);
    if (seen.has(value)) return false;
    seen.add(value);
  }
  return true;
}

ajv.addKeyword({
  keyword: 'x-uniqueBy',
  type: 'array',
  schemaType: 'string',
  validate: hasUniqueProperty,
  errors: false,
});
ajv.addSchema(commonSchema);

const runtimeValidator = ajv.compile<ProblemRuntimeDescriptor>(runtimeSchema);
const capabilitiesValidator =
  ajv.compile<SimulatorCapabilities>(capabilitiesSchema);
const worldRequestValidator = ajv.compile<SimulatorWorldRequest>(
  createWorldRequestSchema
);
const worldResponseValidator = ajv.compile<SimulatorWorldResponse>(
  createWorldResponseSchema
);
const simulationOverlayValidator = ajv.compile<SimulatorSimulationOverlay>(
  simulationOverlaySchema
);
const clockAdvanceRequestValidator = ajv.compile<SimulatorClockAdvanceRequest>(
  advanceClockRequestSchema
);
const clockAdvanceResponseValidator =
  ajv.compile<SimulatorClockAdvanceResponse>(advanceClockResponseSchema);
const errorEnvelopeValidator =
  ajv.compile<SimulatorErrorEnvelope>(errorEnvelopeSchema);
const deploymentRequestValidator = ajv.compile<SimulatorDeploymentRequest>(
  createDeploymentRequestSchema
);
const deploymentResponseValidator = ajv.compile<SimulatorDeploymentResponse>(
  deploymentResponseSchema
);
const materializeWorkloadsRequestValidator =
  ajv.compile<SimulatorMaterializeWorkloadsRequest>(
    materializeWorkloadsRequestSchema
  );
const eventValidator = ajv.compile<SimulatorEvent>(eventSchema);
const eventPageValidator = ajv.compile<SimulatorEventPage>(eventPageSchema);
const resourceProjectionValidator = ajv.compile<SimulatorResourceProjection>(
  resourceProjectionSchema
);
const snapshotValidator = ajv.compile<SimulatorSnapshot>(snapshotSchema);
const { integrityProof: _integrityProofSchema, ...snapshotEnvelopeProperties } =
  snapshotSchema.properties;
const snapshotEnvelopeValidator = ajv.compile<SimulatorSnapshotEnvelope>({
  ...snapshotSchema,
  $id: snapshotSchema.$id.replace(
    'snapshot.schema.json',
    'snapshot-envelope.schema.json'
  ),
  required: snapshotSchema.required.filter(
    (property) => property !== 'integrityProof'
  ),
  properties: snapshotEnvelopeProperties,
});
const capabilityRequirementValidator = ajv.compile<CapabilityRequirement>(
  capabilityRequirementSchema
);
const capabilityCoverageReportValidator = ajv.compile<CapabilityCoverageReport>(
  capabilityReportSchema
);

export class ContractValidationError extends TypeError {
  public readonly validationErrors: readonly ErrorObject[] | null | undefined;

  public constructor(
    public readonly contractName: string,
    validationErrors: ErrorObject[] | null | undefined
  ) {
    super(
      `${contractName} does not match its JSON Schema: ${ajv.errorsText(validationErrors)}`
    );
    this.name = 'ContractValidationError';
    this.validationErrors = validationErrors;
  }
}

function assertContract<T>(
  contractName: string,
  validator: ValidateFunction<T>,
  value: unknown
): asserts value is T {
  if (!validator(value)) {
    throw new ContractValidationError(contractName, validator.errors);
  }
}

export function isProblemRuntimeDescriptor(
  value: unknown
): value is ProblemRuntimeDescriptor {
  return runtimeValidator(value);
}

export function assertProblemRuntimeDescriptor(
  value: unknown
): asserts value is ProblemRuntimeDescriptor {
  assertContract('ProblemRuntimeDescriptor', runtimeValidator, value);
}

export function isSimulatorCapabilities(
  value: unknown
): value is SimulatorCapabilities {
  return capabilitiesValidator(value);
}

export function assertSimulatorCapabilities(
  value: unknown
): asserts value is SimulatorCapabilities {
  assertContract('SimulatorCapabilities', capabilitiesValidator, value);
}

export function isSimulatorWorldRequest(
  value: unknown
): value is SimulatorWorldRequest {
  return worldRequestValidator(value);
}

export function assertSimulatorWorldRequest(
  value: unknown
): asserts value is SimulatorWorldRequest {
  assertContract('SimulatorWorldRequest', worldRequestValidator, value);
}

export function isSimulatorWorldResponse(
  value: unknown
): value is SimulatorWorldResponse {
  return worldResponseValidator(value);
}

export function assertSimulatorWorldResponse(
  value: unknown
): asserts value is SimulatorWorldResponse {
  assertContract('SimulatorWorldResponse', worldResponseValidator, value);
}

export function isSimulatorClockAdvanceRequest(
  value: unknown
): value is SimulatorClockAdvanceRequest {
  return clockAdvanceRequestValidator(value);
}

export function assertSimulatorClockAdvanceRequest(
  value: unknown
): asserts value is SimulatorClockAdvanceRequest {
  assertContract(
    'SimulatorClockAdvanceRequest',
    clockAdvanceRequestValidator,
    value
  );
}

export function isSimulatorClockAdvanceResponse(
  value: unknown
): value is SimulatorClockAdvanceResponse {
  return clockAdvanceResponseValidator(value);
}

export function assertSimulatorClockAdvanceResponse(
  value: unknown
): asserts value is SimulatorClockAdvanceResponse {
  assertContract(
    'SimulatorClockAdvanceResponse',
    clockAdvanceResponseValidator,
    value
  );
}

export function isSimulatorDeploymentRequest(
  value: unknown
): value is SimulatorDeploymentRequest {
  return deploymentRequestValidator(value);
}

export function assertSimulatorDeploymentRequest(
  value: unknown
): asserts value is SimulatorDeploymentRequest {
  assertContract(
    'SimulatorDeploymentRequest',
    deploymentRequestValidator,
    value
  );
}

export function isSimulatorMaterializeWorkloadsRequest(
  value: unknown
): value is SimulatorMaterializeWorkloadsRequest {
  return materializeWorkloadsRequestValidator(value);
}

export function assertSimulatorMaterializeWorkloadsRequest(
  value: unknown
): asserts value is SimulatorMaterializeWorkloadsRequest {
  assertContract(
    'SimulatorMaterializeWorkloadsRequest',
    materializeWorkloadsRequestValidator,
    value
  );
}

export function isSimulatorDeploymentResponse(
  value: unknown
): value is SimulatorDeploymentResponse {
  return deploymentResponseValidator(value);
}

export function assertSimulatorDeploymentResponse(
  value: unknown
): asserts value is SimulatorDeploymentResponse {
  assertContract(
    'SimulatorDeploymentResponse',
    deploymentResponseValidator,
    value
  );
}

export function isSimulatorErrorEnvelope(
  value: unknown
): value is SimulatorErrorEnvelope {
  return errorEnvelopeValidator(value);
}

export function assertSimulatorErrorEnvelope(
  value: unknown
): asserts value is SimulatorErrorEnvelope {
  assertContract('SimulatorErrorEnvelope', errorEnvelopeValidator, value);
}

export function isSimulatorEvent(value: unknown): value is SimulatorEvent {
  return eventValidator(value);
}

export function assertSimulatorEvent(
  value: unknown
): asserts value is SimulatorEvent {
  assertContract('SimulatorEvent', eventValidator, value);
}

export function isSimulatorEventPage(
  value: unknown
): value is SimulatorEventPage {
  return eventPageValidator(value);
}

export function assertSimulatorEventPage(
  value: unknown
): asserts value is SimulatorEventPage {
  assertContract('SimulatorEventPage', eventPageValidator, value);
}

export function isSimulatorResourceProjection(
  value: unknown
): value is SimulatorResourceProjection {
  return resourceProjectionValidator(value);
}

export function assertSimulatorResourceProjection(
  value: unknown
): asserts value is SimulatorResourceProjection {
  assertContract(
    'SimulatorResourceProjection',
    resourceProjectionValidator,
    value
  );
}

export function isSimulatorSnapshot(
  value: unknown
): value is SimulatorSnapshot {
  return snapshotValidator(value);
}

export function isSimulatorSnapshotEnvelope(
  value: unknown
): value is SimulatorSnapshotEnvelope {
  return snapshotEnvelopeValidator(value);
}

export function assertSimulatorSnapshotEnvelope(
  value: unknown
): asserts value is SimulatorSnapshotEnvelope {
  assertContract('SimulatorSnapshotEnvelope', snapshotEnvelopeValidator, value);
}

export function assertSimulatorSnapshot(
  value: unknown
): asserts value is SimulatorSnapshot {
  assertContract('SimulatorSnapshot', snapshotValidator, value);
}

export function isSimulatorSimulationOverlay(
  value: unknown
): value is SimulatorSimulationOverlay {
  return simulationOverlayValidator(value);
}

export function assertSimulatorSimulationOverlay(
  value: unknown
): asserts value is SimulatorSimulationOverlay {
  assertContract(
    'SimulatorSimulationOverlay',
    simulationOverlayValidator,
    value
  );
}

export function isCapabilityRequirement(
  value: unknown
): value is CapabilityRequirement {
  return capabilityRequirementValidator(value);
}

export function assertCapabilityRequirement(
  value: unknown
): asserts value is CapabilityRequirement {
  assertContract(
    'CapabilityRequirement',
    capabilityRequirementValidator,
    value
  );
}

export function isCapabilityCoverageReport(
  value: unknown
): value is CapabilityCoverageReport {
  return capabilityCoverageReportValidator(value);
}

export function assertCapabilityCoverageReport(
  value: unknown
): asserts value is CapabilityCoverageReport {
  assertContract(
    'CapabilityCoverageReport',
    capabilityCoverageReportValidator,
    value
  );
}
