"""Offline checks for build_bundle.py helpers; nothing is downloaded, built or booted."""
import copy
import hashlib
import json
import pathlib
import shutil
import sys
import tempfile
import unittest
from types import SimpleNamespace
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
            ('source_sha256', {**receipt['source_sha256'], build_bundle.RELAY_MODULE: '0' * 64}),
            ('source_sha256', {**receipt['source_sha256'], build_bundle.RELAY_SUM: '0' * 64}),
            ('source_sha256', {k: v for k, v in receipt['source_sha256'].items() if k != build_bundle.RELAY_MODULE}),
            ('source_sha256', {k: v for k, v in receipt['source_sha256'].items() if k != build_bundle.RELAY_SUM}),
            ('source_sha256', {**receipt['source_sha256'], 'go.mod': '0' * 64}),
            ('source_sha256', {**receipt['source_sha256'], '../outside': '0' * 64}),
        ]
        for key, value in mutations:
            with self.subTest(field=key, value=value):
                changed = copy.deepcopy(receipt)
                changed[key] = value
                with self.assertRaises(SystemExit):
                    build_bundle.validate_overlay_provenance(build_bundle.REPO, changed, user_data)

    def repo_fixture(self, path, receipt):
        for name in receipt['source_sha256']:
            target = path / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(build_bundle.REPO / name, target)

    def test_host_graph_is_not_a_guest_input_but_either_guest_pin_rejects_actual_content_drift(self):
        receipt = json.loads((pathlib.Path(build_bundle.__file__).with_name('session-overlay-provenance.json')).read_text())
        user_data = pathlib.Path(build_bundle.__file__).with_name('builder-user-data.sh').read_text()
        with tempfile.TemporaryDirectory() as parent:
            repo = pathlib.Path(parent)
            self.repo_fixture(repo, receipt)
            for name in ('go.mod', 'go.sum'):
                (repo / name).write_text('different simulator host graph\n')
            build_bundle.validate_overlay_provenance(repo, receipt, user_data)
            for name in (build_bundle.RELAY_MODULE, build_bundle.RELAY_SUM):
                with self.subTest(path=name):
                    path = repo / name
                    original = path.read_bytes()
                    path.write_bytes(original + b'\n')
                    with self.assertRaisesRegex(SystemExit, f'source drift: {name}'):
                        build_bundle.validate_overlay_provenance(repo, receipt, user_data)
                    path.write_bytes(original)

    def test_overlay_builder_uses_a_private_readonly_guest_graph_and_rejects_a_mutated_staged_sum(self):
        receipt = json.loads((pathlib.Path(build_bundle.__file__).with_name('session-overlay-provenance.json')).read_text())
        binary_data = b'inert binary fixture for command assertions'
        overlay_data = b'inert tar fixture for command assertions'
        receipt['binary_sha256'] = hashlib.sha256(binary_data).hexdigest()
        receipt['overlay_sha256'] = hashlib.sha256(overlay_data).hexdigest()
        user_data = 'OVERLAY_SHA256=' + receipt['overlay_sha256'] + '\n'

        for denied in (False, True):
            with self.subTest(staged_sum_rewritten=denied), tempfile.TemporaryDirectory() as root:
                work = pathlib.Path(root)
                pins = receipt['source_sha256']
                calls = []

                def run(cmd, **opts):
                    calls.append(cmd)
                    if cmd[:2] == ['go', 'build']:
                        self.assertEqual(opts['cwd'], build_bundle.REPO)
                        env = opts['env']
                        self.assertEqual({k: env[k] for k in ('GOFLAGS', 'GOENV', 'GOWORK', 'GOTOOLCHAIN', 'GOPROXY', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOAMD64')},
                                         {'GOFLAGS': '', 'GOENV': 'off', 'GOWORK': 'off', 'GOTOOLCHAIN': 'local', 'GOPROXY': 'off',
                                          'GOOS': 'linux', 'GOARCH': 'amd64', 'CGO_ENABLED': '0', 'GOAMD64': 'v1'})
                        mod = pathlib.Path(cmd[3].removeprefix('-modfile='))
                        self.assertEqual(mod, work / 'relay-graph/session-relay.mod')
                        self.assertEqual(cmd[2], '-mod=readonly')
                        self.assertEqual(cmd[4:6], ['-trimpath', '-buildvcs=false'])
                        for source, staged in ((build_bundle.RELAY_MODULE, mod), (build_bundle.RELAY_SUM, mod.with_suffix('.sum'))):
                            self.assertEqual(build_bundle.sha256(staged), pins[source])
                        if denied:
                            mod.with_suffix('.sum').write_text('unreviewed staged mutation')
                        pathlib.Path(cmd[cmd.index('-o')+1]).write_bytes(binary_data)
                    else:
                        self.assertEqual(cmd[1:3], ['-I', str(build_bundle.REPO / build_bundle.OVERLAY_TOOL)])
                        for key, digest in (('--binary-sha256', receipt['binary_sha256']),
                                            ('--unit-sha256', pins[build_bundle.RELAY_UNIT]),
                                            ('--license-sha256', pins['LICENSE'])):
                            self.assertEqual(cmd[cmd.index(key)+1], digest)
                        pathlib.Path(cmd[cmd.index('--output')+1]).write_bytes(overlay_data)
                    return SimpleNamespace(stdout=json.dumps({'overlaySHA256': receipt['overlay_sha256']}))

                with (patch.object(build_bundle, 'run', side_effect=run),
                      patch.object(build_bundle.subprocess, 'run', return_value=SimpleNamespace(stdout=receipt['toolchain']+'\n'))):
                    if denied:
                        with self.assertRaisesRegex(SystemExit, 'staged graph drift: .*session-relay.sum'):
                            build_bundle.build_session_overlay(build_bundle.REPO, work, receipt, user_data, offline=True)
                    else:
                        built = build_bundle.build_session_overlay(build_bundle.REPO, work, receipt, user_data, offline=True)
                        self.assertEqual(built['overlaySHA256'], receipt['overlay_sha256'])
                self.assertEqual(len(calls), 1 if denied else 2)

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
