"""Require 90% statement coverage across the runtime packages in a Go profile.

Go can repeat blocks when several test binaries cover the same package. Merge
those execution counts before measuring, as go tool cover does.
"""
import sys
from pathlib import Path


def statement_coverage(profile):
    lines = profile.splitlines()
    if not lines or lines[0] not in ('mode: set', 'mode: count', 'mode: atomic'):
        raise ValueError('missing or invalid coverage mode')
    blocks = {}
    for line in lines[1:]:
        location, statements, count = line.rsplit(' ', 2)
        statements, count = int(statements), int(count)
        if statements < 0 or count < 0:
            raise ValueError('negative coverage data')
        previous = blocks.get(location)
        if previous and previous[0] != statements:
            raise ValueError('inconsistent statement count')
        blocks[location] = (statements, count > 0 or bool(previous and previous[1]))
    total = sum(n for n, _ in blocks.values())
    if not total:
        raise ValueError('empty coverage profile')
    return sum(n for n, hit in blocks.values() if hit) / total


if __name__ == '__main__':
    result = statement_coverage(Path(sys.argv[1]).read_text())
    print(f'Runtime statement coverage: {result:.2%} (required: 90%)')
    if result < 0.90:
        sys.exit(1)
