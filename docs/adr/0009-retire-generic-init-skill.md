# ADR-0009: Generic init-project skill を廃止する

## Status

Accepted

## Context

repository 作成元の template は `/init-project` skill で `packages/backend` と
`packages/frontend` を生成します。TenkaCloud Simulator の boundary は Issue 2574 と
ADR-0007 で `contracts`、`core`、provider plugin、API、Console、scanner、conformance に
固定しました。

generic skill を残すと、依存方向と harness scope が異なる package tree を再生成できます。
また、repository は既に初期化済みであり、user-facing command としての役割がありません。

## Decision

`init-project` skill と参照を削除します。新しい package は design、ADR、workspace dependency
rule に従って通常の feature flow で追加します。

skill 名は公開 API ですが、この repository の初期 commit から Simulator implementation が
存在しないため、利用可能な release を出す前に廃止します。互換 alias は置きません。

## Consequences

- generic backend/frontend tree を誤って生成する経路がなくなる。
- Simulator の package scaffold は通常の review と gate を受ける。
- template として本 repository を使う用途はサポートしません。
- `.claude/` を変更するため、skill audit の Quick Workflow を必須にする。
