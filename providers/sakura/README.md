# Sakura provider

`@tenkacloud/simulator-provider-sakura` は AppRun application descriptor から application、
version、traffic、packet filter の resource state を生成し、AppRun control plane の状態遷移を
決定論的に再現します。

## HTTP data plane

AppRun application が compile plan に存在するとき、plan は
`http / HTTP::Endpoint / Request` の L4 requirement を含みます。provider は同じ
deployment に属する application が一件だけあり、resource record が `ready`、保存済み
application status が `Healthy` の場合だけ endpoint として扱います。不足、重複、非 ready、
壊れた projection を成功へ推測しません。

`Request` は `{Method, Path, Headers, Body}` を境界検証します。GET と HEAD は保存済みの
application name、version、traffic から決定論的な `{StatusCode, Headers, Body}` を返し、
それ以外の構文上正しい method は `Allow: GET, HEAD` を付けた 405 を返します。problem ID に
よる分岐、host network への fetch、参加者 image の実行は行いません。
