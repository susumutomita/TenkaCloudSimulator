"""Small CLI fixtures: input identity/review gate, not authorship proof."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("check-native-provenance.py").resolve()
REPO = SCRIPT.parents[1]
spec = importlib.util.spec_from_file_location("native_provenance", SCRIPT)
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class NativeProvenanceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="native-provenance-fixture-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.identity = {"sourceCommit": "1" * 40, "receiptSha256": "2" * 64}
        for relative in (gate.LICENSE_PATH, gate.LICENSE_MIRROR):
            path = self.root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes((REPO / gate.LICENSE_PATH).read_bytes())
        self.current = {"provenanceSchemaVersion": 1, "status": "accepted",
                        "reviewedReplacementIdentity": self.identity, "sources": []}
        for path, history in gate.SOURCE_HISTORY.items():
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("// Unit fixture only, not a production source review.\npackage fixture\n")
            self.current["sources"].append({"path": path, "sha256": hashlib.sha256(target.read_bytes()).hexdigest(),
                "status": "accepted", "historicalManifest": history,
                "publicReferences": ["https://docs.aws.amazon.com/IAM/latest/APIReference/Welcome.html"]})
        self.write(gate.CURRENT_MANIFEST, self.current)
        for relative in gate.HISTORICAL_HASHES:
            record = json.loads((REPO / relative).read_text())
            record["replacement"]["status"] = "accepted"
            record["replacement"]["reviewedReplacementIdentity"] = self.identity
            self.write(relative, record)

    def write(self, relative, value):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, indent=2) + "\n")

    def mutate(self, relative, callback):
        value = json.loads((self.root / relative).read_text())
        callback(value)
        self.write(relative, value)

    def run_gate(self, error=None):
        result = subprocess.run([sys.executable, str(SCRIPT), "--root", str(self.root)],
                                cwd=tempfile.gettempdir(), text=True, capture_output=True, timeout=5)
        # Parse/runtime/timeout failures cannot stand in for an expected refusal.
        self.assertEqual(result.returncode, 0 if error is None else 1, result.stderr)
        self.assertEqual(result.stderr, "")
        value = json.loads(result.stdout)
        self.assertEqual(value["ok"], error is None)
        if error is not None:
            self.assertEqual(value["error"], error)
        return value

    def test_minimal_reviewed_fixture_passes_explicit_root(self):
        value = self.run_gate()
        self.assertEqual((value["historicalManifests"], value["replacementSources"]), (4, 7))
        self.assertIn("not hashes", value["qualification"])

    def test_missing_original_license_fails(self):
        (self.root / gate.LICENSE_PATH).unlink()
        self.run_gate("missing-file")

    def test_changed_original_license_fails_even_if_mirror_matches(self):
        for path in (gate.LICENSE_PATH, gate.LICENSE_MIRROR):
            (self.root / path).write_text("different license")
        self.run_gate("license-digest")

    def test_missing_or_changed_embedded_mirror_fails(self):
        path = self.root / gate.LICENSE_MIRROR
        path.write_text("different mirror")
        self.run_gate("license-mirror")
        path.unlink()
        self.run_gate("missing-file")

    def test_all_four_missing_historical_manifests_fail(self):
        for relative in gate.HISTORICAL_HASHES:
            path = self.root / relative
            original = path.read_bytes()
            path.unlink()
            self.run_gate("missing-file")
            path.write_bytes(original)

    def test_all_four_historical_source_identity_changes_fail(self):
        for relative in gate.HISTORICAL_HASHES:
            path = self.root / relative
            original = path.read_bytes()
            self.mutate(relative, lambda record: record["historicalAdaptation"].update(commit="0" * 40))
            self.run_gate("historical-identity")
            path.write_bytes(original)

    def test_historical_modifications_localstack_and_source_hashes_are_protected(self):
        relative = "third_party/kumo-cloudwatch-logs.json"
        original = (self.root / relative).read_bytes()
        changes = [lambda old: old["sourceGitBlobs"].update({"internal/service/cloudwatchlogs/types.go": "0" * 40}),
                   lambda old: old["modifications"].pop(),
                   lambda old: old["localStackReview"].update(decision="changed"),
                   lambda old: old.update(copyright="changed", licenseSha256="0" * 64)]
        for change in changes:
            self.mutate(relative, lambda record: change(record["historicalAdaptation"]))
            self.run_gate("historical-identity")
            (self.root / relative).write_bytes(original)

    def test_historical_local_runtime_snapshot_changes_fail(self):
        relative = "third_party/kumo-iam.json"
        self.mutate(relative, lambda record: record["historicalRuntimeFiles"]["internal/aws/iam_roles.go"].update(sha256="0" * 64))
        self.run_gate("historical-runtime-identity")

    def test_missing_extra_and_duplicate_source_inventory_fail(self):
        original = copy.deepcopy(self.current)
        for index in range(len(original["sources"])):
            current = copy.deepcopy(original)
            current["sources"].pop(index)
            self.write(gate.CURRENT_MANIFEST, current)
            self.run_gate("source-inventory")
        for change, error in [(lambda rows: rows.append({**rows[0], "path": "internal/aws/extra.go"}), "source-inventory"),
                              (lambda rows: rows.append(copy.deepcopy(rows[0])), "duplicate-source")]:
            current = copy.deepcopy(original)
            change(current["sources"])
            self.write(gate.CURRENT_MANIFEST, current)
            self.run_gate(error)

    def test_traversal_absolute_backslash_and_normalized_paths_fail(self):
        for path in ("../outside.go", "/tmp/outside.go", "internal\\aws\\iam_roles.go", "internal/aws/../iam_roles.go", "internal//aws/iam_roles.go"):
            current = copy.deepcopy(self.current)
            current["sources"][0]["path"] = path
            self.write(gate.CURRENT_MANIFEST, current)
            self.run_gate("unsafe-path")

    def test_source_hash_drift_missing_file_and_symlink_fail(self):
        path = self.root / self.current["sources"][0]["path"]
        original = path.read_bytes()
        path.write_text("changed source bytes")
        self.run_gate("source-digest")
        path.unlink()
        self.run_gate("missing-file")
        path.symlink_to(self.root / gate.LICENSE_PATH)
        self.run_gate("symlink-path")
        path.unlink()
        path.write_bytes(original)

    def test_pending_global_per_manifest_and_per_source_fail(self):
        self.mutate(gate.CURRENT_MANIFEST, lambda current: current.update(status="pending-review"))
        self.run_gate("pending-review")
        self.write(gate.CURRENT_MANIFEST, self.current)
        for relative in gate.HISTORICAL_HASHES:
            original = (self.root / relative).read_bytes()
            self.mutate(relative, lambda record: record["replacement"].update(status="pending-review"))
            self.run_gate("pending-review")
            (self.root / relative).write_bytes(original)
        for index in range(len(self.current["sources"])):
            current = copy.deepcopy(self.current)
            current["sources"][index]["status"] = "pending-review"
            self.write(gate.CURRENT_MANIFEST, current)
            self.run_gate("pending-review")

    def test_invalid_or_mismatched_review_identity_fails(self):
        self.mutate(gate.CURRENT_MANIFEST, lambda current: current.update(reviewedReplacementIdentity=None))
        self.run_gate("invalid-schema")
        self.write(gate.CURRENT_MANIFEST, self.current)
        self.mutate("third_party/kumo-kms.json", lambda record: record["replacement"].update(reviewedReplacementIdentity={**self.identity, "sourceCommit": "3" * 40}))
        self.run_gate("review-identity-mismatch")

    def test_current_kumo_flag_and_mapping_changes_fail(self):
        relative = "third_party/kumo-iam.json"
        original = (self.root / relative).read_bytes()
        self.mutate(relative, lambda record: record["replacement"].update(currentKumoCode=True))
        self.run_gate("current-relationship")
        (self.root / relative).write_bytes(original)
        self.mutate(relative, lambda record: record["replacement"]["paths"].append("internal/aws/kms_types.go"))
        self.run_gate("replacement-mapping")

    def test_duplicate_json_keys_fail_as_validation_not_parse_traceback(self):
        path = self.root / gate.CURRENT_MANIFEST
        path.write_text(path.read_text().replace('"provenanceSchemaVersion": 1', '"provenanceSchemaVersion": 1, "provenanceSchemaVersion": 1', 1))
        self.run_gate("duplicate-json-key")

    def test_placeholder_digest_bad_reference_and_wrong_history_mapping_fail(self):
        for patch, error in [({"sha256": None}, "invalid-source-digest"),
                             ({"publicReferences": []}, "missing-public-references"),
                             ({"publicReferences": ["file:///tmp/not-a-public-reference"]}, "invalid-public-reference"),
                             ({"historicalManifest": "third_party/wrong.json"}, "replacement-mapping")]:
            current = copy.deepcopy(self.current)
            current["sources"][0].update(patch)
            self.write(gate.CURRENT_MANIFEST, current)
            self.run_gate(error)

    def test_each_of_seven_source_hash_checks_is_mandatory(self):
        for source in self.current["sources"]:
            path = self.root / source["path"]
            original = path.read_bytes()
            path.write_text("drifted source")
            self.run_gate("source-digest")
            path.write_bytes(original)

    def test_unchanged_original_runtime_cannot_be_declared_a_replacement(self):
        source = next(row for row in self.current["sources"] if row["historicalManifest"] is not None)
        # Exercise this optional equality fence with minimal synthetic old bytes.
        # Never archive/copy the old adapted Go source into the test suite, or
        # depend on current production files still containing the old bytes.
        name, path = source["historicalManifest"], source["path"]
        with patch.dict(gate.HISTORICAL_RUNTIME[name][path], sha256=source["sha256"]):
            self.mutate(name, lambda record: record["historicalRuntimeFiles"][path].update(sha256=source["sha256"]))
            with self.assertRaises(gate.ProvenanceFailure) as caught:
                gate.check(self.root)
            self.assertEqual(caught.exception.code, "unchanged-historical-source")

    def test_invalid_review_identity_values_fail(self):
        for identity in ({"sourceCommit": "pending", "receiptSha256": "2" * 64},
                         {"sourceCommit": "1" * 40, "receiptSha256": "not-a-hash"}):
            current = copy.deepcopy(self.current)
            current["reviewedReplacementIdentity"] = identity
            self.write(gate.CURRENT_MANIFEST, current)
            self.run_gate("invalid-review-identity")

    def test_asset_checker_keeps_all_preexisting_non_kumo_checks(self):
        source = (REPO / "scripts/check-assets.py").read_text()
        self.assertIn("native_provenance=runpy.run_path('scripts/check-native-provenance.py')", source)
        self.assertIn("assert native_provenance['main']()==0", source)
        self._assert_original_asset_prefix(source)

    def _assert_original_asset_prefix(self, source):
        # Root-reviewed API-only extension: remove exactly this five-ID literal
        # and this union operand, then require the ORIGINAL baseline1b34 bytes.
        # No generic regex, changing hash, or tolerance for other check edits.
        extension = "expected_sakura={'SakuraListDisks', 'SakuraCreateDisk', 'SakuraGetDisk', 'SakuraUpdateDisk', 'SakuraDeleteDisk'}\n"
        original_assert = "assert len(operations)==len(set(operations)) and set(operations)==expected_legacy|expected_rest|expected_multicloud"
        extended_assert = original_assert + "|expected_sakura"
        prefix = source.split("# Retain exact historical rights", 1)[0]
        # Require the three reviewed Route53 adapter changes exactly once.
        # Normalize only their exact bytes; the original prefix hash is unchanged.
        for approved, original in self._route53_asset_adapters():
            self.assertEqual(prefix.count(approved), 1, "Exact approved Route53 adapter is required once")
            prefix = prefix.replace(approved, original, 1)
        self.assertEqual(prefix.count(extension), 1, "Exact approved five-ID extension is required once")
        self.assertEqual(prefix.count(extended_assert), 1, "Exact approved closed union is required once")
        prefix = prefix.replace(extension, "", 1).replace(extended_assert, original_assert, 1)
        # Multipart adds one reviewed operation to the same closed native set.
        # Normalize only this exact literal; all original guard bytes stay pinned.
        multipart = "expected_legacy={'S3MultipartUpload',"
        self.assertEqual(prefix.count(multipart), 1, "Exact approved multipart operation is required once")
        prefix = prefix.replace(multipart, "expected_legacy={", 1)
        self.assertEqual(hashlib.sha256(prefix.encode()).hexdigest(), "331cc7e21fd3a30aa72b690846cf60c04f06ea19c0035ac73ea13f629334d685")

    @staticmethod
    def _route53_asset_adapters():
        return (
            ("import hashlib,json,runpy\n", "import hashlib,json\n"),
            ("route53_contract=runpy.run_path('test/e2e/route53_openapi.py')\n"
             "inherited_spec=route53_contract['project_before_route53'](spec)\n", ""),
            ("operations=[v['operationId'] for p in inherited_spec['paths'].values()",
             "operations=[v['operationId'] for p in spec['paths'].values()"),
        )

    def test_asset_prefix_rejects_missing_changed_duplicate_route53_adapters(self):
        source = (REPO / "scripts/check-assets.py").read_text()
        for approved, original in self._route53_asset_adapters():
            self.assertEqual(source.count(approved), 1)
            for label, replacement in (("missing", original),
                                       ("changed", approved.replace("route53", "unreviewed", 1) if "route53" in approved else approved.replace("inherited_spec", "unreviewed_spec", 1)),
                                       ("duplicate", approved + approved)):
                # The import has no lowercase route53 or inherited_spec token.
                if label == "changed" and replacement == approved:
                    replacement = approved.replace("runpy", "runpy2", 1)
                with self.subTest(adapter=approved, mutation=label), self.assertRaises(AssertionError):
                    self._assert_original_asset_prefix(source.replace(approved, replacement, 1))

    def test_asset_prefix_rejects_changed_duplicate_or_missing_multipart_extension(self):
        source = (REPO / "scripts/check-assets.py").read_text()
        approved = "expected_legacy={'S3MultipartUpload',"
        for modified in (
            source.replace(approved, "expected_legacy={'S3MultipartAnything',", 1),
            source.replace(approved, "expected_legacy={'S3MultipartUpload','S3MultipartUpload',", 1),
            source.replace(approved, "expected_legacy={", 1),
        ):
            with self.subTest(modified_sha256=hashlib.sha256(modified.encode()).hexdigest()):
                with self.assertRaises(AssertionError):
                    self._assert_original_asset_prefix(modified)

    def test_asset_prefix_rejects_unrelated_strict_check_weakening(self):
        source = (REPO / "scripts/check-assets.py").read_text()
        self.assertEqual(source.count("assert lambda_operations=="), 1)
        weakened = source.replace("assert lambda_operations==", "assert lambda_operations<=", 1)
        with self.assertRaises(AssertionError):
            self._assert_original_asset_prefix(weakened)

    def test_asset_prefix_rejects_changed_duplicate_or_missing_five_id_extension(self):
        source = (REPO / "scripts/check-assets.py").read_text()
        extension = "expected_sakura={'SakuraListDisks', 'SakuraCreateDisk', 'SakuraGetDisk', 'SakuraUpdateDisk', 'SakuraDeleteDisk'}\n"
        self.assertEqual(source.count(extension), 1)
        for modified in (
            source.replace(extension, extension.replace("'SakuraDeleteDisk'", "'SakuraDeleteEverything'"), 1),
            source.replace(extension, extension + extension, 1),
            source.replace(extension, "", 1),
            source.replace("|expected_sakura", "", 1),
        ):
            with self.subTest(modified_sha256=hashlib.sha256(modified.encode()).hexdigest()):
                with self.assertRaises(AssertionError):
                    self._assert_original_asset_prefix(modified)


if __name__ == "__main__":
    unittest.main()
