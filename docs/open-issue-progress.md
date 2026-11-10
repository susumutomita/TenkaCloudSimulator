# Simulator 全open Issueの進捗

確認時点: **2026-11-10 03:45 UTC**。基準main: [214948ad](https://github.com/susumutomita/TenkaCloudSimulator/commit/214948ad4cdabc5351ad895e2c8c46b3646f201e)。
GitHubの全3ページを取得し、PRを除く**264 open Issue**を以下に一度ずつ掲載しています。
これはIssue件数であり、未実装サービス数・完成率ではありません。

- [最新のopen Issue](https://github.com/susumutomita/TenkaCloudSimulator/issues?q=is%3Aissue%20is%3Aopen)
- [最新のopen PR](https://github.com/susumutomita/TenkaCloudSimulator/pulls?q=is%3Apr%20is%3Aopen)
- [CI](https://github.com/susumutomita/TenkaCloudSimulator/actions/workflows/ci.yml)・[正式な対応範囲](../README.md)・[作業記録](development-status.md)

このページは上記時刻のスナップショットです。Issue集合とmainを同時点で再照合し、local候補の状態は03:43–03:45 UTCの確認を反映しています。GitHubや候補の状態はその後も変わります。
Issue本文や過去の作業記録には古い「未実装」「CI待ち」が残るため、最新PRのmerge結果、
source SHA、実際の検証結果を優先します。全264件の実行テスト・公式仕様の再検証を
この文書作成で行ったわけではありません。未照合は未照合として残しています。

## 確認時点の現在地

| 領域 | mainへ反映済み | 現在の残件 |
| --- | --- | --- |
| API reference | AWS JSON本文表示 [#454](https://github.com/susumutomita/TenkaCloudSimulator/pull/454)。検証済みPR source 285df357を214948adへmerge | merge後mainの[通常CI](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/42745070590)も成功。mainでE2E83件・JSON表示/6形式downloadを再確認 |
| Hello World Battle | IAM profiles [#448](https://github.com/susumutomita/TenkaCloudSimulator/pull/448)、native IAM/guest-control・durable command基盤 [#450](https://github.com/susumutomita/TenkaCloudSimulator/pull/450)、CFN intrinsics [#443](https://github.com/susumutomita/TenkaCloudSimulator/pull/443)・SSM値解決 [#445](https://github.com/susumutomita/TenkaCloudSimulator/pull/445) | network/guest候補の統合、完全offline入力と署名、実VM・SSM・原本の操作→採点→回復→cleanup。full Battleは未対応 |
| Bedrock local runtime [#453](https://github.com/susumutomita/TenkaCloudSimulator/issues/453) | 既存Bedrock control-plane catalogのみ | mainを統合したlocal候補737e266。前候補96fca8bのfocused race/vet・source review済み。新HEAD/full gate・新finite model・実Ollama/Apple生成/Claude Code受入は未完了 |
| CloudWatch [#58](https://github.com/susumutomita/TenkaCloudSimulator/issues/58) | Logs API/UIは既存main | local候補386d984のfocused native/race・finite baselineと6 controls・source reviewが成功。vet/build/SDKは進行中、全体gateは未実施 |
| 全cloud scope | AWS112 catalogサービス、GCP40 API、Azure11 RP、native Storage/Blob、opt-in Sakura Diskの限定範囲 | 単位も受入範囲も異なる。これらを加算して全サービス完成率にしない |

main上の機能、local候補、個別検証済み、全体検証待ちを区別します。
原本Battle/WPは、実ワークロードと二チーム分離、採点・回復・cleanupが通るまで有効化しません。

## 依存と重複

- #453はBedrock #162のlocal inference拡張。AgentCore #163、Claude Platform #164は別scopeです。
- #39/#40はCloudFormation #44の子scopeです。#44はservice adapter #39とcallback #40に依存します。
- 原本WP #43はAPI Gateway #37、WAF #38、#39/#40/#41/#44の統合受入です。API Gateway/WAFはmainでも、WP全体は完了ではありません。
- #39のAPI Gateway/Lambda Permission adapterは [#303](https://github.com/susumutomita/TenkaCloudSimulator/pull/303)でmain。WAF本体 [#50](https://github.com/susumutomita/TenkaCloudSimulator/pull/50)とWAF CFN adapterを混同しません。
- SNS #49のSQS fanoutは#48のnative queue状態と同一transaction契約が必要です。過去候補の所在・現行mainとの統合を確認してから再利用します。
- Battleの#56 VPC、#57 EC2、#54 SSMは実network/guest/IAM/cleanup契約で接続します。Route53 #241のprivate zone依存も#56と別に確認します。
- 一つのcatalog実装を共有する例: ECS/Fargate #112/#273、FSx/File Cache #278/#293、IoT #153/#154/#155、Pinpoint/Push #97/#94、SageMaker/A2I #59/#161/#182、WorkSpaces/Core #142/#144。
- それ以外の必須・任意依存は、各Issueの公式operation/field確認で固定します。未確認を「依存なし」と扱いません。

## 更新のしかた

1. 着手前に最新main、open/closed PR、保存branchと作業所有範囲を照合する。
2. 着手・handoff・merge・新しい失敗/解除時に、この表と[作業記録](development-status.md)を更新する。
3. 各増分にIssue、source SHA、PR、実施した検証、未実施/失敗、次の一手を残す。
4. Issueをcloseするのは、元の受入条件に照らして完了を確認したときだけ。metadata APIやAPI200だけでcloseしない。
5. 全件のsnapshotを更新するときは、PRを除く全ページを取得し、重複番号・欠落・追加close/openを確認する。

## 分類別一覧

### 親インデックス (3件)

AWS #52、Azure #316、Google Cloud #317は子scopeをまとめる入口です。子Issueと別のサービスとして加算しません。

| Issue | 現状 | 次の確認・残件 |
| --- | --- | --- |
| [#52 AWS service inventory: 258 normalized services and per-service issue coverage](https://github.com/susumutomita/TenkaCloudSimulator/issues/52) | 親インデックス | AWS258サービスの対応表。サービスIssueと二重計数しない。古いopen/完了記載は最新Issue/PRと照合する。 |
| [#316 Azure: official product catalog implementation plan index (201 canonical products, 2026-11-04)](https://github.com/susumutomita/TenkaCloudSimulator/issues/316) | 親インデックス／部分実装済み | #441の11 Azure Resource Providersとnative Blob及びPropertiesがmain。201製品全実装ではなく、子scopeと二重計数しない。 |
| [#317 Google Cloud service backlog — official snapshot 2026-11-04](https://github.com/susumutomita/TenkaCloudSimulator/issues/317) | 親インデックス／部分実装済み | #441の40 Google Cloud API catalogとnative Storageがmain。現在openの子23件とは分ける。古いP0本文の「GCP実装なし」は現状ではない。 |

### native増分と元のワークフロー (14件)

openのままでも既にmainへ入った増分があります。PRのマージと、そのIssue全体の受入完了を分けています。

| Issue | 現状 | 次の確認・残件 |
| --- | --- | --- |
| [#39 CloudFormation: provision native API Gateway, Lambda permissions and WAF adapters](https://github.com/susumutomita/TenkaCloudSimulator/issues/39) | 一部マージ済み | API Gateway・Lambda Permission のCFN連携は #303。WAF本体は #50。WAF CFN adapter と関連受入を要照合。 |
| [#40 CloudFormation: add a bounded one-shot cfnresponse capability](https://github.com/susumutomita/TenkaCloudSimulator/issues/40) | 未完了 | one-shot cfnresponse の元Issue受入は未確認。mainにCustomResource対応はなく、独立primitive→#44統合が必要。 |
| [#41 Lambda: route original grader urllib HTTP through bounded local API capabilities](https://github.com/susumutomita/TenkaCloudSimulator/issues/41) | 一部マージ済み／要求範囲は未完了 | native Python LambdaとSDK bridgeは既存。要求された原本grader urllib→local API capabilityはmain資料で非対応。 |
| [#43 Integration: run original WP catalog import through all seven flags and cleanup](https://github.com/susumutomita/TenkaCloudSimulator/issues/43) | 統合受入が未完了 | 原本WPの7 flags/300 points・二チーム・cleanupの完全受入は未確認。個別APIやWAF完成と区別。 |
| [#44 CloudFormation: orchestrate custom resource callbacks, rollback and restart recovery](https://github.com/susumutomita/TenkaCloudSimulator/issues/44) | 一部マージ済み／CustomResourceは未完了 | 一般CFN・REST adaptersはmain。custom Lambda callback/rollback/restart state machineの受入が必要。 |
| [#49 SNS: implement basic topic, subscription and publish operations](https://github.com/susumutomita/TenkaCloudSimulator/issues/49) | 基礎APIマージ済み／fanoutは未完了 | SNS native APIは #413。SNS→SQS atomic fanout は保存候補10f8b7aの所在・統合を要確認。main guideは未配線。 |
| [#51 S3: track existing coverage and add bounded multipart upload lifecycle](https://github.com/susumutomita/TenkaCloudSimulator/issues/51) | multipart増分マージ済み | #440でmultipartのnative/API/UI増分済み。Issueの広いS3後続範囲（policy/encryption/lifecycle等）と全S3完成を区別。 |
| [#54 AWS Systems Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/54) | 一部マージ済み／実行連携が未完了 | Parameter Store・CFN SSM snapshotは #313/#445。#450にdurable command基盤。実guest/Run Command/Session Managerの完全受入ではない。 |
| [#55 AWS Security Token Service (STS)](https://github.com/susumutomita/TenkaCloudSimulator/issues/55) | bounded STS増分マージ済み | #442/#446でnative STS・participant handoffの増分済み。Issue全受入条件との最終照合は別。 |
| [#58 Amazon CloudWatch](https://github.com/susumutomita/TenkaCloudSimulator/issues/58) | Logsはmain／DeleteLogStreamを実装中 | 既存Logsは#415。DeleteLogStream候補386d984のfocused native/race・finite baselineと6 controls・source reviewが成功。vet/build/SDKは進行中、aggregateは未実施。保存metrics branchは別範囲。 |
| [#117 AWS Key Management Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/117) | native KMS・alias増分マージ済み | #409/#429で限定ローカル暗号とalias。HSM/実AWS互換/全機能ではない。広いIssueの残条件を照合。 |
| [#266 AWS Secrets Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/266) | native API・version UI増分マージ済み | #408/#427/#430でSecrets Managerとversion metadata UI。追加の全サービス受入や無制限互換を意味しない。 |
| [#423 さくらのクラウド：現行 Go 版の対応範囲と段階的実装](https://github.com/susumutomita/TenkaCloudSimulator/issues/423) | Disk増分マージ済み／追加範囲は未完了 | #432でopt-in Sakura Disk metadata/API/UI。Server/ネットワーク/Object Storage/AppRunの実動作は別。 |
| [#431 CloudFormation: add bounded parameter constraints and conditions for crypto-battle](https://github.com/susumutomita/TenkaCloudSimulator/issues/431) | CFN増分マージ済み／ホスト受入は別 | #434でbounded parameters/conditions及びatomic cleanup。原本cryptoのhost game/scoring全体とは別。古いIssueチェックポイントを現状扱いしない。 |

### 保存候補とruntime作業 (4件)

候補の存在・focused検証はmain反映や全体受入ではありません。新しい変更後は同じcommitで必要なgateを確認します。

| Issue | 現状 | 次の確認・残件 |
| --- | --- | --- |
| [#56 Amazon Virtual Private Cloud](https://github.com/susumutomita/TenkaCloudSimulator/issues/56) | ローカル候補を検証中 | VPC/SG/routes/NACLとNIC/lease候補を保持。単体・有限モデルの結果をmain受入へ流用しない。実packet/guestとの統合が残る。 |
| [#57 Amazon Elastic Compute Cloud](https://github.com/susumutomita/TenkaCloudSimulator/issues/57) | 前提はmain／Battle runtimeは未完了 | #448/#450の基盤はmain。現在のnetwork候補e8340d0は未統合。offline guest入力/署名、VM transport、SSM実executor、原本Battle採点・cleanupが残る。 |
| [#241 Amazon Route 53](https://github.com/susumutomita/TenkaCloudSimulator/issues/241) | 保存候補あり／未統合 | Issue履歴にnative866923f・formal a371b15・UI b8375d0。8操作SDK検証は旧候補の証拠。現在の所在・最新main統合・full gateは未確認。 |
| [#453 Bedrock Runtime: select local Apple Intelligence and Ollama backends](https://github.com/susumutomita/TenkaCloudSimulator/issues/453) | ローカル候補を検証中 | local候補737e266はmain214948adを統合。前候補96fca8bの4 API/stream/tools・strict JSON対策はfocused race/vet・source review済み。新HEAD/full gate・新finite model・実Ollama/Apple生成/Claude Code受入は未完了。 |

### AWSカタログの限定対応 (113件)

[mainのoperation・bounds一覧](aws-catalog-services.md)と照合した113件です。112カタログサービスと1対1ではなく、複数Issueで共有する実装やclosed Issueもあります。SDK/APIの構成保存を、実ワークロード・推論・通信の完成とは扱いません。

| Issue | 現状 | 次の確認・残件 |
| --- | --- | --- |
| [#59 Amazon SageMaker](https://github.com/susumutomita/TenkaCloudSimulator/issues/59) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#61 Amazon Athena](https://github.com/susumutomita/TenkaCloudSimulator/issues/61) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#64 AWS Data Exchange](https://github.com/susumutomita/TenkaCloudSimulator/issues/64) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#65 Amazon Data Firehose](https://github.com/susumutomita/TenkaCloudSimulator/issues/65) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#69 AWS Entity Resolution](https://github.com/susumutomita/TenkaCloudSimulator/issues/69) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#70 AWS Glue](https://github.com/susumutomita/TenkaCloudSimulator/issues/70) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#71 Amazon Kinesis Data Streams](https://github.com/susumutomita/TenkaCloudSimulator/issues/71) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#74 Amazon Managed Streaming for Apache Kafka](https://github.com/susumutomita/TenkaCloudSimulator/issues/74) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#75 Amazon OpenSearch Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/75) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#79 Amazon EventBridge](https://github.com/susumutomita/TenkaCloudSimulator/issues/79) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#80 Amazon MQ](https://github.com/susumutomita/TenkaCloudSimulator/issues/80) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#81 Amazon MWAA](https://github.com/susumutomita/TenkaCloudSimulator/issues/81) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#85 Amazon Managed Blockchain](https://github.com/susumutomita/TenkaCloudSimulator/issues/85) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#88 AWS AppFabric](https://github.com/susumutomita/TenkaCloudSimulator/issues/88) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#94 AWS End User Messaging Push](https://github.com/susumutomita/TenkaCloudSimulator/issues/94) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#95 AWS End User Messaging SMS](https://github.com/susumutomita/TenkaCloudSimulator/issues/95) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#97 Amazon Pinpoint](https://github.com/susumutomita/TenkaCloudSimulator/issues/97) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#98 Amazon Simple Email Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/98) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#103 AWS App Runner](https://github.com/susumutomita/TenkaCloudSimulator/issues/103) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#106 EC2 Image Builder](https://github.com/susumutomita/TenkaCloudSimulator/issues/106) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#112 Amazon Elastic Container Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/112) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#113 Amazon Elastic Kubernetes Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/113) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#115 AWS Certificate Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/115) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#118 AWS Private Certificate Authority](https://github.com/susumutomita/TenkaCloudSimulator/issues/118) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#119 AWS Signer](https://github.com/susumutomita/TenkaCloudSimulator/issues/119) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#123 Amazon Aurora DSQL](https://github.com/susumutomita/TenkaCloudSimulator/issues/123) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#127 Amazon MemoryDB](https://github.com/susumutomita/TenkaCloudSimulator/issues/127) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#129 Oracle Database@AWS](https://github.com/susumutomita/TenkaCloudSimulator/issues/129) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#131 Amazon Timestream](https://github.com/susumutomita/TenkaCloudSimulator/issues/131) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#133 AWS Cloud9](https://github.com/susumutomita/TenkaCloudSimulator/issues/133) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#135 AWS CodeArtifact](https://github.com/susumutomita/TenkaCloudSimulator/issues/135) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#136 AWS CodeBuild](https://github.com/susumutomita/TenkaCloudSimulator/issues/136) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#138 AWS CodeDeploy](https://github.com/susumutomita/TenkaCloudSimulator/issues/138) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#139 AWS CodePipeline](https://github.com/susumutomita/TenkaCloudSimulator/issues/139) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#140 AWS Fault Injection Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/140) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#141 AWS X-Ray](https://github.com/susumutomita/TenkaCloudSimulator/issues/141) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#142 Amazon WorkSpaces](https://github.com/susumutomita/TenkaCloudSimulator/issues/142) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#143 Amazon WorkSpaces Applications](https://github.com/susumutomita/TenkaCloudSimulator/issues/143) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#144 Amazon WorkSpaces Core](https://github.com/susumutomita/TenkaCloudSimulator/issues/144) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#145 Amazon WorkSpaces Thin Client](https://github.com/susumutomita/TenkaCloudSimulator/issues/145) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#146 Amazon WorkSpaces Secure Browser](https://github.com/susumutomita/TenkaCloudSimulator/issues/146) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#148 AWS AppSync](https://github.com/susumutomita/TenkaCloudSimulator/issues/148) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#149 AWS Device Farm](https://github.com/susumutomita/TenkaCloudSimulator/issues/149) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#150 Amazon Location Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/150) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#151 Amazon GameLift Servers](https://github.com/susumutomita/TenkaCloudSimulator/issues/151) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#152 Amazon GameLift Streams](https://github.com/susumutomita/TenkaCloudSimulator/issues/152) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#153 AWS IoT Core](https://github.com/susumutomita/TenkaCloudSimulator/issues/153) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#154 AWS IoT Device Defender](https://github.com/susumutomita/TenkaCloudSimulator/issues/154) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#155 AWS IoT Device Management](https://github.com/susumutomita/TenkaCloudSimulator/issues/155) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#156 AWS IoT FleetWise](https://github.com/susumutomita/TenkaCloudSimulator/issues/156) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#157 AWS IoT Greengrass](https://github.com/susumutomita/TenkaCloudSimulator/issues/157) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#158 AWS IoT SiteWise](https://github.com/susumutomita/TenkaCloudSimulator/issues/158) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#159 AWS IoT TwinMaker](https://github.com/susumutomita/TenkaCloudSimulator/issues/159) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#160 AWS IoT Wireless](https://github.com/susumutomita/TenkaCloudSimulator/issues/160) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#161 Amazon A2I](https://github.com/susumutomita/TenkaCloudSimulator/issues/161) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#162 Amazon Bedrock](https://github.com/susumutomita/TenkaCloudSimulator/issues/162) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#163 Amazon Bedrock AgentCore](https://github.com/susumutomita/TenkaCloudSimulator/issues/163) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#166 Amazon Comprehend](https://github.com/susumutomita/TenkaCloudSimulator/issues/166) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#174 Amazon Kendra](https://github.com/susumutomita/TenkaCloudSimulator/issues/174) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#175 Amazon Lex](https://github.com/susumutomita/TenkaCloudSimulator/issues/175) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#180 Amazon Polly](https://github.com/susumutomita/TenkaCloudSimulator/issues/180) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#181 Amazon Rekognition](https://github.com/susumutomita/TenkaCloudSimulator/issues/181) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#182 Amazon SageMaker AI](https://github.com/susumutomita/TenkaCloudSimulator/issues/182) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#186 AWS Account Management](https://github.com/susumutomita/TenkaCloudSimulator/issues/186) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#187 AWS AppConfig](https://github.com/susumutomita/TenkaCloudSimulator/issues/187) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#188 AWS CloudTrail](https://github.com/susumutomita/TenkaCloudSimulator/issues/188) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#189 Amazon Q Developer in chat applications](https://github.com/susumutomita/TenkaCloudSimulator/issues/189) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#194 AWS Config](https://github.com/susumutomita/TenkaCloudSimulator/issues/194) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#196 Amazon Data Lifecycle Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/196) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#200 AWS License Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/200) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#202 Amazon Managed Service for Prometheus](https://github.com/susumutomita/TenkaCloudSimulator/issues/202) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#204 AWS Proton](https://github.com/susumutomita/TenkaCloudSimulator/issues/204) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#205 AWS Resilience Hub](https://github.com/susumutomita/TenkaCloudSimulator/issues/205) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#206 AWS Resource Explorer](https://github.com/susumutomita/TenkaCloudSimulator/issues/206) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#207 AWS Resource Groups](https://github.com/susumutomita/TenkaCloudSimulator/issues/207) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#208 AWS Service Catalog](https://github.com/susumutomita/TenkaCloudSimulator/issues/208) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#211 AWS Systems Manager Incident Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/211) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#212 AWS Telco Network Builder](https://github.com/susumutomita/TenkaCloudSimulator/issues/212) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#214 AWS User Notifications](https://github.com/susumutomita/TenkaCloudSimulator/issues/214) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#216 AWS Well-Architected Tool](https://github.com/susumutomita/TenkaCloudSimulator/issues/216) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#236 AWS App Mesh](https://github.com/susumutomita/TenkaCloudSimulator/issues/236) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#237 AWS Cloud Map](https://github.com/susumutomita/TenkaCloudSimulator/issues/237) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#242 Amazon Application Recovery Controller (ARC)](https://github.com/susumutomita/TenkaCloudSimulator/issues/242) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#243 AWS Global Accelerator](https://github.com/susumutomita/TenkaCloudSimulator/issues/243) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#246 AWS RTB Fabric](https://github.com/susumutomita/TenkaCloudSimulator/issues/246) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#249 Amazon VPC Lattice](https://github.com/susumutomita/TenkaCloudSimulator/issues/249) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#255 Amazon GuardDuty](https://github.com/susumutomita/TenkaCloudSimulator/issues/255) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#256 AWS Audit Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/256) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#257 Amazon Cloud Directory](https://github.com/susumutomita/TenkaCloudSimulator/issues/257) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#258 Amazon Detective](https://github.com/susumutomita/TenkaCloudSimulator/issues/258) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#259 AWS Firewall Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/259) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#260 AWS IAM Identity Center](https://github.com/susumutomita/TenkaCloudSimulator/issues/260) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#261 Amazon Inspector](https://github.com/susumutomita/TenkaCloudSimulator/issues/261) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#262 Amazon Macie](https://github.com/susumutomita/TenkaCloudSimulator/issues/262) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#264 AWS Payment Cryptography](https://github.com/susumutomita/TenkaCloudSimulator/issues/264) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#265 AWS Resource Access Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/265) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#268 AWS Security Hub](https://github.com/susumutomita/TenkaCloudSimulator/issues/268) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#269 AWS Security Incident Response](https://github.com/susumutomita/TenkaCloudSimulator/issues/269) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#270 Amazon Security Lake](https://github.com/susumutomita/TenkaCloudSimulator/issues/270) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#271 AWS Shield](https://github.com/susumutomita/TenkaCloudSimulator/issues/271) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#272 Amazon Verified Permissions](https://github.com/susumutomita/TenkaCloudSimulator/issues/272) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#273 AWS Fargate](https://github.com/susumutomita/TenkaCloudSimulator/issues/273) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#274 AWS Backup](https://github.com/susumutomita/TenkaCloudSimulator/issues/274) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#277 Amazon Elastic File System](https://github.com/susumutomita/TenkaCloudSimulator/issues/277) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#278 Amazon FSx](https://github.com/susumutomita/TenkaCloudSimulator/issues/278) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#279 AWS Storage Gateway](https://github.com/susumutomita/TenkaCloudSimulator/issues/279) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#281 AWS Billing Conductor](https://github.com/susumutomita/TenkaCloudSimulator/issues/281) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#283 AWS Budgets](https://github.com/susumutomita/TenkaCloudSimulator/issues/283) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#284 AWS Cost and Usage Report](https://github.com/susumutomita/TenkaCloudSimulator/issues/284) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#286 AWS CodeCommit](https://github.com/susumutomita/TenkaCloudSimulator/issues/286) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#293 Amazon File Cache](https://github.com/susumutomita/TenkaCloudSimulator/issues/293) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#296 AWS Cloud WAN](https://github.com/susumutomita/TenkaCloudSimulator/issues/296) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |
| [#302 Amazon Glacier](https://github.com/susumutomita/TenkaCloudSimulator/issues/302) | mainに構成メタデータの限定対応 | 登録operationの構成メタデータのみ。元Issueのruntime/data plane・統合受入との差分を確認。 |

### 仕様・実行方式の確認が必要 (38件)

38件は[現行coverage assessment](coverage-assessment.md)に非対応理由があります。固定botocore/discovery corpusの限界、外部・物理・provider-owned処理などを区別します。これは永久に実装不能との結論ではありません。最新の公式APIとlocal実装可能な契約を確認してから進めます。

| Issue | 現状 | 次の確認・残件 |
| --- | --- | --- |
| [#92 Amazon Connect Health](https://github.com/susumutomita/TenkaCloudSimulator/issues/92) | main資料で未対応の理由を記録 | Amazon Connect Health / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#96 AWS End User Messaging Social](https://github.com/susumutomita/TenkaCloudSimulator/issues/96) | main資料で未対応の理由を記録 | AWS End User Messaging Social / Every resource links a WhatsApp Business Account through Meta, or sends templates and messages to Meta. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#99 AWS Wickr](https://github.com/susumutomita/TenkaCloudSimulator/issues/99) | main資料で未対応の理由を記録 | AWS Wickr / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#102 Savings Plans](https://github.com/susumutomita/TenkaCloudSimulator/issues/102) | main資料で未対応の理由を記録 | Savings Plans / Plans are purchased commitments; offerings and rates are AWS price data. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#114 Red Hat OpenShift Service on AWS](https://github.com/susumutomita/TenkaCloudSimulator/issues/114) | main資料で未対応の理由を記録 | Red Hat OpenShift Service on AWS / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#120 AWS Managed Services](https://github.com/susumutomita/TenkaCloudSimulator/issues/120) | main資料で未対応の理由を記録 | AWS Managed Services / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#164 Claude Platform on AWS](https://github.com/susumutomita/TenkaCloudSimulator/issues/164) | main資料で未対応の理由を記録 | Claude Platform on AWS / No botocore model; `bedrock-agent` is Agents for Amazon Bedrock. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#167 Amazon Comprehend Medical](https://github.com/susumutomita/TenkaCloudSimulator/issues/167) | main資料で未対応の理由を記録 | Amazon Comprehend Medical / Only Detect*, Infer* and analysis-job operations. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#178 Amazon Nova Act](https://github.com/susumutomita/TenkaCloudSimulator/issues/178) | main資料で未対応の理由を記録 | Amazon Nova Act / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#191 AWS Security Agent](https://github.com/susumutomita/TenkaCloudSimulator/issues/191) | main資料で未対応の理由を記録 | AWS Security Agent / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#195 AWS Control Tower](https://github.com/susumutomita/TenkaCloudSimulator/issues/195) | main資料で未対応の理由を記録 | AWS Control Tower / Landing zones, controls and baselines deploy across an AWS Organizations organization; catalogs are AWS-owned. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#197 AWS DevOps Agent](https://github.com/susumutomita/TenkaCloudSimulator/issues/197) | main資料で未対応の理由を記録 | AWS DevOps Agent / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#198 AWS Health](https://github.com/susumutomita/TenkaCloudSimulator/issues/198) | main資料で未対応の理由を記録 | AWS Health / Events and entities are AWS-owned data; the rest is organization-level. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#199 AWS Launch Wizard](https://github.com/susumutomita/TenkaCloudSimulator/issues/199) | main資料で未対応の理由を記録 | AWS Launch Wizard / Deployments provision workloads; workloads and patterns are AWS-owned catalogs. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#210 Service Quotas](https://github.com/susumutomita/TenkaCloudSimulator/issues/210) | main資料で未対応の理由を記録 | Service Quotas / Quotas, AWS default values and the service list are AWS-owned data. Increase requests and support cases are sent to AWS, request templates need an AWS Organizations management account, and automatic management starts AWS-side utilization monitoring and notifications. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#213 AWS Trusted Advisor](https://github.com/susumutomita/TenkaCloudSimulator/issues/213) | main資料で未対応の理由を記録 | AWS Trusted Advisor / Checks and recommendations are AWS-owned data. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#217 AWS Marketplace](https://github.com/susumutomita/TenkaCloudSimulator/issues/217) | main資料で未対応の理由を記録 | AWS Marketplace / Catalog change sets are listing requests reviewed by AWS; agreements and entitlements are purchases. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#219 AWS Elemental Inference](https://github.com/susumutomita/TenkaCloudSimulator/issues/219) | main資料で未対応の理由を記録 | AWS Elemental Inference / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#230 Amazon Elastic VMware Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/230) | main資料で未対応の理由を記録 | Amazon Elastic VMware Service / Every operation provisions VMware Cloud Foundation on bare-metal hosts, VLANs and Elastic IPs. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#244 AWS Interconnect](https://github.com/susumutomita/TenkaCloudSimulator/issues/244) | main資料で未対応の理由を記録 | AWS Interconnect / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#245 AWS Network Security Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/245) | main資料で未対応の理由を記録 | AWS Network Security Manager / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#267 AWS Continuum](https://github.com/susumutomita/TenkaCloudSimulator/issues/267) | main資料で未対応の理由を記録 | AWS Continuum / No botocore model. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#285 VMware Cloud on AWS](https://github.com/susumutomita/TenkaCloudSimulator/issues/285) | main資料で未対応の理由を記録 | VMware Cloud on AWS / No AWS API; `evs` is Amazon Elastic VMware Service, a different product. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#301 AWS Support](https://github.com/susumutomita/TenkaCloudSimulator/issues/301) | main資料で未対応の理由を記録 | AWS Support / Cases are real support requests to AWS. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#319 Batch](https://github.com/susumutomita/TenkaCloudSimulator/issues/319) | main資料で未対応の理由を記録 | Batch (Google Cloud) / Only jobs (which run work) and tasks exist in `batch.v1`. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#323 Google Cloud Universal Ledger (GCUL)](https://github.com/susumutomita/TenkaCloudSimulator/issues/323) | main資料で未対応の理由を記録 | Google Cloud Universal Ledger / No discovery document. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#325 License Manager](https://github.com/susumutomita/TenkaCloudSimulator/issues/325) | main資料で未対応の理由を記録 | Google Cloud License Manager / No discovery document; `licensing.v1` is the Workspace Enterprise License Manager. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#330 Cloud Managed Lustre](https://github.com/susumutomita/TenkaCloudSimulator/issues/330) | main資料で未対応の理由を記録 | Cloud Managed Lustre / No `lustre` discovery document; only references from other APIs. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#334 Cloud Storage for Firebase](https://github.com/susumutomita/TenkaCloudSimulator/issues/334) | main資料で未対応の理由を記録 | Cloud Storage for Firebase / `addFirebase` links an existing Cloud Storage bucket from another provider; the default bucket provisions a real bucket. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#346 Dedicated Interconnect](https://github.com/susumutomita/TenkaCloudSimulator/issues/346) | main資料で未対応の理由を記録 | Dedicated Interconnect / `compute.v1` `interconnects.insert` orders physical links (`requestedLinkCount`) at an interconnect location. Partner Interconnect attachments (#347) are served. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#348 Cross-Cloud Interconnect](https://github.com/susumutomita/TenkaCloudSimulator/issues/348) | main資料で未対応の理由を記録 | Cross-Cloud Interconnect / The same `interconnects.insert` orders physical links to another cloud provider. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#349 Cross-Site Interconnect (CSI)](https://github.com/susumutomita/TenkaCloudSimulator/issues/349) | main資料で未対応の理由を記録 | Cross-Site Interconnect / `compute.v1` `wireGroups` group the physical wires between interconnect locations. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#352 Cloud Network Insights](https://github.com/susumutomita/TenkaCloudSimulator/issues/352) | main資料で未対応の理由を記録 | Cloud Network Insights / No discovery document names it. The closest, `networkmanagement` network monitoring providers, links an external platform and reports observed data. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#360 Media CDN](https://github.com/susumutomita/TenkaCloudSimulator/issues/360) | main資料で未対応の理由を記録 | Media CDN / `networkservices` offers only IAM policy methods for edge cache services, origins and keysets. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#370 Telecom Network Automation](https://github.com/susumutomita/TenkaCloudSimulator/issues/370) | main資料で未対応の理由を記録 | Telecom Network Automation / No `telcoautomation` discovery document. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#392 Data Studio](https://github.com/susumutomita/TenkaCloudSimulator/issues/392) | main資料で未対応の理由を記録 | Data Studio / No discovery document. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#395 Dataflow](https://github.com/susumutomita/TenkaCloudSimulator/issues/395) | main資料で未対応の理由を記録 | Dataflow / Jobs, templates and snapshots run or derive from running pipelines. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |
| [#398 Earth Engine](https://github.com/susumutomita/TenkaCloudSimulator/issues/398) | main資料で未対応の理由を記録 | Earth Engine / No discovery document among the 598 pinned documents. これは固定モデルに基づく資料であり、永久に実装不能との判定ではない。 |

### 残るIssueの個別照合 (91件)

91件は今回の既存カタログ・native増分との対応が確定していません。未着手・コード不存在とは断定しません。保存branchを先に探し、元IssueのP0仕様、P1 native動作、P2 SDK/UI/隔離・restart/cleanup、P3 exact-commit検証を順に確認します。GCPの理由は[現行catalogの非対応表](gcp-catalog.md#not-served)も参照してください。

| Issue | 現状 | 次の確認・残件 |
| --- | --- | --- |
| [#60 Amazon AppFlow](https://github.com/susumutomita/TenkaCloudSimulator/issues/60) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#62 AWS Clean Rooms](https://github.com/susumutomita/TenkaCloudSimulator/issues/62) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#63 Amazon CloudSearch](https://github.com/susumutomita/TenkaCloudSimulator/issues/63) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#66 AWS Data Pipeline](https://github.com/susumutomita/TenkaCloudSimulator/issues/66) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#67 Amazon DataZone](https://github.com/susumutomita/TenkaCloudSimulator/issues/67) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#68 Amazon EMR](https://github.com/susumutomita/TenkaCloudSimulator/issues/68) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#72 AWS Lake Formation](https://github.com/susumutomita/TenkaCloudSimulator/issues/72) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#73 Amazon Managed Service for Apache Flink](https://github.com/susumutomita/TenkaCloudSimulator/issues/73) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#76 Amazon Quick](https://github.com/susumutomita/TenkaCloudSimulator/issues/76) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#77 Amazon Redshift](https://github.com/susumutomita/TenkaCloudSimulator/issues/77) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#78 AWS B2B Data Interchange](https://github.com/susumutomita/TenkaCloudSimulator/issues/78) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#82 Amazon Simple Workflow Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/82) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#84 Amazon Q](https://github.com/susumutomita/TenkaCloudSimulator/issues/84) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#86 Amazon Connect Customer](https://github.com/susumutomita/TenkaCloudSimulator/issues/86) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#90 Amazon Chime SDK](https://github.com/susumutomita/TenkaCloudSimulator/issues/90) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#91 Amazon Connect Decisions](https://github.com/susumutomita/TenkaCloudSimulator/issues/91) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#100 Amazon WorkMail](https://github.com/susumutomita/TenkaCloudSimulator/issues/100) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#101 AWS Billing and Cost Management](https://github.com/susumutomita/TenkaCloudSimulator/issues/101) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#104 Amazon Lightsail](https://github.com/susumutomita/TenkaCloudSimulator/issues/104) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#105 AWS Batch](https://github.com/susumutomita/TenkaCloudSimulator/issues/105) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#107 AWS Elastic Beanstalk](https://github.com/susumutomita/TenkaCloudSimulator/issues/107) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#108 AWS Outposts](https://github.com/susumutomita/TenkaCloudSimulator/issues/108) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#109 AWS Parallel Computing Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/109) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#110 AWS Serverless Application Repository](https://github.com/susumutomita/TenkaCloudSimulator/issues/110) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#116 AWS CloudHSM](https://github.com/susumutomita/TenkaCloudSimulator/issues/116) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#121 AWS re:Post Private](https://github.com/susumutomita/TenkaCloudSimulator/issues/121) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#122 Amazon Aurora](https://github.com/susumutomita/TenkaCloudSimulator/issues/122) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#124 Amazon DocumentDB (with MongoDB compatibility)](https://github.com/susumutomita/TenkaCloudSimulator/issues/124) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#125 Amazon ElastiCache](https://github.com/susumutomita/TenkaCloudSimulator/issues/125) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#126 Amazon Keyspaces (for Apache Cassandra)](https://github.com/susumutomita/TenkaCloudSimulator/issues/126) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#128 Amazon Neptune](https://github.com/susumutomita/TenkaCloudSimulator/issues/128) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#130 Amazon Relational Database Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/130) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#132 AWS Cloud Control API](https://github.com/susumutomita/TenkaCloudSimulator/issues/132) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#137 Amazon CodeCatalyst](https://github.com/susumutomita/TenkaCloudSimulator/issues/137) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#165 Amazon CodeGuru](https://github.com/susumutomita/TenkaCloudSimulator/issues/165) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#168 Amazon DevOps Guru](https://github.com/susumutomita/TenkaCloudSimulator/issues/168) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#169 Amazon Forecast](https://github.com/susumutomita/TenkaCloudSimulator/issues/169) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#170 Amazon Fraud Detector](https://github.com/susumutomita/TenkaCloudSimulator/issues/170) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#171 AWS HealthImaging](https://github.com/susumutomita/TenkaCloudSimulator/issues/171) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#172 AWS HealthLake](https://github.com/susumutomita/TenkaCloudSimulator/issues/172) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#173 AWS HealthOmics](https://github.com/susumutomita/TenkaCloudSimulator/issues/173) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#179 Amazon Personalize](https://github.com/susumutomita/TenkaCloudSimulator/issues/179) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#190 Amazon EC2 Auto Scaling](https://github.com/susumutomita/TenkaCloudSimulator/issues/190) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#193 AWS Compute Optimizer](https://github.com/susumutomita/TenkaCloudSimulator/issues/193) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#201 Amazon Managed Grafana](https://github.com/susumutomita/TenkaCloudSimulator/issues/201) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#203 AWS Organizations](https://github.com/susumutomita/TenkaCloudSimulator/issues/203) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#218 AWS Deadline Cloud](https://github.com/susumutomita/TenkaCloudSimulator/issues/218) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#220 AWS Elemental MediaConnect](https://github.com/susumutomita/TenkaCloudSimulator/issues/220) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#221 AWS Elemental MediaConvert](https://github.com/susumutomita/TenkaCloudSimulator/issues/221) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#222 AWS Elemental MediaLive](https://github.com/susumutomita/TenkaCloudSimulator/issues/222) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#223 AWS Elemental MediaPackage](https://github.com/susumutomita/TenkaCloudSimulator/issues/223) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#224 AWS Elemental MediaTailor](https://github.com/susumutomita/TenkaCloudSimulator/issues/224) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#225 Amazon Interactive Video Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/225) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#226 AWS Application Discovery Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/226) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#228 AWS Database Migration Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/228) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#229 AWS DataSync](https://github.com/susumutomita/TenkaCloudSimulator/issues/229) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#231 AWS Mainframe Modernization](https://github.com/susumutomita/TenkaCloudSimulator/issues/231) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#232 AWS Migration Hub](https://github.com/susumutomita/TenkaCloudSimulator/issues/232) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#233 AWS Transfer Family](https://github.com/susumutomita/TenkaCloudSimulator/issues/233) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#235 AWS Transform MGN](https://github.com/susumutomita/TenkaCloudSimulator/issues/235) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#238 Amazon CloudFront](https://github.com/susumutomita/TenkaCloudSimulator/issues/238) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#239 AWS Direct Connect](https://github.com/susumutomita/TenkaCloudSimulator/issues/239) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#240 Elastic Load Balancing](https://github.com/susumutomita/TenkaCloudSimulator/issues/240) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#247 AWS Verified Access](https://github.com/susumutomita/TenkaCloudSimulator/issues/247) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#248 AWS Virtual Private Network](https://github.com/susumutomita/TenkaCloudSimulator/issues/248) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#250 Amazon Braket](https://github.com/susumutomita/TenkaCloudSimulator/issues/250) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#251 AWS Ground Station](https://github.com/susumutomita/TenkaCloudSimulator/issues/251) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#252 AWS Artifact](https://github.com/susumutomita/TenkaCloudSimulator/issues/252) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#253 Amazon Cognito](https://github.com/susumutomita/TenkaCloudSimulator/issues/253) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#254 AWS Directory Service](https://github.com/susumutomita/TenkaCloudSimulator/issues/254) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#263 AWS Network Firewall](https://github.com/susumutomita/TenkaCloudSimulator/issues/263) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#275 Amazon Elastic Block Store](https://github.com/susumutomita/TenkaCloudSimulator/issues/275) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#276 AWS Elastic Disaster Recovery](https://github.com/susumutomita/TenkaCloudSimulator/issues/276) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#280 Amazon Kinesis Video Streams](https://github.com/susumutomita/TenkaCloudSimulator/issues/280) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#282 AWS Cost Explorer](https://github.com/susumutomita/TenkaCloudSimulator/issues/282) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#290 AWS Auto Scaling](https://github.com/susumutomita/TenkaCloudSimulator/issues/290) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#291 AWS Transit Gateway](https://github.com/susumutomita/TenkaCloudSimulator/issues/291) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#292 AWS PrivateLink](https://github.com/susumutomita/TenkaCloudSimulator/issues/292) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#294 Amazon AI Operations](https://github.com/susumutomita/TenkaCloudSimulator/issues/294) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#295 AWS Application Auto Scaling](https://github.com/susumutomita/TenkaCloudSimulator/issues/295) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#298 AWS CodeStar Notifications](https://github.com/susumutomita/TenkaCloudSimulator/issues/298) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#300 Amazon SimpleDB](https://github.com/susumutomita/TenkaCloudSimulator/issues/300) | 現行main・保存候補・受入を個別確認 | 現行コード・保存branch・公式契約を照合し、依存と実装範囲を固定して着手。 |
| [#318 App Engine](https://github.com/susumutomita/TenkaCloudSimulator/issues/318) | 現行main・保存候補・受入を個別確認 | App Engine / `apps.create` creates the project's single application, which has no delete method, and `services.versions.create` deploys code |
| [#337 Bigtable](https://github.com/susumutomita/TenkaCloudSimulator/issues/337) | 現行main・保存候補・受入を個別確認 | Bigtable / `instances.create` creates an instance and its map of clusters in one request, which the one-resource create cannot represent |
| [#341 Cloud SQL](https://github.com/susumutomita/TenkaCloudSimulator/issues/341) | 現行main・保存候補・受入を個別確認 | Cloud SQL / `sqladmin.v1` flat paths have no `global`, `regions` or `zones` scope, which the flat conventions require, and many instance methods are lifecycle actions (`restart`, `failover`, `clone`, `import`, `executeSql`) |
| [#343 Cloud DNS](https://github.com/susumutomita/TenkaCloudSimulator/issues/343) | 現行main・保存候補・受入を個別確認 | Cloud DNS / `dns.v1` flat paths have no `global`, `regions` or `zones` scope, and a record set is named by two segments (`rrsets/{name}/{type}`) |
| [#366 Private Service Connect](https://github.com/susumutomita/TenkaCloudSimulator/issues/366) | 現行main・保存候補・受入を個別確認 | Private Service Connect / Endpoints are regional forwarding rules and producers use `serviceAttachments`; neither is bound (only global forwarding rules are) |
| [#373 Error Reporting](https://github.com/susumutomita/TenkaCloudSimulator/issues/373) | 現行main・保存候補・受入を個別確認 | Error Reporting / No create method: groups come from reported events (`events.report`), and `groups.update` only changes their tracking state |
| [#375 Cloud Profiler](https://github.com/susumutomita/TenkaCloudSimulator/issues/375) | 現行main・保存候補・受入を個別確認 | Cloud Profiler / `profiles.create` and `createOffline` collect profile data from a profiling agent; there is no configuration resource |
| [#376 Cloud Trace](https://github.com/susumutomita/TenkaCloudSimulator/issues/376) | 現行main・保存候補・受入を個別確認 | Cloud Trace / The only writes, `traces.batchWrite` and `spans.createSpan`, record trace data |
| [#389 BigQuery](https://github.com/susumutomita/TenkaCloudSimulator/issues/389) | 現行main・保存候補・受入を個別確認 | BigQuery / Datasets and tables hold row data (`tabledata.insertAll`) and jobs run queries (`jobs.insert`, `jobs.query`) |

### 新規要件 (1件)

公開環境・認証・共有データの仕様を先に固定します。公開配備や実クラウド操作を、ローカル実装の完了と混同しません。

| Issue | 現状 | 次の確認・残件 |
| --- | --- | --- |
| [#452 シミュレーターのWeb常設化・URLでの状態共有・ユーザー別の記録を検討する](https://github.com/susumutomita/TenkaCloudSimulator/issues/452) | 要件整理／実装未着手の確認対象 | 常設Web・URL状態共有・個人記録。配置先/認証/共有状態/記録範囲を固定し、公開配備と永続アクセスは別途権限を守る。 |
