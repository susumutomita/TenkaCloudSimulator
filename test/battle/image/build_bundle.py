#!/usr/bin/env python3
"""Build the pinned Battle guest bundle on this machine (Linux or macOS, amd64 or arm64).

Usage: build_bundle.py --out NEW_DIR [--work NEW_DIR] [--qemu PATH] [--keep-work]

This automates the procedure in ../README.md, with nothing left to fill in by hand:

1. Download the pinned official Amazon Linux 2023 KVM image and verify the RSA
   signature over SHA256SUMS with the published key, then the image checksum.
2. Build guest/ssm-relay (Linux/amd64, CGO off, -trimpath) and package it with
   scripts/guest-image/session-overlay.py. The overlay digest must equal the one
   pinned in builder-user-data.sh; nothing is substituted when it differs.
3. Boot ONE copy of the image with QEMU user networking and a NoCloud seed whose
   user-data is the unchanged builder-user-data.sh, through repo_relay.py.
4. Pack the emulator found on this machine, its firmware and modules, and the
   prepared disk with pack_bundle.py.

The emulator runs later from the extracted bundle with an empty environment and
the shared libraries of THIS machine, so build the bundle where it will run.
Network access is used only for the image download and the relay's build boot.
The result establishes integrity of the inputs it names, not an independent
review of the image. The bundle is for local use; review licensing before
redistributing it (see scripts/guest-image/SESSION-IMAGE.md).
"""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parents[2]
# The image release matches the mirror GUID pinned in builder-user-data.sh.
IMAGE_RELEASE = '2023.12.20261112.0'
IMAGE_NAME = f'al2023-kvm-{IMAGE_RELEASE}-kernel-6.1-x86_64.xfs.gpt.qcow2'
IMAGE_SHA256 = 'ef0fe2cdda2f6a8931dad1e1c66cb21d9f6b49c85e6025b185371e90028ea7e9'
IMAGE_BASE = f'https://cdn.amazonlinux.com/al2023/os-images/{IMAGE_RELEASE}/kvm/'
SIGNING_KEY = 'https://cdn.amazonlinux.com/al2023/_assets/AL2023_RSASSA_PKCS1_V1_5_SHA_256.pem'
FIRMWARE = ('bios-256k.bin', 'efi-virtio.rom', 'kvmvapic.bin')
RELAY_PORT = 8099
BUILD_TIMEOUT = 3600


def log(message):
    print(f'[build-bundle {time.strftime("%H:%M:%S")}] {message}', flush=True)


def sha256(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as source:
        for chunk in iter(lambda: source.read(1 << 20), b''):
            digest.update(chunk)
    return digest.hexdigest()


def pinned_overlay_digest(user_data):
    """OVERLAY_SHA256 from builder-user-data.sh; the guest checks the served tar against it."""
    found = re.findall(r'^OVERLAY_SHA256=([0-9a-f]{64})\b', user_data, re.MULTILINE)
    if len(found) != 1:
        raise SystemExit('builder-user-data.sh must pin exactly one OVERLAY_SHA256')
    return found[0]


def data_directories(help_text):
    """Directories listed by `qemu-system-x86_64 -L help`, in search order."""
    return [pathlib.Path(line.strip()) for line in help_text.splitlines() if line.strip().startswith('/')]


def module_directory(emulator):
    """The emulator's module directory relative to its prefix, or None when built without modules.

    QEMU relocates its configured module directory relative to the executable, so a
    bundle keeps the same relative path under the tree (bin/../lib/...).
    """
    prefix = emulator.parent.parent
    candidates = [prefix / 'lib/qemu', prefix / 'lib64/qemu', *sorted((prefix / 'lib').glob('*/qemu'))]
    for candidate in candidates:
        if candidate.is_dir() and any(candidate.glob('accel-tcg-x86_64.*')):
            return candidate.relative_to(prefix)
    return None


def seed_command(seed_dir, iso):
    """A NoCloud seed with volume label cidata, using this platform's tool."""
    if sys.platform == 'darwin':
        return ['hdiutil', 'makehybrid', '-quiet', '-iso', '-joliet', '-iso-volume-name', 'cidata', '-joliet-volume-name', 'cidata',
                '-default-volume-name', 'cidata', '-o', str(iso), str(seed_dir)]
    tool = shutil.which('genisoimage') or shutil.which('mkisofs')
    if tool is None:
        raise SystemExit('genisoimage (or mkisofs) is required to write the NoCloud seed')
    return [tool, '-quiet', '-output', str(iso), '-volid', 'cidata', '-joliet', '-rock', str(seed_dir / 'user-data'), str(seed_dir / 'meta-data')]


def builder_command(emulator, disk, iso, serial, cpus):
    """The one-time image builder boot: user networking reaches the relay at 10.0.2.2."""
    return [str(emulator), '-machine', 'q35', '-accel', 'tcg', '-cpu', 'max', '-smp', str(cpus), '-m', '3072',
            '-display', 'none', '-no-reboot', '-monitor', 'none',
            '-drive', f'file={disk},if=virtio,format=qcow2',
            '-drive', f'file={iso},if=virtio,format=raw,readonly=on',
            '-netdev', 'user,id=n0', '-device', 'virtio-net-pci,netdev=n0',
            '-serial', f'file:{serial}']


def qcow2_header_length(path):
    """The runtime accepts a version 3 header only at its original 104-byte length."""
    with open(path, 'rb') as disk:
        header = disk.read(104)
    if header[:4] != b'QFI\xfb':
        raise SystemExit(f'{path} is not a qcow2 image')
    return int.from_bytes(header[4:8], 'big'), int.from_bytes(header[100:104], 'big')


def fetch(url, target):
    log(f'download {url}')
    with urllib.request.urlopen(url, timeout=600) as response, open(target, 'wb') as out:
        shutil.copyfileobj(response, out, 1 << 20)


def run(command, **kwargs):
    log('run ' + ' '.join(command[:4]) + (' ...' if len(command) > 4 else ''))
    return subprocess.run(command, check=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__.split('\n')[0])
    parser.add_argument('--out', required=True, help='new directory for manifest.json and guest-bundle.tar.gz')
    parser.add_argument('--work', help='new scratch directory (default: a temporary directory)')
    parser.add_argument('--qemu', help='qemu-system-x86_64 to bundle (default: the one on PATH)')
    parser.add_argument('--cpus', type=int, default=max(1, min(4, os.cpu_count() or 1)))
    parser.add_argument('--keep-work', action='store_true')
    args = parser.parse_args()

    if sys.platform not in ('linux', 'darwin') or os.uname().machine not in ('x86_64', 'aarch64', 'arm64'):
        raise SystemExit('the Battle runtime is qualified on Linux and macOS (amd64 or arm64) only; use WSL2 on Windows')
    found = args.qemu or shutil.which('qemu-system-x86_64')
    if not found:
        raise SystemExit('qemu-system-x86_64 not found (macOS: brew install qemu; Ubuntu: apt install qemu-system-x86)')
    emulator = pathlib.Path(found).resolve()
    version = subprocess.run([str(emulator), '--version'], capture_output=True, text=True, check=True).stdout.splitlines()[0]
    help_text = subprocess.run([str(emulator), '-L', 'help'], capture_output=True, text=True, check=True).stdout
    firmware = {}
    for name in FIRMWARE:
        match = next((d / name for d in data_directories(help_text) if (d / name).is_file()), None)
        if match is None:
            raise SystemExit(f'{name} not found in the emulator data directories')
        firmware[name] = match.resolve()
    modules = module_directory(emulator)
    user_data = (HERE / 'builder-user-data.sh').read_text()
    overlay_pin = pinned_overlay_digest(user_data)

    out = pathlib.Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=False)
    work = pathlib.Path(args.work).resolve() if args.work else pathlib.Path(tempfile.mkdtemp(prefix='battle-bundle-'))
    work.mkdir(parents=True, exist_ok=True)
    log(f'emulator {emulator} ({version}); modules {modules}; work {work}')
    try:
        # 1. Official image, verified before use.
        download = work / 'download'
        download.mkdir()
        for name in ('SHA256SUMS', 'SHA256SUMS.RSASSA_PKCS1_V1_5_SHA_256', IMAGE_NAME):
            fetch(IMAGE_BASE + name, download / name)
        fetch(SIGNING_KEY, download / 'AL2023.pem')
        run(['openssl', 'dgst', '-sha256', '-verify', str(download / 'AL2023.pem'), '-signature',
             str(download / 'SHA256SUMS.RSASSA_PKCS1_V1_5_SHA_256'), str(download / 'SHA256SUMS')])
        listed = dict(reversed(line.split()) for line in (download / 'SHA256SUMS').read_text().splitlines() if line.strip())
        actual = sha256(download / IMAGE_NAME)
        if listed.get(IMAGE_NAME) != actual or actual != IMAGE_SHA256:
            raise SystemExit(f'image checksum mismatch: {actual}')

        # 2. Session relay overlay, reproducibly built and matched to the user-data pin.
        local = work / 'local'
        local.mkdir()
        relay = work / 'tenkacloud-session-relay'
        env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH='amd64')
        run(['go', 'build', '-trimpath', '-buildvcs=false', '-o', str(relay), './guest/ssm-relay'], cwd=REPO, env=env)
        unit = REPO / 'guest/ssm-relay/tenkacloud-session-relay.service'
        license_file = REPO / 'LICENSE'
        receipt = json.loads(run([sys.executable, '-I', str(REPO / 'scripts/guest-image/session-overlay.py'),
                                  '--binary', str(relay), '--binary-sha256', sha256(relay),
                                  '--unit', str(unit), '--unit-sha256', sha256(unit),
                                  '--license', str(license_file), '--license-sha256', sha256(license_file),
                                  '--output', str(local / 'session-overlay.tar')], capture_output=True, text=True).stdout)
        if receipt['overlaySHA256'] != overlay_pin:
            raise SystemExit(f'session overlay {receipt["overlaySHA256"]} differs from the builder-user-data.sh pin {overlay_pin}')

        # 3. One builder boot of a copy, through the loopback relay.
        disk = work / 'al2023-battle.qcow2'
        shutil.copyfile(download / IMAGE_NAME, disk)
        (download / IMAGE_NAME).unlink()
        seed = work / 'seed'
        seed.mkdir()
        (seed / 'user-data').write_text(user_data)
        (seed / 'meta-data').write_text('instance-id: tenkacloud-builder-1\nlocal-hostname: builder\n')
        iso = work / 'seed.iso'
        run(seed_command(seed, iso))
        serial = work / 'builder-serial.log'
        relay_process = subprocess.Popen([sys.executable, '-I', str(HERE / 'repo_relay.py'), str(local), str(work / 'provenance.jsonl'), str(RELAY_PORT)])
        try:
            time.sleep(1)
            if relay_process.poll() is not None:
                raise SystemExit('repo_relay.py exited; is port 8099 free?')
            started = time.time()
            run(builder_command(emulator, disk, iso, serial, args.cpus), timeout=BUILD_TIMEOUT)
            log(f'builder boot finished in {time.time() - started:.0f}s')
        finally:
            relay_process.terminate()
            relay_process.wait(timeout=30)
        console = serial.read_text(errors='replace')
        if 'BUILDER-DONE' not in console:
            raise SystemExit(f'image builder did not finish; see {serial}')
        if qcow2_header_length(disk) != (3, 104):
            raise SystemExit('prepared disk header is not the shipped version 3 / 104-byte header')

        # 4. Bundle tree and pinned archive.
        tree = work / 'tree'
        (tree / 'bin').mkdir(parents=True)
        (tree / 'firmware').mkdir()
        (tree / 'images').mkdir()
        shutil.copyfile(emulator, tree / 'bin/qemu-system-x86_64')
        for name, path in firmware.items():
            shutil.copyfile(path, tree / 'firmware' / name)
        if modules is not None:
            (tree / modules).mkdir(parents=True)
            for module in (emulator.parent.parent / modules).glob('accel-tcg-x86_64.*'):
                shutil.copyfile(module, tree / modules / module.name)
        shutil.move(str(disk), tree / 'images/al2023-battle.qcow2')
        summary = json.loads(run([sys.executable, '-I', str(HERE.parent / 'pack_bundle.py'), str(tree),
                                  str(out / 'guest-bundle.tar.gz'), str(out / 'manifest.json'), 'bin/qemu-system-x86_64'],
                                 capture_output=True, text=True).stdout)
        summary.update(emulator=str(emulator), emulatorVersion=version, imageRelease=IMAGE_RELEASE,
                       baseDiskSha256=sha256(tree / 'images/al2023-battle.qcow2'), overlaySha256=receipt['overlaySHA256'],
                       platform=f'{sys.platform}/{os.uname().machine}')
        (out / 'build-summary.json').write_text(json.dumps(summary, indent=1, sort_keys=True) + '\n')
        print(json.dumps(summary, sort_keys=True))
    finally:
        if not args.keep_work:
            shutil.rmtree(work, ignore_errors=True)


if __name__ == '__main__':
    main()
