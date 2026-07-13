# Sakura provider

`@tenkacloud/simulator-provider-sakura` は AppRun application descriptor から application、
version、traffic、packet filter の resource state を生成し、AppRun control plane の状態遷移を
決定論的に再現します。

container registry image は digest を含む通常の OCI reference を欠落なく保持するため、
1 文字以上 512 文字以下に制限します。これは GHCR の repository path と 64 桁 SHA-256
digest を組み合わせた reference を受理しつつ、無制限な state を拒否する境界です。

## HTTP data plane

AppRun application の control-plane projection は data plane の代替ではありません。
`simulationOverlay.workloads[]` が同一 target の `BaseUrl` を参照し、descriptor の digest 固定
image、port、health path と一致する場合だけ、explicit overlay の HTTP `Request` / `Probe`
requirement を受理します。provider compile plan はそれらを重複生成しません。workload effect が
materialize した ready な numeric-loopback endpoint に限り、async reducer が GET / HEAD を
bounded に転送します。

`Request` は `{Method, Path, Headers, Body}` を境界検証します。GET / HEAD 以外、workload の
欠落・不一致・未 ready、redirect、timeout、response body 上限超過は成功へ丸めず loud に
失敗します。status、content type、body は実 workload の応答を返し、problem ID による分岐や
control-plane state からの fixed 200 応答は行いません。
