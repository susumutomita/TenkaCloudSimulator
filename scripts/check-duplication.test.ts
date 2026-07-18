import { afterAll, describe, expect, it } from 'bun:test';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import {
  aggregateDuplicatedLines,
  areaOf,
  baselinePath,
  type Clone,
  cloneLabel,
  compareToBaseline,
  largestClonesTouching,
  main,
  readBaseline,
  readJson,
  reportPath,
  runJscpd,
} from './check-duplication';

const tempRoots: string[] = [];

afterAll(() => {
  for (const root of tempRoots) {
    rmSync(root, { recursive: true, force: true });
  }
});

/**
 * 実 jscpd が実 file を走査する最小 fixture repo を作る。src/a.ts と src/b.ts は
 * 同一内容 (= 確実に 1 クローン検出される)。No Mock: バイナリも report も本物を使う。
 */
function makeFixtureRoot(): string {
  const root = mkdtempSync(path.join(tmpdir(), 'dup-ratchet-'));
  tempRoots.push(root);
  mkdirSync(path.join(root, 'src'), { recursive: true });
  mkdirSync(path.join(root, 'scripts'), { recursive: true });
  writeFileSync(
    path.join(root, '.jscpd.json'),
    JSON.stringify({
      format: ['typescript'],
      pattern: '{src,scripts}/**/*.ts',
      minTokens: 30,
      minLines: 5,
      reporters: ['json'],
      output: '.jscpd-report',
      absolute: false,
      gitignore: false,
    })
  );
  const body = [
    'export function sample(value: number): number {',
    '  const doubled = value * 2;',
    '  const tripled = value * 3;',
    '  const combined = doubled + tripled;',
    '  const scaled = combined * value;',
    '  const shifted = scaled + doubled;',
    '  const capped = Math.min(shifted, 1000);',
    '  return capped + tripled + doubled + scaled;',
    '}',
    '',
  ].join('\n');
  const scriptBody = body
    .replaceAll('sample', 'scripted')
    .replaceAll('1000', '2000');
  writeFileSync(path.join(root, 'src', 'a.ts'), body);
  writeFileSync(path.join(root, 'src', 'b.ts'), body);
  writeFileSync(path.join(root, 'scripts', 'x.ts'), scriptBody);
  writeFileSync(path.join(root, 'scripts', 'y.ts'), scriptBody);
  return root;
}

function clone(first: string, second: string, lines: number): Clone {
  return {
    firstFile: { name: first, start: 1, end: lines },
    secondFile: { name: second, start: 1, end: lines },
    lines,
    format: 'typescript',
  };
}

describe('areaOf', () => {
  it('providers / apps / tools は package 単位の area を返す', () => {
    expect(areaOf('providers/aws/src/deploy.ts')).toBe('providers/aws');
    expect(areaOf('apps/console/src/app.tsx')).toBe('apps/console');
    expect(areaOf('tools/cli/src/bin.ts')).toBe('tools/cli');
  });

  it('それ以外は最上位 directory を area として返す', () => {
    expect(areaOf('core/src/canonical.ts')).toBe('core');
    expect(areaOf('scripts/check-duplication.ts')).toBe('scripts');
    expect(areaOf('providers')).toBe('providers');
  });
});

describe('aggregateDuplicatedLines', () => {
  it('クローンの行数を両端の area にそれぞれ加算する', () => {
    const totals = aggregateDuplicatedLines([
      clone('core/src/a.ts', 'providers/aws/src/b.ts', 10),
      clone('core/src/c.ts', 'core/src/d.ts', 7),
    ]);
    expect(totals).toEqual({ core: 24, 'providers/aws': 10 });
  });
});

describe('compareToBaseline', () => {
  it('baseline 超過を regressions、下回りを improvements として返す', () => {
    const { regressions, improvements } = compareToBaseline(
      { core: 30, scripts: 5 },
      { core: 20, scripts: 10 }
    );
    expect(regressions).toEqual([{ area: 'core', baseline: 20, actual: 30 }]);
    expect(improvements).toEqual([
      { area: 'scripts', baseline: 10, actual: 5 },
    ]);
  });

  it('baseline 未記載の area への持ち込みも増加として検出する', () => {
    const { regressions } = compareToBaseline({ 'apps/api': 8 }, {});
    expect(regressions).toEqual([{ area: 'apps/api', baseline: 0, actual: 8 }]);
  });
});

describe('largestClonesTouching', () => {
  it('対象 area に触れるクローンを行数の大きい順に limit 件返す', () => {
    const clones = [
      clone('core/src/a.ts', 'core/src/b.ts', 5),
      clone('core/src/c.ts', 'apps/api/src/d.ts', 12),
      clone('scripts/e.ts', 'scripts/f.ts', 9),
    ];
    const top = largestClonesTouching(clones, 'core', 1);
    expect(top).toHaveLength(1);
    expect(top[0]?.lines).toBe(12);
  });
});

describe('cloneLabel', () => {
  it('両端の file と範囲、行数を 1 行で表現する', () => {
    expect(cloneLabel(clone('src/a.ts', 'src/b.ts', 3))).toBe(
      'src/a.ts:1-3 ⇄ src/b.ts:1-3 (3 lines)'
    );
  });
});

describe('readJson', () => {
  it('file が無いときは指定 message で失敗する', () => {
    expect(() =>
      readJson('/nonexistent/report.json', 'report missing')
    ).toThrow('report missing');
  });
});

describe('readBaseline', () => {
  it('baseline file が無いときは空の record を返す', () => {
    const root = makeFixtureRoot();
    expect(readBaseline(root)).toEqual({});
  });
});

describe('runJscpd', () => {
  it('実 jscpd を実行して report JSON を返す', () => {
    const root = makeFixtureRoot();
    const report = runJscpd(root);
    expect(existsSync(reportPath(root))).toBe(false);
    const areas = Object.keys(
      aggregateDuplicatedLines(report.duplicates)
    ).sort();
    expect(areas).toEqual(['scripts', 'src']);
  });

  it('jscpd が異常終了したときは status 付きで失敗する', () => {
    const root = makeFixtureRoot();
    const config = JSON.parse(
      readFileSync(path.join(root, '.jscpd.json'), 'utf8')
    );
    config.threshold = 0;
    writeFileSync(path.join(root, '.jscpd.json'), JSON.stringify(config));
    expect(() => runJscpd(root)).toThrow(/jscpd exited with status/);
  });
});

describe('main', () => {
  it('--update は baseline を現状の重複量で書き出して 0 を返す', () => {
    const root = makeFixtureRoot();
    expect(main(['--update'], root)).toBe(0);
    const baseline = JSON.parse(readFileSync(baselinePath(root), 'utf8'));
    expect(baseline.src).toBeGreaterThan(0);
  });

  it('baseline と一致しているときは 0 を返す', () => {
    const root = makeFixtureRoot();
    main(['--update'], root);
    expect(main([], root)).toBe(0);
  });

  it('baseline が無い repo に重複があるときは 1 を返す (新規持ち込み検出)', () => {
    const root = makeFixtureRoot();
    expect(main([], root)).toBe(1);
  });

  it('重複が baseline を下回るときは ratchet-down を促しつつ 0 を返す', () => {
    const root = makeFixtureRoot();
    main(['--update'], root);
    const inflated = JSON.parse(readFileSync(baselinePath(root), 'utf8'));
    inflated.src += 100;
    inflated.ghost = 50;
    writeFileSync(baselinePath(root), JSON.stringify(inflated));
    expect(main([], root)).toBe(0);
  });

  it('重複が baseline を超えたときは 1 を返す', () => {
    const root = makeFixtureRoot();
    main(['--update'], root);
    const body = readFileSync(path.join(root, 'src', 'a.ts'), 'utf8');
    writeFileSync(path.join(root, 'src', 'c.ts'), body);
    expect(main([], root)).toBe(1);
  });

  it('report path は root 配下の .jscpd-report を指す', () => {
    expect(reportPath('/repo')).toBe('/repo/.jscpd-report/jscpd-report.json');
  });
});
