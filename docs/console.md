# コンソールの再現範囲と比較証拠

公式Cloudscape React components/global stylesを実importし、Goバイナリへbundleする。S3、Systems Manager、CloudFormationという実サービス名を使用し、非公式Simulatorであることを常時表示。AWS商標ロゴは使わない。

## 一次資料との比較（2026-11-03）

[AWS S3 Getting started](https://docs.aws.amazon.com/AmazonS3/latest/userguide/GetStartedWithS3.html)と[Systems Manager Create parameter](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-create-console.html)の公開手順を確認。[Cloudscape table pattern](https://cloudscape.design/patterns/resource-management/view/table-view/)と[公式CSP指針](https://cloudscape.design/get-started/dev-guides/csp/)も参照。

| 操作 | 公式資料の導線 | 今回の実装・確認 | 未再現 |
| --- | --- | --- | --- |
| S3 bucket一覧 | General purpose buckets→Create bucket | 同じナビ名、一覧、検索/選択/pagination、独立Create bucket pageのGeneral configuration | directory buckets、access points、AWS全サービスglobal navigation |
| S3 upload | bucket→Objects tab→Upload→Files and folders→Add files→Upload | 同じ操作順・名称の独立page、SDKで正確なbytesを読戻し | 複数ファイル/フォルダ、storage classes、暗号化、multipart |
| S3 object/delete | objectsから名前/選択して操作 | exact keyでdownload、確認入力delete、BucketNotEmpty可視化 | object Properties page全体、AWS固有error presentation完全一致 |
| SSM create | Systems Manager→Parameter Store→Create parameter→Name/Description/tier/Type/data type/Value | 公式順の独立page。Standard/String/StringList/textのみ有効、未対応SecureString/Advancedはdisabled | Tags/KMS/AMI validation、実AWSの30秒再作成待機 |
| SSM read | 一覧の名前→Overviewで詳細 | GetParameterによる同じSQLite値、Overview tab、Version/Type/region、edit | History/Tags操作、AWSの全metadata、native session/federation |

実AWSログイン済みコンソールはアクセスしていない。公式ガイドの画像URL取得は環境のHTTP403、Cloudscape live demoはtunnel failureだった。したがって現行実AWSのpixel比較、全配置/余白/レスポンシブ一致は**未検証**。ガイド手順との一致と、Simulatorの実ブラウザ検証を区別する。単にCloudscapeを使ったから本物と同一とは主張しない。

## ブラウザ検証

`make e2e`は実バイナリを空ディレクトリ・PATH空で起動し、UI/SDK共有状態、upload/download、literal dot-segment keys、IAM拒否、Scalarの実Sendを検証。全外部requestsを遮断し、要求があれば失敗。`make audit`は7段階を撮影、DOM snapshot、キーボードfocus/validation、390px/720pxのdocument幅、axe WCAGルールを検証。機械検査は完全なアクセシビリティ認証ではない。

スクリーンショットはreports/console-objects-desktop.png、reports/console-mobile.png、reports/audit/へ保存。ダミー値のみ使用。実ホストintegrationは秘密flagをログや画面に出さず採点結果のみ記録する。
