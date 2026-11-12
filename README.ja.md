# TenkaCloud Simulator

[English](README.md) | 日本語

クラウドのアカウントなしで、APIやコンソールの操作をローカルで練習できる、非公式の学習用シミュレーターです。
AWSを中心に、Google Cloud・Azure・任意で有効化するSakuraの一部にも対応しています。
対応する画面とAPIは、同じSQLiteのデータを操作します。

コンソール、APIリファレンス、SQLite、Python Lambdaの実行環境を1つのGoバイナリに同梱しています。
**ビルド済みバイナリの実行にはGo・Node.js・ホストのPython・Docker・外部DB・実クラウド接続は不要です。
ダミーの資格情報と練習用データだけを使ってください。**

## 起動してサインインする

**Go 1.26・GNU make・POSIXシェル**を用意し、リポジトリのルートで実行します。

```sh
make start
```

現在のソースをビルドし、`127.0.0.1:4566`で起動します。
`./practice.db`と`./simulator-token`は再利用します。ビルドに失敗した場合、古いバイナリは起動しません。

1. <http://127.0.0.1:4566/_simulator/console/> を開き、日本語表示にする場合は**JA**を選びます。
2. **トークンファイルを選択**で`simulator-token`を選び、**サンドボックスを開く**を押します。
   ファイルはブラウザー内で読み取り、サインイン時にトークンだけを送信します。
   マスクされた入力欄への貼り付けも使えます。
3. **Ctrl+C**で終了します。再び`make start`を実行すると、保存したリソースを引き継ぎます。

operator tokenは、この環境を管理するための資格情報です。参加者には渡さず、Git・ログ・画面録画に残さないでください。
新しいトークンファイルは`0600`で作成します。対話端末では起動時にトークンが表示される場合がありますが、
リダイレクトやCIでは表示しません。**アカウント → サインアウト**で失効するのは、そのブラウザーのセッションだけです。

詳しくは[起動設定・既存バイナリの実行](docs/start.md)、[トークンファイルの形式](docs/login-token-file.md)、
[セッション](docs/console-sessions.md)を参照してください。
このREADMEは、このソースツリーの機能を説明しています。mainと配布済みバイナリでは、Lambda Tailなどの対応が異なる場合があります。
[バイナリのソース・バージョン](docs/releases.md)を確認してください。

## まず試す：Python Lambdaを実行してログを見る

上でサインインした管理者として、**AWS**のコンソールを使います。

1. **IAM → ロール → ロールを作成**を開きます。**AWS サービス → Lambda**を選び、
   許可は追加せずに`hello-role`という名前で作成します。
2. **Lambda → 関数を作成**を開きます。名前を`hello-local`にして、
   **許可 → 既存のロール**で`hello-role`を選びます。 **関数を作成**を押します。
3. 次の内容を`lambda_function.py`として保存し、普段使うZIPツールで圧縮します。
   ファイルはZIPの直下に置いてください。関数の実行にホストのPythonは不要です。

```python
def lambda_handler(event, context):
    print("Hello from local Lambda", flush=True)
    return {"message": "hello", "event": event}
```

4. 関数の**コード**タブでZIPをアップロードし、**デプロイ**を押します。
5. **テスト**を開き、`{"name":"world"}`を入力します。**このテストのログ末尾を取得**を選んで
   **テスト**を押すと、レスポンスに入力したイベント、ログに`Hello from local Lambda`が表示されます。

ログ取得は**既定で無効**です。APIでは同期Invokeに`X-Amz-Log-Type: Tail`を指定すると、
`X-Amz-Log-Result`に末尾の最大**4 KiB**をbase64で返します。ログには機密値が含まれる可能性があります。
CloudWatchへの配送や履歴保存はありません。標準出力の後に標準エラーを連結するため、時系列順には並びません。
権限は既存の`lambda:InvokeFunction`を使います。

Python 3.12のみ、ゲストメモリ128 MiB、タイムアウト1〜30秒で、任意のネットワーク接続はできません。
この例は権限なしのロールで動きます。S3/SSMを呼ぶ場合はロールへの明示的な許可が必要です。
詳しくは[LambdaのAPI・ZIP制限・関数内SDK](docs/lambda.md)を参照してください。

## サービス別の使い方

対応する操作と制限は各ガイドにまとめています。APIがあっても、画面・CloudFormation・Challengeのすべてに対応するとは限りません。

- **ストレージ・データ：** [S3](docs/s3.md)（[multipart](docs/s3-multipart.md)）、
  [DynamoDB](docs/dynamodb.md)、[Parameter Store](docs/ssm-parameters.md)。
- **関数・メッセージ：** [Lambda](docs/lambda.md)、[API Gateway](docs/apigateway.md)、
  [SQS](docs/sqs.md)、[SNS](docs/sns.md)、[CloudWatch Logs](docs/cloudwatch-logs.md)。
- **認可・シークレット：** [IAM](docs/iam.md)、[native SDK / STS](docs/sts-native.md)、
  [Secrets Manager](docs/secrets-manager.md)、[KMS](docs/kms.md)。
- **ネットワーク：** [Elastic Load Balancing](docs/elb.md)（Lambda ターゲット）、
  [Route 53](docs/route53.md)。
- **スケーリング：** [ローカルDynamoDBテーブル向けAuto Scaling plans](docs/autoscaling-plans.md)。
- **構成の作成：** [CloudFormation](docs/cloudformation-rest.md)、
  [SSM値パラメータ](docs/cloudformation-ssm-parameters.md)、
  [native IAM profile](docs/cloudformation-iam-profiles.md)、
  [固定SSM policy](docs/iam-ssm-managed-policy.md)。
- **ローカルAI推論（任意で有効化）：** [自分のOllamaで動くBedrock Runtime InvokeModel/Converse](docs/bedrock-runtime.md)。
  既定では無効です。クラウドへの代替接続、ストリーミング、ツール利用には対応しません。
- **他クラウド：** [Google Cloud Storage / Azure Blob Storage](docs/multicloud-storage.md)、
  [App Engine（Python 3.12 WSGIバージョン、トラフィック分割、ファイアウォール）](docs/gcp-appengine.md)、
  [Google Cloudの画面](docs/gcp-console.md)、[Azureの画面](docs/azure-console.md)、
  [任意で有効化するSakura Disk構成メタデータ](docs/sakura.md)。
- **設定情報だけを扱うカタログ：** [AWS](docs/aws-catalog.md)、
  [Google Cloud](docs/gcp-catalog.md)、[Azure Resource Manager](docs/azure-catalog.md)。
  ワークロードの実行、データプレーン、実クラウドへのリソース作成には対応しません。

サインイン後、<http://127.0.0.1:4566/_simulator/docs/> でAPIリファレンスを開けます。
exampleは静的な説明です。**Test Request → Send**で、ローカルの実APIに問い合わせます。
[APIの案内](docs/api-reference.md)と[SDK接続例](test/platform/README.md)に従い、ローカルの接続先と認証を指定してください。
実クラウドの資格情報は使わないでください。

## 他の人に使ってもらう前に

- 学習用であり、本番クラウドの代替ではありません。API・IAM・画面の挙動は限定的です。
  記載のないサービスや操作は未対応で、ロードマップは実装済み機能ではありません。
- Secrets Managerの値やKMSの鍵はローカルのSQLiteに保存します。実際の秘密情報の保管には使わないでください。
- **チームごとに別のプロセス・SQLite・operator token**を用意します。ユーザー認証、プロキシ経路の認可、
  IDに固定した参加者トークンの発行はホスト側の責務です。IAMのID選択やlocalhostへの接続だけではチームを隔離できません。
- Google Cloud Storage・Azure Blobの認可はローカルの所有者設定であり、Google IAM・Azure RBACではありません。
- `--allow-remote`はTLSやチーム隔離を追加しません。信頼できる認証付きプロキシの背後か、隔離したネットワークでのみ使ってください。
  同じOSユーザーからのファイルアクセスは防ぎません。
- サービス対応と問題対応は別です。問題を有効にする前に[問題対応表](docs/catalog.md)を確認してください。

参加者向けに運用する前に、[セキュリティ境界](SECURITY.md)を確認してください。

## 開発する

- [作業ルール](AGENTS.md)、[設計](docs/design/0001-go-standalone.md)、
  [並行開発](docs/contributing/parallel-development.md)、[開発状況](docs/development-status.md)。
- [CI・必要な検査](docs/ci.md)、[形式検査](formal/README.md)、
  [現在のCI結果](https://github.com/susumutomita/TenkaCloudSimulator/actions/workflows/ci.yml)。
  結果は検査した版にだけ適用されます。有限モデルの検査は、実装全体の証明ではありません。
- 再ビルド手順：[コンソール](tools/console/README.md)、[Scalar](tools/scalar/README.md)、
  [Python/WASI](tools/python/README.md)。開発用ツールはバイナリの実行時には不要です。

## ライセンスとKumoへの謝辞

本体は[MITライセンス](LICENSE)です。同梱コンポーネントの原文LICENSE/NOTICEは
`/_simulator/licenses/`にあり、再配布時も保持してください。

[Kumo](https://github.com/sivchari/kumo)のメンテナーとコントリビューターの皆さまに感謝します。
MITで公開された取り組みを、サービス登録・ルーティング・SDK用エンドポイントの設計で参考にしました。
Kumoを実行時のバックエンドとしては使っていません。過去に一部を適合して利用した
[IAM](third_party/kumo-iam.json)、[API Gateway](third_party/kumo-apigateway.json)、
[CloudWatch Logs](third_party/kumo-cloudwatch-logs.json)、[KMS](third_party/kumo-kms.json)の出典を記録しています。
特定できた実行コードは、AWSの公開仕様に基づく独自実装へ置き換えています。
[元のMIT表記](third_party/KUMO-LICENSE.txt)と[レビューした置換範囲](third_party/independent-native-source.json)は保持しています。
これらはクリーンルーム開発や法的適合性の証明ではありません。
