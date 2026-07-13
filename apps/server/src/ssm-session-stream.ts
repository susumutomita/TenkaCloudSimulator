import { createHash, randomUUID, timingSafeEqual } from 'node:crypto';
import { CoreError, type SimulationCore } from '@tenkacloud/simulator-core';
import {
  AWS_PROVIDER,
  type AwsNativeGatewayCommand,
  CLOUDFORMATION_ENGINE,
  SSM_SESSION_RESOURCE,
} from '@tenkacloud/simulator-provider-aws';

const DATA_CHANNEL_PATH = /^\/v1\/native\/aws\/ssm\/data-channel\/([^/]+)$/;
const HEADER_LENGTH = 116;
const PAYLOAD_OFFSET = 120;
const MESSAGE_TYPE_OFFSET = 4;
const MESSAGE_TYPE_LENGTH = 32;
const SCHEMA_VERSION_OFFSET = 36;
const CREATED_DATE_OFFSET = 40;
const SEQUENCE_NUMBER_OFFSET = 48;
const FLAGS_OFFSET = 56;
const MESSAGE_ID_OFFSET = 64;
const PAYLOAD_DIGEST_OFFSET = 80;
const PAYLOAD_TYPE_OFFSET = 112;
const PAYLOAD_LENGTH_OFFSET = 116;
const MAX_LINE_BYTES = 64 * 1024;
const MAX_UNACKNOWLEDGED_MESSAGES = 64;
const RESEND_INTERVAL_MILLISECONDS = 250;
const MAX_RESEND_ATTEMPTS = 20;
const DEFAULT_IDLE_TIMEOUT_MILLISECONDS = 20 * 60 * 1000;
const AGENT_VERSION = '3.3.4515.0';

const INPUT_STREAM = 'input_stream_data';
const OUTPUT_STREAM = 'output_stream_data';
const ACKNOWLEDGE = 'acknowledge';
const CHANNEL_CLOSED = 'channel_closed';

const PAYLOAD_OUTPUT = 1;
const PAYLOAD_SIZE = 3;
const PAYLOAD_HANDSHAKE_REQUEST = 5;
const PAYLOAD_HANDSHAKE_RESPONSE = 6;
const PAYLOAD_HANDSHAKE_COMPLETE = 7;
const PAYLOAD_FLAG = 10;

export interface SsmStreamMessage {
  readonly messageType: string;
  readonly sequenceNumber: number;
  readonly flags: bigint;
  readonly messageId: string;
  readonly payloadType: number;
  readonly payload: Uint8Array;
}

export interface SsmSessionSocketData {
  readonly sessionId: string;
}

interface PendingOutput {
  readonly bytes: Uint8Array;
  readonly messageId: string;
  attempts: number;
  timer: ReturnType<typeof setTimeout> | undefined;
}

interface RegisteredSession {
  readonly sessionId: string;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
  readonly target: string;
  tokenValue: string;
  tokenConsumed: boolean;
  status:
    | 'awaiting'
    | 'connecting'
    | 'connected'
    | 'disconnected'
    | 'terminated';
  authenticated: boolean;
  expectedInputSequence: number;
  outputSequence: number;
  lineBytes: number[];
  pendingOutput: Map<number, PendingOutput>;
  socket: Bun.ServerWebSocket<SsmSessionSocketData> | undefined;
  idleTimer: ReturnType<typeof setTimeout> | undefined;
  queue: Promise<void>;
}

interface StoredSession {
  readonly sessionId: string;
  readonly worldId: string;
  readonly deploymentId: string;
  readonly targetId: string;
  readonly target: string;
  readonly tokenValue: string;
}

export interface SsmSessionStreamOptions {
  readonly core: SimulationCore;
  readonly idleTimeoutMilliseconds?: number;
}

function uuidBytes(value: string): Uint8Array {
  const compact = value.replaceAll('-', '');
  if (!/^[0-9a-f]{32}$/i.test(compact)) {
    throw new TypeError('SSM stream message ID must be a UUID');
  }
  const standard = Uint8Array.from(Buffer.from(compact, 'hex'));
  return Uint8Array.from([...standard.slice(8), ...standard.slice(0, 8)]);
}

function uuidString(bytes: Uint8Array): string {
  const standard = Uint8Array.from([...bytes.slice(8), ...bytes.slice(0, 8)]);
  const value = Buffer.from(standard).toString('hex');
  return `${value.slice(0, 8)}-${value.slice(8, 12)}-${value.slice(12, 16)}-${value.slice(16, 20)}-${value.slice(20)}`;
}

function messageType(bytes: Uint8Array): string {
  return new TextDecoder()
    .decode(
      bytes.slice(
        MESSAGE_TYPE_OFFSET,
        MESSAGE_TYPE_OFFSET + MESSAGE_TYPE_LENGTH
      )
    )
    .replaceAll('\0', '')
    .trim();
}

function payloadDigest(payload: Uint8Array): Uint8Array {
  return new Uint8Array(createHash('sha256').update(payload).digest());
}

export function serializeSsmStreamMessage(
  message: Omit<SsmStreamMessage, 'messageId'> & { readonly messageId?: string }
): Uint8Array {
  if (
    new TextEncoder().encode(message.messageType).byteLength >
    MESSAGE_TYPE_LENGTH
  ) {
    throw new TypeError('SSM stream message type is too long');
  }
  if (
    !Number.isSafeInteger(message.sequenceNumber) ||
    message.sequenceNumber < 0
  ) {
    throw new TypeError('SSM stream sequence number is invalid');
  }
  if (!Number.isInteger(message.payloadType) || message.payloadType < 0) {
    throw new TypeError('SSM stream payload type is invalid');
  }
  const payload = new Uint8Array(message.payload);
  const bytes = Buffer.alloc(PAYLOAD_OFFSET + payload.byteLength);
  bytes.writeUInt32BE(HEADER_LENGTH, 0);
  bytes.fill(
    0x20,
    MESSAGE_TYPE_OFFSET,
    MESSAGE_TYPE_OFFSET + MESSAGE_TYPE_LENGTH
  );
  bytes.write(message.messageType, MESSAGE_TYPE_OFFSET, 'utf8');
  bytes.writeUInt32BE(1, SCHEMA_VERSION_OFFSET);
  bytes.writeBigUInt64BE(BigInt(Date.now()), CREATED_DATE_OFFSET);
  bytes.writeBigInt64BE(BigInt(message.sequenceNumber), SEQUENCE_NUMBER_OFFSET);
  bytes.writeBigUInt64BE(message.flags, FLAGS_OFFSET);
  bytes.set(uuidBytes(message.messageId ?? randomUUID()), MESSAGE_ID_OFFSET);
  bytes.set(payloadDigest(payload), PAYLOAD_DIGEST_OFFSET);
  bytes.writeUInt32BE(message.payloadType, PAYLOAD_TYPE_OFFSET);
  bytes.writeUInt32BE(payload.byteLength, PAYLOAD_LENGTH_OFFSET);
  bytes.set(payload, PAYLOAD_OFFSET);
  return new Uint8Array(bytes);
}

export function parseSsmStreamMessage(value: Uint8Array): SsmStreamMessage {
  const bytes = Buffer.from(value);
  if (
    bytes.byteLength < PAYLOAD_OFFSET ||
    bytes.readUInt32BE(0) !== HEADER_LENGTH
  ) {
    throw new TypeError('SSM stream header is invalid');
  }
  const payloadLength = bytes.readUInt32BE(PAYLOAD_LENGTH_OFFSET);
  if (payloadLength !== bytes.byteLength - PAYLOAD_OFFSET) {
    throw new TypeError('SSM stream payload length is invalid');
  }
  if (bytes.readUInt32BE(SCHEMA_VERSION_OFFSET) !== 1) {
    throw new TypeError('SSM stream schema version is invalid');
  }
  const createdDate = bytes.readBigUInt64BE(CREATED_DATE_OFFSET);
  const sequenceNumber = bytes.readBigInt64BE(SEQUENCE_NUMBER_OFFSET);
  if (
    createdDate === 0n ||
    sequenceNumber < 0n ||
    sequenceNumber > BigInt(Number.MAX_SAFE_INTEGER)
  ) {
    throw new TypeError('SSM stream message metadata is invalid');
  }
  const payload = new Uint8Array(bytes.subarray(PAYLOAD_OFFSET));
  const expectedDigest = payloadDigest(payload);
  const receivedDigest = bytes.subarray(
    PAYLOAD_DIGEST_OFFSET,
    PAYLOAD_DIGEST_OFFSET + expectedDigest.byteLength
  );
  if (!timingSafeEqual(receivedDigest, expectedDigest)) {
    throw new TypeError('SSM stream payload digest is invalid');
  }
  return {
    messageType: messageType(bytes),
    sequenceNumber: Number(sequenceNumber),
    flags: bytes.readBigUInt64BE(FLAGS_OFFSET),
    messageId: uuidString(
      bytes.subarray(MESSAGE_ID_OFFSET, MESSAGE_ID_OFFSET + 16)
    ),
    payloadType: bytes.readUInt32BE(PAYLOAD_TYPE_OFFSET),
    payload,
  };
}

function jsonPayload(value: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(value));
}

function jsonRecord(
  payload: Uint8Array,
  label: string
): Record<string, unknown> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(
      new TextDecoder('utf-8', { fatal: true }).decode(payload)
    );
  } catch {
    throw new TypeError(`${label} is invalid JSON`);
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new TypeError(`${label} must be an object`);
  }
  return { ...parsed } as Record<string, unknown>;
}

function fixedTimeEqual(left: string, right: string): boolean {
  const leftBytes = Buffer.from(left);
  const rightBytes = Buffer.from(right);
  return (
    leftBytes.byteLength === rightBytes.byteLength &&
    timingSafeEqual(leftBytes, rightBytes)
  );
}

function boundedIdleTimeout(value: number | undefined): number {
  const timeout = value ?? DEFAULT_IDLE_TIMEOUT_MILLISECONDS;
  if (
    !Number.isSafeInteger(timeout) ||
    timeout < 50 ||
    timeout > 24 * 60 * 60 * 1000
  ) {
    throw new TypeError('SSM idle timeout must be between 50ms and 24 hours');
  }
  return timeout;
}

function responseString(
  response: Readonly<Record<string, unknown>>,
  key: string
): string {
  const value = response[key];
  if (typeof value !== 'string' || !value) {
    throw new TypeError(`SSM ${key} response is invalid`);
  }
  return value;
}

function inputString(command: AwsNativeGatewayCommand, key: string): string {
  const value = command.command.input[key];
  if (typeof value !== 'string' || !value) {
    throw new TypeError(`SSM ${key} input is invalid`);
  }
  return value;
}

export class SsmSessionStreamGateway {
  readonly websocket: Bun.WebSocketHandler<SsmSessionSocketData>;
  readonly #core: SimulationCore;
  readonly #idleTimeoutMilliseconds: number;
  readonly #sessions = new Map<string, RegisteredSession>();

  constructor(options: SsmSessionStreamOptions) {
    this.#core = options.core;
    this.#idleTimeoutMilliseconds = boundedIdleTimeout(
      options.idleTimeoutMilliseconds
    );
    this.websocket = {
      data: {} as SsmSessionSocketData,
      maxPayloadLength: MAX_LINE_BYTES + PAYLOAD_OFFSET,
      backpressureLimit: 1024 * 1024,
      closeOnBackpressureLimit: true,
      sendPings: true,
      open: (socket) => this.#open(socket),
      message: (socket, message) => this.#enqueue(socket, message),
      close: (socket) => this.#close(socket),
    };
  }

  beforeAwsCommand(command: AwsNativeGatewayCommand): void {
    if (command.service !== 'ssm' || command.operation !== 'ResumeSession')
      return;
    const session = this.#sessions.get(inputString(command, 'SessionId'));
    if (session && session.status !== 'disconnected') {
      throw new CoreError('Conflict', 'SSM session is not disconnected');
    }
  }

  onAwsCommandSuccess(
    command: AwsNativeGatewayCommand,
    response: Readonly<Record<string, unknown>>
  ): void {
    if (command.service !== 'ssm') return;
    if (
      command.operation === 'StartSession' ||
      command.operation === 'ResumeSession'
    ) {
      const sessionId = responseString(response, 'SessionId');
      const tokenValue = responseString(response, 'TokenValue');
      const stored = this.#storedSession(
        command.worldId,
        command.command.deploymentId,
        sessionId,
        tokenValue,
        command.command.targetId
      );
      this.#register(stored);
      return;
    }
    if (command.operation === 'TerminateSession') {
      this.terminate(
        responseString(response, 'SessionId'),
        'Session terminated'
      );
    }
  }

  handles(request: Request): boolean {
    return DATA_CHANNEL_PATH.test(new URL(request.url).pathname);
  }

  upgrade(
    request: Request,
    server: Bun.Server<SsmSessionSocketData>
  ): Response | undefined {
    const url = new URL(request.url);
    const pathMatch = DATA_CHANNEL_PATH.exec(url.pathname);
    if (!pathMatch) return new Response('Not Found', { status: 404 });
    if (
      request.method !== 'GET' ||
      request.headers.get('upgrade')?.toLowerCase() !== 'websocket'
    ) {
      return new Response('WebSocket upgrade required', { status: 426 });
    }
    let sessionId: string;
    try {
      sessionId = decodeURIComponent(pathMatch[1] ?? '');
    } catch {
      return new Response('Invalid session route', { status: 400 });
    }
    const worldId = url.searchParams.get('worldId') ?? '';
    const deploymentId = url.searchParams.get('deploymentId') ?? '';
    const targetId = url.searchParams.get('targetId') ?? '';
    let session = this.#sessions.get(sessionId);
    try {
      session ??= this.#register(
        this.#storedSession(
          worldId,
          deploymentId,
          sessionId,
          undefined,
          targetId
        )
      );
    } catch {
      return new Response('Session not found', { status: 404 });
    }
    if (
      session.worldId !== worldId ||
      session.deploymentId !== deploymentId ||
      session.targetId !== targetId ||
      session.tokenConsumed ||
      session.status === 'connected' ||
      session.status === 'connecting' ||
      session.status === 'terminated'
    ) {
      return new Response('Session not found', { status: 404 });
    }
    session.status = 'connecting';
    if (!server.upgrade(request, { data: { sessionId } })) {
      session.status = 'awaiting';
      return new Response('WebSocket upgrade failed', { status: 400 });
    }
    return undefined;
  }

  terminate(sessionId: string, reason: string): void {
    const session = this.#sessions.get(sessionId);
    if (!session) return;
    session.status = 'terminated';
    session.tokenValue = '';
    this.#clearSessionTimers(session);
    const socket = session.socket;
    if (!socket) return;
    socket.sendBinary(
      serializeSsmStreamMessage({
        messageType: CHANNEL_CLOSED,
        sequenceNumber: 0,
        flags: 3n,
        payloadType: 0,
        payload: jsonPayload({
          MessageId: randomUUID(),
          CreatedDate: String(Date.now()),
          DestinationId: session.target,
          SessionId: session.sessionId,
          MessageType: CHANNEL_CLOSED,
          SchemaVersion: 1,
          Output: reason,
        }),
      })
    );
    socket.close(1000, reason.slice(0, 123));
  }

  #storedSession(
    worldId: string,
    deploymentId: string,
    sessionId: string,
    expectedToken?: string,
    expectedTargetId?: string
  ): StoredSession {
    const world = this.#core.world(worldId);
    const resources = this.#core.store
      .resources(worldId)
      .filter(
        (candidate) =>
          candidate.provider === AWS_PROVIDER &&
          candidate.resourceType === SSM_SESSION_RESOURCE &&
          candidate.deploymentId === deploymentId &&
          candidate.resourceId === sessionId &&
          (expectedTargetId === undefined ||
            candidate.targetId === expectedTargetId)
      );
    const resource = resources.length === 1 ? resources[0] : undefined;
    const properties = resource?.properties;
    const state =
      properties &&
      typeof properties['state'] === 'object' &&
      properties['state'] !== null
        ? (properties['state'] as Readonly<Record<string, unknown>>)
        : undefined;
    const target = properties?.['target'];
    const storedToken = state?.['tokenValue'];
    const expiresAt = state?.['expiresAt'];
    if (
      !resource ||
      state?.['status'] !== 'Active' ||
      typeof target !== 'string' ||
      typeof storedToken !== 'string' ||
      storedToken.length === 0 ||
      typeof expiresAt !== 'string' ||
      Date.parse(expiresAt) <= Date.parse(world.virtualTime) ||
      (expectedToken !== undefined &&
        !fixedTimeEqual(storedToken, expectedToken))
    ) {
      throw new CoreError('NotFound', 'SSM session does not exist');
    }
    return {
      sessionId,
      worldId,
      deploymentId,
      targetId: resource.targetId,
      target,
      tokenValue: storedToken,
    };
  }

  #register(stored: StoredSession): RegisteredSession {
    const previous = this.#sessions.get(stored.sessionId);
    if (previous) this.#clearSessionTimers(previous);
    const session: RegisteredSession = {
      ...stored,
      tokenConsumed: false,
      status: 'awaiting',
      authenticated: false,
      expectedInputSequence: 0,
      outputSequence: 0,
      lineBytes: [],
      pendingOutput: new Map(),
      socket: undefined,
      idleTimer: undefined,
      queue: Promise.resolve(),
    };
    this.#sessions.set(session.sessionId, session);
    return session;
  }

  #open(socket: Bun.ServerWebSocket<SsmSessionSocketData>): void {
    const session = this.#sessions.get(socket.data.sessionId);
    if (session?.status !== 'connecting') {
      socket.close(1008, 'Session not found');
      return;
    }
    session.socket = socket;
    session.status = 'connected';
    this.#touch(session);
  }

  #enqueue(
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    message: string | Buffer
  ): void {
    const session = this.#sessions.get(socket.data.sessionId);
    if (
      !session ||
      session.socket !== socket ||
      session.status !== 'connected'
    ) {
      socket.close(1008, 'Session not found');
      return;
    }
    session.queue = session.queue
      .then(() => this.#message(session, socket, message))
      .catch(() => socket.close(1002, 'Invalid SSM data channel message'));
  }

  async #message(
    session: RegisteredSession,
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    message: string | Buffer
  ): Promise<void> {
    this.#touch(session);
    if (!session.authenticated) {
      this.#authenticateDataChannel(session, socket, message);
      return;
    }
    await this.#streamMessage(session, socket, message);
  }

  #authenticateDataChannel(
    session: RegisteredSession,
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    message: string | Buffer
  ): void {
    if (typeof message !== 'string')
      throw new TypeError('token message must be text');
    const input = jsonRecord(
      new TextEncoder().encode(message),
      'OpenDataChannel'
    );
    const validToken =
      typeof input['TokenValue'] === 'string' &&
      fixedTimeEqual(input['TokenValue'], session.tokenValue);
    session.tokenConsumed = true;
    if (
      input['MessageSchemaVersion'] !== '1.0' ||
      typeof input['RequestId'] !== 'string' ||
      typeof input['ClientId'] !== 'string' ||
      typeof input['ClientVersion'] !== 'string' ||
      !validToken
    ) {
      throw new TypeError('OpenDataChannel token is invalid');
    }
    session.authenticated = true;
    this.#sendOutput(
      session,
      socket,
      PAYLOAD_HANDSHAKE_REQUEST,
      jsonPayload({
        AgentVersion: AGENT_VERSION,
        RequestedClientActions: [
          {
            ActionType: 'SessionType',
            ActionParameters: {
              SessionType: 'Standard_Stream',
              Properties: {},
            },
          },
        ],
      })
    );
  }

  async #streamMessage(
    session: RegisteredSession,
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    message: string | Buffer
  ): Promise<void> {
    if (typeof message === 'string')
      throw new TypeError('stream message must be binary');
    const parsed = parseSsmStreamMessage(message);
    if (parsed.messageType === ACKNOWLEDGE) {
      this.#acknowledge(session, parsed);
      return;
    }
    if (parsed.messageType !== INPUT_STREAM) {
      throw new TypeError('unexpected SSM stream message type');
    }
    if (parsed.sequenceNumber < session.expectedInputSequence) {
      this.#sendAcknowledge(socket, parsed);
      return;
    }
    if (parsed.sequenceNumber !== session.expectedInputSequence) {
      throw new TypeError('out-of-order SSM stream message');
    }
    this.#sendAcknowledge(socket, parsed);
    session.expectedInputSequence += 1;
    switch (parsed.payloadType) {
      case PAYLOAD_HANDSHAKE_RESPONSE:
        this.#completeHandshake(session, socket, parsed.payload);
        return;
      case PAYLOAD_SIZE:
        jsonRecord(parsed.payload, 'terminal size');
        return;
      case PAYLOAD_OUTPUT:
        await this.#handleInput(session, socket, parsed.payload);
        return;
      case PAYLOAD_FLAG:
        if (parsed.payload.byteLength !== 4)
          throw new TypeError('session flag is invalid');
        if (Buffer.from(parsed.payload).readUInt32BE(0) === 2) {
          await this.#timeout(session, 'Session terminated');
        }
        return;
      default:
        throw new TypeError('unsupported SSM stream payload type');
    }
  }

  #completeHandshake(
    session: RegisteredSession,
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    payload: Uint8Array
  ): void {
    const response = jsonRecord(payload, 'handshake response');
    const actions = response['ProcessedClientActions'];
    if (
      !Array.isArray(actions) ||
      !actions.some(
        (action) =>
          action !== null &&
          typeof action === 'object' &&
          !Array.isArray(action) &&
          (action as Record<string, unknown>)['ActionType'] === 'SessionType' &&
          (action as Record<string, unknown>)['ActionStatus'] === 1
      )
    ) {
      throw new TypeError('SessionType handshake failed');
    }
    this.#sendOutput(
      session,
      socket,
      PAYLOAD_HANDSHAKE_COMPLETE,
      jsonPayload({ HandshakeTimeToComplete: 0, CustomerMessage: '' })
    );
    this.#sendOutput(
      session,
      socket,
      PAYLOAD_OUTPUT,
      new TextEncoder().encode('$ ')
    );
  }

  #sendAcknowledge(
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    message: SsmStreamMessage
  ): void {
    socket.sendBinary(
      serializeSsmStreamMessage({
        messageType: ACKNOWLEDGE,
        sequenceNumber: 0,
        flags: 3n,
        payloadType: 0,
        payload: jsonPayload({
          AcknowledgedMessageType: message.messageType,
          AcknowledgedMessageId: message.messageId,
          AcknowledgedMessageSequenceNumber: message.sequenceNumber,
          IsSequentialMessage: true,
        }),
      })
    );
  }

  #sendOutput(
    session: RegisteredSession,
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    payloadType: number,
    payload: Uint8Array
  ): void {
    if (session.pendingOutput.size >= MAX_UNACKNOWLEDGED_MESSAGES) {
      throw new CoreError(
        'QuotaExceeded',
        'SSM output acknowledgement window is full'
      );
    }
    const sequenceNumber = session.outputSequence;
    session.outputSequence += 1;
    const messageId = randomUUID();
    const bytes = serializeSsmStreamMessage({
      messageType: OUTPUT_STREAM,
      sequenceNumber,
      flags: 0n,
      messageId,
      payloadType,
      payload,
    });
    socket.sendBinary(bytes);
    const pending: PendingOutput = {
      bytes,
      messageId,
      attempts: 0,
      timer: undefined,
    };
    session.pendingOutput.set(sequenceNumber, pending);
    this.#scheduleResend(session, sequenceNumber, pending);
  }

  #scheduleResend(
    session: RegisteredSession,
    sequenceNumber: number,
    pending: PendingOutput
  ): void {
    pending.timer = setTimeout(() => {
      if (!session.pendingOutput.has(sequenceNumber) || !session.socket) return;
      if (pending.attempts >= MAX_RESEND_ATTEMPTS) {
        session.socket.close(1011, 'SSM acknowledgement timed out');
        return;
      }
      pending.attempts += 1;
      session.socket.sendBinary(pending.bytes);
      this.#scheduleResend(session, sequenceNumber, pending);
    }, RESEND_INTERVAL_MILLISECONDS);
  }

  #acknowledge(session: RegisteredSession, message: SsmStreamMessage): void {
    const acknowledgement = jsonRecord(message.payload, 'acknowledgement');
    const sequenceNumber = acknowledgement['AcknowledgedMessageSequenceNumber'];
    const messageId = acknowledgement['AcknowledgedMessageId'];
    const messageTypeValue = acknowledgement['AcknowledgedMessageType'];
    if (
      !Number.isSafeInteger(sequenceNumber) ||
      typeof sequenceNumber !== 'number' ||
      typeof messageId !== 'string' ||
      messageTypeValue !== OUTPUT_STREAM
    ) {
      throw new TypeError('SSM acknowledgement is invalid');
    }
    const pending = session.pendingOutput.get(sequenceNumber);
    if (!pending || pending.messageId !== messageId) {
      throw new TypeError('SSM acknowledgement does not match output');
    }
    if (pending.timer) clearTimeout(pending.timer);
    session.pendingOutput.delete(sequenceNumber);
  }

  async #handleInput(
    session: RegisteredSession,
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    payload: Uint8Array
  ): Promise<void> {
    if (payload.byteLength > 0) {
      this.#sendOutput(session, socket, PAYLOAD_OUTPUT, payload);
    }
    for (const byte of payload) {
      if (byte === 3) {
        session.lineBytes = [];
        this.#sendOutput(
          session,
          socket,
          PAYLOAD_OUTPUT,
          new TextEncoder().encode('^C\r\n$ ')
        );
        continue;
      }
      if (byte === 8 || byte === 127) {
        session.lineBytes.pop();
        continue;
      }
      if (byte === 10 || byte === 13) {
        const line = new TextDecoder('utf-8', { fatal: true }).decode(
          Uint8Array.from(session.lineBytes)
        );
        session.lineBytes = [];
        await this.#executeLine(session, socket, line.trim());
        continue;
      }
      if (byte < 32 && byte !== 9) {
        throw new TypeError('unsupported terminal control byte');
      }
      if (session.lineBytes.length >= MAX_LINE_BYTES) {
        throw new CoreError('QuotaExceeded', 'SSM command line is too large');
      }
      session.lineBytes.push(byte);
    }
  }

  async #executeLine(
    session: RegisteredSession,
    socket: Bun.ServerWebSocket<SsmSessionSocketData>,
    line: string
  ): Promise<void> {
    if (!line) {
      this.#sendOutput(
        session,
        socket,
        PAYLOAD_OUTPUT,
        new TextEncoder().encode('\r\n$ ')
      );
      return;
    }
    if (line === 'exit' || line === 'logout') {
      await this.#timeout(session, 'Session ended');
      return;
    }
    let output = '\r\n$ ';
    try {
      await this.#core.executeCommandAsync(
        session.worldId,
        {
          deploymentId: session.deploymentId,
          targetId: session.targetId,
          provider: AWS_PROVIDER,
          engine: CLOUDFORMATION_ENGINE,
          service: 'ssm',
          operation: 'SendCommand',
          resourceType: 'AWS::EC2::Instance',
          input: {
            DocumentName: 'AWS-RunShellScript',
            InstanceIds: [session.target],
            Parameters: { commands: [line] },
          },
        },
        `ssm-stream:${session.sessionId}:${session.expectedInputSequence}:${createHash('sha256').update(line).digest('hex')}`
      );
    } catch (error) {
      const message =
        error instanceof Error ? error.message : 'SSM command failed';
      output = `\r\n${message}\r\n$ `;
    }
    this.#sendOutput(
      session,
      socket,
      PAYLOAD_OUTPUT,
      new TextEncoder().encode(output)
    );
  }

  #touch(session: RegisteredSession): void {
    if (session.idleTimer) clearTimeout(session.idleTimer);
    session.idleTimer = setTimeout(() => {
      void this.#timeout(session, 'Session timed out');
    }, this.#idleTimeoutMilliseconds);
  }

  async #timeout(session: RegisteredSession, reason: string): Promise<void> {
    if (session.status === 'terminated') return;
    try {
      await this.#core.executeCommandAsync(
        session.worldId,
        {
          deploymentId: session.deploymentId,
          targetId: session.targetId,
          provider: AWS_PROVIDER,
          engine: CLOUDFORMATION_ENGINE,
          service: 'ssm',
          operation: 'TerminateSession',
          resourceType: SSM_SESSION_RESOURCE,
          input: { SessionId: session.sessionId },
        },
        `ssm-stream-terminate:${session.sessionId}`
      );
    } finally {
      this.terminate(session.sessionId, reason);
    }
  }

  #close(socket: Bun.ServerWebSocket<SsmSessionSocketData>): void {
    const session = this.#sessions.get(socket.data.sessionId);
    if (!session || session.socket !== socket) return;
    session.socket = undefined;
    session.authenticated = false;
    this.#clearSessionTimers(session);
    if (session.status !== 'terminated') session.status = 'disconnected';
  }

  #clearSessionTimers(session: RegisteredSession): void {
    if (session.idleTimer) clearTimeout(session.idleTimer);
    session.idleTimer = undefined;
    for (const pending of session.pendingOutput.values()) {
      if (pending.timer) clearTimeout(pending.timer);
    }
    session.pendingOutput.clear();
  }
}
