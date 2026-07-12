# AWS provider

`@tenkacloud/simulator-provider-aws` は TenkaCloud Challenge catalog の AWS
CloudFormation template を決定的な resource graph に変換し、競技で使う AWS API
の状態遷移を `@tenkacloud/simulator-core` 上で再現する provider です。

対応範囲は package が公開する `AWS_CAPABILITIES` を正本とします。S3 object、SSM
parameter、security group rule、listener rule、Web ACL association など、状態を実際に
保存・更新できる操作だけを該当 fidelity で公開します。catalog に同梱された Lambda handler と
外部 evaluator は data-plane fidelity まで検証しますが、任意の Lambda code や任意 URL を
simulator process で実行しません。

`EvaluatorFunction` だけは async reducer で参加者の実 HTTP workload を検査します。production
runtime は credential、redirect、fragment を拒否した `https://<name>.workers.dev` だけを許可し、
各 response を 64 KiB、request を 8 秒に制限します。テストと differential conformance は
constructor で明示 trust した loopback origin に限り HTTP を使えます。この trust 設定を server
environment や deployment input から変更する経路は持ちません。

参加者が migration Battle で作る Lambda は `CreateFunction` の control plane として保存します。
受理するのは digest を計算できる bounded な ZIP payload、明示した Node.js runtime、単一の
`x86_64` architecture だけです。作成した関数の configuration と code digest は `GetFunction` で
観測できますが、任意の参加者 code を simulator process で実行しません。catalog 固有 handler と
external evaluator 以外を `InvokeFunction` で成功扱いしないため、L1 の作成契約と L4 の data-plane
実行証跡を分離します。

HTTP data plane は保存済み ALB listener rule、default action、target group、Lambda target を同じ
resource graph から辿ります。`Request` は method allow-list を実際に評価し、forward された場合だけ
catalog handler を呼び出します。したがって listener rule の変更前は edge の 405、変更後は handler
由来の response と flag になり、problem ID や固定の成功応答では分岐しません。

scorer の `Probe` と `Poll` は、Simulator/TenkaCloud が所有する numeric loopback HTTP URL だけを
実際に fetch します。redirect は追わず、response body、timeout、poll 件数を制限します。任意の
cloud URL、hostname 解決、credential 付き URL を provider から proxy する SSRF 経路にはしません。
control-plane projection を 200 と見なす実装ではなく、loopback data-plane の実 status/body が
scoring input です。

## Native AWS gateway

`AwsNativeGateway` は AWS CLI / SDK が送る SigV4 header の構文を検証し、native AWS
request を core の `ExecuteCommandInput` に変換します。受け付ける access key は gateway
起動時に指定した `TCSIM...` key だけです。`AKIA...` / `ASIA...` key、セッショントークン、presigned
credential は実 AWS credential として拒否します。実 secret を検証する入口ではなく、隔離された
simulator world への protocol adapter です。

各 request は次の signed routing header を必須とします。

- `x-tenkacloud-world-id`
- `x-tenkacloud-deployment-id`
- `x-tenkacloud-target-id`（省略時は `default`）

current catalog に必要な transport は、SSM / Logs / WAFv2 の AWS JSON 1.1、
CloudFormation / IAM / EC2 / ELBv2 / RDS / STS の Query、Lambda の REST-JSON、
S3 の path-style REST-XML です。gateway は provider が reducer を持つ operation だけを
route し、未知の service、operation、パスを成功扱いしません。成功と既知エラーは各 AWS
protocol の JSON、XML、header、status に直して `Response` として返します。

Battle metadata の `endpoints` は deploy 時に `Runtime::Endpoint` resource として保存します。
`TenkaCloudRuntime.ResolveEndpoint` は slot の CloudFormation output と `appendPath` を参照
しますが、合成した host を実 workload として公開しません。workload が materialize されていない
デフォルト endpoint は typed unavailable で失敗します。overridable な slot に明示された外部 URL は
simulator world 内へ永続化して解決します。metadata に存在しない slot、target が異なる slot、
credential を埋め込んだ URL は受け付けません。

## L3 reachability projection

`ec2/EvaluateReachability` は `AWS::EC2::Instance` capability として、CloudFormation から
投影済みの network resource と reducer が更新した state だけを読みます。入力は canonical IPv4
CIDR の `SourceCidr`、`tcp` または `udp` の `IpProtocol`、1 から 65535 の `Port`、排他的な
`DestinationInstanceId` または `DestinationLoadBalancerArn` です。host network への接続や
problem ID による既知解答は使いません。

instance 宛てでは、subnet と VPC、明示 route-table association、Internet Gateway への default
route と VPC attachment、public address、security-group ingress、instance の running state を順に
評価します。internet-facing load balancer 宛てでは、同じ public path と load balancer ingress に
加え、port/protocol が一致する一意な listener、一意な forward target group、同一 VPC の target
instance、load-balancer security group を source とする target ingress、target instance の running
state を評価します。Web ACL が関連付いている場合は association の参照整合性と default action も
評価します。

response は lower camel case の `reachable`、安定順の `reasons`、評価した resource ID と判定を
並べた `path` です。network policy により到達できない既知状態は `reachable: false` で返します。
resource の重複、壊れた参照、複数 forward action、要求だけでは判定できない WAF rule などの
未知・曖昧な projection は typed error とし、到達可能または到達不能へ推測しません。
