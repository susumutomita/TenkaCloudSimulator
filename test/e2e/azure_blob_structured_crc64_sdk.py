"""Opt-in official Azure Python SDK acceptance, not a native HTTP test.

Run only on an independently approved loopback-only Linux runner (including the
Simulator subprocess), with no egress, DNS/proxy, cloud credentials, or unrelated
local services. Use a trusted isolated Python environment provisioned separately
from official hash-locked wheels, including azure-storage-blob[ext-checksums].
This script does not install anything or provision/attest network isolation.

Supply the independently reviewed executable SHA-256 for the designated runtime
commit and a build receipt JSON {"source_commit":"...","binary_sha256":"..."}.
Supply its SHA-256 from a trusted source after independently verifying its
signed build provenance; this script checks identity, not signatures or trust.
Do not build from a later fixture-only commit or generate a receipt locally to
stand in for reviewing provenance. Also supply a reviewed SDK lock JSON: {"azure-storage-blob":{"version":"12.31.1","record_sha256":"..."},
"azure-storage-extensions":{"version":"...","record_sha256":"..."},
"azure-core":{"version":"...","record_sha256":"..."},
"requests":{"version":"...","record_sha256":"..."}}. Hashes are of installed
wheel RECORD files; compare against this trusted environment's manifest, not
values obtained from an unreviewed environment. This is an identity check, not
a replacement for external wheel provenance or a read-only runtime image.
"""

import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import socket
import tempfile
from urllib.parse import urlsplit

from crossprovider_official_storage import (
    ACCOUNT, AcceptanceFailure, Team, acceptance_phase, azure_client,
    bounded_items, expect_status, local_network_only,
)

RUNTIME_COMMIT = 'aa74a0088b3aa3a2c50961a61b40361c6bba4e1c'
SDK_VERSION = '12.31.1'
READ_API_VERSION = '2025-01-05'
PACKAGES = {'azure-storage-blob', 'azure-storage-extensions', 'azure-core', 'requests'}
CONTAINER = 'sdk-xsm-crc64'
BLOB = 'crc64-fixture'
DATA = b'X\x00\xfaY'
OFFSET, LENGTH = 1, 2


def require(ok, message):
    if not ok:
        raise AssertionError(message)


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def exact_hex(value, size):
    return isinstance(value, str) and re.fullmatch(f'[a-f0-9]{{{size}}}', value) is not None


def verify_build_receipt(path, trusted_receipt_sha, expected_binary_sha):
    require(exact_hex(trusted_receipt_sha, 64), 'Invalid expected build receipt digest')
    raw = path.read_bytes()
    require(hashlib.sha256(raw).hexdigest() == trusted_receipt_sha, 'Build receipt digest differs')
    receipt = json.loads(raw)
    require(isinstance(receipt, dict) and set(receipt) == {'source_commit', 'binary_sha256'}
            and receipt['source_commit'] == RUNTIME_COMMIT
            and receipt['binary_sha256'] == expected_binary_sha
            and exact_hex(expected_binary_sha, 64), 'Build receipt source or binary differs')


def verify_sdk_lock(path):
    raw = path.read_bytes()
    lock = json.loads(raw)
    require(isinstance(lock, dict) and set(lock) == PACKAGES, 'SDK lock package set differs')
    versions = {}
    for name in sorted(PACKAGES):
        entry = lock[name]
        require(isinstance(entry, dict) and set(entry) == {'version', 'record_sha256'}
                and isinstance(entry['version'], str) and bool(entry['version'])
                and exact_hex(entry['record_sha256'], 64), 'SDK lock entry is invalid')
        dist = importlib.metadata.distribution(name)
        require(dist.version == entry['version'], 'SDK dependency version differs')
        if name == 'azure-storage-blob':
            require(dist.version == SDK_VERSION, 'Azure Blob SDK is not the pinned release')
        records = [f for f in (dist.files or ())
                   if f.name == 'RECORD' and f.parent.name.endswith('.dist-info')]
        require(len(records) == 1 and sha256_file(Path(dist.locate_file(records[0])))
                == entry['record_sha256'], 'Installed SDK RECORD differs')
        versions[name] = dist.version
    return versions, hashlib.sha256(raw).hexdigest()


def require_loopback_only():
    # A supplementary fail-closed check, NOT independent enforcement or proof.
    rows = Path('/proc/net/dev').read_text().splitlines()[2:]
    interfaces = {row.split(':', 1)[0].strip() for row in rows if ':' in row}
    require(interfaces == {'lo'}, 'Visible interfaces are not loopback-only')


def structured_client(team):
    from azure.storage.blob import BlobClient

    def local_auth(request):
        parsed = urlsplit(request.http_request.url)
        require(parsed.scheme == 'http' and parsed.netloc == f'127.0.0.1:{team.port}',
                'SDK request left exact loopback origin')
        # The same local Team authentication used by azure_client. No
        # checksum, API version, range, or response hooks/rewrites.
        request.http_request.headers['X-Simulator-Token'] = team.token

    return BlobClient(
        account_url=f'{team.url}/_simulator/providers/azure/{ACCOUNT}',
        container_name=CONTAINER, blob_name=BLOB, credential=None,
        api_version=READ_API_VERSION, raw_request_hook=local_auth,
        retry_total=0, connection_timeout=3, read_timeout=3)


def run(binary):
    from azure.core import MatchConditions
    from azure.storage.blob import ContentSettings

    with tempfile.TemporaryDirectory(prefix='azure-xsm-sdk-') as directory:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        team = Team(binary, directory, port)
        with local_network_only({port}):
            try:
                with acceptance_phase('local-team-start'):
                    team.start()
                # The legacy helper fixes API version to 2023-11-03.
                with azure_client(team, team.token) as legacy:
                    container = legacy.get_container_client(CONTAINER)
                    blob = container.get_blob_client(BLOB)
                    created = False
                    try:
                        with acceptance_phase('legacy-sdk-seed'):
                            container.create_container()
                            created = True
                            blob.upload_blob(
                                DATA, overwrite=False, content_settings=ContentSettings(
                                    content_type='application/octet-stream',
                                    content_md5=hashlib.md5(DATA).digest()))
                        with acceptance_phase('official-sdk-structured-crc64-read'):
                            with structured_client(team) as client:
                                content = client.download_blob(
                                    offset=OFFSET, length=LENGTH, max_concurrency=1,
                                    validate_content='crc64').readall()
                            require(content == DATA[OFFSET:OFFSET + LENGTH],
                                    'SDK decoded bytes differ from exact seeded slice')
                    finally:
                        if created:
                            with acceptance_phase('legacy-sdk-delete-and-confirm'):
                                if blob.exists():
                                    properties = blob.get_blob_properties()
                                    blob.delete_blob(
                                        etag=properties.etag,
                                        match_condition=MatchConditions.IfNotModified)
                                expect_status(404, blob.get_blob_properties)
                                container.delete_container()
                                require(bounded_items(legacy.list_containers()) == [],
                                        'Fresh local account has surviving containers')
            finally:
                with acceptance_phase('local-team-stop'):
                    team.stop()


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument('--binary', required=True, type=Path)
    cli.add_argument('--binary-sha256', required=True)
    cli.add_argument('--source-commit', required=True, choices=[RUNTIME_COMMIT])
    cli.add_argument('--build-receipt', required=True, type=Path)
    cli.add_argument('--build-receipt-sha256', required=True,
                     help='Independent digest of the verified source-bound build receipt')
    cli.add_argument('--sdk-lock', required=True, type=Path)
    cli.add_argument('--approved-isolated-runner', required=True, action='store_true',
                     help='Attest isolation already independently configured; this flag does not configure it')
    args = cli.parse_args()
    try:
        with acceptance_phase('preflight'):
            require(__debug__, 'The existing Team helper requires assertions on')
            verify_build_receipt(args.build_receipt, args.build_receipt_sha256, args.binary_sha256)
            require_loopback_only()
            binary = args.binary.resolve(strict=True)
            require(binary.is_file() and os.access(binary, os.X_OK)
                    and sha256_file(binary) == args.binary_sha256, 'Executable identity differs')
            versions, lock_sha = verify_sdk_lock(args.sdk_lock)
        run(binary)
    except AcceptanceFailure as error:
        print(json.dumps({'result': 'FAIL', 'phase': error.phase, 'error_type': error.error_type,
                          'http_failure': error.status_failure}))
        return 1
    except Exception as error:
        print(json.dumps({'result': 'FAIL', 'phase': 'sdk-setup', 'error_type': type(error).__name__}))
        return 1
    print(json.dumps({
        'result': 'PASS', 'designated_runtime_commit': RUNTIME_COMMIT,
        'build_receipt_sha256': args.build_receipt_sha256,
        'binary_sha256': args.binary_sha256, 'sdk_versions': versions,
        'sdk_lock_sha256': lock_sha, 'read_api_version': READ_API_VERSION,
        'official_sdk_public_crc64_read_exact_payload_bytes': LENGTH,
        'legacy_sdk_seed_and_cleanup': '2023-11-03',
    }))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
