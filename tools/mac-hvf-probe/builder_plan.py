"""Prepare fixed offline builder inputs; never launch a VM or modify an image."""
import hashlib
import json
import pathlib
import shutil
import sys
from image_probe import checked, IMAGE_SHA512

OVERLAY_SHA256 = '11b2535ad82b18dbe3272a6042e155d9ae2bd6f88f52b97512d61846693aa79e'
BINARY_SHA256 = '7eea651aad51fe984baecd1b402e08e98b95da0f2a51379ffe3cf58b6c82e5f6'
# Builder-only cloud-init. Never supply this to a Battle problem VM.
BUILD_SCRIPT = '''#!/bin/sh
set -eu
exec > /dev/ttyAMA0 2>&1
trap 'echo TCS_BUILD_FAILED; poweroff -f' EXIT
mkdir -p /run/tcs-build
mount -o ro /dev/disk/by-label/TCSOVERLAY /run/tcs-build
cd /run/tcs-build
printf '%s  relay.tar\\n' OVERLAY_HASH | sha256sum -c -
tar -xf relay.tar -C /
printf '%s  /usr/libexec/tenkacloud-session-relay\\n' BINARY_HASH | sha256sum -c -
test "$(cat /etc/tenkacloud/session-relay-image)" = tenkacloud-guest-session-v1
test "$(readlink /etc/systemd/system/multi-user.target.wants/tenkacloud-session-relay.service)" = /usr/lib/systemd/system/tenkacloud-session-relay.service
sync
echo TCS_BUILD_FILES_INSTALLED
trap - EXIT
poweroff
'''.replace('OVERLAY_HASH', OVERLAY_SHA256).replace('BINARY_HASH', BINARY_SHA256)


def prepare(image, overlay, output):
    image = pathlib.Path(checked(image, 'sha512', IMAGE_SHA512, 1024 ** 3))
    overlay = pathlib.Path(checked(overlay, 'sha256', OVERLAY_SHA256, 32 * 1024 ** 2))
    output = pathlib.Path(output)
    if ',' in str(output):
        raise ValueError('QEMU option delimiter in output path')
    # Exclusive new directory. No resume, reuse, overwrite or implicit cleanup.
    output.mkdir(mode=0o700)
    try:
        (output / 'overlay').mkdir(mode=0o700)
        (output / 'seed').mkdir(mode=0o700)
        with image.open('rb') as src, (output / 'candidate.qcow2').open('xb') as dst:
            shutil.copyfileobj(src, dst)
        checked(output / 'candidate.qcow2', 'sha512', IMAGE_SHA512, 1024 ** 3)
        with overlay.open('rb') as src, (output / 'overlay/relay.tar').open('xb') as dst:
            shutil.copyfileobj(src, dst)
        checked(output / 'overlay/relay.tar', 'sha256', OVERLAY_SHA256, 32 * 1024 ** 2)
        (output / 'seed/meta-data').write_text('instance-id: tcs-arm-builder-v1\nlocal-hostname: tcs-builder\n')
        (output / 'seed/network-config').write_text('version: 2\nethernets: {}\n')
        (output / 'seed/user-data').write_text(BUILD_SCRIPT)
        receipt = {'state': 'prepared-not-built', 'vmExecuted': False, 'battleReady': False,
                   'baseSHA512': IMAGE_SHA512, 'overlaySHA256': OVERLAY_SHA256,
                   'builderScriptSHA256': hashlib.sha256(BUILD_SCRIPT.encode()).hexdigest(),
                   'failureRule': 'retain candidate as unqualified; never retry or promote automatically'}
        (output / 'preparation.json').write_text(json.dumps(receipt, indent=2) + '\n')
        return receipt
    except BaseException:
        (output / 'INCOMPLETE').write_text('Preparation failed. This directory is not a usable image.\n')
        raise


if __name__ == '__main__':
    if len(sys.argv) != 4:
        raise SystemExit('usage: builder_plan.py BASE_QCOW2 OVERLAY_TAR NEW_DIRECTORY')
    print(json.dumps(prepare(*sys.argv[1:])))
