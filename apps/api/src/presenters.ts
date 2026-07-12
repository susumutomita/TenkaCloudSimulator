import {
  assertSimulatorDeploymentResponse,
  assertSimulatorEvent,
  assertSimulatorResourceProjection,
  assertSimulatorWorldResponse,
  type SimulatorDeploymentResponse,
  type SimulatorDeploymentStatus,
  type SimulatorEvent,
  type SimulatorResourceProjection,
  type SimulatorWorldResponse,
} from '@tenkacloud/simulator-contracts';
import type {
  DeploymentRecord,
  EventRecord,
  ResourceRecord,
  WorldRecord,
} from '@tenkacloud/simulator-core';
import { coreDiagnostic } from './errors.js';

const DEPLOYMENT_STATUS: Readonly<
  Record<DeploymentRecord['status'], SimulatorDeploymentStatus>
> = {
  ready: 'running',
  deploying: 'deploying',
  failed: 'failed',
  rejected: 'failed',
  deleted: 'deleted',
};

function flattenOutputs(
  outputs: DeploymentRecord['outputs']
): Readonly<Record<string, string>> {
  const { default: defaultOutputs } = outputs;
  if (defaultOutputs && Object.keys(outputs).length === 1) {
    return defaultOutputs;
  }
  return Object.fromEntries(
    Object.entries(outputs).flatMap(([targetId, targetOutputs]) =>
      Object.entries(targetOutputs).map(([key, value]) => [
        `${targetId}.${key}`,
        value,
      ])
    )
  );
}

export function deploymentResponse(
  deployment: DeploymentRecord
): SimulatorDeploymentResponse {
  const response: SimulatorDeploymentResponse = {
    deploymentId: deployment.deploymentId,
    status: DEPLOYMENT_STATUS[deployment.status],
    outputs: flattenOutputs(deployment.outputs),
    diagnostics: deployment.diagnostics.map(coreDiagnostic),
  };
  assertSimulatorDeploymentResponse(response);
  return response;
}

export function eventResponse(event: EventRecord): SimulatorEvent {
  const response: unknown = {
    worldId: event.worldId,
    sequence: event.sequence,
    virtualTimestamp: event.virtualTime,
    command: {
      id: event.commandId,
      operation: event.type,
    },
    type: event.type,
    schemaVersion: '1',
    payloadHash: event.payloadHash,
    payload: event.payload,
  };
  assertSimulatorEvent(response);
  return response;
}

export function resourceProjection(
  resources: readonly ResourceRecord[]
): SimulatorResourceProjection {
  const response: unknown = { resources };
  assertSimulatorResourceProjection(response);
  return response;
}

export function worldResponse(
  world: WorldRecord,
  consoleBaseUrl: string
): SimulatorWorldResponse {
  const response: SimulatorWorldResponse = {
    worldId: world.worldId,
    consoleUrl: `${consoleBaseUrl}/${encodeURIComponent(world.worldId)}`,
  };
  assertSimulatorWorldResponse(response);
  return response;
}
