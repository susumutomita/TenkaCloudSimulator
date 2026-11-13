import hashlib
import pathlib
import tempfile
import unittest
from unittest.mock import patch
import boot_probe


class ArtifactValidation(unittest.TestCase):
    def test_matching_artifacts(self):
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'kernel'
            path.write_bytes(b'test fixture, never executed')
            expected = {'kernel': hashlib.sha256(path.read_bytes()).hexdigest()}
            with patch.object(boot_probe, 'EXPECTED', expected):
                self.assertEqual(boot_probe.verified_files(root), [str(path.resolve())])

    def test_tampered_artifact_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            (pathlib.Path(root) / 'vmlinuz-virt').write_bytes(b'wrong')
            with self.assertRaisesRegex(ValueError, 'digest mismatch'):
                boot_probe.verified_files(root)

    def test_symlink_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            target = pathlib.Path(root) / 'target'
            target.write_bytes(b'test')
            (pathlib.Path(root) / 'vmlinuz-virt').symlink_to(target)
            with self.assertRaisesRegex(ValueError, 'invalid artifact'):
                boot_probe.verified_files(root)


if __name__ == '__main__':
    unittest.main()
