# ADR-0014: Bun の依存解決に 7 日の minimum release age を適用する

- **Status**: Accepted
- **Date**: 2026-07-16
- **Deciders**: Susumu Tomita (@susumutomita)

## Context

[ADR-0001](./0001-supply-chain-hardening.md) の多層防御は、公開直後のパッケージを
一定期間隔離する「minimum release age」を 2 層で持っている。`.npmrc` の
`min-release-age=168h` は npm / yarn / pnpm へ fallback した contributor を守り、
CI の Safe Chain は `make install_ci` 時に公開 7 日未満の tarball ダウンロードを
403 で遮断する。しかし肝心の Bun 自身の解決には同じ制約が無かったため、
ローカルの `bun install` は最新版をそのまま lockfile に固定し、CI の Safe Chain で
初めてブロックされる。実際に console の Cloudscape 移行
([PR-11](https://github.com/susumutomita/TenkaCloudSimulator/pull/11)) では、
公開 1〜2 日の `@cloudscape-design/components@3.0.1329` などの 4 パッケージが
lockfile に入り、CI の install_ci が落ちた。ローカルと CI で解決規則が食い違うと、
違反は push 後にしか検出できない。

## Decision

root `bunfig.toml` の `[install]` に `minimumReleaseAge = 604800` (168h) を設定し、
Bun の依存解決自体に隔離期間を適用する。Bun 1.2.20 以降がサポートする公式機能で、
range 解決 (`^` / `~`) は「公開から 7 日以上経過した最新版」へ自動的に丸められ、
7 日未満のバージョンを明示 pin した場合は install が失敗して手元で即座に分かる。
隔離期間は `.npmrc` / Safe Chain と同じ 168h に揃え、3 層が同じ判定を返すようにする。
緊急のセキュリティパッチ等で例外が必要になった場合は、`minimumReleaseAgeExcludes`
への追加を ADR で正当化してから行う。

## Consequences

- **Good**: ローカルの `bun install` / `bun update` が CI と同じ判定を返し、
  隔離違反が push 前に検出できる。乗っ取り直後の悪性バージョンを掴む窓が閉じる。
- **Bad**: 新機能・新パッチの取り込みが最大 7 日遅れる。Dependabot が
  公開直後のバージョンへ更新する PR は、エージングが済むまで install が失敗する。
- **Tradeoff**: 隔離無し (従来) は最新版を即座に使えるが、CI で初めて落ちる
  非対称性と引き換えだった。隔離期間の長短を再検討するトリガーは、
  上流の重大脆弱性修正を 7 日待てないケースが実際に発生したとき。

## References

- 関連コード: `bunfig.toml`
- 関連 PR / Issue: [PR-11](https://github.com/susumutomita/TenkaCloudSimulator/pull/11)
- 関連 ADR: [ADR-0001](./0001-supply-chain-hardening.md)
- 外部資料: [Bun install lifecycle & security 設定](https://bun.com/docs/install/lifecycle)
