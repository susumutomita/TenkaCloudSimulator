import { createHash } from 'node:crypto';
import type {
  Fidelity,
  Origin,
  Plane,
  Requirement,
  RequirementClassification,
  SourceLocation,
} from './model.ts';
import { FIDELITIES } from './model.ts';

export interface RequirementInput {
  problemId: string;
  targetId: string;
  provider: string;
  engine: string;
  service: string;
  resourceType: string;
  operation: string;
  fidelity: readonly Fidelity[];
  plane: Plane;
  origin: Origin;
  classification: RequirementClassification;
  source: SourceLocation;
}

export function createRequirement(input: RequirementInput): Requirement {
  if (input.fidelity.length === 0) {
    throw new Error('requirement fidelity must be non-empty');
  }
  if (new Set(input.fidelity).size !== input.fidelity.length) {
    throw new Error('requirement fidelity must be unique');
  }
  const fidelity = FIDELITIES.filter((level) => input.fidelity.includes(level));
  if (
    fidelity.length !== input.fidelity.length ||
    fidelity.some((level, index) => level !== input.fidelity[index])
  ) {
    throw new Error('requirement fidelity must be in canonical L0..L4 order');
  }
  const identity = [
    input.problemId,
    input.targetId,
    input.provider,
    input.engine,
    input.service,
    input.resourceType,
    input.operation,
    fidelity.join(','),
    input.plane,
    input.origin,
    input.classification,
    input.source.path,
    String(input.source.line),
    input.source.jsonPointer ?? '',
  ].join('\u0000');
  return {
    id: createHash('sha256').update(identity).digest('hex'),
    ...input,
    fidelity,
  };
}

export function compareRequirements(
  left: Requirement,
  right: Requirement
): number {
  return [
    left.problemId,
    left.targetId,
    left.provider,
    left.service,
    left.resourceType,
    left.operation,
    left.fidelity.join(','),
    left.plane,
    left.origin,
    left.classification,
    left.source.path,
    String(left.source.line).padStart(10, '0'),
    left.id,
  ]
    .join('|')
    .localeCompare(
      [
        right.problemId,
        right.targetId,
        right.provider,
        right.service,
        right.resourceType,
        right.operation,
        right.fidelity.join(','),
        right.plane,
        right.origin,
        right.classification,
        right.source.path,
        String(right.source.line).padStart(10, '0'),
        right.id,
      ].join('|')
    );
}
