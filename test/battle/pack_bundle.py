#!/usr/bin/env python3
"""Pack a guest asset tree into the pinned bundle archive + manifest.

Usage: pack_bundle.py TREE OUT_ARCHIVE OUT_MANIFEST EXECUTABLE_RELATIVE_PATH

The archive is a gzip'd USTAR stream of regular files only (no PAX records,
links or directories), which is what guestbundle.Extract accepts. The manifest
pins every file and the compressed archive by SHA-256. Packing executes nothing.
"""
import gzip
import hashlib
import json
import os
import platform
import sys
import tarfile

# The manifest names the platform that will run the emulator (Go naming); the
# Simulator refuses a bundle whose os/arch differ from its own host.
GOOS = {'linux': 'linux', 'darwin': 'darwin'}.get(sys.platform)
GOARCH = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine().lower())
if GOOS is None or GOARCH is None:
    raise SystemExit(f'unsupported bundle host {sys.platform}/{platform.machine()}')

tree, out_archive, out_manifest, executable = sys.argv[1:5]
files = []
for root, dirs, names in os.walk(tree):
    dirs.sort()
    for name in sorted(names):
        full = os.path.join(root, name)
        if os.path.islink(full) or not os.path.isfile(full):
            raise SystemExit('non-regular bundle input: ' + full)
        files.append(os.path.relpath(full, tree))
files.sort()
if executable not in files:
    raise SystemExit('executable role missing from tree')
entries = []
with open(out_archive, 'xb') as raw:
    with gzip.GzipFile(fileobj=raw, mode='wb', compresslevel=1, mtime=0) as gz:
        # Written by hand: guestbundle.Extract refuses any byte after the two
        # end-of-archive blocks, so the usual 10240-byte record padding that
        # tarfile and GNU tar append would be rejected as trailing data.
        for rel in files:
            full = os.path.join(tree, rel)
            digest, size = hashlib.sha256(), 0
            with open(full, 'rb') as source:
                for chunk in iter(lambda: source.read(1 << 20), b''):
                    digest.update(chunk)
                    size += len(chunk)
            info = tarfile.TarInfo(rel)
            info.size, info.mode, info.uid, info.gid, info.mtime = size, 0o500 if rel == executable else 0o400, 0, 0, 0
            gz.write(info.tobuf(format=tarfile.USTAR_FORMAT))
            with open(full, 'rb') as source:
                for chunk in iter(lambda: source.read(1 << 20), b''):
                    gz.write(chunk)
            gz.write(b'\0' * (-size % 512))
            entry = {'path': rel, 'size': size, 'sha256': digest.hexdigest()}
            if rel == executable:
                entry['executable'] = True
            entries.append(entry)
        gz.write(b'\0' * 1024)
archive_digest = hashlib.sha256()
with open(out_archive, 'rb') as source:
    for chunk in iter(lambda: source.read(1 << 20), b''):
        archive_digest.update(chunk)
manifest = {'os': GOOS, 'arch': GOARCH, 'archiveSha256': archive_digest.hexdigest(), 'files': entries}
data = json.dumps(manifest, indent=1, sort_keys=True).encode() + b'\n'
with open(out_manifest, 'xb') as out:
    out.write(data)
print(json.dumps({'archiveSha256': manifest['archiveSha256'], 'manifestSha256': hashlib.sha256(data).hexdigest(),
                  'archiveBytes': os.path.getsize(out_archive), 'files': len(entries)}, sort_keys=True))
