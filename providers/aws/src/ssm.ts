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
import { COMMAND_RESOURCE, declaration, SSM_COMMAND_RESOURCE } from './model';
import { expireSsmSessions, reduceSsmSession } from './ssm-session';
import {
  awsResources,
  findBy,
  findStack,
  resourcesForDeployment,
  result,
  stackProperties,
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import {
  booleanValue,
  numberValue,
  objectValue,
  optionalString,
  optionalStringArray,
  stringArray,
  stringValue,
} from './value';

const PARAMETER_RESOURCE = 'AWS::SSM::Parameter';
const INSTANCE_RESOURCE = 'AWS::EC2::Instance';
const MAX_COMMAND_DOCUMENT_BYTES = 64 * 1024;
const MAX_REVERT_AFTER_SECONDS = 24 * 60 * 60;

function parameterName(resource: ReturnType<typeof findParameter>): string {
  const stored = storedProperties(resource);
  const name = stored.templateProperties['Name'];
  return typeof name === 'string' ? name : stored.refValue;
}

function findParameter(world: ProviderWorldView, name: string) {
  return findBy(
    world,
    PARAMETER_RESOURCE,
    (properties) =>
      properties.refValue === name ||
      properties.templateProperties['Name'] === name,
    'SSM parameter'
  );
}

function parameterDocument(resource: ReturnType<typeof findParameter>) {
  const stored = storedProperties(resource);
  const state = stateObject(stored);
  return {
    Name: parameterName(resource),
    Type: stored.templateProperties['Type'] ?? 'String',
    Value: stored.templateProperties['Value'] ?? '',
    Version: state['version'] ?? 1,
    LastModifiedDate: state['lastModifiedDate'],
    ARN: stored.attributes['Arn'],
    DataType: stored.templateProperties['DataType'] ?? 'text',
  };
}

function allParameters(world: ProviderWorldView) {
  return [...awsResources(world, PARAMETER_RESOURCE)].sort((left, right) =>
    parameterName(left).localeCompare(parameterName(right))
  );
}

function requestedInstanceIds(
  command: ProviderCommandInput
): readonly string[] {
  const direct = optionalStringArray(
    command.input['InstanceIds'],
    'InstanceIds'
  );
  if (direct && direct.length > 0) return direct;
  const targets = command.input['Targets'];
  if (!Array.isArray(targets)) {
    throw new CoreError(
      'ValidationFailed',
      'SendCommand requires InstanceIds or Targets'
    );
  }
  return targets.flatMap((target, index) => {
    const object = objectValue(target, `Targets[${index}]`);
    if (object['Key'] !== 'InstanceIds') {
      throw new CoreError(
        'ValidationFailed',
        'only InstanceIds SSM targets are supported'
      );
    }
    return stringArray(object['Values'], `Targets[${index}].Values`);
  });
}

function commandLines(command: ProviderCommandInput): readonly string[] {
  return documentLines(command.input['Parameters'], 'Parameters');
}

function documentLines(value: unknown, label: string): readonly string[] {
  const parameters = objectValue(value, label);
  const commands = parameters['commands'] ?? parameters['Commands'];
  return stringArray(commands, `${label}.commands`);
}

function mutableState(
  state: Readonly<Record<string, unknown>>
): Record<string, unknown> {
  return { ...state };
}

function serviceState(state: Record<string, unknown>): Record<string, unknown> {
  const services = state['services'];
  const parsed =
    services === undefined
      ? {}
      : { ...objectValue(services, 'instance services') };
  state['services'] = parsed;
  return parsed;
}

interface CommandEffect {
  readonly matches: RegExp;
  apply(state: Record<string, unknown>, line: string): void;
}

function nonDefaultAuthToken(line: string): boolean {
  const normalized = line.replaceAll('\\', '');
  const literal = /auth_token\s*=\s*["']([^"']+)["']/.exec(normalized)?.[1];
  if (literal !== undefined) return literal !== 'change-me';
  const argument =
    /--arg\s+([A-Za-z][A-Za-z0-9_]*)\s+(?:"([^"]+)"|'([^']+)'|([^\s]+))/.exec(
      normalized
    );
  if (!argument) return false;
  const name = argument[1] ?? '';
  const value = argument[2] ?? argument[3] ?? argument[4] ?? '';
  return (
    value !== 'change-me' &&
    value.length > 0 &&
    normalized.includes(`auth_token=$${name}`)
  );
}

const COMMAND_EFFECTS: readonly CommandEffect[] = [
  {
    matches: /systemctl (?:start|restart) nginx/,
    apply: (state) => {
      serviceState(state)['nginx'] = 'running';
      if (state['nginxRateLimitConfigured'] === true) {
        state['rateLimitEnabled'] = true;
      }
    },
  },
  {
    matches: /systemctl stop nginx/,
    apply: (state) => {
      serviceState(state)['nginx'] = 'stopped';
    },
  },
  {
    matches: /systemctl (?:start|restart) tenkacloud-vibe/,
    apply: (state) => {
      serviceState(state)['tenkacloud-vibe'] = 'running';
    },
  },
  {
    matches: /systemctl stop tenkacloud-vibe/,
    apply: (state) => {
      serviceState(state)['tenkacloud-vibe'] = 'stopped';
    },
  },
  {
    matches: /tc qdisc del/,
    apply: (state) => {
      state['networkDelayMs'] = 0;
    },
  },
  {
    matches: /command -v tc|ip route show default/,
    apply: () => {
      // Package availability and interface discovery do not change simulation state.
    },
  },
  {
    matches: /wipe_database\.sh/,
    apply: (state) => {
      state['databaseWiped'] = true;
    },
  },
  {
    matches:
      /DELETE\s+FROM\s+posts\s+WHERE\s+author\s*=\s*['"]redteam-spam['"]/i,
    apply: (state) => {
      state['databaseWiped'] = false;
      state['boardClean'] = true;
    },
  },
  {
    matches: /seed-(?:sqlite|postgres)\.sql|sqlite3|PGPASSWORD=/,
    apply: (state) => {
      state['databaseWiped'] = false;
    },
  },
  {
    matches:
      /config\.json\.redteam-auth\.bak.*\bcp\b|\bcp\b.*config\.json\.redteam-auth\.bak/,
    apply: (state) => {
      state['authBackup'] = state['authRequired'] ?? false;
      state['authTokenBackup'] = state['authTokenConfigured'] ?? false;
    },
  },
  {
    matches: /auth_required=true/,
    apply: (state, line) => {
      state['authRequired'] = true;
      if (nonDefaultAuthToken(line)) state['authTokenConfigured'] = true;
    },
  },
  {
    matches: /auth_required=false/,
    apply: (state) => {
      state['authRequired'] = false;
    },
  },
  {
    matches: /redteam-auth\.bak.*\bmv\b|\bmv\b.*redteam-auth\.bak/,
    apply: (state) => {
      state['authRequired'] = state['authBackup'] ?? false;
      state['authTokenConfigured'] = state['authTokenBackup'] ?? false;
      delete state['authBackup'];
      delete state['authTokenBackup'];
    },
  },
  {
    matches:
      /api\.py.*cur\.execute.*(?:%s|\?).*(?:username.*password|password.*username)/i,
    apply: (state) => {
      state['sqliParameterized'] = true;
    },
  },
  {
    matches:
      /(?=.*limit_req)(?=.*(?:\/etc\/nginx|nginx\.conf))(?=.*(?:tee|sed|printf|cat|>))/,
    apply: (state) => {
      state['nginxRateLimitConfigured'] = true;
    },
  },
  {
    matches: /docker compose (?:up -d|restart)(?:\s+api)?/,
    apply: () => {
      // The catalog security workload bind-mounts api.py and reloads it.
    },
  },
  {
    matches: /deface_site\.sh/,
    apply: (state) => {
      state['siteDefaced'] = true;
    },
  },
  {
    matches: /restore_site\.sh/,
    apply: (state) => {
      state['siteDefaced'] = false;
    },
  },
  {
    matches: /install_backdoor\.sh/,
    apply: (state) => {
      state['backdoorInstalled'] = true;
    },
  },
  {
    matches: /remove_backdoor\.sh/,
    apply: (state) => {
      state['backdoorInstalled'] = false;
    },
  },
  {
    matches: /pkill -f 'curl -s -o \/dev\/null'/,
    apply: (state) => {
      state['loadActive'] = false;
    },
  },
  {
    matches: /END=\$\(|while \[ \$\(date|curl -s -o \/dev\/null/,
    apply: (state) => {
      state['loadActive'] = true;
    },
  },
  {
    matches: /^set -a;|^true$/,
    apply: () => {
      // Shell environment setup is accepted without creating an AWS state change.
    },
  },
];

function applyCatalogCommand(
  line: string,
  state: Record<string, unknown>
): void {
  const delay = line.match(/netem delay (\d+)ms/);
  if (delay?.[1]) {
    state['networkDelayMs'] = Number(delay[1]);
    return;
  }
  const effect = COMMAND_EFFECTS.find((candidate) =>
    candidate.matches.test(line.trim())
  );
  if (effect) {
    effect.apply(state, line);
    return;
  }
  throw new CoreError(
    'UnsupportedCapability',
    `SSM command is outside the catalog reducer: ${line}`
  );
}

function sendCommand(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  const documentName = stringValue(
    command.input['DocumentName'],
    'DocumentName'
  );
  if (documentName !== 'AWS-RunShellScript') {
    throw new CoreError(
      'UnsupportedCapability',
      `SSM document ${documentName} is not supported`
    );
  }
  const instanceIds = requestedInstanceIds(command);
  const lines = commandLines(command);
  const updatedInstances: ResourceDeclaration[] = instanceIds.map(
    (instanceId) => {
      const instance = findBy(
        world,
        INSTANCE_RESOURCE,
        (properties) => properties.refValue === instanceId,
        'EC2 instance'
      );
      const stored = storedProperties(instance);
      const nextState = mutableState(stateObject(stored));
      lines.forEach((line) => {
        applyCatalogCommand(line, nextState);
      });
      return updateStoredResource(instance, { state: nextState });
    }
  );
  const commandId = deterministicId('command', {
    worldId: command.worldId,
    deploymentId: command.deploymentId,
    instanceIds,
    documentName,
    lines,
  });
  const invocation = declaration({
    resourceType: COMMAND_RESOURCE,
    resourceId: commandId,
    properties: {
      logicalId: commandId,
      physicalId: commandId,
      refValue: commandId,
      dependsOn: instanceIds,
      attributes: {},
      templateProperties: {},
      status: 'SUCCESS',
      instanceIds,
      documentName,
      commands: lines,
      requestedAt: world.world.virtualTime,
    },
  });
  return result(
    'AwsSsmCommandExecuted',
    {
      Command: {
        CommandId: commandId,
        DocumentName: documentName,
        Status: 'Success',
        InstanceIds: instanceIds,
      },
    },
    [...updatedInstances, invocation]
  );
}

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

function boundedDocument(
  value: unknown,
  label: string
): Readonly<Record<string, unknown>> {
  const document = objectValue(value, label);
  let serialized: string;
  try {
    serialized = JSON.stringify(document);
  } catch {
    throw new CoreError(
      'ValidationFailed',
      `${label} must be JSON serializable`
    );
  }
  if (
    new TextEncoder().encode(serialized).byteLength > MAX_COMMAND_DOCUMENT_BYTES
  ) {
    throw new CoreError(
      'QuotaExceeded',
      `${label} exceeds the command document limit`
    );
  }
  return document;
}

function catalogTarget(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  targetRef: string,
  targetResource: string
): { readonly resource: ResourceRecord; readonly targetId: string } {
  const stack = stackProperties(findStack(world, command.deploymentId));
  const output = stack.outputs[targetRef];
  if (output === undefined) {
    throw new CoreError(
      'NotFound',
      `SSM command target output ${targetRef} does not exist`
    );
  }
  if (output !== targetResource) {
    throw new CoreError(
      'Conflict',
      `SSM command target ${targetResource} does not match ${targetRef}`
    );
  }
  const instance = resourcesForDeployment(
    world,
    command.deploymentId,
    INSTANCE_RESOURCE
  ).find((resource) => resource.properties['refValue'] === targetResource);
  if (!instance) {
    throw new CoreError(
      'NotFound',
      'SSM command target resource does not exist'
    );
  }
  return { resource: instance, targetId: stack.targetId };
}

function scheduledAt(virtualTime: string, afterSeconds: number): string {
  const current = Date.parse(virtualTime);
  if (!Number.isFinite(current)) {
    throw new CoreError('ValidationFailed', 'world virtual time is invalid');
  }
  return new Date(current + afterSeconds * 1000).toISOString();
}

function scheduleCatalogCommand(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  exactFields(
    command.input,
    ['targetRef', 'targetResource', 'documentName', 'parameters', 'revert'],
    'SSM command input'
  );
  const targetRef = stringValue(command.input['targetRef'], 'targetRef');
  const targetResource = stringValue(
    command.input['targetResource'],
    'targetResource'
  );
  const documentName = stringValue(
    command.input['documentName'],
    'documentName'
  );
  if (documentName !== 'AWS-RunShellScript') {
    throw new CoreError(
      'UnsupportedCapability',
      `SSM document ${documentName} is not supported`
    );
  }
  const parameters = boundedDocument(command.input['parameters'], 'parameters');
  const parameterLines = documentLines(parameters, 'parameters');
  const revert = objectValue(command.input['revert'], 'revert');
  exactFields(
    revert,
    ['afterSeconds', 'documentName', 'paramTemplate'],
    'revert'
  );
  const afterSeconds = numberValue(
    revert['afterSeconds'],
    'revert.afterSeconds'
  );
  if (
    !Number.isInteger(afterSeconds) ||
    afterSeconds < 1 ||
    afterSeconds > MAX_REVERT_AFTER_SECONDS
  ) {
    throw new CoreError(
      'ValidationFailed',
      `revert.afterSeconds must be an integer from 1 through ${MAX_REVERT_AFTER_SECONDS}`
    );
  }
  const revertDocumentName =
    optionalString(revert['documentName'], 'revert.documentName') ??
    documentName;
  if (revertDocumentName !== 'AWS-RunShellScript') {
    throw new CoreError(
      'UnsupportedCapability',
      `SSM revert document ${revertDocumentName} is not supported`
    );
  }
  const revertParameters = boundedDocument(
    revert['paramTemplate'],
    'revert.paramTemplate'
  );
  const catalog = catalogTarget(command, world, targetRef, targetResource);
  const target = catalog.resource;
  const scheduledRevertAt = scheduledAt(world.world.virtualTime, afterSeconds);
  const commandId = deterministicId('command', {
    worldId: command.worldId,
    deploymentId: command.deploymentId,
    targetId: catalog.targetId,
    targetRef,
    targetResource,
    documentName,
    parameters,
    revert,
    requestedAt: world.world.virtualTime,
  });
  const transitionId = deterministicId('transition', {
    commandId,
    targetId: catalog.targetId,
    scheduledRevertAt,
  });
  const transition = {
    transitionId,
    status: 'Scheduled',
    scheduledAt: scheduledRevertAt,
    documentName: revertDocumentName,
    parameters: revertParameters,
  } as const;
  const targetStored = storedProperties(target);
  const targetState = mutableState(stateObject(targetStored));
  for (const line of parameterLines) applyCatalogCommand(line, targetState);
  const updatedTarget: ResourceDeclaration = {
    provider: target.provider,
    resourceType: target.resourceType,
    resourceId: target.resourceId,
    properties: { ...target.properties, state: targetState },
  };
  const resource = declaration({
    resourceType: SSM_COMMAND_RESOURCE,
    resourceId: commandId,
    properties: {
      logicalId: commandId,
      physicalId: commandId,
      refValue: commandId,
      dependsOn: [target.resourceId],
      attributes: { TransitionId: transitionId },
      templateProperties: {
        targetRef,
        targetResource,
        documentName,
        parameters,
        revert,
      },
      status: 'SUCCESS',
      targetRef,
      targetResource,
      documentName,
      parameters,
      state: {
        status: 'Success',
        requestedAt: world.world.virtualTime,
        transition,
      },
    },
  });
  return {
    events: [
      {
        type: 'AwsSsmCommandScheduled',
        payload: {
          commandId,
          resourceId: commandId,
          targetResource,
          transitionId,
          scheduledRevertAt,
        },
      },
    ],
    resources: [updatedTarget, resource],
    deletedResourceIds: [],
    outputs: {},
    response: { commandId, status: 'Success', scheduledRevertAt },
  };
}

interface DueTransition {
  readonly resource: ResourceRecord;
  readonly targetId: string;
  readonly transition: Readonly<Record<string, unknown>>;
  readonly transitionId: string;
  readonly scheduledAt: string;
}

function dueTransitions(
  input: ProviderClockInput,
  world: ProviderWorldView
): readonly DueTransition[] {
  const targetTime = Date.parse(input.virtualTime);
  if (!Number.isFinite(targetTime)) {
    throw new CoreError('ValidationFailed', 'clock target time is invalid');
  }
  return awsResources(world, SSM_COMMAND_RESOURCE)
    .flatMap((resource) => {
      const state = stateObject(storedProperties(resource));
      const transition = objectValue(
        state['transition'],
        'SSM command transition'
      );
      if (transition['status'] !== 'Scheduled') return [];
      const transitionId = stringValue(
        transition['transitionId'],
        'transitionId'
      );
      const scheduledAt = stringValue(
        transition['scheduledAt'],
        'transition scheduledAt'
      );
      const scheduledTime = Date.parse(scheduledAt);
      if (!Number.isFinite(scheduledTime)) {
        throw new CoreError(
          'ValidationFailed',
          `transition ${transitionId} scheduledAt is invalid`
        );
      }
      return scheduledTime <= targetTime
        ? [
            {
              resource,
              targetId: resource.targetId,
              transition,
              transitionId,
              scheduledAt,
            },
          ]
        : [];
    })
    .sort((left, right) =>
      `${left.scheduledAt}\u0000${left.targetId}\u0000${left.transitionId}`.localeCompare(
        `${right.scheduledAt}\u0000${right.targetId}\u0000${right.transitionId}`
      )
    );
}

function transitionTargetKey(
  command: ResourceRecord,
  targetResource: string
): string {
  return JSON.stringify([
    command.deploymentId,
    command.targetId,
    targetResource,
  ]);
}

function transitionTarget(
  command: ResourceRecord,
  targetResource: string,
  world: ProviderWorldView,
  updatedTargets: ReadonlyMap<string, ResourceRecord>
): ResourceRecord {
  const key = transitionTargetKey(command, targetResource);
  const existing = updatedTargets.get(key);
  if (existing) return existing;
  const target = resourcesForDeployment(
    world,
    command.deploymentId,
    INSTANCE_RESOURCE
  ).find(
    (resource) =>
      resource.targetId === command.targetId &&
      storedProperties(resource).refValue === targetResource
  );
  if (!target) {
    throw new CoreError(
      'NotFound',
      `SSM transition target ${targetResource} does not exist`
    );
  }
  return target;
}

export function advanceSsmClock(
  input: ProviderClockInput,
  world: ProviderWorldView
): ProviderClockResult {
  const due = dueTransitions(input, world);
  const expiredSessions = expireSsmSessions(input, world);
  const updatedTargets = new Map<string, ResourceRecord>();
  const commandResources: ResourceRecord[] = [];
  const events = [];
  const appliedTransitionIds: string[] = [];
  for (const item of due) {
    const commandProperties = storedProperties(item.resource);
    const targetResource = stringValue(
      commandProperties['targetResource'],
      'targetResource'
    );
    const documentName = stringValue(
      item.transition['documentName'],
      'transition documentName'
    );
    if (documentName !== 'AWS-RunShellScript') {
      throw new CoreError(
        'UnsupportedCapability',
        `SSM transition document ${documentName} is not supported`
      );
    }
    const parameters = boundedDocument(
      item.transition['parameters'],
      'transition parameters'
    );
    const target = transitionTarget(
      item.resource,
      targetResource,
      world,
      updatedTargets
    );
    const targetState = mutableState(stateObject(storedProperties(target)));
    for (const line of documentLines(parameters, 'transition parameters')) {
      applyCatalogCommand(line, targetState);
    }
    updatedTargets.set(transitionTargetKey(item.resource, targetResource), {
      ...target,
      properties: { ...target.properties, state: targetState },
    });
    const commandState = stateObject(commandProperties);
    commandResources.push({
      ...item.resource,
      properties: {
        ...item.resource.properties,
        status: 'REVERTED',
        state: {
          ...commandState,
          status: 'Reverted',
          transition: {
            ...item.transition,
            status: 'Applied',
            appliedAt: input.virtualTime,
          },
        },
      },
    });
    events.push({
      type: 'AwsSsmCommandReverted',
      payload: {
        commandId: item.resource.resourceId,
        targetResource,
        transitionId: item.transitionId,
        appliedAt: input.virtualTime,
      },
    });
    appliedTransitionIds.push(item.transitionId);
  }
  return {
    events: [...events, ...expiredSessions.events],
    resources: [
      ...updatedTargets.values(),
      ...commandResources,
      ...expiredSessions.resources,
    ],
    deletedResourceRefs: [],
    appliedTransitionIds: [
      ...appliedTransitionIds,
      ...expiredSessions.appliedTransitionIds,
    ],
  };
}

function reduceSendCommand(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  if (command.resourceType === SSM_COMMAND_RESOURCE) {
    return scheduleCatalogCommand(command, world);
  }
  if (command.resourceType !== INSTANCE_RESOURCE) {
    throw new CoreError(
      'UnsupportedCapability',
      `SSM SendCommand resource ${command.resourceType} is not supported`
    );
  }
  return sendCommand(command, world);
}

export function reduceSsm(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  switch (command.operation) {
    case 'StartSession':
    case 'ResumeSession':
    case 'TerminateSession':
      return reduceSsmSession(command, world);
    case 'GetParameter': {
      const name = stringValue(command.input['Name'], 'Name');
      return result('AwsSsmParameterRead', {
        Parameter: parameterDocument(findParameter(world, name)),
      });
    }
    case 'GetParameters': {
      const names = stringArray(command.input['Names'], 'Names');
      const available = new Map(
        allParameters(world).map((resource) => [
          parameterName(resource),
          resource,
        ])
      );
      return result('AwsSsmParametersRead', {
        Parameters: names.flatMap((name) => {
          const resource = available.get(name);
          return resource ? [parameterDocument(resource)] : [];
        }),
        InvalidParameters: names.filter((name) => !available.has(name)),
      });
    }
    case 'GetParametersByPath': {
      const path = stringValue(command.input['Path'], 'Path').replace(
        /\/$/,
        ''
      );
      const recursive =
        command.input['Recursive'] === undefined
          ? false
          : booleanValue(command.input['Recursive'], 'Recursive');
      const prefix = `${path}/`;
      const parameters = allParameters(world).filter((resource) => {
        const name = parameterName(resource);
        if (!name.startsWith(prefix)) return false;
        return recursive || !name.slice(prefix.length).includes('/');
      });
      return result('AwsSsmParametersByPathRead', {
        Parameters: parameters.map(parameterDocument),
      });
    }
    case 'DescribeParameters':
      return result('AwsSsmParametersDescribed', {
        Parameters: allParameters(world).map((resource) => {
          const parameter = parameterDocument(resource);
          return {
            Name: parameter.Name,
            Type: parameter.Type,
            Version: parameter.Version,
            LastModifiedDate: parameter.LastModifiedDate,
            DataType: parameter.DataType,
          };
        }),
      });
    case 'PutParameter': {
      const name = stringValue(command.input['Name'], 'Name');
      const value = stringValue(command.input['Value'], 'Value');
      const type =
        command.input['Type'] === undefined
          ? 'String'
          : stringValue(command.input['Type'], 'Type');
      const overwrite =
        command.input['Overwrite'] === undefined
          ? false
          : booleanValue(command.input['Overwrite'], 'Overwrite');
      const existing = awsResources(world, PARAMETER_RESOURCE).find(
        (resource) => parameterName(resource) === name
      );
      if (existing && !overwrite) {
        throw new CoreError('Conflict', 'SSM parameter already exists');
      }
      if (existing) {
        const stored = storedProperties(existing);
        const state = stateObject(stored);
        const version =
          typeof state['version'] === 'number' ? state['version'] + 1 : 2;
        const updated = updateStoredResource(existing, {
          templateProperties: {
            ...stored.templateProperties,
            Name: name,
            Type: type,
            Value: value,
          },
          attributes: { ...stored.attributes, Value: value },
          state: {
            ...state,
            version,
            lastModifiedDate: world.world.virtualTime,
          },
        });
        return result(
          'AwsSsmParameterPut',
          { Version: version, Tier: 'Standard' },
          [updated]
        );
      }
      const resourceId = deterministicId('ssm', {
        worldId: command.worldId,
        deploymentId: command.deploymentId,
        name,
      });
      const created = declaration({
        resourceType: PARAMETER_RESOURCE,
        resourceId,
        properties: {
          logicalId: resourceId,
          physicalId: name,
          refValue: name,
          dependsOn: [],
          attributes: {
            Arn: `arn:aws:ssm:us-east-1:000000000000:parameter${name}`,
            Value: value,
          },
          templateProperties: { Name: name, Type: type, Value: value },
          status: 'CREATE_COMPLETE',
          state: { version: 1, lastModifiedDate: world.world.virtualTime },
        },
      });
      return result('AwsSsmParameterPut', { Version: 1, Tier: 'Standard' }, [
        created,
      ]);
    }
    case 'SendCommand':
      return reduceSendCommand(command, world);
    default:
      throw new CoreError(
        'UnsupportedCapability',
        `SSM operation ${command.operation} is not supported`
      );
  }
}
