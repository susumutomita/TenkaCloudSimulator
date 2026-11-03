# Lambdaのローカル実行

LambdaのPython 3.12関数を、埋込CPython 3.12.16/WASIで実際に実行する。S3/SSMと同じGoバイナリ・SQLite・local access tokenを使い、外部Python、Docker、Node、クラウド接続は実行時に不要。

この段階では既存wp2shell問題の完走を意味しない。LambdaのCloudFormation、API Gateway/WAF、関数内SDK/HTTP接続は未対応。問題の選択は引き続きhello-worldのみ。[対応順](design/0002-catalog-workloads.md)。

## 対応API

| API | native path / method | IAM |
| --- | --- | --- |
| CreateFunction | POST /2015-03-31/functions | lambda:CreateFunction + iam:PassRole |
| ListFunctions | GET /2015-03-31/functions | lambda:ListFunctions on * |
| GetFunctionConfiguration | GET /2015-03-31/functions/{name}/configuration | lambda:GetFunctionConfiguration |
| UpdateFunctionCode | PUT /2015-03-31/functions/{name}/code | lambda:UpdateFunctionCode |
| Invoke | POST /2015-03-31/functions/{name}/invocations | lambda:InvokeFunction |
| DeleteFunction | DELETE /2015-03-31/functions/{name} | lambda:DeleteFunction |

関数ARNはAWS形式の `arn:aws:lambda:<region>:000000000000:function:<name>`。native nameまたは同一region/accountの非修飾ARNを使用する。各関数の実ARNを評価し、Create/PassRoleは同一policy snapshotを使う。IAM trust/AssumeRoleの再現ではない。execution roleは既存local identityのみで、権限のないサービス接続を自動追加しない。

SDKは同一endpointのdummy TCSIM署名scopeでlambdaへ振り分ける。unsigned requestは `X-Simulator-Service: lambda`、`X-Simulator-Region`を明示する。S3 object keyとLambda URLの衝突を避け、通常のS3リクエストは従来のデータにアクセスする。認証は通常の`X-Simulator-Token`またはブラウザcookie。実AWSキー/session tokenは拒否する。

InvokeはRequestResponseとDryRunのみ。Python例外はHTTP200 + `X-Amz-Function-Error: Unhandled`でJSONのerrorType/errorMessageを返す。サービスの認可・入力・保存エラーは適切なHTTPエラー。ZIP更新はRevisionIdの楽観lockを実装し、削除後に同名関数が再作成されても古いrole/environmentを復活させない。

空のInvoke bodyは`{}`として扱う。Create時のTagsは保存し、追加のlambda:TagResourceを要求する。Tagsの一覧/変更APIやtag条件のIAM評価は未対応。現在のcontextはfunction_name/function_version/invoked_function_arnのみ。

## 制限と実行境界

- 4 region、100 functions/process、ZIP合計8 MiB。ZIP 256 KiB、展開1 MiB/64 entries、regular Python sourceのみ。危険なpath、symlink、重複、破損ZIP、展開量超過を拒否する。
- MemorySize128 MiBのWASM linear memory、実行1–30秒、同時2 sandbox、event/result/stdout/stderr各256 KiB、収集log64 KiB。queue待ちも実行期限に含む。Go runtime/JIT/embedded assets等を含むOS process全体のRSS上限ではない。
- 初回は固定interpreterをGo内でcompileする。待機は関数の実行時間とは別で、最大2分またはrequest/終了cancelまで。固定モジュールのJIT自体は終了後に後処理する場合があるが、終了処理は待たず、閉じたstoreへruntimeを公開しない。guestは終了後に開始しない。通常の実行ごとに独立したcold sandboxを作り、module globalsや/tmpのwarm保持は実装しない。
- ホストfilesystem、process作成、socket、ホストenvironmentを渡さない。読取専用のstdlibと当該ZIPだけを提供する。日時は実時計、sleepは期限で中断できる相対時刻待機。外部native extension、ctypes/SSL、pip install、boto3、任意networkは利用不可。
- Versions/aliases、async/Event、log tail/CloudWatch、client context、layers/images、VPC、KMS、resource policy/Function URL、source-download URL、UpdateFunctionConfiguration、GetFunction等は未対応。未知fieldsを黙って受理しない。

CPythonは変更していない。WASIの絶対時刻poll非対応に合わせ、configureのclock_nanosleep検出を無効にして標準nanosleep fallbackを使用する。stdlibは517ファイルの選択subset。原文license、ソースcommentの権利表示、WASI libc/LLVM/SQLiteの通知と固定commit/hashを同梱する。[再構築](../tools/python/README.md)。

## Cloudscape画面と検証

Lambda→Functions→Create function（Author from scratch、Python 3.12、既存local role）→Test。Code tabからZIPをDeployし、同じnative API/SDKで読戻す。Configurationは実設定を表示する。Invokeのみ許可された参加者には、一覧/configurationを許可せず、hostから渡された名前のTestを用意する。inline code editor、monitoring、role自動作成等は未再現。実AWSログイン済み画面とのpixel一致は未検証。

実バイナリを空cwd/PATHで起動するE2Eは、ブラウザ作成/実行/ZIP更新/削除、SDKのcode hash/readback、参加者Invoke/metadata拒否、別processの同名関数とtoken分離を検証する。Go race testsはSQL/hashの実Python実行、host capability拒否、deadline/heap/output bounds、concurrent cold isolation、SQLite失敗rollback/破損fail-closedを検証する。

一次資料: [CreateFunction](https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunction.html)、[Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html)、[ListFunctions](https://docs.aws.amazon.com/lambda/latest/api/API_ListFunctions.html)、[environment variables](https://docs.aws.amazon.com/lambda/latest/dg/configuration-envvars.html)。対応部分と未対応部分を区別し、AWS全体との互換性を主張しない。
