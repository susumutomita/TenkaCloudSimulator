# Console Cloudscape 統一 設計

Issue 9 (`console を Cloudscape Design System で本体プロダクトと統一する`) の設計ゲート。
実装前に代替案・選定理由・エッジケースを残すという quality-bar の要求 (`docs/architecture/quality-bar.md`) を満たす。

## 問題

統合コンソール `apps/console` は Vite + React 19 に手書き `styles.css` (857 行) と `view.tsx` (588 行)
だけで構成され、コンポーネントライブラリを使っていない。TenkaCloud 本体 (participant-portal 等の
Cloudscape ベース SPA) と並べるとデザイントークン・余白・状態表現が揃わず、見た目の品質が明確に劣る。
本体側では simulator 問題がまだカタログ品質に達していないため `make local` から既定 OFF に gate されて
おり、その品質観点の 1 つがこの console のデザイン一貫性である。

behavior (`model.ts` / `client.ts` / `loader.ts` / `launch-token.ts`) は維持したまま、表示層だけを
Cloudscape へ差し替える。

## 現状のコンポーネント → Cloudscape 対応

`view.tsx` の各コンポーネントに割り当てる Cloudscape primitive。

| 現状 (bespoke) | Cloudscape 置換 |
| --- | --- |
| `app-shell` + `ShellHeader` + `shell-footer` | `AppLayout` (content 中心、navigation/tools 非表示) + `TopNavigation` (brand + protocol badge) |
| `ReadyConsole` の縦積み | `ContentLayout` (header に world hero) + `SpaceBetween` |
| `hero` セクション | `ContentLayout` の `header` に `Header` (variant h1、actions に Refresh `Button`) |
| `metrics` (`Metric` 4 枚) | `ColumnLayout` + `Box`、または `KeyValuePairs` |
| `panel` セクション | `Container` + `Header` (variant h2、eyebrow は `Header` description) |
| `ResourceGraph` / `provider-lane` | provider ごとに `Container` + `Cards` (または `Table`) |
| `ResourceCard` | `Cards` の item。status は `StatusIndicator`、meta は `KeyValuePairs` |
| `propertyCategories` の `details` | `ExpandableSection` (Properties は既定折り畳み維持) |
| `JsonEntry` / `properties` の `dl` | `KeyValuePairs`。複数行 JSON は `Box variant="code"` |
| `StatusBadge` | `StatusIndicator` (status → type mapping)。未知 status は `type="pending"` |
| `EventTimeline` / `EventItem` | `Container` + `Table` (sequence / type / timestamp / payload `ExpandableSection`) |
| `OutputList` | `KeyValuePairs` |
| `Diagnostics` / `DiagnosticItem` | `Table` または `Cards`。code は `StatusIndicator type="error"` |
| `ProviderOperationPanel` の `form` | `Form` + `FormField` + `Input` / `Textarea` + submit `Button` |
| `OperationField` | `FormField` (label) + `Input` |
| `ConsoleOperationResult` | `Alert` (error → `type="error"` role=alert、success → `type="success"`) |
| `LoadingState` | `Spinner` + `Box`。`aria-busy` は container に付与 |
| `ErrorState` | `Alert type="error"` (role=alert) + retry `Button` |
| empty-state テキスト | 各コンテナの `empty` slot |

`styles.css` (857 行) は Cloudscape の global-styles + design token に置き換え、残すのは console 固有の
最小レイアウト調整のみに削減する。

## 選択肢

### A. Cloudscape へ全面移行し、テストは現行の `renderToStaticMarkup` を維持

view を Cloudscape で再構築し、`console.test.tsx` の view assertion (HTML 文字列 `.toContain`) はそのまま
残す。差分が最小に見えるが、下記「重大リスク」のとおり Cloudscape は client 前提で `renderToStaticMarkup`
(DOM なし SSR) と相性が悪く、1 つでも component が SSR 中に `window` 等へ触れると view 系テストが全滅する。
`<span>Target</span><code>default</code>` のような exact-markup assertion も Cloudscape の生成 DOM では
成立しない。採用不可。

### B. Cloudscape へ全面移行し、view テストを client-side rendering へ移す

view を Cloudscape で再構築し、view 系テスト (`WorldConsoleView` / `ConsoleOperationResult` を
`renderToStaticMarkup` で検証している 4 ケース) を `@testing-library/react` + DOM 環境 (happy-dom) の
client render + role / text query へ書き換える。`client.ts` / `loader.ts` / `model.ts` / `launch-token.ts`
の behavior テスト (実 HTTP + 実 SQLite、No Mock) は一切変えない。表示層の刷新と表示層テストの刷新を
同一 PR に閉じられ、Cloudscape の a11y role を活用した堅牢な assertion になる。テスト基盤に
`@testing-library/react` と DOM 環境という新規 devDependency が要る。

### C. hybrid (レイアウトのみ Cloudscape、状態表現は bespoke を温存)

Container / Header / SpaceBetween 等のレイアウトだけ Cloudscape 化し、status badge や metric は手書きの
まま残す。テスト書き換えを避けられるが、「本体と視覚的に一貫」という受け入れ条件を満たさず、bespoke CSS も
大きく残る。中途半端で quality-bar (MVP 不可) に反する。採用不可。

### 選定

**B を採用する。** 受け入れ条件「Cloudscape で描画され本体と視覚的に一貫」「bespoke CSS を大幅削減」
「回帰なし」を満たせるのは全面移行だけで、全面移行は Cloudscape の client 前提と衝突するため view テストの
client-side 化が不可欠。behavior テスト (実 API / 実 DB) は無変更で No Mock を維持する。

## 重大リスク: Cloudscape と `renderToStaticMarkup`

現行 `console.test.tsx` は view を `react-dom/server` の `renderToStaticMarkup` で SSR し、返る HTML 文字列に
対して `.toContain` / `.toMatch` で検証している (loading / error / ready / empty / launch-token 秘匿 / 未実装
provider 診断の 6 経路)。Cloudscape component は `useLayoutEffect` / `useId` / CSS-in-JS / ブラウザ API を
前提とし、DOM の無い `renderToStaticMarkup` では警告・不安定出力・例外のいずれかを起こしうる。したがって
全面移行では view テストの client-side 化 (選択肢 B) が前提条件になる。実装着手時に最初のスライスとして、
代表 component (`Alert` / `StatusIndicator` / `Container`) を happy-dom + `@testing-library/react` で render
できることを Red-Green で確認してから本移行に入る (spike を最初の失敗テストとして残す)。

## エッジケース

- **loading / error / empty**: 3 状態すべてを Cloudscape で表現し、`aria-busy` (loading) と role=alert (error)
  の accessible シグナルを保持する。empty は各コンテナの `empty` slot でカバーする。
- **launch-token 秘匿**: error 表示に生 token (`tc_sim_v1...`) を絶対に出さない既存不変を維持する
  (テスト `errorView.not.toContain('tc_sim_v1')` を client 版でも保持)。
- **React 19 `useActionState` の operation form**: `Form` + `Button` に載せ替えても pending 表示
  (`Executing…` / disabled) と idempotency key の `crypto.randomUUID()` 既定値を維持する。
- **未実装 provider 診断**: `MissingProvider` code と source (`unavailable.json`) が Diagnostics に出続ける。
- **dark / light・responsive・a11y**: Cloudscape の標準に乗せ、bespoke media query を撤去する。
- **未知 status 値**: `StatusIndicator` の type mapping に無い status は `pending` にフォールバックし、
  fail loudly ではなく安全表示にする (status は provider 由来で任意文字列を取りうるため)。

## テスト戦略

- **behavior テスト (無変更)**: `client.ts` / `loader.ts` / `model.ts` / `launch-token.ts` は実 HTTP
  (`Bun.serve` + 実 provider) と実 SQLite (`SimulationStore`) で検証する既存ケースをそのまま使う。No Mock 維持。
- **view テスト (書き換え)**: `WorldConsoleView` / `ConsoleOperationResult` を `@testing-library/react` の
  client render + role / accessible name / text query へ移す。現行の文字列 assertion が担保している振る舞い
  (provider projection 表示、policy / reachability、output、event、operation form、3 状態、token 秘匿、未実装
  診断) を 1:1 で移送し、カバレッジ 100% を維持する。
- **BDD 日本語**: `describe` / `it` は日本語で振る舞いを表現する既存スタイルを踏襲する。

## 実装スライス (着手順)

1. spike: happy-dom + `@testing-library/react` を devDependency に追加し、Cloudscape 代表 component の client
   render を Red-Green で確認する。
2. shell: `AppLayout` + `TopNavigation` + `ContentLayout` へ骨格を移す。3 状態 (loading / error / ready) を
   Cloudscape 化し、view テストを client 版へ移送する。
3. ready 本体: metrics / resource graph / operation form / outputs / diagnostics / event timeline を順に
   Cloudscape 化し、各スライスでテストを緑に保つ。
4. `styles.css` を削減し、残す最小レイアウトだけ token ベースに書き換える。
5. gate: `nr typecheck` / `nr test:coverage` (100%) / `bun scripts/architecture-harness.ts --staged` /
   `make before-commit` をすべて緑にする。

## 未確定 (実装時に判断)

- resource / event / diagnostics を `Cards` と `Table` のどちらで出すか (情報密度と responsive で決める)。
- `@testing-library/react` の DOM 環境を happy-dom と jsdom のどちらにするか (Bun test との相性で決める)。
