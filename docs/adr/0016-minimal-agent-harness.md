# ADR-0016: Minimal agent harness

- **Status**: Accepted
- **Date**: 2026-08-01
- **Deciders**: Susumu Tomita (`@susumutomita`)

## Context

Simulator には、常時ロードされる長い instruction、固定 5 role の `/feature`、`Plan.md`、日本語 BDD、blanket No Mock、100% coverage、SessionStart / Stop / PreCompact / PostToolUse hook が重なっていた。これらの一部は安全や repository boundary ではなく、過去モデルを補助する実装手順だった。

モデルと toolchain が改善しても同じ補助を残すと、problem-independent な capability 設計より ceremony の消化が優先され、設定や検証器そのものを直す必要がある場面も阻害する。

## Decision

agent harness を task、repository boundary、guardrails、verifiable completion へ縮小する。

- `CLAUDE.md` は `AGENTS.md` を import し、Skill と subagent を任意ツールとして扱う。
- `AGENTS.md` は Challenge / Simulator / TenkaCloud の ownership、provider independence、安全、完了 gate を示す。
- hook は秘密情報と危険 command の実行前防御に限定する。
- `Plan.md`、固定 role、特定 Skill、TDD 順序、テスト言語、blanket No Mock を必須条件から外す。
- contract、conformance、integration、production image E2E など、モデルが自分で成否を確認できる検証を優先する。
- steering は同じ失敗が繰り返され、既存 gate で防げない証拠がある場合だけ追加する。

本 ADR は ADR-0003 と ADR-0004 のうち、一律な作業手順と常設 hook に関する判断を supersede する。supply-chain、container isolation、provider independence、required CI は維持する。

## Consequences

- **Good**: capability と検証へ集中でき、モデル世代に合わせて方法を選べる。
- **Bad**: すべての変更が同じ作業ログと role play を通る一貫性は失われる。
- **Tradeoff**: 反復する失敗が見つかった場合は、再現例と eval を作り、最小の rule または Skill だけを戻す。

## References

- `CLAUDE.md`
- `AGENTS.md`
- `.claude/settings.json`
- `.claude/rules/test-authoring.md`
- `docs/architecture/steering.md`
- `docs/architecture/harness.md`
- `docs/architecture/quality-bar.md`
