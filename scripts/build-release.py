#!/usr/bin/env python3
"""Build standalone assets from an immutable Git commit; never publish them."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

TARGETS = (('linux', 'amd64'), ('linux', 'arm64'), ('darwin', 'amd64'), ('darwin', 'arm64'))


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ref', required=True, help='Git tag or immutable source commit')
    parser.add_argument('--version', required=True, help='Candidate version, e.g. v0.1.0')
    parser.add_argument('--output', required=True, type=Path, help='New or empty output directory')
    args = parser.parse_args()
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', args.version):
        parser.error('version must be a v-prefixed semantic release version')
    root = Path(__file__).resolve().parents[1]
    commit = run('git', 'rev-parse', '--verify', '--end-of-options', args.ref + '^{commit}', cwd=root)
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error('output directory must be empty; existing artifacts will not be overwritten')
    go = os.environ.get('GO', 'go')
    # An ambient overlay/workspace/user config must not replace committed bytes.
    env = {k: v for k, v in os.environ.items() if not k.startswith('GO')}
    for key in ('GOCACHE', 'GOPATH', 'GOPROXY', 'GOSUMDB'):
        if key in os.environ:
            env[key] = os.environ[key]
    env.update({'GOENV': 'off', 'GOWORK': 'off', 'GOTOOLCHAIN': 'local',
                'GOFLAGS': '', 'GOEXPERIMENT': '', 'CGO_ENABLED': '0'})
    with tempfile.TemporaryDirectory(prefix='tenkacloud-release-') as temporary:
        temporary = Path(temporary)
        archive = temporary / 'source.tar'
        subprocess.run(['git', 'archive', '--format=tar', '--output=' + str(archive), commit], cwd=root, check=True)
        source = temporary / 'source'
        source.mkdir()
        with tarfile.open(archive) as tar:
            tar.extractall(source, filter='data')
        toolchain = run(go, 'version', cwd=source, env=env)
        if not toolchain.startswith('go version go1.26.10 '):
            parser.error('release builds require Go 1.26.10')
        subprocess.run(['python3', 'scripts/check-assets.py'], cwd=source, check=True)
        subprocess.run([go, 'mod', 'verify'], cwd=source, check=True, env=env)
        for target, arch in TARGETS:
            name = f'tenkacloud-simulator_{target}_{arch}'
            subprocess.run([go, 'build', '-mod=readonly', '-trimpath', '-buildvcs=false',
                            '-ldflags=-s -w', '-o', str(output / name), './cmd/simulator'],
                           cwd=source, check=True, env={**env, 'GOOS': target, 'GOARCH': arch})
            print(name, flush=True)
    (output / 'BUILDINFO.json').write_text(json.dumps({
        'version': args.version, 'source_commit': commit,
        'source_repository': 'https://github.com/susumutomita/TenkaCloudSimulator',
        'toolchain': toolchain, 'cgo_enabled': False,
        'build_flags': ['-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w'],
        'targets': [f'{target}/{arch}' for target, arch in TARGETS],
    }, indent=2) + '\n')
    assets = sorted(output.iterdir())
    with (output / 'SHA256SUMS').open('w') as sums:
        for asset in assets:
            with asset.open('rb') as stream:
                digest = hashlib.file_digest(stream, 'sha256').hexdigest()
            sums.write(f'{digest}  {asset.name}\n')


if __name__ == '__main__':
    main()
