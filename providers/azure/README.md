# Azure provider

`@tenkacloud/simulator-provider-azure` は Bicep から Container App と Role
Assignment の resource state を生成し、Azure control plane の状態遷移を決定論的に
再現します。

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
