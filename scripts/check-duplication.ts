#!/usr/bin/env bun
/**
 * jscpd ベースライン・ラチェット (コピー&ペースト検出)。
 *
 * 目的は「重複ゼロ」ではない。責務分離のための意図的な類似実装は存在してよい。
 * 検出したいのは既存実装を調べずに持ち込まれる新しいコピー&ペースト / 再実装なので、
 * 現状の重複量を area (workspace) 単位で baseline に焼き込み、それを超えたとき
 * だけ失敗する。判断の正本: docs/adr/0015-duplication-ratchet-and-dead-code-report.md
 *
 * 使い方:
 *   bun scripts/check-duplication.ts            — gate (増加で exit 1)
 *   bun scripts/check-duplication.ts --update   — baseline を現状に更新
 *
 * baseline を増やす方向の更新は、なぜ重複が正当かを PR body に書く。減らす方向は
 * いつでも歓迎で、gate が actual < baseline を検出したら ratchet-down を促す。
 */

import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const JSCPD_BIN = join(REPO_ROOT, 'node_modules', '.bin', 'jscpd');

/** providers / apps / tools は package 単位、それ以外は最上位 directory を area とする。 */
const NESTED_ROOTS = new Set(['providers', 'apps', 'tools']);

interface CloneFileRef {
  readonly name: string;
  readonly start: number;
  readonly end: number;
}

export interface Clone {
  readonly firstFile: CloneFileRef;
  readonly secondFile: CloneFileRef;
  readonly lines: number;
  readonly format: string;
}

export interface JscpdReport {
  readonly duplicates: readonly Clone[];
}

export function areaOf(path: string): string {
  const parts = path.split('/');
  const head = parts[0] ?? path;
  if (NESTED_ROOTS.has(head) && parts.length > 1) {
    return `${head}/${parts[1]}`;
  }
  return head;
}

/**
 * area ごとの重複行数。クローンの行数を firstFile / secondFile 双方の area に加算する
 * (= 同一 area 内クローンはその area に 2 回乗る)。移動を伴わない編集では安定し、
 * 新しいコピー&ペーストは必ずどこかの area の増分として現れる。
 */
export function aggregateDuplicatedLines(
  clones: readonly Clone[]
): Record<string, number> {
  const totals: Record<string, number> = {};
  for (const clone of clones) {
    for (const file of [clone.firstFile, clone.secondFile]) {
      const area = areaOf(file.name);
      totals[area] = (totals[area] ?? 0) + clone.lines;
    }
  }
  return totals;
}

export interface AreaDelta {
  readonly area: string;
  readonly baseline: number;
  readonly actual: number;
}

export interface Comparison {
  readonly regressions: readonly AreaDelta[];
  readonly improvements: readonly AreaDelta[];
}

/** baseline 未記載の area は 0 扱い (= 新 area への持ち込みも増加として検出する)。 */
export function compareToBaseline(
  actual: Record<string, number>,
  baseline: Record<string, number>
): Comparison {
  const areas = new Set([...Object.keys(actual), ...Object.keys(baseline)]);
  const regressions: AreaDelta[] = [];
  const improvements: AreaDelta[] = [];
  for (const area of [...areas].sort()) {
    const a = actual[area] ?? 0;
    const b = baseline[area] ?? 0;
    if (a > b) {
      regressions.push({ area, baseline: b, actual: a });
    } else if (a < b) {
      improvements.push({ area, baseline: b, actual: a });
    }
  }
  return { regressions, improvements };
}

/** 失敗時の調査の取っ掛かり: 対象 area に触れるクローンを行数の大きい順に返す。 */
export function largestClonesTouching(
  clones: readonly Clone[],
  area: string,
  limit: number
): readonly Clone[] {
  return clones
    .filter(
      (c) =>
        areaOf(c.firstFile.name) === area || areaOf(c.secondFile.name) === area
    )
    .toSorted((x, y) => y.lines - x.lines)
    .slice(0, limit);
}

export function cloneLabel(clone: Clone): string {
  const f = clone.firstFile;
  const s = clone.secondFile;
  return `${f.name}:${f.start}-${f.end} ⇄ ${s.name}:${s.start}-${s.end} (${clone.lines} lines)`;
}

export function baselinePath(root: string): string {
  return join(root, 'scripts', 'duplication-baseline.json');
}

export function reportPath(root: string): string {
  return join(root, '.jscpd-report', 'jscpd-report.json');
}

export function readJson<T>(path: string, missingMessage: string): T {
  if (!existsSync(path)) {
    throw new Error(missingMessage);
  }
  return JSON.parse(readFileSync(path, 'utf8')) as T;
}

export function readBaseline(root: string): Record<string, number> {
  const path = baselinePath(root);
  if (!existsSync(path)) {
    return {};
  }
  return JSON.parse(readFileSync(path, 'utf8')) as Record<string, number>;
}

/**
 * 検査用の実行は console 出力を抑え、JSON レポートだけ生成する (CLI 引数が
 * .jscpd.json の reporters を上書きする)。人間向けの詳細表示は `make dup_report`。
 */
export function runJscpd(root: string): JscpdReport {
  const result = spawnSync(JSCPD_BIN, ['--reporters', 'json', '--silent'], {
    cwd: root,
    stdio: ['ignore', 'inherit', 'inherit'],
  });
  if (result.status !== 0) {
    throw new Error(`jscpd exited with status ${result.status ?? 'unknown'}`);
  }
  const report = readJson<JscpdReport>(
    reportPath(root),
    `jscpd report not found at ${reportPath(root)}`
  );
  // 生成物を lint 対象に残さないよう、読み取り後に report directory を片付ける。
  rmSync(join(root, '.jscpd-report'), { recursive: true, force: true });
  return report;
}

function sortedRecord(totals: Record<string, number>): Record<string, number> {
  return Object.fromEntries(
    Object.entries(totals).sort(([a], [b]) => a.localeCompare(b))
  );
}

export function main(
  argv: readonly string[],
  root: string = REPO_ROOT
): number {
  const update = argv.includes('--update');
  const report = runJscpd(root);
  const actual = aggregateDuplicatedLines(report.duplicates);

  if (update) {
    const path = baselinePath(root);
    writeFileSync(
      path,
      `${JSON.stringify(sortedRecord(actual), null, 2)}\n`,
      'utf8'
    );
    console.log(`duplication baseline updated: ${path}`);
    for (const [area, lines] of Object.entries(sortedRecord(actual))) {
      console.log(`  ${area}: ${lines} duplicated lines`);
    }
    return 0;
  }

  const baseline = readBaseline(root);
  const { regressions, improvements } = compareToBaseline(actual, baseline);

  if (improvements.length > 0) {
    console.log(
      'duplication decreased below the baseline — consider ratcheting down:'
    );
    for (const d of improvements) {
      console.log(`  ${d.area}: ${d.baseline} → ${d.actual} duplicated lines`);
    }
    console.log('  (bun scripts/check-duplication.ts --update)');
  }

  if (regressions.length === 0) {
    console.log('OK duplication is at or below the baseline for every area.');
    return 0;
  }

  console.error(
    'NG duplication increased vs scripts/duplication-baseline.json:'
  );
  for (const d of regressions) {
    console.error(
      `  ${d.area}: baseline ${d.baseline} → actual ${d.actual} duplicated lines`
    );
    for (const clone of largestClonesTouching(report.duplicates, d.area, 5)) {
      console.error(`    - ${cloneLabel(clone)}`);
    }
  }
  console.error(
    [
      '',
      'まず既存実装を探して再利用・抽出で解消してください (bunx jscpd で全クローン表示)。',
      '責務分離のための意図的な重複なら、PR body に理由を書いた上で',
      '`bun scripts/check-duplication.ts --update` で baseline を更新します。',
    ].join('\n')
  );
  return 1;
}

if (import.meta.main) process.exit(main(process.argv.slice(2)));
