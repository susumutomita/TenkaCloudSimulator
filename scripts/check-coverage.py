"""Enforce 90% full-module Go statement and unfiltered Cobertura line coverage."""
import os
from pathlib import Path
import re
import sys
import xml.etree.ElementTree as ET


def statement_counts(path):
    lines = Path(path).read_text().splitlines()
    if not lines or lines[0] not in ('mode: set', 'mode: count', 'mode: atomic'):
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


def main(args):
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
