---
name: architecture-harness
description: Go Simulatorのarchitecture/quality gateを確認する。
---

AGENTS.mdとdocs/design/0001-go-standalone.mdを読み、core/provider/AWS/serverの依存方向を確認する。
`make before-commit`を実行し、protocol/state/UI変更なら`make e2e`、model変更なら`TLA_JAR=/path/to/tla2tools.jar make formal`も実行する。
このrepoには旧TypeScript architecture-harness.tsは存在しない。未実行の検査を成功扱いしない。
