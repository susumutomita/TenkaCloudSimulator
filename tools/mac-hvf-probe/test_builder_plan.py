import hashlib
import pathlib
import tempfile
import unittest
from unittest import mock
import builder_plan


class BuilderPreparation(unittest.TestCase):
    def inputs(self, root):
        image, overlay = root / 'base', root / 'overlay.tar'
        image.write_bytes(b'base fixture')
        overlay.write_bytes(b'overlay fixture')
        return image, overlay

    def test_copy_is_independent_and_no_reuse(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            image, overlay = self.inputs(root)
            output = root / 'new'
            with mock.patch.object(builder_plan, 'IMAGE_SHA512', hashlib.sha512(image.read_bytes()).hexdigest()), mock.patch.object(builder_plan, 'OVERLAY_SHA256', hashlib.sha256(overlay.read_bytes()).hexdigest()):
                receipt = builder_plan.prepare(image, overlay, output)
                self.assertFalse(receipt['vmExecuted'])
                self.assertNotEqual(image.stat().st_ino, (output / 'candidate.qcow2').stat().st_ino)
                (output / 'candidate.qcow2').write_bytes(b'guest changes')
                self.assertEqual(image.read_bytes(), b'base fixture')
                with self.assertRaises(FileExistsError):
                    builder_plan.prepare(image, overlay, output)

    def test_drift_rejected_before_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            image, overlay = self.inputs(root)
            with self.assertRaises(ValueError):
                builder_plan.prepare(image, overlay, root / 'new')
            self.assertFalse((root / 'new').exists())

    def test_copy_failure_is_not_promoted(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            image, overlay = self.inputs(root)
            with mock.patch.object(builder_plan, 'IMAGE_SHA512', hashlib.sha512(image.read_bytes()).hexdigest()), mock.patch.object(builder_plan, 'OVERLAY_SHA256', hashlib.sha256(overlay.read_bytes()).hexdigest()), mock.patch.object(builder_plan.shutil, 'copyfileobj', side_effect=OSError('disk full')):
                with self.assertRaises(OSError):
                    builder_plan.prepare(image, overlay, root / 'new')
            self.assertTrue((root / 'new/INCOMPLETE').exists())
            self.assertFalse((root / 'new/preparation.json').exists())

    def test_guest_script_fixed_hashes_and_failure_marker(self):
        script = builder_plan.BUILD_SCRIPT
        self.assertIn(builder_plan.OVERLAY_SHA256, script)
        self.assertIn(builder_plan.BINARY_SHA256, script)
        self.assertIn('mount -o ro', script)
        self.assertIn('TCS_BUILD_FAILED', script)
        self.assertNotIn('curl', script)
        self.assertNotIn('systemctl start tenkacloud-session-relay', script)

    def test_cleanup_runs_after_final_with_cloud_init_stopped(self):
        script = builder_plan.BUILD_SCRIPT
        self.assertIn('After=cloud-final.service', script)
        self.assertIn('systemctl start --no-block tcs-builder-finalize.service', script)
        self.assertLess(script.index('systemctl stop cloud-init-main.service'), script.index('cloud-init clean --logs --seed --machine-id'))
        self.assertIn('MainPID --value cloud-init-main.service', script)
        self.assertIn('test ! -e /var/lib/cloud/instance', script)
        self.assertIn('test ! -L /var/lib/cloud/instance', script)
        self.assertIn('test ! -L /var/lib/cloud/seed', script)
        self.assertIn('TCS_BUILD_STATE_CLEAN', script)
