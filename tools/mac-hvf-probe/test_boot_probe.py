import hashlib
import pathlib
import tempfile
import unittest
from unittest.mock import patch, Mock, MagicMock
from contextlib import ExitStack
import io
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


class BootExecutionContract(unittest.TestCase):
    def run_case(self, chunks, ticks=None, code=0, expected_error=None, inspect_identity=False):
        child = Mock()
        child.stdin = io.BytesIO()
        child.stdout = Mock()
        child.stderr = None
        child.returncode = code
        selector = MagicMock()
        selector.__enter__.return_value = selector
        selector.select.return_value = [object()]
        output, errors = Mock(), Mock()
        output.buffer, errors.buffer = io.BytesIO(), io.BytesIO()
        with ExitStack() as stack:
            stack.enter_context(patch.object(boot_probe.platform, 'system', return_value='Darwin'))
            stack.enter_context(patch.object(boot_probe.platform, 'machine', return_value='arm64'))
            stack.enter_context(patch.object(boot_probe, 'verified_files', return_value=['/fixed/kernel', '/fixed/initrd']))
            popen = stack.enter_context(patch.object(boot_probe.subprocess, 'Popen', return_value=child))
            stack.enter_context(patch.object(boot_probe.selectors, 'DefaultSelector', return_value=selector))
            stack.enter_context(patch.object(boot_probe.os, 'read', side_effect=chunks))
            stack.enter_context(patch.object(boot_probe.time, 'monotonic', side_effect=ticks, return_value=0))
            stack.enter_context(patch.object(boot_probe.sys, 'stdout', output))
            stack.enter_context(patch.object(boot_probe.sys, 'stderr', errors))
            if expected_error:
                with self.assertRaises(expected_error):
                    boot_probe.main('/fixed', inspect_identity=inspect_identity)
                child.kill.assert_called_once_with()
                self.assertEqual(output.write.call_count, 0, 'failure printed success')
            else:
                boot_probe.main('/fixed', inspect_identity=inspect_identity)
                child.kill.assert_not_called()
            self.assertTrue(child.stdin.closed)
            child.stdout.close.assert_called()
            child.wait.assert_called_with(timeout=2)
            args = popen.call_args.args[0]
            self.assertEqual(popen.call_args.kwargs['env'], {})
            self.assertEqual(args[args.index('-machine') + 1], 'virt,accel=hvf')
            self.assertEqual(args[args.index('-nic') + 1], 'none')
            self.assertEqual(args[args.index('-serial') + 1], 'stdio')
            self.assertEqual(args[args.index('-kernel') + 1], '/fixed/kernel')
            self.assertEqual(args[args.index('-initrd') + 1], '/fixed/initrd')
            for forbidden in ['-drive', '-netdev', '-fsdev'] + ([] if inspect_identity else ['-device']):
                self.assertNotIn(forbidden, args)
            if inspect_identity:
                self.assertEqual(args[args.index('-uuid') + 1], 'b42718a6-5b28-4f82-8d9c-cd91dfe925c1')
                self.assertIn('null,id=session-probe', args)
                self.assertIn('virtserialport,chardev=session-probe,name=org.tenkacloud.session.0', args)
            return child

    def test_success_uses_fixed_safe_argv_and_environment(self):
        self.run_case([b'~ # ', b'TCS_ARCH_aarch64\nPower down\n', b''])

    def test_deadline_stops_owned_child_without_success(self):
        self.run_case([], ticks=[0, 31], expected_error=TimeoutError)

    def test_serial_budget_stops_owned_child_without_success(self):
        self.run_case([b'x' * 8192] * 129, expected_error=ValueError)

    def test_missing_guest_marker_or_failed_exit_never_passes(self):
        for chunks, code in [([b'Power down', b''], 0),
                             ([b'TCS_ARCH_aarch64', b''], 0),
                             ([b'TCS_ARCH_aarch64 Power down', b''], 1)]:
            with self.subTest(code=code, chunks=chunks):
                self.run_case(chunks, code=code, expected_error=ValueError)

    def test_identity_probe_uses_only_fixed_null_channel(self):
        self.run_case([b'~ # ', b'org.tenkacloud.session.0\nTCS_ARCH_aarch64\nPower down\n', b''], inspect_identity=True)

    def test_wrong_platform_never_launches(self):
        with patch.object(boot_probe.platform, 'system', return_value='Linux'), \
             patch.object(boot_probe.subprocess, 'Popen') as popen:
            with self.assertRaises(SystemExit):
                boot_probe.main('/not/read')
            popen.assert_not_called()


if __name__ == '__main__':
    unittest.main()
