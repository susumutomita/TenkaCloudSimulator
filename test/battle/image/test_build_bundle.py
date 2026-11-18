"""Offline checks for build_bundle.py helpers; nothing is downloaded, built or booted."""
import copy
import json
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import build_bundle  # noqa: E402


class BuildBundleHelperTests(unittest.TestCase):
    def test_overlay_provenance_matches_current_inputs(self):
        receipt = json.loads((pathlib.Path(build_bundle.__file__).with_name('session-overlay-provenance.json')).read_text())
        user_data = pathlib.Path(build_bundle.__file__).with_name('builder-user-data.sh').read_text()
        build_bundle.validate_overlay_provenance(build_bundle.REPO, receipt, user_data)

    def test_overlay_provenance_rejects_stale_or_forged_inputs(self):
        receipt = json.loads((pathlib.Path(build_bundle.__file__).with_name('session-overlay-provenance.json')).read_text())
        user_data = pathlib.Path(build_bundle.__file__).with_name('builder-user-data.sh').read_text()
        mutations = [
            ('version', 2), ('toolchain', 'go1.26.2'),
            ('target', {'GOOS': 'linux', 'GOARCH': 'arm64', 'CGO_ENABLED': '0', 'GOAMD64': 'v1'}),
            ('flags', ['-trimpath', '-buildvcs=false', '-ldflags=-buildid=']),
            ('overlay_sha256', '0' * 64), ('binary_sha256', 'not-a-digest'),
            ('source_sha256', {**receipt['source_sha256'], 'go.mod': '0' * 64}),
            ('source_sha256', {k: v for k, v in receipt['source_sha256'].items() if k != 'go.mod'}),
            ('source_sha256', {**receipt['source_sha256'], '../outside': '0' * 64}),
        ]
        for key, value in mutations:
            with self.subTest(field=key, value=value):
                changed = copy.deepcopy(receipt)
                changed[key] = value
                with self.assertRaises(SystemExit):
                    build_bundle.validate_overlay_provenance(build_bundle.REPO, changed, user_data)
        # A Go directive change is enough to change the relay build ID even when
        # its actual package/dependency source stays the same.
        with patch.object(build_bundle, 'sha256', side_effect=lambda p: '0' * 64 if p.name == 'go.mod' else receipt['source_sha256'][str(p.relative_to(build_bundle.REPO))]):
            with self.assertRaisesRegex(SystemExit, 'source drift: go.mod'):
                build_bundle.validate_overlay_provenance(build_bundle.REPO, receipt, user_data)

    def test_overlay_pin_is_read_from_the_committed_user_data(self):
        user_data = (pathlib.Path(build_bundle.__file__).with_name('builder-user-data.sh')).read_text()
        self.assertRegex(build_bundle.pinned_overlay_digest(user_data), r'^[0-9a-f]{64}$')

    def test_overlay_pin_must_be_unique(self):
        for text in ('', 'OVERLAY_SHA256=' + 'a' * 64 + '\nOVERLAY_SHA256=' + 'b' * 64 + '\n'):
            with self.assertRaises(SystemExit):
                build_bundle.pinned_overlay_digest(text)

    def test_data_directories_keep_qemu_search_order(self):
        listed = build_bundle.data_directories('/opt/homebrew/Cellar/qemu/10.1.2/share/qemu\n/opt/homebrew/share/qemu\n\n')
        self.assertEqual(listed, [pathlib.Path('/opt/homebrew/Cellar/qemu/10.1.2/share/qemu'), pathlib.Path('/opt/homebrew/share/qemu')])

    def test_module_directory_is_relative_to_the_emulator_prefix(self):
        with tempfile.TemporaryDirectory() as root:
            prefix = pathlib.Path(root)
            (prefix / 'bin').mkdir()
            emulator = prefix / 'bin/qemu-system-x86_64'
            emulator.write_bytes(b'')
            self.assertIsNone(build_bundle.module_directory(emulator))
            (prefix / 'lib/x86_64-linux-gnu/qemu').mkdir(parents=True)
            (prefix / 'lib/x86_64-linux-gnu/qemu/accel-tcg-x86_64.so').write_bytes(b'')
            self.assertEqual(build_bundle.module_directory(emulator), pathlib.Path('lib/x86_64-linux-gnu/qemu'))

    def test_seed_uses_the_cidata_label_on_each_platform(self):
        seed = pathlib.Path('/work/seed')
        with patch.object(build_bundle.sys, 'platform', 'darwin'):
            command = build_bundle.seed_command(seed, pathlib.Path('/work/seed.iso'))
            self.assertEqual(command[:2], ['hdiutil', 'makehybrid'])
            self.assertIn('cidata', command)
        with patch.object(build_bundle.sys, 'platform', 'linux'), patch.object(build_bundle.shutil, 'which', return_value='/usr/bin/genisoimage'):
            command = build_bundle.seed_command(seed, pathlib.Path('/work/seed.iso'))
            self.assertEqual(command[0], '/usr/bin/genisoimage')
            self.assertEqual(command[command.index('-volid') + 1], 'cidata')

    def test_builder_boot_reaches_only_the_loopback_relay(self):
        command = build_bundle.builder_command(pathlib.Path('/q'), pathlib.Path('/d.qcow2'), pathlib.Path('/s.iso'), pathlib.Path('/serial.log'), 2)
        self.assertIn('user,id=n0', command)
        self.assertNotIn('hostfwd', ' '.join(command))
        self.assertIn('-no-reboot', command)

    def test_a_named_work_directory_must_be_new_and_is_never_reused(self):
        with tempfile.TemporaryDirectory() as parent:
            existing = pathlib.Path(parent, 'kept')
            existing.mkdir()
            (existing / 'unrelated').write_text('keep me')
            with self.assertRaises(SystemExit):
                build_bundle.scratch_directory(str(existing))
            self.assertEqual((existing / 'unrelated').read_text(), 'keep me')
            fresh = build_bundle.scratch_directory(str(pathlib.Path(parent, 'new', 'work')))
            self.assertTrue(fresh.is_dir())
            self.assertEqual(list(fresh.iterdir()), [])

    def test_qcow2_header_length_reads_version_and_length(self):
        with tempfile.NamedTemporaryFile() as disk:
            header = bytearray(104)
            header[:4] = b'QFI\xfb'
            header[4:8] = (3).to_bytes(4, 'big')
            header[100:104] = (104).to_bytes(4, 'big')
            disk.write(header)
            disk.flush()
            self.assertEqual(build_bundle.qcow2_header_length(disk.name), (3, 104))
        with tempfile.NamedTemporaryFile() as other:
            other.write(b'\0' * 104)
            other.flush()
            with self.assertRaises(SystemExit):
                build_bundle.qcow2_header_length(other.name)


if __name__ == '__main__':
    unittest.main()
