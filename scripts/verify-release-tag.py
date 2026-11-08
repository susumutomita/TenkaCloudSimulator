#!/usr/bin/env python3
"""Refuse publication if the GitHub tag no longer identifies the built commit."""
import argparse
import json
import re
import subprocess


def verify_tag(repository, tag, commit):
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
        raise ValueError('invalid repository')
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', tag):
        raise ValueError('invalid version tag')
    if not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise ValueError('source must be a full commit SHA')
    path = f'repos/{repository}/git/ref/tags/{tag}'
    for _ in range(16):
        result = json.loads(subprocess.check_output(['gh', 'api', path], text=True))
        obj = result['object']
        sha = obj['sha']
        if not re.fullmatch(r'[0-9a-f]{40}', sha):
            raise ValueError('invalid GitHub tag object SHA')
        if obj['type'] == 'commit':
            if sha != commit:
                raise ValueError('release tag moved from the validated source')
            return
        if obj['type'] != 'tag':
            raise ValueError('release tag must resolve to a commit')
        path = f'repos/{repository}/git/tags/{sha}'
    raise ValueError('release tag nesting exceeds the supported bound')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--commit', required=True)
    args = parser.parse_args()
    verify_tag(args.repository, args.tag, args.commit)
    print('Current release tag matches the validated source commit.')
