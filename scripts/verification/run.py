#!/usr/bin/env python3
"""Execute the reviewed vendored runner only after validating its immutable pin."""
import hashlib
import json
from pathlib import Path
import runpy
import sys

HERE = Path(__file__).resolve().parent


def verify_pin(directory=HERE):
    pin = json.loads((directory / 'upstream.json').read_text())
    source = directory / 'vendor' / 'verify.py'
    if hashlib.sha256(source.read_bytes()).hexdigest() != pin['sha256']:
        raise ValueError('Vendored verification runner differs from the reviewed pin')
    return source


def main():
    try:
        source = verify_pin()
    except (OSError, ValueError, KeyError) as error:
        print(f'Verification blocked: {error}', file=sys.stderr)
        return 2
    # No fetching, installation, shell interpolation or toolchain replacement.
    runpy.run_path(str(source), run_name='__main__')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
