# ADR-0015: 重複ラチェットで止め、デッドコードは報告する

- **Status**: Accepted
- **Date**: 2026-07-18
- **Deciders**: Susumu Tomita (`susumutomita`)

## Context

AI エージェント主導の開発では、既存実装を調べずに同等の helper を再実装するコピー&ペーストと、
リファクタ後に呼び出し元だけ消えて取り残される未使用コードが、少しずつ蓄積します。
Biome は認知的複雑度と未使用変数を 1 file 単位で検査しますが、ファイル横断の重複と
未使用 export / 未使用 file は検査範囲外です。人間のレビューも全 file の記憶に依存する
ため、この 2 つは機械検査が必要です。

## Decision

役割の異なる 2 つの検査を、CI での扱いを分けて導入します。

1 つ目は jscpd によるコピー&ペースト検出です。全体重複率の閾値方式では分母に薄まって新規の
コピー&ペーストを止められないため、area (workspace) 単位の重複行数を
`scripts/duplication-baseline.json` に焼き込み、それを超えたときだけ失敗する
ラチェット方式 (`scripts/check-duplication.ts`) を採用します。増分を正確に指せる検査
なので `make before-commit` と CI で止める側に置きます。意図的な責務分離の類似は
baseline に残してよく、baseline を増やす更新は PR body で理由を説明します。

2 つ目は knip による未使用 export / 未使用 file の検出です。knip は変更前との差分を
出す機能を持たず、報告は常に現時点の全量になるため、止める側に置くと既存の負債で
新規 PR が失敗し、無視 comment の量産で形骸化します。そのため `knip.json` の rules を
warn にして exit 0 を保ち、CI では job summary へ知らせるだけ (`make dead_code`) に
します。検出が本物かどうかは人間が判断します。

## Consequences

- **Good**: 新しいコピー&ペーストは CI で確実に止まり、未使用コードは PR ごとに可視化される。
  重複が減った場合は gate が ratchet-down を促すため、負債は単調に減る方向へ働く。
- **Bad**: baseline と knip entry の保守が増える。新しい workspace を追加したときは
  `knip.json` の entry 追随が必要になる。
- **Tradeoff**: jscpd を SARIF + GitHub Code Scanning で差分表示する案は、外部 action
  への依存が増えるため採用しない。knip を error 化して止める案は、全量報告の性質上
  形骸化リスクが高いため採用しない。knip が PR 差分を報告できるようになったら再検討する。

## References

- 関連コード: `scripts/check-duplication.ts`、`.jscpd.json`、`knip.json`
- 関連 ADR: [ADR-0003](./0003-quality-first-no-mvp.md)、[ADR-0008](./0008-simulator-source-roots-and-gates.md)
- 外部資料: https://zenn.dev/singularity/articles/clean-code-ci-for-ai-era
