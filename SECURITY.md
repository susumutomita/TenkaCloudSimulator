# Security Policy

## サポート対象

TenkaCloud Simulator は最新の `main` branch と、TenkaCloud が pin する release image を
サポートします。protocol compatibility は
[Simulator Protocol](./docs/architecture/protocol.md) を正本とします。

## 脆弱性の報告

security issue は public Issue に書かず、次の private 経路で報告してください。

- [GitHub Private vulnerability reporting](https://github.com/susumutomita/TenkaCloudSimulator/security/advisories/new)
- `oyster880@gmail.com`

48 時間以内に受領確認を返します。報告には影響する protocol version、provider、resource、
operation、再現手順を含めてください。実 credential や participant secret は添付しないで
ください。

## Security boundary

Simulator は untrusted IaC、metadata、snapshot、provider request、workload image を処理します。
主な security invariant は次のとおりです。

- 実 cloud credential を受け付けません。
- world を tenant、event、team、deployment で隔離する。
- workload は non-privileged、resource bounded、egress deny をデフォルトにする。
- image は digest と allowlist を検証する。
- archive、template、snapshot の path traversal と symlink escape を拒否する。
- event、log、diagnostic に secret と credential を保存しません。
- unsupported operation を成功扱いにしません。

設計は [Simulator 設計](./docs/design/2026-07-12-tenkacloud-simulator.md)、supply chain
invariant は [ADR-0001](./docs/adr/0001-supply-chain-hardening.md) と
[harness](./docs/architecture/harness.md) を参照してください。

## 報告に含める情報

- 影響する release、protocol identifier、provider、operation。
- namespace isolation または privilege boundary への影響。
- network request と event sequence を含む最小再現手順。
- secret を除いた log と diagnostic。
- 回避策または修正案があればその内容。
