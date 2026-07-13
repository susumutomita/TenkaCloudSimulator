# TenkaCloud Simulator 設計

## 問題

TenkaCloud の Docker 問題は local container と問題側 `/verify` で実行できます。一方、
CloudFormation、Bicep、GCP Infrastructure Manager、Sakura AppRun を使う問題は、実クラウド
なしでは起動できません。旧 Kumo 経路は IAM と SSM の materialize に留まり、問題から
観測できる resource lifecycle、権限、network、data plane を再現しませんでした。

必要なのは AWS 互換 endpoint の有無ではありません。current catalog が要求する API と
状態遷移を列挙し、その全てを同じ deterministic world で再現できることです。

## 選択肢

### A. 既存 emulator を provider ごとに組み合わせる

LocalStack、Azurite、各種 GCP emulator などを起動し、足りない部分だけを補います。
native CLI 互換を早く得られる反面、resource ごとに source of truth が分散します。
Composite の shared clock、snapshot、policy、network、coverage 診断を一貫させられません。

### B. IaC を local container へ直接変換する

問題 template から container と設定を生成します。workload data plane には向きますが、
CLI で resource を調査、変更、復旧する問題では control plane の意味が失われます。
旧 Kumo materializer と同じ failure mode です。

### C. Event-sourced core と provider plugin を実装する

API gateway は provider の公開契約を受け、command に正規化します。provider reducer は
状態遷移を event と projection にし、core は world、graph、clock、snapshot、quota だけを
所有します。実装量は増えますが、coverage を測れ、Composite を 1 world に置け、未実装を
loud に拒否できます。

Issue 2574 の invariant を同時に満たせる C を採用します。native API の protocol surface は
current catalog の実使用から広げます。

## Component 境界

```text
contracts <- core <- apps/api
     ^         ^         ^
     |         |         |
     +--- provider plugins+
     +--- scanner --------+
     +--- CLI / Console --+
```

- `contracts` は runtime dependency を持たず、schema と生成 type だけを公開する。
- `core` は provider package を import せず、起動側が plugin を registry に渡す。
- provider は contracts と core の plugin interface だけに依存する。
- API は native request を provider gateway へ渡し、共通 command を dispatcher へ渡す。
- Console と CLI は API client だけを使い、storage adapter を直接呼びません。
- scanner は provider capability manifest を読むだけで、provider implementation を
  import しません。

architecture test は依存方向と core 内の provider 名分岐を検査します。

## World と state transition

world namespace は `tenantId / eventId / teamId` です。deployment は world の子で、単一
target または Composite target を持ちます。Composite の全 target は同じ sequence、seed、
virtual clock、network model を共有します。

Composite の IaC source は `templateBody` 内の versioned artifact bundle で target ごとに
分離します。core は target ID、provider、engine、entry path の完全一致と相対 artifact path
を検証してから、各 compiler に entry 本文と同 target の artifact set を渡します。これにより
AWS CloudFormation と複数 file の GCP Terraform のように形式も path も異なる source を
誤って broadcast しません。

```text
request
  -> schema validation
  -> namespace authorization
  -> idempotency lookup
  -> all-target capability preflight
  -> provider invariant evaluation
  -> append events atomically
  -> update projections
  -> serialize provider response
```

SQLite の transaction で event append、idempotency record、projection checkpoint をまとめます。
resource graph と provider state は event から再構築可能です。snapshot は起動高速化のための
checkpoint であり、source of truth ではありません。

virtual clock は request で暗黙に wall clock へ追随しません。明示した advance command と
provider transition schedule だけで進みます。同じ seed、event、clock は同じ ID、fault、
transition を生成します。

scheduled transition は provider-owned resource projection に保存します。core は schedule の
意味を解釈せず、clock advance 時に全 provider の optional hook を安定順で呼び、due transition
の resource 更新と event を clock update と同じ transaction に入れます。hook が process、
network、shell を実行することは禁止し、workload effect は別 runner の command として扱います。

公開 clock mutation は `POST /v1/worlds/{worldId}/clock/advance` とし、正の safe integer の
milliseconds だけを受け取ります。core は provider 名順で optional `advanceClock` hook を評価し、
全 provider に advance 前の同じ world view を渡します。hook が返す resource update/delete、
event、opaque transition ID と `ClockAdvanced` は quota 確認後に一括 commit します。provider 名を
条件分岐する clock 実装、wall clock 参照、advance と scoring probe の暗黙結合は行いません。

## Provider plugin contract

plugin は manifest、plan compiler、command reducer、query projector、native gateway を
公開します。core は plugin ID を opaque string として扱います。

```text
manifest() -> capabilities
compile(plan source, overlay) -> target plan or diagnostics
reduce(command, world view) -> events and response
advanceClock?(prior world view, target ISO time) -> events, resources, applied transition IDs
project(query, world view) -> response
gateway(request) -> provider command or provider error
```

reducer は I/O を行いません。workload の起動や停止は event を受ける effect runner が行い、
結果を新しい command として戻します。これにより event replay が外部 process を再起動せず、
deterministic に完了します。

AWS と Sakura は同じ contract suite を通します。Azure と GCP の追加で core file の差分が
生じた場合、plugin boundary 違反として扱います。

## IaC compile と capability preflight

scanner と deploy compiler は同じ `SimulationPlan` schema を境界にします。scanner は静的な
requirement、compiler は deploy parameter を解決した resource instance を生成します。

- CloudFormation は resource type、dependency、intrinsic function、custom resource handler を
  抽出する。IAM `Action` は YAML structure から authorization inventory として抽出し、
  permission allow-list を participant の実行要件とはみなさない。
- Terraform は resource block、IAM binding、output を抽出する。
- Bicep は comment を code として扱わずに resource type、API version、property、module、output を
  抽出する。同じ compiled resource ID へ解決される複数の resource 宣言は受理しない。
- AppRun descriptor は application、component、image、port、scaling、health check を抽出する。
  overlay workload は descriptor path ではなく、descriptor 内の digest 固定 image、port、
  health path と照合する。
- IaC から分からない workload、fault、scoring probe だけを versioned overlay で補う。

overlay は capability requirement と workload artifact を追加できますが、採点条件や答えを
持てません。documented な participant operation が IAM allow-list にしか現れない場合は、
exact な operation を overlay へ昇格して binding にします。source location と binding /
authorization-inventory の分類を全 requirement に残し、diagnostic を元 file まで戻します。

preflight は全 requirement と registry を比較してから `DeploymentRequested` を受理します。
拒否時には audit 用 `DeploymentRejected` だけを残し、resource graph と workload effect は
変更しません。

## Current catalog baseline

初期 baseline は `TenkaCloudChallenge` commit
`aeece635fa2701f4d9139a27759a683fe59603f2` です。18 problem のうち cloud-backed は 9、
target は AWS 9 と GCP 1 の計 10 です。AWS template は 108 resource instance、26 resource
type、GCP target は Cloud Run service と IAM member の 2 resource を宣言します。

AWS resource type は CloudFormation custom resource、IAM、SSM、S3、Lambda、EC2 と VPC、
ELBv2、RDS、WAFv2、Logs を含みます。Battle は IaC resource だけでなく SSM disruption、
HTTP probe、EC2-as-container、player が作る Lambda、ECS、App Runner、API Gateway、ECR も
必要とします。

scanner は requirement の `plane` を `deploy`、`participant`、`workload`、`scoring`、
`operator`、`access` に分けます。source は IaC resource、IAM policy、metadata probe、
metadata disruption、code static analysis、simulation overlay を区別します。resource scan だけを
catalog coverage の証跡にはしません。

現 catalog には Azure と Sakura の sample がありません。両 provider の plugin contract は
synthetic conformance fixture で証明し、catalog coverage とは別の証跡として扱います。
`cloudflare-api-security` の external workload と、migration Battle の alternative platform は
IaC だけから確定できないため、versioned overlay で明示します。

## Fidelity

fidelity は順序付きの単一 flag ではなく、各 operation に必要な dimension の集合として
保存します。report では Issue の L0 から L4 に要約します。

| Level | dimension | 例 |
| --- | --- | --- |
| L0 | contract | shape、error、pagination、idempotency |
| L1 | control | CRUD、dependency、output、async transition |
| L2 | security | IAM、RBAC、public exposure、audit |
| L3 | network | route、firewall、reachability |
| L4 | data-plane | HTTP、object access、probe、fault |

`required: [contract, control, security]` に対し `implemented: [contract, control]` なら、resource
が同名でも `insufficient` です。これにより L4 が不要な SSM problem と workload が必要な
Lambda problem を区別します。

## Native gateway

AWS gateway は JSON、Query、REST-JSON、REST-XML のうち current catalog が使う protocol を
service adapter へ分離します。SigV4 の構文は検証しますが、実 credential は受け付けません。
Simulator 専用 credential と world routing header だけを許可します。

Azure、GCP、Sakura gateway も authorization header から real credential を識別して拒否します。
Console は provider gateway を特別扱いせず、generated API client を使います。

## Provider-neutral raw HTTP projection

TenkaCloud の authenticated raw data-plane route は request を
`http / HTTP::Endpoint / Request` command の `{Method, Path, Headers, Body}` に正規化します。
provider は同じ deployment に属する endpoint resource を列挙し、一件だけが core 上で `ready`、
かつ provider 固有の実行状態も ready の場合に限って、保存済み projection から
`{StatusCode, Headers, Body}` を返します。GET と HEAD だけを成功させ、構文上正しい未対応
method は `Allow: GET, HEAD` 付き 405 とします。resource の不在、複数候補、非 ready、壊れた
response projection は typed error です。

方式は次の三案を比較しました。

- resource projection 方式を Container App、Cloud Run、AppRun の simulated endpoint に採用する。
  control plane と同じ event state が応答の正本になり、再現性と snapshot 復元後の同値性を保てる。
- materialized workload への forwarding は実 container の実行結果が fidelity に必要な workload 専用である。
  simulated endpoint のデフォルト値には使わず、host network へ抜ける fallback も作らない。
- problem ID ごとの固定応答は catalog の答えを provider へ埋め込み、新しい問題を誤って成功させるため
  採用しない。

method、パス、header、UTF-8 body の共通境界は core の provider-neutral helper が所有し、
Container App の external/Running、Cloud Run の Ready、AppRun の Healthy と version/traffic 整合性は
各 provider が所有します。compile plan は実 endpoint が存在する target だけに Request L4
requirement を追加し、capability preflight 前に実行可能性を誤広告しません。

## Workload と network

control plane の resource は常に core が source of truth です。Lambda、Cloud Run、EC2 相当の
実行が必要な場合だけ OCI workload を materialize します。

- rootless、read-only root filesystem、capability drop、CPU と memory limit を必須にする。
- egress は default deny で、world network policy が許可した simulator service だけに接続する。
- host port は loopback または Codespaces proxy に限定し、deployment output として公開する。
- container process の全 interface bind は明示した container mode だけに限定し、launcher は
  publish address を host loopback に固定する。
- effect runner の失敗は resource を ready にせず、typed failure event を残す。

Simulator 自身を OCI image で動かす local mode では、ADR-0012 に従って digest 固定 Docker CLI
と host daemon socket を使います。launcher は non-root process に socket group だけを追加し、
Simulator control container を各 world の internal network に接続します。これにより health check
と raw HTTP forwarding は host loopback へ抜けずに実 workload を観測でき、participant endpoint
は fixed proxy の loopback publish のまま維持できます。socket や control-container identity を
構成できない runtime は workload capability を広告しません。

effect dispatcher は contract 検証済みの `simulationOverlay.workloads` から runtime に必要な
image、command、port、health path だけを effect runner へ渡します。runner は declaration の
unknown field を拒否し、image は digest pin と runtime allowlist の双方を満たす必要があります。
同じ world と workload ID の再実行は spec hash が一致するときだけ既存 container を再利用し、
不一致や停止済み container は loud に拒否します。
起動済み workload は Docker label から world 単位で列挙します。`WorkloadPolicy` の optional な
`controlContainer` は bounded かつ safe な Docker container name、または lowercase hexadecimal の
完全な container ID だけを受け付けます。設定時は world network の作成時と再利用時の双方で exact
selector から実 ID を解決し、control container を idempotent に接続します。health check と probe は
deterministic な proxy container DNS 名と宣言済み port から組み立てた internal URL だけを使い、proxy
から workload への forward を含む経路を検証します。participant へ返す endpoint は同じ proxy が
publish した `127.0.0.1` のままで、任意 URL を effect runner の probe 入力にはしません。
`controlContainer` が未設定の host-process policy は従来どおり loopback endpoint で health check と
probe を行います。

world cleanup は label が一致する proxy と workload を停止し、設定済み control container を
idempotent に切断してから internal network を削除します。一部の停止または切断に失敗した場合は
network prune で成功扱いにせず typed failure にし、再試行可能な状態を残します。overlay materialize
を公開 lifecycle に接続するときは capability preflight と deployment commit の後に effect command を
発行し、成否を表す event が core に保存されるまで deployment を ready として返しません。

network projection は subnet、route、security rule、listener、target health を扱います。data plane
probe は同じ projection で reachability を判定し、host network の偶然に依存しません。

AWS の L3 判定は `ec2/EvaluateReachability` に正規化し、source IPv4 CIDR、transport protocol、
port、instance または load balancer の排他的 destination を入力にします。reducer は同じ
deployment の VPC、subnet、明示 route-table association、Internet Gateway attachment、default
route、security-group の現在 ingress、listener、forward target group、target instance state、
Web ACL association を安定順で辿ります。既知の deny は理由と評価 path を伴う非到達 response にし、
参照欠損・重複・複数 action・入力だけでは評価できない WAF rule は typed error にします。
problem ID や template 名による分岐、host network probe、projection 欠損時の allow は行いません。

## Console と観測性

Console は provider 固有の語彙を保った resource graph、event timeline、policy、reachability、
output、diagnostic を表示します。AWS、Azure、GCP、Sakura の既存 Console を複製しません。

Server-Sent Events は event cursor 以降の変更を配信します。再接続時は cursor から replay し、
取りこぼしを防ぎます。Console の mutation 後も response だけで local state を確定せず、同じ
event stream で CLI mutation と同様に反映します。

## TenkaCloud integration

TenkaCloud の adapter は次だけを所有します。

1. pinned image を loopback で起動する。
2. world と deployment を lifecycle API で作る。
3. status、output、Console URL を portal へ渡す。
4. reset は world delete と再作成、stop は world delete と process stop を行う。
5. snapshot download と upload を protocol client へ委譲する。

Docker runtime の compose と `/verify` は変更しません。single cloud と Composite cloud だけを
Simulator adapter へ dispatch します。problem definition と scoring logic は Challenge repository
に残します。

Codespaces では API と Console を 1 origin の path prefix で公開します。portal が handoff URL を
生成し、参加者は browser だけで Console を開けます。native CLI は optional です。

release image は GHCR の multi-platform manifest として配布し、TenkaCloud は manifest digest を
pin します。image は build 済み Console と bundled server だけを持ち、non-root user で実行し、
state directory 以外を read-only にできます。Codespaces の forwarded port は private visibility
をデフォルトにし、handoff URL の token は fragment だけで渡します。

## Security と abuse case

- real credential に見える access key、JWT、service account key を request boundary で拒否する。
- namespace field は authenticated launch token と一致させ、body の自己申告を信頼しない。
- native gateway の request body、IaC archive、snapshot、event page に size と count limit を
  設ける。
- template path、container mount、snapshot entry は traversal と symlink escape を拒否する。
- workload image は digest pin と allowlist を必須にする。
- error と event payload は secret value を redaction してから永続化する。
- SSRF を避けるため provider endpoint と effect callback は simulator-owned address だけを
  許可する。

## Edge case

- 同じ idempotency key と異なる payload は既存結果を返さず conflict にする。
- Composite の一部 target が不足している場合、他 target も作らない。
- async transition 中の snapshot は scheduled transition を含めて復元する。
- delete の一部 workload が失敗しても tombstone と failure を記録し、再試行できる。
- plugin の duplicate capability、unknown event version、projection hash 不一致は起動を止める。
- event stream の遅い consumer は bounded buffer を超えたら cursor replay を要求される。
- output の同名 key は target ID namespace で分離する。
- provider の pagination token は world、query、cursor に bind し、別 query へ流用できない。

## Test strategy

- schema の valid、invalid、boundary example を contract test にする。
- core は SQLite file と実 HTTP process を使い、mock store や stub API を使わない。
- 全 provider は同じ contract suite と deterministic replay suite を通す。
- API、CLI、browser は 1 つの process と SQLite file を共有する end-to-end test を通す。
- scanner は current catalog を pin した compatibility test と unknown resource の negative test を
  持つ。
- workload test は rootless runtime がある環境で実 container を起動し、CI では明示した
  capability check 後に実行する。
- differential conformance は disposable cloud だけを使う manual または nightly workflow に
  分離し、通常 test が real credential を要求しないようにする。

## Completion audit

`Plan.md` の受け入れ証跡を全て current-state evidence で確認します。narrow fixture の green を
current catalog 全体の証跡に使いません。coverage report は pinned Challenge commit と report
hash を記録し、Simulator capability manifest の変更で必ず再生成します。
