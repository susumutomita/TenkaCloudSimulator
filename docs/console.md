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

`make e2e`は実バイナリを空ディレクトリ・PATH空で起動し、UI/SDK共有状態、upload/download、literal dot-segment keys、IAM拒否、Scalarの実Sendを検証。全外部requestsを遮断し、要求があれば失敗。`make audit`は50段階を撮影、DOM snapshot、キーボードfocus/validation、390px/720pxのdocument幅、axe WCAGルールを検証。機械検査は完全なアクセシビリティ認証ではない。

スクリーンショットはreports/console-objects-desktop.png、reports/console-mobile.png、reports/audit/へ保存。ダミー値のみ使用。実ホストintegrationは秘密flagをログや画面に出さず採点結果のみ記録する。

現状のURLはサービス単位のhash routingまで。リージョン・operatorの選択identity・選択resource詳細はページ状態で、reload時の復元やresource詳細のブラウザBack履歴は実AWS同等ではない。これらと未実装tab/設定の追加は引き続き対応が必要であり、今回のhello-world完走と区別する。

リージョン変更は選択bucket/detail/modalを破棄して、そのリージョンのbucket一覧へ戻す。保存中のcontext変更をUIでガードし、古いregionのbucketへ誤って書き込まないことをSDK readback付きの実ブラウザ回帰で検証する。

LambdaはFunctions一覧→Author from scratchで作成→CodeのZIP Deploy→Testの実Python実行→Configuration→確認入力付き削除を同じnative APIで行う。Invoke限定identityには一覧/configurationを開放せず、host提供の名前からTestする。inline editor/monitoring等の未再現点と実行制限は[Lambda](lambda.md)。公開AWS guideとの操作対応であり、ログイン済みAWS画面とのpixel一致は未検証。

サービス変更はメニュー・breadcrumb・サービス単位hashのいずれも作成/編集/uploadフォームと入力・選択file・フォームerrorを破棄する。遷移先の一覧と操作を表示し、移動前の未送信フォームから別サービスのリソースを作らない。4種類のフォーム×menu/hashの8通りを実ブラウザとSDK readbackで検証する。

S3はbucket→Objects→Show versionsでversion ID、最新判定、delete markerを表示し、指定版をdownload/永久削除する。Properties→Bucket Versioning→Edit→Enable/Suspend、Permissions→Block public access (bucket settings)→Edit→Save changes→confirm入力もnative APIと同じ状態へ接続した。一次資料は[version一覧](https://docs.aws.amazon.com/AmazonS3/latest/userguide/list-obj-version-enabled-bucket.html)・[bucket公開ブロック設定](https://docs.aws.amazon.com/AmazonS3/latest/userguide/configuring-block-public-access-bucket.html)。bucket-level設定のみであり、未実装のACL/resource policiesを公開可能にする機能はない。実ブラウザでSDK更新→履歴読取→旧版download→marker除去→current復元、PC/390px・外部通信0を検証する。object全Properties page・実AWSの余白/全設定との一致は未検証。

S3のProperties→Tagsは公式Cloudscape TagEditorのAdd tag/Key/Value/Remove/Save tagsを用い、native checksum付きPUT/GETと同じprovider-ownedタグを扱う。CFN resource Tagsも同じ値を作る。2チームのSDK/Console更新、空でない履歴のDeleteStack拒否と明示cleanupを実バイナリで検証する。AWS-generated tags/ABAC/課金は未対応。ログイン済AWS画面とのpixel一致・全page layout同等性は未検証。

## IAM / Lambdaのロール導線と比較の限界

IAM→Roles→Create role→Role details→Lambda→Create function→Permissions→Existing roleを、native IAM Query APIの状態で操作する。Trust relationships と inline permissions は限定された実装範囲で検証する。従来の独自JSON identity APIは Local identities に分離している。自動ロール作成、AWS全managed policies、STSや完全なIAM評価との同等性は主張しない。[IAMの対応範囲](iam.md)を参照。

[公式Lambda execution-role手順](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html)と[公式IAM role作成](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create.html)を操作の参照にする。認証済みの現行AWS画面は直接確認していないため、pixel一致・最新の全配置は未検証。既存の実ブラウザ回帰は空のIAMからrole作成、Lambdaのrole選択、関数作成、ZIP Deploy、実Pythonが42を返すまでを検査する。PC/390px、別teamのrole非共有、participant管理拒否も別の実行検証として扱う。

IAMはnative Query APIを使うCloudscape Wizard、Permissions/Trust relationshipsの実状態操作に対応する。従来のidentity方式はLocal identitiesへ残す。[導線・制限](iam.md)。

API GatewayはAPIs→Regional REST作成→resource/method→Lambda proxy→Deploy API/new stage→stage実行をnative APIと共有する。作成時はAPI/method限定Lambda resource permissionを追加し、実行ロールによるサービスアクセスとは分離する。応答は安全な非実行downloadで、AWSのbrowser app hostingとは異なる。resource tree、既存stage編集等の差と実確認範囲は[API Gateway](apigateway.md)と[再現差分](console-fidelity.md)を参照。


## Access-token paste boundary

Login uses the real Cloudscape password Input. The field accepts only the token
itself; command labels, surrounding spaces and invalid/expired tokens are not
trimmed or granted access. Clipboard HTML is never interpreted. Copy a plain-text
token when the clipboard contains no text/plain representation. CR/LF are removed
only as required by the existing HTML password-input behavior.

A capture-phase backup records an explicit plain-text paste and its selection.
Normal native insertion/onChange cancels that backup, preserving native undo.
Only a paste without the normal state update uses the next-task fallback; newer
typing/paste/navigation invalidates it, including a later empty/HTML-only paste;
nullable clipboard data is ignored safely. Keyboard submission flushes it first.
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
unverified. No Mac or real user credentials were accessed. PR411の元HEADとマージ後mainでは既存のブラウザ・CI検査が成功した。
それでも実Mac/Diaの動作確認にはならず、以後の変更は新しいHEADの検証が必要。 The final focused helper passed real
trusted plain/HTML clipboard cases, native and selected-replacement Undo, the
artificial canceled/default/bubble path, stale typing/paste/navigation boundaries,
immediate Enter, invalid inputs, safe HTTP/network errors, busy-submit guards and
PC1440/390 reflow/axe with zero external requests/page errors. The null-data case
is explicitly synthetic, not OS-paste evidence. This is not real Mac/Dia validation.

References: [React capture-phase paste events](https://react.dev/reference/react-dom/components/common#onpaste),
[HTML password sanitization](https://html.spec.whatwg.org/multipage/input.html#password-state-(type=password)),
and [official Cloudscape Input](https://cloudscape.design/components/input/).

### Update and retry

Stop the old simulator with Ctrl+C in its terminal, then run `git pull --ff-only`
and `make start` in the repository. The start helper rebuilds current source and
reuses `./practice.db` and `./simulator-token`; preserve any configured
`START_STATE` / `START_TOKEN_FILE` overrides. In the browser use Cmd+Shift+R to
reload the updated page, then paste into Access token and choose Open sandbox.
On the owner's Mac, `pbcopy < ./simulator-token` is an optional plain-text copy
command (substitute the configured private token path). It is documentation only,
not a command executed during this investigation. Never send the token in chat or
include it in logs/screenshots. If paste still leaves the field empty, the actual
Mac/Dia behavior needs owner-device verification; no official Dia-specific cause
is confirmed. Native paste is preserved following
[Chrome's paste guidance](https://developer.chrome.com/docs/lighthouse/best-practices/paste-preventing-inputs).

## クラウド別ナビゲーションと言語（刷新中）

AWS、Google Cloud、Azure を同じ階層で選び、そのクラウドの対応済みサービスだけを検索する。サービスのバックログ件数を操作可能なサービス数として表示しない。旧サービスhash URLは維持する。AWSは公式Cloudscape、Google CloudとAzureはそれぞれの公開公式資料に基づく専用の外枠・リソース導線を持つ。名前や色の変更だけで実コンソールとの一致とは扱わない。

言語は日本語／英語を明示選択する。初回は英語、選択した表示言語だけをブラウザへ保存する。資格情報は保存しない。入力中のフォーム・選択リソース・認証主体を言語切替で初期化しない。Cloudscapeの標準表示には公式翻訳メッセージを同梱し、画面独自の静的文言も翻訳する。APIの値、ARN、リソース名、エラーコードは翻訳しない。

共通言語設定は実Chromiumで切替・保存・再読込・編集中の値の保持・保存禁止環境を検査した。共通の言語設定と各クラウド操作は実埋込バイナリのPC1440/390で検証済み。言語12状態・ストレージ操作22状態のaxe、外部通信0・ページエラー0、チーム分離、403/401、世代412拒否と詳細表示を確認した。既存E2Eを含む全体ゲートと新HEADのCIは進行中。完了前の画面を完成版として扱わない。Google Cloudの公開手順とAzureの公開公式画像を参照しているが、認証済みの現行画面は未確認。[GCP画面](gcp-console.md)、[Azure画面](azure-console.md)、[共通契約](design/0006-console-provider-contract.md)に範囲を分けて記録する。

明示的なローカルトークンファイル選択は[専用ガイド](login-token-file.md)を参照。実埋込バイナリでもファイル選択・ログイン／ログアウト、古い読込結果の破棄、日英表示を検証したが、実Mac/Diaの確認は含まない。
