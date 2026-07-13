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
- single / composite runtime の `aws / cloudformation`、`azure / bicep`、
  `gcp / infra-manager`、`gcp / terraform`、`sakura / apprun`。
- `docker / compose`。既存 local-play の担当なので cloud capability は生成しない。
- CloudFormation の resource type と、YAML の `Action` field に構造化された IAM action。
  コメント、Description、UserData、inline code の `service:Operation` は IAM inventory に
  含めない。
- Terraform の `resource` block。
- Azure provider と同じ parser が受理する Bicep resource、property、dependency、output の
  structural subset。Managed Environment と Container App の `environmentId` 依存、および
  3つのTenkaCloud adapter parameterから作るboundedな`uniqueString` resource名を含む。
  module、loop、condition、未知resource type、未対応parameter・型・expressionは黙って
  読み飛ばさず`invalid`にする。
- Sakura provider と同じ strict application parser が受理する AppRun JSON descriptor。
  壊れた JSON、unknown shape、provider が扱えない値は `invalid` にする。
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
      "engine": "cloudformation",
      "service": "ssm",
      "resourceType": "AWS::SSM::Parameter",
      "operation": "lifecycle",
      "fidelity": ["L1"]
    }
  ]
}
```

identity は `provider / engine / service / resourceType / operation` です。provider が同じでも
別 engine の実装を流用して coverage を満たした扱いにはしません。同一 identity の
重複、未知 field、未知 fidelity は manifest 自体のエラーです。`fidelity` は
`L0` から `L4` の独立した dimension の集合であり、必ず非空、重複なし、
`L0`, `L1`, `L2`, `L3`, `L4` の canonical 順の配列で記述します。数字の大小は
上位・下位の互換性を意味しません。requirement も fidelity の canonical set を
保持し、必要な全 dimension が capability 集合に含まれる場合だけ `covered` です。
report の `requiredFidelity`、`implementedFidelity`、および
diagnostic の `availableFidelity` は、manifest が宣言した集合全体を公開 dimension 名で
保持します。

## CLI

```bash
bun src/cli.ts \
  --catalog /path/to/TenkaCloudChallenge \
  --catalog-commit "$(git -C /path/to/TenkaCloudChallenge rev-parse HEAD)" \
  --capabilities /path/to/capabilities.json \
  --simulator-version tenkacloud-simulator-0.1.0+git.<clean-simulator-commit> \
  --output coverage-report.json
```

`--output` を省略すると JSON を標準出力へ書きます。終了コードは、全 binding requirement
が満たされれば `0`、binding の `missing` / `insufficient` または `invalid` があれば `1`、
引数または manifest を読めない場合は `2` です。report の root は公開
`CapabilityCoverageReport` schema に従い、binding requirement を canonical な nested shape
で出力します。scanner 固有の problem、diagnostic、IAM authorization inventory は
`inventory` に分離します。scan 対象は Git checkout でなければならず、`catalogCommit` はその
checkout の HEAD と一致し、catalog scope に未 commit の変更がない場合だけ受理されます。
加えて scan に使った全 source は `catalogCommit` の tracked Git blob と byte 一致しなければ
ならず、ignored / untracked artifact や `assume-unchanged` で隠された drift も拒否します。
`simulator-version` は clean Simulator commit を含む capability manifest の `version` と
完全一致させます。`reportHash` は
その field だけを除き、object key を再帰的に整列した canonical report payload の SHA-256
です。report はタイムスタンプや絶対パスを含まないため、同じ catalog、commit、manifest、
simulator version から常に同じ bytes を生成します。

## 開発

```bash
bun install --ignore-scripts
bun test
bun test --coverage
bun run typecheck
```
