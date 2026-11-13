import pathlib
import json
import os
import tempfile
import unittest
from unittest import mock
import builder_launch as b


class BuilderLauncher(unittest.TestCase):
    def test_exact_no_network_and_new_disk_only(self):
        args = b.argv('/private/operator/new', '/firmware')
        self.assertEqual(args[args.index('-nic') + 1], 'none')
        self.assertEqual(args[args.index('-machine') + 1], 'virt,accel=hvf')
        self.assertNotIn('-netdev', args)
        self.assertNotIn('-virtfs', args)
        self.assertNotIn('-chardev', args)
        drives = [args[i + 1] for i, v in enumerate(args) if v == '-drive']
        self.assertEqual(len(drives), 3)
        self.assertEqual(drives[0], 'file=/private/operator/new/candidate.qcow2,format=qcow2,if=none,id=os')
        self.assertTrue(all('readonly=on' in d for d in drives[1:]))

    def test_invalid_inputs_never_spawn(self):
        with tempfile.TemporaryDirectory() as root, mock.patch.object(b.platform, 'system', return_value='Darwin'), mock.patch.object(b.platform, 'machine', return_value='arm64'), mock.patch.object(b.subprocess, 'Popen') as popen, mock.patch.object(b.subprocess, 'run') as run:
            with self.assertRaises((ValueError, FileNotFoundError)):
                b.execute(root, root)
            popen.assert_not_called()
            run.assert_not_called()

    def run_fake(self, root, error=None, serial=b'', code=0):
        child = mock.Mock()
        child.returncode = code
        child.stdout.fileno.return_value = 5
        selector = mock.MagicMock()
        selector.__enter__.return_value.select.return_value = [True]
        return child, selector

    def test_spawn_failure_retains_attempt_no_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with mock.patch.object(b.platform, 'system', return_value='Darwin'), mock.patch.object(b.platform, 'machine', return_value='arm64'), mock.patch.object(b, 'validate', return_value=root), mock.patch.object(b, 'verified_firmware', return_value='/firmware'), mock.patch.object(b.subprocess, 'run') as iso, mock.patch.object(b.subprocess, 'Popen', side_effect=OSError('spawn failed')):
                with self.assertRaises(OSError):
                    b.execute(root, root)
                self.assertEqual(iso.call_count, 2)
                self.assertTrue(all(c.kwargs['env'] == {} for c in iso.call_args_list))
            self.assertTrue((root / 'ATTEMPTED').exists())
            self.assertFalse((root / 'build-result.json').exists())

    def test_missing_marker_and_timeout_reap_exact_child(self):
        for timed_out in (False, True):
            with self.subTest(timeout=timed_out), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                child, sel = self.run_fake(root)
                times = [0, 91] if timed_out else [0, 0, 0]
                with mock.patch.object(b.platform, 'system', return_value='Darwin'), mock.patch.object(b.platform, 'machine', return_value='arm64'), mock.patch.object(b, 'validate', return_value=root), mock.patch.object(b, 'verified_firmware', return_value='/firmware'), mock.patch.object(b.subprocess, 'run'), mock.patch.object(b.subprocess, 'Popen', return_value=child), mock.patch.object(b.selectors, 'DefaultSelector', return_value=sel), mock.patch.object(b.time, 'monotonic', side_effect=times), mock.patch.object(b.os, 'read', return_value=b''):
                    with self.assertRaises((ValueError, TimeoutError)):
                        b.execute(root, root)
                child.kill.assert_called_once()
                self.assertFalse((root / 'build-result.json').exists())
                self.assertTrue(any(c.kwargs == {'timeout': 2} for c in child.wait.call_args_list))

    def test_hardlink_rejected_before_hash_or_spawn(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            original = root / 'original'
            original.write_bytes(b'publisher image')
            original.chmod(0o600)
            os.link(original, root / 'candidate.qcow2')
            (root / 'preparation.json').write_text(json.dumps({
                'state': 'prepared-not-built', 'vmExecuted': False,
                'sourceIdentity': {'device': original.stat().st_dev, 'inode': original.stat().st_ino}}))
            with mock.patch.object(b, 'checked') as check:
                with self.assertRaisesRegex(ValueError, 'private independent'):
                    b.validate(root)
                check.assert_not_called()

    def test_quota_and_iso_failure_never_publish_receipt(self):
        for iso_fail in (True, False):
            with self.subTest(iso_fail=iso_fail), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                child, sel = self.run_fake(root)
                with mock.patch.object(b.platform, 'system', return_value='Darwin'), mock.patch.object(b.platform, 'machine', return_value='arm64'), mock.patch.object(b, 'validate', return_value=root), mock.patch.object(b, 'verified_firmware', return_value='/firmware'), mock.patch.object(b.subprocess, 'run', side_effect=OSError('ISO failed') if iso_fail else None), mock.patch.object(b.subprocess, 'Popen', return_value=child) as popen, mock.patch.object(b.selectors, 'DefaultSelector', return_value=sel), mock.patch.object(b.os, 'read', return_value=b'x' * (4 * 1024 ** 2 + 1)):
                    with self.assertRaises((OSError, ValueError)):
                        b.execute(root, root)
                self.assertFalse((root / 'build-result.json').exists())
                if iso_fail:
                    popen.assert_not_called()
                else:
                    child.kill.assert_called_once()

    def test_success_persists_log_before_unqualified_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'candidate.qcow2').write_bytes(b'built fixture')
            child, sel = self.run_fake(root)
            serial = b'TCS_BUILD_FILES_INSTALLED\nTCS_BUILD_STATE_CLEAN\n[1] reboot: Power down\n'
            with mock.patch.object(b.platform, 'system', return_value='Darwin'), mock.patch.object(b.platform, 'machine', return_value='arm64'), mock.patch.object(b, 'validate', return_value=root), mock.patch.object(b, 'verified_firmware', return_value='/firmware'), mock.patch.object(b.subprocess, 'run'), mock.patch.object(b.subprocess, 'Popen', return_value=child), mock.patch.object(b.selectors, 'DefaultSelector', return_value=sel), mock.patch.object(b.os, 'read', side_effect=[serial, b'']):
                result = b.execute(root, root)
            self.assertEqual((root / 'builder-serial.log').read_bytes(), serial)
            self.assertEqual(json.loads((root / 'build-result.json').read_text()), result)
            self.assertEqual(result['state'], 'built-not-qualified')
            self.assertFalse(result['battleReady'])
            self.assertFalse(result['cloudInitCleanupVerified'])
            child.kill.assert_not_called()

    def test_log_failure_does_not_publish_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'candidate.qcow2').write_bytes(b'built fixture')
            child, sel = self.run_fake(root)
            serial = b'TCS_BUILD_FILES_INSTALLED\nTCS_BUILD_STATE_CLEAN\n[1] reboot: Power down\n'
            original_open = pathlib.Path.open
            def fail_log(path, *args, **kwargs):
                if path == root / 'builder-serial.log':
                    raise OSError('log disk full')
                return original_open(path, *args, **kwargs)
            with mock.patch.object(b.platform, 'system', return_value='Darwin'), mock.patch.object(b.platform, 'machine', return_value='arm64'), mock.patch.object(b, 'validate', return_value=root), mock.patch.object(b, 'verified_firmware', return_value='/firmware'), mock.patch.object(b.subprocess, 'run'), mock.patch.object(b.subprocess, 'Popen', return_value=child), mock.patch.object(b.selectors, 'DefaultSelector', return_value=sel), mock.patch.object(b.os, 'read', side_effect=[serial, b'']), mock.patch.object(pathlib.Path, 'open', fail_log):
                with self.assertRaisesRegex(OSError, 'log disk full'):
                    b.execute(root, root)
            self.assertFalse((root / 'build-result.json').exists())
            child.wait.assert_called_with(timeout=2)

    def test_missing_cleanup_or_explicit_failure_rejects(self):
        for extra in (b'', b'TCS_BUILD_STATE_CLEAN\nTCS_BUILD_FAILED\n'):
            with self.subTest(extra=extra), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                child, sel = self.run_fake(root)
                serial = b'TCS_BUILD_FILES_INSTALLED\n' + extra + b'[1] reboot: Power down\n'
                with mock.patch.object(b.platform, 'system', return_value='Darwin'), mock.patch.object(b.platform, 'machine', return_value='arm64'), mock.patch.object(b, 'validate', return_value=root), mock.patch.object(b, 'verified_firmware', return_value='/firmware'), mock.patch.object(b.subprocess, 'run'), mock.patch.object(b.subprocess, 'Popen', return_value=child), mock.patch.object(b.selectors, 'DefaultSelector', return_value=sel), mock.patch.object(b.os, 'read', side_effect=[serial, b'']):
                    with self.assertRaisesRegex(ValueError, 'completion unconfirmed'):
                        b.execute(root, root)
                self.assertFalse((root / 'build-result.json').exists())
                child.kill.assert_called_once()
