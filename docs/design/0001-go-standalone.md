# Go単一バイナリの設計

`cmd/simulator`はCLI、numeric loopback listener、token-file、shutdownを所有する。`internal/server`は埋込Cloudscape/Scalar、local token境界、能力表、identity control APIを提供する。`internal/provider`はprovider dispatch、`internal/core`はprovider中立のSQLite object stateと限定identity-policy評価。`internal/aws`はS3 REST/XML、SSM JSON 1.1、CloudFormation Query/XMLとAWS所有テーブルを持つ。

Coreは問題ID/採点を知らない。SSM・stack/ownershipのschemaはAWS layerがcoreの共有DBを使用して初期化する。CFNはcompileで全リソースと参照を検証後、stack、ownership claim、parameter、identityを単一transactionで作成。削除でも同一transaction。手動削除されたparameterのclaimを残し、他stackが再取得して旧stackのcleanupで消される事故を防ぐ。role namespaceはglobal、SSM/stackはregion別。

Lambda定義/ZIP/region/role/environment/revisionはAWS layerのprovider-owned SQLite tableに保存する。native APIのIAM・PassRoleを評価した後、provider-neutralなinternal/workload/pythonへコード/event/contextだけを渡す。固定CPython/WASI・選択stdlib・原文rightsを埋め込み、host filesystem/environment/process/socketは渡さない。guest memory/時間/出力量/同時実行を制限する。現段階に関数内SDK bridge、Lambda CFN、問題固有のhandlerはない。[制限](../lambda.md)。

Consoleは公式Cloudscape/Reactを開発時にbundleし、fonts/assets/licensesを埋め込む。独自模倣CSSではない。UI/SDK/Scalarはいずれも実HTTPを通り同一SQLite状態を使う。実行時Node/npm、CDN、クラウドproxy不要。UIは実AWS全機能と同一ではない。[比較](../console.md)。

CLI token authorityはoperatorと15分participantを区別し、participant identity header上書きを拒否する。主催ホストはチームごとに別プロセス/DB/tokenを管理し、ログイン済み利用者とチームを認可する。単一プロセス内のidentity選択でチーム分離しない。library compositionのserver.Newは認証を自動追加しない。

SQLiteは既存のncruces SQLite/Wasm engineをGo process内で実行する。外部SQLite実行ファイルやWasm runtimeサービスは不要。有限quotaと接続数制限を維持し、実AWSの整合性/リージョン/アカウント/クォータ全体を再現しない。

Protocol/API subsetと制限は[README](../../README.md)。AWS以外は未実装だがprovider registry/coreの分離を保ち、将来Azure/GCPをAWS handlerに混ぜない。旧Python world/deploy/grading protocolは復活させず、必要なSSM/CFNの知識だけGoへ移行。問題取り込み・採点はTenkaCloud/Challenge責務とする。

CloudFormationのRef/GetAtt/Subはresource dependency graphを先にtopological sortし、forward referenceとcycleを識別する。リソースの論理名の辞書順に依存させない。DeleteStackはimmediate transactionを取得してからstack documentとownershipを読み、同じtransactionで削除する。遅延した旧stack IDの削除が、再利用された物理名の別stackを壊さない。

CloudFormationのIAM判定は要求開始時のpolicy snapshotを使い、Createは生成対象ID（冪等retryは既存ID）、named Describe/Deleteは実際のStack ARNを評価する。DeleteのID解決と認可は同じwrite transaction内。全stackのDescribeにはglobal許可に加え各ARNの明示Denyも評価し、拒否時に部分Outputsを返さない。

IAMはglobal namespaceなので、自動Role名はStack固有IDとlogical IDから接尾辞を生成し64文字に収める。同名Stackの別リージョン作成を衝突させない。明示RoleNameはその名前を保持し、CAPABILITY_NAMED_IAMを必須とする。既存の保存済みphysical IDは読み出した値を使い、再計算・改名しない。[AWS IAM Role仕様](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-iam-role.html)、[CloudFormation action/resource対応](https://docs.aws.amazon.com/service-authorization/latest/reference/list_cloudformation.html)を参照。
