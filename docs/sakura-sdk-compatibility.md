# Sakura Disk metadata and the community SDK

This local, opt-in provider implements bounded Disk configuration metadata. It does
not allocate block storage, boot a VM, attach an Icon, or report physical Availability.

The community `sacloud/sacloud-sdk-go` serializer at commit
`d8e98d8762e049e3ffd40bc70667cf9ad20029c0` emits `Icon: {"ID": 0, "Tags": []}`
for an unset Icon. A real local CLI request initially received HTTP 501 for this
shape. The adapter now recognizes it as an explicit no-icon marker. Existing
`Icon: null` and `{}` remain accepted; numeric `ID: 0` may omit `Tags`, or include
exactly an empty array. String `"0"`, other numbers, booleans, null IDs, null Tags,
nonempty Tags, unknown members, and real Icon IDs remain unsupported (HTTP 501).
These neutral markers do not change persisted metadata or create Icon support.

The compatibility fixture uses the pinned community SDK's real request serializer
and response/error decoder through its public middleware seam, with a terminal
transport restricted to allocated numeric-loopback endpoints. This does not verify
the stock authentication, retry, default-endpoint behavior, or high-level waiters.
It does not establish differential equivalence with the real Sakura service.
SDK acquisition and test dependencies belong to an isolated development module;
the runtime module does not depend on this SDK. Runtime acceptance results must be
reported separately from source inspection and the original native HTTP probes.
