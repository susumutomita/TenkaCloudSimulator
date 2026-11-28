# Go単一バイナリの設計

`cmd/simulator`はCLI、numeric loopback listener、token-file、shutdownを所有する。`internal/server`は埋込Cloudscape/Scalar、local token境界、能力表、identity control APIを提供する。`internal/provider`はprovider dispatch、`internal/core`はprovider中立のSQLite object stateと限定identity-policy評価。`internal/aws`はS3 REST/XML、SSM JSON 1.1、CloudFormation Query/XMLとAWS所有テーブルを持つ。

`core.Store` はインメモリ起動時に専用の SQLite データベースを所有する。要求の取り消しで接続が入れ替わっても、コミット済みのプロバイダーのテーブルと行を次の接続から参照できる。未コミットの書き込みはロールバックされ、メモリ上のデータベースはストアを閉じた時点で解放される。メモリとファイルのどちらでも、接続ごとに外部キー制約と 5 秒のロック待機時間を設定する。

Coreは問題ID/採点を知らない。SSM・stack/ownershipのschemaはAWS layerがcoreの共有DBを使用して初期化する。CFNはcompileで全リソースと参照を検証後、stack、ownership claim、parameter、identity、S3 bucket/config/tagsを単一transactionで作成。削除でも同一transaction。手動削除されたparameterのclaimを残し、他stackが再取得して旧stackのcleanupで消される事故を防ぐ。role/bucket namespaceはglobal、SSM/stackはregion別、Lambda claimはlambda:<region>。

Lambda定義/ZIP/region/role/environment/revisionはAWS layerのprovider-owned SQLite tableに保存する。native APIのIAM・PassRoleを評価した後、provider-neutralなinternal/workload/pythonへコード/event/contextと明示したbounded capability・固定module FSを渡す。固定CPython/WASI・選択stdlib・原文rightsを埋め込み、host filesystem/environment/process/socketは渡さない。guest memory/時間/出力量/同時実行を制限する。限定inline Lambda CFNを同じ保存処理へ接続する。関数内SDKは保存role/regionに固定したnative SSM/S3 subsetのみ。問題固有のhandlerや任意HTTP proxyはない。[制限](../lambda.md)。

`internal/inference`はprovider中立の有界テキスト生成を持つ。cloud protocol、IAM、問題ID、採点を知らない。AWS layerのBedrock Runtimeが`bedrock:InvokeModel`を評価し、Anthropic Messages/Converseの本文を厳密に検証してから、operatorが対応付けたbackend model名だけを渡す。Ollama adapterは設定されたnumeric loopback `host:port`だけへproxy・redirect・名前解決なしで接続し、同時実行・期限・入出力サイズを制限する。無効・未対応付け・停止中のbackendは明示エラーで、別backendや合成応答へ切り替えない。promptと応答はSQLite・logへ保存しない。Apple Foundation Modelsは任意の別ビルドSwift helper（Apple silicon/macOS 27+）をoperatorが絶対pathで指定した場合だけ使用する。標準Go buildはhelper/Swiftに依存しない。availability失敗やtoken上限で終了を確認できない応答は明示エラーとする。[範囲](../bedrock-runtime.md)。

Consoleは公式Cloudscape/Reactを開発時にbundleし、fonts/assets/licensesを埋め込む。独自模倣CSSではない。UI/SDK/Scalarはいずれも実HTTPを通り同一SQLite状態を使う。実行時Node/npm、CDN、クラウドproxy不要。UIは実AWS全機能と同一ではない。[比較](../console.md)。

SSM の [ドキュメント履歴](../ssm-documents.md) はリージョンとドキュメント名で検索し、1 回の SQLite 読み取りで番号順のスナップショットを返す。ページごとに IAM を評価し、続きは対象・呼出者・スナップショットに結び付く。一覧に定義本文は含めず、Console の本文比較には別の GetDocument 権限が必要。ドキュメントは実行しない。

SNS の HTTP/HTTPS コンソールは native Query を呼び出し、通知先 URL をローカル SQLite に記録するだけで外部に接続しない。購読属性を読む際は ARN から親トピックを特定して IAM を先に評価し、購読と親の存在およびリージョンの一致を同じ読み取りで確認する。専用 Scalar は us-east-1 固定で通常のログインセッションを使い、コンソールの演習用 ID 選択を引き継がない。[対応範囲](../sns.md)。

Service Quotas の `GetServiceQuota` は、B2B Data Interchange の `CreateProfile` がリージョン単位で強制している上限を同じ定義から読み取る。呼出元には対象リージョン・アカウントのクォータ ARN に対する `servicequotas:GetServiceQuota` が必要となる。[対応リージョンと API の範囲](../aws-catalog.md#b2b-data-interchange-profile-quota)

AWS カタログの AppFlow は S3 間のフロー設定のみを保持する。[トリガー設定](https://docs.aws.amazon.com/appflow/1.0/APIReference/API_TriggerConfig.html)に従い、`CreateFlow` と `UpdateFlow` は、`OnDemand` に付けたスケジュール設定と、実行間隔の式がない（空白のみも含む）`Scheduled` の定義を `ValidationException` で拒否する。検証は保存前に行うため、拒否された要求で既存の設定は変わらない。スケジューラ、コネクタープロファイル、ジョブ実行およびオブジェクト転送は実装しない。

AWS Batch のジョブキューは、先頭ジョブのタイムアウト設定のみを保存する。設定できる時間は 600～86,400 秒で、コンテナ系では `CANCEL`、SageMaker Training では `TERMINATE` を受け付ける。更新時は変更後のキュー種別とアクションの組み合わせを検証し、拒否された要求で既存設定は変わらない。タイマーやジョブの実行・終了処理は実装しない。
スケジューリングポリシーの削除時は、同じトランザクションで保存済みジョブキューからの参照を調べる。使用中ならキュー名は返さずに拒否し、保存状態を変えない。
FIFO キューに公平配分ポリシーを後から追加する更新は、現在の保存状態を同じトランザクションで確認して拒否する。ほかの更新項目も保存しない。
ジョブキューの削除は、認可時と同じ世代の保存状態が `DISABLED` であることを削除のトランザクションで確認する。有効なキューは `ClientException`（HTTP 400）で拒否し、設定や参照先は変更しない。
コンピューティング環境の削除時も、同じトランザクションで保存状態と同一リージョンの全キューの参照を確認する。環境を無効化し、キューから名前または ARN による参照をすべて外すまで、削除は `ClientException`（HTTP 400）で失敗する。
ジョブ定義は、EC2 の単一コンテナ向けにイメージ名（タグは任意）と整数の CPU・メモリだけを保存する。実行内容・認証情報・登録時のタグなどは受け付けない。登録は定義名の ARN で認可し、名前ごとの採番と保存を同じトランザクションで行う。無効化はリビジョン ARN 単位の認可と世代チェックで `INACTIVE` に変え、読み戻しと採番のため残す。リージョンごとに50リビジョン（無効化済みを含む）のローカル上限があり、自動削除はしない。イメージ取得・コンテナ・ジョブ実行は実装しない。
AWS カタログコンソールのジョブ定義では、名前、イメージ名、vCPU 数、メモリ (MiB) のみを登録画面に表示する。詳細表示・再取得・登録解除は保存済みの正確なリビジョン ARN を使い、登録解除後も INACTIVE として一覧に残す。SDK・API リファレンスと同じ Batch API を利用し、物理削除やジョブ実行は提供しない。

[Cognito User Pools](cognito-user-pools.md) は、検証済みのローカル SigV4 で既定ではプールの作成・参照・一覧・削除だけを扱う。保存先はチームの SQLite であり、サインイン、セッション、SAML、トークン発行は実装しない。

`--cognito-local-federation-config` を明示した認証付き数値ループバック環境では、[テスト用 SAML IdP と公開アプリクライアントの設定作成・参照](cognito-federation-configuration.md)も可能。署名・スコープ・IAM は既定操作と同じ境界で検証する。実 IdP との通信、SAML ログイン、トークン発行、コンソール、Scalar は提供しない。

SimpleDB の `PutAttributes` と `DeleteAttributes` は AWS SDK の標準形式の `Expected` 条件を 1 件だけ受け付ける。属性の単一値との比較、または属性が存在しないことの確認を更新と同じ SQLite トランザクションで行う。条件を満たさない場合は他の属性も変更しない。削除は、値を指定すると一致する値だけ、属性名だけならその属性、属性の指定がなければ項目全体が対象となる。最後の属性を削除すると項目も消える。無条件の削除は何度実行してもよい。番号付きの条件形式とバッチ操作には対応しない。[ネイティブ `Select`](../simpledb-select.md) は同じ SQLite の項目を読み取り、限定した述語とページ送りを提供する。コンソールの項目名検索とAPIリファレンスの読み取り例も同じ操作を使う。新しい値の組が追加されるときは同じ書き込みトランザクションで最大256組を検証し、超過時は他の属性も変更しない。[既存超過データの扱い](../simpledb-item-limits.md)。

[SimpleDB コンソール](../simpledb-console.md)は既存の認可とローカル Query API を使い、選択したドメインで既知の項目を読み取って、1属性だけを条件付きで置換する。書き込みの成功応答を受けてから属性を読み直す。単一値の属性については、表示された値との一致を条件に削除でき、最後の属性を削除すると項目も消える。項目名検索はドメイン内の全件または属性値の完全一致を25件ずつ取得する。カーソルは元の検索と認証スコープに対応し、各ページの権限を個別に判定する。ドメインの削除はコンソールでは扱わない。API Reference の `CreateDomain` は、同じ認可と既存の Query API でローカルの練習用ドメインを作る。消去は SDK の `DeleteDomain` を使う。

Redshift の `DescribeTags` は既存のローカル ARN 1件に限り、同じ認可でタグのキーと値を読む。全体検索や SQL は扱わない。

別サブコマンド `local-sso` は、同じ Go バイナリの中で使い捨ての Entra SAML IdP、
Cognito 受信側とサンプルアプリを別々の数値ループバック HTTPS オリジンで動かす。
SQLite、テスト用 ID、署名鍵、ブラウザー用 CA は各実行で生成する。
管理操作は既存の署名済み SDK 境界を通し、準備専用の管理リスナーは準備完了を表示する前に閉じる。
Cognito の SAML POST は同じオリジンで完了し、検証済みアプリのコールバックへ別のナビゲーションで進む。
サンプルの成功は通常の Console の認証、実 AWS・Microsoft のアカウント、Azure OAuth/Graph/ARM を有効にしない。
Entra の SAML コンポーネントは Cognito に依存しないが、別 SP は明示的に登録する必要がある。
[起動手順と制約](../local-sso.md)。

[ローカル Entra OIDC 検証フィクスチャ](../azure-entra-verification-fixture.md)は、明示的な別コマンドで数値ループバックにだけ待ち受ける。通常のゲートウェイや Azure の権限は変更せず、検証用メタデータと一時公開鍵を返す。テスト用トークンは明示指定した起動出力に限り、実テナント、ログイン、SAML、HTTP発行には対応しない。

Azure Queue は Blob と独立したプロバイダールートを持ち、固定 account・owner・environment に結びつくローカル SQLite にメッセージを保存する。受信と更新は表示予約と pop receipt を更新し、削除は現在の receipt を要求する。Console と API リファレンスは同じ所有者境界とデータプレーンを使う。アカウント全体のキュー一覧や Azure RBAC は模倣しない。[操作範囲と検証条件](../acceptance/azure-queue.md)。

CLI token authorityはoperatorと15分participantを区別し、participant identity header上書きを拒否する。主催ホストはチームごとに別プロセス/DB/tokenを管理し、ログイン済み利用者とチームを認可する。単一プロセス内のidentity選択でチーム分離しない。library compositionのserver.Newは認証を自動追加しない。

SQLiteは既存のncruces SQLite/Wasm engineをGo process内で実行する。外部SQLite実行ファイルやWasm runtimeサービスは不要。有限quotaと接続数制限を維持し、実AWSの整合性/リージョン/アカウント/クォータ全体を再現しない。

[Route 53のPTRレコード](../route53.md)は、公開ゾーンのSQLiteに保存した名前をTestDNSAnswerで返す。外部DNSへの問い合わせや実IPアドレスの逆引き設定は行わない。認証済みの通常コンソールでは既存公開ゾーンとレコードを参照し、1件のPTRを選択ゾーンの同じネイティブ認可で作成できる。INSYNC受領証と同じ名前・種類・TTL・値の再読で確認する。ゾーンの変更、上書き、削除、限定ハンドオフは[この画面の対象外](../route53.md#console-browse-public-zones-and-create-one-ptr)。

VPC コンソールは「VPC のみ」を既定にし、既存のネイティブ CreateVpc を使う。「VPC など」は、認証済みのシミュレーター管理者だけが CloudFormation に新規 VPC とサブネット 1 つの固定テンプレートを送る。参加者ブラウザーにはスタック出力が開示されないため、この選択肢を表示しない。新規スタックが両方を所有し、作成受領証の正確なスタック ID で再取得できたときだけ成功を表示する。ネイティブ EC2 所有 VPC への追加や、実ネットワークの作成は行わない。[対応範囲](../ec2-read-discovery.md#console-vpc-and-one-subnet)。

VPC の左ナビゲーションの [サブネット](../ec2-native-subnet.md) は、
既存の DescribeVpcs と DescribeSubnets の範囲を照合し、同一リージョンの保存済み行だけを表示する。
単独の作成ページは EC2 管理 VPC のみ選択可能で、保存済みサブネットを確認して詳細に進む。
ルートは実 API の明示的な関連付けまたは確認済みのネイティブメインテーブルのみ表示する。
サブネット名・タグ・ACL の関連付けは新設しない。

[VPC の DNS 設定](../ec2-vpc-dns-attributes.md)は、EC2 Query で DNS 解決と DNS ホスト名の設定値を1項目ずつ取得・更新する。対象の VPC ARN と操作ごとに認可する。CloudFormation 管理の VPC は両方の所有権記録を照合した上で参照のみ可能。EC2 管理の VPC は同じ行に永続化し、同じ値を再送しても行のリビジョンを増やさない。コンソールは同じ API を使い、項目を順に保存・再取得し、後の項目だけ失敗した場合は途中結果を表示する。DNS の名前解決やホスト名の配布は行わない。

[EC2 管理 VPC の名前とタグ](../ec2-read-discovery.md#native-vpc-tag-editing)は、`CreateTags` / `DeleteTags` で一つの VPC に一つずつ設定する。操作と VPC ARN で認可し、SQLite トランザクション内でネイティブ所有権・CloudFormation の宣言・既存行の正規化済みデータを照合する。元の行と完全一致したときだけタグを更新し、保存結果を読み直す。DNS や経路のリビジョンは進めない。設定は既存の `DescribeVpcs` に現れる。CloudFormation 管理 VPC、VPC 以外のリソース、全タグ一括削除は対象外。停止中コピーのファイル指紋に紐づく旧メインルート修復の計画は、タグ変更後のファイルには再利用できない。

選択した VPC の[リソースマップ](../ec2-vpc-console.md#read-only-resource-map)は、通常のサインイン済みコンソールで、選択リージョン・利用中の演習 ID に紐づく既存の Describe API を使う。サブネット、ルートテーブル、インターネットゲートウェイの一覧は独立に取得し、まとめて同時点の状態を保証しない。サブネットの明示的な関連付けが優先される。暗黙の関連付けは、整合したサブネットとルートテーブルの一覧に、選択した EC2 管理 VPC のメイン関連付けと CIDR が一致する `local` ルートがある場合だけ表示する。ゲートウェイへのルートは、同じ VPC への接続と所有元を確認できた保存済みの宛先だけを表示する。

権限不足や整合性の確認に失敗した一覧を空として扱わない。取得できた関係は確認できない関係と区別し、選択・リージョンの変更やタブを閉じた後は古い応答を表示しない。DNS などの変更結果を再取得している間はタブの切り替えを止める。[形式モデル](../../formal/EC2VpcResourceMap.tla)が扱うのは、読み取り結果を正規化した後の有限の関係と表示の分類まで。API の実装やブラウザーの実行順序の証明ではない。通信の転送、到達性の判定、EC2 インスタンスの起動は行わない。

API Reference の EC2 用プリセットは、既存の Query API の `DescribeInstances` を呼び、選択リージョンに記録されたメタデータを表示する。起動や稼働確認は行わない。

ネイティブ EC2 の [DescribeSecurityGroups](../ec2-read-discovery.md#security-group-readback) は、既存の EC2 IAM 判定とリージョンの範囲内で CloudFormation 管理グループと未接続のネイティブグループを読む。グループと親 VPC の型付き所有権を同じ SQLite スナップショットで照合し、テンプレートの初期値ではなく今の TCP/IPv4 受信ルールを返す。XML 1.0 で値をそのまま返せない行や壊れた所有権があれば一覧全体を拒否し、保存内容は変更しない。通常のサインイン済み VPC コンソールも同じ API と IAM を使い、選択した CloudFormation 管理の VPC に属するグループだけを表示する。権限不足、読み取り失敗、空の結果は区別する。セキュリティグループの変更やパケット処理は対象外。

[DescribeRouteTables](../ec2-read-discovery.md#route-table-readback) は、CloudFormation 管理とネイティブ EC2 管理のテーブルを同じリージョンの SQLite から返す。新規の EC2 管理 VPC にはメインルートテーブルを同じトランザクションで作成し、その VPC 内の各テーブルに書き換えできない IPv4 `local` を記録する。メインの関連付けにサブネット ID は付かず、通常の Console は現在のサブネットと全テーブルを読んで暗黙にメインを利用するサブネットを区別する。既存 VPC は自動移行せず、CloudFormation の設定と所有権も変えない。

[旧ネイティブ VPC のオフライン修復](../ec2-legacy-native-main-repair.md)は、停止した SQLite の別コピーに対するローカルファイル所有者専用の CLI。読み取り専用の `plan` と、元のバイト列・対象リージョン・VPC ID に紐づく `apply` を分ける。排他トランザクションで親と既存テーブルの世代を更新し、1行のメインテーブルを追加する。既存の関連付け、静的ルート、リプレイ記録、IAM、CloudFormation や他の VPC は変更しない。起動、API、Console に修復の書き込み経路は追加しない。

明示的なサブネット関連付けの検索（`association.subnet-id`）は、リージョン全体の整合性検査と IAM 判定の後に行い、暗黙の利用は結果に含めない。ネイティブのテーブルとサブネットは、同じ VPC 内で [明示的に関連付け・変更・解除](../ec2-read-discovery.md#native-subnet-route-table-associations)できる。変更時は変更先テーブルの IAM 許可も確認し、同じトランザクションで以前の関連付け ID を廃止する。`MaxResults=5..100` は同じリージョン、利用者、条件、読み取った状態に結び付け、表示と操作完了は全ページの確認後に行う。単一表の `RouteTableId.1` とページ指定は併用しない。メインは親 VPC の削除時にのみ削除し、他の依存関係が残る場合は拒否する。実通信は扱わない。

[インターネットゲートウェイ](../ec2-internet-gateways.md)は、未接続で作成し、EC2 管理の VPC と 1 対 1 で接続・切断できる。両端点の IAM 許可、同一リージョンと所有権、依存関係、保存後の状態を確認し、接続中はゲートウェイも VPC も削除できない。CloudFormation 側は同じ SQLite スナップショットで型付き所有権と保存済みネットワークを照合し、表示だけ行う。Console は最大100件ずつの一覧を取り直し、接続・切断では対象 ID の個別確認も行う。途中結果や古い応答から成功を表示しない。経路やパブリック IP の設定、ゲスト起動、実通信は扱わない。

[ネイティブ IPv4 ルート](../ec2-native-default-route.md)は、同じネイティブ VPC に属するルートテーブルと接続済みゲートウェイに向けて、テーブルごとに最大 16 個の正規化した CIDR を保存できる。デフォルト以外は VPC 内や予約範囲と重複できない。書き込みと同一 SQLite トランザクションで所有権・依存関係・保存結果を照合し、ルートが残る間は表の削除やゲートウェイの切断を拒否する。Console は書き込み後に対象の表を個別に再照会し、表示中のリージョンと利用者の応答で、他の宛先を含めた全ルートの一致を確認してから成功を表示する。経路設定の保存のみで、通信の転送や到達性は扱わない。

[既存 VPC のネイティブ IPv4 サブネット](../ec2-native-subnet.md)は、EC2 管理の親 VPC に対して追加・参照・削除できる。Console と SDK は同じ EC2 API と SQLite を使い、CloudFormation の所有権を変更しない。ルーティングやゲストの到達性は扱わない。
[保存済みの EC2 管理サブネットの名前とタグ](../ec2-native-subnet.md#native-subnet-tag-editing)も、一つの ID と一つのタグを指定する。VPC タグと同じ Query 処理を使い、対象をサブネット ARN で認可する。サブネットと親 VPC の所有権・CloudFormation の宣言・正規化済みドキュメントを同じ SQLite トランザクション内で検証する。更新対象は既存のサブネット行のタグだけで、サブネットのリビジョン、パブリック IPv4 設定、親 VPC の DNS と経路を変更しない。エラー時は途中の変更を確定せず、既存の `DescribeSubnets` から保存済みのタグを取得する。作成時のタグ、スタック管理のサブネット、パケットの挙動は対象外。

[ネイティブのセキュリティグループとネットワーク ACL](../ec2-read-discovery.md)は、同じ VPC に保存できる未接続の設定である。Console と SDK は同じ Query API で保存・再照会し、インスタンス・サブネットへの関連付けや通信の制御は扱わない。子が残る VPC の削除を拒否する。

[未接続ネットワーク ACL の IPv4 ルール](../ec2-detached-network-acls.md)は、作成・置換・削除ごとに対象 ACL の ARN で認可し、同じリージョン内で ACL と親 VPC の所有権・CloudFormation の宣言・保存後の状態を照合する。ルールは ACL の既存 SQLite 行に保存し、ACL の削除とともに消える。方向ごとに 1〜32766 の番号で最大 20 件。最終の拒否ルールは変更できない。確認には同じ ACL の `DescribeNetworkAcls` を使う。既定 ACL、サブネットへの関連付け、パケットの許可・拒否は扱わない。


[VPC コンソールの個別ページ](../ec2-vpc-console.md)は、リージョン付きの VPC・サブネット・ルートテーブル・インターネットゲートウェイ・ネットワーク ACL・セキュリティグループの閲覧経路を提供する。ページの表示と操作は対応する API の通知、認証とセッション範囲、同じリージョンの再照会に基づく。リンクは API の認可を増やさない。応答を待つ間は画面内の移動を止め、ブラウザー履歴で離脱した場合は現在の画面用の照会を破棄する。既存の VPC からも個別設定を閲覧できる。実通信・インスタンスの配置要件は変えない。

[EC2 コンソールのテンプレートプレビュー](../ec2-read-discovery.md#instance-launch-preview-is-not-a-launch)は、現在の演習 ID とリージョンで既存の `ValidateTemplate` のみに接続する。パラメータ名と IAM の確認項目を返すが、既定値の内容やリソースは表示・保存しない。AMI の有無、起動権限、ゲストの実行可否は検証せず、インスタンスは起動しない。

EC2 インスタンス一覧の作成は、テンプレートプレビューと別の操作。認証済みループバックの管理者が管理者 ID を選択し、明示的な Battle VM 実行環境が作成を受け付ける場合だけ利用できる。同一スタックに受信ルールのないネットワークと IAM ロール／プロファイルを含む固定構成を生成し、設定済み AMI エイリアスで既存の `CreateStack` に送る。権限評価や実行経路は変えない。応答した Stack ID の `CREATE_COMPLETE` と対応するリージョンの EC2 保存行を照合し、未確認時に作成を再送しない。保存済みの状態はゲストの現在の稼働確認ではない。

Protocol/API subsetと制限は[README](../../README.md)。限定的なGoogle Cloud Storage/Azure Blob Storageを独立providerとして接続し、認証済みActorから固定project/account所有者へ写像する。[Cloud DNS](../acceptance/gcp-cloud-dns.md) は同じproject所有者の範囲でSRVを含むRRsetの変更と参照を提供する。所有者コンソールではSRVの追加・置換・削除を変更受領証と再読で確認する。SRVの優先度・重み・ポートは保存するが、名前解決や接続先の選択は行わない。AWS IAMの権限を継承せず、owner-onlyローカル認可と公式SDKの検証範囲は[マルチクラウドストレージ](../multicloud-storage.md)に記す。旧Python world/deploy/grading protocolは復活させず、必要なSSM/CFNの知識だけGoへ移行。問題取り込み・採点はTenkaCloud/Challenge責務とする。

Azure Blobの通常APIは `2023-11-03` に固定する。BlobのGET/HEADでは単一の `If-Modified-Since` を扱い、通常のGETには有界の単一範囲CRC64がある。明示的にネゴシエートした `2025-01-05` のGet Blobだけで、最大4 MiBの単一セグメントを構造化CRC64として返す。共通のローカル所有者・読み取り条件を適用し、通常の範囲チェックサムヘッダーと併用しない。それ以外の同バージョン操作やSDK全体の互換性は対象外。[Azure Blobの条件付き読み取りとチェックサムの詳細](../azure-blob.md#single-range-download-subset)。

CloudFormationのRef/GetAtt/Subはresource dependency graphを先にtopological sortし、forward referenceとcycleを識別する。リソースの論理名の辞書順に依存させない。DeleteStackはimmediate transactionを取得してからstack documentとownershipを読み、同じtransactionで削除する。遅延した旧stack IDの削除が、再利用された物理名の別stackを壊さない。

CloudFormation の EC2 インスタンスは、同じスタックで宣言・所有する一つの VPC のサブネットとセキュリティグループにだけ配置できる。作成トランザクションの確定前に宣言、保存行、所有権を照合し、不整合はスタックもイベントも残さず拒否する。実際の VM の起動・準備完了の確認は従来どおりコミット後に行う。

任意の Battle VM 実行環境で、既存の capability resolve はチームの正規 DB に保存された `CREATE_COMPLETE` の Stack ID と EC2 所有権を、起動時の保持済みレシート・ready 記録・lease と照合する。起動コールバックは CloudFormation の確定を待たない。先に到達した resolve は 409 になり、後から再試行できる。seal と既存の NIC 認可は変わらない。この照合は新しい通信経路や RDS の動作を保証しない。

`--rds-private-postgres-topology` は認証付きの数値ループバックでだけ使える任意の設定検証。`Engine=postgres`、単独 DB インスタンス、明示的な `PubliclyAccessible=false` の `CreateDBInstance` / `ModifyDBInstance` を対象にする。通常の設定モードは変えない。既存の `rds` IAM 判定後、書き込みと同じ SQLite トランザクションで DB サブネットグループと現行のネイティブ EC2 所有行を照合する。初期の範囲は、Internet Gateway への接続や有効な外向き経路がない単一の IPv4 VPC、その VPC と同じ所有者に属する2つ以上の AZ のサブネット、および1〜5個の固有のセキュリティグループ。未知・別所有者・別 VPC の参照は書き込まない。これはその時点の設定検証であり、継続的な接続証明、SQL、バックエンド、ポート、エンドポイントの追加ではない。応答は引き続き `creating`。

RDS エンジンの DB インスタンスが返すパラメータグループ名とオプショングループ名は、保存済みの設定を示す。`CreateDBInstance`、`DescribeDBInstances`、`ModifyDBInstance`、`DeleteDBInstance` の応答では、実際に適用されたと誤認させないよう、任意項目の [`ParameterApplyStatus`](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_DBParameterGroupStatus.html) と [オプショングループの `Status`](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_OptionGroupMembership.html) を返さない。旧 SQLite 行でも参照時は状態だけを省略し、保存行を書き換えない。認可済みの変更が確定する際は古い適用状態を取り除くが、指定していない設定やタグは保持する。DB インスタンスの `creating`（削除応答は `deleting`）、エンドポイントなし、セキュリティグループの状態、および共有する Aurora・Neptune・DocumentDB の応答は従来どおり。

Linux/amd64 の非 root 運用者は、認証付き数値ループバックで [`--rds-owned-postgres-config`](rds-owned-workload.md) を個別に指定できる。既存の設定検証と、承認済み PostgreSQL バンドル・独立したピン・専用作業ディレクトリ・永続 SQLite を必須とし、Battle との併用は拒否する。起動時は構成を受け入れるだけで、既存の IAM とトポロジー検証を通り、所有権を確定した指定 DB の新規作成だけが内部実行の対象になる。API の応答形式は変えず、接続先・認証情報・稼働の保証やゲスト接続は提供しない。

CloudFormationのIAM判定は要求開始時のpolicy snapshotを使い、Createは生成対象ID（冪等retryは既存ID）、named Describe/Deleteは実際のStack ARNを評価する。DeleteのID解決と認可は同じwrite transaction内。全stackのDescribeにはglobal許可に加え各ARNの明示Denyも評価し、拒否時に部分Outputsを返さない。

IAMはglobal namespaceなので、自動Role名はStack固有IDとlogical IDから接尾辞を生成し64文字に収める。同名Stackの別リージョン作成を衝突させない。明示RoleNameはその名前を保持し、CAPABILITY_NAMED_IAMを必須とする。既存の保存済みphysical IDは読み出した値を使い、再計算・改名しない。[AWS IAM Role仕様](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-iam-role.html)、[CloudFormation action/resource対応](https://docs.aws.amazon.com/service-authorization/latest/reference/list_cloudformation.html)を参照。

S3 version history/public-access-blockはAWS layerのaws_s3_versions/aws_s3_configに保存する。current-object viewは既存coreのobjectsと共有し、history/current/null置換・marker除去を単一transactionで更新する。GET時の旧状態importもtransaction内。config XMLの読込/検証は共有DB transactionより前で行い、遅い本文送信で他操作の接続を塞がない。bucket削除はcurrentだけでなく全history/markersを確認。provider-neutral coreへS3 versioningの分岐を持ち込まない。[保存・IAM・quota詳細](../s3.md)。

S3 REST/XMLは既存のIAM判定後、通常の`PutObject`と`CreateMultipartUpload`だけで、単一の`x-amz-storage-class: STANDARD`を省略と同じ扱いにする。既存の保存形式・応答・トランザクションは変えない。他のstorage class、`RestoreObject`、対象外の操作での当該ヘッダーは引き続き`NotImplemented`。

`GetObject`と`HeadObject`は既存のIAM判定とバージョン解決の後、`If-Match`の送信側文法に沿った空要素のないリストを検証し、選択した版に強いETagが1つでも一致すると条件を満たす。`*`の単独指定は従来どおり。不正な書式や一致なしは`412 PreconditionFailed`。版の参照処理、他の条件の優先順位、書き込みは変えない。

CloudFormationはservice RoleArnを受け付けず、呼出者のimmutable policy snapshotでStack ARNに加え全resource actionを事前確認する。SSM Put/DeleteParameter、IAM Create/DeleteRole・inline policy・指定managed policyのattach/detach・作成時TagRole、S3 Create/DeleteBucketと指定設定のPut actionを個別評価する。CAPABILITY_IAMは権限付与ではない。resource事前確認の拒否は変更を残さない。version履歴が残るbucketのDeleteStackはAPIエラーとなり、先行削除もrollbackする。非同期AWS lifecycle/Eventsとの同等性は主張しない。

EC2管理の既存IPv4サブネットは、正確なサブネットARNの `ec2:ModifySubnetAttribute` 権限で `MapPublicIpOnLaunch.Value` の真偽だけを保存できる。既存の型付きネットワーク行と親VPCの所有権を同じSQLiteトランザクションで確認し、CloudFormation側は変更しない。Consoleは同一ビューの `DescribeSubnets` で対象と値を検証してから成功を通知する。アドレス割り当て、実通信、ゲスト接続は含まない。

CFNのDependsOn文字列/配列をimplicit Ref/GetAtt/Subと統合して事前検証し、依存順で作成・逆順で削除する。inline Pythonはindex.pyのZIPへ変換し、native saveLambdaFunctionを同じtransactionで呼び出す。新しいresource typeのclaim namespaceは未対応旧readerのIAM fallbackに一致させず、未知kindはdefault処理で削除しない。旧binaryとのDB共有/downgradeはサポートしない。

Native IAMはAWS layerのrole/inline-policy tablesを所有し、信頼先とaggregate identityを同じSQLiteへ保存する。空roleはidentity policy rowを作らずimplicit Deny。旧JSON control APIのnative ownership検査と書込はAWS adapterの同一transaction内で行い、coreへAWS schemaを入れない。既存CFN roleはmetadata-only trustを保ちnativeへの暗黙移行をしない。[IAM](../iam.md)。

SDK bridgeはAWS layerの固定shimとrole-bound closureが所有し、runtimeにはAWS service/action、DB、tokenや問題IDを渡さない。guest stdoutのbounded JSON交換は権限の根拠ではなく、各native handlerでIAMを再評価する。S3は直接S3 handlerへdispatchし、bucket/keyがLambda protocol pathに似ていても別serviceへ解釈しない。

CloudFormationのSSM値パラメータは生成済みStack ARNと全source GetParameters権限を検査後、同一read transactionで同region/nameのString値を取得する。入力keyとresolved valueを別に保存し、Ref/条件には値を渡す。native create前にread transactionを閉じ、以降のsource更新・削除で保存snapshotを更新しない。matching ClientRequestToken再送は既存Stack認可後、sourceを再取得しない。[対応範囲](../cloudformation-ssm-parameters.md)。

CFNの[native IAM role/profile構成](../cloudformation-iam-profiles.md)はactive profile参照または新規zero-inline role形で選択する。従来の1-inline metadata-only roleと保存済legacy stackは暗黙移行しない。canonical external roleの認可・generation確認・作成は同一write transaction内で行う。保存したgeneration/fingerprintと最後のevent/history書込後のretirement・child不在を照合し、drift・外部依存・ignored writeで全体をrollbackする。profile CFN対応はEC2/SSM実行や元Battle対応を意味しない。


### Detached native EC2 security group rule configuration

Native security group TCP/IPv4 rules are persisted by direction and exact tuple
under the canonical region/group owner. A database foreign key removes rules
with their group. SDK and Console share EC2 Query `DescribeSecurityGroups`
readback. This separate detached state never modifies CloudFormation-owned
ingress or feeds live network/instance attachment decisions. See the
[security group subset](../ec2-read-discovery.md#security-group-readback).
