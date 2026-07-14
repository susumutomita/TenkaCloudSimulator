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

CloudFormation で作成した `AWS::Lambda::Url` も、同じ resource graph 上の HTTP
data plane として扱います。`AuthType: NONE` と public な
`lambda:InvokeFunctionUrl` permission、および `InvokedViaFunctionUrl: true` の
`lambda:InvokeFunction` permission が一意に揃った Function URL は、stack output の URL から対象 URL resource
と Lambda function を解決し、`Request` と scorer の `Probe` を catalog handler へ内部
dispatch します。合成した `*.lambda-url.*.on.aws` へ network request は送りません。ALB
listener がある deployment で URL が明示されない `Request` は従来の ALB 経路を維持し、
Function URL が明示された場合だけ URL 経路を選びます。未知 URL、複数候補、
AWS IAM 認証付き URL、public permission のない URL は成功扱いしません。
Lambda payload v2 の `rawQueryString` は受け取った origin-relative path の `?` 以降を
byte 表現のまま保持し、`%20` と `+`、percent escape の大文字小文字を再 serialize しません。

scorer の `Probe` は、deployed Function URL なら上記の内部 dispatch を使います。`Poll` とそれ以外の
`Probe` は Simulator/TenkaCloud が所有する numeric loopback HTTP URL だけを実際に fetch します。
redirect は追わず、response body、timeout、poll 件数を制限します。任意の
cloud URL、hostname 解決、credential 付き URL を provider から proxy する SSRF 経路にはしません。
control-plane projection を 200 と見なす実装ではなく、Lambda handler または loopback data-plane の
実 status/body が scoring input です。

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

SSM セッション Manager のセッション ID、stream URL、期限 transition、stream registry は target identity を
含みます。data channel が実行する `SendCommand` / `TerminateSession` も開始時の targetId を維持し、
同じ deployment 内の別 AWS target や `default` target へセッション state を付け替えません。

Battle metadata の `endpoints` は deploy 時に `Runtime::Endpoint` resource として保存します。
`TenkaCloudRuntime.ResolveEndpoint` は slot の CloudFormation output と `appendPath` を参照
しますが、合成した host を実 workload として公開しません。workload が materialize されていない
デフォルト endpoint は typed unavailable で失敗します。overridable な slot に明示された外部 URL は
simulator world 内へ永続化して解決します。metadata に存在しない slot、target が異なる slot、
credential を埋め込んだ URL は受け付けません。

### Managed placement projection

`microservice-migration-battle` の local play では、managed tier を workload の
`/meta.platform` から決めません。generic provider-operation API が公開する
`CreateManagedFunction` / `CreateManagedService` は、materialize 済みの reviewed workload と slot を
provider 側で照合し、participant が作成した control-plane resource を world / deployment / target
scoped resource graph に保存します。通常の Lambda `CreateFunction` は managed placement の対象に
なりません。CloudFormation の participant-controlled `Properties` や `Metadata` に
`ParticipantCreated` / `EligibleManagedPlacement` を記述しても、provider-owned な resource 直下の
eligibility に昇格しません。managed-create operation は AWS SDK wire protocol の再現ではなく、
任意 code を実行しない Simulator 固有の bounded subset です。

ready resource の name identity (`refValue`) は、同じ world / deployment / target /
resource type で通常 resource と managed resource の間でも一意です。managed-create は
eligibility の有無にかかわらず同名の ready resource があれば conflict にします。一方、
managed-describe は provider が認定した `EligibleManagedPlacement: true` の resource だけを
返し、同名の通常 resource を managed resource として投影しません。

managed-create input は name と slot だけです。ECS だけは `DesiredCount: 1` と
`LaunchType: "FARGATE"` も要求します。image、URL、platform、cluster、task definition は
participant から受け取りません。provider は reviewed workload の exact declaration
（digest-pinned image、bounded command、port、health path、target、resource ref）と resource ID から
`ReviewedArtifactHash` を計算し、`EligibleManagedPlacement` と一緒に resource へ
固定します。応答の `PlacementEligibility: "ELIGIBLE"` は AWS 上の service liveness を模倣する
status ではなく、reviewed artifact への placement 候補が resource graph に確定したことだけを
表します。

reviewed `Runtime::Workload` の `ready` は participant 入力ではありません。core は workload
effect runner が digest、world / target identity、resource ref を一致させ、loopback proxy 越しの
`healthPath` が 2xx になった materialization 結果を返した後だけ `ready` へ遷移させます。
managed reducer はその projection を作り出さず、一意な `ready` resource のみを参照します。
この state は liveness lease ではないため、binding 後の container 停止を `DescribeEndpointPlacement`
の成功として採点しません。scorer は `EffectiveUrl` への実 HTTP probe も必須とし、stale / dead
container はその data-plane probe 失敗によって fail closed になります。

`TenkaCloudRuntime.BindManagedResource` は `Slot` と `ManagedResourceId` だけを受け取ります。
endpoint URL は入力させず、materialize 済みの reviewed `Runtime::Workload` と endpoint slot の
`OutputKey` を照合して内部導出します。verified platform は ready な participant-created resource の
resource type からだけ導出します。binding 後の URL override、同じ slot の再 binding、同じ resource
の別 slot への再利用、別 world / deployment / target の参照、未知の resource type は拒否します。
同じ slot に Lambda / ECS / App Runner の未 binding 候補を複数作成すること自体は許可し、
endpoint の active binding は常に 1 つ、managed resource の active binding も常に 1 つに制限します。

command envelope の `targetId` は必須です。input の `TargetId` は省略可能な照合値であり、
指定する場合は command scope の `targetId` と完全一致しなければ拒否します。下の CLI 例は
`--target default`、HTTP 例は `"targetId": "default"` でその scope を明示しています。

`TenkaCloudRuntime.DescribeEndpointPlacement` は scorer 向けに、`DeploymentId`、`TargetId`、
`Slot`、`EffectiveUrl`、`ReviewedWorkloadId`、`ReviewedArtifactHash`、`ManagedResourceId`、
`ManagedResourceType`、`VerifiedPlatform` だけを返します。resource の template properties や
participant code、environment、credential は返しません。

公開する capability identity は次のとおりです。

```text
aws/cloudformation/lambda/AWS::Lambda::Function/CreateManagedFunction
aws/cloudformation/lambda/AWS::Lambda::Function/DescribeManagedFunction
aws/cloudformation/ecs/AWS::ECS::Service/CreateManagedService
aws/cloudformation/ecs/AWS::ECS::Service/DescribeManagedService
aws/cloudformation/apprunner/AWS::AppRunner::Service/CreateManagedService
aws/cloudformation/apprunner/AWS::AppRunner::Service/DescribeManagedService
aws/cloudformation/runtime/Runtime::Endpoint/BindManagedResource
aws/cloudformation/runtime/Runtime::Endpoint/DescribeEndpointPlacement
```

たとえば `users` slot を Lambda tier へ配置する generic CLI 操作は次の 3 回です。

```bash
printf '%s\n' '{"FunctionName":"users-managed","Slot":"users"}' \
  > /tmp/create-managed-function.json
bun tools/cli/src/bin.ts operation \
  --url "$SIMULATOR_URL" --token "$SIMULATOR_TOKEN" \
  --world "$WORLD_ID" --provider aws --operation CreateManagedFunction \
  --deployment "$DEPLOYMENT_ID" --target default --engine cloudformation \
  --service lambda --resource 'AWS::Lambda::Function' \
  --input /tmp/create-managed-function.json --idempotency users-lambda-create

printf '%s\n' \
  '{"Slot":"users","ManagedResourceId":"<create response ManagedResourceId>"}' \
  > /tmp/bind-users.json
bun tools/cli/src/bin.ts operation \
  --url "$SIMULATOR_URL" --token "$SIMULATOR_TOKEN" \
  --world "$WORLD_ID" --provider aws --operation BindManagedResource \
  --deployment "$DEPLOYMENT_ID" --target default --engine cloudformation \
  --service runtime --resource 'Runtime::Endpoint' \
  --input /tmp/bind-users.json --idempotency users-lambda-bind

printf '%s\n' '{"Slot":"users"}' > /tmp/describe-users.json
bun tools/cli/src/bin.ts operation \
  --url "$SIMULATOR_URL" --token "$SIMULATOR_TOKEN" \
  --world "$WORLD_ID" --provider aws --operation DescribeEndpointPlacement \
  --deployment "$DEPLOYMENT_ID" --target default --engine cloudformation \
  --service runtime --resource 'Runtime::Endpoint' \
  --input /tmp/describe-users.json --idempotency users-lambda-describe
```

同じ create 操作を HTTP で直接呼ぶ場合の envelope は次のとおりです。

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $SIMULATOR_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: orders-ecs-create' \
  --data '{
    "deploymentId": "'"$DEPLOYMENT_ID"'",
    "targetId": "default",
    "engine": "cloudformation",
    "service": "ecs",
    "resourceType": "AWS::ECS::Service",
    "input": {
      "ServiceName": "orders-managed",
      "Slot": "orders",
      "DesiredCount": 1,
      "LaunchType": "FARGATE"
    }
  }' \
  "$SIMULATOR_URL/v1/worlds/$WORLD_ID/providers/aws/operations/CreateManagedService"
```

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
