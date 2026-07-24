# API Gateway REST API と WAFv2 WebACLAssociation の capability coverage

## 問題

TenkaCloudChallenge の `catalog-coverage` gate は、pinned Simulator revision に対して
`bun run simulator:compatibility` を実行し、catalog 全体が要求する AWS resource を
capability manifest がすべて `covered` にすることを要求します。baseline も allowlist も
なく、`supported` が `false` の PR はそのまま CI で fail します。

新しい Challenge `wp2shell-friday-night-patch`（API Gateway + Lambda + WAFv2 の
incident-response 問題）は、次の 7 requirement を `missing` にします。すべて
`operation=lifecycle`、`classification=binding`（IaC resource から抽出、CI を実際に
止める種別）です。

| service    | resourceType                          | 個数 |
| ---------- | -------------------------------------- | ---- |
| apigateway | `AWS::ApiGateway::RestApi`              | 1    |
| apigateway | `AWS::ApiGateway::Resource`             | 1    |
| apigateway | `AWS::ApiGateway::Method`               | 2    |
| apigateway | `AWS::ApiGateway::Deployment`           | 1    |
| apigateway | `AWS::ApiGateway::Stage`                | 1    |
| wafv2      | `AWS::WAFv2::WebACLAssociation`         | 1    |

`AWS::WAFv2::WebACL` 自体は `battles/stackstack` からすでに coverage 済みで、今回
不足しているのは association resource だけです。

## 制約

- [`docs/architecture/protocol.md`](../architecture/protocol.md) の fidelity model
  (`L0` contract / `L1` control / `L2` security / `L3` network / `L4` data-plane) を
  そのまま使う。新しい dimension は追加しない。
- `tools/catalog-scanner/src/cloudformation.ts` の `resourceFidelity()` は
  `AWS::ApiGateway::*` を network marker に含めないため `L1` だけを要求し、
  `AWS::WAFv2::*` は marker に含むため `L1` + `L3` を要求する。simulator 側の
  `providers/aws/src/catalog-manifest.ts` はこの要求と非対称にならないよう合わせる。
- 「resourceType を registry に足すだけ」の stub は禁止する
  (`docs/architecture/quality-bar.md`)。`providers/aws/tests/cloudformation.test.ts` の
  `package fixture の26 resource typeを依存順かつ決定的に宣言する` テストは、
  `CLOUDFORMATION_RESOURCE_TYPES` に登録した type が必ず fixture 内で実際に
  synthesize されることを強制しており、この gate を回避しない。
- Lambda `InvokeFunction` は `providers/aws/src/lambda.ts` の `invokeHandler()` が
  問題ごとに手実装した logical ID switch で再現しており、汎用コード実行系ではない。
  `wp2shell-friday-night-patch` の `AppFunction` 個別の grading 挙動 (Python 実装の
  incident-response ロジック) を再現することは、今回の 7 requirement の scope 外である
  (`AWS::Lambda::Function` の `lifecycle` は既存 coverage で満たしている)。

## 選択肢

### A. manifest だけを機械的に拡張する

`CLOUDFORMATION_RESOURCE_TYPES` に 6 type を足し、`catalog-manifest.ts` の
fidelity 計算だけ調整する。`bun run simulator:compatibility` は緑になるが、
`compileCloudFormation` は未知 type として reject し続けるため実際には deploy
できず、`covered` の意味が空になる。quality bar の禁止する stub そのもの。

### B. API Gateway の HTTP data-plane まで丸ごと proxy する

`native-gateway.ts` に AWS_PROXY 統合の HTTP router を足し、REST API の
path/method match から Lambda invoke まで実際に転送する。ELBv2 (`L3`) の
既存実装と同じ深さで見ると overreach です。ELBv2 の `L3` は
`DescribeRules` / `ModifyRule` という control/network state の read-write
であって、実 HTTP proxy ではありません (`elb.ts`)。API Gateway だけ
`L4` 相当の data-plane を先取りすると、fidelity の意味が resource ごとに
ばらつきます。

### C. 要求された fidelity ちょうど (L1 / L3) の control・network plane を実装する (採用)

- **L1 (control)**: `AWS::ApiGateway::RestApi` / `Resource` / `Method` /
  `Deployment` / `Stage` を `CLOUDFORMATION_RESOURCE_TYPES` に登録し、
  `cloudformation.ts` の `physicalRef()` / `resourceAttributes()` に実際の
  Ref / GetAtt 解決を実装する。`RestApi` の `RootResourceId` GetAtt、
  `Stage` の `Ref` (StageName を返す、実 AWS と同じ規約) を含む。
- **L3 (network)**: `AWS::WAFv2::WebACLAssociation` を deploy 時に
  `wafv2:AssociateWebACL` と同じ状態遷移 (`waf.ts` の
  `AssociateWebACL` reducer) へ揃える。`providers/aws/src/state.ts` に
  `webAclAssociationEffects()` を追加し、`deploy.ts` の
  `deployCloudFormation()` から `customResourceObjects()` と同じ post-process
  段階で呼ぶ。CFN で宣言した association は、明示的に `wafv2 associate-web-acl`
  を呼ばなくても `GetWebACLForResource` / `ListWebACLs` の応答へ反映される
  (実 AWS の `AWS::WAFv2::WebACLAssociation` も CFN 作成時に同じ API を裏で
  呼ぶ)。ARN 照合ロジックは `waf.ts` の `findAssociableResource` と
  重複させず、`state.ts` の `matchesAssociableArn()` へ共通化する。

C は `docs/architecture/protocol.md` が定義する fidelity dimension をそのまま
使い、新しい invariant を追加しないため ADR は起票しない。

## Edge case

- WebACLAssociation が参照する `ResourceArn` / `WebACLArn` が同一 stack 内の
  resource に一致しない場合は `ValidationFailed` で fail loudly にする
  (実 AWS も存在しない resource を association すると deploy が失敗する)。
- `UpdateStack` は毎回 template 全体を再 compile するため、テンプレートから
  association resource を削除した場合、次回 deploy で対象 resource の state は
  `initialState()` の初期値 (`associatedResources: []` / `webAclArn` なし) に
  戻る。個別の disassociate 処理を追加で書く必要はない。
- 同一 stack に複数の association がある場合、対象・WebACL それぞれの
  `state` 更新を集約してから 1 回だけ resource を差し替える。

## 未実装として明示する範囲

`wafv2:UpdateWebACL` は `wp2shell-friday-night-patch` の IAM policy に現れるが、
`classification=authorization-inventory` (catalog scanner の binding gate 対象外) であり、
今回の 7 requirement に含まれない。既存の `AWS_CAPABILITIES` にも
`apigateway:*` の command capability は無く、この問題の participant 権限にも
`apigateway:*` action は要求されていない。追加は本 PR の scope 外とする。
