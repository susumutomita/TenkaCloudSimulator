# DynamoDB サブセット仕様

公式DynamoDB JSON API（`DynamoDB_20120810`、JSON 1.0）の基本操作だけを、リージョン別のroot POST + `X-Amz-Target: DynamoDB_20120810.<操作>` で実装する。状態はAWS層所有のSQLiteテーブル `aws_dynamodb_tables` / `aws_dynamodb_items` に保存し、`--state` 指定時は再起動後も保持される。署名は検証せず、ダミー `TCSIM…` 資格情報か無署名のみ許可する（他サービスと同じ境界）。

公式仕様の参照: [CreateTable](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_CreateTable.html) / [PutItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_PutItem.html) / [GetItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_GetItem.html) / [DeleteItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DeleteItem.html) / [エラー一覧](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/CommonErrors.html)

## 対応操作

| 操作 | 内容 |
| --- | --- |
| CreateTable | HASH 1つ + RANGE 0/1つ。キー属性は `S`/`N`/`B` のみ。AttributeDefinitions はキー属性ちょうどと一致すること（非キー属性の定義は ValidationException）。BillingMode は `PAY_PER_REQUEST`、または ProvisionedThroughput（各1以上）付きの `PROVISIONED` |
| ListTables | 名前順。`Limit`（1–100）と `ExclusiveStartTableName` によるページング、`LastEvaluatedTableName` を返す |
| DescribeTable | KeySchema/AttributeDefinitions/BillingModeSummary/ProvisionedThroughput に加え、実測の ItemCount と TableSizeBytes |
| DeleteTable | テーブルと全項目を同一トランザクションで削除。応答の TableStatus は `DELETING` |
| PutItem | 属性値 `S/N/B/BOOL/NULL/SS/NS/BS/L/M` を再帰検証（深さ32まで）。項目サイズ上限400KB（実DynamoDBと同じ）。数値キーは正規化するので `2` と `2.0` は同一キー |
| GetItem | キー完全一致。存在しない項目はエラーではなく空応答。キーの型不一致・不足・余分な属性は ValidationException |
| DeleteItem | 存在しない項目の削除も成功（実DynamoDBと同じ） |

ConditionExpression は `attribute_not_exists(<パーティションキー>)` と `attribute_exists(<パーティションキー>)` の2形式だけを評価し、失敗時は `ConditionalCheckFailedException`。それ以外の式は黙って成功させず ValidationException で明示する。

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
- CloudFormation の `AWS::DynamoDB::Table`、Lambda 実行ロールSDKブリッジからの DynamoDB アクセス、Cloudscape コンソール画面

## 数値の範囲と互換性（2026-11-04）

数値は符号付き10進数（小数点、e/E指数を含む）として整数・文字列演算で正規化し、二進浮動小数点へ変換しない。有効数字は先頭・末尾のゼロを除き38桁まで。ゼロ、および絶対値 1E-130 以上 9.9999999999999999999999999999999999999E+125 以下を受け付ける。指数部はゼロを除いて9桁までという実装上の制限がある。

旧実装の `big.Rat` が受け付けた分数、16進数、範囲外・精度超過の数値は新規操作で ValidationException となる。既存の有効な10進数の canonical key は保持し、保存した AttributeValue の原文を変更しない。DB migration、既存データの削除・自動修復は行わない。旧実装で保存された範囲外の数値は未対応の既存データとして扱う。

参照（2026-11-04確認）: [AWS DynamoDB constraints](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Constraints.html)。全てのAWS数値パーサーの字句互換を実証したものではない。
