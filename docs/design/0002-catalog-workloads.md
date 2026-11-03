# 既存クラウド問題の段階的な対応

基準はmain `e9e67dc`（PR #29の実Python Lambda foundationまでmerge済み）、Challenge `a749b142416770abee3f173673702c55df3856aa`。
hello-world以外を対応済みとは表示しない。問題コード、テンプレート、採点をSimulator専用の固定応答へ置き換えない。

## 次の対象と順番

次は `challenges/wp2shell-friday-night-patch`。既存S3/SSMを使用し、EC2の実Linux/Session Managerや他providerの同時実装を必要としないため優先する。

1. Python 3.12 Lambdaの実行。元のPythonコードを実行するCPython/WASIをGoバイナリへ埋め込み、native Create/Get/Invoke/Deleteと永続状態、関数ARN/IAM、Cloudscapeの関数一覧・詳細・Test導線を実装する。
2. 証拠用S3 bucketのCloudFormation、versioning、public-access-blockの対応範囲を追加する。既存object API/consoleと同じ状態を使用し、非empty削除、versionの復元/削除、stack所有権、失敗時rollbackを検証する。
3. native API Gateway、Lambda proxy invoke、WAFv2の必要なルールと楽観lock、CloudFormation関係、custom resource lifecycleを追加する。APIだけ返す成功にはしない。
4. 未変更の実テンプレートで初期化し、証拠読取、WAF Count→Block、SSM修復、credential rotation、正常/攻撃HTTP、実Lambda採点、実TenkaCloud multi-flag ledger、削除まで2チームで検証する。ここまで成功して初めて問題の選択を有効にする。

Lambda/WAF/API GatewayはAWS layerに属し、問題ID・flag計算をcore/runtimeへ入れない。API/SDK/UIは同じHTTP/stateを使用する。今後のEC2 Battleは実LinuxワークロードとSSM Session Managerが必要であり、systemctl文字列への固定応答で代用しない。hello-multicloudはAWS Lambdaだけでは完了せず、GCP/Azure/Sakura各targetの実ワークロードとcomposite-probe採点まで必要。

現在の追加範囲: step 2のnative S3 version history/public-access-blockとCloudscape操作を実装する。S3 CloudFormationのBucketName/Versioning/PublicAccessBlock/resource TagsとRef/Arnを追加する。Step 3/4とwp2shellの完了ではない。問題の選択を有効にする条件はstep 4の実採点完走のまま。

## 実行境界の受け入れ条件

CPythonを利用する場合も、実行時の外部Python/Docker/Nodeを要求しない。WASIにはホストOSのfilesystem、環境変数、network、process作成を渡さない。stdlibと当該関数のコードだけを読取専用で提供する。実行時間、linear memory、同時実行、code/event/result/log量を制限し、無限loop/大量allocation/巨大出力/host file読取/process実行/外部socketをnegative testする。

将来のSDK/HTTP bridgeは呼出元が送るprincipalやURLを信用せず、保存したexecution roleと同一環境の宣言済みtargetに限定する。外部AWSや任意loopback/metadata endpointへのproxyにしない。各段階はrace/coverage90/SDK/browser/security/既存形式検査を維持し、原文licenseと固定source/build provenanceを同梱する。

一次資料: [CPython WASI build](https://github.com/python/cpython/blob/3.12/Tools/wasm/README.md)、[wazero](https://wazero.io/docs/)、[Lambda CreateFunction](https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunction.html)、[Lambda Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html)。これらは対応完了の証拠ではなく実装契約の参照元。

追加段階: Lambda inline CFNと明示DependsOnの作成/逆順削除をnative保存処理に接続する。これだけでstep3/4やWP採点が完成するわけではない。managed BasicExecutionRoleはmetadataのみ。次は保存execution roleに固定した制限付きSDK bridgeで、ネットワークや任意endpointは与えない。
