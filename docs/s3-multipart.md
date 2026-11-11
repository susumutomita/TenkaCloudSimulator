# S3 multipart uploadの限定実装

CreateMultipartUpload、UploadPart、ListParts、ListMultipartUploads、
CompleteMultipartUpload、AbortMultipartUploadをpath-styleのS3 REST/XMLで扱う。
SDK、S3 bucketのManagement画面、API referenceは同じHTTP APIとSQLite状態を使う。
これはIssue51のmultipart部分であり、S3全機能の対応完了ではない。
通常object、versioning、bucket設定とCloudFormationの範囲は[S3資料](s3.md)を参照。

## APIとIAM

`/{bucket}/{key}`のkeyは空でない1024 UTF-8 bytes以下の文字列。
制御文字など既存S3 key validationで拒否する文字は使用できない。
root endpointと`/_simulator/providers/aws/` endpointは同じ状態を参照する。
各要求はlocal token認証に加えて次のIAM action/resourceを評価する。
upload IDは権限を付与せず、principal選択も認証やチーム分離にはならない。

| 操作・AWS一次資料 | Methodとquery | 要求・成功応答 | IAM action / resource |
| --- | --- | --- | --- |
| [CreateMultipartUpload](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CreateMultipartUpload.html) | `POST /{bucket}/{key}?uploads` | 空body。`Content-Type`を完成objectへ保存し、省略時は`application/octet-stream`。200 XMLのBucket、Key、UploadId | `s3:PutObject` / `arn:aws:s3:::{bucket}/{key}` |
| [UploadPart](https://docs.aws.amazon.com/AmazonS3/latest/API/API_UploadPart.html) | `PUT /{bucket}/{key}?uploadId={id}&partNumber={n}` | bytes body。同じpart番号は置換。200、quoted MD5の`ETag` header | `s3:PutObject` / object ARN |
| [ListParts](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListParts.html) | `GET /{bucket}/{key}?uploadId={id}` | 空body。200 XMLのPartNumber、ETag、LastModified、Sizeとcursor | `s3:ListMultipartUploadParts` / object ARN |
| [ListMultipartUploads](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListMultipartUploads.html) | `GET /{bucket}?uploads` | 空body。200 XMLのKey、UploadId、Initiatedとcursor | `s3:ListBucketMultipartUploads` / `arn:aws:s3:::{bucket}` |
| [CompleteMultipartUpload](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html) | `POST /{bucket}/{key}?uploadId={id}` | 下記XML。200 XMLのLocation、Bucket、Key、ETag。versionがある場合は`x-amz-version-id` | `s3:PutObject` / object ARN |
| [AbortMultipartUpload](https://docs.aws.amazon.com/AmazonS3/latest/API/API_AbortMultipartUpload.html) | `DELETE /{bucket}/{key}?uploadId={id}` | 空body。204、uploadと全partを削除 | `s3:AbortMultipartUpload` / object ARN |

`uploads`は空のflagだけを受け付ける。生成するUploadIdは暗号乱数の32文字hexで、
要求には返された値をそのまま使用する。bucket/key/idの完全一致で検索し、
別keyや別bucketへの流用は拒否する。同じkeyに複数のuploadを開始できる。
同じSQLiteファイルで再起動した場合、upload IDと保存済みpartは保持される。
再起動で失効するbrowser/participant sessionとは別の保存状態である。

queryは単一値だけを受け付け、重複は`InvalidArgument`。
表と下記ページ送りのquery、およびrouting hintの`x-id`以外は受け付けない。
`versionId`、通常object一覧のquery、別操作のmultipart queryを混在させない。
未対応query/headerは`NotImplemented`、不正なmethod/resourceの組合せは
`InvalidArgument`を返し、成功した扱いにはしない。

## 結合、versionとquota

Completeのbodyは1 MiB以下で、次の要素だけを受け付ける。
S3の標準XML namespaceは省略可能。未知・重複field、余分な属性、DTDや
後続XML documentは`MalformedXML`。PartNumber/ETag各fieldのtextは
XMLデコード後で64 bytes以下（前後spaceも含む）。Partは1〜10000件で、PartNumberを
1〜10000の厳密な昇順に並べる。番号の欠番は可能である。

```xml
<CompleteMultipartUpload>
  <Part><PartNumber>1</PartNumber><ETag>"保存時に返された32文字MD5"</ETag></Part>
  <Part><PartNumber>2</PartNumber><ETag>"保存時に返された32文字MD5"</ETag></Part>
</CompleteMultipartUpload>
```

上記ETagは説明用であり、実際のUploadPart応答またはListPartsの値へ置き換える。
各ETagは引用符を含めて保存値と完全一致する必要がある。結合時に保存bodyのMD5も
再計算する。完成ETagは各採用partの生MD5 digestを順に連結したbytesのMD5に
`-採用part数`を付け、引用符で囲む。完成body全体の単純MD5とは異なる。

| ローカル上限 | 範囲 |
| --- | --- |
| 8 MiB | 1 partおよび結合後object。それぞれの上限 |
| 5 MiB以上 | Completeに採用する最後以外のpart。最後のpartは0 bytesも可能 |
| 1〜10000 | PartNumber。同番号の再送は置換し、以前のETagは無効になる |
| 64 MiB | 全bucketのretained object versions、保存part、未import legacy objectのbody合計 |
| 10000 rows | 上記versions、delete markers、parts、未import objectの合計 |
| 1000 uploads | 全bucketのactive upload数。part未保存のuploadも数える |
| 1024 UTF-8 bytes | keyと作成時Content-Type、それぞれの上限 |

64 MiB/10000 rowsは通常PUTとも共有する論理保存quotaで、current object viewの
重複は数えない。upload metadata rowの1000件上限は別に評価する。
SQLiteファイル全体、プロセスメモリ全体、AWSの課金サイズの上限ではない。
part置換は旧partを解放してからquotaを計算し、拒否時は旧partを保持する。

Completeは指定partを番号順に結合し、開始時のContent-Typeで通常PUTと同じ
version/current処理を行う。versioning状態はComplete時点の値を使い、
Enabledでは新version、未設定では現在値の置換、Suspendedではnull版の置換となる。
既存numbered versionsの保持は[S3 versioningの契約](s3.md#保存と削除)に従う。
Locationは相対path `/{bucket}/{escaped-key}`で、実AWS endpoint URLではない。

upload削除、全partの解放、完成version作成、current view更新は同じimmediate
transactionに含まれる。Completeに指定しなかった保存partも成功時に削除する。
quota、XML/part検証、保存処理の失敗時は完成objectを部分的に残さず、既存objectと
upload/partsを保持する。Abortもuploadと全partを同じtransactionで削除し、
完成済みobject/versionには触れない。完了・中止済みIDでの再要求は
`NoSuchUpload`となり、同じ成功応答の再送ではない。応答を失った場合は一覧と
objectを確認してから再操作する。

active uploadが1件でも残るbucketは、partが0件でも`BucketNotEmpty`で削除を拒否する。
これは本Simulatorのローカル制約で、実AWSのbucket削除挙動と同等とはしない。
CloudFormationのDeleteStackも同じ判定を使い、失敗すると同じstack削除transactionで
先に行ったresource削除をrollbackする。不要なuploadは明示的にAbortする。
自動期限切れやlifecycleによる回収は行わない。

## ページ送りとregion

ListPartsはPartNumber昇順。`max-parts`は0〜1000、既定1000、
`part-number-marker`は0〜10000、既定0。markerより大きい番号を返す。
`IsTruncated=true`なら返された`NextPartNumberMarker`を次の要求へ渡す。
`max-parts=0`は空の一覧と`IsTruncated=false`を返す。

ListMultipartUploadsはkey昇順、同key内は開始時刻の古い順、同時刻はupload ID昇順。
InitiatedはUTC・小数点以下9桁固定で保存する。
`prefix`、`max-uploads`（0〜1000、既定1000）、`key-marker`、
`upload-id-marker`に対応する。key-markerだけならそのkey全体を飛ばす。
両markerを渡すと同keyの指定uploadより一覧順で後から再開する。
upload-id-markerはこのbucket・同keyの既存active uploadを指定する。
削除済み・別key・別bucketのupload IDは`InvalidArgument`となり、一覧を最初から取り直す。
`IsTruncated=true`なら`NextKeyMarker`と`NextUploadIdMarker`の両方を渡す。
prefix/key-markerは各1024 UTF-8 bytes以下、upload-id-markerは128 bytes以下。
upload-id-markerの単独指定は拒否する。`max-uploads=0`は空・非truncated。
delimiter、CommonPrefixes、encoding-typeによる一覧変換は未対応。
どちらの一覧も複数requestを跨ぐsnapshotを保証せず、変更後は再取得する。

6操作ともbucketのregionを要求する。`X-Simulator-Region`またはdummy署名の
S3 region scopeを使い、両方ある場合は一致が必要。無指定は`us-east-1`。
対応regionは`us-east-1`、`us-west-2`、`ap-northeast-1`、`eu-west-1`。
raw HTTPのbucket region不一致は400 `IllegalLocationConstraintException`と
`x-amz-bucket-region`を返す。botocoreはこのheaderを見て正しいregionで
自動再要求する場合があるため、SDKの最終成功だけでは最初の不一致要求が
受理されたとは判断しない。未対応region、service選択やscopeの不一致は
`InvalidArgument`。実AWS署名の検証や複数accountの隔離を追加するものではない。

HTTPで取得したbucketのregionと作成時刻を、保存transaction内でも照合する。
body受信待ちなどの間に同名bucketが削除・再作成された場合、同regionでも
作成時刻が変われば409 `ConditionalRequestConflict`、別regionになれば
400 `IllegalLocationConstraintException`で拒否し、新しいbucketへ要求を流用しない。
状態を更新してから再操作する。この検査はbucket置換の検出であり、未対応の
object条件headerを有効にするものではない。

## 主なエラーと未対応

| HTTP / S3 code | 条件 |
| --- | --- |
| 400 `InvalidArgument` | 不正なID形式、part番号、cursor、空body制約、region/service scopeなど |
| 400 `MalformedXML` | 不正・空のComplete文書、未知field、field textの64 bytes超過、bodyの1 MiB超過など |
| 400 `InvalidPart` | 未保存part、ETagの形式・保存値との不一致 |
| 400 `InvalidPartOrder` | Completeの番号が範囲外、重複または降順 |
| 400 `EntityTooSmall` | Completeの最後以外に5 MiB未満のpart |
| 400 `BadDigest` | 指定したContent-MD5がUploadPartまたはCompleteのbodyと不一致 |
| 403 `AccessDenied` | IAM拒否。token等のアクセス制御も別途適用 |
| 404 `NoSuchBucket` / `NoSuchUpload` | bucketなし、または指定bucket/key/idのactive uploadなし |
| 409 `BucketNotEmpty` | bucket削除時にobject、version、marker、active uploadが残る |
| 409 `ConditionalRequestConflict` | HTTPで取得したbucketが保存transactionまでの間に同regionで置換された |
| 413 `EntityTooLarge` | partまたは完成objectが8 MiB超過 |
| 501 `NotImplemented` | 未対応query/header/option |
| 503 `ServiceUnavailable` | combined保存quotaまたはactive upload数の上限 |
| 500 `InternalError` | 保存データの整合性・SQL等の内部失敗 |

UploadPartとCompleteは任意指定のbase64 `Content-MD5`を検証する。
multipart checksum extensions（`x-amz-checksum-*`、
`x-amz-sdk-checksum-algorithm`）、`if-*`条件header、UploadPartCopy、
copy-source、ACL、object tagging、任意metadata、storage class、暗号化/KMS/SSE-C、
expected-bucket-owner、request-payerは未対応。streaming署名/aws-chunked、
presigned URL、AWS session token、virtual-hosted-styleや実AWS規模のobjectも未対応。
作成時はContent-Type以外のobject metadataとしてCache-Control、
Content-Disposition、Content-Encoding、Content-Language、Expiresを指定すると
501 `NotImplemented`を返す。
未知fieldを黙って保存したり暗号化済み扱いにしたりしない。
bucket policy、暗号化設定、lifecycle、replication、Object Lock、access pointsなど
Issue51に含まれる他のS3拡張は、このmultipart対応によって有効にはならない。

## 操作画面と検証範囲

S3 bucketのManagementからkeyを指定して開始し、一覧のuploadを選択する。
番号とpartファイルを指定して1 partずつSave partし、保存済み番号・size・ETagを確認する。
画面は大きな1ファイルを自動分割しない。別uploadの選択・開始、part保存の成功後は
ファイル選択を消し、番号を1へ戻す。次の番号とpartファイルを明示的に選ぶ。
Completeはpart一覧を再取得し、表示済みの番号・size・ETagと比較する。
SDK等による変更があれば一覧を更新して確認文字列を消し、再確認を求める。
一致する全partを番号順に送信するため、一部だけを選んで結合するにはSDK/APIを使う。
確認欄へ`complete`を入力して結合、`abort`を入力して全partを破棄する。
再取得後の並行変更まで予約・lockする機能ではなく、最後はnative APIの検証に従う。
成功表示には応答から得たupload ID、part/完成ETag、完成version ID（ある場合）を表示し、
言語を切り替えてもその値を保持する。

再起動や画面を開き直した後はRefresh uploadsでuploadを選び直せる。
未送信のファイル選択は保存されないため、必要なpartファイルを選び直す。
中断した変更は再試行前に一覧を更新する。単独の`.`/`..` keyはbrowserのpath処理を
避けるため画面では拒否し、SDKを使う。画面のAPIエラーも実HTTP応答に基づく。
Cloudscapeを使う独立Simulatorの画面であり、認証済み実AWSコンソールの観察や
全操作・pixel一致の検証は行っていない。

回帰の対象はnative store/HTTP、実binaryを使うSDK/browser、再起動・別DB/tokenの
分離、保存失敗時rollbackとquota、有限multipart状態モデルである。
検証入口は`make before-commit`、`make e2e`、`TLA_JAR=... make formal`。
モデルは有限状態の抽象化で、Go/SQL/XML/IAMや実AWS互換性全体の形式証明ではない。
本資料は現在HEADの全gate、coverage、モデル、hosted CIの成功を宣言するものではない。
結果は対象Issue・PRと対象commitのCIを確認する。
[開発時の参照先](development-status.md)から辿れる。
