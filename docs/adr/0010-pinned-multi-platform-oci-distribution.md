# ADR-0010: digest 固定の multi-platform OCI image を配布する

## Status

Accepted

## Context

TenkaCloud は local machine と Codespaces から同じ Simulator release を起動します。source
checkout の dependency 状態に launcher が依存すると、protocol が同じでも実行物が変わり、
participant ごとの再現性と rollback が失われます。Apple Silicon と Linux runner の両方を
支える必要もあります。

## Decision

production server と Console を 1 つの OCI image にまとめ、GHCR へ `linux/amd64` と
`linux/arm64` の manifest list として公開します。build と runtime の Bun base image は tag
だけでなく multi-platform digest まで固定します。release workflow の action と QEMU image も
commit または digest へ固定します。

runtime は非 root user、read-only root filesystem で動作できる構成とし、書き込み先を
`TENKACLOUD_SIMULATOR_STATE_DIR` だけに限定します。container は内部で `0.0.0.0` に bind
できますが、launcher は host の `127.0.0.1` にだけ publish します。launch secret と host 側
public origin は image に埋め込まず、起動ごとに渡します。

TenkaCloud は mutable tag ではなく published manifest digest を設定として保持します。rollback
は以前の digest へ戻し、protocol compatibility を capability discovery で確認します。

## Consequences

- local machine と Codespaces が同じ byte-addressed release を使える。
- amd64 と arm64 を 1 つの manifest digest で pin できる。
- image build 後に non-root、health check、read-only root、state volume を実 container で検証する
  必要がある。
- base image または GitHub Action の更新は digest 差分として review される。
