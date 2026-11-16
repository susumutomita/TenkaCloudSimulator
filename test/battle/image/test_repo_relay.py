"""Offline regression checks for the build relay's TLS trust configuration."""
import os
from pathlib import Path
import runpy
import ssl
import sys
import tempfile
import unittest
from unittest.mock import patch


RELAY = Path(__file__).with_name('repo_relay.py')


class RelayTrustTests(unittest.TestCase):
    def load_relay(self, directory, certificate=None):
        environment = dict(os.environ)
        environment.pop('SSL_CERT_FILE', None)
        if certificate is not None:
            environment['SSL_CERT_FILE'] = str(certificate)
        with patch.dict(os.environ, environment, clear=True), \
                patch.object(sys, 'argv', [str(RELAY), directory,
                                          str(Path(directory) / 'provenance.jsonl'), '8099']), \
                patch('http.server.ThreadingHTTPServer') as server:
            namespace = runpy.run_path(str(RELAY))
        server.assert_called_once_with(('127.0.0.1', 8099), namespace['Handler'])
        server.return_value.serve_forever.assert_called_once_with()
        return namespace['context']

    def assert_verified(self, context):
        self.assertEqual(context.verify_mode, ssl.CERT_REQUIRED)
        self.assertTrue(context.check_hostname)

    def test_system_trust_without_environment_specific_ca(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assert_verified(self.load_relay(directory))

    def test_explicit_ca_preserves_verification(self):
        # Reuse an installed public trust anchor, without contacting a server.
        roots = ssl.create_default_context().get_ca_certs(binary_form=True)
        self.assertTrue(roots, 'the test host must have a system trust store')
        with tempfile.TemporaryDirectory() as directory:
            certificate = Path(directory) / 'ca.pem'
            certificate.write_text(ssl.DER_cert_to_PEM_cert(roots[0]))
            context = self.load_relay(directory, certificate)
            self.assert_verified(context)
            self.assertEqual(context.cert_store_stats()['x509_ca'], 1)

    def test_missing_explicit_ca_fails_instead_of_falling_back(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(FileNotFoundError):
                self.load_relay(directory, Path(directory) / 'missing.pem')

    def test_invalid_explicit_ca_fails_instead_of_disabling_tls(self):
        with tempfile.TemporaryDirectory() as directory:
            certificate = Path(directory) / 'invalid.pem'
            certificate.write_text('not a certificate')
            with self.assertRaises(ssl.SSLError):
                self.load_relay(directory, certificate)


if __name__ == '__main__':
    unittest.main()
