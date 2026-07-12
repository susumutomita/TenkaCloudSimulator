# TenkaCloud Catalog Scanner

TenkaCloudChallenge の `metadata.json` と provider-native IaC を静的解析し、
Simulator が実装すべき capability requirement を決定論的な JSON にします。

この scanner は resource 一覧だけを coverage と見なしません。各 requirement には
provider、service、resource type、operation、fidelity に加え、操作主体を示す
`plane`、検出根拠を示す `origin`、ゲート対象かを示す `classification`、元ファイルの
行番号を残します。

`classification: "binding"` は実行が証明された capability で、coverage gate の対象です。
`classification: "authorization-inventory"` は IAM `Allow` が示す許可上限です。後者も
report に coverage 付きで残しますが、許可されているだけでは実行要件とは扱わず、
`missing` / `insufficient` でも report status を失敗させません。同じ操作を versioned
simulation overlay が要求した場合、その overlay requirement は `binding` として失敗します。

## 対応入力

- legacy `cfnTemplate`。`aws / cloudformation / <cfnTemplate>` に正規化する。
- single runtime の `aws / cloudformation`。
- composite runtime の `aws / cloudformation` と `gcp / infra-manager`。
- `docker / compose`。既存 local-play の担当なので cloud capability は生成しない。
- CloudFormation の resource type と、YAML の `Action` field に構造化された IAM action。
  コメント、Description、UserData、inline code の `service:Operation` は IAM inventory に
  含めない。
- Terraform の `resource` block。
- metadata の endpoint、scoring probe、attack probe、disruption action。

未対応 engine、壊れた metadata、problem 外への entry、存在しない IaC、未知の
disruption action は無視せず `invalid` diagnostic にします。

## Capability manifest

```json
{
  "schemaVersion": "1",
  "version": "2026-07-12",
  "capabilities": [
    {
      "provider": "aws",
      "service": "ssm",
      "resourceType": "AWS::SSM::Parameter",
      "operation": "lifecycle",
      "fidelity": "L1"
    }
  ]
}
```

identity は `provider / service / resourceType / operation` です。同一 identity の
重複、未知 field、未知 fidelity は manifest 自体のエラーです。

## CLI

```bash
bun src/cli.ts \
  --catalog /path/to/TenkaCloudChallenge \
  --capabilities /path/to/capabilities.json \
  --output coverage-report.json
```

`--output` を省略すると JSON を標準出力へ書きます。終了コードは、全 binding requirement
が満たされれば `0`、binding の `missing` / `insufficient` または `invalid` があれば `1`、
引数または manifest を読めない場合は `2` です。summary は binding 件数を既存 field に、
IAM inventory 件数を `authorizationInventory` に分けます。report はタイムスタンプや絶対パス
を含まないため、同じ catalog と manifest から常に同じ bytes を生成します。

## 開発

```bash
bun install --ignore-scripts
bun test
bun test --coverage
bun run typecheck
```
