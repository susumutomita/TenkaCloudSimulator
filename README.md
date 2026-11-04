# TenkaCloud Simulator

非公式のAWS学習用シミュレーター。Go単一バイナリに公式Cloudscapeコンソール、Scalar API reference、SQLiteを埋め込む。実行時にNode、Python、Docker、実AWSや外部DBは不要。AWSロゴは使用しない。

## 起動

```sh
make start
```

Go 1.26が必要（CIは1.26.10）。`make start`は毎回現在のソースをビルドし、`./practice.db`と`./simulator-token`を再利用する。コンソールは <http://127.0.0.1:4566/_simulator/console/>。対話端末ではoperator tokenを表示し、同じ値を`simulator-token`に0600で保存する。CI環境や出力のリダイレクト時には値を表示しない。この秘密は参加者へ渡さない。[起動・上書き設定・既存バイナリの使い方](docs/start.md)。Scalarは `/_simulator/docs/`、能力表は `/_simulator/v1/capabilities`。SIGINT/SIGTERMで終了。`--state`省略は毎回空のメモリーDB。

## 対応範囲

| サービス | 対応 | 制限 |
| --- | --- | --- |
| S3 | ListBuckets/CreateBucket/HeadBucket/GetBucketLocation/DeleteBucket/ListObjectsV2/PutObject/GetObject/HeadObject/DeleteObject、Get/PutBucketVersioning、ListObjectVersions、Get/Put/DeletePublicAccessBlock、Get/Put/DeleteBucketTagging | path-style、4リージョン。versioning/versions・bucket public-access-block対応。multipart/ACLなし |
| IAM | 9 role/inline-policy Query操作、Lambda service trust、Cloudscape作成Wizard | root path/限定service principal、managed policies/STSなし。[詳細](docs/iam.md) |
| Systems Manager Parameter Store | PutParameter/GetParameter/GetParameters/GetParametersByPath/DescribeParameters/DeleteParameter | StandardのString/StringList/textのみ。SecureString/KMS/labelsなし |
| CloudFormation | CreateStack/DescribeStacks/DeleteStack | S3 Bucket、SSM Parameter、inline Python Lambda Function、1 inline identity policy付きIAM Role。API Gateway5種とLambda Permission、Ref/Sub/GetAttの限定評価。[実行例](docs/cloudformation-rest.md)。その他は明示エラー |
| API Gateway | Regional REST管理16操作、NONE/Lambda proxy、immutable deployment/stage、ローカルHTTP実行 | 同一region/accountのLambda SourceArn grantが必須。応答は非実行download。WAF/v2/authorizer/既存stage編集なし。限定CFN対応。[詳細](docs/apigateway.md) |
| Lambda | CreateFunction/ListFunctions/GetFunctionConfiguration/UpdateFunctionCode/Invoke/DeleteFunction、Add/Get/RemovePermission | 埋込CPython 3.12.16/WASIでPythonコードを実行。128 MiB/1–30秒、同期のみ。限定inline CFNに対応。関数内SSM/S3 SDKは限定対応。network・Function URLは未対応 |

リージョン: us-east-1、us-west-2、ap-northeast-1、eu-west-1。1プロセスの上限: 8 MiB/object、64 MiB retained object versions合計、1000 buckets、10000 objects/versions/delete markers、1000 parameters/identities、100 stacks、100 resources/stack。SSM値4096 UTF-8 bytes、TemplateBody51200 bytes。プロセスメモリ全体の上限ではない。

S3の設定・version ID/IAM・履歴quota・削除境界は[対応詳細](docs/s3.md)。S3のCloudFormationはBucketName/Versioning/PublicAccessBlock/ユーザーTagsとRef/Arnに対応。空でない履歴は削除しない。CFN専用service roleは未対応で、呼出者にStack ARNと各リソース操作の両方の権限が必要。CAPABILITY_IAMは権限を与えない。

IAMはidentity policyのVersion/Statement/Sid/Effect/Action/Resource、配列、`*`/`?`、明示Deny優先を評価する。一般のCondition/Principal/NotAction/NotResource/resource policies/boundaries/SCPは未対応。限定Lambda resource policyはAPI GatewayからのSourceArn付きInvokeだけを許可する。CFNのRoleはローカルidentityを作り、trust metadataは認可に使用しない。カタログのSignInLocalDevelopmentAccess/AWSLambdaBasicExecutionRole ARNは記録のみで権限を付与しない。STS・AWSサインイン・CloudShellは未実装。

## IAMからLambdaを作成

Services→IAM→Roles→Create roleで信頼先Lambda→権限なし→名前と確認へ進む。roleのPermissionsで必要なinline policyを追加する。Create a Lambda function→Permissions→Existing roleで選び、Code→ZIP Deploy→Testで実Pythonを実行する。trust policyは作成・実行時に評価し、権限なしはimplicit Deny。未実装のAWS managed policiesを付与した扱いにしない。

既存のlocal identity/CFN roleは従来の方式を維持し、Local identitiesから管理する。native IAM roleとは別の保存モデルで、相互の名前採用は拒否。参加者もiam操作・PassRoleの個別認可が必要。[IAM仕様・制限・API例](docs/iam.md)。

## API/SDK

```sh
# operatorのみ。トークン値をコマンド履歴に直接書かない。
SIM_TOKEN=$(cat simulator-token)
curl -H "X-Simulator-Token: $SIM_TOKEN" -X PUT http://127.0.0.1:4566/practice-bucket
curl -H "X-Simulator-Token: $SIM_TOKEN" http://127.0.0.1:4566/
```

SDKは明示endpointとダミー`TCSIM…`キーを使用し、before-sign/build hookで`X-Simulator-Token`を付ける。署名は検証しない。実AWS形式キー、session tokens、presigned URLは拒否。S3はREST/XML、SSMはroot POST + `X-Amz-Target: AmazonSSM.GetParameter`等のJSON 1.1、CloudFormationはroot POST Query/XML。`/_simulator/providers/aws/`も同一状態。SDK実例は[test/e2e/check.py](test/e2e/check.py)、ローカル接続は[test/platform/README.md](test/platform/README.md)。

## 問題を解く

[現行29問の対応表](docs/catalog.md)。hello-worldのみ、実テンプレート取り込み→チーム別起動→SSM取得→TenkaCloudの実採点→削除を開発用接続で検証。他のAWSワークロードは未対応であり選択しない。Docker/coordination問題は本来の実行系を使う。Simulator本体に問題IDや採点を持ち込まない。通常のTenkaCloud起動に自動統合済みではない。

## 認証と分離

1チーム/環境に1プロセス・1SQLite・固有operator tokenを割り当てる。ホストがoperator権限で`POST /_simulator/v1/access`に`{"Principal":"role-name"}`を送り、固定identityの15分participant tokenを発行する。participantは他identityへの切替・IAM管理・stack秘密outputs取得不可。tokenは再起動で失効。ユーザー認証・チーム割当・proxy認可はホスト責務。同一OS利用者からのファイルアクセスを隔離するものではない。

通常はnumeric loopbackのみ。`--allow-remote`は信頼された認証proxy/隔離ネットワークのための明示指定であり、TLSやtenant isolationを自動追加しない。開発限定の`--unsafe-no-auth`はloopbackだけで使用し、参加者用には使わない。[セキュリティ境界](SECURITY.md)。

## 開発と検証

```sh
make before-commit             # fmt/vet/assets/modules/race/coverage/build
make cross-check               # 5 OS/archのcompile
python3 -m venv /tmp/tcsim-tests
/tmp/tcsim-tests/bin/pip install -r scripts/requirements-test.txt
/tmp/tcsim-tests/bin/playwright install chromium
/tmp/tcsim-tests/bin/python scripts/get-audit-tools.py /tmp/simulator-audit
AXE_SCRIPT=/tmp/simulator-audit/axe.min.js make e2e PYTHON=/tmp/tcsim-tests/bin/python
# システムChromiumも CHROMIUM_EXECUTABLE=/path/to/chromium で使用可能
TLA_JAR=/path/to/tla2tools.jar make formal
npm ci --prefix tools/console --ignore-scripts
node tools/console/build.mjs
```

statementとsource-line coverageの両方90%必須。ホストPythonはSDK/browser/coverage/license等の開発支援だけに使用する。Lambda用CPythonはGoに埋め込み、実行時の外部Pythonは不要。[Lambdaの範囲・制限](docs/lambda.md)、[CI](docs/ci.md)、[設計](docs/design/0001-go-standalone.md)、[画面の再現範囲](docs/console.md)、[形式検証](formal/README.md)、[引継ぎ](docs/handoff/README.md)。

## ライセンス

本体の[MIT LICENSE](LICENSE)と依存ライブラリの原文rights noticesを保持。実行バイナリの`/_simulator/licenses/`にGo/Scalar/Cloudscape/CPython-WASIの原文LICENSE/NOTICEを同梱。再生成手順と出所は[tools/console/README.md](tools/console/README.md)、[tools/scalar/README.md](tools/scalar/README.md)、[tools/python/README.md](tools/python/README.md)。AWSの商標や公式サービスとの提携を意味しない。
