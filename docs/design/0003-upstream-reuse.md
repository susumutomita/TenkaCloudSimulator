# LocalStack / Kumoの歴史的調査と現在の実装方針

以下は2026-11-03の歴史的調査（historical）。当時の部分移植候補という判断は、現在の独自runtime方針により置き換えられた（superseded）。固定source・license・元の権利記録は維持する。既存Simulatorの旧版ではなく、[sivchari/kumo](https://github.com/sivchari/kumo)と[localstack/localstack](https://github.com/localstack/localstack)の公開sourceを対象とする。当時の参照checkoutは別directoryで調査した。upstreamのプログラム・テスト・Docker imageは実行していない。

| 対象 | 固定commit・ライセンス | 実装と単一バイナリへの適合 |
| --- | --- | --- |
| Kumo | `ce37a8fb406ecb96b5619204f645c26302b651a5`。[MIT原文](https://github.com/sivchari/kumo/blob/ce37a8fb406ecb96b5619204f645c26302b651a5/LICENSE)、copyright2025 sivchari。LICENSE SHA256 `02f8d83277fea29291727639f45ddafcbcbf7cbc2d92ea1297eb84f0b8ee9fe2` | Go1.26.0/toolchain1.26.8。stdlibだけのprotocol/path/event処理はGoへ部分移植しやすい。大量のAWS SDK dependencies、internal package境界、MemoryStorage/JSON persistenceは現在のSQLite/所有権/認可へ適合させる必要がある |
| LocalStack公開source | `8b9a79f05846835cf4dff63ab7eefdde9df83783`。[LICENSE.txt](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/LICENSE.txt)とpyprojectはApache-2.0表記。LocalStack contributors2017+、Atlassian2016。LICENSE SHA256 `dcf0bef59ebd52c34046e43df3480d47f26c2f1f2ebcc8dd1ba0bb71ce0b058b` | 主にPython。Docker/moto-ext等の依存、Lambda container executorがあり、丸ごとの組込は現行Go/WASI構成に適合しない。API semantics・resource provider・テストを選び、出典/変更/NOTICEと各依存の権利を確認してGoへ適合する候補 |

LocalStack公開repoはarchive済み。[公式移行説明](https://blog.localstack.cloud/the-road-ahead-for-localstack/)では2026年3月以降の配布image/account方式と旧Community sourceを区別している。公開snapshotのApache表記を現行商用image・非公開機能への利用許可と扱わない。[現行WAF公式資料](https://docs.localstack.cloud/aws/services/waf/)はUltimate plan表記で、公開sourceからのWAF実装再利用権を確認できたという意味ではない。

## 当時の既存WP問題向け候補（historical / superseded）

| 必要範囲 | 調査したsourceと判断 |
| --- | --- |
| API Gatewayのpath/method解決とproxy event | Kumo `internal/service/execapi/path.go`、`event.go`、`apigateway/executeapi.go`を当時の最初の移植候補とした。literal/parameter/greedy/ANYの導線とevent fieldを再利用する案を調べ、empty greedy・曖昧な優先度・body/query/header境界はAWS一次資料とローカルregressionで確認する |
| APIからLambdaの実行 | Kumoの`execapi.InvokeFunctionSync`はHTTP clientを使い、自身のLambda endpointへ送る。ここは現在の宣言済みtargetと実WASI invocationへ結び、任意URL/token/identityのproxyを作らない |
| Lambda workload | Kumo `lambda/handlers.go`のInvokeはRuntime API handlerまたはInvokeEndpointへ配送する。同期実行backendが無ければ502を返し、code自体を単独で実行するものではない。現在の固定CPython/WASIを維持し、error/timeoutなど適合するprotocol/testを再利用する |
| CloudFormation | Kumo `cloudformation/storage.go`の`parseTemplateResources`はJSONからresource記録/ID/CreateCompleteを作る。この記録生成だけではS3/SSM/Lambdaの実共有状態、YAML intrinsic、所有権、custom callbackを満たさない。現在のnative transaction/lifecycleに必要resource providerを移植・接続する |
| WAFv2/HTTP検査 | 固定Kumo snapshotにWAFv2 handlerは見つからない。WebACL ARNを保持する他serviceのfieldはWAF対応として数えない。LocalStackの公開部分・依存ライセンスも個別確認し、実際のHTTPにCount/Blockが作用するところまで検証する |
| EC2/VPC/SSMの残りの問題 | Kumoのcontrol-plane model/APIは後続の移植候補。記録上のinstance作成だけで、既存問題が必要とする実Linux/Session Manager/workloadを実行できるとは判定しない |

Kumo READMEのサービス数は、各問題を採点まで通せることの証明ではない。移植する単位ごとに同じSQLite state、region/account、保存roleのIAM、2チームのauthority分離、作成→操作→採点→削除を検証する。Cloudscapeと埋込Go単一バイナリを保持する。先に完了した実装を別backendへ二重化しない。

## 当時の取り込み条件と権利記録（historical）

当初の調査commit自体にはupstream実装code・実行dependencyを追加していなかった。当時の計画はAPI Gatewayで再利用するfileを固定し、original SHA256、commit/source URL、変更内容を記録するものだった。現在は下記の独自runtime方針を適用する。MIT部分を移植する場合、元のcopyrightと完全なpermission noticeをsource/third_partyと埋込license表示へ保持する。Apache部分は元のcopyright/適用NOTICE、完全なlicense、変更した旨と関連dependencyの権利を確認する。これらが欠けるcodeは配布対象にしない。

## 現在のruntime方針と置換範囲

Kumoには、service registry、protocol境界、SDK endpoint、service wiringの設計を理解する参考として敬意を払う。現在はKumo/LocalStackのengineを土台にせず、公式protocol仕様とこのSimulatorのSQLite・認可・実行境界に沿う処理を維持する。上の候補一覧は新たなsource移植の指示ではない。

歴史的なselected type/lifecycleの適合記録は `metadataHistorical` として扱い、固定source URL/hashと元のMIT permissionを消さない。[IAM](../../third_party/kumo-iam.json)、[API Gateway](../../third_party/kumo-apigateway.json)、[KMS](../../third_party/kumo-kms.json)、[Logs](../../third_party/kumo-cloudwatch-logs.json) のprovenanceは履歴の参照先であり、現在のruntime全体が上流由来という意味ではない。

置換対象は、IAMの明示XML投影、REST/KMSの明示JSON投影、API Gatewayのroute programとnative requestからの閉じたevent投影、Logsの明示JSON投影・同一transactionでのcatalog admission・batch window計画である。標準wire field名は互換性のため保持する。既存DB文書の読取、SQLite/SQL、trust、managed-policy合成、KMS crypto/AAD、Logs HMAC/cursor/read SQLなど本来のローカル処理は、この置換のために別engineへ移さない。

この記述はclean-room、著作者、法的適法性の認定でも、新旧全体CIが成功したという主張でもない。有限モデルは各モデルが列挙する範囲だけを検査し、runtime実装全体や権利関係を証明しない。WP全体はHTTP/WAF/custom lifecycleと全採点の受入条件が揃うまで選択不可のままとする。
