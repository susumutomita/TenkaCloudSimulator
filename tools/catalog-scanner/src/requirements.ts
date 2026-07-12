import { createHash } from 'node:crypto';
import type {
  Fidelity,
  Origin,
  Plane,
  Requirement,
  RequirementClassification,
  SourceLocation,
} from './model.ts';

export interface RequirementInput {
  problemId: string;
  targetId: string;
  provider: string;
  engine: string;
  service: string;
  resourceType: string;
  operation: string;
  fidelity: Fidelity;
  plane: Plane;
  origin: Origin;
  classification: RequirementClassification;
  source: SourceLocation;
}

export function createRequirement(input: RequirementInput): Requirement {
  const identity = [
    input.problemId,
    input.targetId,
    input.provider,
    input.engine,
    input.service,
    input.resourceType,
    input.operation,
    input.fidelity,
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
        right.plane,
        right.origin,
        right.classification,
        right.source.path,
        String(right.source.line).padStart(10, '0'),
        right.id,
      ].join('|')
    );
}
