# Go単一バイナリの設計

`cmd/simulator`はCLI、numeric loopback listener、token-file、shutdownを所有する。`internal/server`は埋込Cloudscape/Scalar、local token境界、能力表、identity control APIを提供する。`internal/provider`はprovider dispatch、`internal/core`はprovider中立のSQLite object stateと限定identity-policy評価。`internal/aws`はS3 REST/XML、SSM JSON 1.1、CloudFormation Query/XMLとAWS所有テーブルを持つ。

Coreは問題ID/採点を知らない。SSM・stack/ownershipのschemaはAWS layerがcoreの共有DBを使用して初期化する。CFNはcompileで全リソースと参照を検証後、stack、ownership claim、parameter、identity、S3 bucket/config/tagsを単一transactionで作成。削除でも同一transaction。手動削除されたparameterのclaimを残し、他stackが再取得して旧stackのcleanupで消される事故を防ぐ。role/bucket namespaceはglobal、SSM/stackはregion別、Lambda claimはlambda:<region>。

Lambda定義/ZIP/region/role/environment/revisionはAWS layerのprovider-owned SQLite tableに保存する。native APIのIAM・PassRoleを評価した後、provider-neutralなinternal/workload/pythonへコード/event/contextと明示したbounded capability・固定module FSを渡す。固定CPython/WASI・選択stdlib・原文rightsを埋め込み、host filesystem/environment/process/socketは渡さない。guest memory/時間/出力量/同時実行を制限する。限定inline Lambda CFNを同じ保存処理へ接続する。関数内SDKは保存role/regionに固定したnative SSM/S3 subsetのみ。問題固有のhandlerや任意HTTP proxyはない。[制限](../lambda.md)。

`internal/inference`はprovider中立の有界テキスト生成を持つ。cloud protocol、IAM、問題ID、採点を知らない。AWS layerのBedrock Runtimeが`bedrock:InvokeModel`を評価し、Anthropic Messages/Converseの本文を厳密に検証してから、operatorが対応付けたbackend model名だけを渡す。Ollama adapterは設定されたnumeric loopback `host:port`だけへproxy・redirect・名前解決なしで接続し、同時実行・期限・入出力サイズを制限する。無効・未対応付け・停止中のbackendは明示エラーで、別backendや合成応答へ切り替えない。promptと応答はSQLite・logへ保存しない。Apple Foundation Modelsは任意の別ビルドSwift helper（Apple silicon/macOS 27+）をoperatorが絶対pathで指定した場合だけ使用する。標準Go buildはhelper/Swiftに依存しない。availability失敗やtoken上限で終了を確認できない応答は明示エラーとする。[範囲](../bedrock-runtime.md)。

Consoleは公式Cloudscape/Reactを開発時にbundleし、fonts/assets/licensesを埋め込む。独自模倣CSSではない。UI/SDK/Scalarはいずれも実HTTPを通り同一SQLite状態を使う。実行時Node/npm、CDN、クラウドproxy不要。UIは実AWS全機能と同一ではない。[比較](../console.md)。

SSM の [ドキュメント履歴](../ssm-documents.md) はリージョンとドキュメント名で検索し、1 回の SQLite 読み取りで番号順のスナップショットを返す。ページごとに IAM を評価し、続きは対象・呼出者・スナップショットに結び付く。一覧に定義本文は含めず、Console の本文比較には別の GetDocument 権限が必要。ドキュメントは実行しない。

SNS の HTTP/HTTPS コンソールは native Query を呼び出し、通知先 URL をローカル SQLite に記録するだけで外部に接続しない。購読属性を読む際は ARN から親トピックを特定して IAM を先に評価し、購読と親の存在およびリージョンの一致を同じ読み取りで確認する。専用 Scalar は us-east-1 固定で通常のログインセッションを使い、コンソールの演習用 ID 選択を引き継がない。[対応範囲](../sns.md)。

AWS カタログの AppFlow は S3 間のフロー設定のみを保持する。[トリガー設定](https://docs.aws.amazon.com/appflow/1.0/APIReference/API_TriggerConfig.html)に従い、`CreateFlow` と `UpdateFlow` は、`OnDemand` に付けたスケジュール設定と、実行間隔の式がない（空白のみも含む）`Scheduled` の定義を `ValidationException` で拒否する。検証は保存前に行うため、拒否された要求で既存の設定は変わらない。スケジューラ、コネクタープロファイル、ジョブ実行およびオブジェクト転送は実装しない。

AWS Batch のジョブキューは、先頭ジョブのタイムアウト設定のみを保存する。設定できる時間は 600～86,400 秒で、コンテナ系では `CANCEL`、SageMaker Training では `TERMINATE` を受け付ける。更新時は変更後のキュー種別とアクションの組み合わせを検証し、拒否された要求で既存設定は変わらない。タイマーやジョブの実行・終了処理は実装しない。

[Cognito User Pools](cognito-user-pools.md) は、検証済みのローカル SigV4 で既定ではプールの作成・参照・一覧・削除だけを扱う。保存先はチームの SQLite であり、サインイン、セッション、SAML、トークン発行は実装しない。

`--cognito-local-federation-config` を明示した認証付き数値ループバック環境では、[テスト用 SAML IdP と公開アプリクライアントの設定作成・参照](cognito-federation-configuration.md)も可能。署名・スコープ・IAM は既定操作と同じ境界で検証する。実 IdP との通信、SAML ログイン、トークン発行、コンソール、Scalar は提供しない。

SimpleDB の `PutAttributes` と `DeleteAttributes` は AWS SDK の標準形式の `Expected` 条件を 1 件だけ受け付ける。属性の単一値との比較、または属性が存在しないことの確認を更新と同じ SQLite トランザクションで行う。条件を満たさない場合は他の属性も変更しない。削除は、値を指定すると一致する値だけ、属性名だけならその属性、属性の指定がなければ項目全体が対象となる。最後の属性を削除すると項目も消える。無条件の削除は何度実行してもよい。番号付きの条件形式とバッチ操作には対応しない。[ネイティブ `Select`](../simpledb-select.md) は同じ SQLite の項目を読み取り、限定した述語とページ送りを提供する。コンソールの項目名検索とAPIリファレンスの読み取り例も同じ操作を使う。新しい値の組が追加されるときは同じ書き込みトランザクションで最大256組を検証し、超過時は他の属性も変更しない。[既存超過データの扱い](../simpledb-item-limits.md)。

[SimpleDB コンソール](../simpledb-console.md)は既存の認可とローカル Query API を使い、選択したドメインで既知の項目を読み取って、1属性だけを条件付きで置換する。書き込みの成功応答を受けてから属性を読み直す。単一値の属性については、表示された値との一致を条件に削除でき、最後の属性を削除すると項目も消える。項目名検索はドメイン内の全件または属性値の完全一致を25件ずつ取得する。カーソルは元の検索と認証スコープに対応し、各ページの権限を個別に判定する。ドメインの削除はコンソールでは扱わない。API Reference の `CreateDomain` は、同じ認可と既存の Query API でローカルの練習用ドメインを作る。消去は SDK の `DeleteDomain` を使う。

Redshift の `DescribeTags` は既存のローカル ARN 1件に限り、同じ認可でタグのキーと値を読む。全体検索や SQL は扱わない。

[ローカル Entra OIDC 検証フィクスチャ](../azure-entra-verification-fixture.md)は、明示的な別コマンドで数値ループバックにだけ待ち受ける。通常のゲートウェイや Azure の権限は変更せず、検証用メタデータと一時公開鍵を返す。テスト用トークンは明示指定した起動出力に限り、実テナント、ログイン、SAML、HTTP発行には対応しない。

Azure Queue は Blob と独立したプロバイダールートを持ち、固定 account・owner・environment に結びつくローカル SQLite にメッセージを保存する。受信と更新は表示予約と pop receipt を更新し、削除は現在の receipt を要求する。Console と API リファレンスは同じ所有者境界とデータプレーンを使う。アカウント全体のキュー一覧や Azure RBAC は模倣しない。[操作範囲と検証条件](../acceptance/azure-queue.md)。

CLI token authorityはoperatorと15分participantを区別し、participant identity header上書きを拒否する。主催ホストはチームごとに別プロセス/DB/tokenを管理し、ログイン済み利用者とチームを認可する。単一プロセス内のidentity選択でチーム分離しない。library compositionのserver.Newは認証を自動追加しない。

SQLiteは既存のncruces SQLite/Wasm engineをGo process内で実行する。外部SQLite実行ファイルやWasm runtimeサービスは不要。有限quotaと接続数制限を維持し、実AWSの整合性/リージョン/アカウント/クォータ全体を再現しない。

[Route 53のPTRレコード](../route53.md)は、公開ゾーンのSQLiteに保存した名前をTestDNSAnswerで返す。外部DNSへの問い合わせや実IPアドレスの逆引き設定は行わない。認証済みの通常コンソールでは既存公開ゾーンとレコードを参照し、1件のPTRを選択ゾーンの同じネイティブ認可で作成できる。INSYNC受領証と同じ名前・種類・TTL・値の再読で確認する。ゾーンの変更、上書き、削除、限定ハンドオフは[この画面の対象外](../route53.md#console-browse-public-zones-and-create-one-ptr)。

VPC コンソールの「VPC とサブネット1つ」は、認証済みのシミュレーター管理者だけが CloudFormation に固定テンプレートを送る。参加者ブラウザーにはスタック出力が開示されないため、この選択肢を表示しない。新規スタックが両方を所有し、作成受領証の正確なスタック ID で再取得できたときだけ成功を表示する。ネイティブ EC2 所有 VPC への追加や、実ネットワークの作成は行わない。[対応範囲](../ec2-read-discovery.md#console-vpc-and-one-subnet)。

API Reference の EC2 用プリセットは、既存の Query API の `DescribeInstances` を呼び、選択リージョンに記録されたメタデータを表示する。起動や稼働確認は行わない。

ネイティブ EC2 の [DescribeSecurityGroups](../ec2-read-discovery.md#security-group-readback) は、既存の EC2 IAM 判定とリージョンの範囲内で CloudFormation 管理グループと未接続のネイティブグループを読む。グループと親 VPC の型付き所有権を同じ SQLite スナップショットで照合し、テンプレートの初期値ではなく今の TCP/IPv4 受信ルールを返す。XML 1.0 で値をそのまま返せない行や壊れた所有権があれば一覧全体を拒否し、保存内容は変更しない。通常のサインイン済み VPC コンソールも同じ API と IAM を使い、選択した CloudFormation 管理の VPC に属するグループだけを表示する。権限不足、読み取り失敗、空の結果は区別する。セキュリティグループの変更やパケット処理は対象外。

[DescribeRouteTables](../ec2-read-discovery.md#route-table-readback) は、CloudFormation 管理とネイティブ EC2 管理のテーブルを同じリージョンの SQLite から返す。有効なデフォルト経路を持てるのは CloudFormation 管理のテーブルだけで、関連付けもスタックが管理する。ネイティブのテーブルとサブネットは、同じ VPC 内に限り [明示的に関連付け・解除](../ec2-read-discovery.md#native-subnet-route-table-associations)できる。両リソースに対する認可を確認し、関連付けが残っていればテーブルもサブネットも削除しない。解除しても暗黙のメインテーブルには移らず、経路の追加やパケットの転送は行わない。

明示的なサブネット関連付けの検索（`association.subnet-id`）は、同じリージョンの整合性検査とIAM判定を通過した表だけを後段で絞り込む。空入力は従来の全件取得、CloudFormationのルートテーブル画面は選択VPCと入力IDに応答を結び付ける。暗黙の関連付け・経路の作成や実通信は扱わない。

[既存 VPC のネイティブ IPv4 サブネット](../ec2-native-subnet.md)は、EC2 管理の親 VPC に対して追加・参照・削除できる。Console と SDK は同じ EC2 API と SQLite を使い、CloudFormation の所有権を変更しない。ルーティングやゲストの到達性は扱わない。
[ネイティブのセキュリティグループとネットワーク ACL](../ec2-read-discovery.md)は、同じ VPC に保存できる未接続の設定である。Console と SDK は同じ Query API で保存・再照会し、インスタンス・サブネットへの関連付けや通信の制御は扱わない。子が残る VPC の削除を拒否する。


[EC2 コンソールのテンプレートプレビュー](../ec2-read-discovery.md#instance-launch-preview-is-not-a-launch)は、現在の演習 ID とリージョンで既存の `ValidateTemplate` のみに接続する。パラメータ名と IAM の確認項目を返すが、既定値の内容やリソースは表示・保存しない。AMI の有無、起動権限、ゲストの実行可否は検証せず、インスタンスは起動しない。

Protocol/API subsetと制限は[README](../../README.md)。限定的なGoogle Cloud Storage/Azure Blob Storageを独立providerとして接続し、認証済みActorから固定project/account所有者へ写像する。[Cloud DNS](../acceptance/gcp-cloud-dns.md) は同じproject所有者の範囲でSRVを含むRRsetの変更と参照を提供する。所有者コンソールではSRVの追加・置換・削除を変更受領証と再読で確認する。SRVの優先度・重み・ポートは保存するが、名前解決や接続先の選択は行わない。AWS IAMの権限を継承せず、owner-onlyローカル認可と公式SDKの検証範囲は[マルチクラウドストレージ](../multicloud-storage.md)に記す。旧Python world/deploy/grading protocolは復活させず、必要なSSM/CFNの知識だけGoへ移行。問題取り込み・採点はTenkaCloud/Challenge責務とする。

Azure Blobの通常APIは `2023-11-03` に固定する。BlobのGET/HEADでは単一の `If-Modified-Since` を扱い、通常のGETには有界の単一範囲CRC64がある。明示的にネゴシエートした `2025-01-05` のGet Blobだけで、最大4 MiBの単一セグメントを構造化CRC64として返す。共通のローカル所有者・読み取り条件を適用し、通常の範囲チェックサムヘッダーと併用しない。それ以外の同バージョン操作やSDK全体の互換性は対象外。[Azure Blobの条件付き読み取りとチェックサムの詳細](../azure-blob.md#single-range-download-subset)。

CloudFormationのRef/GetAtt/Subはresource dependency graphを先にtopological sortし、forward referenceとcycleを識別する。リソースの論理名の辞書順に依存させない。DeleteStackはimmediate transactionを取得してからstack documentとownershipを読み、同じtransactionで削除する。遅延した旧stack IDの削除が、再利用された物理名の別stackを壊さない。

CloudFormation の EC2 インスタンスは、同じスタックで宣言・所有する一つの VPC のサブネットとセキュリティグループにだけ配置できる。作成トランザクションの確定前に宣言、保存行、所有権を照合し、不整合はスタックもイベントも残さず拒否する。実際の VM の起動・準備完了の確認は従来どおりコミット後に行う。

任意の Battle VM 実行環境で、既存の capability resolve はチームの正規 DB に保存された `CREATE_COMPLETE` の Stack ID と EC2 所有権を、起動時の保持済みレシート・ready 記録・lease と照合する。起動コールバックは CloudFormation の確定を待たない。先に到達した resolve は 409 になり、後から再試行できる。seal と既存の NIC 認可は変わらない。この照合は新しい通信経路や RDS の動作を保証しない。

`--rds-private-postgres-topology` は認証付きの数値ループバックでだけ使える任意の設定検証。`Engine=postgres`、単独 DB インスタンス、明示的な `PubliclyAccessible=false` の `CreateDBInstance` / `ModifyDBInstance` を対象にする。通常の設定モードは変えない。既存の `rds` IAM 判定後、書き込みと同じ SQLite トランザクションで DB サブネットグループと現行のネイティブ EC2 所有行を照合する。初期の範囲は、Internet Gateway への接続や有効な外向き経路がない単一の IPv4 VPC、その VPC と同じ所有者に属する2つ以上の AZ のサブネット、および1〜5個の固有のセキュリティグループ。未知・別所有者・別 VPC の参照は書き込まない。これはその時点の設定検証であり、継続的な接続証明、SQL、バックエンド、ポート、エンドポイントの追加ではない。応答は引き続き `creating`。

Linux/amd64 の非 root 運用者は、認証付き数値ループバックで [`--rds-owned-postgres-config`](rds-owned-workload.md) を個別に指定できる。既存の設定検証と、承認済み PostgreSQL バンドル・独立したピン・専用作業ディレクトリ・永続 SQLite を必須とし、Battle との併用は拒否する。起動時は構成を受け入れるだけで、既存の IAM とトポロジー検証を通り、所有権を確定した指定 DB の新規作成だけが内部実行の対象になる。API の応答形式は変えず、接続先・認証情報・稼働の保証やゲスト接続は提供しない。

CloudFormationのIAM判定は要求開始時のpolicy snapshotを使い、Createは生成対象ID（冪等retryは既存ID）、named Describe/Deleteは実際のStack ARNを評価する。DeleteのID解決と認可は同じwrite transaction内。全stackのDescribeにはglobal許可に加え各ARNの明示Denyも評価し、拒否時に部分Outputsを返さない。

IAMはglobal namespaceなので、自動Role名はStack固有IDとlogical IDから接尾辞を生成し64文字に収める。同名Stackの別リージョン作成を衝突させない。明示RoleNameはその名前を保持し、CAPABILITY_NAMED_IAMを必須とする。既存の保存済みphysical IDは読み出した値を使い、再計算・改名しない。[AWS IAM Role仕様](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-iam-role.html)、[CloudFormation action/resource対応](https://docs.aws.amazon.com/service-authorization/latest/reference/list_cloudformation.html)を参照。

S3 version history/public-access-blockはAWS layerのaws_s3_versions/aws_s3_configに保存する。current-object viewは既存coreのobjectsと共有し、history/current/null置換・marker除去を単一transactionで更新する。GET時の旧状態importもtransaction内。config XMLの読込/検証は共有DB transactionより前で行い、遅い本文送信で他操作の接続を塞がない。bucket削除はcurrentだけでなく全history/markersを確認。provider-neutral coreへS3 versioningの分岐を持ち込まない。[保存・IAM・quota詳細](../s3.md)。

CloudFormationはservice RoleArnを受け付けず、呼出者のimmutable policy snapshotでStack ARNに加え全resource actionを事前確認する。SSM Put/DeleteParameter、IAM Create/DeleteRole・inline policy・指定managed policyのattach/detach・作成時TagRole、S3 Create/DeleteBucketと指定設定のPut actionを個別評価する。CAPABILITY_IAMは権限付与ではない。resource事前確認の拒否は変更を残さない。version履歴が残るbucketのDeleteStackはAPIエラーとなり、先行削除もrollbackする。非同期AWS lifecycle/Eventsとの同等性は主張しない。

CFNのDependsOn文字列/配列をimplicit Ref/GetAtt/Subと統合して事前検証し、依存順で作成・逆順で削除する。inline Pythonはindex.pyのZIPへ変換し、native saveLambdaFunctionを同じtransactionで呼び出す。新しいresource typeのclaim namespaceは未対応旧readerのIAM fallbackに一致させず、未知kindはdefault処理で削除しない。旧binaryとのDB共有/downgradeはサポートしない。

Native IAMはAWS layerのrole/inline-policy tablesを所有し、信頼先とaggregate identityを同じSQLiteへ保存する。空roleはidentity policy rowを作らずimplicit Deny。旧JSON control APIのnative ownership検査と書込はAWS adapterの同一transaction内で行い、coreへAWS schemaを入れない。既存CFN roleはmetadata-only trustを保ちnativeへの暗黙移行をしない。[IAM](../iam.md)。

SDK bridgeはAWS layerの固定shimとrole-bound closureが所有し、runtimeにはAWS service/action、DB、tokenや問題IDを渡さない。guest stdoutのbounded JSON交換は権限の根拠ではなく、各native handlerでIAMを再評価する。S3は直接S3 handlerへdispatchし、bucket/keyがLambda protocol pathに似ていても別serviceへ解釈しない。

CloudFormationのSSM値パラメータは生成済みStack ARNと全source GetParameters権限を検査後、同一read transactionで同region/nameのString値を取得する。入力keyとresolved valueを別に保存し、Ref/条件には値を渡す。native create前にread transactionを閉じ、以降のsource更新・削除で保存snapshotを更新しない。matching ClientRequestToken再送は既存Stack認可後、sourceを再取得しない。[対応範囲](../cloudformation-ssm-parameters.md)。

CFNの[native IAM role/profile構成](../cloudformation-iam-profiles.md)はactive profile参照または新規zero-inline role形で選択する。従来の1-inline metadata-only roleと保存済legacy stackは暗黙移行しない。canonical external roleの認可・generation確認・作成は同一write transaction内で行う。保存したgeneration/fingerprintと最後のevent/history書込後のretirement・child不在を照合し、drift・外部依存・ignored writeで全体をrollbackする。profile CFN対応はEC2/SSM実行や元Battle対応を意味しない。
