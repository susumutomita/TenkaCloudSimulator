import { createHash } from 'node:crypto';
import type {
  SimulatorOverlayArtifact,
  SimulatorOverlayWorkload,
  SimulatorSimulationOverlay,
} from '@tenkacloud/simulator-contracts';
import simulationOverlaySchema from '@tenkacloud/simulator-contracts/schemas/simulation-overlay.schema.json';
import Ajv2020 from 'ajv/dist/2020.js';
import type { ResolvedTargetSource } from './artifact-bundle';
import type {
  CapabilityRequirement,
  SingleRuntimeTarget,
  WorkloadDeclaration,
} from './domain';
import { CoreError } from './errors';

interface IdentifiedTarget extends SingleRuntimeTarget {
  readonly id: string;
}

export interface ResolvedSimulationOverlay {
  readonly document?: SimulatorSimulationOverlay;
  readonly requirements: readonly CapabilityRequirement[];
  readonly workloads: readonly WorkloadDeclaration[];
}

const ajv = new Ajv2020({ allErrors: true, strict: true });
const validateOverlay = ajv.compile<SimulatorSimulationOverlay>(
  simulationOverlaySchema
);

function validationFailed(message: string): never {
  throw new CoreError('ValidationFailed', message);
}

function artifactHash(content: string): string {
  return createHash('sha256').update(content, 'utf8').digest('hex');
}

function verifyArtifact(
  artifact: SimulatorOverlayArtifact | undefined,
  targetId: string,
  sources: ReadonlyMap<string, ResolvedTargetSource>
): void {
  if (!artifact) return;
  const source = sources.get(targetId);
  const file = source?.artifacts.find(
    (candidate) => candidate.path === artifact.path
  );
  if (!file) {
    validationFailed(
      `simulation overlay artifact ${artifact.path} is missing for target ${targetId}`
    );
  }
  if (artifactHash(file.content) !== artifact.sha256) {
    validationFailed(
      `simulation overlay artifact ${artifact.path} hash does not match`
    );
  }
}

function targetFor(
  targetId: string,
  targets: ReadonlyMap<string, IdentifiedTarget>
): IdentifiedTarget {
  const target = targets.get(targetId);
  if (!target) {
    validationFailed(`simulation overlay target ${targetId} is not in runtime`);
  }
  return target;
}

function requirementIdentity(requirement: CapabilityRequirement): string {
  return [
    requirement.provider,
    requirement.engine,
    requirement.service,
    requirement.resourceType,
    requirement.operation,
    ...requirement.fidelity,
  ].join('\u0000');
}

function workloadDeclaration(
  workload: SimulatorOverlayWorkload
): WorkloadDeclaration {
  return {
    id: workload.id,
    targetId: workload.targetId,
    resourceRef: workload.resourceRef,
    image: workload.image,
    ...(workload.command === undefined ? {} : { command: workload.command }),
    containerPort: workload.containerPort,
    ...(workload.healthPath === undefined
      ? {}
      : { healthPath: workload.healthPath }),
  };
}

export function resolveSimulationOverlay(
  value: unknown,
  targets: readonly IdentifiedTarget[],
  sources: readonly ResolvedTargetSource[]
): ResolvedSimulationOverlay {
  if (value === undefined) return { requirements: [], workloads: [] };
  if (!validateOverlay(value)) {
    validationFailed(
      `simulation overlay is invalid: ${ajv.errorsText(validateOverlay.errors)}`
    );
  }
  const targetMap = new Map(targets.map((target) => [target.id, target]));
  const sourceMap = new Map(sources.map((source) => [source.targetId, source]));
  const requirements = new Map<string, CapabilityRequirement>();
  const workloads: WorkloadDeclaration[] = [];
  const explicit = new Set<string>();
  for (const item of value.requirements ?? []) {
    const target = targetFor(item.targetId, targetMap);
    verifyArtifact(item.artifact, item.targetId, sourceMap);
    const requirement: CapabilityRequirement = {
      provider: target.provider,
      engine: target.engine,
      service: item.service,
      resourceType: item.resourceType,
      operation: item.operation,
      fidelity: [item.fidelity],
      source: {
        path: item.artifact?.path ?? 'simulation-overlay',
      },
    };
    const identity = requirementIdentity(requirement);
    if (explicit.has(identity)) {
      validationFailed(
        `simulation overlay requirement ${identity} is duplicated`
      );
    }
    explicit.add(identity);
    requirements.set(identity, requirement);
  }
  const workloadIds = new Set<string>();
  for (const workload of value.workloads ?? []) {
    const target = targetFor(workload.targetId, targetMap);
    verifyArtifact(workload.artifact, workload.targetId, sourceMap);
    const workloadIdentity = `${workload.targetId}\u0000${workload.id}`;
    if (workloadIds.has(workloadIdentity)) {
      validationFailed(
        `simulation overlay workload ${workload.id} is duplicated for ${workload.targetId}`
      );
    }
    workloadIds.add(workloadIdentity);
    workloads.push(workloadDeclaration(workload));
    const requirement: CapabilityRequirement = {
      provider: target.provider,
      engine: target.engine,
      service: 'runtime',
      resourceType: 'Runtime::Workload',
      operation: 'Materialize',
      fidelity: ['L4'],
      source: {
        path:
          workload.artifact?.path ??
          `simulation-overlay#workloads/${workload.id}`,
      },
    };
    requirements.set(requirementIdentity(requirement), requirement);
  }
  return {
    document: value,
    requirements: [...requirements.values()],
    workloads,
  };
}
