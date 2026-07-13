# Issue 2574 実装計画

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

Capability manifest の source provenance は command module の位置から repository root
を固定し、実 Git `HEAD` と worktree の clean state を出力前に照合します。caller の cwd
を採用する案は別 repository の commit を誤って名乗れるため、環境変数で root を渡す案は
release job の入力を新たな信頼境界にするため採用しません。Git 検査は shell を介さず、
timeout と出力上限を持つ subprocess にします。

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
