# TenkaCloud Simulator

非公式のクラウド学習用シミュレーター。AWSを中心に開発し、Google Cloud Storage・Azure Blob Storageの限定的なローカル接続候補を追加しています（[範囲と検証状況](docs/multicloud-storage.md)）。Go単一バイナリにCloudscapeコンソール、Scalar API reference、SQLite、Python Lambda用の固定CPython/WASIを埋め込みます。実行時にGo・Node・ホストPython・Docker・外部DB・実AWSは不要です。実AWSの資格情報は入力しないでください。

## 起動する

ソースからの起動にはGo 1.26が必要です（CIは1.26.10）。

```sh
make start
```

手動でビルド・起動する場合は次と同じ保存先を指定します。既にビルド済みのバイナリを直接実行する場合、実行時のGoは不要です。

```sh
make build
./dist/tenkacloud-simulator --listen=127.0.0.1:4566 \
  --state=./practice.db --token-file=./simulator-token
```

<http://127.0.0.1:4566/_simulator/console/> を開き、`simulator-token`に保存されたoperator tokenでサインインします。新しいtokenファイルは0600で作成され、既存のprivate regular fileは同じ値を再利用します。operator tokenは参加者へ渡さず、Gitやログに保存しないでください。Ctrl+Cで終了し、再起動すると同じSQLiteとtokenを使用します。CLIで`--state`を省略した場合は毎回空のメモリーDBです。

`make start`は現在のソースを毎回ビルドし、`./practice.db`と`./simulator-token`を再利用する起動コマンドです。Goがない・buildが失敗した場合、古いbinaryを起動しません。tokenを表示するのは実stdout端末かつ`CI`変数が存在しない場合のみで、空の`CI`やリダイレクトも表示を抑止します。端末のscrollbackや画面共有にも注意してください。Account→Sign outは、そのbrowser sessionだけをサーバー側で失効させます。[起動設定・既存バイナリの実行](docs/start.md)、[セッションの詳細](docs/console-sessions.md)。

## 使えるサービス

対応するコンソール操作とSDK/APIは同じSQLite状態を使用します。画面だけの模擬成功ではなく、実装済みのHTTP APIで状態を操作します。操作ごとのUI有無とAPI範囲は各資料を確認してください。

| サービス | 実装済みの操作・状態 | 主な未対応 |
| --- | --- | --- |
| [S3](docs/s3.md) | bucket作成・一覧・存在確認・region取得・削除、object PUT/GET/HEAD/DELETE・ListObjectsV2、bucket versioning・ListObjectVersions、public access block・tags | multipart、ACL、一般bucket policy、暗号化設定。path-styleのみ |
| [SQS](docs/sqs.md) | Standard queue JSON 1.0 API 9操作：CreateQueue、GetQueueUrl、ListQueues、GetQueueAttributes、DeleteQueue、SendMessage、ReceiveMessage、DeleteMessage、ChangeMessageVisibility | UI、CloudFormation、function SDK bridge、Challenge連携、FIFO、batch、DLQ、SNS連携 |
| [KMS](docs/kms.md) | CreateKey、DescribeKey、ListKeys、EnableKey、DisableKey、GetKeyPolicy、Encrypt、Decrypt。対称鍵・実ローカルAES-GCM、同一SQLiteのSDK/Cloudscape | alias、tags、grants、custom policy、rotation、scheduled deletion、CFN・他サービス暗号化。raw keyはSQLite保存でHSM/保存時暗号化保証ではない |
| [Systems Manager Parameter Store](docs/console.md) | PutParameter、GetParameter、GetParameters、GetParametersByPath、DescribeParameters、DeleteParameter。最新値・version・overwrite判定 | SecureString/KMS、履歴・labels。StandardのString/StringList/textのみ |
| [DynamoDB](docs/dynamodb.md) | CreateTable/ListTables/DescribeTable/DeleteTable、PutItem/GetItem/DeleteItem | Native JSON 1.0 API only; S/N/B keys, optional sort key; only partition-key attribute_(not_)exists conditions. No Query/Scan/indexes/Streams/console/CFN/Challenge integration |
| [IAM](docs/iam.md) | Create/Get/List/DeleteRole、Put/Get/Delete/ListRolePolicy、UpdateAssumeRolePolicy。Lambda service trust、inline policy評価 | STS、managed policyの権限付与、一般Condition・permissions boundary・SCP |
| [Lambda](docs/lambda.md) | CreateFunction、ListFunctions、GetFunctionConfiguration、UpdateFunctionCode、Invoke、DeleteFunction、AddPermission/GetPolicy/RemovePermission。固定CPython 3.12.16/WASIによるPython ZIP実行、限定S3/SSM SDK bridge | 他言語、非同期実行、Function URL、任意network。128 MiB・timeout 1–30秒 |
| [CloudFormation](docs/cloudformation-rest.md) | CreateStack、DescribeStacks、DeleteStack。S3 Bucket、SSM Parameter、inline Python Lambda、限定IAM Role、API Gateway 5 resource types・Lambda Permission、限定Ref/Sub/GetAtt/DependsOn | UpdateStack、一般resource types、実AWS相当の非同期lifecycle、CFN専用service role |
| [API Gateway](docs/apigateway.md) | Regional REST管理16操作。API/resource/method/integration、immutable deployment、新規stage、ローカルLambda proxy実行 | HTTP/WebSocket API、authorizer、API key、既存stage編集、public endpoint |

対応regionは`us-east-1`、`us-west-2`、`ap-northeast-1`、`eu-west-1`。S3 objectは8 MiB、保持version合計64 MiB、SSM値4096 UTF-8 bytes、CFN TemplateBody51200 bytesなどの上限があります。全フィールド・エラー・quotaの範囲は各サービス資料を確認してください。 1プロセスの上限は1000 buckets、10000 objects/versions/delete markers、1000 parameters/identities、100 stacks、100 resources/stackです。これらはプロセスメモリ全体の上限ではありません。

S3のCloudFormationはBucketName/Versioning/PublicAccessBlock/ユーザーTagsとRef/Arnに対応し、空でない履歴は削除しません。呼出者にはStack ARNと各リソース操作の両方の権限が必要で、CAPABILITY_IAMは権限を付与しません。

SQSはnative JSON 1.0 APIのみ対応します。保持中・期限切れ未回収を含む物理message rowはプロセス全体10000件、UTF-8 bodyとserialized attribute JSONの合計64 MiBまでです。これはSQLiteファイル全体・プロセスメモリ・AWS課金サイズの上限ではありません。この開発候補には下記の限定GCP Storage・Azure Blob実装がありますが、両クラウドの他サービスとSakuraは未対応です。Issueや計画は実装完了を意味しません。[開発状況](docs/development-status.md)を参照してください。Cloudscapeを使用していますが、本物のAWSコンソールとの全画面・全挙動の一致や実AWSとの差分検証完了は主張していません。

## Google Cloud Storage・Azure Blob Storageのローカル接続候補

ServicesからGoogle Cloud · Cloud Storage／Azure · Blob Storageを開きます。固定のsynthetic project/accountで、作成→upload→実ファイルdownload→削除を操作できます。2 CLI・別DB/tokenとPC/390pxの実操作は検証済みですが、公式SDK・全体CI・実クラウド画面との一致は未検証です。[対応・制限](docs/multicloud-storage.md)。

既定所有者はsimulator-adminです。参加者に使わせる新規環境は、ホストが`--cloud-storage-owner=learner`を指定し、learnerへ固定したparticipant tokenを発行します。この所有者設定はGoogle IAMやAzure RBACではなく、AWSの権限も付与しません。同じDBの所有者変更は拒否します。CLIフラグは実CLIの再起動・所有者変更拒否・AWS権限非付与までGo/raceで検証しています。

## IAMロールからLambdaを作る

Services→IAM→Roles→Create roleで信頼先Lambda、権限なし、名前と確認へ進みます。RoleのPermissionsに必要なinline policyを追加し、Lambda→Create function→Permissions→Existing roleで選択します。CodeでZIPをDeployし、Testで実Pythonを実行します。権限なしはimplicit Denyです。未実装のmanaged policyを選んだ扱いで権限を与えません。

IAMはAction/Resourceの限定wildcardと明示Deny優先を評価します。一般のCondition/Principal/NotAction/NotResource、resource policies、permissions boundary・SCPは未対応です。限定Lambda resource policyはAPI GatewayのローカルSourceArn付きInvokeのみ対応します。既存CFN roleやLocal identitiesはnative IAM roleとは別の保存・信頼モデルで、相互の名前採用を拒否します。カタログのSignInLocalDevelopmentAccess/AWSLambdaBasicExecutionRole ARNは記録のみで権限を付与しません。[認可の詳細](docs/iam.md)。

## API referenceとSDK

サインイン後、`/_simulator/docs/`を開きます。記載されたexampleは静的な説明であり、現在の保存状態ではありません。**Test Request→Send**で実際に問い合わせ、status・headers・bodyを確認してください。S3はXML/bytes、SSMはJSON 1.1、SQSはroot POSTと`X-Amz-Target: AmazonSQS.<operation>`のJSON 1.0、IAM/CloudFormationはQuery/XML等のnative形式を保ちます。対応するlive S3 metadataの応答は「Read fields」で読み解けます。object bodyは対象外です。

```sh
# operator用。値を直接コマンド履歴に書かず、出力・共有しない。
SIM_TOKEN=$(cat simulator-token)
curl -H "X-Simulator-Token: $SIM_TOKEN" -X PUT http://127.0.0.1:4566/practice-bucket
curl -H "X-Simulator-Token: $SIM_TOKEN" http://127.0.0.1:4566/
unset SIM_TOKEN
```

SDKには明示endpoint、region、ダミー`TCSIM…`キーを設定し、request hookで`X-Simulator-Token`を付けます。ダミー署名はwire routing用で、認証ではありません。実AWS形式キー・AWS session token・presigned URLは拒否します。[接続例](test/platform/README.md)、[実SDKテスト](test/e2e/check.py)。

## チームを分離して問題を解く

1チーム/環境につき、固有のプロセス・SQLite・operator tokenを割り当てます。operatorは`POST /_simulator/v1/access`で固定identityに結び付いた15分participant tokenを発行します。participantは別identityやadminへ切り替えられません。認可は各IAM actionでも評価します。localhostへbindするだけではtenant隔離やユーザー認証は成立しません。

ユーザー認証、チーム割当、proxy認可はホスト側の責務です。browser sessionはSDK tokenとは別のcredentialを使用し、15分以内かつparticipantの期限を超えず、logoutは他browser・SDK tokenを失効させません。ホストproxyが別sessionを持つ場合は、ホスト自身でlogoutを処理する必要があります。participant/browser sessionは再起動で失効します。privateファイルを再利用するoperator tokenとは異なります。同一OS利用者からのファイルアクセスを隔離するものではありません。

[問題対応表](docs/catalog.md)では、hello-worldの取り込み→別チーム起動→SSM取得→実ホスト採点→削除を開発用接続で検証しています。全問題対応やTenkaCloud標準起動への自動統合を意味しません。未対応の問題を選択しないでください。

既定はnumeric loopbackです。`--allow-remote`は信頼された認証proxy/隔離ネットワーク向けの明示指定で、TLS・認証・tenant隔離を追加しません。`--unsafe-no-auth`はloopbackの開発専用です。[セキュリティ境界](SECURITY.md)。

## 開発と検証

```sh
make before-commit       # format/vet、asset/module整合、race、statement90%、build
# source-line90%もCIと同様に確認する（上のcoverage.out生成後）
go run github.com/boumenot/gocover-cobertura@v1.4.0 < reports/coverage.out > reports/coverage.xml
python3 scripts/check-coverage.py reports/coverage.out reports/coverage.xml
make cross-check         # 5 OS/architectureのcompile
python3 -m venv /tmp/tcsim-tests
/tmp/tcsim-tests/bin/pip install -r scripts/requirements-test.txt
/tmp/tcsim-tests/bin/playwright install chromium
/tmp/tcsim-tests/bin/python scripts/get-audit-tools.py /tmp/tcsim-audit
AXE_SCRIPT=/tmp/tcsim-audit/axe.min.js make e2e PYTHON=/tmp/tcsim-tests/bin/python
TLA_JAR=/path/to/tla2tools.jar make formal
```

main由来の受入証拠とKMS旧HEADの証拠は別入力です。現mainを取り込んだKMS候補の全体coverage・SDK/browser・形式検査・hosted CIは未実行です。

CIはstatementと未フィルターsource-line coverageを両方90%以上に強制します。`make before-commit`単独のcoverage判定はstatementのみで、source-line判定はCIのCobertura生成後ステージと上記の2コマンドで行います。SDK/browser、license、依存security、埋込asset、形式検査も行います。cross compileは他OSでの実行成功を意味しません。開発用Python/Node/Javaはruntime依存ではありません。UI・Scalarの再生成は権利表記とlockを保持する[各build手順](tools/console/README.md)・[Scalar手順](tools/scalar/README.md)・[Python runtimeの権利・再生成手順](tools/python/README.md)に従います。

形式検査にはObjectStore、StackOwnership、S3Versions、NativeIAMOwnership、RESTDeploymentSnapshot、SSMParameterState、StartupTokenVisibility、BrowserSessions、SQSLifecycle、SQSStorageBudget、ProviderActor、GCPStorageState、AzureBlobState、ProjectAuthority、BlobRange、ProviderPath、KMSKeyState、KMSProtocolAdmissionの18有限モデルと94の意図した欠陥検出をregistryへ登録しています。追加モデルの有限検査結果と、統合後の固定HEAD検査は区別して記録します。TLCと意図したmutantの反例で限定した安全条件を検査しますが、Go実装のrefinement、暗号、全AWS仕様・実AWSとの一致を証明するものではありません。変更はregistryとsource対応を更新し、固定HEADのCI結果で判断します。[形式検査](formal/README.md)、[CI](docs/ci.md)、[設計](docs/design/0001-go-standalone.md)。

## ライセンス

本体は[MIT](LICENSE)。`/_simulator/licenses/`にGo・Cloudscape・Scalar・CPython/WASI等の原文LICENSE/NOTICEを同梱しています。再配布時も保持してください。AWS公式サービスではなく、AWSロゴは使用しません。[設計・引継ぎ資料](docs/handoff/README.md)。
