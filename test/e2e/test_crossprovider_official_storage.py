"""Fail-closed checks for the real official-storage acceptance harness.

The SDK path check uses the pinned official client and Requests without sending
HTTP. Runtime/service conformance remains in crossprovider_official_storage.py.
"""
import importlib.util
import itertools
import json
from pathlib import Path
import subprocess
import sys
import unittest


HARNESS = Path(__file__).with_name('crossprovider_official_storage.py')
spec = importlib.util.spec_from_file_location('official_storage_acceptance', HARNESS)
acceptance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(acceptance)


class AcceptanceBoundaryTests(unittest.TestCase):
    def test_missing_sdk_fails_before_launch(self):
        # -S excludes installed SDK distributions even in the acceptance venv.
        # A launch attempt would fail as a normal runtime error rather than the
        # required dependency exit code; no simulator fixture is needed here.
        result = subprocess.run(
            [sys.executable, '-S', str(HARNESS), '--binary', sys.executable,
             '--provider', 'both'], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 2)
        report = json.loads(result.stdout)
        self.assertEqual(report['result'], 'FAIL/NOT-RUN')
        self.assertEqual(set(report['missingOfficialSDKs']), {
            'google-cloud-storage', 'google-auth', 'azure-storage-blob',
            'azure-core', 'requests',
        })

    def test_optimized_python_cannot_disable_acceptance_assertions(self):
        result = subprocess.run(
            [sys.executable, '-O', str(HARNESS), '--binary', sys.executable],
            capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Acceptance requires enabled assertions', result.stderr)
        self.assertNotIn('PASS', result.stdout)

    def test_azure_official_path_limit_is_explicit_and_independent(self):
        report = acceptance.azure_sdk_key_contract()
        self.assertEqual({row['key']: row['prepared_key'] for row in report},
                         {'a/./b': 'a/b', 'a/../b': 'b'})
        self.assertEqual(len(acceptance.KEYS), 12)
        self.assertEqual(len(acceptance.AZURE_KEYS), 10)
        self.assertTrue({'a/./b', 'a/../b'}.issubset(acceptance.KEYS))
        self.assertEqual(set(acceptance.AZURE_KEYS),
                         set(acceptance.KEYS) - {'a/./b', 'a/../b'})

    def test_nonterminating_sdk_list_cannot_succeed(self):
        with self.assertRaisesRegex(AssertionError, 'finite acceptance bound'):
            acceptance.bounded_items(itertools.count(), limit=3)

    def test_unrelated_failure_is_not_expected_http_denial(self):
        def transport_failure():
            raise OSError('test-only-private-request-value')

        with self.assertRaises(AssertionError) as failure:
            acceptance.expect_status(403, transport_failure)
        self.assertNotIn('test-only-private-request-value', str(failure.exception))

    def test_phase_failure_does_not_expose_exception_content(self):
        with self.assertRaises(acceptance.AcceptanceFailure) as failure:
            with acceptance.acceptance_phase('gcp:test-boundary'):
                raise ValueError('test-only-private-request-value')
        self.assertEqual(failure.exception.phase, 'gcp:test-boundary')
        self.assertEqual(failure.exception.error_type, 'ValueError')
        self.assertNotIn('test-only-private-request-value', str(failure.exception))


if __name__ == '__main__':
    unittest.main()
