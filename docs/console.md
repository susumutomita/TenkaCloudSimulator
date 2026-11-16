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

## 実画面を参照した Home と参加者導線（2026-11-08）

利用者提供の現行 AWS Console Home・Services flyout・Parameter Store の画面と、
ReadOnly role で開いている実 AWS の SSM 一覧を閲覧して比較した。これは実画面の
読み取りによる配置確認であり、実 AWS リソースの作成・変更や画素単位の一致試験ではない。
上の2026-11-03記録は当時の確認範囲として保持する。

- Console の起点は特定サービスではなく Console Home。中央のカード型dashboard、
  常設サービス検索、濃紺の2ペインServicesメニュー、サービスshortcut帯を参照した。
  Simulatorは利用可能サービスとローカル環境情報を表示し、実装していない料金・Health・
  利用履歴を架空の実測値として表示しない。AWSロゴや利用者のaccount情報もコピーしない。
- 参加者handoffもHomeへ着地する。Homeと割当Parameter名の一覧は値のAPIを呼ばない。
  Systems Manager → Parameter Store → 自分のParameter名を明示的に開いて初めて
  GetParameterを送る。Stringの値は詳細で通常表示し、回答隠しの特殊ルールは追加しない。
- 一覧の列は名前・利用枠・種類・データ型・最終変更日、My parametersタブを使う。
  参加者scopeはParameter名だけを保持するため、取得していないmetadataはダッシュ表示。
  他のParameterを列挙するDescribeParameters権限を広げたり、値を先読みしたりしない。
  Public parametersとSettingsは未実装と表示する。直接の名前入力は一覧取得エラー時の補助。
- パンくずのConsole HomeはHomeへ戻る。SSM詳細URLはregionとParameter名を保持し、
  reloadとBack/Forwardで戻れる。値、operator token、STS秘密鍵はURLへ含めない。
  一覧で取得済みのDescriptionだけを同じidentity/region/名前の詳細へ引き継ぐ。
  直接URLやreloadで説明を取得していない場合は編集欄へ未取得と明示し、未編集なら保持する。
- 同じ参加者sessionから同梱API referenceの静的GETを開ける。Scalarの実Sendにも
  元のexact Parameter名・region・Get/Put操作・IAM判定が適用される。資料の閲覧を
  S3やIAM管理操作、他チームのAPIアクセス許可へ変換しない。

この変更はAWSの全機能・全画面の再現ではない。料金、Health、ウィジェット編集、
CloudShell、未実装サービス、その他providerのidentity体系は追加していない。

## ブラウザ検証

`make e2e`は実バイナリを空ディレクトリ・PATH空で起動し、UI/SDK共有状態、upload/download、literal dot-segment keys、IAM拒否、Scalarの実Sendを検証。全外部requestsを遮断し、要求があれば失敗。`make audit`は50段階を撮影、DOM snapshot、キーボードfocus/validation、390px/720pxのdocument幅、axe WCAGルールを検証。機械検査は完全なアクセシビリティ認証ではない。

スクリーンショットはreports/console-objects-desktop.png、reports/console-mobile.png、reports/audit/へ保存。ダミー値のみ使用。実ホストintegrationは秘密flagをログや画面に出さず採点結果のみ記録する。

SSM詳細はregion・Parameter名をhashに保持する。その他サービスの選択resource詳細とoperatorの選択identityはページ状態で、reload時の復元やresource詳細のブラウザBack履歴は実AWS同等ではない。これらと未実装tab/設定の追加は引き続き対応が必要であり、今回のhello-world完走と区別する。

リージョン変更は選択bucket/detail/modalを破棄して、そのリージョンのbucket一覧へ戻す。保存中のcontext変更をUIでガードし、古いregionのbucketへ誤って書き込まないことをSDK readback付きの実ブラウザ回帰で検証する。

LambdaはFunctions一覧→Author from scratchで作成→CodeのZIP Deploy→Testの実Python実行→Configuration→確認入力付き削除を同じnative APIで行う。Invoke限定identityには一覧/configurationを開放せず、host提供の名前からTestする。CodeMirrorでの保存済みhandler編集・他ZIPファイル保持と未保存下書きの制限、monitoring等の未再現点と実行制限は[Lambda](lambda.md)。公開AWS guideとの操作対応であり、ログイン済みAWS画面とのpixel一致は未検証。

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

## CloudFormation Events（2026-11-08、検証中）

Stacksの名前を選ぶか「Open stack history」に名前または正確なStack IDを
入力すると、既存のDescribeStackEvents APIから同じSQLiteの記録を表示する。
Eventsが初期タブで、Outputsは出力タブを明示的に選んだ場合だけ取得する。
時刻、論理ID、状態、リソースタイプ、物理ID、イベントIDを新しい順に表示し、
100件を超える履歴は「Load older events」で追加する。Refreshは先頭ページを
再取得する。削除済みスタックは保持範囲内の正確なStack IDで参照できる。
名前の再利用による別世代の混在を避け、画面の一覧からはIDを渡す。

リージョン・演習ID・サービス・選択スタック変更とCloseで古い読取を破棄し、
拒否されたRefresh/追加読込では既存行も消す。IAMとparticipant browserへの
既存拒否を変更せず、DescribeStacks一覧権限がないoperator演習IDでも、
許可されたStack ARNのEventsを名前指定で読む導線を提供する。

記録は確定した同期ローカル作成・削除のみ。失敗した要求はAPIエラーとなり、
failure event、任意例外、parameter/property値、Outputsはイベントに記録しない。
CloudTrail実操作記録、Lambda自動ログ配送、実AWS非同期lifecycleやpixel一致を
この表示から主張しない。native仕様は
[DescribeStackEvents](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStackEvents.html)
を参照するが、実AWS呼出しは行わない。

新規Node投影・Query試験4件と既存console試験を含む19件、production bundle、
Python構文検査は成功。実SDK作成104イベント、ページ追加、削除世代、権限変更、
古い応答、1440/390px reflow/axeを要求するブラウザ回帰をmake e2eへ登録した。
クラウドChromiumは既存調査でsocket EPERMとなっており、新規GUI回帰は未実行。
`bee5cbf`固定sourceのmake before-commitは全Go race/fmt/vet/assets/modules/buildを
通過し、statement coverageは20691/21659（95.53%、閾値90%）。その後の変更は
E2E fixtureの明示SSM Nameとこの証跡だけで、runtime/埋込assetsは同一。
実際のE2E native fixtureも104件、100+4件のページ、削除世代の取得を通過した。
これはブラウザ実行の代替ではない。make e2eと公開HEADのhosted CIは未完了。


## Favorite service shortcuts

The AWS shell starts with no pinned services. In the Services menu or its search
results, use a service's star to add or remove it from Favorites. The quickbar
shows only these selected services, and disappears when none are selected.
Favorites survive reloads in the same browser and local simulator origin; they
are not synchronized to AWS, other browsers, or another local port. Only public
service IDs are stored. If browser storage is unavailable, the console explains
that the preference lasts only while the page remains open.

This follows the AWS [favorites quickbar](https://docs.aws.amazon.com/awsconsolehelpdocs/latest/gsg/add-fave.html)
and [add/remove star](https://docs.aws.amazon.com/awsconsolehelpdocs/latest/gsg/add-fave-services.html)
interaction. Drag reordering and AWS account-level synchronization are not
implemented. The simulator's existing capability and participant-session filter
still applies to Services, search and Favorites. A scoped Parameter Store handoff
continues to show only Console Home and Systems Manager, even if this browser
previously pinned S3. This is a session-scope restriction, not dynamic IAM-policy
discovery; stars never grant API permission or change the session.

### Read-only EC2 and VPC inspection

The EC2 and VPC views list the local CloudFormation-managed resources returned
by `DescribeInstances` and `DescribeVpcs` in the selected region. Discovery must
advertise both read operations and `console: true` before these views appear.
They do not implement instance launch/start/stop, VPC creation, or network edits.
The recorded simulator runtime state is not an AWS instance-state claim or a
probe of guest health; absent/unknown state is shown as unconfirmed. UserData,
runtime plans, credentials and internal connection details are never displayed.

Lists are bounded by the API and must fail rather than silently truncate. A read
failure clears previous data and is shown as an error, not an empty-success
result. Switching identity, region or service discards old in-flight responses.
The initial read API is for the local operator/exercise-identity context, not a
new STS participant authorization surface. Each team's isolated process/database
remains the tenancy boundary.
