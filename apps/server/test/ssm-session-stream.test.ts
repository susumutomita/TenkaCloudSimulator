import { afterEach, beforeEach, describe, expect, it } from 'bun:test';
import { createHash, randomUUID } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  ProviderRegistry,
  SimulationCore,
  SimulationStore,
} from '@tenkacloud/simulator-core';
import { AwsProvider } from '@tenkacloud/simulator-provider-aws';
import { createNativeGatewayHandler } from '../src/native-app';
import {
  parseSsmStreamMessage,
  type SsmSessionSocketData,
  SsmSessionStreamGateway,
  type SsmStreamMessage,
  serializeSsmStreamMessage,
} from '../src/ssm-session-stream';

const ACCESS_KEY_ID = 'TCSIMLOCALACCESS01';
const CREDENTIALS = {
  awsAccessKeyId: ACCESS_KEY_ID,
  azureCredential: 'tcsim_azure_session_credential',
  gcpCredential: 'tcsim_google_session_credential',
  sakuraCredential: 'tcsim_sakura_session_token:tcsim_sakura_session_secret',
};

interface TestSessionContext {
  readonly worldId: string;
  readonly deploymentId: string;
  readonly instanceId: string;
  readonly origin: string;
  readonly stream: SsmSessionStreamGateway;
}

interface Inbox {
  readonly messages: (string | ArrayBuffer)[];
  readonly waiters: ((value: string | ArrayBuffer) => void)[];
}

let directory: string;
let store: SimulationStore;
let core: SimulationCore;
const servers: Bun.Server<SsmSessionSocketData>[] = [];
const sockets: WebSocket[] = [];
let requestSequence: number;

beforeEach(async () => {
  requestSequence = 0;
  directory = await mkdtemp(join(tmpdir(), 'simulator-ssm-stream-'));
  store = new SimulationStore(join(directory, 'simulation.sqlite'));
  core = new SimulationCore(store, new ProviderRegistry([new AwsProvider()]));
});

afterEach(async () => {
  for (const socket of sockets.splice(0)) socket.close();
  for (const server of servers.splice(0)) void server.stop(true);
  store.close();
  await rm(directory, { recursive: true, force: true });
});

function awsRequest(
  context: Pick<TestSessionContext, 'worldId' | 'deploymentId' | 'origin'>,
  operation: string,
  input: Readonly<Record<string, unknown>>
): Request {
  requestSequence += 1;
  const body = JSON.stringify(input);
  const headers = new Headers({
    'content-type': 'application/x-amz-json-1.1',
    'x-amz-date': '20260712T010203Z',
    'x-amz-target': `AmazonSSM.${operation}`,
  });
  const signature = createHash('sha256')
    .update(`${requestSequence}:${operation}:${body}`)
    .digest('hex');
  headers.set(
    'authorization',
    `AWS4-HMAC-SHA256 Credential=${ACCESS_KEY_ID}/20260712/us-east-1/ssm/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-target, Signature=${signature}`
  );
  return new Request(
    `${context.origin}/v1/native/aws/${context.worldId}/${context.deploymentId}`,
    { method: 'POST', headers, body }
  );
}

async function createSessionContext(
  idleTimeoutMilliseconds = 5_000
): Promise<TestSessionContext> {
  const deploymentId = 'ssm-stream-deployment';
  const world = core.createWorld(
    {
      tenantId: 'ssm-stream-tenant',
      eventId: 'ssm-stream-event',
      teamId: 'ssm-stream-team',
      deploymentId,
      virtualTime: '2026-07-12T00:00:00.000Z',
    },
    'ssm-stream-world'
  );
  const templateBody = await readFile(
    new URL(
      '../../../providers/aws/tests/fixtures/catalog-stack.yaml',
      import.meta.url
    ),
    'utf8'
  );
  const deployment = core.createDeployment(
    world.worldId,
    {
      deploymentId,
      problemId: 'ssm-stream-problem',
      runtime: {
        provider: 'aws',
        engine: 'cloudformation',
        entry: 'template.yaml',
      },
      templateBody,
      metadata: { cfnParameters: { FlagSeed: 'stream-seed' } },
    },
    'ssm-stream-deployment-key'
  );
  const instanceId = deployment.outputs['default']?.['InstanceId'];
  if (!instanceId) throw new Error('fixture instance ID is missing');
  const stream = new SsmSessionStreamGateway({
    core,
    idleTimeoutMilliseconds,
  });
  let nativeGateway: ReturnType<typeof createNativeGatewayHandler> | undefined;
  const server = Bun.serve<SsmSessionSocketData>({
    hostname: '127.0.0.1',
    port: 0,
    fetch: async (request, bunServer) => {
      if (stream.handles(request)) return stream.upgrade(request, bunServer);
      return (
        (await nativeGateway?.(request)) ??
        new Response('Not Found', { status: 404 })
      );
    },
    websocket: stream.websocket,
  });
  servers.push(server);
  nativeGateway = createNativeGatewayHandler({
    core,
    credentials: CREDENTIALS,
    simulatorOrigin: server.url.origin,
    beforeAwsCommand: (command) => stream.beforeAwsCommand(command),
    onAwsCommandSuccess: (command, response) =>
      stream.onAwsCommandSuccess(command, response),
  });
  return {
    worldId: world.worldId,
    deploymentId,
    instanceId,
    origin: server.url.origin,
    stream,
  };
}

async function nativeJson(
  context: TestSessionContext,
  operation: string,
  input: Readonly<Record<string, unknown>>
): Promise<{
  readonly response: Response;
  readonly body: Record<string, unknown>;
}> {
  const response = await fetch(awsRequest(context, operation, input));
  const body = (await response.json()) as Record<string, unknown>;
  return { response, body };
}

function responseString(body: Record<string, unknown>, key: string): string {
  const value = body[key];
  if (typeof value !== 'string') throw new Error(`${key} is missing`);
  return value;
}

function socketInbox(socket: WebSocket): Inbox {
  const inbox: Inbox = { messages: [], waiters: [] };
  socket.binaryType = 'arraybuffer';
  socket.addEventListener('message', (event) => {
    const value = event.data as string | ArrayBuffer;
    const waiter = inbox.waiters.shift();
    if (waiter) waiter(value);
    else inbox.messages.push(value);
  });
  return inbox;
}

function nextMessage(inbox: Inbox): Promise<string | ArrayBuffer> {
  const message = inbox.messages.shift();
  if (message !== undefined) return Promise.resolve(message);
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(
      () => reject(new Error('WebSocket message timed out')),
      2_000
    );
    inbox.waiters.push((value) => {
      clearTimeout(timeout);
      resolve(value);
    });
  });
}

async function openSocket(
  url: string
): Promise<{ socket: WebSocket; inbox: Inbox }> {
  const socket = new WebSocket(url);
  sockets.push(socket);
  const inbox = socketInbox(socket);
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(
      () => reject(new Error('WebSocket open timed out')),
      2_000
    );
    socket.addEventListener('open', () => {
      clearTimeout(timeout);
      resolve();
    });
    socket.addEventListener('error', () => {
      clearTimeout(timeout);
      reject(new Error('WebSocket open failed'));
    });
  });
  return { socket, inbox };
}

function binaryMessage(value: string | ArrayBuffer): SsmStreamMessage {
  if (!(value instanceof ArrayBuffer))
    throw new Error('binary message expected');
  return parseSsmStreamMessage(new Uint8Array(value));
}

function acknowledge(socket: WebSocket, message: SsmStreamMessage): void {
  socket.send(
    Buffer.from(
      serializeSsmStreamMessage({
        messageType: 'acknowledge',
        sequenceNumber: 0,
        flags: 3n,
        payloadType: 0,
        payload: new TextEncoder().encode(
          JSON.stringify({
            AcknowledgedMessageType: message.messageType,
            AcknowledgedMessageId: message.messageId,
            AcknowledgedMessageSequenceNumber: message.sequenceNumber,
            IsSequentialMessage: true,
          })
        ),
      })
    )
  );
}

async function authenticate(
  socket: WebSocket,
  inbox: Inbox,
  tokenValue: string
): Promise<void> {
  socket.send(
    JSON.stringify({
      MessageSchemaVersion: '1.0',
      RequestId: randomUUID().replaceAll('-', ''),
      TokenValue: tokenValue,
      ClientId: randomUUID().replaceAll('-', ''),
      ClientVersion: '1.2.835.0',
    })
  );
  const request = binaryMessage(await nextMessage(inbox));
  expect(request.payloadType).toBe(5);
  acknowledge(socket, request);
  socket.send(
    Buffer.from(
      serializeSsmStreamMessage({
        messageType: 'input_stream_data',
        sequenceNumber: 0,
        flags: 0n,
        payloadType: 6,
        payload: new TextEncoder().encode(
          JSON.stringify({
            ClientVersion: '1.2.835.0',
            ProcessedClientActions: [
              {
                ActionType: 'SessionType',
                ActionStatus: 1,
                ActionResult: null,
                Error: '',
              },
            ],
            Errors: [],
          })
        ),
      })
    )
  );
  let sawComplete = false;
  let sawPrompt = false;
  while (!sawComplete || !sawPrompt) {
    const message = binaryMessage(await nextMessage(inbox));
    if (message.messageType === 'acknowledge') continue;
    acknowledge(socket, message);
    if (message.payloadType === 7) sawComplete = true;
    if (
      message.payloadType === 1 &&
      new TextDecoder().decode(message.payload).includes('$ ')
    ) {
      sawPrompt = true;
    }
  }
}

async function commandOutput(
  socket: WebSocket,
  inbox: Inbox,
  sequenceNumber: number,
  command: string
): Promise<string> {
  socket.send(
    Buffer.from(
      serializeSsmStreamMessage({
        messageType: 'input_stream_data',
        sequenceNumber,
        flags: 0n,
        payloadType: 1,
        payload: new TextEncoder().encode(`${command}\r`),
      })
    )
  );
  let output = '';
  while (!output.includes('$ ')) {
    const message = binaryMessage(await nextMessage(inbox));
    if (message.messageType === 'acknowledge') continue;
    acknowledge(socket, message);
    if (message.payloadType === 1) {
      output += new TextDecoder().decode(message.payload);
    }
  }
  return output;
}

function instanceState(
  context: TestSessionContext
): Readonly<Record<string, unknown>> {
  const resource = store
    .resources(context.worldId)
    .find(
      (candidate) => candidate.properties['refValue'] === context.instanceId
    );
  const state = resource?.properties['state'];
  if (state === null || typeof state !== 'object' || Array.isArray(state)) {
    throw new Error('instance state is missing');
  }
  return state as Readonly<Record<string, unknown>>;
}

describe('SSM Session Manager streaming endpoint', () => {
  it('公式frame handshake後のcatalog shellだけを実state遷移へ接続する', async () => {
    const context = await createSessionContext();
    const started = await nativeJson(context, 'StartSession', {
      Target: context.instanceId,
    });
    expect(started.response.status).toBe(200);
    const streamUrl = responseString(started.body, 'StreamUrl');
    const tokenValue = responseString(started.body, 'TokenValue');
    const { socket, inbox } = await openSocket(streamUrl);
    await authenticate(socket, inbox, tokenValue);
    const accepted = await commandOutput(
      socket,
      inbox,
      1,
      'systemctl stop nginx'
    );
    expect(accepted).toContain('$ ');
    expect(
      (instanceState(context)['services'] as Record<string, unknown>)['nginx']
    ).toBe('stopped');
    const rejected = await commandOutput(socket, inbox, 2, 'uname -a');
    expect(rejected).toContain('outside the catalog reducer');
    const protocolClosed = new Promise<number>((resolve) =>
      socket.addEventListener('close', (event) => resolve(event.code), {
        once: true,
      })
    );
    socket.send(new Uint8Array([1, 2, 3]));
    expect(await protocolClosed).toBe(1002);
  });

  it('切断tokenの再利用と別tenant routeを拒否しResume tokenだけを受理する', async () => {
    const context = await createSessionContext();
    const started = await nativeJson(context, 'StartSession', {
      Target: context.instanceId,
    });
    const streamUrl = responseString(started.body, 'StreamUrl');
    const tokenValue = responseString(started.body, 'TokenValue');
    const opened = await openSocket(streamUrl);
    await authenticate(opened.socket, opened.inbox, tokenValue);
    const closed = new Promise<void>((resolve) =>
      opened.socket.addEventListener('close', () => resolve(), { once: true })
    );
    opened.socket.close();
    await closed;
    await Bun.sleep(10);
    expect(servers[0]?.pendingWebSockets).toBe(0);
    await expect(openSocket(streamUrl)).rejects.toThrow(
      'WebSocket open failed'
    );

    const sessionId = responseString(started.body, 'SessionId');
    const resumed = await nativeJson(context, 'ResumeSession', {
      SessionId: sessionId,
    });
    expect(resumed.response.status).toBe(200);
    const resumedUrl = responseString(resumed.body, 'StreamUrl');
    const wrongTenantUrl = new URL(resumedUrl);
    wrongTenantUrl.searchParams.set('deploymentId', 'another-deployment');
    await expect(openSocket(wrongTenantUrl.toString())).rejects.toThrow(
      'WebSocket open failed'
    );
    const reconnected = await openSocket(resumedUrl);
    await authenticate(
      reconnected.socket,
      reconnected.inbox,
      responseString(resumed.body, 'TokenValue')
    );
  });

  it('idle timeoutがsessionをTerminateしてdata channelを閉じる', async () => {
    const context = await createSessionContext(100);
    const started = await nativeJson(context, 'StartSession', {
      Target: context.instanceId,
    });
    const opened = await openSocket(responseString(started.body, 'StreamUrl'));
    const closed = new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(
        () => reject(new Error('idle close timed out')),
        2_000
      );
      opened.socket.addEventListener(
        'close',
        () => {
          clearTimeout(timeout);
          resolve();
        },
        { once: true }
      );
    });
    await closed;
    const sessionId = responseString(started.body, 'SessionId');
    const session = store
      .resources(context.worldId)
      .find((candidate) => candidate.resourceId === sessionId);
    expect(session?.properties['status']).toBe('TERMINATED');
  });

  it('出力ACK欠落時は同じsequenceを有限回再送してchannelを閉じる', async () => {
    const context = await createSessionContext(10_000);
    const started = await nativeJson(context, 'StartSession', {
      Target: context.instanceId,
    });
    const opened = await openSocket(responseString(started.body, 'StreamUrl'));
    opened.socket.send(
      JSON.stringify({
        MessageSchemaVersion: '1.0',
        RequestId: randomUUID().replaceAll('-', ''),
        TokenValue: responseString(started.body, 'TokenValue'),
        ClientId: randomUUID().replaceAll('-', ''),
        ClientVersion: '1.2.835.0',
      })
    );
    const initial = binaryMessage(await nextMessage(opened.inbox));
    const resent = binaryMessage(await nextMessage(opened.inbox));
    expect(resent.sequenceNumber).toBe(initial.sequenceNumber);
    expect(resent.messageId).toBe(initial.messageId);
    const closed = new Promise<number>((resolve, reject) => {
      const timeout = setTimeout(
        () => reject(new Error('acknowledgement timeout did not close')),
        7_000
      );
      opened.socket.addEventListener(
        'close',
        (event) => {
          clearTimeout(timeout);
          resolve(event.code);
        },
        { once: true }
      );
    });
    expect(await closed).toBe(1011);
  }, 10_000);

  it('digest改変と不正headerをframe parserが拒否する', () => {
    const valid = serializeSsmStreamMessage({
      messageType: 'input_stream_data',
      sequenceNumber: 0,
      flags: 0n,
      payloadType: 1,
      payload: new TextEncoder().encode('systemctl stop nginx\r'),
    });
    const corrupted = new Uint8Array(valid);
    const finalIndex = corrupted.length - 1;
    corrupted[finalIndex] = (corrupted[finalIndex] ?? 0) ^ 1;
    expect(() => parseSsmStreamMessage(corrupted)).toThrow(
      'payload digest is invalid'
    );
    const invalidHeader = new Uint8Array(valid);
    invalidHeader[3] = 0;
    expect(() => parseSsmStreamMessage(invalidHeader)).toThrow(
      'header is invalid'
    );
  });
});
