# ADR-0013: root script を strict typecheck の対象にする

- **Status**: Accepted
- **Date**: 2026-07-13
- **Deciders**: Susumu Tomita (`susumutomita`)

## Context

ADR-0008 は `scripts/` を application source root に含めました。一方、root の
`typecheck` command は workspace package の script だけを filter 実行していたため、
architecture harness、release verification、CLI entrypoint の型エラーを検出しませんでした。
実行テストと Biome はこの欠落を代替できず、strict option と
`noUncheckedIndexedAccess` の契約が root script に適用されない状態でした。

## Decision

`tsconfig.scripts.json` を追加し、共有 strict option を継承して `scripts/**/*.ts` を
production と test の両方で型検査します。root の `typecheck` はこの検査を先に実行し、
その後に全 workspace の既存 typecheck を実行します。

対象 file を coverage や typecheck から除外する案と、script ごとに個別 command を置く案は
採用しません。前者は application root 契約を弱め、後者は新しい script の接続漏れを
再発させるためです。

## Consequences

- **Good**: harness と release verification を含む全 application root の型安全性を、
  `make before-commit` と CI の同じ gate で証明できる。
- **Bad**: root script が workspace source を import すると、その公開型まで strict 検査の
  依存 graph に入る。
- **Tradeoff**: script が別 runtime や別 compiler option を必要とする場合は、対象 root を
  除外せず、専用 tsconfig を追加して root typecheck から明示的に呼び出す。

## References

- 関連コード: `tsconfig.scripts.json`
- 関連コード: `package.json`
- 関連 Issue: https://github.com/susumutomita/TenkaCloud/issues/2574
- 関連 ADR: [ADR-0008](./0008-simulator-source-roots-and-gates.md)
