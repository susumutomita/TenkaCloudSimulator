"""Trusted Debian ARM image prerequisites only; not a Battle launcher."""
import hashlib
import json
import os
import pathlib
import platform
import selectors
import socket
import subprocess
import sys
import time
from probe import QEMU, stop_owned
from boot_probe import verified_firmware, read_private_channel

IMAGE_SHA512 = '4a166ab9a73f27857896b799dff913199ea982e2379889340e60734ba43bea83840c1317f6257a9a8ca466b2293862181a597edc5cdb5d19d4cb704a85b4130a'
SEED_SHA256 = '40c641be2139814ace726a735da8f6b8994586c4aec55e68e495ec4d4b404478'
UUID = 'b42718a6-5b28-4f82-8d9c-cd91dfe925c1'


def checked(path, algorithm, digest, limit):
    path = pathlib.Path(path)
    if ',' in str(path):
        raise ValueError('QEMU option delimiter in artifact path')
    if path.is_symlink() or not path.is_file() or path.stat().st_size > limit:
        raise ValueError('invalid artifact')
    hasher = hashlib.new(algorithm)
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            hasher.update(block)
    if hasher.hexdigest() != digest:
        raise ValueError('artifact hash mismatch')
    return str(path.resolve(strict=True))


def observations(output):
    lines = output.splitlines()
    return all(marker in lines for marker in [b'TCS_PID1_systemd',
        ('TCS_UUID_' + UUID).encode(), b'TCS_BASH_PRESENT',
        b'TCS_NAMED_CHAR_PRESENT', b'TCS_PREREQ_DONE'])


def main(directory, firmware_directory):
    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        raise SystemExit('Apple Silicon required')
    root = pathlib.Path(directory).resolve(strict=True)
    disk = checked(root / 'debian-13-genericcloud-arm64.qcow2', 'sha512', IMAGE_SHA512, 1024 ** 3)
    seed = checked(root / 'seed-final.iso', 'sha256', SEED_SHA256, 16 * 1024 ** 2)
    firmware = verified_firmware(firmware_directory)
    host, guest = socket.socketpair()
    child = None
    output = bytearray()
    start = time.monotonic()
    try:
        args = [QEMU, '-no-user-config', '-nodefaults', '-machine', 'virt,accel=hvf',
                '-cpu', 'host', '-m', '512', '-smp', '1', '-nic', 'none',
                '-display', 'none', '-monitor', 'none', '-serial', 'stdio',
                '-bios', firmware, '-uuid', UUID,
                '-drive', f'file={disk},format=qcow2,if=none,id=os,snapshot=on',
                '-device', 'virtio-blk-pci,drive=os',
                '-drive', f'file={seed},format=raw,if=none,id=seed,readonly=on',
                '-device', 'virtio-blk-pci,drive=seed', '-device', 'virtio-serial-pci',
                '-chardev', f'socket,id=session-probe,fd={guest.fileno()}',
                '-device', 'virtserialport,chardev=session-probe,name=org.tenkacloud.session.0']
        child = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                 stderr=subprocess.STDOUT, env={}, pass_fds=(guest.fileno(),))
        guest.close()
        with selectors.DefaultSelector() as selector:
            selector.register(child.stdout, selectors.EVENT_READ)
            while True:
                if time.monotonic() - start > 90:
                    raise TimeoutError('image boot deadline exceeded')
                if not selector.select(timeout=0.1):
                    continue
                block = os.read(child.stdout.fileno(), 8192)
                if not block:
                    break
                output.extend(block)
                if len(output) > 4 * 1024 ** 2:
                    raise ValueError('serial output budget exceeded')
        child.wait(timeout=2)
        if child.returncode != 0 or not observations(output) or not read_private_channel(host):
            raise ValueError('image prerequisite execution unconfirmed')
    except BaseException:
        if child is not None:
            stop_owned(child)
        sys.stderr.buffer.write(output[-16384:])
        raise
    finally:
        host.close()
        guest.close()
        if child is not None and child.stdout is not None:
            child.stdout.close()
    sys.stdout.buffer.write(output)
    print(json.dumps({'result': 'trusted-debian-arm-image-prerequisites-pass',
                      'battleReady': False, 'relayExecuted': False,
                      'systemdBashNamedDevice': True, 'privateBytesObserved': True,
                      'ownedChildReaped': True, 'elapsedSeconds': round(time.monotonic() - start, 3)}))


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: image_probe.py IMAGE_DIRECTORY FIRMWARE_DIRECTORY')
    main(sys.argv[1], sys.argv[2])
