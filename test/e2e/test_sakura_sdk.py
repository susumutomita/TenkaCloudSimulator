"""Failure-path cleanup regressions; no real processes, credentials or network."""
from pathlib import Path
from types import SimpleNamespace
import tempfile
import traceback
import unittest
from unittest.mock import patch

import sakura_sdk


class CleanupTests(unittest.TestCase):
    def exercise(self, problems):
        calls = []
        clients = []
        for index, label in enumerate(('a', 'b')):
            def close(label=label):
                calls.append(label)
                if label in problems:
                    raise problems[label]
            clients.append(SimpleNamespace(process=SimpleNamespace(pid=index + 1),
                                           token=label, db=label, close=close))
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / 'report.json'
            with patch.object(sakura_sdk, 'CLI', side_effect=clients), \
                    patch.object(sakura_sdk, 'create', return_value='1'), \
                    patch.object(sakura_sdk, 'frame', side_effect=ValueError('acceptance failed')):
                with self.assertRaises(BaseException) as caught:
                    sakura_sdk.run('unused-binary', 'unused-client', report)
            self.assertFalse(report.exists(), 'failed acceptance must not publish success')
        self.assertEqual(calls, ['b', 'a'], 'every owned CLI must receive close')
        return caught.exception

    def test_close_failure_does_not_skip_other_cli(self):
        error = self.exercise({'b': RuntimeError('fake-secret')})
        self.assertIsInstance(error, AssertionError)
        self.assertIn('1 owned CLI cleanup check', str(error))
        self.assertNotIn('fake-secret', ''.join(traceback.format_exception(type(error), error, error.__traceback__)))

    def test_multiple_close_failures_are_reported_after_all_attempts(self):
        error = self.exercise({'b': RuntimeError('fake-secret-b'),
                               'a': AssertionError('fake-secret-a')})
        self.assertIsInstance(error, AssertionError)
        self.assertIn('2 owned CLI cleanup checks', str(error))

    def test_interrupt_during_close_still_closes_other_cli(self):
        error = self.exercise({'b': KeyboardInterrupt()})
        self.assertIsInstance(error, AssertionError)

    def test_successful_cleanup_preserves_acceptance_failure(self):
        error = self.exercise({})
        self.assertIsInstance(error, ValueError)
        self.assertEqual(str(error), 'acceptance failed')


if __name__ == '__main__':
    unittest.main()
