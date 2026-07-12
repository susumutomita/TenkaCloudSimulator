# GCP provider

`@tenkacloud/simulator-provider-gcp` は Infrastructure Manager 用 Terraform から Cloud
Run service と IAM member の resource state を生成し、GCP control plane の状態遷移を
決定論的に再現します。

## HTTP data plane

Cloud Run service が compile plan に存在するとき、plan は
`http / HTTP::Endpoint / Request` の L4 requirement を含みます。provider は同じ
deployment に属する Cloud Run service が一件だけあり、resource record が `ready`、保存済み
service status が `Ready` の場合だけ endpoint として扱います。不足、重複、非 ready、壊れた
projection を成功へ推測しません。

`Request` は `{Method, Path, Headers, Body}` を境界検証します。GET と HEAD は保存済みの
`responseStatus` と `responseBody` から `{StatusCode, Headers, Body}` を返し、それ以外の
構文上正しい method は `Allow: GET, HEAD` を付けた 405 を返します。problem ID による
分岐、host network への fetch、参加者 code の実行は行いません。
