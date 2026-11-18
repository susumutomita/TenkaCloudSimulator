"""Offline acceptance for process observation scoped to one rehearsal runtime.

Oracle: rehearsal promises guests/supervisors "of this run". Invocation in
internal/workload/guestvm/prepare.go supplies -qmp followed by
unix:<runtime>/<instance>/qmp.sock,server=on,wait=off. Darwin's supervisor argv
is [self, __tenkacloud-guest-supervisor, none|fd3, executable, *args]
(internal/workload/guestprocess/process_darwin.go). This is local ownership
acceptance, not AWS compatibility or actual two-VM cleanup evidence.

Linux synthetic /proc bytes preserve argv boundaries; Darwin synthetic ps bytes
deliberately do not. Ambiguous candidate evidence must fail closed with the
reviewed fixed diagnostic, without publishing command lines. Actual production
definitions execute against fake OS/process-table inputs only. No CLI/import,
real process enumeration, child, network, VM or credential access is performed.
"""
import ast
import os
from pathlib import Path
import re
import types
import unittest


SOURCE = Path(__file__).resolve().parents[1] / 'rehearsal.py'
ROOT = '/tmp/battle-team/rt'
PEER = '/tmp/peer-team/rt'
PREFIX_PEER = '/tmp/battle-team/rt-peer/rt'
CANARY = 'SYNTHETIC_PRIVATE_ARG_912'
AMBIGUOUS = 'process ownership observation ambiguous'
QEMU = '/opt/qemu/bin/qemu-system-x86_64'
SIMULATOR = '/opt/simulator'


def guest_argv(root, executable=QEMU):
    return [executable, '-display', 'none', '-qmp',
            f'unix:{root}/instance/qmp.sock,server=on,wait=off']


def supervisor_argv(root, mode='none', executable=QEMU):
    return [SIMULATOR, '__tenkacloud-guest-supervisor', mode,
            *guest_argv(root, executable)]


class SyntheticInventory:
    def __init__(self, platform, rows):
        self.platform = platform
        self.rows = dict(rows)
        self.reads = []

    def path(self, *parts):
        path = Path(*parts)
        if str(path).startswith('/proc/'):
            expected = path.parts
            if len(expected) != 4 or expected[3] != 'cmdline':
                raise AssertionError('unreviewed proc fixture access')
            pid = int(expected[2])

            def read_bytes():
                self.reads.append(pid)
                return b'\0'.join(a.encode() for a in self.rows[pid]) + b'\0'

            return types.SimpleNamespace(read_bytes=read_bytes)
        raise AssertionError('unreviewed filesystem access')

    def listdir(self, path):
        if path != '/proc' or self.platform != 'linux':
            raise AssertionError('unreviewed process enumeration')
        return [str(pid) for pid in self.rows]

    def ps(self, command, **kwargs):
        if command != ['ps', '-axww', '-o', 'pid=,command='] or self.platform != 'darwin':
            raise AssertionError('unreviewed subprocess boundary')
        self.reads.append('ps')
        return types.SimpleNamespace(stdout=b'\n'.join(
            str(pid).encode() + b' ' + ' '.join(argv).encode()
            for pid, argv in self.rows.items()))


def source_namespace(platform, rows, runtime=ROOT):
    inventory = SyntheticInventory(platform, rows)
    namespace = {
        'RUNTIME_DIR': Path(runtime), 're': re,
        'os': types.SimpleNamespace(path=types.SimpleNamespace(
            isdir=lambda path: path == '/proc' and platform == 'linux',
            basename=os.path.basename, dirname=os.path.dirname,
            normpath=os.path.normpath, commonpath=os.path.commonpath),
            listdir=inventory.listdir),
        'sys': types.SimpleNamespace(platform=platform),
        'pathlib': types.SimpleNamespace(Path=inventory.path, PurePosixPath=Path),
        'subprocess': types.SimpleNamespace(run=inventory.ps),
    }
    nodes = []
    for node in ast.parse(SOURCE.read_text(), filename=str(SOURCE)).body:
        if isinstance(node, ast.FunctionDef):
            nodes.append(node)
        elif isinstance(node, ast.Assign):
            try:
                value = ast.literal_eval(node.value)
            except (ValueError, TypeError):
                continue
            for target in node.targets:
                if isinstance(target, ast.Name) and target.id not in namespace:
                    namespace[target.id] = value
    exec(compile(ast.Module(body=nodes, type_ignores=[]), str(SOURCE), 'exec'), namespace)
    return namespace, inventory


class RehearsalProcessScopeTests(unittest.TestCase):
    def inspect(self, platform, kind, rows, runtime=ROOT):
        namespace, inventory = source_namespace(platform, rows, runtime)
        result = namespace[kind]()
        self.assertTrue(inventory.reads or not rows, 'actual process inventory must run')
        return result

    def assert_ambiguous(self, platform, kind, rows, runtime=ROOT):
        with self.assertRaises(RuntimeError) as raised:
            self.inspect(platform, kind, rows, runtime)
        self.assertIs(type(raised.exception), RuntimeError)
        self.assertEqual(str(raised.exception), AMBIGUOUS)
        self.assertNotIn(CANARY, str(raised.exception))

    def combinations(self):
        for platform in ('linux', 'darwin'):
            for kind, argv in (('qemu_processes', guest_argv),
                               ('supervisor_processes', supervisor_argv)):
                yield platform, kind, argv

    def test_own_live_process_is_counted(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                self.assertEqual(self.inspect(platform, kind, [(101, argv(ROOT))]), [101])

    def test_distinct_peer_is_not_counted(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                self.assertEqual(self.inspect(platform, kind, [(202, argv(PEER))]), [])

    def test_prefix_sibling_peer_is_not_counted(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                self.assertEqual(self.inspect(platform, kind, [(303, argv(PREFIX_PEER))]), [])

    def test_mixed_inventory_retains_only_own_process(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                rows = [(101, argv(ROOT)), (202, argv(PEER)), (303, argv(PREFIX_PEER))]
                self.assertEqual(self.inspect(platform, kind, rows), [101])

    def test_own_exited_peer_live_is_not_own_cleanup_debt(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                before = self.inspect(platform, kind, [(101, argv(ROOT)), (303, argv(PREFIX_PEER))])
                after = self.inspect(platform, kind, [(303, argv(PREFIX_PEER))])
                self.assertEqual(before, [101])
                self.assertEqual(after, [])
                self.assertTrue(not after)

    def test_true_own_leftover_cannot_certify_cleanup(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                leftovers = self.inspect(platform, kind, [(101, argv(ROOT)), (202, argv(PEER))])
                self.assertEqual(leftovers, [101])
                self.assertFalse(not leftovers)

    def test_empty_inventory_has_no_leftovers(self):
        for platform, kind, _ in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                self.assertEqual(self.inspect(platform, kind, []), [])

    def test_unrelated_argument_with_own_path_does_not_claim_peer(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                row = argv(PEER) + ['-name', ROOT + '/' + CANARY]
                self.assertEqual(self.inspect(platform, kind, [(202, row)]), [])

    def test_qemu_and_supervisor_are_separate_process_roles(self):
        for platform in ('linux', 'darwin'):
            with self.subTest(platform=platform):
                rows = [(101, guest_argv(ROOT)), (102, supervisor_argv(ROOT))]
                self.assertEqual(self.inspect(platform, 'qemu_processes', rows), [101])
                self.assertEqual(self.inspect(platform, 'supervisor_processes', rows), [102])

    def test_actual_supervisor_none_and_fd3_modes_are_detected(self):
        for platform in ('linux', 'darwin'):
            for mode in ('none', 'fd3'):
                with self.subTest(platform=platform, mode=mode):
                    self.assertEqual(self.inspect(platform, 'supervisor_processes',
                                                 [(101, supervisor_argv(ROOT, mode))]), [101])

    def test_linux_whitespace_runtime_keeps_argv_boundaries(self):
        root = '/tmp/battle team/rt'
        for kind, argv in (('qemu_processes', guest_argv), ('supervisor_processes', supervisor_argv)):
            with self.subTest(kind=kind):
                rows = [(101, argv(root)), (202, argv(root + '-peer/rt'))]
                self.assertEqual(self.inspect('linux', kind, rows, root), [101])

    def test_linux_whitespace_executable_is_not_silently_missed(self):
        executable = '/opt/qemu tool/bin/qemu-system-x86_64'
        self.assertEqual(self.inspect('linux', 'qemu_processes',
                                     [(101, guest_argv(ROOT, executable))]), [101])

    def test_darwin_whitespace_runtime_is_fixed_ambiguous_failure(self):
        root = '/tmp/battle team/rt'
        for kind, argv in (('qemu_processes', guest_argv), ('supervisor_processes', supervisor_argv)):
            with self.subTest(kind=kind):
                self.assert_ambiguous('darwin', kind, [(101, argv(root) + [CANARY])], root)

    def test_darwin_whitespace_executable_is_fixed_ambiguous_failure(self):
        executable = '/opt/qemu tool/bin/qemu-system-x86_64'
        for kind, argv in (('qemu_processes', guest_argv), ('supervisor_processes', supervisor_argv)):
            with self.subTest(kind=kind):
                self.assert_ambiguous('darwin', kind,
                                      [(101, argv(ROOT, executable=executable) + [CANARY])])

    def test_duplicate_conflicting_qmp_is_fixed_ambiguous_failure(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                row = argv(ROOT) + ['-qmp', guest_argv(PEER)[-1], CANARY]
                self.assert_ambiguous(platform, kind, [(101, row)])

    def test_noop_detector_rejects_always_empty_implementation(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                namespace, _ = source_namespace(platform, [(101, argv(ROOT))])
                namespace[kind] = lambda: []
                with self.assertRaises(AssertionError):
                    self.assertEqual(namespace[kind](), [101])

    def test_unrelated_tcp_qmp_peer_only_is_not_counted(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                peer = argv(PEER)
                peer[-1] = 'tcp:127.0.0.1:4444,server=on,wait=off'
                self.assertEqual(self.inspect(platform, kind, [(202, peer)]), [])

    def test_own_unix_qmp_with_unrelated_tcp_peer_retains_only_own(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                peer = argv(PEER)
                peer[-1] = 'tcp:127.0.0.1:4444,server=on,wait=off'
                rows = [(101, argv(ROOT)), (202, peer)]
                self.assertEqual(self.inspect(platform, kind, rows), [101])

    def test_distinct_unix_peer_with_alternative_qmp_options_is_not_counted(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                peer = argv(PEER)
                peer[-1] = f'unix:{PEER}/instance/qmp.sock,server=on,wait=on'
                self.assertEqual(self.inspect(platform, kind, [(202, peer)]), [])

    def test_own_qmp_with_invalid_owned_options_remains_fixed_failure(self):
        for platform, kind, argv in self.combinations():
            with self.subTest(platform=platform, kind=kind):
                own = argv(ROOT)
                own[-1] = f'unix:{ROOT}/instance/qmp.sock,server=off,wait=off'
                self.assert_ambiguous(platform, kind, [(101, own)])


if __name__ == '__main__':
    unittest.main()
