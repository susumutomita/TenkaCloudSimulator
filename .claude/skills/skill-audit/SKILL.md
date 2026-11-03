---
name: skill-audit
description: ローカルskillの参照先、ツール権限、実行可能コマンドを確認する。
---

SKILL.mdの目的・trigger・必要ツール・相対リンクを読む。外部送信や権限拡大、削除、秘密読取が目的に対して過剰でないか確認する。参照ファイルとコマンドの実在をrgで確認し、危険な処理を実行して検査しない。ユーザーの承認範囲を超えない。
Go Simulatorの品質契約はAGENTS.mdとmake before-commit。旧Bun architecture-harness.tsや存在しないADRを前提にしない。修正後の参照先と実コマンドの結果を報告する。
