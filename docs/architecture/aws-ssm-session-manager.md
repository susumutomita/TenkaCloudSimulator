# AWS SSM Session Manager native compatibility

## Scope

The simulator accepts the standard AWS CLI command below for catalog EC2
instances. The endpoint path carries the simulator world and deployment because
AWS CLI does not provide a supported way to add the simulator routing headers
to `StartSession`, `ResumeSession`, and `TerminateSession` requests.

```bash
aws ssm start-session \
  --target i-0123456789abcdef0 \
  --endpoint-url http://127.0.0.1:7777/v1/native/aws/<world-id>/<deployment-id>
```

`StartSession` returns the AWS response fields `SessionId`, `StreamUrl`, and
`TokenValue`. `StreamUrl` is a simulator-owned WebSocket URL. The bearer token
is bound to one world, deployment, target, and session and is rotated by
`ResumeSession`. `TerminateSession` permanently invalidates the session.

Only the default `Standard_Stream` shell session is supported. Session
documents, port forwarding, SSH tunnelling, KMS session encryption, and remote
host forwarding remain unsupported.

## Protocol contract

The implementation follows the primary AWS implementations:

- [`aws/session-manager-plugin`](https://github.com/aws/session-manager-plugin)
  opens the `StreamUrl`, then sends a text `OpenDataChannelInput` containing
  `MessageSchemaVersion`, `RequestId`, `TokenValue`, `ClientId`, and
  `ClientVersion`.
- [`clientmessage.go`](https://github.com/aws/session-manager-plugin/blob/mainline/src/message/clientmessage.go)
  defines the 120-byte, big-endian binary envelope. Its stored header length is
  116 bytes; the four-byte payload length follows it before the payload.
- [`streaming.go`](https://github.com/aws/session-manager-plugin/blob/mainline/src/datachannel/streaming.go)
  defines ordered input/output stream messages, acknowledgements, handshake
  payloads, and retransmission behaviour.
- [`amazon-ssm-agent`](https://github.com/aws/amazon-ssm-agent)
  is the reference for the agent side of the `SessionType` handshake.

After the text token exchange the simulator sends a `HandshakeRequest` for
`Standard_Stream`, acknowledges every valid sequential client frame, and sends
`HandshakeComplete` before the prompt. Server output remains buffered until the
plugin acknowledges it and is retransmitted with a finite bound.

## Security and state boundaries

- The WebSocket URL is accepted only on the configured simulator origin.
- The session token is bearer authentication. It is single-use for each data
  channel connection and is rotated before a reconnect.
- Session lookup always includes world and deployment. A token or session ID
  from another tenant route fails closed.
- Input is line-buffered with explicit size limits. Terminal resize frames are
  accepted but do not mutate the world.
- Completed lines are submitted to the existing `AWS-RunShellScript` catalog
  reducer. Unsupported lines return an in-session error. No input is ever
  passed to `sh`, `bash`, `Bun.spawn`, or another host process.
- Idle data channels are terminated after the bounded server timeout.
  Disconnected, timed-out, and terminated channels cannot continue to mutate
  state. Only a non-expired disconnected session can be resumed.

## Fidelity gate

Unit tests against a locally reimplemented client are necessary but are not
sufficient to advertise L4. `StartSession` reached L4 only after the released
AWS CLI 2.34.14 and released Session Manager Plugin 1.2.835.0 completed the
standard command, protocol handshake, catalog command, and termination path.
The reproducible check is:

```bash
SSM_SESSION_PLUGIN_BINARY=/absolute/path/to/session-manager-plugin \
  bun --cwd apps/server run verify:ssm-plugin
```

`ResumeSession` and `TerminateSession` keep L0/L1 fidelity. Their reducer,
tenant isolation, token rotation, and WebSocket lifecycle have real integration
coverage, but automatic reconnect through a released plugin has not yet been
used as an L4 claim.
