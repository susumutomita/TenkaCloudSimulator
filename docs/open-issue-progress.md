# Issue と進捗の参照先

進捗、担当、依存、検証結果は対象 Issue または PR で管理します。
この文書には Issue 全件の一覧や件数、作業途中のスナップショットを複製しません。

- [Issue 一覧](https://github.com/susumutomita/TenkaCloudSimulator/issues)
- [PR 一覧](https://github.com/susumutomita/TenkaCloudSimulator/pulls)
- [CI](https://github.com/susumutomita/TenkaCloudSimulator/actions/workflows/ci.yml)
- [対応範囲](../README.md#find-a-service-guide)と[非対応理由](coverage-assessment.md)

サービス別の調査は既存の親インデックスから辿れます。製品、API、実装単位の
数え方は異なり、親 Issue と子 Issue を加算して完成率にはしません。

- [AWS](https://github.com/susumutomita/TenkaCloudSimulator/issues/52)
- [Azure](https://github.com/susumutomita/TenkaCloudSimulator/issues/316)
- [Google Cloud](https://github.com/susumutomita/TenkaCloudSimulator/issues/317)

着手前に open / closed の Issue・PR、現在の main、既存 branch / worktree と
所有範囲を確認します。新しい Issue や進捗ファイルの作成は必須ではありません。
関連する既存 Issue・PR に、必要な判断、source SHA、実施した検証と未実施・失敗、
残る受入条件を記録します。公開や状態変更の権限は別途守り、拒否された投稿を
別の場所へ転載しません。

main に入った増分とローカル候補、個別検証と全体検証を区別します。
Issue の完了は元の受入条件で判断し、metadata API や API 200 だけでは閉じません。
原本 Battle / WP は、実ワークロード、チーム分離、採点、回復、cleanup の検証なしに
有効化しません。公開配備や永続アクセスも、ローカル実装とは別の権限が必要です。

[開発時の参照先](development-status.md)と[並行開発の規則](contributing/parallel-development.md)も参照してください。
過去の一覧は Git 履歴に残りますが、現在の担当・対応範囲・検証結果として扱いません。
