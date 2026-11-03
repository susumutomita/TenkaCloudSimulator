# 引継ぎ

現行作業はmain 639af7aからの未公開ローカル変更。以前のクラウド環境の最終未保存差分は確認不能で、失われた可能性がある。旧レビューZIPはネットワーク制約で実物検証できず、最新GitHub sourceを基準に復旧した。過去の報告を新実装の検証結果として扱わない。

hello-worldは実カタログ/実ホストのimport、CFN deployment、SSM read、flag採点、重複加点防止、チーム別token/DB/process、teardownをdevelopment integrationで検証。TenkaCloudの通常起動に常設されたproduction adapterではない。実STS/federationや他問題を対応済みとしない。[再現手順](../../test/platform/README.md)。

変更後はmake before-commit/e2e/audit/formal/cross-checkとdependency/security検査を実行。失敗・未実行は明記する。今回のpush/PR/mergeは本人承認済み。既存branch/PRを確認し、重大問題なし・レビュー・exact HEAD必須CI成功後のみmergeする。実AWSやTailscaleの設定・公開は行わない。問題catalogや元の作業を削除/上書きしない。
