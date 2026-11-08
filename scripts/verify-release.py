#!/usr/bin/env python3
"""Verify the closed release manifest before executing or publishing any asset."""
import argparse
import hashlib
import json
from pathlib import Path
import re

TARGETS = ('linux_amd64', 'linux_arm64', 'darwin_amd64', 'darwin_arm64')
ASSETS = {f'tenkacloud-simulator_{target}' for target in TARGETS} | {'BUILDINFO.json'}


def verify(directory, commit, version=None):
    directory = Path(directory)
    names = {p.name for p in directory.iterdir()}
    if names != ASSETS | {'SHA256SUMS'}:
        raise ValueError('release must contain exactly the four binaries, BUILDINFO and SHA256SUMS')
    if any(p.is_symlink() or not p.is_file() for p in directory.iterdir()):
        raise ValueError('release assets must be regular files, not links')
    entries = {}
    for line in (directory / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9_.-]+)', line)
        if not match or match[2] not in ASSETS or match[2] in entries:
            raise ValueError('invalid, duplicate or unexpected checksum entry')
        entries[match[2]] = match[1]
    if set(entries) != ASSETS:
        raise ValueError('checksum manifest is incomplete')
    for name, expected in entries.items():
        with (directory / name).open('rb') as stream:
            actual = hashlib.file_digest(stream, 'sha256').hexdigest()
        if actual != expected:
            raise ValueError('checksum mismatch: ' + name)
    info = json.loads((directory / 'BUILDINFO.json').read_text())
    if not re.fullmatch(r'[0-9a-f]{40}', commit) or info['source_commit'] != commit:
        raise ValueError('release source commit does not match the selected source')
    if version is not None and info['version'] != version:
        raise ValueError('release version does not match the selected tag')
    if info['source_repository'] != 'https://github.com/susumutomita/TenkaCloudSimulator':
        raise ValueError('unexpected source repository')
    if info['build_flags'] != ['-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w']:
        raise ValueError('unexpected release build flags')
    if info['targets'] != [t.replace('_', '/') for t in TARGETS] or info['cgo_enabled'] is not False:
        raise ValueError('unexpected build targets or CGO configuration')
    if not info['toolchain'].startswith('go version go1.26.10 '):
        raise ValueError('release requires the pinned Go 1.26.10 toolchain')
    return info


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--commit', required=True)
    parser.add_argument('--version')
    args = parser.parse_args()
    verify(args.directory, args.commit, args.version)
    print('Complete release manifest, checksums, toolchain and source verified.')


if __name__ == '__main__':
    main()
