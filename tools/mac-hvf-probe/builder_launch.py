"""Operator-only candidate builder. Import/tests never boot a VM."""
import hashlib
import json
import os
import pathlib
import platform
import selectors
import subprocess
import sys
import time
from builder_plan import BUILD_SCRIPT, IMAGE_SHA512, OVERLAY_SHA256
from image_probe import checked, OFFLINE_WAIT_MASK
from boot_probe import verified_firmware
from probe import QEMU, stop_owned


def argv(root, firmware):
    return [QEMU, '-no-user-config', '-nodefaults', '-machine', 'virt,accel=hvf',
            '-cpu', 'host', '-m', '512', '-smp', '1', '-nic', 'none',
            '-display', 'none', '-monitor', 'none', '-serial', 'stdio',
            '-bios', firmware, '-smbios', OFFLINE_WAIT_MASK,
            '-drive', f'file={root}/candidate.qcow2,format=qcow2,if=none,id=os',
            '-device', 'virtio-blk-pci,drive=os',
            '-drive', f'file={root}/seed.iso,format=raw,if=none,id=seed,readonly=on',
            '-device', 'virtio-blk-pci,drive=seed',
            '-drive', f'file={root}/overlay.iso,format=raw,if=none,id=overlay,readonly=on',
            '-device', 'virtio-blk-pci,drive=overlay']


def validate(root):
    root = pathlib.Path(root).resolve(strict=True)
    if ',' in str(root) or not root.is_dir() or root.stat().st_mode & 0o077 or root.stat().st_uid != os.getuid():
        raise ValueError('operator-private builder directory required')
    if any((root / n).exists() for n in ('INCOMPLETE', 'ATTEMPTED', 'build-result.json', 'builder-serial.log', 'seed.iso', 'overlay.iso')):
        raise ValueError('incomplete or already attempted directory')
    receipt = json.loads((root / 'preparation.json').read_text())
    if receipt.get('state') != 'prepared-not-built' or receipt.get('vmExecuted') is not False:
        raise ValueError('preparation receipt absent/invalid')
    candidate = root / 'candidate.qcow2'
    st = candidate.stat()
    original = receipt.get('sourceIdentity')
    if (st.st_nlink != 1 or st.st_uid != os.getuid() or st.st_mode & 0o077
            or not isinstance(original, dict) or set(original) != {'device', 'inode'}
            or original == {'device': st.st_dev, 'inode': st.st_ino}):
        raise ValueError('candidate must be a private independent file')
    checked(candidate, 'sha512', IMAGE_SHA512, 1024 ** 3)
    checked(root / 'overlay/relay.tar', 'sha256', OVERLAY_SHA256, 32 * 1024 ** 2)
    expected = {'user-data': BUILD_SCRIPT,
                'meta-data': 'instance-id: tcs-arm-builder-v1\nlocal-hostname: tcs-builder\n',
                'network-config': 'version: 2\nethernets: {}\n'}
    for name, content in expected.items():
        checked(root / 'seed' / name, 'sha256', hashlib.sha256(content.encode()).hexdigest(), 16384)
    if {p.name for p in (root / 'seed').iterdir()} != set(expected):
        raise ValueError('unexpected seed file')
    if {p.name for p in (root / 'overlay').iterdir()} != {'relay.tar'}:
        raise ValueError('unexpected overlay file')
    return root


def execute(root, firmware_directory):
    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        raise ValueError('Apple Silicon only')
    root = validate(root)
    firmware = verified_firmware(firmware_directory)
    # Exclusive marker is created before ANY subprocess. A failed attempt cannot
    # be resumed or retried against a partly modified image.
    with (root / 'ATTEMPTED').open('x') as f:
        f.write('Unqualified builder attempt. Never resume automatically.\n')
    child = None
    output = bytearray()
    try:
        for source, label in [('seed', 'cidata'), ('overlay', 'TCSOVERLAY')]:
            target = root / (source + '.iso')
            if target.exists():
                raise ValueError('ISO output already exists')
            subprocess.run(['/usr/bin/hdiutil', 'makehybrid', '-iso', '-joliet',
                            '-default-volume-name', label, '-o', str(target), str(root / source)],
                           check=True, timeout=30, env={}, stdin=subprocess.DEVNULL,
                           stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        child = subprocess.Popen(argv(root, firmware), stdin=subprocess.DEVNULL,
                                 stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env={})
        start = time.monotonic()
        with selectors.DefaultSelector() as sel:
            sel.register(child.stdout, selectors.EVENT_READ)
            while True:
                if time.monotonic() - start > 90:
                    raise TimeoutError('builder deadline exceeded')
                if not sel.select(timeout=0.1):
                    continue
                block = os.read(child.stdout.fileno(), 8192)
                if not block:
                    break
                output.extend(block)
                if len(output) > 4 * 1024 ** 2:
                    raise ValueError('builder serial budget exceeded')
        child.wait(timeout=2)
        lines = output.splitlines()
        if child.returncode != 0 or b'TCS_BUILD_FILES_INSTALLED' not in lines or b'TCS_BUILD_STATE_CLEAN' not in lines or b'TCS_BUILD_FAILED' in lines or not any(b'reboot: Power down' in l for l in lines):
            raise ValueError('builder completion unconfirmed')
        hasher = hashlib.sha256()
        with (root / 'candidate.qcow2').open('rb') as disk:
            for block in iter(lambda: disk.read(1024 * 1024), b''):
                hasher.update(block)
        digest = hasher.hexdigest()
        result = {'state': 'built-not-qualified', 'diskSHA256': digest,
                  'ownedChildReaped': True, 'battleReady': False, 'relayExecuted': False,
                  'cloudInitCleanupObserved': True, 'cloudInitCleanupVerified': False}
    except BaseException:
        if child is not None:
            stop_owned(child)
        raise
    finally:
        if child is not None and child.stdout is not None:
            child.stdout.close()
        # Log write can fail. Missing receipt or log is never a successful build.
        with (root / 'builder-serial.log').open('xb') as f:
            f.write(output)
    # No receipt is published until serial log persistence has succeeded.
    with (root / 'build-result.json').open('x') as f:
        f.write(json.dumps(result, indent=2) + '\n')
    return result


if __name__ == '__main__':
    if len(sys.argv) != 4 or sys.argv[1] != '--operator-build':
        raise SystemExit('usage: builder_launch.py --operator-build NEW_PREPARED_DIRECTORY FIRMWARE_DIRECTORY')
    print(json.dumps(execute(sys.argv[2], sys.argv[3])))
