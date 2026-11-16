"""Enforce 90% full-module Go statement and unfiltered Cobertura line coverage."""
import json
import os
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET

MODES = ('mode: set', 'mode: count', 'mode: atomic')


def statement_counts(path):
    lines = Path(path).read_text().splitlines()
    if not lines or lines[0] not in MODES:
        raise ValueError('missing or invalid Go coverage mode')
    blocks = {}
    for line in lines[1:]:
        location, size, count = line.rsplit(' ', 2)
        size, count = int(size), int(count)
        if not re.fullmatch(r'.+:\d+\.\d+,\d+\.\d+', location) or size < 0 or count < 0:
            raise ValueError('invalid Go coverage block')
        previous = blocks.get(location)
        if previous and previous[0] != size:
            raise ValueError('inconsistent duplicate coverage block')
        blocks[location] = (size, count > 0 or bool(previous and previous[1]))
    total = sum(size for size, _ in blocks.values())
    if total == 0:
        raise ValueError('Go coverage profile has no statements')
    return sum(size for size, hit in blocks.values() if hit), total


def line_counts(path):
    root = ET.parse(path).getroot()
    covered, total = int(root.attrib['lines-covered']), int(root.attrib['lines-valid'])
    if root.tag != 'coverage' or not 0 <= covered <= total or total == 0:
        raise ValueError('invalid or empty Cobertura line counts')
    return covered, total


def claim(tested, packages):
    for package in packages:
        if package in tested:
            raise ValueError('package tested by more than one shard: ' + package)
        tested.add(package)


def check_parts(packages, parts):
    """Shards that split one package list must run every listed test exactly once."""
    name = ' '.join(packages)
    if not all(isinstance(p, dict) and set(p) == {'index', 'count', 'listed', 'tests'} and isinstance(p['tests'], list) for p in parts):
        raise ValueError('invalid shard part: ' + name)
    count, listed = parts[0]['count'], parts[0]['listed']
    if type(count) is not int or not isinstance(listed, list) or not listed or len(set(listed)) != len(listed):
        raise ValueError('invalid listed tests: ' + name)
    if any(p['count'] != count or p['listed'] != listed for p in parts) or sorted(p['index'] for p in parts) != list(range(count)):
        raise ValueError('shard parts are missing, repeated or from another split: ' + name)
    ran = [test for p in parts for test in p['tests']]
    if len(ran) != len(set(ran)) or set(ran) != set(listed):
        raise ValueError('shard parts do not run each listed test exactly once: ' + name)


def merge_shards(output, expected_packages, head, manifests):
    expected = Path(expected_packages).read_text().split()
    if not expected or len(set(expected)) != len(expected):
        raise ValueError('expected package list is empty or repeats a package')
    if not manifests:
        raise ValueError('no shard manifests')
    tested, mode, blocks, split = set(), None, [], {}
    for manifest_path in map(Path, manifests):
        manifest = json.loads(manifest_path.read_text())
        if not isinstance(manifest, dict) or set(manifest) - {'part'} != {'head', 'packages', 'profile'}:
            raise ValueError('invalid shard manifest: ' + manifest_path.name)
        if manifest['head'] != head:
            raise ValueError('shard was tested at another head: ' + manifest_path.name)
        packages, profile = manifest['packages'], manifest['profile']
        if not isinstance(packages, list) or not packages or not all(isinstance(p, str) and p for p in packages):
            raise ValueError('shard lists no packages: ' + manifest_path.name)
        if not isinstance(profile, str) or Path(profile).name != profile:
            raise ValueError('shard profile must sit beside its manifest: ' + manifest_path.name)
        if 'part' in manifest:
            split.setdefault(tuple(packages), []).append(manifest['part'])
        else:
            claim(tested, packages)
        lines = (manifest_path.parent / profile).read_text().splitlines()
        if not lines or lines[0] not in MODES or mode not in (None, lines[0]):
            raise ValueError('missing or mismatched shard coverage mode: ' + profile)
        mode = lines[0]
        blocks += lines[1:]
    for packages, parts in split.items():
        check_parts(packages, parts)
        claim(tested, packages)
    if tested != set(expected):
        missing, extra = sorted(set(expected) - tested), sorted(tested - set(expected))
        raise ValueError(f'shards do not cover the module exactly: missing={missing} extra={extra}')
    Path(output).write_text('\n'.join([mode, *blocks]) + '\n')


def main(args):
    if args[:1] == ['merge']:
        if len(args) < 5:
            raise ValueError('usage: check-coverage.py merge OUTPUT EXPECTED_PACKAGES HEAD MANIFEST...')
        merge_shards(args[1], args[2], args[3], args[4:])
        return
    if not 1 <= len(args) <= 2:
        raise ValueError('usage: check-coverage.py PROFILE [COBERTURA]')
    metrics = [('Go statements (full module)', *statement_counts(args[0]))]
    if len(args) == 2:
        metrics.append(('Source lines (unfiltered Cobertura)', *line_counts(args[1])))
    passed = all(covered * 100 >= total * 90 for _, covered, total in metrics)
    for name, covered, total in metrics:
        print(f'{name}: {covered}/{total} ({100 * covered / total:.2f}%; minimum 90%)')
    if len(args) == 2:
        summary = '## Go coverage\n\n| Metric | Covered / total | Coverage | Minimum |\n| --- | --- | --- | --- |\n'
        summary += ''.join(f'| {name} | {covered} / {total} | {100 * covered / total:.2f}% | 90% |\n' for name, covered, total in metrics)
        summary += '\nGate: **' + ('PASS' if passed else 'FAIL') + '**. Includes cmd/simulator and every internal package.\n'
        summary += '\nDownload the coverage artifact for the raw Go profile, unfiltered Cobertura XML and HTML source view.\n'
        Path(args[1]).with_name('coverage-summary.md').write_text(summary)
        if os.environ.get('GITHUB_STEP_SUMMARY'):
            with Path(os.environ['GITHUB_STEP_SUMMARY']).open('a') as output:
                output.write(summary)
    if not passed:
        raise SystemExit('Coverage is below the required 90% floor.')


if __name__ == '__main__':
    main(sys.argv[1:])
