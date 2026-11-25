"""Run the fixed native/browser fixture in an unprivileged loopback-only namespace.

Tools and the binary must already be provisioned. Failure to create namespaces,
drop capabilities or retain Chromium's sandbox fails the check; there is no
privileged, networked or sandbox-disabled fallback. Namespace identity checks
correlate this launch, rather than attesting arbitrary caller-supplied evidence.
"""
import argparse
import fcntl
import hashlib
import importlib.metadata
import os
from pathlib import Path
import re
import shutil
import socket
import struct
import sys

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = Path(__file__).with_name('s3_multipart_paging.py').resolve()
CAPABILITIES = ('CapEff', 'CapPrm', 'CapInh', 'CapAmb')
SHELL = '"$1" link set lo up\nshift\nexec "$@"'


def require(condition, reason):
    if not condition:
        raise RuntimeError(reason)


def binary_digest(binary):
    require(binary.is_absolute() and binary.is_file() and os.access(binary, os.X_OK),
            'The already-built simulator executable is required')
    with binary.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def pinned_chromium():
    pins = re.findall(r'^playwright==([^\s]+)$',
                      (ROOT / 'scripts/requirements-test.txt').read_text(), re.MULTILINE)
    require(len(pins) == 1 and importlib.metadata.version('playwright') == pins[0],
            'Use the pinned Playwright test environment')
    from playwright.sync_api import sync_playwright
    with sync_playwright() as playwright:
        executable = Path(playwright.chromium.executable_path).resolve()
    require(executable.is_absolute() and executable.is_file() and os.access(executable, os.X_OK),
            'Install the pinned Playwright Chromium before isolation')
    return executable


def namespace_inode(name):
    return os.stat('/proc/self/ns/' + name).st_ino


def loopback_up():
    # Unlike /sys/class/net, this ioctl observes the calling network namespace.
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as control:
        flags = fcntl.ioctl(control, 0x8913, struct.pack('256s', b'lo'))  # SIOCGIFFLAGS
    return bool(struct.unpack_from('H', flags, 16)[0] & 1)  # IFF_UP


def validate_inside_context(expected_uid, parent_net, parent_user, *, uid, euid,
                            net, user, status, interfaces, lo_up):
    require(expected_uid > 0 and uid == euid == expected_uid,
            'Keep the original non-root runner UID')
    require(parent_net > 0 and parent_user > 0 and net != parent_net and user != parent_user,
            'A new user and network namespace are required')
    rows = {}
    for line in status.splitlines():
        key, separator, value = line.partition(':')
        if separator and key in CAPABILITIES:
            require(key not in rows, 'Duplicate capability status')
            rows[key] = value.strip()
    require(set(rows) == set(CAPABILITIES) and
            all(re.fullmatch('[0-9a-fA-F]+', value) and int(value, 16) == 0
                for value in rows.values()), 'Drop all effective, permitted, inheritable and ambient capabilities')
    require(interfaces == ['lo'] and lo_up, 'Enable only the isolated loopback interface')


def inside(args):
    validate_inside_context(args.uid, args.parent_net, args.parent_user,
                            uid=os.getuid(), euid=os.geteuid(),
                            net=namespace_inode('net'), user=namespace_inode('user'),
                            status=Path('/proc/self/status').read_text(),
                            interfaces=[name for _, name in socket.if_nameindex()],
                            lo_up=loopback_up())
    binary = Path(args.binary)
    require(re.fullmatch('[a-f0-9]{64}', args.binary_sha256) and
            binary_digest(binary) == args.binary_sha256, 'Simulator changed during isolation')
    require(str(pinned_chromium()) == args.chromium, 'Pinned Chromium changed during isolation')
    os.environ.update(SIMULATOR_BINARY=str(binary), SIMULATOR_BINARY_SHA256=args.binary_sha256,
                      CHROMIUM_EXECUTABLE=args.chromium)
    os.execv(sys.executable, [sys.executable, str(FIXTURE)])


def command(args, tools, chromium, digest):
    # All variable data is positional argv. The shell text and fixture are fixed.
    return [tools['unshare'], '--user', '--map-current-user', '--net', '--keep-caps',
            tools['sh'], '-eu', '-c', SHELL, 's3-paging-netns', tools['ip'],
            tools['setpriv'], '--inh-caps=-all', '--ambient-caps=-all', '--bounding-set=-all',
            sys.executable, str(Path(__file__).resolve()), '--inside', '--binary', args.binary,
            '--binary-sha256', digest, '--chromium', str(chromium), '--uid', str(os.getuid()),
            '--parent-net', str(namespace_inode('net')), '--parent-user', str(namespace_inode('user'))]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--binary', default=str(ROOT / 'dist/tenkacloud-simulator'))
    parser.add_argument('--inside', action='store_true', help=argparse.SUPPRESS)
    for name in ('binary-sha256', 'chromium'):
        parser.add_argument('--' + name, help=argparse.SUPPRESS)
    for name in ('uid', 'parent-net', 'parent-user'):
        parser.add_argument('--' + name, type=int, help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    private = (args.binary_sha256, args.chromium, args.uid, args.parent_net, args.parent_user)
    require(__debug__ and sys.platform == 'linux', 'An unoptimized Linux test interpreter is required')
    if args.inside:
        require(all(value is not None for value in private), 'Incomplete namespace launch context')
        inside(args)
        return
    require(all(value is None for value in private), 'Namespace-only arguments require --inside')
    require(os.getuid() == os.geteuid() and os.getuid() > 0, 'Run as the non-root CI user')
    args.binary = str(Path(args.binary).resolve())
    digest = binary_digest(Path(args.binary))
    chromium = pinned_chromium()
    tools = {name: shutil.which(name) for name in ('unshare', 'sh', 'ip', 'setpriv')}
    require(all(tools.values()), 'Existing unshare, sh, ip and setpriv tools are required')
    launch = command(args, tools, chromium, digest)
    os.execv(launch[0], launch)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, importlib.metadata.PackageNotFoundError):
        print('S3 paging isolation or pinned-tool validation failed; no fallback was attempted', file=sys.stderr)
        sys.exit(1)
