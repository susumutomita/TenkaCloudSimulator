# ADR-0007: Event-sourced core と provider plugin を採用する

## Status

Accepted

## Context

TenkaCloud の cloud problem をローカル実行するには、IaC の resource 作成だけでなく、CLI、
SDK、Console から観測できる API、状態遷移、権限、network、data plane を再現する必要が
あります。旧 Kumo 経路は IAM と SSM の materialize に留まり、current catalog の大半を
実行できませんでした。

Issue 2574 は 4 provider の Composite target、deterministic replay、snapshot、capability
coverage、未実装の loud failure を同時に求めます。provider ごとの独立 emulator を並べると、
shared world と coverage の source of truth が分散します。

## Decision

Simulator は append-only event log と projection を持つ provider-independent core を採用します。
provider 固有の API、resource、reducer、projection は共通 plugin contract の実装として登録
します。core は provider package を import せず、provider 名による分岐を持ちません。

API spec は request validation、response serialization、error envelope を生成します。挙動は
provider reducer と invariant に明示し、spec だけから推測しません。

deploy 前に problem requirement と capability registry を全 target について比較します。不足が
あれば resource event と workload effect を作らず、source location 付き diagnostic を返します。

Composite の source は versioned artifact bundle とし、runtime target ごとに ID、provider、
engine、entry path が一致する entry 本文と artifact set だけを compiler へ渡します。単一の
本文を全 provider へ暗黙に流用することを protocol の前提にしません。

SQLite event store を初期の durable source of truth とします。snapshot は projection の
checkpoint であり、event log を置き換えません。seed と virtual clock を world に固定します。

## Consequences

- API、CLI、Console、IaC が 1 つの world と event sequence を共有できる。
- provider を追加しても core の分岐を増やさず、同じ contract test を再利用できる。
- Composite target の policy、network、clock、snapshot を 1 つに保てる。
- current catalog の不足を provider、resource、operation、fidelity 単位で検出できる。
- event schema、projection migration、effect runner の設計と試験が必要である。
- native API の protocol fidelity は provider ごとに明示して実装する必要がある。
- event payload の redaction と snapshot validation が security boundary になる。

## Rejected alternatives

- 既存 emulator の寄せ集めは source of truth と clock が分散するため採用しません。
- IaC から container だけを生成する方式は participant の CLI 操作を再現できないため採用
  しません。
- provider logic を core の switch に置く方式は Azure、GCP、Sakura の追加で core invariant を
  破るため採用しません。
