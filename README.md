# TenkaCloud Simulator

TenkaCloud Simulator は、TenkaCloud の問題カタログで使う AWS、Azure、
GCP、さくらのクラウド API と観測可能な状態遷移を、実クラウドへ接続せずに
再現するローカル実行環境です。

汎用クラウドの完全な複製は目指しません。問題の IaC、CLI、SDK、採点処理が
実際に使う resource、operation、fidelity を catalog scanner で抽出し、実装済み
capability と一致した問題だけを起動します。不足した capability は resource を
作る前に診断付きで拒否します。

## 状態

Issue 2574 の実装中です。設計と公開契約の正本は次の文書です。

- [設計](./docs/design/2026-07-12-tenkacloud-simulator.md)
- [protocol](./docs/architecture/protocol.md)
- [Definition of Done](./docs/architecture/quality-bar.md)
- [ADR-0007](./docs/adr/0007-event-sourced-provider-plugin-architecture.md)

## アーキテクチャ

```text
CloudFormation / Bicep / Infrastructure Manager / AppRun
AWS CLI / az / gcloud / usacloud / SDK / Console
                         |
                  provider gateways
                         |
                 command dispatcher
                         |
          team-scoped simulation world
     graph / policy / network / clock / events
                         |
             optional workload containers
```

Console と CLI は専用の状態を持たず、同じ versioned API を呼びます。provider
module は共通 plugin contract を実装し、core は provider 名による分岐を持ちません。

## リポジトリ構成

```text
contracts/                     OpenAPI、capability、snapshot schema
core/                          world、graph、events、policy、network
providers/{aws,azure,gcp,sakura}/
apps/api/                      lifecycle API と provider gateway
apps/console/                  共通 Web Console
tools/catalog-scanner/         IaC と metadata の静的解析
tools/cli/                     API を操作する CLI
conformance/                   provider 契約と差分検査
```

## 開発

```bash
make install
make dev
make test
make typecheck
make build
make before-commit
```

Bun、Hono、Vite、React、Biome を使用します。依存 package の lifecycle script は
デフォルトで無効です。

## OCI image

production image は API と Console を同じ origin で提供します。mutable tag は検証用にだけ
使い、TenkaCloud からは publish 後の manifest digest を指定します。

```bash
docker build --tag tenkacloud-simulator:local .
docker run --read-only --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --publish 127.0.0.1:7777:7777 \
  --env TENKACLOUD_SIMULATOR_LAUNCH_SECRET="$TENKACLOUD_SIMULATOR_LAUNCH_SECRET" \
  --env TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID="$TENKACLOUD_SIMULATOR_AWS_ACCESS_KEY_ID" \
  --env TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL="$TENKACLOUD_SIMULATOR_AZURE_CREDENTIAL" \
  --env TENKACLOUD_SIMULATOR_GCP_CREDENTIAL="$TENKACLOUD_SIMULATOR_GCP_CREDENTIAL" \
  --env TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL="$TENKACLOUD_SIMULATOR_SAKURA_CREDENTIAL" \
  --env TENKACLOUD_SIMULATOR_PUBLIC_ORIGIN=http://127.0.0.1:7777 \
  --volume "$PWD/.simulator-state:/var/lib/tenkacloud-simulator" \
  tenkacloud-simulator:local
```

image は non-root で起動し、container mode だけが内部 `0.0.0.0` bind を使います。host publish
address は必ず loopback にします。`.devcontainer/` は同じ Bun version を持ち、port 7777 を
Codespaces の private forwarded port として宣言します。

## 5 operation lifecycle API

```text
GET    /v1/capabilities
POST   /v1/worlds
POST   /v1/worlds/{worldId}/deployments
GET    /v1/worlds/{worldId}/deployments/{deploymentId}
DELETE /v1/worlds/{worldId}
```

provider native API と共通 command API は、上記 lifecycle が所有する同じ world を
操作します。初期 protocol identifier は `2026-07-11` です。version policy と error envelope は
[protocol](./docs/architecture/protocol.md) を参照してください。

## セキュリティ境界

- 実クラウド credential を受け付けません。
- world を `tenantId / eventId / teamId / deploymentId` で隔離する。
- workload container は非特権かつ egress deny をデフォルトにする。
- 未実装 operation を成功扱いにしません。
- snapshot の import は schema と size limit の検証後に適用する。

## ライセンス

[MIT](./LICENSE)
