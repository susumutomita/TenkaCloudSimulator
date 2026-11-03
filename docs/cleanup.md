# 旧コード整理

削除したもの:

- internal/server/web/style.css: 旧自作コンソールCSS。新indexはCloudscape app.css/app.jsだけを参照し、旧CSS importは存在しない。
- parameters.html / parameters.css / parameters.js: 作業途中の独立SSM画面。Cloudscape単一React entryへ統合済みで参照なし。
- .claude/scripts/check-test-style.sh / stop-gate-reminder.sh / follow-up-reminder.sh: settings.jsonのhookに登録されず、旧TS/Bunの構成を前提にしたscript。現在のgateはGNUmakefile→Makefile。

旧architecture-harness/skill-audit等の説明は、存在しないTypeScript検査器やBunテストではなく実在するGo gateと参照先検査へ更新。履歴や有効な設計知識、ライセンス原文は保持。

残すPythonの理由:

- test/e2e/check.py / audit.py: 実バイナリ・boto3・Chromium/axeの検証。実行時依存ではない。
- scripts/check-coverage.py / test-coverage-gate.py: statement/line両方90%を強制し、欠落・不正レポートも失敗させる。
- check-assets / check-cross-build / check-formal / get-audit-tools / update-go-notices: CIで使う検証・license provenance。
- tools/scalar/notices.py: bundled packagesの原文rights evidenceの再生成。
- build-vulndb-snapshot.py: canonical vuln.go.devがこの環境で403のため、公式Go vuln DB mirrorの原文OSVを使う検査補助として使用。canonical CIの代替成功と偽らない。
- catalog-matrix.py: 実カタログmetadataと参照templateから対応表を再生成。問題コードは実行しない。
- tools/python/package.py / check.py: 新しい埋込CPython/WASIの固定artifact・stdlib・原文rightsを構築/検証。ホストPythonは実行時に不要。
- internal/workload/python/bootstrap.py: go:embedでWASI内へ渡す有効な関数呼出glue。旧Pythonクラウドruntimeではなく、既存Python Lambdaコードの実行に必要。

現行mainには旧Pythonクラウドruntime自体は既に存在しなかった。hello-worldに必要なCFN/SSM知識はGoへ復旧し、残るPythonを言語だけで消して機能・検査を失わせない。検索、埋込assetチェック、build、SDK/browserテストで参照漏れを確認する。
