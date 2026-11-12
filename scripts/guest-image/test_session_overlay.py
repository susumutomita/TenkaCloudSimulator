import hashlib
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import unittest

class OverlayTests(unittest.TestCase):
    def test_exact_inputs_license_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            args = [sys.executable, str(pathlib.Path(__file__).with_name('session-overlay.py'))]
            for name in ('binary', 'unit', 'license'):
                path = root / name
                data = ('inert ' + name).encode()
                path.write_bytes(data)
                args += ['--' + name, str(path), '--' + name + '-sha256', hashlib.sha256(data).hexdigest()]
            output = root / 'out.tar'
            args += ['--output', str(output)]
            self.assertEqual(subprocess.run(args, capture_output=True).returncode, 0)
            original = output.read_bytes()
            with tarfile.open(output) as archive:
                self.assertEqual(archive.extractfile('usr/share/licenses/tenkacloud-session-relay/LICENSE').read(), b'inert license')
                self.assertTrue(all(not n.startswith('/') and '..' not in n.split('/') for n in archive.getnames()))
            self.assertNotEqual(subprocess.run(args, capture_output=True).returncode, 0)
            self.assertEqual(output.read_bytes(), original)
            output.unlink()
            (root / 'binary').write_bytes(b'drift')
            self.assertNotEqual(subprocess.run(args, capture_output=True).returncode, 0)
            self.assertFalse(output.exists())
if __name__ == '__main__':
    unittest.main()
