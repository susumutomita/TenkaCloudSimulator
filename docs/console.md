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

`make e2e`は実バイナリを空ディレクトリ・PATH空で起動し、UI/SDK共有状態、upload/download、literal dot-segment keys、IAM拒否、Scalarの実Sendを検証。全外部requestsを遮断し、要求があれば失敗。`make audit`は23段階を撮影、DOM snapshot、キーボードfocus/validation、390px/720pxのdocument幅、axe WCAGルールを検証。機械検査は完全なアクセシビリティ認証ではない。

スクリーンショットはreports/console-objects-desktop.png、reports/console-mobile.png、reports/audit/へ保存。ダミー値のみ使用。実ホストintegrationは秘密flagをログや画面に出さず採点結果のみ記録する。

現状のURLはサービス単位のhash routingまで。リージョン・operatorの選択identity・選択resource詳細はページ状態で、reload時の復元やresource詳細のブラウザBack履歴は実AWS同等ではない。これらと未実装tab/設定の追加は引き続き対応が必要であり、今回のhello-world完走と区別する。

リージョン変更は選択bucket/detail/modalを破棄して、そのリージョンのbucket一覧へ戻す。保存中のcontext変更をUIでガードし、古いregionのbucketへ誤って書き込まないことをSDK readback付きの実ブラウザ回帰で検証する。

LambdaはFunctions一覧→Author from scratchで作成→CodeのZIP Deploy→Testの実Python実行→Configuration→確認入力付き削除を同じnative APIで行う。Invoke限定identityには一覧/configurationを開放せず、host提供の名前からTestする。inline editor/monitoring等の未再現点と実行制限は[Lambda](lambda.md)。公開AWS guideとの操作対応であり、ログイン済みAWS画面とのpixel一致は未検証。

サービス変更はメニュー・breadcrumb・サービス単位hashのいずれも作成/編集/uploadフォームと入力・選択file・フォームerrorを破棄する。遷移先の一覧と操作を表示し、移動前の未送信フォームから別サービスのリソースを作らない。4種類のフォーム×menu/hashの8通りを実ブラウザとSDK readbackで検証する。

S3はbucket→Objects→Show versionsでversion ID、最新判定、delete markerを表示し、指定版をdownload/永久削除する。Properties→Bucket Versioning→Edit→Enable/Suspend、Permissions→Block public access (bucket settings)→Edit→Save changes→confirm入力もnative APIと同じ状態へ接続した。一次資料は[version一覧](https://docs.aws.amazon.com/AmazonS3/latest/userguide/list-obj-version-enabled-bucket.html)・[bucket公開ブロック設定](https://docs.aws.amazon.com/AmazonS3/latest/userguide/configuring-block-public-access-bucket.html)。bucket-level設定のみであり、未実装のACL/resource policiesを公開可能にする機能はない。実ブラウザでSDK更新→履歴読取→旧版download→marker除去→current復元、PC/390px・外部通信0を検証する。object全Properties page・実AWSの余白/全設定との一致は未検証。

S3のProperties→Tagsは公式Cloudscape TagEditorのAdd tag/Key/Value/Remove/Save tagsを用い、native checksum付きPUT/GETと同じprovider-ownedタグを扱う。CFN resource Tagsも同じ値を作る。2チームのSDK/Console更新、空でない履歴のDeleteStack拒否と明示cleanupを実バイナリで検証する。AWS-generated tags/ABAC/課金は未対応。ログイン済AWS画面とのpixel一致・全page layout同等性は未検証。

## IAM / Lambdaのロール導線と比較の限界

IAM→Roles→Create role→Role details→Lambda→Create function→Permissions→Existing roleを実装した。role ARNとpermissions JSONは既存のlocal identity API/SQLiteを使用し、create時のIf-None-Match:*で同名identityを上書きしない。編集だけが既存policyを置き換える。simulator-adminは選択肢から除く。operatorだけが管理でき、participantはIAMの一覧/作成を拒否する。

[公式Lambda execution-role手順](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html)と[公式IAM role作成](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create.html)を参照した。公式はLambda service principalへのtrust、managed policies、自動role作成等を持つが、ここでは未対応。AWS風の項目名/Cloudscape componentの使用を、AWS画面の完全再現やtrust評価と扱わない。認証済みの現行AWS画面を直接確認していないため、pixel一致/最新の全配置は未検証。公開文書は操作の参考資料であり、実AWS画面を監査した証拠ではない。

実ブラウザでは空のIAMからrole作成、Lambdaのrole選択、関数作成、ZIP Deploy、実Pythonが42を返すまで確認する。PC/390px、別teamのrole非共有、participant管理拒否も検証。残る差分はdocs/console-fidelity.mdに記録する。

IAMはnative Query APIを使うCloudscape Wizard、Permissions/Trust relationshipsの実状態操作に対応する。従来のidentity方式はLocal identitiesへ残す。[導線・制限](iam.md)。

API GatewayはAPIs→Regional REST作成→resource/method→Lambda proxy→Deploy API/new stage→stage実行をnative APIと共有する。作成時はAPI/method限定Lambda resource permissionを追加し、実行ロールによるサービスアクセスとは分離する。応答は安全な非実行downloadで、AWSのbrowser app hostingとは異なる。resource tree、既存stage編集等の差と実確認範囲は[API Gateway](apigateway.md)と[再現差分](console-fidelity.md)を参照。


## Access-token paste boundary (local candidate)

Login uses the real Cloudscape password Input. The field accepts only the token
itself; command labels, surrounding spaces and invalid/expired tokens are not
trimmed or granted access. Clipboard HTML is never interpreted. Copy a plain-text
token when the clipboard contains no text/plain representation. CR/LF are removed
only as required by the existing HTML password-input behavior.

A capture-phase backup records an explicit plain-text paste and its selection.
Normal native insertion/onChange cancels that backup, preserving native undo.
Only a paste without the normal state update uses the next-task fallback; newer
typing/paste/navigation invalidates it, and keyboard submission flushes it first.
No token is logged, stored in browser storage, or granted authority by this UI.
The server still checks its exact local credential and issues its separate opaque
browser session.401 remains invalid/expired; connection failures and other HTTP
errors have fixed distinct messages without reflecting response bodies.

Baseline cloud Chromium tests used actual Clipboard.writeText and trusted paste
events, not fill() as paste evidence. Valid generated tokens logged in/out/in;
invalid/spaces/label inputs remained401. An artificial target preventDefault
fixture caused the old input to stay empty and is a regression seam, not a Dia
root-cause claim. An HTML-only clipboard also produced empty password input while
a rich editor received its content, but the user's clipboard format is unknown.
Linux Meta+V is not Mac proof; native context-menu Paste and real Mac/Dia remain
unverified. No Mac or real user credentials were accessed. Candidate browser and
final immutable gates must run before claiming the new behavior passes.

References: [React capture-phase paste events](https://react.dev/reference/react-dom/components/common#onpaste),
[HTML password sanitization](https://html.spec.whatwg.org/multipage/input.html#password-state-(type=password)),
and [official Cloudscape Input](https://cloudscape.design/components/input/).
