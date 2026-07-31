# AGENTS.md

TenkaCloudSimulator の AI エージェント向け作業契約です。方法ではなく、リポジトリ境界、安全、検証可能な完了条件を固定します。

## Repository boundary

TenkaCloudSimulator は、クラウド操作をローカルで再現する capability contract と runtime を所有します。

- TenkaCloudChallenge は問題、scenario、metadata、grading、必要 capability を所有する。
- Simulator は実装可能な capability を宣言し、problem ID や問題固有の分岐を持たない。
- TenkaCloud は Challenge と Simulator の compatibility を比較し、platform integration を行う。
- `core/` は provider 非依存を保ち、provider 実装は registry から注入する。

既定の stack は Bun、Hono、Vite、React、Biome、Bun test です。

```bash
make install
make dev
```

## Working contract

- 依頼、Issue、既存 contract、conformance test から受け入れ条件を把握する。コードと履歴から解決できる曖昧さはリポジトリ内で確認する。
- 変更前に同じ capability、provider、helper、test fixture を検索する。問題固有ロジックを Simulator へ持ち込まない。
- 方法はタスクに合わせて選ぶ。`Plan.md`、専用 Skill、固定 role、固定人数の subagent、文書先行、TDD の順序は必須ではない。
- contract、実装、conformance、利用側の影響が一貫した working increment を作る。
- 単純な修正を ceremony や multi-agent 化で膨らませない。複雑な provider 境界や security 変更では独立した比較や反証を使ってよい。

## Guardrails

- `.env`、秘密情報、認証情報を読み書きしない。
- 破壊的操作、release、shared environment への変更は、明示的な承認なしに行わない。
- container isolation、filesystem、network、process の境界を security boundary として扱う。
- チェックを通すためだけに test、type、lint、harness、設定を弱めない。設定または invariant が根本原因なら、証拠と検出器テストを伴って修正してよい。
- 障害を固定レスポンス、空値、silent fallback へ変換して隠さない。

## Verification

モデルが自分で成否を判定できる検証を先に見つける。変更に最も近い unit、conformance、integration、production image E2E、preview を選ぶ。

- provider contract 変更は、少なくとも contract と実装の双方を検証する。
- 外部 API、時刻、file、process、container boundary は test double で制御してよい。production runtime に mock fallback を置かない。
- バグ修正は失敗を再現し、修正後に同じ経路で解消したことを確認する。
- cross-repository contract を変える場合は、Challenge と TenkaCloud への影響を PR 本文へ明記する。

PR 前の標準ゲート:

```bash
make before-commit
```

PR 本文には、受け入れ条件、実行した検証、cross-repository impact、残る risk または未検証事項を書く。review 系 Skill は必要なときだけ使い、完了の必須経路にはしない。

## Sources of truth

- architecture invariant: [`docs/architecture/harness.md`](./docs/architecture/harness.md)
- quality: [`docs/architecture/quality-bar.md`](./docs/architecture/quality-bar.md)
- steering: [`docs/architecture/steering.md`](./docs/architecture/steering.md)
