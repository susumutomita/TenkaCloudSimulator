# LocalStack / Kumoの再利用判断

調査日2026-11-03。既存Simulatorの旧版ではなく、[sivchari/kumo](https://github.com/sivchari/kumo)と[localstack/localstack](https://github.com/localstack/localstack)の公開sourceを対象とする。自分の未公開SDK作業と既存PRを保全し、参照checkoutは別directoryに取得した。upstreamのプログラム・テスト・Docker imageは実行していない。

| 対象 | 固定commit・ライセンス | 実装と単一バイナリへの適合 |
| --- | --- | --- |
| Kumo | `ce37a8fb406ecb96b5619204f645c26302b651a5`。[MIT原文](https://github.com/sivchari/kumo/blob/ce37a8fb406ecb96b5619204f645c26302b651a5/LICENSE)、copyright2025 sivchari。LICENSE SHA256 `02f8d83277fea29291727639f45ddafcbcbf7cbc2d92ea1297eb84f0b8ee9fe2` | Go1.26.0/toolchain1.26.8。stdlibだけのprotocol/path/event処理はGoへ部分移植しやすい。大量のAWS SDK dependencies、internal package境界、MemoryStorage/JSON persistenceは現在のSQLite/所有権/認可へ適合させる必要がある |
| LocalStack公開source | `8b9a79f05846835cf4dff63ab7eefdde9df83783`。[LICENSE.txt](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/LICENSE.txt)とpyprojectはApache-2.0表記。LocalStack contributors2017+、Atlassian2016。LICENSE SHA256 `dcf0bef59ebd52c34046e43df3480d47f26c2f1f2ebcc8dd1ba0bb71ce0b058b` | 主にPython。Docker/moto-ext等の依存、Lambda container executorがあり、丸ごとの組込は現行Go/WASI構成に適合しない。API semantics・resource provider・テストを選び、出典/変更/NOTICEと各依存の権利を確認してGoへ適合する候補 |

LocalStack公開repoはarchive済み。[公式移行説明](https://blog.localstack.cloud/the-road-ahead-for-localstack/)では2026年3月以降の配布image/account方式と旧Community sourceを区別している。公開snapshotのApache表記を現行商用image・非公開機能への利用許可と扱わない。[現行WAF公式資料](https://docs.localstack.cloud/aws/services/waf/)はUltimate plan表記で、公開sourceからのWAF実装再利用権を確認できたという意味ではない。

## 次の既存WP問題に使う範囲

| 必要範囲 | 調査したsourceと判断 |
| --- | --- |
| API Gatewayのpath/method解決とproxy event | Kumo `internal/service/execapi/path.go`、`event.go`、`apigateway/executeapi.go`を最初の移植候補とする。literal/parameter/greedy/ANYの導線とevent fieldを再利用し、empty greedy・曖昧な優先度・body/query/header境界はAWS一次資料とローカルregressionで確認する |
| APIからLambdaの実行 | Kumoの`execapi.InvokeFunctionSync`はHTTP clientを使い、自身のLambda endpointへ送る。ここは現在の宣言済みtargetと実WASI invocationへ結び、任意URL/token/identityのproxyを作らない |
| Lambda workload | Kumo `lambda/handlers.go`のInvokeはRuntime API handlerまたはInvokeEndpointへ配送する。同期実行backendが無ければ502を返し、code自体を単独で実行するものではない。現在の固定CPython/WASIを維持し、error/timeoutなど適合するprotocol/testを再利用する |
| CloudFormation | Kumo `cloudformation/storage.go`の`parseTemplateResources`はJSONからresource記録/ID/CreateCompleteを作る。この記録生成だけではS3/SSM/Lambdaの実共有状態、YAML intrinsic、所有権、custom callbackを満たさない。現在のnative transaction/lifecycleに必要resource providerを移植・接続する |
| WAFv2/HTTP検査 | 固定Kumo snapshotにWAFv2 handlerは見つからない。WebACL ARNを保持する他serviceのfieldはWAF対応として数えない。LocalStackの公開部分・依存ライセンスも個別確認し、実際のHTTPにCount/Blockが作用するところまで検証する |
| EC2/VPC/SSMの残りの問題 | Kumoのcontrol-plane model/APIは後続の移植候補。記録上のinstance作成だけで、既存問題が必要とする実Linux/Session Manager/workloadを実行できるとは判定しない |

Kumo READMEのサービス数は、各問題を採点まで通せることの証明ではない。移植する単位ごとに同じSQLite state、region/account、保存roleのIAM、2チームのauthority分離、作成→操作→採点→削除を検証する。Cloudscapeと埋込Go単一バイナリを保持する。先に完了した実装を別backendへ二重化しない。

## sourceを取り込むときの条件

この調査commitにはupstream実装code・実行dependencyを追加していない。次のAPI Gateway incrementで再利用するfileを固定し、original SHA256、commit/source URL、変更内容を記録する。MIT部分を移植する場合、元のcopyrightと完全なpermission noticeをsource/third_partyと埋込license表示へ保持する。Apache部分は元のcopyright/適用NOTICE、完全なlicense、変更した旨と関連dependencyの権利を確認する。これらが欠けるcodeは配布対象にしない。

既存のbounded function SDK incrementは、既存問題のPython sourceをそのまま実行してSSM/S3へ接続する検証を完了させる。次のAPI Gateway実装には上記KumoのGo sourceを適合し、全てをゼロから設計し直す方法にはしない。WP全体はHTTP/WAF/custom lifecycle/7採点が揃うまで選択不可のままとする。
