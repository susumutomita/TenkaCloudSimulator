"""Offline refusal and CI-entry-point controls; no namespaces or browser are launched."""
import argparse
import ast
import contextlib
import hashlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
spec = importlib.util.spec_from_file_location('s3_paging_launcher', HERE / 'run_s3_multipart_paging.py')
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


def valid_context():
    return dict(uid=1001, euid=1001, net=102, user=202,
                status=''.join(f'{key}:\t0000000000000000\n' for key in launcher.CAPABILITIES),
                interfaces=['lo'], lo_up=True)


def entrypoints(workflow, aggregate, fixture):
    console = workflow.split('  console-acceptance:\n', 1)[1].split('  checks:\n', 1)[0]
    offline = '/tmp/simulator-tests/bin/python test/e2e/test_s3_multipart_paging_launcher.py'
    assert console.count(offline) == 1
    assert 'test/e2e/run_s3_multipart_paging.py' not in console
    assert 'test/e2e/s3_multipart_paging.py' not in console
    assert 'reports/s3-multipart-paging.json' not in console
    assert 'continue-on-error:' not in console and 'sudo' not in console and 'sysctl' not in console
    step = console.split('      - name: Check S3 multipart launcher refusals offline\n', 1)[1].split('      - name:', 1)[0]
    assert 'if:' not in step and 'run: ' + offline in step
    assert 'needs: [console-acceptance, checks, go-test, platform-audit]' in workflow
    tree = ast.parse(aggregate)
    conformance = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == 'Conformance')
    method = next(node for node in conformance.body if isinstance(node, ast.FunctionDef) and
                  node.name == 'test_s3_multipart_paging_authenticated_browser')
    assert not method.decorator_list
    constants = [node.value for node in ast.walk(method) if isinstance(node, ast.Constant)]
    assert {'test_s3_multipart_paging_launcher.py', 'run_s3_multipart_paging.py', '--binary'} <= set(constants)
    calls = [node for node in ast.walk(method) if isinstance(node, ast.Call) and
             isinstance(node.func, ast.Attribute) and node.func.attr == 'run' and
             any(isinstance(child, ast.Constant) and child.value == 'run_s3_multipart_paging.py'
                 for child in ast.walk(node))]
    assert len(calls) == 1
    assert any(node.arg == 'check' and isinstance(node.value, ast.Constant) and node.value.value is True
               for node in calls[0].keywords)
    tree = ast.parse(fixture)
    run = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == 'run')
    assert isinstance(run.body[0], ast.Expr) and isinstance(run.body[0].value, ast.Call)
    assert isinstance(run.body[0].value.func, ast.Name) and run.body[0].value.func.id == 'run_preflight'
    assert any(isinstance(node, ast.keyword) and node.arg == 'chromium_sandbox' and
               isinstance(node.value, ast.Constant) and node.value.value is True for node in ast.walk(tree))
    assert '--no-sandbox' not in fixture


class LauncherTests(unittest.TestCase):
    def test_context_accepts_only_changed_namespaces_nonroot_uid_zero_caps_and_live_loopback(self):
        launcher.validate_inside_context(1001, 101, 201, **valid_context())
        mutations = {
            'root': {'uid': 0, 'euid': 0},
            'changed uid': {'uid': 1002, 'euid': 1002},
            'effective root': {'euid': 0},
            'host network': {'net': 101},
            'host user namespace': {'user': 201},
            'extra interface': {'interfaces': ['lo', 'eth0']},
            'missing loopback': {'interfaces': []},
            'loopback down': {'lo_up': False},
            'missing capabilities': {'status': 'CapEff:\t0\n'},
            'duplicate capability': {'status': valid_context()['status'] + 'CapEff:\t0\n'},
        }
        for cap in launcher.CAPABILITIES:
            mutations[cap] = {'status': valid_context()['status'].replace(cap + ':\t0000000000000000', cap + ':\t1')}
        for name, changes in mutations.items():
            with self.subTest(name=name), self.assertRaises(RuntimeError):
                launcher.validate_inside_context(1001, 101, 201, **(valid_context() | changes))

    def test_namespace_only_parameters_and_unknown_arguments_cannot_select_a_command(self):
        for args in (['--binary-sha256', 'a' * 64], ['--inside'], ['--inside-extra'],
                     ['--fixture', '/tmp/another.py'], ['--skip-isolation']):
            with self.subTest(args=args), patch.object(launcher.os, 'execv') as execute, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises((RuntimeError, SystemExit)):
                    launcher.main(args)
                execute.assert_not_called()

    def test_root_refusal_happens_before_tools_or_namespaces(self):
        with patch.object(launcher.os, 'getuid', return_value=0), patch.object(launcher.os, 'geteuid', return_value=0), \
                patch.object(launcher, 'pinned_chromium') as chromium, patch.object(launcher.os, 'execv') as execute:
            with self.assertRaisesRegex(RuntimeError, 'non-root'):
                launcher.main([])
            chromium.assert_not_called()
            execute.assert_not_called()

    def test_missing_host_tools_cannot_fall_back_to_networked_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / 'simulator'
            binary.write_bytes(b'fixture')
            binary.chmod(0o700)
            with patch.object(launcher.os, 'getuid', return_value=1001), \
                    patch.object(launcher.os, 'geteuid', return_value=1001), \
                    patch.object(launcher, 'pinned_chromium', return_value=Path('/tmp/pinned')), \
                    patch.object(launcher.shutil, 'which', return_value=None), patch.object(launcher.os, 'execv') as execute:
                with self.assertRaisesRegex(RuntimeError, 'tools are required'):
                    launcher.main(['--binary', str(binary)])
                execute.assert_not_called()

    def test_fixed_argv_keeps_metacharacters_out_of_shell_text(self):
        args = argparse.Namespace(binary='/tmp/path with spaces/$(no); simulator')
        tools = {name: '/usr/bin/' + name for name in ('unshare', 'sh', 'ip', 'setpriv')}
        with patch.object(launcher.os, 'getuid', return_value=1001), \
                patch.object(launcher, 'namespace_inode', side_effect=[101, 201]):
            command = launcher.command(args, tools, Path('/tmp/pinned browser'), 'a' * 64)
        self.assertEqual(command[:5], [tools['unshare'], '--user', '--map-current-user', '--net', '--keep-caps'])
        self.assertEqual(command[8], launcher.SHELL)
        self.assertNotIn(args.binary, launcher.SHELL)
        self.assertIn(args.binary, command)
        self.assertNotIn('--map-root-user', command)
        self.assertEqual(command[command.index(tools['setpriv']) + 1:command.index(tools['setpriv']) + 4],
                         ['--inh-caps=-all', '--ambient-caps=-all', '--bounding-set=-all'])
        self.assertEqual(command[command.index('--parent-net') + 1], '101')
        self.assertEqual(command[command.index('--parent-user') + 1], '201')

    def test_inside_revalidates_binary_and_browser_before_fixed_fixture_exec(self):
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / 'simulator'
            binary.write_bytes(b'reviewed-fixture-only')
            binary.chmod(0o700)
            digest = hashlib.sha256(binary.read_bytes()).hexdigest()
            args = argparse.Namespace(uid=1001, parent_net=101, parent_user=201, binary=str(binary),
                                      binary_sha256=digest, chromium='/tmp/pinned-chromium')
            with patch.object(launcher.os, 'getuid', return_value=1001), \
                    patch.object(launcher.os, 'geteuid', return_value=1001), \
                    patch.object(launcher, 'namespace_inode', side_effect={'net': 102, 'user': 202}.__getitem__), \
                    patch.object(launcher.Path, 'read_text', return_value=valid_context()['status']), \
                    patch.object(launcher.socket, 'if_nameindex', return_value=[(1, 'lo')]), \
                    patch.object(launcher, 'loopback_up', return_value=True), \
                    patch.object(launcher, 'pinned_chromium', return_value=Path(args.chromium)), \
                    patch.object(launcher.os, 'execv') as execute, patch.dict(launcher.os.environ):
                launcher.inside(args)
                execute.assert_called_once_with(launcher.sys.executable,
                                                [launcher.sys.executable, str(HERE / 's3_multipart_paging.py')])
                self.assertEqual(launcher.os.environ['SIMULATOR_BINARY_SHA256'], digest)
                self.assertEqual(launcher.os.environ['CHROMIUM_EXECUTABLE'], args.chromium)
                binary.write_bytes(b'changed')
                execute.reset_mock()
                with self.assertRaisesRegex(RuntimeError, 'changed during isolation'):
                    launcher.inside(args)
                execute.assert_not_called()
                binary.write_bytes(b'reviewed-fixture-only')
                with patch.object(launcher, 'pinned_chromium', return_value=Path('/tmp/unpinned')):
                    with self.assertRaisesRegex(RuntimeError, 'Chromium changed'):
                        launcher.inside(args)
                execute.assert_not_called()

    def test_console_ci_offline_and_opt_in_aggregate_keep_fixture_guards(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        aggregate = (HERE / 'check.py').read_text()
        fixture = (HERE / 's3_multipart_paging.py').read_text()
        entrypoints(workflow, aggregate, fixture)
        changes = [
            (workflow.replace('python test/e2e/test_s3_multipart_paging_launcher.py',
                              'python test/e2e/another.py'), aggregate, fixture),
            (workflow.replace('      - name: Check native discovery',
                              '      - run: python test/e2e/run_s3_multipart_paging.py\n      - name: Check native discovery'),
             aggregate, fixture),
            (workflow.replace('      - name: Check native discovery',
                              '      - run: python test/e2e/s3_multipart_paging.py\n      - name: Check native discovery'),
             aggregate, fixture),
            (workflow.replace('needs: [console-acceptance, checks, go-test, platform-audit]', 'needs: [checks]'), aggregate, fixture),
            (workflow.replace('      - name: Check S3 multipart launcher refusals offline\n',
                              '      - name: Check S3 multipart launcher refusals offline\n        if: false\n'), aggregate, fixture),
            (workflow, aggregate.replace("'run_s3_multipart_paging.py'", "'another.py'"), fixture),
            (workflow, aggregate.replace(' def test_s3_multipart_paging_authenticated_browser(self):',
                                         ' @unittest.skip("fixture")\n def test_s3_multipart_paging_authenticated_browser(self):'), fixture),
            (workflow, aggregate.replace('check=True,timeout=300,stdout=report', 'check=False,timeout=300,stdout=report'), fixture),
            (workflow, aggregate, fixture.replace('    run_preflight()\n', '    pass\n', 1)),
            (workflow, aggregate, fixture.replace('chromium_sandbox=True', 'chromium_sandbox=False')),
        ]
        for index, inputs in enumerate(changes):
            with self.subTest(index=index), self.assertRaises((AssertionError, StopIteration)):
                entrypoints(*inputs)


if __name__ == '__main__':
    unittest.main()
