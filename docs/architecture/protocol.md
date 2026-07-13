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
- snapshot schema は API と独立した `snapshotVersion` を持つ。
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
idempotent です。別 namespace の caller からは存在しない world として扱います。

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
POST /v1/worlds/{worldId}/clock/advance
```

command は `deploymentId`、service、resource identity、input、idempotency key を持ちます。
provider reducer は validation、authorization、invariant、event、projection、response を
返します。dispatcher は provider 固有 field を解釈しません。

provider compiler が返す plan の target ID、provider、engine は、core が渡した target identity と
完全一致しなければなりません。deploy と command reducer が返す resource も、呼び出した provider と
同じ provider identity を持つ必要があります。core はこの境界を永続化前に検証し、不一致を
`ValidationFailed` として atomic に拒否します。

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
未知の table shape は DB を変更せず拒否します。virtual clock hook だけは world 全体の deterministic
projection を従来どおり受け取り、各 resource が保持する target identity を更新・削除時にも維持します。

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

snapshot は namespace、seed、clock、last sequence、resource graph、provider projection を
含みます。import は schema、protocol range、hash、quota を検証した後、新しい world として
復元します。hash が一致していても、event と deployment の world、deployment ID、空でない
一意な target identity、resource が参照する deployment / target / provider の対応を import 前に
検証し、resource graph が閉じていない snapshot は `SnapshotIncompatible` で拒否します。検証に
失敗した snapshot が world、event、deployment、resource、idempotency を部分的に永続化することは
ありません。既存 world への上書き import は行いません。

`Runtime::Workload` projection は world 固有の container と materialization endpoint を参照するため、
別 world へそのままコピーできません。workload resource を 1 件でも含む snapshot は、source world の
projection と一致していても、world、event、resource、idempotency を永続化する前に
`SnapshotIncompatible` で拒否します。portable restore は
[TenkaCloud Issue 2605](https://github.com/susumutomita/TenkaCloud/issues/2605) で async
rematerialization を実装し、新しい world 専用の container と endpoint を確定できるまで提供しません。
これにより snapshot import は既存 world の container を共有したり、任意 OCI workload や loopback
endpoint の新しい信頼根になったりしません。workload を含まない snapshot の restore は引き続き
対応します。

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
