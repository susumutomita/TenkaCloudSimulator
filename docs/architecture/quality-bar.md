# Quality Bar（Definition of Done）

完了は固定手順の消化ではなく、Simulator の contract と観測可能な runtime behavior が受け入れ条件を満たすことを証拠で示せる状態です。

## Definition of Done

- capability contract、provider implementation、conformance の関係が一貫している。
- `core/` は provider 非依存で、problem ID または cloud vendor literal による分岐を持たない。
- application source root は workspace package かどうかに関係なく共有 strict option で typecheck される。
- container、Docker socket、filesystem、network、process の trust boundary と失敗経路を扱う。
- production code に仮実装、silent fallback、問題固有 shortcut、不要な重複を残さない。
- UI 変更では loading、empty、error、success と関連する accessibility を確認する。
- 変更に最も近い test または実行経路を使い、required CI を通す。
- 実 cloud、production image、cross-repository checkout でしか確認できない条件は、未検証事項と確認方法を明記する。

## Test strategy

- pure logic は unit、contract は conformance、provider/runtime integration は integration、container behavior は production image E2E で検証する。
- 外部 API、時刻、file、process、container boundary は test double で制御してよい。実接続でしか確認できない契約には別の integration path を持つ。
- coverage は blind spot の指標として使い、既存 CI 閾値を満たす。数値だけを上げる assertion を追加しない。
- TDD の順序、テストタイトルの言語、blanket No Mock を一律に要求しない。最も確実で安価な回帰検出を選ぶ。

## Not completion criteria

- `Plan.md`、設計文書、Issue を作ったこと。
- 特定 Skill、review、固定 role の subagent を実行したこと。
- lint、coverage、CI だけが緑で、受け入れ条件または runtime behavior を確認していないこと。

複雑な設計判断は必要に応じて文書化する。文書作成をすべての変更へ課す ceremony にはしない。
