"""Offline regressions for immutable packaging and fail-closed release validation."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


build = load('release_build', ROOT / 'scripts/build-release.py')
verify = load('release_verify', ROOT / 'scripts/verify-release.py')
tag_check = load('release_tag', ROOT / 'scripts/verify-release-tag.py')
COMMIT = 'a' * 40
FLAGS = ['-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w']


class ManifestTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.info = {'source_commit': COMMIT, 'version': 'v0.1.0', 'host_protocol': 1,
                     'source_repository': 'https://github.com/susumutomita/TenkaCloudSimulator',
                     'toolchain': 'go version go1.26.10 linux/amd64',
                     'cgo_enabled': False, 'build_flags': FLAGS,
                     'targets': [t.replace('_', '/') for t in verify.TARGETS]}
        for name in verify.ASSETS - {'BUILDINFO.json'}:
            (self.directory / name).write_bytes(b'fixture-binary-' + name.encode())
        self.refresh()

    def refresh(self):
        (self.directory / 'BUILDINFO.json').write_text(json.dumps(self.info))
        (self.directory / 'SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256((self.directory / n).read_bytes()).hexdigest()}  {n}\n'
            for n in sorted(verify.ASSETS)))

    def check(self):
        return verify.verify(self.directory, COMMIT, 'v0.1.0')

    def test_complete_candidate(self):
        self.assertEqual(self.check(), self.info)

    def test_wrong_source_and_version(self):
        with self.assertRaises(ValueError):
            verify.verify(self.directory, 'b' * 40)
        with self.assertRaises(ValueError):
            verify.verify(self.directory, COMMIT, 'v0.2.0')
        with self.assertRaises(ValueError):
            verify.verify(self.directory, 'HEAD')

    def test_corrupted_asset(self):
        (self.directory / sorted(verify.ASSETS)[-1]).write_bytes(b'tampered')
        with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
            self.check()

    def test_extra_missing_and_link_assets(self):
        extra = self.directory / 'extra'
        extra.write_bytes(b'extra')
        with self.assertRaises(ValueError):
            self.check()
        extra.unlink()
        target = self.directory / 'tenkacloud-simulator_linux_amd64'
        data = target.read_bytes()
        target.unlink()
        with self.assertRaises(ValueError):
            self.check()
        outside = self.directory.parent / (self.directory.name + '-link-target')
        outside.write_bytes(data)
        self.addCleanup(outside.unlink)
        target.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, 'regular files'):
            self.check()

    def test_manifest_entry_rejections(self):
        manifest = self.directory / 'SHA256SUMS'
        good = manifest.read_text()
        first = good.splitlines()[0]
        for bad in (good + first + '\n', '\n'.join(good.splitlines()[1:]) + '\n',
                    good.replace('  BUILDINFO.json', '  ../BUILDINFO.json'),
                    good.replace('  BUILDINFO.json', '  unexpected'),
                    good.replace('  BUILDINFO.json', ' BUILDINFO.json'),
                    good + '\n'):
            with self.subTest(manifest=bad):
                manifest.write_text(bad)
                with self.assertRaises(ValueError):
                    self.check()

    def test_provenance_rejections(self):
        for key, value in [('source_commit', 'b' * 40), ('version', 'v0.2.0'),
                           ('toolchain', 'go version go1.25.0 linux/amd64'),
                           ('targets', ['linux/amd64']), ('cgo_enabled', True),
                           ('source_repository', 'https://example.invalid/fork'),
                           ('build_flags', ['-race']), ('host_protocol', 2),
                           ('host_protocol', True), ('host_protocol', '1')]:
            with self.subTest(key=key):
                old = self.info[key]
                self.info[key] = value
                self.refresh()
                with self.assertRaises(ValueError):
                    self.check()
                self.info[key] = old

    def test_missing_host_protocol_refused(self):
        del self.info['host_protocol']
        self.refresh()
        with self.assertRaises(ValueError):
            self.check()


class TagTests(unittest.TestCase):
    def check(self, objects):
        with patch.object(tag_check.subprocess, 'check_output', side_effect=[
                json.dumps({'object': obj}) for obj in objects]) as api:
            tag_check.verify_tag('owner/repo', 'v0.1.0', COMMIT)
            return api.call_args_list

    def test_lightweight_and_annotated_tags(self):
        self.assertEqual(len(self.check([{'sha': COMMIT, 'type': 'commit'}])), 1)
        calls = self.check([{'sha': 'b' * 40, 'type': 'tag'}, {'sha': COMMIT, 'type': 'commit'}])
        self.assertEqual(calls[1].args[0], ['gh', 'api', 'repos/owner/repo/git/tags/' + 'b' * 40])

    def test_moved_malformed_or_nested_tags_fail_closed(self):
        for objects in ([{'sha': 'b' * 40, 'type': 'commit'}],
                        [{'sha': COMMIT, 'type': 'tree'}],
                        [{'sha': '--unexpected', 'type': 'tag'}],
                        [{'sha': 'b' * 40, 'type': 'tag'}] * 16):
            with self.subTest(objects=objects), self.assertRaises(ValueError):
                self.check(objects)


class WorkflowTests(unittest.TestCase):
    def test_release_publication_retains_all_gates_and_draft_first(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('needs: [validate, formal, build, smoke]', workflow)
        self.assertIn('uses: ./.github/workflows/ci.yml', workflow)
        self.assertIn('uses: ./.github/workflows/formal.yml', workflow)
        self.assertIn("if: github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')", workflow)
        self.assertIn('  workflow_call:', (ROOT / '.github/workflows/formal.yml').read_text())
        self.assertIn('--draft ', workflow)
        self.assertEqual(workflow.count('scripts/verify-release-tag.py --repository'), 2)
        self.assertLess(workflow.index('--draft '), workflow.index('--draft=false'))
        for target in verify.TARGETS:
            self.assertIn('target: ' + target, workflow)


class BuildTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        (self.root / 'scripts').mkdir()
        (self.root / 'scripts/check-assets.py').write_text("from pathlib import Path\nassert Path('source.txt').read_text() == 'committed'\n")
        (self.root / 'source.txt').write_text('committed')
        self.git('init', '-q')
        self.git('add', '.')
        self.git('-c', 'user.name=Release Test', '-c', 'user.email=release@example.invalid',
                 'commit', '-qm', 'fixture')
        self.commit = self.git('rev-parse', 'HEAD')
        (self.root / 'source.txt').write_text('uncommitted')
        self.go = self.root / 'fake-go'
        self.go.write_text('''#!''' + sys.executable + '''
import json, os, pathlib, sys
args = sys.argv[1:]
if args == ['version']:
    print('go version go1.26.10 linux/amd64')
elif args == ['mod', 'verify']:
    assert pathlib.Path('source.txt').read_text() == 'committed'
else:
    assert args == ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', '-o', args[6], './cmd/simulator'], args
    assert os.environ['CGO_ENABLED'] == '0'
    assert os.environ.get('GOFLAGS', '') == '', 'inherited GOFLAGS'
    assert os.environ.get('GOEXPERIMENT', '') == '', 'inherited GOEXPERIMENT'
    assert os.environ['GOWORK'] == 'off'
    assert os.environ['GOENV'] == 'off'
    assert os.environ['GOTOOLCHAIN'] == 'local'
    pathlib.Path(args[6]).write_text(pathlib.Path('source.txt').read_text() + os.environ['GOOS'] + os.environ['GOARCH'])
''')
        self.go.chmod(0o700)
        self.output = self.root / 'output'

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root, text=True).strip()

    def invoke(self, ref=None, version='v0.1.0', env=None):
        with patch.object(build, '__file__', str(self.root / 'scripts/build-release.py')), \
                patch.dict(os.environ, {'GO': str(self.go), **(env or {})}), \
                patch.object(sys, 'argv', ['build-release.py', '--ref=' + (ref or self.commit),
                                         '--version', version, '--output', str(self.output)]), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            build.main()

    def test_immutable_source_and_complete_manifest(self):
        self.invoke()
        info = verify.verify(self.output, self.commit, 'v0.1.0')
        self.assertEqual(info['build_flags'], FLAGS)
        self.assertEqual(info['host_protocol'], 1)
        for target in verify.TARGETS:
            self.assertEqual((self.output / ('tenkacloud-simulator_' + target)).read_text(),
                             'committed' + target.replace('_', ''))
        self.assertEqual((self.root / 'source.txt').read_text(), 'uncommitted')

    def test_ambient_build_flags_do_not_change_release(self):
        self.invoke(env={'GOFLAGS': '-tags=ambient', 'GOEXPERIMENT': 'ambient'})
        verify.verify(self.output, self.commit, 'v0.1.0')

    def test_nonempty_output_is_preserved(self):
        self.output.mkdir()
        existing = self.output / 'existing'
        existing.write_text('keep')
        with self.assertRaises(SystemExit):
            self.invoke()
        self.assertEqual(existing.read_text(), 'keep')

    def test_invalid_version_or_ref_refused(self):
        for version in ('1.0.0', 'v1.0', 'v1.0.0/evil', 'v1.0.0\n'):
            with self.subTest(version=version), self.assertRaises(SystemExit):
                self.invoke(version=version)
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(ref='--help')

    def test_failed_build_has_no_complete_manifest(self):
        self.go.write_text(self.go.read_text().replace("else:\n    assert args", "else:\n    raise SystemExit(1)\n    assert args"))
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke()
        self.assertFalse((self.output / 'SHA256SUMS').exists())
        self.assertFalse((self.output / 'BUILDINFO.json').exists())

    def test_wrong_toolchain_refused(self):
        self.go.write_text(self.go.read_text().replace('go1.26.10', 'go1.25.0'))
        with self.assertRaises(SystemExit):
            self.invoke()
        self.assertEqual(list(self.output.iterdir()), [])


if __name__ == '__main__':
    unittest.main()
