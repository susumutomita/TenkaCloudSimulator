# IAM roles と実行権限

IAM Query/XMLの21操作を実装する。既存ロールの9操作に加え、カスタマー管理ポリシーの`CreatePolicy`、`GetPolicy`、`ListPolicies`、`DeletePolicy`、`CreatePolicyVersion`、`GetPolicyVersion`、`ListPolicyVersions`、`SetDefaultPolicyVersion`、`DeletePolicyVersion`、`AttachRolePolicy`、`DetachRolePolicy`、`ListAttachedRolePolicies`を提供する。既存操作は`CreateRole`、`GetRole`、`ListRoles`、`DeleteRole`、`PutRolePolicy`、`GetRolePolicy`、`DeleteRolePolicy`、`ListRolePolicies`、`UpdateAssumeRolePolicy`。root POST、Version `2010-05-08`、`X-Simulator-Service: iam`でルーティングする。ダミーTCSIM署名のSDKはIAM scopeで自動振分け。実AWS資格情報・session tokenは使わない。認証はCLIのlocal tokenで行う。

ScalarにはIAM roles専用のPOST `/_simulator/providers/aws/` があり、IAM headerを初期入力した21操作の静的リクエスト例を選べる。新しい状態や別APIではなく既存AWS provider aliasで、root POSTと同一処理。従来root POSTの操作例も保持する。例そのものをHTTP送信するテストで、作成・読取・一覧・更新・削除の既存9操作を検証する。追加12操作は専用SDKライフサイクルで検証する。boto3はURL-encoded policyを辞書へ復元する。ネイティブSDK、コンソール、Lambdaは同じSQLiteのロール・inline policyを共有する。

## コンソール

Services→IAM→Roles→Create role。公式Cloudscape WizardでSelect trusted entity→Add permissions→Name, review, and createへ進む。AWS serviceのLambdaを選ぶと`lambda.amazonaws.com`を信頼する。Custom trust policyは限定service-principal JSONを受け付ける。作成直後はservice権限がなくimplicit Deny。Permissions→Create inline policyで必要なAction/Resourceを指定し、Trust relationshipsで信頼先を確認・編集する。

Create a Lambda function→Create function→Existing roleで空の権限のロールも選べる。Code→ZIP Deploy→Testで実Pythonを実行する。関数作成者には`lambda:CreateFunction`と当該ARNの`iam:PassRole`が必要。ネイティブロールは作成時・実行開始時にLambda serviceの信頼を要求する。信頼を外したロール、削除済みロール、ARNの大文字小文字を変えた参照は実行しない。STSによるsession発行ではない。

公開AWSの[ロール作成](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create_for-service.html)と[Lambda execution role](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html)に沿った限定導線。ログイン済みAWSコンソールは閲覧していない。AWS-managed policyの選択表、全trust種別、現在の全アカウントの外観一致を主張しない。

## APIの制限とエラー

| 項目 | 実装範囲 |
| --- | --- |
| scope/name | Global、root pathのみ。role名は1–64の英数字・`_`・`-`。AWSが許す他の記号と非root pathは未対応 |
| 名前の同一性 | role名はcase insensitive。一意なRoleId、作成日時、ARN、description、MaxSessionDurationを保持 |
| trust | JSON keyはVersion/Statement/Sid/Effect/Action/Principal/Serviceの正確な綴りを要求。Version2012-10-17、1–10 statements、Allow/Deny、Action sts:AssumeRole、明示Service principalのみ。lambda.amazonaws.comへのAllowが必要、Deny優先。32KiBまで |
| inline permissions | identity policy subset、最大10policy/100aggregate statements/32KiB。未知のCondition等は拒否。明示Deny優先 |
| pagination | ListRoles/ListRolePoliciesのMaxItems1–1000、返却Markerで継続 |
| limits | Query本文128KiB。active+retired role名1000まで。削除ロールのnative再作成は新RoleIdを発行 |
| メタデータ | description1000文字、AWSのAPI仕様の許可文字範囲（tab/改行/CR、U+0020–007E、U+00A1–00FF）、MaxSessionDuration3600–43200（STSなし、実session期間ではない） |
| errors | malformed input/trust400、認可403、NoSuchEntity404、EntityAlreadyExists/DeleteConflict/LimitExceeded409、本文超過413、未対応操作/field501。DB障害500は内部詳細を隠す |

各操作は`iam:<Action>`とrole ARN（ListRolesは`*`）へ現在のidentity policyを評価する。ローカルparticipant tokenの固定identityも同じ判定を受け、headerでadminへ変更できない。operator-onlyの旧identity管理とは認可境界が異なる。

## カスタマー管理ポリシーの操作

IAM→Policies→Create policyでJSONを入力し、作成後のARNを利用する。IAM→Roles→対象ロール→Permissions→Add permissions→Attach policiesからローカルのポリシーを選ぶ。Policiesの詳細ではバージョンの作成、既定版の変更、不要な非既定版の削除を行う。ロールのinline policyと管理ポリシーの既定版を合成して評価し、どちらにある明示Denyも優先する。ポリシーを作るだけでは権限を付与しない。

```python
# iam is boto3.client('iam', endpoint_url=local_url, ...) with the host token.
policy = iam.create_policy(PolicyName='ReadObjects', PolicyDocument=json.dumps({
    'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow',
    'Action': 's3:GetObject', 'Resource': 'arn:aws:s3:::practice-bucket/*'}]}))['Policy']
iam.attach_role_policy(RoleName='practice-executor', PolicyArn=policy['Arn'])
assert iam.list_policies(Scope='Local')['Policies']
# Create another document/version before selecting its returned VersionId.
version = iam.create_policy_version(PolicyArn=policy['Arn'],
    PolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[
        {'Effect':'Deny','Action':'s3:GetObject','Resource':'*'}]}), SetAsDefault=True)['PolicyVersion']
iam.set_default_policy_version(PolicyArn=policy['Arn'], VersionId='v1')
iam.delete_policy_version(PolicyArn=policy['Arn'], VersionId=version['VersionId'])
iam.detach_role_policy(RoleName='practice-executor', PolicyArn=policy['Arn'])
iam.delete_policy(PolicyArn=policy['Arn'])
```

ロールは先にCreateRoleする。例のバケット・オブジェクトはダミーで、作成済みを意味しない。API referenceの例も固定テンプレートで、実行結果ではない。`Test Request → Send`で現在の状態を取得する。GetPolicyVersionのDocumentだけをRFC3986で一度エンコードし、SDKは通常辞書に戻す。CreatePolicyVersionとListPolicyVersionsにはDocumentを含めない。成功応答はtext/xml、IAMエラー応答はapplication/xmlで、いずれもXML本文。ホスト認証の401は別のJSONエンベロープである。

| 項目 | ローカル実装と制限 |
| --- | --- |
| 対象 | ローカルアカウント000000000000のcustomer policy、Path `/`のみ。名前1–128文字、英数字と `_+=,.@-`、case-insensitiveで一意。ARNは保存済みの正確な値を使う |
| 一覧 | `ListPolicies(Scope='Local')`を必ず指定。AWSの省略時既定値Allを変更した扱いにはせず、省略/All/AWSを501で拒否。OnlyAttached/PolicyUsageFilter/非root PathPrefixも501 |
| 文書 | raw UTF-8 32KiB、tab/LF/CRとU+0020–00FF、compact JSON 6144文字まで。引用符内の空白は残る。Query本文全体128KiB。既存identity grammar、Condition/Principal等は未対応 |
| 上限 | active+retired policy名1000、保持version5、managed attachment20/role（inline10とは別）、物理edge20000。合成100 statements/32KiBの既存上限も維持 |
| 世代 | 初期defaultはv1。削除版番号を再利用せず、名前を再作成すると新PolicyId。attachmentは現在のRoleIdとPolicyIdに結び付く |
| 反映 | default変更と全attachment先の実行用aggregateを同一transactionで更新。一つでも合成上限超過や書込失敗なら全体rollback。新しい版を既定にして作る場合も同じ |
| 削除 | attachmentまたは非既定版が残るpolicyはDeleteConflict。既定版だけのpolicyはDeletePolicyで削除。既定版自体のDeletePolicyVersionはDeleteConflict |
| 再送 | 存在する両親への重複Attach/未接続Detachはローカルno-op。AWSとの比較済み挙動ではない。CreatePolicyVersionは非冪等で自動再送しない |
| ページング | MaxItems1–1000。Markerは操作・フィルタ・親世代に限定した継続位置で、認証情報や一覧snapshotではない |

認可は[AWS Service Authorization Reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_iam.html)に沿い、ListPoliciesは`*`、policy/version操作はpolicy ARN、Attach/Detach/ListAttachedRolePoliciesはrole ARNを評価する。対象policyのGetPolicy権限を追加要求する独自の認可条件は作らない。UIは情報表示のため別途Get/List権限を必要とする。Lambdaは保存した実行ロールで各bridge操作時の現在の権限を評価し、Invoke全体の権限snapshotは保証しない。

公式の[管理ポリシーの版](https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_managed-versioning.html)、[CreatePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreatePolicy.html)、[AttachRolePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_AttachRolePolicy.html)、[IAM quotas](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html)を基準にした限定実装。全AWS IAM互換ではない。詳しい操作・状態・モデルと未検証の対応は[実装契約](design/0010-iam-managed-policy-contract.md)を参照する。実AWS差分試験や認証済みコンソール確認は行っていない。

## 既存identity/CloudFormationとの互換性

Local identitiesは以前のJSON control APIを保持する。既存CFNのAWS::IAM::Roleも従来のidentity方式で、trust/managed policy ARNはメタデータのみ。これらをnative roleへ暗黙移行しない。IAM画面のLocal identitiesから旧データを管理でき、Lambda ARNの手入力も維持する。

native role名と旧identity/CFN ownershipの採用を相互に拒否する。旧JSON APIのPUT/DELETEはnative active/retired名へ409を返す。所有確認と変更は同一transactionで、同時CreateRoleとの競合でも片方だけ成功する。削除後のnative名も予約を残し、legacy identityを作って削除済みroleの信頼検査を回避できない。inline policy更新と実行identityのaggregate更新も単一transaction、失敗時rollback。

DeleteRoleはinline policyまたはmanaged attachmentが残るとDeleteConflict。先にDeleteRolePolicyとDetachRolePolicyを実行する。ロールを削除しても既存関数の定義は残り、次回Invokeが拒否される。AWSの全関連サービスの削除制約は再現しない。AWS-managed policies、STS、users/groupsへのattachment、resource policies、boundaries/SCP、tags、advanced trust Conditionsは未実装。

チーム分離は引き続き1チーム1プロセス・SQLite・token authority。role名/ARNだけでtenant分離しない。実SDK、実Lambda、二重プロセス、権限拒否、削除、PC/スマホの実ブラウザを検証する。

## 上流再利用と権利

以前のRole selected wire type適合はhistorical / superseded（`metadataHistorical`）。固定commit/source SHAと当時の改変範囲は[provenance](../third_party/kumo-iam.json)、原文copyright/permissionは[Kumo license](../third_party/KUMO-LICENSE.txt)に保持する。現在は明示XML投影がRoleId・要素順・Description省略・一度encodeしたtrust文書のXML escape・呼出側wrapperを定義する。既存role JSONの読取、SQLite、認可、trust判定、managed aggregate、ownership、consoleはこの投影変更で置き換えない。Kumoはregistry/protocol/SDK endpoint/service wiringの設計参照として扱い、著作者・clean-room・法的適法性を認定するものではない。Kumo serverやLocalStack/Pythonを実行時依存にせず、上流の対応サービス数をこのSimulatorの対応数とは扱わない。

Descriptionの文字制約は[CreateRole API](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateRole.html)に合わせ、XMLで置換されるNUL等を保存前に拒否する。

## Bounded native STS interoperability

[Native SDK/STS](sts-native.md) adds explicit AWS account-root/account-ID and
IAM-role trust principals with `StringEquals` `sts:ExternalId`. Existing
service-principal trust remains supported. Unsupported condition operators,
wildcard principals and unrecognized trust fields are rejected. Both caller
permission and target trust are required for AssumeRole. This does not implement
federation, real AWS authentication, permissions boundaries or general IAM
conditions. CFn-owned legacy roles remain separate from native IAM listings but
can be resolved by STS from their actual owned stack records.
