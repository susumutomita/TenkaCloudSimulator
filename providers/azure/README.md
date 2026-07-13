# Azure provider

`@tenkacloud/simulator-provider-azure` は Bicep から Managed Environment、Container App、
Role Assignment の resource state を生成し、Azure control plane の状態遷移を決定論的に
再現します。Container App の `environmentId: <managed-environment-symbol>.id` は暗黙の
依存関係として plan に残します。

## TenkaCloud adapter parameter

live Azure adapter と同じ入力を検査するため、targeted compiler は次の3宣言を一組で
受理します。

```bicep
param tenkacloudNamePrefix string
param tenkacloudProblemId string
param tenkacloudTeam string
```

resource 名は Managed Environment の
`'tc-${uniqueString(tenkacloudNamePrefix, tenkacloudProblemId, tenkacloudTeam)}-env'`と、
Container App の同じ式に`-app`を付ける bounded な 2 形だけを追加で展開します。
別の parameter、型、default、関数、引数順、suffix、複数 interpolation は受理しません。
従来の literal 名も引き続き受理します。

## HTTP data plane

外部 ingress を持つ Container App が compile plan に存在するとき、plan は
`http / HTTP::Endpoint / Request` の L4 requirement を含みます。provider は同じ
deployment に属する Container App が一件だけあり、resource record が `ready`、保存済み
application status が `Running`、external ingress が有効な場合だけ endpoint として扱います。
不足、重複、非 ready、壊れた projection を成功へ推測しません。

`Request` は `{Method, Path, Headers, Body}` を境界検証します。GET と HEAD は保存済みの
`responseStatus` と `responseBody` から `{StatusCode, Headers, Body}` を返し、それ以外の
構文上正しい method は `Allow: GET, HEAD` を付けた 405 を返します。problem ID による
分岐、host network への fetch、参加者 code の実行は行いません。

## Targeted Bicep output

Container App の採点 URL は、通常の Bicep と同じ
`'https://${app.properties.configuration.ingress.fqdn}'` 形式で string output にできます。
targeted compiler が展開する補間は、この HTTPS prefix と Container App ingress FQDN の
組み合わせだけです。任意の文字列補間や未知 resource/property は成功へ推測せず、compile
error にします。
