# Lambdaのローカル実行

LambdaのPython 3.12関数を、埋込CPython 3.12.16/WASIで実際に実行する。S3/SSMと同じGoバイナリ・SQLite・local access tokenを使い、外部Python、Docker、Node、クラウド接続は実行時に不要。

この段階では既存wp2shell問題の完走を意味しない。Lambdaの限定CloudFormationに対応するが、API GatewayのRegional REST→Lambda proxyと対応CFNリソースを利用できる。WAF、関数内HTTP接続はこのmainの対応範囲外。関数内SDKは下記SSM/S3の限定subset。問題の選択は引き続きhello-worldのみ。[対応順](design/0002-catalog-workloads.md)。

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

SDKは同一endpointのdummy TCSIM署名scopeでlambdaへ振り分ける。unsigned requestは `X-Simulator-Service: lambda`、`X-Simulator-Region`を明示する。S3 object keyとLambda URLの衝突を避け、通常のS3リクエストは従来のデータにアクセスする。認証は通常の`X-Simulator-Token`またはブラウザcookie。`TCSIMOPERATOR`や引き受けたロールのsession資格情報によるSigV4署名も使える（[native SDK / STS](sts-native.md)）。その場合は関数名だけのpath、query stringなし、本文384KiBまで。sessionの認可はロールのpolicy snapshotで行う。関数内のSDK呼び出しは常に保存済みexecution roleで評価し、呼び出し元sessionの権限・境界を引き継がない。SSM parameterに限定したconsole sessionからはLambdaを使えない。実AWSキーと、検証できないsession tokenは拒否する。

InvokeはRequestResponseとDryRunのみ。Python例外はHTTP200 + `X-Amz-Function-Error: Unhandled`でJSONのerrorType/errorMessageを返す。サービスの認可・入力・保存エラーは適切なHTTPエラー。ZIP更新はRevisionIdの楽観lockを実装し、削除後に同名関数が再作成されても古いrole/environmentを復活させない。

同期Invokeで明示的に`X-Amz-Log-Type: Tail`を指定すると、`X-Amz-Log-Result`に実際に捕捉したguest出力の末尾最大4096 bytesをbase64で返す。省略または`None`では返さない。IAMは同じ`lambda:InvokeFunction`で、metadata読取権限やCloudWatch権限を付与しない。DryRunでのTail、async/Event、client contextは未対応として拒否する。ログに機密値が含まれる可能性があるため、要求・表示は明示的に行う。

Tailは従来の先頭64 KiBの内部Logsとは独立し、内部SDK交換・result frameを除外する。stdoutの後にstderrを連結するため、両者の時系列順は再現しない。UTF-8文字の途中から始まる場合があり、表示側は不正な先頭bytesを置換して復号する。関数例外・timeout・sandbox失敗時も上限内で捕捉済みの出力のみを返す。guest内の未flush出力は回収できず、開始前の失敗には出力がない。AWSのSTART/END/REPORT行の生成、永続ログ保存、CloudWatch Logsへの自動配送は行わない。

空のInvoke bodyは`{}`として扱う。Create時のTagsは保存し、追加のlambda:TagResourceを要求する。Tagsの一覧/変更APIやtag条件のIAM評価は未対応。現在のcontextはfunction_name/function_version/invoked_function_arnのみ。

## 制限と実行境界

- 4 region、100 functions/process、ZIP合計8 MiB。ZIP 256 KiB、展開1 MiB/64 entries、regular Python sourceのみ。危険なpath、symlink、重複、破損ZIP、展開量超過を拒否する。
- MemorySize128 MiBのWASM linear memory、実行1–30秒、同時2 sandbox、event/result/stdout/stderr各256 KiB、内部Logsは先頭64 KiB、明示要求のTailは末尾4096 bytes。queue待ちも実行期限に含む。Go runtime/JIT/embedded assets等を含むOS process全体のRSS上限ではない。
- 初回は固定interpreterをGo内でcompileする。待機は関数の実行時間とは別で、最大2分またはrequest/終了cancelまで。固定モジュールのJIT自体は終了後に後処理する場合があるが、終了処理は待たず、閉じたstoreへruntimeを公開しない。guestは終了後に開始しない。通常の実行ごとに独立したcold sandboxを作り、module globalsや/tmpのwarm保持は実装しない。
- ホストfilesystem、process作成、socket、ホストenvironmentを渡さない。読取専用のstdlib・当該ZIPと固定のlocal SDK shimを提供する。日時は実時計、sleepは期限で中断できる相対時刻待機。外部native extension、ctypes/SSL、pip install、upstream boto3 package、任意networkは利用不可。
- Versions/aliases、async/Event、CloudWatch Logsへの自動配送、client context、layers/images、VPC、KMS、resource policy/Function URL、source-download URL、UpdateFunctionConfiguration、GetFunction等は未対応。未知fieldsを黙って受理しない。

CPythonは変更していない。WASIの絶対時刻poll非対応に合わせ、configureのclock_nanosleep検出を無効にして標準nanosleep fallbackを使用する。stdlibは517ファイルの選択subset。原文license、ソースcommentの権利表示、WASI libc/LLVM/SQLiteの通知と固定commit/hashを同梱する。[再構築](../tools/python/README.md)。

## Cloudscape画面と検証

Lambda→Functions→Create function（Author from scratch、Python 3.12、既存local role）→CodeMirrorのPython編集→作成/Deploy→Test。Services→IAM→Roles→Create roleでローカルroleを作り、LambdaのPermissions→Existing roleから選べる。初期policyは明示Denyでservice権限なし。Code tabからZIPをDeployし、同じnative API/SDKで読戻す。Configurationは実設定を表示する。Invokeのみ許可された参加者には、一覧/configurationを許可せず、hostから渡された名前のTestを用意する。CodeMirror（同梱エディタ。VS Code/Code-OSSそのものではない）で保存済みPython関数のハンドラーファイルを編集できる。作成時は入力したソースをZIP化してnative APIへ送信し、編集後はRevisionId付きDeployが成功するまでTestを無効化する。既存関数やCFNの`index.py`も、再読込後に保存済みZIPから読み出す。デプロイはハンドラーファイルのみ更新し、他のZIP entryと関数設定を保持する。未保存の下書きはメモリのみで、再読込/他サービスへの移動前にはダウンロードが必要。破損ZIPや非UTF-8ソースなどは明示エラーとし、完全なZIP更新も利用できる。

ローカル独自API `GET/PUT /_simulator/v1/lambda/{name}/source` を利用する（AWS native GetFunctionではない）。通常のlocal token/cookie認証を必須とし、GETは`lambda:GetFunction`、PUTはそれに加えて`lambda:UpdateFunctionCode`を実関数ARNに要求する。PUTは`Source`と必須`RevisionId`だけを受理し、編集パスは保存済みHandlerから決める。古いrevision・削除後同名再作成への書込は拒否する。署名SDK向けの新操作としては公開しない。

これはCode-OSS全体や拡張機能/ターミナルではない。monitoring、role自動作成等は未再現。実AWSログイン済み画面とのpixel一致は未検証。

Test の「このテストのログ末尾を取得」を明示的に選ぶと、その同期呼び出しの実出力を表示する。既定では要求しない。選択解除、入力変更、新しい呼び出し、関数やサービスの移動で以前の表示を消し、遅れて返った別の呼び出し結果を破棄する。中断は表示と待機を取り消すもので、既に実行した関数の副作用を巻き戻す保証はない。CloudWatch の永続履歴や Monitor tab の対応を意味しない。

実バイナリを空cwd/PATHで起動するE2Eは、ブラウザ作成/実行/ZIP更新/削除、SDKのcode hash/readback、参加者Invoke/metadata拒否、別processの同名関数とtoken分離を検証する。Go race testsはSQL/hashの実Python実行、host capability拒否、deadline/heap/output bounds、concurrent cold isolation、SQLite失敗rollback/破損fail-closedを検証する。

一次資料: [CreateFunction](https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunction.html)、[Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html)、[ListFunctions](https://docs.aws.amazon.com/lambda/latest/api/API_ListFunctions.html)、[environment variables](https://docs.aws.amazon.com/lambda/latest/dg/configuration-envvars.html)。対応部分と未対応部分を区別し、AWS全体との互換性を主張しない。

関数が改行なしでstdoutへログを書いても、SDK通信frameは独立した行へ分離する。`VersionId`を明示した場合は空文字もnative S3へそのまま検証させる。空のversion selectorは通常のGET/DELETEへ置き換えずエラーとなり、現行objectを変更しない。

## CloudFormationからの作成

AWS::Lambda::FunctionのPython3.12/Code.ZipFile inline sourceに対応する。handlerはindex.function、memory128MiB、timeout1–30秒。FunctionName/Description/Role/Environment.Variables/TagsとRef/Arnを扱い、実ZIPをnative Lambdaと同じSQLiteへ保存する。S3 package/image/layers/architecture選択/versions/alias、UpdateStackは未対応。保存済みphysical IDを改名しない。

CFN呼出者はStack操作に加えlambda:CreateFunction、実行roleのiam:PassRole、tag付きならlambda:TagResourceを必要とする。削除はlambda:DeleteFunctionを必要とする。execution roleは既存local identityまたは同stackで先行作成されたroleで、managed AWSLambdaBasicExecutionRole ARNはmetadataのみ。CloudWatchへの実通信や権限追加はない。関数内SDK bridgeは下記の許可操作に限定する。

DependsOnのlogical ID文字列/配列とRef/GetAtt/Subを統合した順序で作成し、依存元から逆順に削除する。未知ID/型/cycleは変更前に拒否。function/code/role/stack/ownershipは単一transactionで保存・削除し、失敗時は全体rollback。Lambdaのclaim namespaceはlambda:<region>とし、未対応の古いreaderがrole fallbackで誤削除できないようにする。SQLiteを古い実行ファイルと共有・downgradeする運用はサポートしない。

[公式Lambda CFN](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-lambda-function.html)、[DependsOnと削除順](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-attribute-dependson.html)を参照した限定実装。既存問題の実採点完了を意味しない。

## 関数内の限定ローカルSDK

`import boto3`互換の小さな独自shimをAWS layerから読取専用で渡す。upstream boto3/botocore全体の同梱ではなく、`client("ssm").get_parameter/get_parameters`、`client("s3").get_object/put_object/delete_object`だけに対応。S3 Bodyはbytes/string、GetObjectのBody.read()はbytes。S3 object bodyは32KiBまで。native SDK/HTTPの8MiB object上限は変えない。GetObjectは過大bodyを切り詰めずエラーにする。

API呼出しはsocketを開かず、保存済みexecution roleと関数regionに固定したhost closureからnative SSM/S3 handlerへ渡す。IAM・quota・SQLite transaction・version IDは通常SDKと共通。Invokeした参加者のidentityは実行roleに置き換えない。参加者の直接SSMを拒否しても、関数は自身のroleで許可されたSSMを読める。roleの明示Denyは次回の実操作を拒否する。

role policy・SSM読取・S3 object処理のDB接続待ちとqueryにもinvocation contextを伝える。接続待ち中にcancelされた操作は接続の解放を待たず終了し、後からPutObject/DeleteObjectを開始しない。回帰テストは唯一の接続を保持し、5操作それぞれの待機を確認してからcancelし、解放前の終了と保存済みobjectの維持を検証する。

任意service/operation、endpoint/credential/identity/header指定、別regionを拒否する。ネットワーク権限やtokenはguestに渡さない。互換ClientErrorはError.Code/MessageとHTTPStatusCodeを持つ。resource policy、STS、managed/trust policy、HTTP/API Gateway、paginator/session、upstream SDK全機能は未対応。

汎用runtimeはAWSを知らず、trusted providerが明示したcontext-bound capabilityと固定FSだけを受ける。JSON message64KiB、128calls、request+response合計1MiB、未読response1件まで。native response bufferはencoding前48KiBまでで、SSMの複数値やJSON escapeでも上限を超えればEntityTooLargeを返し、部分的な値を成功扱いしない。S3 object bodyの32KiB上限とは別の制限。read/host callbackはinvocation contextで終了し、Closeはactive/queued invocationをcancelする。native SQLite操作の完了とtimeoutが競合した場合、応答だけで副作用の有無を断定せずnative APIで状態を確認する。RPC本文をruntime logに混ぜない。これらはlocal simulatorの範囲で、実AWS全体の互換性を意味しない。
