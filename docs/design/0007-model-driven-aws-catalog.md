# 0007 Model-driven bounded AWS service catalog

## Context

The backlog contains one Issue per AWS service (#52 and its children). Each asks
for a bounded native workflow: official operations, persisted state, local IAM
authorization, explicit errors for anything unsupported, a console and an API
reference that share the same state, and gate evidence. Writing a hand-made
protocol handler per service, as the first eleven services did, does not scale
to hundreds of services and repeats the same wire, validation and storage code.

## Decision

Add `internal/aws/catalog`, an engine that serves reviewed per-service
specifications against documentation-free subsets of the pinned official
botocore 1.41.6 service models.

- `tools/awsmodels/extract.py` copies only the reviewed operations, their
  transitive shapes, paginators and the HTTP route of every official operation
  into `internal/aws/catalog/models/<service>.json`, with the source file hash.
- A specification (`services_*.go`) binds each official operation to one role:
  create, read, list, update, delete, tag, untag or list tags. It names the
  official resource shape, ARN template, identity members, quotas, parents,
  references, generated members and small `Check` hooks for semantic rules the
  shapes cannot express (for example ASL `StartAt`).
- Requests are decoded from the official binding (JSON 1.0/1.1 targets, or
  REST-JSON labels, query strings, headers and payloads), validated against the
  official input shape (unknown members, required members, types, enums,
  lengths, ranges and RE2-expressible patterns), authorized through the local
  identity policy as `<prefix>:<Operation>` on the resource ARN, executed in one
  SQLite transaction, and projected onto the official output shape.
- Unbound official operations return HTTP 501 `UnknownOperationException`.
  Input members that the bounded model would ignore are refused with the
  operation's own validation exception.
- Capabilities, the console view and the per-service OpenAPI documents are all
  derived from the same descriptors, so they cannot drift from the server.

## Alternatives considered

1. Hand-written handlers per service, as for SQS or KMS. This gives the most
   control but costs thousands of lines per service and duplicates validation,
   error mapping and persistence. Rejected for the backlog; still used where a
   service needs real behavior (crypto, execution, queues).
2. A schema-less generic store that accepts any JSON for any operation name.
   Cheap, but it would silently simulate success for invalid input and invent
   operations, which the working contract forbids. Rejected.
3. Embedding complete botocore models. Simpler extraction, but it would ship
   tens of megabytes and expose operations that are not reviewed. Rejected in
   favor of reviewed subsets plus routing metadata.

## Edge cases and invariants

- Authorization precedes disclosure: a denied caller cannot learn whether a
  resource exists, and denied, invalid, failed or stale writes leave state
  unchanged.
- Writes are guarded by a per-row generation; a concurrent change makes the
  later write fail with a conflict instead of overwriting.
- Identifiers are never reused; client tokens replay the original resource
  for identical input and are refused for different input.
- Parents with children are refused unless the specification documents
  cascading deletion. Referenced ARNs must name live resources.
- Pagination tokens are bound to the operation, region and filters.
- Global services use one partition-wide scope and region-less ARNs.
- Request bodies are limited to 256 KiB, stored descriptions to 64 KiB, all
  catalog metadata to 32 MiB, tags to 50 per resource and client tokens to
  10,000 for 24 hours. These are simulator limits, not AWS quotas.

The finite model `formal/CatalogResourceLifecycle.tla` covers the store-level
invariants above. Wire validation and projection are covered by native tests
and the boto3 end-to-end flows; the model is not a proof of the Go code.

## Consequences

Adding a service means extending `selection.json`, running the extractor,
writing a reviewed specification, regenerating the served reference and adding
SDK evidence. Services that require execution, data planes, networks, OAuth or
external systems keep those operations unbound and documented as unsupported.
