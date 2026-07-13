# Issue 2574 実装計画

### Issue 4 final four-cloud evidence - 2026-07-13

#### 目的

merged TenkaCloudChallenge commit
`488ed4a2d103cbe596295c940620d68d8f420c99` の `hello-multicloud` 契約を
Simulator の同一 world で再現し、merged Simulator source
`463c7a1650925b1d5177d67540b2399c86783916` と catalog commit を結び付けた
最終 compatibility evidence を固定します。

#### 制約と設計判断

- Challenge の production metadata と 4 provider の IaC bytes を fixture の正本にします。
  テスト専用の scoring 条件や別形式の target は作りません。
- 選択肢 A は E2E 内へ metadata と IaC を inline 化する案、選択肢 B は provider ごとの
  fixture と catalog metadata fixture を分けて digest 検証する案です。review 時に差分を
  照合でき、provider 単体テストからも再利用できるため選択肢 B を採用します。
- 選択肢 A は外部 Challenge checkout をテスト時に読む案、選択肢 B は reviewed commit の
  bytes を repository fixture として保持する案です。offline、deterministic、100％ coverage
  gate を維持するため選択肢 B を採用し、SHA-256 と production metadata の構造を E2E で
  検証します。
- world は 1 件、deployment は 1 件とし、AWS、GCP、Azure、Sakura の 4 target を同時に
  deploy します。各 output と実装済み HTTP data plane を target ごとに probe し、metadata
  の `success: all` と 4 件の `expectStatus` から得点を算出します。
- clean catalog HEAD、clean Simulator source SHA、manifest version、canonical report hash の
  いずれかが drift した場合は evidence generation を失敗させます。

#### データの流れと責務

1. catalog fixture が runtime targets、IaC artifact、scoring metadata を提供します。
2. Simulation Core が 4 provider module を同じ world に登録し、1 deployment を作成します。
3. provider が namespaced output と HTTP endpoint を materialize します。
4. E2E が metadata の output key と path だけを使って 4 endpoint を probe します。
5. capability manifest generator が clean Simulator SHA を version に埋め込みます。
6. catalog scanner が clean Challenge commit の tracked blobs と capability manifest を照合し、
   canonical hash 付き report を生成します。

#### エッジケース

- target、output key、scoring path の不足や重複を成功扱いしません。
- AWS/GCP は `/`、Azure/Sakura は `/healthz` を使い、root path の誤った hello 仮定を
  持ち込みません。
- 4 probe のうち 1 件でも status が metadata の `expectStatus` 外なら 0 点です。
- dirty checkout、可変 ref、manifest version 不一致、canonical hash drift を拒否します。

#### タスク

1. final catalog fixture と 4-cloud E2E の期待をテスト先行で追加します。
2. Azure/Sakura provider を含む同一 world deploy と scoring probe を実装します。
3. final source/catalog provenance で 2 report を再生成して整形します。
4. architecture harness、before-commit、review、security review、simplify review を実行します。

#### 検証手順

```bash
bun test apps/server/test/hello-multicloud.test.ts
bun test tools/catalog-scanner/tests/catalog-scanner.test.ts tools/capability-manifest/test/manifest.test.ts
make before-commit
```

#### 進捗ログ

- 2026-07-13: merged Simulator と Challenge の immutable SHA を確認し、fixture、world、
  scoring、provenance の責務を上記の構造に固定しました。
- 2026-07-13: Challenge の 6 fixture を byte-for-byte で固定し、同一 world の 4 target
  deploy、4 HTTP probe、`success: all` の 100 点判定を実行する E2E を通しました。
- 2026-07-13: catalog coverage を 174/174、missing 0、insufficient 0、invalid 0 で再生成し、
  canonical report hash を検証しました。`make before-commit` は 493 tests、100％ coverage、
  build を含めて通過しました。
- 2026-07-13: security review で source commit の自己申告リスクを確認したため、Simulator
  `463c7a1650925b1d5177d67540b2399c86783916` の `git archive` から依存関係を再構成し、2 report
  を再生成しました。作業 branch の report と byte-for-byte で一致しました。

## 目的

TenkaCloud の current catalog が必要とする cloud runtime をローカルで再現し、
Docker 問題と simulated-cloud 問題を `make local` の同じ導線から操作できるように
します。完了条件は GitHub Issue 2574 の受け入れ条件と
`docs/architecture/quality-bar.md` の両方です。

## 作業順序

1. protocol、設計、ADR、受け入れ証跡を先に確定します。
2. TypeScript template の残骸を project 固有構成へ整理します。
3. contract、core、scanner をテスト先行で実装します。
4. provider module、API、CLI、Console を実装します。
5. TenkaCloud と TenkaCloudChallenge を公開契約だけで接続します。
6. catalog compatibility、fresh clone、全品質 gate を検証します。

## 実装スライス

### 1. Protocol と catalog scanner

- 5 operation lifecycle API と共通 error envelope を JSON Schema と OpenAPI で
  固定します。
- protocol version、snapshot version、capability version の互換規則を固定します。
- CloudFormation、Bicep、Terraform、AppRun descriptor、optional simulation overlay
  から capability requirement を抽出します。
- JSON coverage report と人間向け診断を同じ中間表現から生成します。

### 2. Simulation Core

- namespace、event log、snapshot、resource graph、virtual clock、idempotency を
  実装します。
- capability preflight は resource event より前に全 target を一括検査します。
- provider registry は plugin contract のみを参照し、provider 固有分岐を core に
  持ちません。

### 3. Provider と共有 UI

- AWS CloudFormation、IAM、SSM と Sakura AppRun の vertical slice を作ります。
- AWS catalog が使う S3、Lambda、EC2、VPC、ELBv2、RDS、WAF、Logs と workload
  data plane を順次追加します。
- GCP Cloud Run、Azure sample、Sakura sample、Composite target を追加します。
- API、CLI、Console が同じ world を操作する end-to-end test を追加します。

### 4. Platform integration

- TenkaCloud には lifecycle client、image launcher、portal wiring だけを追加します。
- Docker runtime は現行 `/verify` 経路を維持し、cloud runtime だけ Simulator へ
  dispatch します。
- start、stop、reset、Console URL、snapshot transfer、Codespaces proxy を接続します。
- TenkaCloudChallenge には versioned simulation overlay と compatibility workflow
  だけを追加します。

### 5. Hardening と release

- 実 credential 拒否、egress deny、quota、request size、snapshot validation を
  強制します。
- disposable cloud を使う differential conformance は manual または nightly に
  分離します。
- 3 repository の protocol version、image digest、compatibility matrix を固定します。

## 受け入れ証跡

| 要件 | 証跡 |
| --- | --- |
| coverage の不足表示 | scanner fixture と current catalog の JSON report |
| unsupported capability の preflight 拒否 | event log に resource event がない integration test |
| API、CLI、Console の shared world | browser と CLI を同じ API process へ接続する test |
| AWS と Sakura の共通 plugin | provider contract suite と core import-boundary scan |
| Docker と simulated-cloud の `make local` | TenkaCloud local-play integration test |
| current catalog の local-playable | pinned catalog commit の coverage report が不足 0 |
| Codespaces の browser-only 導線 | forwarded URL の end-to-end smoke test |
| TenkaCloud の薄い境界 | path 単位の architecture test と review |
| quality gate | fresh clone で harness、before-commit、build、test |

## 失敗条件

- 未実装 operation を空の成功 response にします。
- provider 固有の resource model を core に追加します。
- Console だけが持つ state を作ります。
- problem の採点条件や答えを Simulator または TenkaCloud に移します。
- current catalog の不足を follow-up 扱いにして完了とします。

## 進捗

- 2026-07-12: Issue 2574 と既存 local play、Composite Runtime の契約を再確認し、
  protocol、event-sourced world、provider plugin、coverage gate の設計を固定しました。
- 2026-07-13: PR 1 の CI で Safe-chain が公開直後の transitive dependency を
  minimum-age policy により拒否したため、検査を無効化せず、十分な公開期間を持つ
  互換版を root override と lockfile で固定する方針にしました。
- 2026-07-13: package coverage は product package だけでなく application root である
  architecture harness も 100％を要求するため、CLI、repo check、file walk を process
  spawn だけでなく in-process で検証できる境界へ整理し、閾値は緩和しません。
- 2026-07-13: 実 Docker を使う workload runner テストは、独立した gate 実行が同じ
  daemon 上で競合しないよう、実行ごとに固有の world ID を使う方針にしました。
  また runner は `docker stop` の受付だけで成功とせず、auto-remove 完了を bounded wait
  してから cleanup 成功を返し、直後の一覧や network prune に tombstone を残しません。
- 2026-07-13: production image smoke test の native credential fixture も各 provider gateway
  の実入力境界を満たす値に固定します。起動前 validation に失敗した場合は、auto-remove で
  原因を消さず container log を出力してから bounded cleanup し、EXIT trap が元の失敗を
  上書きしないようにします。

### Authoritative managed placement projection - 2026-07-13

#### 目的

`microservice-migration-battle` の managed tier を participant workload の `/meta.platform`
ではなく、Simulator の world / deployment / target scoped resource graph から検証できるように
します。

#### 制約

- participant が入力した platform 名や URL を信頼しません。
- 任意の participant code や image は実行しません。
- AWS native wire protocol の未対応部分を成功扱いせず、generic provider-operation API 上の
  L1 control-plane projection として境界を明示します。
- 同じ slot 向けの未 binding 候補は複数許可しますが、1 endpoint は 1 active binding、
  1 managed resource は 1 endpoint だけに binding できます。別 world、deployment、target、
  未 ready、非 participant resource は fail closed にします。

#### タスク

- [x] Lambda / ECS / App Runner の participant-created resource を event log、snapshot、replay に
  残すテストを先に追加します。
- [x] reviewed workload URL を `Runtime::Endpoint` の `OutputKey` から内部導出する binding を
  テスト先行で追加します。
- [x] duplicate binding、resource reuse、scope mismatch、未 ready、unknown tier の拒否を固定します。
- [x] redacted placement projection、capability、protocol、README を更新します。
- [x] provider tests、typecheck、lint、repository gates を実行します。
- [x] create response loss 時の namespace / deployment lookup と idempotent cleanup を追加します。
- [x] snapshot restore response loss lookup と portable workload rematerialization を追加します。

#### 検証手順

`bun test providers/aws/tests/managed-placement.test.ts`、`bun run typecheck`、
`make before-commit` を順に実行します。

#### 進捗ログ

- 2026-07-13: 既存の event-sourced resource graph、generic provider-operation API、
  `Runtime::Workload` materialization を再利用する設計を選定しました。participant URL を binding
  input に取らず、reviewed workload の loopback endpoint だけを内部で採用します。
- 2026-07-13: cross-repository 監査で world create の commit-after-response-loss を検出し、
  create-world idempotency record を使う `/v1/worlds/by-deployment/{deploymentId}` durable lookup と
  deleted world の再 cleanup を追加スコープにしました。
- 2026-07-13: snapshot clone と create recovery の identity を分離し、restore result lookup、旧 URL と
  managed placement の sanitization、new-world workload rematerialization、部分成功 retry を追加しました。
- 2026-07-13: workload effect の event quota を SQLite の `materialization` lease で先取りし、
  `deletion` intent と `BEGIN IMMEDIATE` 内で相互排他にしました。dead materialization lease は回収し、
  deletion intent は cleanup / tombstone commit failure と close 後も保持します。再送 DELETE と起動時
  reconciliation が dead owner を引き継ぐため、cleanup 成功直後・tombstone 前の SIGKILL でも後続
  materialization を拒否して orphan を回収できます。
- 2026-07-13: Docker の遅延生成 fence は image を world effect 前に explicit pull / inspect し、両方の
  `docker run` を `--pull=never` に固定しました。cleanup は container と deterministic network の連続不在を
  Docker timeout 以上確認し、late proxy / late network の実 Docker regression test を追加しました。
- 2026-07-13: lifecycle / store / runtime focused 132 tests と workload runner の実 Docker 15 tests、core / server /
  runner typecheck、変更対象の Biome check、textlint、`git diff --check` が成功しました。
- 2026-07-13: PR #7 の provider result identity、SSM token redaction、capability compatibility、exact
  SQLite DDL hardening を取り込みました。reservation は schema version 2 の canonical table とし、exact
  version 1 からだけ atomic migration します。

#### 振り返り

participant input から placement eligibility、tier、URL を決めず、provider-owned resource type と
reviewed workload projection を結合することで trust boundary を明確にできました。外部 effect と
SQLite transaction の間は完全な原子性を持てないため、durable quota reservation、single-flight、
delete 待機、再送 cleanup を組み合わせ、failure と process restart を idempotent recovery path に
含めました。focused 136 tests、typecheck、lint、architecture harness、full 556 tests、100％ coverage、build
を含む `make before-commit` が成功しました。

### Snapshot authenticity boundary - 2026-07-13

#### 目的

公開 snapshot hash を再計算できる caller が provider resource、event、deployment、output を
改変して restore できないように、認証済み snapshot export と restore の間へ server-held
integrity proof を追加します。

#### 制約と設計判断

- proof は launch token authority の既存 secret を再利用しますが、launch token とは別の
  versioned domain を HMAC 入力へ含めます。
- 署名対象は proof 自身を除く API snapshot envelope 全体の canonical JSON とします。
- proof の version、algorithm、base64url 形式、追加 field を schema と runtime の両方で厳密に
  検証し、signature 比較には timing-safe comparison を使います。
- generic API は signer と verifier の明示 injection を必須にし、未設定時に署名なしで動作する
  fallback を持ちません。
- authenticated POST は proof 検証に成功するまで core snapshot 変換、restore、SQLite write を
  実行しません。

#### データの流れ

1. GET が core snapshot を API envelope へ変換します。
2. server authority が proof を除く envelope を canonicalize して HMAC proof を付与します。
3. POST が schema を検証し、authority verifier で proof を timing-safe に照合します。
4. proof が正しい場合だけ API envelope を core snapshot へ変換し、transactional restore を開始します。

#### テストと検証

- [x] exact signed snapshot の restore、replay、response-loss lookup を維持します。
- [x] provider resource、event、deployment、output の改変と public hash 再計算を拒否し、DB count が
  変化しないことを確認します。
- [x] 別 source、deployment、namespace の proof、malformed proof、追加 proof field を拒否します。
- [x] response と log に authority secret が露出しないことを確認します。
- [x] contracts、API、server の focused tests、typecheck、architecture harness を実行します。

#### 進捗ログ

- 2026-07-13: snapshot version 2、strict proof schema、launch authority HMAC、generic API injection、
  restore 前 verification を実装しました。version 1 は authenticity を持たないため失効させました。
- 2026-07-13: canonical base64url の padding-bit malleability と locale-dependent key sort を監査で検出し、
  exact ASCII comparison と UTF-16 code-unit 順の versioned canonicalizer へ修正しました。
- 2026-07-13: forged resource、event、deployment、output、cross-scope proof、unsigned / malformed proof、
  response-loss replay を含む focused 44 tests、root typecheck、textlint、architecture harness が成功しました。
