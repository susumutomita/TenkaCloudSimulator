"""Check immutable historical provenance and reviewed replacement input identity.

Hashes and review metadata are not an authorship, legal, or clean-room proof.
Root source review must bind the code-freeze commit to these seven source hashes
and the external independent review receipt before pending can become accepted.
This checker uses only the Python standard library and never invokes Git/network.
"""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import sys
from urllib.parse import urlsplit

LICENSE_SHA256 = "02f8d83277fea29291727639f45ddafcbcbf7cbc2d92ea1297eb84f0b8ee9fe2"
LICENSE_PATH = "third_party/KUMO-LICENSE.txt"
LICENSE_MIRROR = "internal/legal/assets/KUMO-LICENSE.txt"
CURRENT_MANIFEST = "third_party/independent-native-source.json"
HISTORICAL_HASHES = {'third_party/kumo-apigateway.json': 'b6eb06d3fe11b12c2e6fbba90050c5c0590afbea083f2ada35952ebfcff2efed',
 'third_party/kumo-cloudwatch-logs.json': '06310e25cce85674608ee86f67e1c1a47c20559c5022581565cb85d41e9a3e50',
 'third_party/kumo-iam.json': '721f11d35c386129514fa9fa341340568072df0481a07db7fd064c3a2378d4c5',
 'third_party/kumo-kms.json': '4516e66d7d5a409918d929a021381ef6066b07e88cad1f91e83d7d8baf5f6bcb'}
HISTORICAL_RUNTIME = {'third_party/kumo-apigateway.json': {'internal/aws/apigateway_store.go': {'gitBlob': '2c7f35c9036966d8dc61755aad3f294cce941ec8',
                                                                           'sha256': 'c5d7b85e8ec9d7771a612548301bfd8c8904f9c66d3fc9a3da38557a6739e3d5',
                                                                           'sourceCommit': '1b34ed4e5fe7c9a314194c10bd2d0261ec7efb98'},
                                      'internal/aws/apirouting/event.go': {'gitBlob': '4df5208109ff4163be2fbcb8803ccbbd8b14548d',
                                                                           'sha256': 'e857f63cbc603977f24fd038466493ab84cf715707691bb65ff06c0e966eca68',
                                                                           'sourceCommit': '1b34ed4e5fe7c9a314194c10bd2d0261ec7efb98'},
                                      'internal/aws/apirouting/path.go': {'gitBlob': '6d52855187cfa8ecde85bb098721fabe19202fc0',
                                                                          'sha256': 'eff3abb00d0f6b8973853dc187458dd483ca55a68670967b01a2027e81e91e68',
                                                                          'sourceCommit': '1b34ed4e5fe7c9a314194c10bd2d0261ec7efb98'}},
 'third_party/kumo-cloudwatch-logs.json': {'internal/aws/cloudwatch_logs_store.go': {'gitBlob': '553966ce2046b508359c40ed79ef7cd3293b716e',
                                                                                     'sha256': 'e51fa3089657926a0af14a00a18681e36a66ff2cb1d819afb7ef314f25080846',
                                                                                     'sourceCommit': '1b34ed4e5fe7c9a314194c10bd2d0261ec7efb98'}},
 'third_party/kumo-iam.json': {'internal/aws/iam_roles.go': {'gitBlob': '00ba32537aab4fb0689d0a759a58ec9aa0b61fd5',
                                                             'sha256': '560d64b591201d0f7201e3358d8182409bdb987e69df5c93a1fcf05cd81f79db',
                                                             'sourceCommit': '1b34ed4e5fe7c9a314194c10bd2d0261ec7efb98'}},
 'third_party/kumo-kms.json': {'internal/aws/kms_types.go': {'gitBlob': '3e302d61f622724a05d28c41d52b0b4335ec7f38',
                                                             'sha256': '7853b7463b96c8122ef0d274dd84bc8dce64923336a9c00f490d21ca8811f172',
                                                             'sourceCommit': '1b34ed4e5fe7c9a314194c10bd2d0261ec7efb98'}}}
SOURCE_HISTORY = {'internal/aws/apigateway_store.go': 'third_party/kumo-apigateway.json',
 'internal/aws/apirouting/event.go': 'third_party/kumo-apigateway.json',
 'internal/aws/apirouting/path.go': 'third_party/kumo-apigateway.json',
 'internal/aws/cloudwatch_logs_store.go': 'third_party/kumo-cloudwatch-logs.json',
 'internal/aws/iam_roles.go': 'third_party/kumo-iam.json',
 'internal/aws/kms_types.go': 'third_party/kumo-kms.json',
 'internal/aws/wire_json_projection.go': None}


class ProvenanceFailure(Exception):
    def __init__(self, code, target):
        self.code, self.target = code, target


def require(condition, code, target):
    if not condition:
        raise ProvenanceFailure(code, target)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def unique_object(pairs):
    value = {}
    for key, item in pairs:
        require(key not in value, "duplicate-json-key", "provenance JSON")
        value[key] = item
    return value


def relative_path(value):
    require(isinstance(value, str) and value and "\\" not in value and "\x00" not in value,
            "unsafe-path", "source inventory")
    path = PurePosixPath(value)
    require(not path.is_absolute() and not any(part in (".", "..") for part in path.parts)
            and str(path) == value, "unsafe-path", "source inventory")
    return path


def read_bytes(root, relative):
    path = relative_path(relative)
    current = root
    for part in path.parts:
        current = current / part
        require(not current.is_symlink(), "symlink-path", relative)
    require(current.is_file(), "missing-file", relative)
    require(current.resolve().is_relative_to(root), "unsafe-path", relative)
    try:
        return current.read_bytes()
    except OSError:
        raise ProvenanceFailure("unreadable-file", relative) from None


def load_json(root, relative):
    try:
        return json.loads(read_bytes(root, relative), object_pairs_hook=unique_object)
    except (ValueError, UnicodeError):
        raise ProvenanceFailure("invalid-json", relative) from None


def exact_fields(value, fields, target):
    require(isinstance(value, dict) and set(value) == set(fields), "invalid-schema", target)


def reviewed_identity(value):
    exact_fields(value, ("sourceCommit", "receiptSha256"), "review identity")
    require(isinstance(value["sourceCommit"], str) and re.fullmatch(r"[0-9a-f]{40}", value["sourceCommit"])
            and isinstance(value["receiptSha256"], str) and re.fullmatch(r"[0-9a-f]{64}", value["receiptSha256"]),
            "invalid-review-identity", "review identity")


def check(root):
    root = Path(root).resolve()
    license_bytes = read_bytes(root, LICENSE_PATH)
    require(hashlib.sha256(license_bytes).hexdigest() == LICENSE_SHA256,
            "license-digest", LICENSE_PATH)
    require(read_bytes(root, LICENSE_MIRROR) == license_bytes, "license-mirror", LICENSE_MIRROR)
    records = {}
    for name, expected_hash in HISTORICAL_HASHES.items():
        record = load_json(root, name)
        exact_fields(record, ("provenanceSchemaVersion", "historicalAdaptation", "historicalRuntimeFiles", "replacement"), name)
        require(type(record["provenanceSchemaVersion"]) is int and record["provenanceSchemaVersion"] == 2,
                "invalid-schema", name)
        require(hashlib.sha256(canonical(record["historicalAdaptation"])).hexdigest() == expected_hash,
                "historical-identity", name)
        require(record["historicalRuntimeFiles"] == HISTORICAL_RUNTIME[name], "historical-runtime-identity", name)
        replacement = record["replacement"]
        exact_fields(replacement, ("currentKumoCode", "status", "reviewedReplacementIdentity", "paths"), name)
        require(replacement["currentKumoCode"] is False, "current-relationship", name)
        expected_paths = set(HISTORICAL_RUNTIME[name])
        paths = replacement["paths"]
        require(isinstance(paths, list) and all(isinstance(p, str) for p in paths)
                and len(paths) == len(set(paths)) and set(paths) == expected_paths,
                "replacement-mapping", name)
        records[name] = record

    current = load_json(root, CURRENT_MANIFEST)
    exact_fields(current, ("provenanceSchemaVersion", "status", "reviewedReplacementIdentity", "sources"), CURRENT_MANIFEST)
    require(type(current["provenanceSchemaVersion"]) is int and current["provenanceSchemaVersion"] == 1,
            "invalid-schema", CURRENT_MANIFEST)
    sources = current["sources"]
    require(isinstance(sources, list), "invalid-schema", CURRENT_MANIFEST)
    paths = []
    for source in sources:
        exact_fields(source, ("path", "sha256", "status", "historicalManifest", "publicReferences"), CURRENT_MANIFEST)
        relative_path(source["path"])
        paths.append(source["path"])
    require(len(paths) == len(set(paths)), "duplicate-source", CURRENT_MANIFEST)
    require(set(paths) == set(SOURCE_HISTORY), "source-inventory", CURRENT_MANIFEST)
    require(current["status"] == "accepted", "pending-review", CURRENT_MANIFEST)
    identity = current["reviewedReplacementIdentity"]
    reviewed_identity(identity)
    for name, record in records.items():
        replacement = record["replacement"]
        require(replacement["status"] == "accepted", "pending-review", name)
        require(replacement["reviewedReplacementIdentity"] == identity, "review-identity-mismatch", name)
    for source in sources:
        path = source["path"]
        require(source["historicalManifest"] == SOURCE_HISTORY[path], "replacement-mapping", path)
        require(source["status"] == "accepted", "pending-review", path)
        digest = source["sha256"]
        require(isinstance(digest, str) and re.fullmatch(r"[0-9a-f]{64}", digest), "invalid-source-digest", path)
        references = source["publicReferences"]
        require(isinstance(references, list) and references and all(isinstance(ref, str) for ref in references),
                "missing-public-references", path)
        for reference in references:
            try:
                url = urlsplit(reference)
                valid = url.scheme == "https" and url.hostname and not url.username and not url.password
            except ValueError:
                valid = False
            require(valid, "invalid-public-reference", path)
        require(hashlib.sha256(read_bytes(root, path)).hexdigest() == digest, "source-digest", path)
        history = SOURCE_HISTORY[path]
        if history is not None:
            require(digest != HISTORICAL_RUNTIME[history][path]["sha256"], "unchanged-historical-source", path)
    return {"ok": True, "historicalManifests": len(records), "replacementSources": len(sources),
            "licenseSha256": LICENSE_SHA256, "reviewedReplacementIdentity": identity,
            "qualification": "Identity and review metadata checked; independent authorship and legal/refinement conclusions require source review, not hashes or removed comments."}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    try:
        result = check(args.root)
    except ProvenanceFailure as failure:
        print(json.dumps({"ok": False, "error": failure.code, "target": failure.target}))
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main())
