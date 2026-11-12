#!/usr/bin/env python3
"""Package reviewed guest relay inputs; never execute a relay or start a VM."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile

parser = argparse.ArgumentParser()
parser.add_argument('--binary', type=Path, required=True)
parser.add_argument('--unit', type=Path, required=True)
parser.add_argument('--binary-sha256', required=True)
parser.add_argument('--unit-sha256', required=True)
parser.add_argument('--license', type=Path, required=True)
parser.add_argument('--license-sha256', required=True)
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
inputs = []
for path, expected, target, mode in [
    (args.binary, args.binary_sha256, 'usr/libexec/tenkacloud-session-relay', 0o755),
    (args.license, args.license_sha256, 'usr/share/licenses/tenkacloud-session-relay/LICENSE', 0o644),
    (args.unit, args.unit_sha256, 'usr/lib/systemd/system/tenkacloud-session-relay.service', 0o644),
]:
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 32 * 1024 * 1024:
        raise SystemExit('invalid regular input')
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != expected:
        raise SystemExit('reviewed input digest mismatch')
    inputs.append((target, data, mode))
inputs.append(('etc/tenkacloud/session-relay-image', b'tenkacloud-guest-session-v1\n', 0o644))
# Exclusive output creation prevents silently replacing a reviewed artifact.
with args.output.open('xb') as output:
    with tarfile.open(fileobj=output, mode='w', format=tarfile.USTAR_FORMAT) as archive:
        for target, data, mode in inputs:
            info = tarfile.TarInfo(target)
            info.size, info.mode, info.uid, info.gid, info.mtime = len(data), mode, 0, 0, 0
            archive.addfile(info, io.BytesIO(data))
        link = tarfile.TarInfo('etc/systemd/system/multi-user.target.wants/tenkacloud-session-relay.service')
        link.type = tarfile.SYMTYPE
        link.linkname = '/usr/lib/systemd/system/tenkacloud-session-relay.service'
        link.mode, link.uid, link.gid, link.mtime = 0o777, 0, 0, 0
        archive.addfile(link)
print(json.dumps({'overlaySHA256': hashlib.sha256(args.output.read_bytes()).hexdigest(),
                  'binarySHA256': args.binary_sha256, 'unitSHA256': args.unit_sha256, 'licenseSHA256': args.license_sha256,
                  'installedOrExecuted': False}, sort_keys=True))
