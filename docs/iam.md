# IAM roles と実行権限

IAM Query/XMLの9操作を実装する。`CreateRole`、`GetRole`、`ListRoles`、`DeleteRole`、`PutRolePolicy`、`GetRolePolicy`、`DeleteRolePolicy`、`ListRolePolicies`、`UpdateAssumeRolePolicy`。root POST、Version `2010-05-08`、`X-Simulator-Service: iam`でルーティングする。ダミーTCSIM署名のSDKはIAM scopeで自動振分け。実AWS資格情報・session tokenは使わない。認証はCLIのlocal tokenで行う。

ScalarにはIAM roles専用のPOST `/_simulator/providers/aws/` があり、IAM headerを初期入力した9操作の例を選べる。新しい状態や別APIではなく既存AWS provider aliasで、root POSTと同一処理。従来root POSTの操作例も保持する。例そのものをHTTP送信するテストで、作成・読取・一覧・更新・削除の全9操作を検証する。boto3はURL-encoded policyを辞書へ復元する。ネイティブSDK、コンソール、Lambdaは同じSQLiteのロール・inline policyを共有する。

## コンソール

Services→IAM→Roles→Create role。公式Cloudscape WizardでSelect trusted entity→Add permissions→Name, review, and createへ進む。AWS serviceのLambdaを選ぶと`lambda.amazonaws.com`を信頼する。Custom trust policyは限定service-principal JSONを受け付ける。作成直後はservice権限がなくimplicit Deny。Permissions→Create inline policyで必要なAction/Resourceを指定し、Trust relationshipsで信頼先を確認・編集する。

Create a Lambda function→Create function→Existing roleで空の権限のロールも選べる。Code→ZIP Deploy→Testで実Pythonを実行する。関数作成者には`lambda:CreateFunction`と当該ARNの`iam:PassRole`が必要。ネイティブロールは作成時・実行開始時にLambda serviceの信頼を要求する。信頼を外したロール、削除済みロール、ARNの大文字小文字を変えた参照は実行しない。STSによるsession発行ではない。

公開AWSの[ロール作成](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create_for-service.html)と[Lambda execution role](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html)に沿った限定導線。ログイン済みAWSコンソールは閲覧していない。managed policy選択表、全trust種別、現在の全アカウントの外観一致を主張しない。

## APIの制限とエラー

| 項目 | 実装範囲 |
| --- | --- |
| scope/name | Global、root pathのみ。role名は1–64の英数字・`_`・`-`。AWSが許す他の記号と非root pathは未対応 |
| 名前の同一性 | role名はcase insensitive。一意なRoleId、作成日時、ARN、description、MaxSessionDurationを保持 |
| trust | Version2012-10-17、1–10 statements、Allow/Deny、Action sts:AssumeRole、明示Service principalのみ。lambda.amazonaws.comへのAllowが必要、Deny優先。32KiBまで |
| inline permissions | identity policy subset、最大10policy/100aggregate statements/32KiB。未知のCondition等は拒否。明示Deny優先 |
| pagination | ListRoles/ListRolePoliciesのMaxItems1–1000、返却Markerで継続 |
| limits | Query本文128KiB。active+retired role名1000まで。削除ロールのnative再作成は新RoleIdを発行 |
| メタデータ | description1000文字、AWSのAPI仕様の許可文字範囲（tab/改行/CR、U+0020–007E、U+00A1–00FF）、MaxSessionDuration3600–43200（STSなし、実session期間ではない） |
| errors | malformed input/trust400、認可403、NoSuchEntity404、EntityAlreadyExists/DeleteConflict/LimitExceeded409、本文超過413、未対応操作/field501。DB障害500は内部詳細を隠す |

各操作は`iam:<Action>`とrole ARN（ListRolesは`*`）へ現在のidentity policyを評価する。ローカルparticipant tokenの固定identityも同じ判定を受け、headerでadminへ変更できない。operator-onlyの旧identity管理とは認可境界が異なる。

## 既存identity/CloudFormationとの互換性

Local identitiesは以前のJSON control APIを保持する。既存CFNのAWS::IAM::Roleも従来のidentity方式で、trust/managed policy ARNはメタデータのみ。これらをnative roleへ暗黙移行しない。IAM画面のLocal identitiesから旧データを管理でき、Lambda ARNの手入力も維持する。

native role名と旧identity/CFN ownershipの採用を相互に拒否する。旧JSON APIのPUT/DELETEはnative active/retired名へ409を返す。所有確認と変更は同一transactionで、同時CreateRoleとの競合でも片方だけ成功する。削除後のnative名も予約を残し、legacy identityを作って削除済みroleの信頼検査を回避できない。inline policy更新と実行identityのaggregate更新も単一transaction、失敗時rollback。

DeleteRoleはinline policyが残るとDeleteConflict。先にDeleteRolePolicyする。ロールを削除しても既存関数の定義は残り、次回Invokeが拒否される。AWSの全関連サービスの削除制約は再現しない。managed policies、STS、users/groups、resource policies、boundaries/SCP、tags、advanced trust Conditionsは未実装。

チーム分離は引き続き1チーム1プロセス・SQLite・token authority。role名/ARNだけでtenant分離しない。実SDK、実Lambda、二重プロセス、権限拒否、削除、PC/スマホの実ブラウザを検証する。

## 上流再利用と権利

Role wire fieldsはKumoのMIT実装の選択部分を適合した。固定commit/source SHAと改変範囲は[provenance](../third_party/kumo-iam.json)、原文copyright/permissionは[Kumo license](../third_party/KUMO-LICENSE.txt)。SQLite、認可、trust判定、ownership、consoleはこの実装で追加した。Kumo serverやLocalStack/Pythonを実行時依存にせず、上流の対応サービス数をこのSimulatorの対応数とは扱わない。

Descriptionの文字制約は[CreateRole API](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateRole.html)に合わせ、XMLで置換されるNUL等を保存前に拒否する。
