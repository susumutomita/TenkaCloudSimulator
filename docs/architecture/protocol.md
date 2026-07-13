# Simulator Protocol v1

TenkaCloud、TenkaCloudChallenge、TenkaCloudSimulator の独立 release をつなぐ公開契約の
正本です。wire format の正本は `contracts/schemas/`、HTTP の正本は
`contracts/openapi.yaml` とし、本書は意味と互換規則を定義します。

## Version

API path の major version は `/v1` です。初期 protocol identifier は
`2026-07-11` です。client は mutation request に
`x-tenkacloud-simulator-protocol` header を送り、server は同じ header を response に
返します。capability discovery は version negotiation の入口なので header なしでも
呼び出せます。

- backward compatible な field、operation、capability の追加は identifier を変えない。
- field の削除、意味の変更、既存 error code の再利用は新しい日付 identifier を発行する。
- server は実装する identifier の範囲を公開し、共通範囲がない request を
  `ProtocolVersionMismatch` で拒否する。
- client は unknown field を無視し、unknown enum value は明示的な incompatibility と
  して扱う。
- snapshot schema は API と独立した `snapshotVersion` を持つ。server-authenticated integrity
  proof を必須にした現行 version は `2` である。署名がなく caller による再生成が可能だった
  version `1` は security boundary として失効させ、import や自動移行をしない。
- TenkaCloud は protocol range と Simulator image digest の両方を pin する。

## Lifecycle API

### `GET /v1/capabilities`

protocol version、Simulator version、provider ごとの resource、operation、fidelity、
constraint を返します。capability entry は次の identity を持ちます。

```text
provider / engine / service / resourceType / operation
```

同じ identity の重複登録は process 起動時に失敗します。

初期 consumer との互換 shape は `providers[provider].engines[engine]` の下に
`operations`、`resources`、`fidelity` を置きます。詳細 capability entry は追加 field として
返し、初期 consumer が unknown field を無視できる形にします。詳細 entry の `engine` は server が
常に返しますが、同じ `2026-07-11` protocol の初期 response も検証できるよう受理側では optional な
additive field とします。

### `POST /v1/worlds`

`tenantId`、`eventId`、`teamId`、caller が割り当てた `deploymentId`、seed、virtual clock を
受け取り、`worldId` と `consoleUrl` を返します。
同じ namespace と idempotency key の再送は同じ world を返します。別 payload での
key 再利用は `IdempotencyConflict` です。

### `GET /v1/worlds/by-deployment/{deploymentId}`

world create が SQLite transaction を commit した後に HTTP response が消失し、bounded replay でも
`worldId` を取得できない場合の durable recovery endpoint です。launch token が持つ
tenant / event / team namespace、パスの `deploymentId`、create 時の `idempotency-key` を
`POST /v1/worlds` の永続 idempotency record と照合し、同じ `worldId` / `consoleUrl` shape を
返します。header を省略した場合は create と同じく `deploymentId` と `create-world` operation
から導出するデフォルト key を使います。明示 key で create した caller は lookup にも同じ header を
送らなければなりません。

lookup は namespace / deployment 全体を検索しません。同じ namespace / deployment に snapshot
restore などで複数 world が存在しても、create-world scope と key の response が元の world を
一意に指します。返却された world は同じ idempotency identity で create request を replay するか、
`DELETE /v1/worlds/{worldId}` で cleanup できます。

lookup は deleted world も返します。world delete は idempotent なため、delete response loss 後も
lookup → delete を再実行して cleanup 完了を確定できます。不存在、別 namespace、token と
path の deployment 不一致、unknown / mismatched idempotency key は、列挙を防ぐためすべて同じ
`NotFound` とします。この GET も response に protocol identifier を返し、wire shape の正本は
OpenAPI と v1 world response schema です。

### `GET /v1/worlds/{sourceWorldId}/snapshots/restores/{snapshotHash}`

snapshot restore の transaction commit 後に HTTP response が消失した場合の durable recovery
endpoint です。source world、64 文字 lowercase SHA-256 snapshot hash、restore 時と同じ
`idempotency-key` を restore-snapshot record と deterministic restored world ID に照合します。
header 省略時は restore POST と同じデフォルト key を使います。

source と restored world のどちらか、または両方が deleted でも pointer は残り、lookup と
idempotent delete を再実行できます。別 namespace / deployment の token、別 hash、別 key、
不正 hash、不存在はすべて `NotFound` です。caller は source と restored world を別 identity として
durable に所有し、restored world を採用してから source を cleanup します。

### `POST /v1/worlds/{worldId}/deployments`

`problemId`、single または Composite の `runtime`、`templateBody`、任意の metadata と
`simulationOverlay` を受け取ります。metadata は catalog の metadata.json、overlay は
Challenge で schema 検証済みの simulation.json 本文であり、両者を混ぜません。Composite は
2 から 8 個の target を持ちます。target ID は `^[a-z][a-z0-9-]{0,31}$` に一致しなければならず、
HTTP schema、core、catalog scanner の全境界で同じ規則を適用します。全 target の
capability preflight を先に完了し、不足が 1 件でもあれば resource event を 1 件も
生成せず `UnsupportedCapability` を返します。

single target の `templateBody` は IaC source 本文です。target ごとに異なる source を持つ
Composite では、同じ string field に次の versioned artifact bundle を JSON serialize して
渡します。

```json
{
  "format": "tenkacloud.simulator.artifacts.v1",
  "targets": [
    {
      "id": "aws-main",
      "provider": "aws",
      "engine": "cloudformation",
      "entry": "template.yaml",
      "artifacts": [
        { "path": "template.yaml", "content": "Resources: {}\n" }
      ]
    }
  ]
}
```

bundle target は runtime target と ID、provider、engine、entry path が全て一致する必要が
あり、各 runtime target にちょうど 1 件を要求します。artifact path は problem directory
相対とし、絶対 path、`..`、NUL、重複を拒否します。file entry は同名 artifact、directory
entry は strict prefix 配下だけを含み、`main.tf` を必須 entrypoint とします。
target と artifact は canonical sort し、欠落、余剰 field、空 content、unknown format は
`ValidationFailed` です。bundle marker がない本文は後方互換の raw source として全 target に
渡します。provider compiler は file entry の content、または directory entry の `main.tf`
content と、同じ target の read-only artifact set を受け取ります。

`simulationOverlay` は additive な JSON field ですが、server は schemaVersion、target identity、
OCI digest、artifact hash を provider/effect 境界でも再検証します。reference だけ、unknown
version、未固定 image、scoring/answer/secret field を実行入力として受け付けません。

deploy は event を append した後に projection を更新します。同期完了できない
operation は `accepted` を返し、virtual clock と provider reducer が状態を進めます。

### `GET /v1/worlds/{worldId}/deployments/{deploymentId}`

deployment status、target ごとの status、namespace 付き output、diagnostic を返します。
読み取りは projection を使い、event log の順序を変更しません。

### `DELETE /v1/worlds/{worldId}`

world に属する workload を停止して削除 event を append します。同じ request の再送は
idempotent です。workload projection を持つ deleted world への再送も runner cleanup を再実行します。
これにより、materialization と delete が別の SQLite connection で競合して delete 後に遅れて
container が作られた場合や、cleanup failure 後に process を再起動した場合も、同じ DELETE で
orphan を回収できます。別 namespace の caller からは存在しない world として扱います。

同じ Simulator process では workload materialization と非同期 provider command を world 単位で
直列化します。さらに SQLite の `BEGIN IMMEDIATE` transaction 内で、world ごとの
`materialization` lease と `deletion` intent を相互排他にするため、同じ state file を見る別 process
との DELETE 競合も cleanup 前に停止します。materialization の成功 event 枠は外部 effect の前に
lease として保存され、dead owner の lease だけを回収します。deletion intent は cleanup や最終 commit
が失敗しても削除せず、materialization を継続して拒否します。再送 DELETE または server 起動時の
reconciliation は dead owner の intent を引き継ぎ、cleanup と tombstone を完了してから解放します。
live owner または生死を確認できない owner の intent は引き継がず fail closed にします。

Docker runner は world resource を作る前に OCI digest を明示的に pull / inspect し、container 起動は
`--pull=never` に固定します。DELETE cleanup は container と deterministic world network の双方が
Docker command timeout 以上の連続した quiet window で不在であることを確認してから intent を解放します。
この fence のため、workload を持つ world の DELETE latency には少なくとも設定済み Docker timeout が
加わります。

同期 DELETE の client は、この server-side completion boundary より短い通常 request deadline を
適用しません。server が cleanup と tombstone の完了を所有し、CLI は response を待ちます。library
caller が処理を中断する場合だけ `deleteWorld` へ明示的な `AbortSignal` を渡し、CLI は process signal で
中断します。通常 request のデフォルト 10 秒 deadline と、library caller が指定する最大 10 分の request
timeout policy は維持します。これにより Docker timeout を緩和せず、server が完了を返す前に client
だけが失敗する状態を避けます。

production HTTP transport も同じ completion boundary を所有します。Bun server の global
`idleTimeout` はデフォルトの 10 秒を維持し、query のない exact
`DELETE /v1/worlds/{worldId}` の処理中だけ `server.timeout(request, 0)` で request 単位の idle timeout を
解除します。response、例外、response 未生成のすべてで `finally` によりデフォルト値へ復元してから socket
を再利用可能にします。別 method、query 付き、trailing slash、nested path には解除を適用しません。
これにより短い read / write と認証失敗後の keep-alive は fail-fast のまま、cleanup 中に response bytes が
流れない時間だけで同期 DELETE の接続を切断しません。

### `POST /v1/worlds/{worldId}/clock/advance`

launch token で認証した world mutation として `{ "milliseconds": <positive safe integer> }` を
受け取り、wall clock を参照せず virtual clock を明示的に進めます。response は target clock と
実際に適用した provider transition の opaque ID を返します。

```json
{
  "clock": "2026-07-12T00:10:00.000Z",
  "appliedTransitions": [
    { "provider": "aws", "transitionId": "revert-command-01" }
  ]
}
```

core は provider module を provider 名の昇順で呼び、全 module に advance 前の同じ
`ProviderWorldView` と target ISO time を渡します。optional hook が無い module は transition
なしとして扱います。各 hook は deterministic な resource update/delete、provider event、
適用済み transition ID を返します。core は provider 名と transition ID の安定順で結果を確定し、
`ClockAdvanced`、provider event、resource projection、world clock を単一 SQLite transaction で
保存します。hook error、invalid result、event/resource quota 超過のいずれでも clock を含む全変更を
rollback します。scoring probe は clock advance に含めず、TenkaCloud の scoring loop が catalog
metadata に対応する provider `Probe` / `Poll` operation を呼びます。

## Provider operation API

CLI、SDK、Console は共通 command API を使えます。native CLI を受ける provider gateway
も同じ `ProviderCommand` に正規化します。

```text
POST /v1/worlds/{worldId}/providers/{provider}/operations/{operation}
GET  /v1/worlds/{worldId}/resources
GET  /v1/worlds/{worldId}/events?after={sequence}
GET  /v1/worlds/{worldId}/events/stream?after={sequence}
GET  /v1/worlds/{worldId}/snapshots
POST /v1/worlds/{worldId}/snapshots
GET  /v1/worlds/{worldId}/snapshots/restores/{snapshotHash}
POST /v1/worlds/{worldId}/clock/advance
```

command は `deploymentId`、service、resource identity、input、idempotency key を持ちます。
provider reducer は validation、authorization、invariant、event、projection、response を
返します。dispatcher は provider 固有 field を解釈しません。

provider compiler が返す plan の target ID、provider、engine は、core が渡した target identity と
完全一致しなければなりません。deploy と command reducer が返す resource も、呼び出した provider と
同じ provider identity を持つ必要があります。core はこの境界を永続化前に検証し、不一致を
`ValidationFailed` として atomic に拒否します。

managed placement も同じ provider operation endpoint を使います。AWS provider の
`BindManagedResource` は participant URL や self-reported platform を受け取らず、slot と ready な
participant-created resource の ID だけを受け取ります。effective URL は reviewed workload の
materialization と slot の output key から provider が導出します。projection の
`VerifiedPlatform` は resource type の固定写像から導出し、別 world、deployment、target、重複 slot、
resource 再利用、未 ready、未知 type は fail closed にします。
`ReviewedArtifactHash` は workload resource ID と exact declaration を canonical hash に含めます。
image、command、port、health path、target、resource ref の変更や unknown field は、既存 managed
resource の bind / describe を conflict にします。
`重複 slot` とは 1 endpoint への 2 つ目の active binding です。同じ slot 向けの未 binding
placement 候補は tier 比較のため複数作成できますが、endpoint と managed resource はそれぞれ
1 active binding までです。CloudFormation の participant-controlled properties / metadata は provider-owned
eligibility に昇格せず、command の `targetId` は必須、input の `TargetId` は指定時に完全一致を
必須とします。

`Runtime::Workload` の `ready` は workload effect runner が digest と scope を照合し、simulator-owned
loopback endpoint の health path が 2xx になった materialization 後に core だけが設定します。
managed-create / bind reducer はこの state を作らず、対象となる一意な ready workload を参照する
だけです。ready projection は継続的な liveness lease ではないため、scoring は redacted
placement とは別に `EffectiveUrl` を実 HTTP probe し、stale / dead container を成功扱いしません。

Composite の command projection は `deploymentId` と `targetId` の組で分離します。同じ
provider / engine を複数 target が共有しても、reducer が読み書きできる resource と output は
選択された target に属するものだけです。resource projection は `targetId` を永続化し、更新と
削除も world / deployment / target / provider / resource identity の完全一致を要求します。
target identity を持たない旧 SQLite deployment / resource row は別 target へ誤帰属させず、
起動時 migration を fail closed にして schema 変更前に明示的に拒否します。SQLite schema は
`PRAGMA user_version` で version を固定し、既知の旧 schema だけを `BEGIN IMMEDIATE` から commit までの
単一 transaction で検査・移行します。table shape は column、primary key、foreign key だけでなく、
`sqlite_master.sql` を token 正規化した既知 DDL signature と schema object の組まで一致を要求します。
未知の `CHECK` / `UNIQUE`、`COLLATE`、`STRICT`、`WITHOUT ROWID` を含む DDL、未来 version、部分的または
未知の table shape は DB を変更せず拒否します。schema version 2 は exact な version 1 の 5-table
schema にだけ `event_reservations` を追加し、必須の positive-count `CHECK`、owner、world foreign key、
`materialization | deletion` の `operation_kind` を canonical DDL として検証します。version 1 に先行
reservation table が混在する場合も補正せず拒否します。
virtual clock hook だけは world 全体の deterministic projection を従来どおり受け取り、各 resource が
保持する target identity を更新・削除時にも維持します。

Console も CLI / SDK と同じ provider operation endpoint を使います。Console 専用 mutation
や client-side state は作らず、`provider`、`targetId`、`engine`、`service`、`resourceType`、
`operation`、JSON `input` を同じ command envelope へ変換します。Console から送った command と
外部 CLI / API から送った command は、同じ world の event log と resource / deployment
projection を更新します。

Console は SSE replay で現在 cursor より後の event を受信したとき、event timeline の追加だけで
済ませず、resource projection と選択中 deployment projection を API から再取得してから表示を
更新します。これにより、Console 自身の mutation に加えて CLI / SDK / 直接 API による mutation
も手動 refresh なしで同じ shared world に反映されます。event がない bounded replay では
projection を再取得せず、次の replay を待ちます。

resource API は core projection の JSON を返し、provider storage を直接読みません。event replay
API は `after` sequence より後を最大 100 件返す `{ events, nextCursor }` envelope です。
SSE は同じ cursor 規則を使い、各 event の `id` を sequence とします。接続時点の bounded replay
を送信して終了し、consumer は `Last-Event-ID` または `after` で再接続します。cursor が不正な
request は `ValidationFailed` とし、無制限 buffer は作りません。

clock advance は `{ "milliseconds": <positive safe integer> }` を受け取り、次を返します。

```json
{
  "clock": "2026-07-12T00:00:01.000Z",
  "appliedTransitions": [
    { "provider": "aws", "transitionId": "transition-..." }
  ]
}
```

core は target virtual time を先に計算し、provider 名順で optional clock hook を純粋評価して
から、clock、provider resource、event を 1 SQLite transaction で保存します。hook は外部 I/O
や shell を実行せず、自 provider の projection と deterministic transition ID だけを返します。
clock hook が resource を削除するときは、`deploymentId`、`targetId`、`resourceId` の組を返し、
core はその provider の完全一致する deployment target projection だけを削除します。
同じ virtual time までに期限が来た transition は一度だけ適用されます。

## Raw data-plane API

participant-facing HTTP は launch token で認証した次の route を通します。末尾 path と
query、method、UTF-8 body、end-to-end header は provider の
`http / HTTP::Endpoint / Request` command にそのまま正規化し、`StatusCode`、`Headers`、
`Body` を raw HTTP response に戻します。

```text
ALL /v1/worlds/{worldId}/data-plane/{provider}/{targetId}/{path}
```

world namespace と ready deployment target を先に照合し、別 tenant/team の world は 404 として
隠します。request/response body は 64 KiB、header は 64 件に制限し、非 UTF-8 body、CR/LF を
含む header、hop-by-hop header、不正 status/body shape は fail closed です。Bearer launch
token、host、content-length、protocol header は provider へ渡しません。caller が
`idempotency-key` を
指定した再送だけが同じ command として collapse し、指定しない HTTP request は state change 後の
再評価を妨げない固有 key と event を持ちます。

## Error envelope

error response は HTTP status に加えて次を返します。

```json
{
  "error": {
    "code": "UnsupportedCapability",
    "message": "deployment requires unavailable simulator capabilities",
    "requestId": "request-id",
    "retryable": false,
    "diagnostics": []
  }
}
```

`diagnostics` は provider、service、resource type、operation、required fidelity、available
fidelity、source location を含みます。secret、answer、採点条件、credential は含めません。

主な code は `ValidationFailed`、`NotFound`、`Conflict`、`UnauthorizedOperation`、
`UnsupportedCapability`、`NotImplemented`、`IdempotencyConflict`、`QuotaExceeded`、
`SnapshotIncompatible` です。未登録 operation は `NotImplemented` であり、空成功には
しません。

## Event と snapshot

event は world 内で単調増加する sequence、virtual タイムスタンプ、command identity、
schema version、payload hash を持ちます。projection を直接更新する公開経路は作りません。
外部 workload effect が消費する成功 event 数は durable materialization lease に含め、成功または失敗を
commit する transaction 内で解放します。world delete は別の persistent deletion intent を取得し、
cleanup failure や tombstone commit failure では保持します。deleted world の再 cleanup と tombstone が
完了した transaction だけが intent を解放します。restore quota は workload deployment ごとの
retryable failure event 1 件もあらかじめ含めます。

snapshot version `2` は namespace、seed、clock、last sequence、resource graph、provider
projection、`integrityProof` を含みます。公開 `hash` は deterministic replay と corruption 検出用で、
caller が graph を変更して再計算できるため authenticity boundary には使いません。
authenticated GET は launch token authority の server-held secret を使い、proof 自身を除く exact API
envelope を `{ domain: "tenkacloud-simulator.snapshot-integrity", version: "1", envelope }` として
canonical JSON 化した HMAC-SHA256 を付与します。wire canonicalization の正本は contracts の
`canonicalSimulatorSnapshotIntegrityPayload` です。object key は Unicode normalization をせず、
UTF-16 code unit の昇順で再帰的に並べ、array 順序を維持し、scalar と key は JSON string encoding を
使います。locale collation や RFC の別 canonicalization profile へ置換しません。proof は
`version: "1"`、
`algorithm: "HMAC-SHA256"`、43 文字の canonical unpadded base64url `value` だけを持ち、追加 field、
別 version、別 algorithm、非 canonical encoding を拒否します。secret は snapshot や error に含めません。
version `2` の portability は同じ launch token authority secret を共有する Simulator instance 間に
限ります。verifier は current secret だけを使うため、secret rotation は未復元 snapshot の proof を
意図的に失効させます。rotation 時に保持が必要な world は旧 secret の有効期間中に restore し、
新 secret の instance から snapshot を再 export します。複数 key を暗黙に試す fallback は設けません。

import は schema と proof を先に検証し、proof が正しい場合だけ core snapshot へ変換して protocol
range、hash、quota を検証した後、新しい world として復元します。proof は source world ID、namespace、
deployment、provider projection、event、resource、output、公開 hash を envelope 全体として拘束します。
比較は canonical encoding の完全一致を timing-safe に行います。unsigned version `1`、別 authority の
proof、改変または malformed proof は `ValidationFailed` とし、core restore や SQLite mutation を
開始しません。hash が一致していても、event と deployment の world、deployment ID、空でない
一意な target identity、resource が参照する deployment / target / provider の対応を import 前に
検証し、resource graph が閉じていない snapshot は `SnapshotIncompatible` で拒否します。検証に
失敗した snapshot が world、event、deployment、resource、idempotency を部分的に永続化することは
ありません。既存 world への上書き import は行いません。

`Runtime::Workload` projection は world 固有の container と materialization endpoint を参照するため、
portable restore は旧 projection をそのままコピーしません。import transaction では workload を
`pending`、対象 deployment を `deploying` に戻し、旧 `materialization` と
resource status にかかわらず全 `Workload.*.Endpoint` output を除去します。旧 endpoint 値は
import する event payload からも除去し、payload hash を再計算します。`Runtime::Endpoint` の world-bound
`ManagedPlacement` と `state.overrideUrl` も除去し、managed placement は新 endpoint に対する
明示的な再 bind を必要とします。active snapshot と workload / deployment lifecycle が矛盾する
graph は sanitization で隠さず `SnapshotIncompatible` として import 前に拒否します。

transaction は new world、sanitized graph、restore idempotency pointer を先に commit し、その後
new world ID 専用の workload を非同期 materialize します。複数 deployment の一部が成功して後続が
失敗した場合、同じ snapshot と restore key の再送は同じ new world を取得し、`ready` deployment を
再起動せず `failed` / `deploying` だけを再試行します。runner がない初回 restore は永続化前に
`SnapshotIncompatible`、runner failure は durable pointer を残した retryable
`WorkloadEffectFailed` です。これにより source world の container、listener、loopback URL を新しい
trust root とせず、新 world 専用の health-checked endpoint だけを active projection に保存します。

provider の一時的な接続 credential は portable projection ではありません。`AWS SSM Session Manager` の
`TokenValue` は snapshot export 時に空へ置換し、source world の保存状態は変更しません。restore 後の
active セッションは空 token で data channel を開けず、`ResumeSession` が新しい world 用の token を
発行してから接続します。snapshot import に空でない `TokenValue` が含まれる場合は、world、resource、
idempotency を保存する前に `SnapshotIncompatible` で拒否します。

## Capability coverage

catalog scanner は problem ごとに requirement を生成し、capability manifest と比較します。
比較単位は provider、resource、operation、fidelity です。report は次を区別します。

各 requirement は `classification` を持ちます。IaC resource、metadata、overlay から得る
`binding` requirement だけが preflight と coverage workflow を失敗させます。CloudFormation
IAM policy の `Action` は YAML structure から `authorization-inventory` として抽出し、許可
上限の監査情報として report に残しますが、単独では実行証拠になりません。コメントや説明文
に現れる action token は抽出しません。同一 operation を overlay が明示した場合、その別
requirement は `binding` のため通常どおり gate します。

IaC と既存 metadata から導出できない requirement は Challenge metadata の
`simulationOverlay: { schemaVersion: "1", entry: "simulation.json" }` だけから読み込みます。
overlay の provider と engine は normalized target から継承し、任意指定を許しません。scanner
は overlay と参照 artifact の SHA-256 を catalog hash に含め、target 不一致、パス escape、
symlink、hash 不一致、unknown field を `invalid` とします。workload image は OCI digest pin を
必須にし、overlay に scoring、answer、secret、environment、host mount を表現する field は
ありません。

release artifact と一緒に生成する offline manifest は、digest allowlist と effect runner を
設定したときに提供できる最大 capability を表します。そのため各 provider の
`Runtime::Workload/Materialize` を L4 として含めます。一方、稼働中の `/v1/capabilities` は
effect runner の設定が完全な場合だけ同 capability を広告します。catalog compatibility と
live preflight を分離し、未設定 runtime で workload deployment を成功扱いしません。

offline manifest の `fidelity` は `L0` から `L4` の独立した dimension を表す
canonical set です。非空、重複なし、`L0` から `L4` の順の配列だけを
受理し、最大値や累積レベルとして扱いません。requirement も canonical set を
保持し、coverage は必要な全 dimension が manifest の集合に含まれるかで判定します。
report は requirement と実装済みの集合全体を `requiredFidelity`、
`implementedFidelity`、diagnostic の `availableFidelity` に保持します。
manifest capability と requirement の照合 identity は `provider / engine / service /
resourceType / operation` の完全一致です。同じ provider の別 engine が同名 operation を
実装していても、対象 runtime engine の coverage として流用しません。

offline manifest の `version` は package version だけでなく、生成元の clean な
Simulator commit を `+git.<40-character-sha>` として含みます。manifest CLI はこの commit を
必須引数として受け取り、catalog 側 compatibility runner は Simulator checkout の actual HEAD
と clean status を検証してから渡します。これにより、同じ package version の別 commit が
同一の public report identity を名乗ることはできません。

catalog scanner は scan に実際に使った metadata、IaC、overlay、参照 artifact の各 byte を、
`catalogCommit` にある Git blob と照合します。通常の clean status 検証に加え、この照合で
ignored / untracked artifact や `assume-unchanged` によって status から隠れた byte drift も
fail closed にします。Git blob がない source や digest が異なる source から report は
生成しません。

OCI image で起動した local runtime は、[ADR-0012](../adr/0012-bounded-host-docker-workload-runner.md)
の境界に限って host Docker socket を使います。release image は client binary だけを持ち、
daemon を内包しません。launcher は process を non-root のまま socket group に追加し、
Simulator control container を world の internal network へ接続します。health check と認証済み
data-plane forwarding は internal endpoint を使い、participant-facing endpoint は引き続き host
loopback にだけ publish します。socket、allowlist、quota、control-container identity の一部でも
欠けた場合、workload capability は fail closed です。

Sakura AppRun の HTTP data plane は、AppRun application だけから synthetic な 200 応答を
生成しません。対象 target の `BaseUrl` output に binding された overlay workload が
application の digest 固定 image、container port、health path と一致する場合だけ explicit
overlay の `Request` / `Probe` requirement を受理します。materialization resource が ready に
なった後、実行時は
保存済みの numeric loopback HTTP endpoint へ bounded な GET / HEAD だけを転送し、status、
content type、body を実 workload の応答から返します。binding の欠落、不一致、未 ready、
loopback 以外の endpoint、timeout、body 上限超過は fail closed です。

- `covered`: 必要な fidelity dimension をすべて実装済み集合に含む。
- `missing`: resource または operation がありません。
- `insufficient`: operation はあるが必要な fidelity dimension が集合にない。
- `invalid`: IaC または overlay が protocol schema に違反している。

coverage workflow は binding requirement の `missing` / `insufficient`、または `invalid` が
1 件でもあれば失敗します。IAM authorization inventory の不足は summary の専用 field に
残しますが status を変更しません。unknown binding resource を無視する option は提供しません。

## Runtime bind contract

通常 process は `TENKACLOUD_SIMULATOR_HOST` を loopback に限定します。OCI container 内では
`TENKACLOUD_SIMULATOR_CONTAINER_MODE=1` のときに限り `0.0.0.0` bind を許可します。この mode
は外部公開を許可する指定ではありません。launcher は container port を必ず host の
`127.0.0.1` に publish し、全 mode で launch token を必須にします。container mode は host
側 URL を `TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN` に明示し、server が内部 bind address を
Console URL として返さないようにします。

native CLI gateway を有効にする production runtime は、起動ごとに次の simulator-only
credential も受け取ります。これらは real cloud credential ではなく、同じ process 内の world
routing を保護する値です。

- `TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID`: `TCSIM` prefix の uppercase ID
- `TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL`: `tcsim_` prefix の Bearer token
- `TENKACLOUD_SIMULATOR_GCP_CREDENTIAL`: `tcsim_` prefix の Bearer token
- `TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL`: `tcsim_` token と secret の Basic pair

credential は image に埋め込まず、launcher が生成し、参加者の対応 CLI process だけへ渡し
ます。gateway request は credential に加えて world、deployment、target routing header を署名
または認証済み header として要求します。
