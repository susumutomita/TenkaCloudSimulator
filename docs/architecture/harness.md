# Architecture Harness

architecture harness は、TenkaCloudSimulator の repository state に対して決定論的に判定できる契約だけを機械強制します。思考手順、開発順序、role play は対象外です。

application source root は `contracts/`、`core/`、`providers/`、`apps/`、`tools/`、`conformance/`、`scripts/` です。

## Enforced categories

- dependency lifecycle、lockfile、GitHub Actions、既知 IOC などの supply-chain boundary。
- focused test、型エスケープ、未実装 marker、runtime mock data などの客観的シグナル。
- `.claude/` の metadata、hidden instruction、remote execution pattern。
- public UI の metadata、external link、image alt、accessible name。
- `core/` の provider independence と、全 application source root の typecheck coverage。
- production image と runtime cleanup に関する CI gate。

検出ロジックとテストは `scripts/architecture-harness.ts`、`scripts/architecture-harness.test.ts` などの実装を正本とします。

## Deliberately not enforced

- `Plan.md`、Issue、設計文書の作成。
- docs → refactor → feature の固定順序。
- TDD の実行順序、テストタイトルの言語、blanket No Mock。
- 特定 Skill、review、subagent、固定人数の role。
- SessionStart、Stop、PreCompact、PostToolUse の reminder または自動介入。

これらは必要なタスクでだけ使います。完了は [`quality-bar.md`](./quality-bar.md) と受け入れ条件で判断します。

## Commands

```bash
bun scripts/architecture-harness.ts --staged --fail-on=error
make before-commit
```

CI は全件 harness、typecheck、test、coverage、build、production image E2E を最終強制点として実行します。

## Rule lifecycle

新しい rule は、同じ失敗の反復、低誤検知の決定論的判定、修正可能な message、検出器テストを必要とします。既存 lint、typecheck、test、CI と重複させません。

model、runtime、toolchain が改善して不要になった rule は削除します。誤検知または前提の陳腐化を証拠で示せる場合、成果物を無理に変形せず rule 自体を修正します。
