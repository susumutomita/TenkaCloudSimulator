# ADR-0011: provider-neutral clock hook で scheduled transition を適用する

## Status

Accepted

## Context

Battle catalog は障害発火後の復旧や phase 進行を wall clock ではなく再現可能な virtual time で
進める必要があります。schedule の意味を core に追加すると AWS SSM、Azure operation、GCP
revision など provider 固有の分岐が core に入り、plugin boundary を壊します。一方、provider
reducer が直接 timer や process を起動すると event replay と snapshot が非決定的になります。

## Decision

ProviderModule に optional な純粋 clock hook を追加します。hook は直前の world view と target
virtual time を受け取り、自 provider の更新 resource、削除 identity、event、適用済み
transition ID を返します。core は provider ID 順で hook を評価し、quota と所有 provider を
検証してから、clock と全結果を 1 SQLite transaction で保存します。

公開 API は `POST /v1/worlds/{worldId}/clock/advance` とし、正の safe integer milliseconds だけを
受け取ります。response は新しい clock と provider/transition ID の組を返します。hook は shell、
network、container、real cloud API を呼びません。

## Consequences

- 同じ event log、resource projection、advance 値から同じ transition が適用される。
- core は provider 固有 schedule schema を知りません。
- provider は transition を resource projection に永続化し、適用済み状態を明示する必要がある。
- workload の外部 effect は clock hook ではなく effect runner へ委譲する。
