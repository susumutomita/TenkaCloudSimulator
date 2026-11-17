"""The wrapper pins the shared runner and preserves native acceptance contracts."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from run import HERE, verify_pin

ROOT = HERE.parents[1]


class AdapterTests(unittest.TestCase):
    def test_pinned_runner_and_license(self):
        source = verify_pin()
        pin = json.loads((HERE / 'upstream.json').read_text())
        self.assertEqual(pin['commit'], 'b434822e6bd2e80480dc8af610ad3f2a234a35e0')
        self.assertEqual(hashlib.sha256(source.read_bytes()).hexdigest(), pin['sha256'])
        self.assertIn('MIT License', (HERE / 'vendor' / 'LICENSE').read_text())

    def test_modified_runner_is_not_executed(self):
        with tempfile.TemporaryDirectory() as temp:
            copy = Path(temp) / 'adapter'
            shutil.copytree(HERE, copy)
            marker = Path(temp) / 'executed'
            (copy / 'vendor' / 'verify.py').write_text(f'open({str(marker)!r}, "w").close()')
            result = subprocess.run([sys.executable, str(copy / 'run.py'), 'fast'], capture_output=True)
            self.assertEqual(result.returncode, 2)
            self.assertIn(b'blocked', result.stderr)
            self.assertFalse(marker.exists())

    def test_native_commands_are_preserved(self):
        data = json.loads((ROOT / 'verification.json').read_text())
        self.assertEqual(data['profiles']['fast'], ['native-checks'])
        self.assertEqual(data['checks']['native-checks']['argv'], ['make', 'before-commit'])
        self.assertEqual(data['checks']['e2e']['argv'], ['make', 'e2e'])
        self.assertIn('battle-vm', data['profiles']['release'])
        self.assertTrue(data['checks']['battle-vm']['blocked_reason'])
        self.assertNotIn('argv', data['checks']['battle-vm'])

    def test_existing_factory_gate_still_runs(self):
        makefile = (ROOT / 'Makefile').read_text()
        self.assertIn('$(PYTHON) scripts/factory_acceptance.py', makefile)
        self.assertIn('$(MAKE) test build', makefile)
        self.assertIn("-m unittest discover -s scripts/verification", makefile)

    def test_release_vm_gap_is_recorded_blocked(self):
        # Call the exact pinned runner against a disposable fixture, not a VM.
        import runpy
        runner = runpy.run_path(str(verify_pin()))
        data = json.loads((ROOT / 'verification.json').read_text())
        with tempfile.TemporaryDirectory() as temp:
            result = runner['run_check']('battle-vm', data['checks']['battle-vm'], ROOT, Path(temp))
        self.assertEqual(result['status'], 'blocked')
        self.assertIsNone(result['exit_code'])


if __name__ == '__main__':
    unittest.main()
