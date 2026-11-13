"""Trusted Alpine kernel/initramfs boot check, never a Battle launcher."""
import hashlib
import json
import os
import pathlib
import platform
import selectors
import subprocess
import sys
import time
from probe import QEMU, stop_owned

EXPECTED = {
    'vmlinuz-virt': 'ebf299aa6829b15166008db24b076ac0a8b3e778c72ed66be488742aaf05d741',
    'initramfs-virt': '0f7da92ca19e6b87b8f0a250e3ce84cc8335c16b2369b18d96f68cc486d638b8',
}


def verified_files(directory):
    root = pathlib.Path(directory).resolve(strict=True)
    result = []
    for name, digest in EXPECTED.items():
        path = root / name
        if path.is_symlink() or not path.is_file() or path.stat().st_size > 100 * 1024 * 1024:
            raise ValueError('invalid artifact')
        if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError('artifact digest mismatch')
        result.append(str(path))
    return result


def main(directory, inspect_identity=False):
    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        raise SystemExit('Apple Silicon required; no fallback')
    kernel, initrd = verified_files(directory)
    args = [QEMU, '-no-user-config', '-nodefaults', '-machine', 'virt,accel=hvf',
            '-cpu', 'host', '-m', '256', '-smp', '1', '-nic', 'none',
            '-display', 'none', '-monitor', 'none', '-serial', 'stdio',
            '-kernel', kernel, '-initrd', initrd,
            '-append', 'console=ttyAMA0 rdinit=/bin/sh panic=-1']
    if inspect_identity:
        args += ['-uuid', 'b42718a6-5b28-4f82-8d9c-cd91dfe925c1',
                 '-device', 'virtio-serial-pci', '-chardev', 'null,id=session-probe',
                 '-device', 'virtserialport,chardev=session-probe,name=org.tenkacloud.session.0']
    start = time.monotonic()
    child = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.STDOUT, env={})
    output = bytearray()
    sent = False
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(child.stdout, selectors.EVENT_READ)
            while True:
                if time.monotonic() - start > 30:
                    raise TimeoutError('boot deadline exceeded')
                if not selector.select(timeout=0.1):
                    continue
                data = os.read(child.stdout.fileno(), 8192)
                if not data:
                    break
                output.extend(data)
                if len(output) > 1024 * 1024:
                    raise ValueError('serial output budget exceeded')
                if b'~ # ' in output and not sent:
                    if inspect_identity:
                        child.stdin.write(b"/bin/busybox mount -t sysfs sysfs /sys; /bin/busybox cat /sys/class/dmi/id/product_uuid; /bin/busybox cat /sys/class/virtio-ports/*/name; /bin/busybox test -c /dev/virtio-ports/org.tenkacloud.session.0 && echo TCS_NAMED_CHAR_PRESENT\n")
                    child.stdin.write(b"printf 'TCS_ARCH_'; /bin/busybox uname -m; /bin/busybox poweroff -f\n")
                    child.stdin.flush()
                    sent = True
        child.wait(timeout=2)
        if child.returncode != 0 or b'TCS_ARCH_aarch64' not in output or b'Power down' not in output:
            raise ValueError('guest execution or orderly poweroff unconfirmed')
    except BaseException:
        stop_owned(child)
        sys.stderr.buffer.write(output[-8192:])
        raise
    finally:
        for stream in (child.stdin, child.stdout):
            stream.close()
    print(json.dumps({'result': 'trusted-arm64-initramfs-boot-pass', 'battleReady': False,
                      'guestBooted': True, 'ownedChildReaped': True,
                      'elapsedSeconds': round(time.monotonic() - start, 3),
                      'artifacts': EXPECTED,
                      'identityProbe': inspect_identity,
                      'matchingDMIUUID': b'b42718a6-5b28-4f82-8d9c-cd91dfe925c1' in output.splitlines(),
                      'namedVirtioPort': b'org.tenkacloud.session.0' in output.splitlines(),
                      'namedCharacterDevice': b'TCS_NAMED_CHAR_PRESENT' in output.splitlines()}))
    sys.stdout.buffer.write(output)


if __name__ == '__main__':
    if len(sys.argv) not in (2, 3) or (len(sys.argv) == 3 and sys.argv[2] != '--identity'):
        raise SystemExit('usage: boot_probe.py VERIFIED_ARTIFACT_DIRECTORY')
    main(sys.argv[1], inspect_identity=len(sys.argv) == 3)
