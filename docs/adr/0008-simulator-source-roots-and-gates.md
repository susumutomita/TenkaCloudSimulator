# ADR-0008: Simulator source root と実行 gate を固定する

## Status

Accepted

## Context

リポジトリ作成時の TypeScript template は application source を `packages/`、`src/`、
`scripts/` と仮定していました。Issue 2574 が定める repository boundary は
`contracts/`、`core/`、`providers/`、`apps/`、`tools/`、`conformance/` です。

template の scope を残したまま実装すると、anti-MVP、型 escape、mock data、browser security、
公開品質の検査が Simulator 本体を通過してしまいます。また、template の
`make before-commit` は workspace の typecheck、test、coverage、build を実行しません。
gate が green でも application が未検証になる構造です。

## Decision

application source root を `contracts/`、`core/`、`providers/`、`apps/`、`tools/`、
`conformance/`、`scripts/` に固定します。file scope を持つ harness rule と pre-release rule は、
共通 helper を使って全 root を検査します。

`core/` の provider independence を専用 invariant で検査します。core から provider package への
import と provider literal による分岐を error にします。

`make before-commit` は architecture harness、harness test、text lint、Biome に加えて、
workspace 全体の typecheck、test、coverage、build を必須にします。CI と local gate は同じ
Make target を使います。

scope と gate の検出 logic には regression test を追加します。設定を緩めて通す変更は
認めません。

## Consequences

- 新しい top-level package も同じ品質 rule を受ける。
- source root の追加には、本 ADR を supersede する ADR と harness test が必要である。
- application package が 1 つでも script を欠く場合、before-commit は loud に失敗する。
- build と coverage の実行時間は増えるが、green gate が application の検証を意味する。
- fixture と generated artifact は明示した helper でだけ除外し、パスごとの例外を増やしません。
