import pathlib
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
