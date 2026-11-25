"""Offline CLI/identity checks; never start a simulator or import the SDK."""

import contextlib
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import azure_blob_structured_crc64_sdk as fixture


CANDIDATE = '26414108dfb499172b7db81d912055bacfa91231'
EARLIER_BUILD = 'aa74a0088b3aa3a2c50961a61b40361c6bba4e1c'


class ReviewedIdentityTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.binary = self.root / 'simulator'
        self.binary.write_bytes(b'offline fixture, not an executable program')
        self.binary.chmod(0o700)
        self.binary_sha = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.receipt = self.root / 'receipt.json'
        self.sdk_lock = self.root / 'sdk-lock.json'

    def receipt_digest(self, source=CANDIDATE, binary_sha=None):
        self.receipt.write_text(json.dumps({
            'source_commit': source,
            'binary_sha256': self.binary_sha if binary_sha is None else binary_sha,
        }))
        return hashlib.sha256(self.receipt.read_bytes()).hexdigest()

    def argv(self, source=CANDIDATE, receipt_sha=None):
        cmd = [
            'fixture', '--binary', str(self.binary), '--binary-sha256', self.binary_sha,
            '--build-receipt', str(self.receipt),
            '--build-receipt-sha256', self.receipt_digest() if receipt_sha is None else receipt_sha,
            '--sdk-lock', str(self.sdk_lock), '--approved-isolated-runner',
        ]
        if source is not None:
            cmd.extend(['--source-commit', source])
        return cmd

    def invoke(self, argv, sdk_error=None):
        output = io.StringIO()
        with (
            mock.patch.object(sys, 'argv', argv),
            mock.patch.object(fixture, 'require_loopback_only') as isolation,
            mock.patch.object(fixture, 'verify_sdk_lock',
                              return_value=({'azure-storage-blob': fixture.SDK_VERSION}, 'a' * 64),
                              side_effect=sdk_error) as lock,
            mock.patch.object(fixture, 'run') as run,
            contextlib.redirect_stdout(output),
            contextlib.redirect_stderr(io.StringIO()),
        ):
            try:
                status = fixture.main()
            except SystemExit as error:
                status = error.code
        return status, json.loads(output.getvalue() or '{}'), isolation, lock, run

    def test_uses_reviewed_candidate_for_receipt_and_report(self):
        for source in (CANDIDATE, 'c' * 40):
            with self.subTest(source=source):
                receipt_sha = self.receipt_digest(source=source)
                status, report, isolation, lock, run = self.invoke(self.argv(source, receipt_sha))
                self.assertEqual(status, 0)
                self.assertEqual(report['result'], 'PASS')
                self.assertEqual(report['designated_runtime_commit'], source)
                self.assertEqual(report['build_receipt_sha256'], receipt_sha)
                self.assertEqual(report['binary_sha256'], self.binary_sha)
                isolation.assert_called_once_with()
                lock.assert_called_once_with(self.sdk_lock)
                run.assert_called_once_with(self.binary.resolve())

    def test_malformed_source_fails_before_isolation_or_sdk(self):
        for source in (CANDIDATE[:8], CANDIDATE.upper(), 'main', 'z' * 40):
            with self.subTest(source=source):
                status, report, isolation, lock, run = self.invoke(self.argv(source))
                self.assertEqual((status, report['phase']), (1, 'preflight'))
                isolation.assert_not_called()
                lock.assert_not_called()
                run.assert_not_called()

    def test_independent_source_is_required(self):
        status, report, isolation, lock, run = self.invoke(self.argv(source=None))
        self.assertEqual((status, report), (2, {}))
        isolation.assert_not_called()
        lock.assert_not_called()
        run.assert_not_called()

    def test_rejects_valid_digest_for_superseded_source(self):
        status, report, isolation, lock, run = self.invoke(
            self.argv(receipt_sha=self.receipt_digest(source=EARLIER_BUILD)))
        self.assertEqual((status, report['phase']), (1, 'preflight'))
        isolation.assert_not_called()
        lock.assert_not_called()
        run.assert_not_called()

    def test_rejects_changed_receipt(self):
        self.receipt_digest()
        status, report, isolation, lock, run = self.invoke(self.argv(receipt_sha='a' * 64))
        self.assertEqual((status, report['phase']), (1, 'preflight'))
        isolation.assert_not_called()
        lock.assert_not_called()
        run.assert_not_called()

    def test_rejects_receipt_bound_to_another_binary(self):
        status, report, isolation, lock, run = self.invoke(
            self.argv(receipt_sha=self.receipt_digest(binary_sha='a' * 64)))
        self.assertEqual((status, report['phase']), (1, 'preflight'))
        isolation.assert_not_called()
        lock.assert_not_called()
        run.assert_not_called()

    def test_rejects_changed_executable_before_sdk(self):
        argv = self.argv()
        self.binary.write_bytes(b'not the independently reviewed bytes')
        status, report, isolation, lock, run = self.invoke(argv)
        self.assertEqual((status, report['phase']), (1, 'preflight'))
        isolation.assert_called_once_with()
        lock.assert_not_called()
        run.assert_not_called()

    def test_sdk_pin_failure_stops_runner(self):
        status, report, isolation, lock, run = self.invoke(self.argv(), sdk_error=ValueError())
        self.assertEqual((status, report['phase']), (1, 'preflight'))
        isolation.assert_called_once_with()
        lock.assert_called_once_with(self.sdk_lock)
        run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
