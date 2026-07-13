import { afterEach, describe, expect, it } from 'bun:test';
import type {
  ExecuteCommandInput,
  ProviderCommandInput,
  ProviderWorldView,
} from '@tenkacloud/simulator-core';
import { SSM_SESSION_RESOURCE } from '../src/model';
import { expireSsmSessions, reduceSsmSession } from '../src/ssm-session';
import {
  cleanupContexts,
  createContext,
  execute,
  resourceByLogicalId,
  type TestContext,
} from './support';

afterEach(cleanupContexts);

function instanceId(context: TestContext): string {
  const value = resourceByLogicalId(context, 'Instance').properties['refValue'];
  if (typeof value !== 'string')
    throw new Error('fixture instance ID is missing');
  return value;
}

function sessionInput(
  context: TestContext,
  requestDigit: string
): Readonly<Record<string, unknown>> {
  return {
    Target: instanceId(context),
    __SimulatorOrigin: 'http://127.0.0.1:7777',
    __SimulatorRequestId: `tcsim-${requestDigit.repeat(24)}`,
  };
}

function sessionCommand(
  context: TestContext,
  operation: string,
  input: Readonly<Record<string, unknown>>,
  deploymentId = context.deploymentId
): ExecuteCommandInput {
  return {
    deploymentId,
    targetId: 'default',
    provider: 'aws',
    engine: 'cloudformation',
    service: 'ssm',
    operation,
    resourceType: SSM_SESSION_RESOURCE,
    input,
  };
}

function responseString(
  response: Readonly<Record<string, unknown>>,
  key: string
): string {
  const value = response[key];
  if (typeof value !== 'string') throw new Error(`${key} is missing`);
  return value;
}

function startSession(
  context: TestContext,
  requestDigit = 'a'
): Readonly<Record<string, unknown>> {
  return execute(
    context,
    'ssm',
    'StartSession',
    sessionInput(context, requestDigit),
    SSM_SESSION_RESOURCE
  );
}

function providerCommand(
  context: TestContext,
  operation: string,
  input: Readonly<Record<string, unknown>>
): ProviderCommandInput {
  return {
    worldId: context.worldId,
    deploymentId: context.deploymentId,
    service: 'ssm',
    operation,
    resourceType: SSM_SESSION_RESOURCE,
    input,
  };
}

function providerWorld(context: TestContext): ProviderWorldView {
  return {
    world: context.core.world(context.worldId),
    resources: context.store.resources(context.worldId),
  };
}

function replaceResourceState(
  context: TestContext,
  resourceId: string,
  state: Readonly<Record<string, unknown>>
): void {
  const resource = context.store
    .resources(context.worldId)
    .find((candidate) => candidate.resourceId === resourceId);
  if (!resource) throw new Error(`resource ${resourceId} is missing`);
  context.store.saveResource({
    ...resource,
    properties: { ...resource.properties, state },
  });
}

describe('AWS SSM Session Manager reducer', () => {
  it('StartSession が対象EC2へ標準Stream URLと単一session tokenを発行する', async () => {
    const context = await createContext();
    const response = execute(
      context,
      'ssm',
      'StartSession',
      sessionInput(context, 'a'),
      SSM_SESSION_RESOURCE
    );
    const sessionId = responseString(response, 'SessionId');
    expect(responseString(response, 'TokenValue')).not.toBe('');
    const streamUrl = new URL(responseString(response, 'StreamUrl'));
    expect(streamUrl.protocol).toBe('ws:');
    expect(streamUrl.pathname).toBe(
      `/v1/native/aws/ssm/data-channel/${sessionId}`
    );
    expect(streamUrl.searchParams.get('worldId')).toBe(context.worldId);
    expect(streamUrl.searchParams.get('deploymentId')).toBe(
      context.deploymentId
    );
    expect(streamUrl.searchParams.get('targetId')).toBe('default');
    expect(streamUrl.searchParams.has('tokenValue')).toBeFalse();
    expect(
      context.store
        .resources(context.worldId)
        .find((resource) => resource.resourceId === sessionId)?.properties[
        'target'
      ]
    ).toBe(instanceId(context));
  });

  it('同じ instance と request の session identity を Composite target ごとに分離する', async () => {
    const context = await createContext();
    const command = providerCommand(
      context,
      'StartSession',
      sessionInput(context, '9')
    );
    const base = providerWorld(context);
    const primary = reduceSsmSession(command, base);
    const secondary = reduceSsmSession(command, {
      ...base,
      resources: base.resources.map((resource) => ({
        ...resource,
        targetId: 'secondary',
      })),
    });
    const primaryId = responseString(primary.response, 'SessionId');
    const secondaryId = responseString(secondary.response, 'SessionId');

    expect(primaryId).not.toBe(secondaryId);
    expect(
      new URL(responseString(primary.response, 'StreamUrl')).searchParams.get(
        'targetId'
      )
    ).toBe('default');
    expect(
      new URL(responseString(secondary.response, 'StreamUrl')).searchParams.get(
        'targetId'
      )
    ).toBe('secondary');
  });

  it('ResumeSession はtokenをrotateしTerminate後の再接続を拒否する', async () => {
    const context = await createContext();
    const started = execute(
      context,
      'ssm',
      'StartSession',
      sessionInput(context, 'a'),
      SSM_SESSION_RESOURCE
    );
    const sessionId = responseString(started, 'SessionId');
    const resumed = execute(
      context,
      'ssm',
      'ResumeSession',
      {
        SessionId: sessionId,
        __SimulatorOrigin: 'http://127.0.0.1:7777',
        __SimulatorRequestId: `tcsim-${'b'.repeat(24)}`,
      },
      SSM_SESSION_RESOURCE
    );
    expect(responseString(resumed, 'TokenValue')).not.toBe(
      responseString(started, 'TokenValue')
    );
    expect(
      execute(
        context,
        'ssm',
        'TerminateSession',
        { SessionId: sessionId },
        SSM_SESSION_RESOURCE
      )
    ).toEqual({ SessionId: sessionId });
    expect(() =>
      execute(
        context,
        'ssm',
        'ResumeSession',
        {
          SessionId: sessionId,
          __SimulatorOrigin: 'http://127.0.0.1:7777',
          __SimulatorRequestId: `tcsim-${'c'.repeat(24)}`,
        },
        SSM_SESSION_RESOURCE
      )
    ).toThrow('terminated SSM session cannot be resumed');
  });

  it('virtual clock期限でsessionをTimedOutへ遷移してResumeを拒否する', async () => {
    const context = await createContext();
    const started = execute(
      context,
      'ssm',
      'StartSession',
      sessionInput(context, 'd'),
      SSM_SESSION_RESOURCE
    );
    const sessionId = responseString(started, 'SessionId');
    const advanced = context.core.advanceClock(context.worldId, 20 * 60 * 1000);
    expect(advanced.appliedTransitions).toHaveLength(1);
    const resource = context.store
      .resources(context.worldId)
      .find((candidate) => candidate.resourceId === sessionId);
    expect(resource?.properties['status']).toBe('TIMED_OUT');
    expect(() =>
      execute(
        context,
        'ssm',
        'ResumeSession',
        {
          SessionId: sessionId,
          __SimulatorOrigin: 'http://127.0.0.1:7777',
          __SimulatorRequestId: `tcsim-${'e'.repeat(24)}`,
        },
        SSM_SESSION_RESOURCE
      )
    ).toThrow('SSM session has timed out');
  });

  it('存在しないdeployment routeと未対応session documentを成功扱いしない', async () => {
    const context = await createContext();
    const started = execute(
      context,
      'ssm',
      'StartSession',
      sessionInput(context, 'f'),
      SSM_SESSION_RESOURCE
    );
    const sessionId = responseString(started, 'SessionId');
    expect(() =>
      context.core.executeCommand(
        context.worldId,
        sessionCommand(
          context,
          'TerminateSession',
          { SessionId: sessionId },
          'another-deployment'
        ),
        'cross-deployment-session'
      )
    ).toThrow('deployment does not exist');
    expect(() =>
      execute(
        context,
        'ssm',
        'ResumeSession',
        {
          SessionId: 'session-does-not-exist',
          __SimulatorOrigin: 'http://127.0.0.1:7777',
          __SimulatorRequestId: `tcsim-${'0'.repeat(24)}`,
        },
        SSM_SESSION_RESOURCE
      )
    ).toThrow('SSM session does not exist');
    expect(() =>
      execute(
        context,
        'ssm',
        'StartSession',
        {
          ...sessionInput(context, '1'),
          DocumentName: 'AWS-StartPortForwardingSession',
        },
        SSM_SESSION_RESOURCE
      )
    ).toThrow('is not supported');
  });

  it('origin request field parameter reason target stateの不正境界を拒否する', async () => {
    const fieldContext = await createContext();
    const base = sessionInput(fieldContext, '2');
    const invalidInputs: readonly Readonly<Record<string, unknown>>[] = [
      { ...base, Unexpected: true },
      { ...base, __SimulatorOrigin: 'not-a-url' },
      { ...base, __SimulatorOrigin: 'ftp://example.test' },
      { ...base, __SimulatorRequestId: 'invalid-request-id' },
      { ...base, Parameters: { portNumber: ['22'] } },
      { ...base, Reason: 'x'.repeat(257) },
    ];
    for (const [index, input] of invalidInputs.entries()) {
      expect(() =>
        fieldContext.core.executeCommand(
          fieldContext.worldId,
          sessionCommand(fieldContext, 'StartSession', input),
          `invalid-session-input-${index}`
        )
      ).toThrow();
    }
    expect(
      fieldContext.core.executeCommand(
        fieldContext.worldId,
        sessionCommand(fieldContext, 'StartSession', {
          ...sessionInput(fieldContext, 'a'),
          Parameters: {},
          Reason: 'catalog repair',
        }),
        'valid-optional-session-input'
      )
    ).toHaveProperty('SessionId');

    const stateContext = await createContext();
    const instance = resourceByLogicalId(stateContext, 'Instance');
    const state = instance.properties['state'];
    if (state === null || typeof state !== 'object' || Array.isArray(state)) {
      throw new Error('fixture instance state is missing');
    }
    replaceResourceState(stateContext, instance.resourceId, {
      ...(state as Readonly<Record<string, unknown>>),
      instanceState: 'stopped',
    });
    expect(() => startSession(stateContext, '3')).toThrow(
      'SSM session target is not running'
    );
    expect(() =>
      reduceSsmSession(
        providerCommand(stateContext, 'StartSession', {
          ...sessionInput(stateContext, 'b'),
        }),
        {
          ...providerWorld(stateContext),
          resources: providerWorld(stateContext).resources.filter(
            (resource) => resource.resourceType !== 'AWS::EC2::Instance'
          ),
        }
      )
    ).toThrow('SSM session target does not exist');
  });

  it('壊れたsession projectionと純粋reducerのunknown operationを成功扱いしない', async () => {
    const statusContext = await createContext();
    const statusSessionId = responseString(
      startSession(statusContext, '4'),
      'SessionId'
    );
    replaceResourceState(statusContext, statusSessionId, {
      status: 'Broken',
      tokenValue: 'token',
      generation: 0,
      expiresAt: '2026-07-12T00:20:00.000Z',
    });
    expect(() =>
      execute(
        statusContext,
        'ssm',
        'ResumeSession',
        {
          SessionId: statusSessionId,
          __SimulatorOrigin: 'http://127.0.0.1:7777',
          __SimulatorRequestId: `tcsim-${'5'.repeat(24)}`,
        },
        SSM_SESSION_RESOURCE
      )
    ).toThrow('SSM session status is invalid');

    const generationContext = await createContext();
    const generationSessionId = responseString(
      startSession(generationContext, '6'),
      'SessionId'
    );
    replaceResourceState(generationContext, generationSessionId, {
      status: 'Active',
      tokenValue: 'token',
      generation: -1,
      expiresAt: '2026-07-12T00:20:00.000Z',
    });
    expect(() =>
      execute(
        generationContext,
        'ssm',
        'ResumeSession',
        {
          SessionId: generationSessionId,
          __SimulatorOrigin: 'http://127.0.0.1:7777',
          __SimulatorRequestId: `tcsim-${'7'.repeat(24)}`,
        },
        SSM_SESSION_RESOURCE
      )
    ).toThrow('SSM session generation is invalid');
    replaceResourceState(generationContext, generationSessionId, {
      status: 'Active',
      tokenValue: 'token',
      generation: 0,
      expiresAt: 'invalid',
    });
    expect(() =>
      generationContext.core.advanceClock(generationContext.worldId, 1)
    ).toThrow('session expiresAt is invalid');

    const timeoutContext = await createContext();
    const timeoutSessionId = responseString(
      startSession(timeoutContext, '8'),
      'SessionId'
    );
    timeoutContext.core.advanceClock(timeoutContext.worldId, 20 * 60 * 1000);
    expect(() =>
      execute(
        timeoutContext,
        'ssm',
        'TerminateSession',
        { SessionId: timeoutSessionId },
        SSM_SESSION_RESOURCE
      )
    ).toThrow('SSM session has timed out');

    expect(() =>
      reduceSsmSession(
        providerCommand(timeoutContext, 'UnknownSessionOperation', {}),
        providerWorld(timeoutContext)
      )
    ).toThrow('SSM session operation UnknownSessionOperation is not supported');

    const invalidClockWorld: ProviderWorldView = {
      ...providerWorld(timeoutContext),
      world: { ...providerWorld(timeoutContext).world, virtualTime: 'invalid' },
    };
    expect(() =>
      reduceSsmSession(
        providerCommand(timeoutContext, 'StartSession', {
          ...sessionInput(timeoutContext, '9'),
        }),
        invalidClockWorld
      )
    ).toThrow('world virtual time is invalid');
    expect(() =>
      expireSsmSessions(
        {
          previousVirtualTime: timeoutContext.core.world(timeoutContext.worldId)
            .virtualTime,
          virtualTime: 'invalid',
        },
        providerWorld(timeoutContext)
      )
    ).toThrow('clock target time is invalid');
  });
});
