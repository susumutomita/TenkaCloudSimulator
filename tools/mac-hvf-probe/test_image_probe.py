import hashlib
import pathlib
import tempfile
import unittest
from unittest import mock
import image_probe


class ImageProbeContract(unittest.TestCase):
    def test_all_markers_required_as_complete_lines(self):
        lines = [b'TCS_PID1_systemd', ('TCS_UUID_' + image_probe.UUID).encode(),
                 b'TCS_BASH_PRESENT', b'TCS_NAMED_CHAR_PRESENT', b'TCS_PREREQ_DONE']
        self.assertTrue(image_probe.observations(b'\r\n'.join(lines)))
        for index in range(len(lines)):
            self.assertFalse(image_probe.observations(b'\n'.join(lines[:index] + lines[index + 1:])))
        self.assertFalse(image_probe.observations(b'echo ' + b';echo '.join(lines)))

    def test_hash_and_size_enforced(self):
        with tempfile.TemporaryDirectory() as root:
            path = pathlib.Path(root) / 'image'
            path.write_bytes(b'fixture')
            digest = hashlib.sha512(b'fixture').hexdigest()
            self.assertEqual(image_probe.checked(path, 'sha512', digest, 7), str(path.resolve()))
            for expected, limit in [(digest, 6), ('0' * 128, 7)]:
                with self.assertRaises(ValueError):
                    image_probe.checked(path, 'sha512', expected, limit)

    def test_symlink_and_option_delimiter_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            target = pathlib.Path(root) / 'target'
            target.write_bytes(b'fixture')
            link = pathlib.Path(root) / 'link'
            link.symlink_to(target)
            for path in [link, pathlib.Path(root) / 'image,other']:
                with self.assertRaises(ValueError):
                    image_probe.checked(path, 'sha512', '', 7)

    def test_offline_launch_and_spawn_failure_close_channels(self):
        host, guest = mock.Mock(), mock.Mock()
        guest.fileno.return_value = 21
        with tempfile.TemporaryDirectory() as root, \
             mock.patch.object(image_probe.platform, 'system', return_value='Darwin'), \
             mock.patch.object(image_probe.platform, 'machine', return_value='arm64'), \
             mock.patch.object(image_probe, 'checked', side_effect=['/disk', '/seed']), \
             mock.patch.object(image_probe, 'verified_firmware', return_value='/firmware'), \
             mock.patch.object(image_probe.socket, 'socketpair', return_value=(host, guest)), \
             mock.patch.object(image_probe.subprocess, 'Popen', side_effect=OSError('test spawn failure')) as spawn:
            with self.assertRaises(OSError):
                image_probe.main(root, root)
        args = spawn.call_args.args[0]
        self.assertEqual(args[args.index('-smbios') + 1],
                         'type=11,value=io.systemd.credential:systemd.extra-unit.systemd-networkd-wait-online.service=')
        self.assertEqual(args[args.index('-nic') + 1], 'none')
        self.assertEqual(args[args.index('-machine') + 1], 'virt,accel=hvf')
        self.assertNotIn('-netdev', args)
        self.assertEqual(spawn.call_args.kwargs['env'], {})
        self.assertEqual(spawn.call_args.kwargs['pass_fds'], (21,))
        self.assertIn('file=/disk,format=qcow2,if=none,id=os,snapshot=on', args)
        self.assertIn('file=/seed,format=raw,if=none,id=seed,readonly=on', args)
        host.close.assert_called_once()
        guest.close.assert_called_once()
