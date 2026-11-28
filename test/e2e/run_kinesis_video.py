"""Run the Kinesis Video SDK, catalog and browser fixtures on an isolated Linux host."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys


HERE = Path(__file__).resolve().parent
AXE_SHA256 = 'e9e5863c33a874f09bc01acd9234b7e3c871479f5eef8802fa582544465e6d01'


def neutral_host():
    if not __debug__ or os.environ.get('PYTHONOPTIMIZE'):
        raise SystemExit('Python assertions must be enabled for Kinesis Video E2E')
    if sys.platform != 'linux' or os.geteuid() == 0:
        raise SystemExit('Kinesis Video E2E requires a non-root Linux host')
    configs = [Path.home() / name for name in ('.aws/config', '.aws/credentials', '.boto')]
    if any(key.startswith(('AWS_', 'BOTO_')) for key in os.environ) or any(
        path.exists() or path.is_symlink() for path in [*configs, Path('/etc/boto.cfg')]
    ):
        raise SystemExit('Kinesis Video E2E requires a host without ambient AWS configuration')


def isolated(binary, jars):
    if {name for _, name in socket.if_nameindex()} != {'lo'}:
        raise SystemExit('Kinesis Video E2E requires loopback as its only network interface')
    status = dict(line.split(':', 1) for line in Path('/proc/self/status').read_text().splitlines() if ':' in line)
    if any(int(status[name], 16) for name in ('CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb')):
        raise SystemExit('Kinesis Video E2E requires empty Linux capability sets')

    from playwright.sync_api import sync_playwright
    with sync_playwright() as driver:
        chromium = Path(driver.chromium.executable_path).resolve(strict=True)
    axe = Path(os.environ['AXE_SCRIPT']).resolve(strict=True)
    if hashlib.sha256(axe.read_bytes()).hexdigest() != AXE_SHA256:
        raise SystemExit('AXE_SCRIPT must be the pinned axe-core bundle from scripts/get-audit-tools.py')
    with binary.open('rb') as executable:
        digest = hashlib.file_digest(executable, 'sha256').hexdigest()
    expected_digest = os.environ.get('SIMULATOR_BINARY_SHA256')
    if expected_digest is not None and digest != expected_digest:
        raise SystemExit('The simulator executable differs from SIMULATOR_BINARY_SHA256')
    print(f'Kinesis Video E2E simulator SHA-256 {digest}', flush=True)
    env = {**os.environ, 'SIMULATOR_BINARY': str(binary), 'SIMULATOR_BINARY_SHA256': digest,
           'CHROMIUM_EXECUTABLE': str(chromium), 'AXE_SCRIPT': str(axe)}
    receipt = {'binarySHA256': digest}
    for script, args in [('aws_catalog_fixtures.py', ['kinesisvideo']),
                         ('kinesis_video_fragments.py', ['--java-libs', str(jars)]),
                         ('kinesis_video_media_console.py', [])]:
        result = subprocess.run([sys.executable, '-B', str(HERE / script), *args], env=env,
                                capture_output=True, text=True)
        if result.returncode:
            frames = [(Path(name), line) for name, line in re.findall(r'File "([^"\n]+)", line (\d+)', result.stderr)]
            locations = [f'{path.relative_to(HERE)}:{line}' for path, line in frames if path.is_relative_to(HERE)]
            raise SystemExit(f'{script} exited {result.returncode} at ' + ', '.join(locations[-8:]))
        if script == 'aws_catalog_fixtures.py':
            if result.stdout.strip() != 'Catalog fixture lifecycles passed for kinesisvideo':
                raise SystemExit('The Kinesis control operation sweep did not report completion')
            receipt['catalog'] = 'kinesisvideo'
        else:
            outcome = json.loads(result.stdout)
            if outcome.get('cleanup') is not True:
                raise SystemExit(f'{script} did not report successful cleanup')
            if script == 'kinesis_video_fragments.py':
                receipt['sdkCalls'] = int(outcome['boto3Calls'])
            else:
                receipt['browserResponses'] = int(outcome['nativeBrowserResponses'])
                receipt['scalarSends'] = len(outcome['scalarSends'])
    with binary.open('rb') as executable:
        if hashlib.file_digest(executable, 'sha256').hexdigest() != digest:
            raise SystemExit('The simulator executable changed during the Kinesis Video E2E run')
    print(json.dumps(receipt, sort_keys=True), flush=True)
    if destination := os.environ.get('KINESIS_VIDEO_RECEIPT'):
        Path(destination).write_text(json.dumps(receipt, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('jars', type=Path)
    parser.add_argument('--isolated', action='store_true')
    args = parser.parse_args()
    neutral_host()
    binary = args.binary.resolve(strict=True)
    jars = args.jars.resolve(strict=True)
    if args.isolated:
        isolated(binary, jars)
    else:
        command = ['unshare', '--user', '--map-current-user', '--net', '--keep-caps',
                   'sh', '-eu', '-c', 'ip link set lo up\nexec setpriv --inh-caps=-all '
                   '--ambient-caps=-all --bounding-set=-all "$@"', 'video-namespace',
                   sys.executable, '-B', str(Path(__file__).resolve()), '--isolated', str(binary), str(jars)]
        subprocess.run(command, check=True)


if __name__ == '__main__':
    main()
