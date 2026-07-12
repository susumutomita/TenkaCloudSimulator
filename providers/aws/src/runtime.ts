import {
  CoreError,
  deterministicId,
  type ProviderCommandInput,
  type ProviderCommandResult,
  type ProviderWorldView,
  type ResourceDeclaration,
} from '@tenkacloud/simulator-core';
import { declaration, RUNTIME_ENDPOINT_RESOURCE } from './model';
import {
  resourcesForDeployment,
  result,
  stateObject,
  storedProperties,
  updateStoredResource,
} from './state';
import {
  booleanValue,
  objectValue,
  optionalString,
  stringValue,
} from './value';

export interface RuntimeEndpointCompileInput {
  readonly metadata?: unknown;
  readonly outputs: Readonly<Record<string, string>>;
  readonly problemId: string;
  readonly targetId: string;
  readonly stackId: string;
  readonly stackName: string;
}

function endpointUrl(base: string, appendPath: string | undefined): string {
  if (!base || appendPath === undefined) return base;
  let parsed: URL;
  try {
    parsed = new URL(base);
  } catch {
    throw new CoreError(
      'ValidationFailed',
      'runtime endpoint CloudFormation output must be an HTTP URL'
    );
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new CoreError(
      'ValidationFailed',
      'runtime endpoint CloudFormation output must use HTTP or HTTPS'
    );
  }
  return new URL(
    appendPath.replace(/^\//, ''),
    `${parsed.toString()}/`
  ).toString();
}

export function compileRuntimeEndpoints(
  input: RuntimeEndpointCompileInput
): readonly ResourceDeclaration[] {
  if (input.metadata === undefined) return [];
  const metadata = objectValue(input.metadata, 'deployment metadata');
  const value = metadata['endpoints'];
  if (value === undefined) return [];
  if (!Array.isArray(value)) {
    throw new CoreError(
      'ValidationFailed',
      'deployment metadata endpoints must be an array'
    );
  }
  const slots = new Set<string>();
  return value.map((entry, index) => {
    const endpoint = objectValue(entry, `metadata.endpoints[${index}]`);
    const slot = stringValue(
      endpoint['slot'],
      `metadata.endpoints[${index}].slot`
    );
    if (slots.has(slot)) {
      throw new CoreError(
        'ValidationFailed',
        `runtime endpoint slot ${slot} is duplicated`
      );
    }
    slots.add(slot);
    const defaultValue = objectValue(
      endpoint['default'],
      `metadata.endpoints[${index}].default`
    );
    if (defaultValue['from'] !== 'cfn-output') {
      throw new CoreError(
        'UnsupportedCapability',
        `runtime endpoint slot ${slot} source is not supported`
      );
    }
    const outputKey = stringValue(
      defaultValue['key'],
      `metadata.endpoints[${index}].default.key`
    );
    const output = input.outputs[outputKey];
    if (output === undefined) {
      throw new CoreError(
        'ValidationFailed',
        `runtime endpoint slot ${slot} references missing output ${outputKey}`
      );
    }
    const appendPath = optionalString(
      defaultValue['appendPath'],
      `metadata.endpoints[${index}].default.appendPath`
    );
    const overridable = booleanValue(
      endpoint['overridable'] ?? false,
      `metadata.endpoints[${index}].overridable`
    );
    const resourceId = deterministicId('runtime-endpoint', {
      problemId: input.problemId,
      targetId: input.targetId,
      slot,
    });
    return declaration({
      resourceType: RUNTIME_ENDPOINT_RESOURCE,
      resourceId,
      properties: {
        logicalId: `RuntimeEndpoint.${slot}`,
        physicalId: `${input.stackId}:${slot}`,
        refValue: slot,
        dependsOn: [input.stackName],
        attributes: {},
        templateProperties: {
          Slot: slot,
          OutputKey: outputKey,
          ...(appendPath === undefined ? {} : { AppendPath: appendPath }),
          Overridable: overridable,
        },
        status: 'CREATE_PENDING',
        Slot: slot,
        ProblemId: input.problemId,
        TargetId: input.targetId,
        OutputKey: outputKey,
        DefaultUrl: endpointUrl(output, appendPath),
        Overridable: overridable,
      },
    });
  });
}

function validatedOverride(value: unknown): string {
  const text = stringValue(value, 'OverrideUrl');
  if (text.length > 2048) {
    throw new CoreError('ValidationFailed', 'OverrideUrl is too long');
  }
  let parsed: URL;
  try {
    parsed = new URL(text);
  } catch {
    throw new CoreError('ValidationFailed', 'OverrideUrl must be a URL');
  }
  if (
    (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') ||
    parsed.username ||
    parsed.password ||
    parsed.hash
  ) {
    throw new CoreError(
      'ValidationFailed',
      'OverrideUrl must be a credential-free HTTP URL without a fragment'
    );
  }
  return parsed.toString();
}

function endpointResponse(
  resource: ReturnType<typeof findEndpoint>,
  url: string
): Readonly<Record<string, unknown>> {
  const properties = storedProperties(resource);
  return {
    Slot: properties.refValue,
    Url: url,
    Source: 'override',
    Overridable: properties.templateProperties['Overridable'] === true,
    OutputKey: properties.templateProperties['OutputKey'],
  };
}

function findEndpoint(
  command: ProviderCommandInput,
  world: ProviderWorldView,
  slot: string,
  targetId: string
) {
  const endpoint = resourcesForDeployment(
    world,
    command.deploymentId,
    RUNTIME_ENDPOINT_RESOURCE
  ).find(
    (resource) =>
      resource.properties['Slot'] === slot &&
      resource.properties['TargetId'] === targetId
  );
  if (!endpoint) {
    throw new CoreError('NotFound', 'runtime endpoint does not exist');
  }
  return endpoint;
}

export function reduceRuntime(
  command: ProviderCommandInput,
  world: ProviderWorldView
): ProviderCommandResult {
  if (command.operation !== 'ResolveEndpoint') {
    throw new CoreError(
      'UnsupportedCapability',
      `Runtime operation ${command.operation} is not supported`
    );
  }
  const slot = stringValue(command.input['Slot'], 'Slot');
  const targetId = stringValue(command.input['TargetId'], 'TargetId');
  const endpoint = findEndpoint(command, world, slot, targetId);
  const properties = storedProperties(endpoint);
  const state = stateObject(properties);
  if (command.input['OverrideUrl'] !== undefined) {
    if (properties.templateProperties['Overridable'] !== true) {
      throw new CoreError('Conflict', 'runtime endpoint is not overridable');
    }
    const overrideUrl = validatedOverride(command.input['OverrideUrl']);
    return result(
      'AwsRuntimeEndpointOverridden',
      endpointResponse(endpoint, overrideUrl),
      [updateStoredResource(endpoint, { state: { ...state, overrideUrl } })]
    );
  }
  const overrideUrl = state['overrideUrl'];
  if (typeof overrideUrl !== 'string') {
    throw new CoreError('Conflict', 'runtime endpoint workload is unavailable');
  }
  return result(
    'AwsRuntimeEndpointResolved',
    endpointResponse(endpoint, overrideUrl)
  );
}
