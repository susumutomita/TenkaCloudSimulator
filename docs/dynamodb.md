# DynamoDB サブセット仕様

公式DynamoDB JSON API（`DynamoDB_20120810`、JSON 1.0）の基本操作だけを、リージョン別のroot POST + `X-Amz-Target: DynamoDB_20120810.<操作>` で実装する。状態はAWS層所有のSQLiteテーブル `aws_dynamodb_tables` / `aws_dynamodb_items` に保存し、`--state` 指定時は再起動後も保持される。署名は検証せず、ダミー `TCSIM…` 資格情報か無署名のみ許可する（他サービスと同じ境界）。

公式仕様の参照: [CreateTable](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_CreateTable.html) / [PutItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_PutItem.html) / [GetItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_GetItem.html) / [DeleteItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DeleteItem.html) / [エラー一覧](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/CommonErrors.html)

JSONのフィールド名は大文字・小文字を区別し、同名フィールドの重複、JSON null、不正UTF-8、未対応のネストしたフィールドを拒否する。DynamoDBのNULL値は `{"NULL":true}` で指定する。Content-Typeは `application/x-amz-json-1.0`、署名サービスは `dynamodb` とし、サービス／リージョン指定の矛盾を受け付けない。署名の検証を追加するものではなく、引き続きローカルアクセストークンが認証を担当する。

送信用JSONは1 MiB、構造上のネストは128段までとし、属性値の32段制限とは別に検査する。保存項目の400 KiB上限も独立して適用するため、エスケープによって大きくなったJSONは保存サイズ上限に達する前に拒否される場合がある。

CreateTableではHASHとRANGEに同じ属性名を使用できず、AttributeDefinitionsは相異なるキー属性と一致する必要がある。不正なschemaは保存前に拒否するため、失敗した名前で正しいCreateTableを再実行できる。明示された空のBillingMode・ConditionExpression・ExclusiveStartTableName、およびLimit=0を、省略時の既定値として扱わない。

## 対応操作

| 操作 | 内容 |
| --- | --- |
| CreateTable | HASH 1つ + RANGE 0/1つ。キー属性は `S`/`N`/`B` のみ。AttributeDefinitions はキー属性ちょうどと一致すること（非キー属性の定義は ValidationException）。BillingMode は `PAY_PER_REQUEST`、または ProvisionedThroughput（各1以上）付きの `PROVISIONED` |
| ListTables | 名前順。`Limit`（1–100）と `ExclusiveStartTableName` によるページング、`LastEvaluatedTableName` を返す |
| DescribeTable | KeySchema/AttributeDefinitions/BillingModeSummary/ProvisionedThroughput に加え、即時の ItemCount と semantic size 合計の TableSizeBytes（下記制限あり） |
| DeleteTable | テーブルと全項目を同一トランザクションで削除。応答の TableStatus は `DELETING` |
| PutItem | 属性値 `S/N/B/BOOL/NULL/SS/NS/BS/L/M` を再帰検証（深さ32まで）。項目サイズ上限409600 bytes（数値のサイズは下記の推定式）。数値キーは正規化するので `2` と `2.0` は同一キー |
| GetItem | キー完全一致。存在しない項目はエラーではなく空応答。キーの型不一致・不足・余分な属性は ValidationException |
| DeleteItem | 存在しない項目の削除も成功（実DynamoDBと同じ） |

ConditionExpression は `attribute_not_exists(<パーティションキー>)` と `attribute_exists(<パーティションキー>)` の2形式だけを評価し、失敗時は `ConditionalCheckFailedException`。それ以外の式は黙って成功させず ValidationException で明示する。

String/Binaryのパーティションキーは2048 bytes、ソートキーは1024 bytesまで。StringはUTF-8、BinaryはBase64復号後の長さで判定し、上限ちょうどを受け付ける。NSの重複は正規化した数値、BSの重複は復号したバイト列で判定する。例えばNSの `1` と `1.0` は重複として拒否し、SSの `"1"` と `"1.0"` は別の文字列として保持する。有効な値の保存表現を書き換えず、失敗した書込は既存項目・統計を変更しない。以前受け付けた範囲外のキー・重複値setを自動修復・削除することはなく、現在の検証範囲外のデータとして扱う。参照: [AWSのデータ型・キー長・setの一意性](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.NamingRulesDataTypes.html)。

## エラー

- `ResourceNotFoundException`: 存在しないテーブルへの操作
- `ResourceInUseException`: 同名テーブルの再作成
- `ValidationException`: 名前（3–255文字）、キースキーマ、キー型、属性値、不正な数値・Base64、未対応フィールドなど
- `ConditionalCheckFailedException`: 条件不一致
- `LimitExceededException`: シミュレーター上限（テーブル100/プロセス、項目10000/テーブル）
- `UnknownOperationException`（HTTP 501）: 未実装操作
- `AccessDeniedException`（HTTP 403）: IAM評価の拒否、または実AWS形式の資格情報

認可は他サービスと同じく identity policy で `dynamodb:<操作>` と `arn:aws:dynamodb:<region>:000000000000:table/<名前>`（ListTables は `*`）を評価する。リージョンは us-east-1 / us-west-2 / ap-northeast-1 / eu-west-1 で、テーブルはリージョン別名前空間。

## 未対応（明示エラーまたは未文書化）

- Query / Scan / UpdateItem / BatchWriteItem / BatchGetItem / PartiQL
- グローバルセカンダリインデックス / ローカルセカンダリインデックス、Streams、TTL、Point-in-time recovery、タグ、テーブルクラス
- ConditionExpression の上記2形式以外、ProjectionExpression、ReturnValues、ReturnConsumedCapacity、式属性名・値
- ProvisionedThroughput の実スロットリング（数値は保存・表示のみ）、オートスケーリング、容量消費量の計算
- 項目の順序保証、強整合性/結果整合性の区別（ConsistentRead は受付のみで常に保存済み状態を読む）
- CloudFormation の `AWS::DynamoDB::Table`、Lambda 実行ロールSDKブリッジからの DynamoDB アクセス

## 数値の範囲と互換性（2026-11-04）

数値は符号付き10進数（小数点、e/E指数を含む）として整数・文字列演算で正規化し、二進浮動小数点へ変換しない。有効数字は先頭・末尾のゼロを除き38桁まで。ゼロ、および絶対値 1E-130 以上 9.9999999999999999999999999999999999999E+125 以下を受け付ける。指数部はゼロを除いて9桁までという実装上の制限がある。

旧実装の `big.Rat` が受け付けた分数、16進数、範囲外・精度超過の数値は新規操作で ValidationException となる。既存の有効な10進数の canonical key は保持し、保存した AttributeValue の原文を変更しない。DB migration、既存データの削除・自動修復は行わない。旧実装で保存された範囲外の数値は未対応の既存データとして扱う。

参照（2026-11-04確認）: [AWS DynamoDB constraints](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Constraints.html)。全てのAWS数値パーサーの字句互換を実証したものではない。

## 項目サイズとテーブル統計（2026-11-04）

PutItem の上限判定と DescribeTable の TableSizeBytes は同じ計算を使う。属性名と文字列はUTF-8のバイト数、B/BSはBase64復号後のバイト数、BOOL/NULLは値1 byte。SS/BS/NSは各値の和、L/Mはコンテナ3 bytesと各要素1 byteを加え、M内の属性名も含める。JSONの引用符・エスケープ・Base64の転送用膨張は含めない。409600 bytesちょうどは受付け、超過した更新は既存の項目・統計を変更せず拒否する。

数値とNSは先頭・末尾のゼロを除いた有効数字をdとして `ceil(d/2)+1` bytes（ゼロは2 bytes）と推定する。AWS自身が概算として公開する式を用いたもので、負数・ゼロを含む非公開の内部エンコードや数値を含む400KiB付近の完全互換は保証しない。料金計算、容量消費、100 bytesの項目別ストレージ課金オーバーヘッドは実装していない。

TableSizeBytes は上記の項目サイズの即時合計であり、SQLiteファイル・JSON文字数や課金ストレージ容量ではない。AWSの統計は約6時間ごとに更新されるため、この更新タイミングも異なる。再起動後は保存JSONを再計算する。JSON破損や現在の検証範囲外の旧数値がある場合は、架空のサイズを返さず InternalServerError とする。自動migration・データ削除は行わない。

参照（2026-11-04確認）: [AWS item sizes and formats](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/CapacityUnitCalculations.html)、[AWS limits](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Constraints.html)、[TableDescription](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TableDescription.html)。

開発者用の任意SDK検証は、既にboto3をインストール済みのPythonへのパスを `DYNAMODB_SIZE_SDK_PYTHON` に指定し、`go test ./internal/aws -run '^TestDynamoDBSemanticSizeSDKHTTP$' -v` で実行できる。実際のloopback HTTPとダミー資格情報を使用し、AWSへ接続しない。Python/boto3は製品バイナリの実行時依存ではない。通常のサイズ・永続化・境界テストにPythonは不要。

## 世代と条件更新

ネイティブの項目操作は同一トランザクション内で TableId とキー schema を再照合する。
古い snapshot による削除済み・再作成済みテーブルへの操作を拒否する。
条件不一致・拒否・失敗時は既存項目と統計を維持し、テーブル削除は項目と同時に行う。
既存 orphan 項目は起動時に拒否し、自動 migration・旧 DB の削除や修復は行わない。
これらのネイティブ保証と、下記の画面側の非同期読み取り制御は別の検証範囲である。

## Cloudscape コンソール（この開発 branch の追加機能）

AWS の Services で DynamoDB を選ぶと Tables が開く。日本語では
サービス検索に「ダイナモDB」「テーブル」「項目」も使える。
デフォルト認証と既存のブラウザーセッションを利用し、SDK と同じ SQLite を操作する。
参加者の IAM 拒否を画面操作で回避しない。チーム分離には既存の別プロセス・別 DB・別 token
の境界が必要で、画面上の Principal 選択自体は認証・チーム分離ではない。

- Tables: ページごとの一覧・検索、前後ページ、名前の完全一致による詳細表示。
- Create table: 名前、partition key と任意の sort key、各 S/N/B 型。
  画面では on-demand (PAY_PER_REQUEST) のみ作成する。容量課金・スロットリングを再現しない。
- Table overview: 実際の TableId、schema、状態、項目数と論理サイズ。
- Explore table items: 完全な partition/sort key による GetItem。
  Query/Scan は未対応なので項目一覧や架空の検索結果を表示しない。
- Create item: DynamoDB AttributeValue JSON。既定は attribute_not_exists、
  exists 条件または無条件の全置換も明示して選択できる。
  数値は文字列、バイナリは Base64。重複属性名は生の JSON をネイティブ検証へ渡して拒否する。
- Delete item: 取得済みの正確なキーを表示し、delete の入力で確認する。
  キー変更後に以前の結果や削除対象を再利用しない。
- Delete table: 対象テーブル名を入力して確認する。ローカル項目も直ちに削除される。

画面は TableId を読み取り前後で検証し、不一致時は表示を消して再選択を求める。
ただし公式 API の変更要求は TableName を使うため、事前検証と送信の間の再作成を
原子的に防ぐ UI 用 TableId-CAS を提供するものではない。
リージョン・Principal・ナビゲーションが変わるとコンポーネントを再生成して旧表示・下書きを消す。
言語変更では入力を保つ。書き込み ACK 後はフォームを閉じてから独立した再読み取りを行い、
その失敗時の Retry read は読み取りだけを再試行する。書き込みの自動再送はない。
Cancel は古い読み取りの公開と読み込み表示を無効にするが、既にサーバーへ到達した変更の
ロールバックは保証しない。

実装は公式 Cloudscape 部品を利用した独立したローカル画面であり、認証済み AWS コンソールを
観測したものでも、完全な外観・全サービス互換や実 AWS との差分検証を示すものでもない。
公開一次資料として [テーブル作成](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/getting-started-step-1.html)、
[項目作成](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/getting-started-step-2.html)、
[GetItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_GetItem.html)
を2026-11-05に確認した。資料の最終改訂日は未確認。

### 検証範囲

この画面の focused 検証では実バイナリ・2 CLI/DB/token と実 SDK を使用し、
S/N/B、PK/SK、PC1440px/390px、英日、権限拒否、リージョン・Principal 切替、
条件・重複属性拒否、Cancel、ACK 後の再読み取り失敗と無再送を確認した。
4 フローで SDK90送受信、28画面の axe/reflow 検査違反0、外部通信0、ページ例外0。
初回の390pxリージョン選択は Cloudscape More 内の実導線と異なるテスト selector で失敗し、
失敗記録を保全して実際のメニュー操作へ修正した。PC結果をスマートフォン成功に流用していない。

DynamoDBConsoleEpoch は独立した有限 UI 状態モデルで、scoped baseline3,832状態と
10個の意味的変異制御が成功した。JS/Go/SQL の refinement proof ではない。
この branch では正式な34モデル/207制御の登録と必須ブラウザー E2E を追加したが、
現 HEAD の全241形式ケース・51 E2E・全体両90%品質ゲート・hosted CI は別途必要であり、
focused の成功をもって全体検証済みとはしない。
