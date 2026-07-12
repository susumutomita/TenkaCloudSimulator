import {
  CoreError,
  deterministicId,
  type ProviderClockInput,
  type ProviderClockResult,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceDeclaration,
  type ResourceRecord,
} from '@tenkacloud/simulator-core';
import { declaration, SSM_SESSION_RESOURCE } from './model';
import {
  resourcesForDeployment,
  result,
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import { objectValue, optionalString, stringValue } from './value';

const INSTANCE_RESOURCE = 'AWS::EC2::Instance';
const DEFAULT_SESSION_DOCUMENT = 'SSM-SessionManagerRunShell';
const SESSION_TTL_MILLISECONDS = 20 * 60 * 1000;
const INTERNAL_ORIGIN = '__SimulatorOrigin';
const INTERNAL_REQUEST_ID = '__SimulatorRequestId';

type SessionStatus = 'Active' | 'Terminated' | 'TimedOut';

function exactFields(
  value: Readonly<Record<string, unknown>>,
  allowed: readonly string[],
  label: string
): void {
  const unexpected = Object.keys(value).filter((key) => !allowed.includes(key));
  if (unexpected.length > 0) {
    throw new CoreError(
      'ValidationFailed',
      `${label} contains unsupported field ${unexpected.sort()[0]}`
    );
  }
}

function streamOrigin(value: unknown): string {
  const origin = stringValue(value, INTERNAL_ORIGIN);
  let parsed: URL;
  try {
    parsed = new URL(origin);
  } catch {
    throw new CoreError('ValidationFailed', 'simulator origin is invalid');
  }
  if (
    !['http:', 'https:'].includes(parsed.protocol) ||
    parsed.username ||
    parsed.password ||
    parsed.pathname !== '/' ||
    parsed.search ||
    parsed.hash
  ) {
    throw new CoreError('ValidationFailed', 'simulator origin is invalid');
  }
  return parsed.origin;
}

function requestIdentity(value: unknown): string {
  const requestId = stringValue(value, INTERNAL_REQUEST_ID);
  if (!/^tcsim-[0-9a-f]{24}$/.test(requestId)) {
    throw new CoreError(
      'ValidationFailed',
      'simulator request identity is invalid'
    );
  }
  return requestId;
}

function sessionExpiry(virtualTime: string): string {
  const timestamp = Date.parse(virtualTime);
  if (!Number.isFinite(timestamp)) {
    throw new CoreError('ValidationFailed', 'world virtual time is invalid');
  }
  return new Date(timestamp + SESSION_TTL_MILLISECONDS).toISOString();
}

function sessionStreamUrl(
  origin: string,
  command: ProviderCommandInput,
  sessionId: string
): string {
  const url = new URL(
    `/v1/native/aws/ssm/data-channel/${encodeURIComponent(sessionId)}`,
    origin
  );
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
  url.searchParams.set('worldId', command.worldId);
  url.searchParams.set('deploymentId', command.deploymentId);
  return url.toString();
}

function sessionRecord(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  sessionId: string
): ResourceRecord {
  return (
    resourcesForDeployment(
      world,
      command.deploymentId,
      SSM_SESSION_RESOURCE
    ).find((resource) => storedProperties(resource).refValue === sessionId) ??
    (() => {
      throw new CoreError('NotFound', 'SSM session does not exist');
    })()
  );
}

function sessionStatus(resource: ResourceRecord): SessionStatus {
  const status = stateObject(storedProperties(resource))['status'];
  if (status === 'Active' || status === 'Terminated' || status === 'TimedOut') {
    return status;
  }
  throw new CoreError('ValidationFailed', 'SSM session status is invalid');
}

function activeSession(
  resource: ResourceRecord,
  world: ProviderWorldView
): Readonly<Record<string, unknown>> {
  const stored = storedProperties(resource);
  const state = stateObject(stored);
  const status = sessionStatus(resource);
  if (status === 'Terminated') {
    throw new CoreError('Conflict', 'terminated SSM session cannot be resumed');
  }
  const expiresAt = stringValue(state['expiresAt'], 'session expiresAt');
  if (
    status === 'TimedOut' ||
    Date.parse(expiresAt) <= Date.parse(world.world.virtualTime)
  ) {
    throw new CoreError('Conflict', 'SSM session has timed out');
  }
  return state;
}

function targetInstance(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  target: string
): ResourceRecord {
  const instance = resourcesForDeployment(
    world,
    command.deploymentId,
    INSTANCE_RESOURCE
  ).find((resource) => storedProperties(resource).refValue === target);
  if (!instance) {
    throw new CoreError('NotFound', 'SSM session target does not exist');
  }
  const state = stateObject(storedProperties(instance));
  if (state['instanceState'] !== 'running') {
    throw new CoreError('Conflict', 'SSM session target is not running');
  }
  return instance;
}

function startSession(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  exactFields(
    command.input,
    [
      'DocumentName',
      'Parameters',
      'Reason',
      'Target',
      INTERNAL_ORIGIN,
      INTERNAL_REQUEST_ID,
    ],
    'StartSession input'
  );
  const target = stringValue(command.input['Target'], 'Target');
  const instance = targetInstance(command, world, target);
  const documentName =
    optionalString(command.input['DocumentName'], 'DocumentName') ??
    DEFAULT_SESSION_DOCUMENT;
  if (documentName !== DEFAULT_SESSION_DOCUMENT) {
    throw new CoreError(
      'UnsupportedCapability',
      `SSM session document ${documentName} is not supported`
    );
  }
  if (command.input['Parameters'] !== undefined) {
    const parameters = objectValue(command.input['Parameters'], 'Parameters');
    if (Object.keys(parameters).length > 0) {
      throw new CoreError(
        'UnsupportedCapability',
        'SSM session document parameters are not supported'
      );
    }
  }
  if (command.input['Reason'] !== undefined) {
    const reason = stringValue(command.input['Reason'], 'Reason');
    if (reason.length > 256) {
      throw new CoreError('ValidationFailed', 'Reason exceeds 256 characters');
    }
  }
  const origin = streamOrigin(command.input[INTERNAL_ORIGIN]);
  const requestId = requestIdentity(command.input[INTERNAL_REQUEST_ID]);
  const sessionId = deterministicId('session', {
    worldId: command.worldId,
    deploymentId: command.deploymentId,
    target,
    requestId,
  });
  const tokenValue = deterministicId('token', { sessionId, requestId });
  const expiresAt = sessionExpiry(world.world.virtualTime);
  const resource = declaration({
    resourceType: SSM_SESSION_RESOURCE,
    resourceId: sessionId,
    properties: {
      logicalId: sessionId,
      physicalId: sessionId,
      refValue: sessionId,
      dependsOn: [instance.resourceId],
      attributes: {},
      templateProperties: {},
      status: 'ACTIVE',
      target,
      documentName,
      state: {
        status: 'Active',
        tokenValue,
        generation: 0,
        startedAt: world.world.virtualTime,
        expiresAt,
      },
    },
  });
  return result(
    'AwsSsmSessionStarted',
    {
      SessionId: sessionId,
      StreamUrl: sessionStreamUrl(origin, command, sessionId),
      TokenValue: tokenValue,
    },
    [resource]
  );
}

function resumeSession(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  exactFields(
    command.input,
    ['SessionId', INTERNAL_ORIGIN, INTERNAL_REQUEST_ID],
    'ResumeSession input'
  );
  const sessionId = stringValue(command.input['SessionId'], 'SessionId');
  const resource = sessionRecord(command, world, sessionId);
  const state = activeSession(resource, world);
  const origin = streamOrigin(command.input[INTERNAL_ORIGIN]);
  const requestId = requestIdentity(command.input[INTERNAL_REQUEST_ID]);
  const generation = Number(state['generation']);
  if (!Number.isSafeInteger(generation) || generation < 0) {
    throw new CoreError(
      'ValidationFailed',
      'SSM session generation is invalid'
    );
  }
  const tokenValue = deterministicId('token', {
    sessionId,
    requestId,
    generation: generation + 1,
  });
  const updated = updateStoredResource(resource, {
    status: 'ACTIVE',
    state: {
      ...state,
      status: 'Active',
      tokenValue,
      generation: generation + 1,
      resumedAt: world.world.virtualTime,
      expiresAt: sessionExpiry(world.world.virtualTime),
    },
  });
  return result(
    'AwsSsmSessionResumed',
    {
      SessionId: sessionId,
      StreamUrl: sessionStreamUrl(origin, command, sessionId),
      TokenValue: tokenValue,
    },
    [updated]
  );
}

function terminateSession(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  exactFields(command.input, ['SessionId'], 'TerminateSession input');
  const sessionId = stringValue(command.input['SessionId'], 'SessionId');
  const resource = sessionRecord(command, world, sessionId);
  const state = stateObject(storedProperties(resource));
  if (sessionStatus(resource) === 'TimedOut') {
    throw new CoreError('Conflict', 'SSM session has timed out');
  }
  const updated: ResourceDeclaration = updateStoredResource(resource, {
    status: 'TERMINATED',
    state: {
      ...state,
      status: 'Terminated',
      tokenValue: '',
      terminatedAt: world.world.virtualTime,
    },
  });
  return result('AwsSsmSessionTerminated', { SessionId: sessionId }, [updated]);
}

export function reduceSsmSession(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'StartSession':
      return startSession(command, world);
    case 'ResumeSession':
      return resumeSession(command, world);
    case 'TerminateSession':
      return terminateSession(command, world);
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `SSM session operation ${command.operation} is not supported`
      );
  }
}

export function expireSsmSessions(
  input: ProviderClockInput,
  world: ProviderWorldView
): ProviderClockResult {
  const targetTime = Date.parse(input.virtualTime);
  if (!Number.isFinite(targetTime)) {
    throw new CoreError('ValidationFailed', 'clock target time is invalid');
  }
  const resources: ResourceRecord[] = [];
  const events = [];
  const appliedTransitionIds: string[] = [];
  for (const resource of world.resources) {
    if (
      resource.provider !== 'aws' ||
      resource.resourceType !== SSM_SESSION_RESOURCE ||
      resource.status !== 'ready'
    ) {
      continue;
    }
    const stored = storedProperties(resource);
    const state = stateObject(stored);
    if (state['status'] !== 'Active') continue;
    const expiresAt = stringValue(state['expiresAt'], 'session expiresAt');
    const expiryTime = Date.parse(expiresAt);
    if (!Number.isFinite(expiryTime)) {
      throw new CoreError('ValidationFailed', 'session expiresAt is invalid');
    }
    if (expiryTime > targetTime) continue;
    const transitionId = deterministicId('transition', {
      sessionId: resource.resourceId,
      expiresAt,
    });
    resources.push({
      ...resource,
      properties: {
        ...resource.properties,
        status: 'TIMED_OUT',
        state: {
          ...state,
          status: 'TimedOut',
          tokenValue: '',
          timedOutAt: input.virtualTime,
        },
      },
    });
    events.push({
      type: 'AwsSsmSessionTimedOut',
      payload: {
        sessionId: resource.resourceId,
        transitionId,
        timedOutAt: input.virtualTime,
      },
    });
    appliedTransitionIds.push(transitionId);
  }
  return {
    events,
    resources,
    deletedResourceIds: [],
    appliedTransitionIds,
  };
}
