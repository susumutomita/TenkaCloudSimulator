import { afterEach, describe, expect, it } from 'bun:test';
import { createHash } from 'node:crypto';
import {
  AwsNativeGateway,
  type AwsNativeGatewayCommand,
} from '../src/native-gateway';
import {
  cleanupContexts,
  createContext,
  resourceByLogicalId,
  type TestContext,
} from './support';

const ORIGIN = 'https://aws.simulator.test';
const ACCESS_KEY_ID = 'TCSIMLOCALACCESS01';

afterEach(cleanupContexts);

function instanceId(context: TestContext): string {
  const value = resourceByLogicalId(context, 'Instance').properties['refValue'];
  if (typeof value !== 'string')
    throw new Error('fixture instance ID is missing');
  return value;
}

function request(
  context: TestContext,
  operation: string,
  input: Readonly<Record<string, unknown>>,
  sequence: number,
  deploymentId = context.deploymentId
): Request {
  const body = JSON.stringify(input);
  const headers = new Headers({
    'content-type': 'application/x-amz-json-1.1',
    'x-amz-date': '20260712T010203Z',
    'x-amz-target': `AmazonSSM.${operation}`,
  });
  const signature = createHash('sha256')
    .update(`${sequence}:${operation}:${body}`)
    .digest('hex');
  headers.set(
    'authorization',
    `AWS4-HMAC-SHA256 Credential=${ACCESS_KEY_ID}/20260712/us-east-1/ssm/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-target, Signature=${signature}`
  );
  return new Request(
    `${ORIGIN}/v1/native/aws/${encodeURIComponent(context.worldId)}/${encodeURIComponent(deploymentId)}`,
    { method: 'POST', headers, body }
  );
}

describe('AWS CLI SSM native session route', () => {
  it('独自routing headerなしのendpoint pathでStart Resume Terminateをcoreへ接続する', async () => {
    const context = await createContext();
    const completed: AwsNativeGatewayCommand[] = [];
    const gateway = new AwsNativeGateway({
      simulatorOrigin: ORIGIN,
      simulatorAccessKeyId: ACCESS_KEY_ID,
      onCommandSuccess: (command) => {
        completed.push(command);
      },
    });
    const startedResponse = await gateway.handle(
      request(context, 'StartSession', { Target: instanceId(context) }, 1),
      context.core
    );
    expect(startedResponse.status).toBe(200);
    const started = (await startedResponse.json()) as Record<string, unknown>;
    expect(started['StreamUrl']).toStartWith(
      'wss://aws.simulator.test/v1/native/aws/ssm/data-channel/'
    );
    expect(completed[0]?.worldId).toBe(context.worldId);
    const sessionId = started['SessionId'];
    const firstToken = started['TokenValue'];
    expect(typeof sessionId).toBe('string');
    const resumedResponse = await gateway.handle(
      request(context, 'ResumeSession', { SessionId: sessionId }, 2),
      context.core
    );
    const resumed = (await resumedResponse.json()) as Record<string, unknown>;
    expect(resumedResponse.status).toBe(200);
    expect(resumed['TokenValue']).not.toBe(firstToken);
    const terminatedResponse = await gateway.handle(
      request(context, 'TerminateSession', { SessionId: sessionId }, 3),
      context.core
    );
    expect(terminatedResponse.status).toBe(200);
    expect(await terminatedResponse.json()).toEqual({ SessionId: sessionId });
  });

  it('endpoint pathの別deploymentからsessionを観測できない', async () => {
    const context = await createContext();
    const gateway = new AwsNativeGateway({
      simulatorOrigin: ORIGIN,
      simulatorAccessKeyId: ACCESS_KEY_ID,
    });
    const response = await gateway.handle(
      request(
        context,
        'StartSession',
        { Target: instanceId(context) },
        4,
        'another-deployment'
      ),
      context.core
    );
    expect(response.status).toBe(404);
  });
});
