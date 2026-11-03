# 現行問題カタログ対応表

入力: TenkaCloudChallenge `a749b142416770abee3f173673702c55df3856aa`。`scripts/catalog-matrix.py` がmetadataと参照テンプレートを読み取り生成。29問、Resources宣言のリソース型31種類（AWS 30、Custom 1。MetadataのCloudFormation::Init等は含めない）。宣言上のIaC保有8問と、cloudワークロード7問（cryptoのcoordination主体を除く）は区別する。

hello-worldだけを開発用ローカル接続で完走確認済み。TenkaCloud標準起動へ自動統合・配布済みという意味ではない。他の既存問題は削除しない。Docker20問とlocal coordination1問は本来の実行系で扱う。

| 問題パス | Simulator要否・状態 | 宣言されたリソース型 | 採点 | 必要な状態・API/動作 |
| --- | --- | --- | --- | --- |
| `battles/crypto-battle` | 不要（coordinationゲーム） | AWS::IAM::Role, AWS::SSM::Parameter | coordination | Bun/pluginの実行とゲーム固有状態。cryptoのIaCは任意付帯でありプレイ対応の根拠ではない |
| `battles/forensic-casebook` | 不要（coordinationゲーム） | — | coordination | Bun/pluginの実行とゲーム固有状態。cryptoのIaCは任意付帯でありプレイ対応の根拠ではない |
| `battles/hello-world-battle` | 未対応（選択不可） | AWS::EC2::Instance, AWS::EC2::InternetGateway, AWS::EC2::Route, AWS::EC2::RouteTable, AWS::EC2::SecurityGroup, AWS::EC2::Subnet, AWS::EC2::SubnetRouteTableAssociation, AWS::EC2::VPC, AWS::EC2::VPCGatewayAttachment, AWS::IAM::InstanceProfile, AWS::IAM::Role | uptime-flat | 実ワークロード、記載リソース/API・状態・IAMと固有採点が必要。APIだけの疑似成功では不十分 |
| `battles/microservice-migration-battle` | 未対応（選択不可） | AWS::EC2::Instance, AWS::EC2::InternetGateway, AWS::EC2::Route, AWS::EC2::RouteTable, AWS::EC2::SecurityGroup, AWS::EC2::Subnet, AWS::EC2::SubnetRouteTableAssociation, AWS::EC2::VPC, AWS::EC2::VPCGatewayAttachment, AWS::IAM::InstanceProfile, AWS::IAM::Role | phased-polling | 実ワークロード、記載リソース/API・状態・IAMと固有採点が必要。APIだけの疑似成功では不十分 |
| `battles/security-battle-royale` | 未対応（選択不可） | AWS::EC2::Instance, AWS::EC2::InternetGateway, AWS::EC2::Route, AWS::EC2::RouteTable, AWS::EC2::SecurityGroup, AWS::EC2::Subnet, AWS::EC2::SubnetRouteTableAssociation, AWS::EC2::VPC, AWS::EC2::VPCGatewayAttachment, AWS::IAM::InstanceProfile, AWS::IAM::Role | uptime-multi | 実ワークロード、記載リソース/API・状態・IAMと固有採点が必要。APIだけの疑似成功では不十分 |
| `battles/stackstack` | 未対応（選択不可） | AWS::EC2::Instance, AWS::EC2::InternetGateway, AWS::EC2::Route, AWS::EC2::RouteTable, AWS::EC2::SecurityGroup, AWS::EC2::Subnet, AWS::EC2::SubnetRouteTableAssociation, AWS::EC2::VPC, AWS::EC2::VPCGatewayAttachment, AWS::ElasticLoadBalancingV2::Listener, AWS::ElasticLoadBalancingV2::LoadBalancer, AWS::ElasticLoadBalancingV2::TargetGroup, AWS::IAM::InstanceProfile, AWS::IAM::Role, AWS::Lambda::Function, AWS::Logs::LogGroup, AWS::RDS::DBInstance, AWS::RDS::DBSubnetGroup, AWS::S3::Bucket, AWS::SSM::Parameter, AWS::WAFv2::WebACL, Custom::EmptyStackBuckets | phased-polling | 実ワークロード、記載リソース/API・状態・IAMと固有採点が必要。APIだけの疑似成功では不十分 |
| `challenges/greeting-expression-lab` | 不要（Docker実ワークロード） | — | verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/hello-multicloud` | 未対応（選択不可） | AWS::IAM::Role, AWS::Lambda::Function, AWS::Lambda::Permission, AWS::Lambda::Url | composite-probe | 実ワークロード、記載リソース/API・状態・IAMと固有採点が必要。APIだけの疑似成功では不十分。GCP/Azure/Sakuraと各HTTPS probeも必要 |
| `challenges/hello-world` | ローカル縦切り検証済み | AWS::IAM::Role, AWS::SSM::Parameter | flag | Create/Describe/DeleteStack → SSM GetParameter。チーム別プロセス/DB/認証。実ホストflag採点100点・誤答5点減点・重複加点防止 |
| `challenges/memory-slot-lab` | 不要（Docker実ワークロード） | — | verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/php-memory-lifetime-lab` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/pxe-icmp-oob` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/security-hype-lp` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/sqli-demo` | 不要（Docker実ワークロード） | — | verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-defend` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-first-request` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-observability` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-onboarding` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-recover` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-safe-exposure` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-secrets` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-ship` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/stackstack-vibe-build` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/wp-exposed-backup` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/wp-harden-leaks` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/wp-midnight-admin` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/wp2shell-friday-night-patch` | 未対応（選択不可） | AWS::ApiGateway::Deployment, AWS::ApiGateway::Method, AWS::ApiGateway::Resource, AWS::ApiGateway::RestApi, AWS::ApiGateway::Stage, AWS::CloudFormation::CustomResource, AWS::IAM::Role, AWS::Lambda::Function, AWS::Lambda::Permission, AWS::S3::Bucket, AWS::SSM::Parameter, AWS::WAFv2::WebACL, AWS::WAFv2::WebACLAssociation | multi-flag | 実ワークロード、記載リソース/API・状態・IAMと固有採点が必要。APIだけの疑似成功では不十分 |
| `challenges/wp2shell-local-lab` | 不要（Docker実ワークロード） | — | multi-verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |
| `challenges/xss-demo` | 不要（Docker実ワークロード） | — | verify | Compose、実アプリ、専用verify。Simulatorの成功判定で代用不可 |

## hello-worldの具体的対応

| 段階 | 実行内容 | IAM/秘密・状態 | 画面/検証 |
| --- | --- | --- | --- |
| 取り込み | ホスト既存downloadCatalog、ハッシュ付きsealed snapshot | 問題コードを実行しない | hello-worldのみ選択可能 |
| 起動 | 実テンプレートをAWS SDK CreateStackへ送信 | SSMとinline policyを同一SQLiteトランザクション作成。秘密Outputsはホストのみ | CREATE_COMPLETEと所有jobタグを実ホストが確認 |
| 操作 | Systems Manager Parameter Store、GetParameter | ホスト発行15分tokenで固定identity。ssm読取のみ | 一覧→名前→Overview→Value。SDKも同じ値 |
| 採点 | TenkaCloud既存HostingServiceのsubmit-flag | ホストのprivate outputと比較。採点ledgerはホストDB | 他チームflagはwrong、100/95点、8同時提出1加点 |
| 分離/終了 | 2チーム別Go process/SQLite/token authority、DeleteStack | 他チームtoken 401、participant DescribeStacks 403。driftでも所有権保持 | DELETEDまで確認 |

実STS、AWS federation/CloudShell/managed-policy権限・trust-policy評価は実装しない。接続用のローカルルーティングを本物の一時AWS資格情報として提示しない。テンプレートに含まれるAWS console URLはローカルアクセス先の根拠にしない。
