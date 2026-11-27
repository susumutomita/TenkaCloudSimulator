"""Offline public-output acceptance, independently fixed before the repair.

Oracle: rehearsal's existing promise that secrets never enter its report, plus
the reviewed requirement that public diagnostics are an allowlist, not raw text
with secret-pattern substitutions. battle-hosts.yml uploads report.json and
simulator.log; stdout is the job log. Synthetic values below are NOT credentials.
Only definitions and the explicitly selected local output/process boundaries
execute: never the script's imports, CLI, networking, guest or startup loop.
"""
import ast
import contextlib
import hashlib
import io
import json
import math
import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import types
import unittest
from unittest.mock import Mock, call


SOURCE = Path(__file__).resolve().parents[1] / 'rehearsal.py'
CANARIES = ('SYNTHETIC_XTRACE_SECRET_718', 'SYNTHETIC_BEARER_SECRET_718',
            'SYNTHETIC_SESSION_SECRET_718', 'SYNTHETIC_AWS_SECRET_718',
            'UNRECOGNIZED_PRIVATE_TEXT_718')
RAW = ('+ TOKEN=' + CANARIES[0] + '\nAuthorization: Bearer ' + CANARIES[1]
       + '\nX-Amz-Security-Token: ' + CANARIES[2]
       + '\nAWS_SECRET_ACCESS_KEY=' + CANARIES[3] + '\n' + CANARIES[4])
DIGEST = 'ab' * 32


class ClientError(Exception):
    def __init__(self, code, status):
        super().__init__(RAW)
        self.response = {'Error': {'Code': code, 'Message': RAW},
                         'ResponseMetadata': {'HTTPStatusCode': status, 'RequestId': RAW}}


SDK_TRANSPORT_TYPES = {name: type(name, (Exception,), {}) for name in (
    'ConnectionClosedError', 'ConnectTimeoutError', 'ReadTimeoutError',
    'EndpointConnectionError', 'HTTPClientError', 'ResponseParserError')}


def source_tree():
    return ast.parse(SOURCE.read_text(), filename=str(SOURCE))


def compile_nodes(nodes):
    return compile(ast.fix_missing_locations(ast.Module(body=nodes, type_ignores=[])),
                   str(SOURCE), 'exec')


class InertProcess:
    pid = 123
    returncode = 0

    def poll(self):
        return self.returncode

    def wait(self, **kwargs):
        return self.returncode

    def send_signal(self, value):
        raise AssertionError('already-exited fixture must not receive signals')


class PublicHarness:
    def __init__(self, directory):
        self.work = Path(directory)
        self.console = io.StringIO()
        self.process_streams = []
        self.report = {'phases': [], 'score': {'total': 0, 'rounds': []},
                       'templateSha256': DIGEST, 'result': 'PASS'}
        self.ns = {
            'json': json, 're': re, 'math': math, 'hashlib': hashlib,
            'ClientError': ClientError, **SDK_TRANSPORT_TYPES,
            'contextmanager': contextlib.contextmanager,
            'os': os, 'pathlib': types.SimpleNamespace(Path=Path),
            'time': types.SimpleNamespace(time=lambda: 10.0, sleep=lambda _: None,
                                          strftime=lambda *a: '2026-11-18T00:00:00Z',
                                          gmtime=lambda: None),
            'T0': 0, 'WORK': self.work, 'REPORT': self.work / 'report.json',
            'report': self.report, 'signal': signal, 'exit_code': 1,
            'args': types.SimpleNamespace(stop_after='', binary='/unused'),
            'PORT': 12345, 'state_path': self.work / 'state.db',
            'token_path': self.work / 'operator-token',
            'RUNTIME_CONFIG': self.work / 'runtime.json',
            'subprocess': types.SimpleNamespace(Popen=self.popen, STDOUT=subprocess.STDOUT,
                                               DEVNULL=subprocess.DEVNULL,
                                               TimeoutExpired=subprocess.TimeoutExpired),
            'SCORING': {'endpoints': [{'slot': 'frontend', 'path': '/', 'expectStatus': [200]},
                                      {'slot': 'api', 'path': '/', 'expectStatus': [200]}],
                        'pointsPerSuccess': 10, 'failurePenalty': -5},
        }
        tree = source_tree()
        definitions = []
        for node in tree.body:
            if isinstance(node, ast.FunctionDef):
                definitions.append(node)
            elif isinstance(node, ast.Assign):
                try:
                    value = ast.literal_eval(node.value)
                except (ValueError, TypeError):
                    continue
                for target in node.targets:
                    if isinstance(target, ast.Name) and target.id not in self.ns:
                        self.ns[target.id] = value
        exec(compile_nodes(definitions), self.ns)

    def popen(self, *args, **kwargs):
        # A local fake child: no command is executed. Exercise actual redirection.
        stream = kwargs['stdout']
        self.process_streams.append(stream)
        stream.write(RAW.encode())
        stream.flush()
        return InertProcess()

    def call(self, name, *args, **kwargs):
        with contextlib.redirect_stdout(self.console):
            return self.ns[name](*args, **kwargs)

    def saved(self):
        path = self.work / 'report.json'
        if not path.exists():
            raise AssertionError('save must produce a public report')
        return json.loads(path.read_text())

    def exception_path(self, error):
        outer = [node for node in source_tree().body if isinstance(node, ast.Try)
                 and any(isinstance(h.type, ast.Name) and h.type.id == 'BaseException'
                         for h in node.handlers)]
        if len(outer) != 1:
            raise AssertionError('expected one actual outer failure boundary')
        self.ns.update(injected_error=error, process=InertProcess(),
                       runtime_listing=lambda: [], qemu_processes=lambda: [],
                       supervisor_processes=lambda: [])
        path = self.work / 'simulator.log'
        if not path.exists():
            path.write_text(RAW)
        if 'sim_log' not in self.ns:
            self.ns['sim_log'] = io.BytesIO()
        block = ast.Try(body=ast.parse('raise injected_error').body,
                        handlers=outer[0].handlers, orelse=[], finalbody=outer[0].finalbody)
        with contextlib.redirect_stdout(self.console):
            exec(compile_nodes([block]), self.ns)

    def initialize_stack(self, sdk, process, *, expect_describe=True):
        outer, = (node for node in source_tree().body if isinstance(node, ast.Try)
                  and any(isinstance(h.type, ast.Name) and h.type.id == 'BaseException' for h in node.handlers))
        block = outer.body
        start, = (i for i, stmt in enumerate(block) if isinstance(stmt, ast.Assign)
                  and isinstance(stmt.value, ast.Call) and isinstance(stmt.value.func, ast.Name)
                  and stmt.value.func.id == 'phase' and stmt.value.args[0].value == 'initialize')
        end, = (i for i, stmt in enumerate(block) if isinstance(stmt, ast.Expr)
                and isinstance(stmt.value, ast.Call) and isinstance(stmt.value.func, ast.Name)
                and stmt.value.func.id == 'check' and stmt.value.args[1].value == 'stack-create-complete')
        factory = Mock(return_value=sdk)
        self.ns.update(args=types.SimpleNamespace(create_timeout=900), client=factory,
                       process=process, stack_id='', TEMPLATE=RAW.encode(),
                       secrets=types.SimpleNamespace(token_hex=lambda _: CANARIES[3]))
        try:
            with contextlib.redirect_stdout(self.console):
                exec(compile_nodes(block[start:end + 1]), self.ns)
        finally:
            self.call('save')
            clients = [call('cloudformation', read_timeout=900)]
            if expect_describe:
                clients.append(call('cloudformation'))
            if factory.call_args_list != clients:
                raise AssertionError('stack creation must use the unchanged SDK client setup')

    def child_capture(self):
        body = source_tree().body
        positions = [i for i, n in enumerate(body) if isinstance(n, ast.Assign)
                     and any(isinstance(t, ast.Name) and t.id == 'process' for t in n.targets)]
        if len(positions) != 1:
            raise AssertionError('expected one actual simulator process setup')
        end = positions[0]
        # All preparation between the original token/state paths and child launch
        # remains included, permitting private capture setup without a fake sink.
        starts = [i for i, n in enumerate(body[:end]) if isinstance(n, ast.Assign)
                  and any(isinstance(t, ast.Name) and t.id == 'state_path' for t in n.targets)]
        if len(starts) != 1:
            raise AssertionError('simulator capture boundary changed')
        exec(compile_nodes(body[starts[0] + 1:end + 1]), self.ns)


class RehearsalPublicOutputTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix='battle-public-output-')
        self.addCleanup(directory.cleanup)
        self.h = PublicHarness(directory.name)
        self.addCleanup(self.close_streams)

    def close_streams(self):
        for stream in self.h.process_streams:
            if not stream.closed:
                stream.close()

    def assert_private_text_absent(self, value):
        public = value if isinstance(value, str) else json.dumps(value, sort_keys=True)
        for marker in CANARIES:
            self.assertNotIn(marker, public)

    def test_save_omits_raw_and_unknown_report_fields(self):
        self.h.report.update(cloudInitOutputTail=RAW, stdout=RAW, stderr=RAW,
                             transcript=RAW, error=RAW, reason=RAW,
                             unknown={'nested': [RAW]}, unknownSafeNumeric=7,
                             stackEvents=[{'reason': RAW}])
        self.h.call('save')
        saved = self.h.saved()
        self.assertEqual(saved['templateSha256'], DIGEST)
        self.assertEqual(saved['result'], 'PASS')
        self.assertEqual(saved['score']['total'], 0)
        self.assert_private_text_absent(saved)
        for key in ('cloudInitOutputTail', 'stdout', 'stderr', 'transcript',
                    'unknown', 'unknownSafeNumeric'):
            self.assertNotIn(key, saved, 'public report uses a field allowlist')

    def test_save_preserves_structured_success(self):
        self.h.call('save')
        saved = self.h.saved()
        for key in ('result', 'score', 'phases', 'templateSha256'):
            self.assertIn(key, saved)
        self.assertEqual(saved['result'], 'PASS')
        self.assertEqual(saved['templateSha256'], DIGEST)
        self.assertEqual(saved['score']['total'], 0)

    def test_all_authored_phase_and_check_labels_survive_public_projection(self):
        # This is an authored-operation inventory, not the security allowlist.
        # Reuse the literal names of real driver steps to detect lost evidence.
        outer = [node for node in source_tree().body if isinstance(node, ast.Try)
                 and any(isinstance(h.type, ast.Name) and h.type.id == 'BaseException'
                         for h in node.handlers)]
        self.assertEqual(len(outer), 1)
        inventory = {}
        current_phase = None
        for statement in outer[0].body + outer[0].finalbody:
            if isinstance(statement, ast.Assign) and isinstance(statement.value, ast.Call):
                call = statement.value
                if isinstance(call.func, ast.Name) and call.func.id == 'phase':
                    self.assertIsInstance(call.args[0], ast.Constant)
                    current_phase = call.args[0].value
                    inventory.setdefault(current_phase, set())
            for node in ast.walk(statement):
                if isinstance(node, ast.Call) and isinstance(node.func, ast.Name):
                    index = {'check': 1, 'send_command': 3}.get(node.func.id)
                    if index is not None and len(node.args) > index:
                        label = node.args[index]
                        if isinstance(label, ast.Constant) and isinstance(label.value, str):
                            self.assertIsNotNone(current_phase)
                            inventory[current_phase].add(label.value)
                # Shutdown records its check directly rather than calling check().
                if isinstance(node, ast.Dict):
                    for key, value in zip(node.keys, node.values):
                        if (isinstance(key, ast.Constant) and key.value == 'name'
                                and isinstance(value, ast.Constant)
                                and value.value == 'simulator-exit-and-no-guest-left'):
                            self.assertEqual(current_phase, 'shutdown')
                            inventory[current_phase].add(value.value)
        self.assertEqual(set(inventory),
                         {'startup', 'initialize', 'operate', 'grade', 'cleanup', 'shutdown'})
        self.assertTrue(all(inventory.values()), 'every actual phase needs check evidence')
        expected = set()
        for phase_name, labels in inventory.items():
            entry = self.h.call('phase', phase_name)
            for label in sorted(labels):
                self.h.call('check', entry, label, True)
                expected.add((phase_name, label, True))
        saved = self.h.saved()
        actual = {(phase['name'], check['name'], check['passed'])
                  for phase in saved['phases'] for check in phase['checks']}
        self.assertEqual(actual, expected)
        for phase in saved['phases']:
            for check in phase['checks']:
                self.assertIs(check['passed'], True)

    def test_result_enum_retains_known_values_and_rejects_unknown_values(self):
        for result in ('PASS', 'PARTIAL', 'FAIL'):
            with self.subTest(result=result):
                self.h.report['result'] = result
                self.h.call('save')
                self.assertEqual(self.h.saved()['result'], result)
        for result in (RAW, 'UNKNOWN_RESULT_VALUE_718'):
            with self.subTest(result=result):
                self.h.report['result'] = result
                try:
                    self.h.call('save')
                except (ValueError, TypeError):
                    continue
                saved = self.h.saved()
                self.assertNotEqual(saved.get('result'), result)
                self.assert_private_text_absent(saved)

    def test_log_rejects_arbitrary_multiline_text(self):
        self.h.call('log', RAW)
        self.assert_private_text_absent(self.h.console.getvalue())

    def test_known_check_console_summary_survives(self):
        entry = self.h.call('phase', 'startup')
        self.h.call('check', entry, 'simulator-ready-with-pinned-bundle', True,
                    startupSeconds=1.5)
        console = self.h.console.getvalue()
        self.assertIn('PASS', console)
        self.assertIn('simulator-ready-with-pinned-bundle', console)
        self.assertEqual(self.h.saved()['phases'][0]['checks'][0]['startupSeconds'], 1.5)

    def test_check_preserves_identity_boolean_and_numeric_diagnostics(self):
        entry = self.h.call('phase', 'operate')
        self.h.call('check', entry, 'participant-shell-executes-in-guest', True,
                    exitCode=0, seconds=1.5, transcript=RAW, stdout=RAW, stderr=RAW,
                    unknownShape={'private': RAW})
        saved = self.h.saved()
        phase = saved['phases'][0]
        self.assertEqual(phase['name'], 'operate')
        check = phase['checks'][0]
        self.assertEqual(check['name'], 'participant-shell-executes-in-guest')
        self.assertIs(check['passed'], True)
        self.assertEqual(check['exitCode'], 0)
        self.assertEqual(check['seconds'], 1.5)
        self.assert_private_text_absent(saved)
        self.assert_private_text_absent(self.h.console.getvalue())
        for key in ('stdout', 'stderr', 'transcript', 'unknownShape'):
            self.assertNotIn(key, check)

    def test_failed_check_records_false_and_propagates(self):
        entry = self.h.call('phase', 'cleanup')
        with self.assertRaisesRegex(RuntimeError, '^stack-deleted failed$'):
            self.h.call('check', entry, 'stack-deleted', False, error=RAW, seconds=2)
        check = self.h.saved()['phases'][0]['checks'][0]
        self.assertEqual(check['name'], 'stack-deleted')
        self.assertIs(check['passed'], False)
        self.assertEqual(check['seconds'], 2)
        self.assert_private_text_absent(self.h.saved())
        self.assert_private_text_absent(self.h.console.getvalue())

    def test_unknown_labels_enum_values_and_unbounded_numbers_fail_closed(self):
        entry = self.h.call('phase', 'initialize')
        try:
            self.h.call('check', entry, CANARIES[4], True, status=RAW,
                        httpStatus=float('inf'), seconds=-1, exitCode=10 ** 100)
        except (ValueError, TypeError):
            pass  # Explicit validation rejection is allowed; stringify is not.
        self.h.call('save')
        saved = self.h.saved()
        self.assert_private_text_absent(saved)
        self.assert_private_text_absent(self.h.console.getvalue())
        serialized = json.dumps(saved)
        self.assertNotIn('Infinity', serialized)
        self.assertNotIn(str(10 ** 100), serialized)

    def test_invalid_digest_is_not_published(self):
        self.h.report['templateSha256'] = RAW
        try:
            self.h.call('save')
        except (ValueError, TypeError):
            return
        self.assert_private_text_absent(self.h.saved())

    def stack_fixture(self, stack_status):
        sdk = Mock()
        sdk.create_stack.return_value = {'StackId': RAW}
        sdk.describe_stacks.return_value = {'Stacks': [
            {'StackId': RAW, 'StackStatus': stack_status, 'StackStatusReason': RAW}]}
        sdk.describe_stack_events.return_value = {'StackEvents': [
            {'StackId': RAW, 'ResourceType': 'AWS::CloudFormation::Stack',
             'ResourceStatus': stack_status, 'ResourceStatusReason': RAW}]}
        process = Mock()
        process.poll.return_value = None
        return sdk, process

    def stack_signals(self):
        phase, = self.h.saved()['phases']
        self.assertEqual(phase['name'], 'initialize')
        self.assert_private_text_absent(self.h.saved())
        self.assert_private_text_absent(self.h.console.getvalue())
        self.assert_private_text_absent((self.h.work / 'simulator.log').read_text())
        return phase['stackCreateDiagnostics'], phase['checks']

    def test_stack_diagnostics_observe_normal_success_without_extra_calls(self):
        sdk, process = self.stack_fixture('CREATE_COMPLETE')
        self.h.initialize_stack(sdk, process)
        signals, checks = self.stack_signals()
        self.assertEqual(signals, {'createStack': 'response-confirmed',
                                   'describeStacks': 'stack-observed',
                                   'describeStackEvents': 'no-failed-event-observed',
                                   'simulatorAtCreateReply': 'running', 'simulatorAtReadback': 'running'})
        self.assertEqual([(c['name'], c['passed']) for c in checks],
                         [('create-stack-response', True), ('stack-create-complete', True)])
        self.assertTrue(checks[0]['responded'])
        self.assertEqual(checks[1]['status'], 'CREATE_COMPLETE')
        sdk.create_stack.assert_called_once()
        sdk.describe_stacks.assert_called_once_with(StackName=RAW)
        sdk.describe_stack_events.assert_called_once_with(StackName=RAW)

    def test_stack_diagnostics_distinguish_service_failure_and_owned_instance_event(self):
        sdk, process = self.stack_fixture('CREATE_FAILED')
        sdk.create_stack.side_effect = ClientError('InternalFailure', 500)
        sdk.describe_stack_events.return_value['StackEvents'] += [
            {'StackId': RAW, 'ResourceType': 'AWS::EC2::Instance', 'ResourceStatus': 'CREATE_FAILED',
             'PhysicalResourceId': RAW, 'ResourceStatusReason': RAW}]
        with self.assertRaisesRegex(RuntimeError, '^stack-create-complete failed$'):
            self.h.initialize_stack(sdk, process)
        signals, checks = self.stack_signals()
        self.assertEqual(signals, {'createStack': 'service-internal-failure',
                                   'describeStacks': 'stack-observed',
                                   'describeStackEvents': 'instance-create-failed',
                                   'simulatorAtCreateReply': 'running', 'simulatorAtReadback': 'running'})
        self.assertFalse(checks[0]['responded'])
        self.assertEqual(checks[1]['status'], 'CREATE_FAILED')
        self.assertFalse(checks[1]['passed'])
        sdk.create_stack.assert_called_once()
        sdk.describe_stacks.assert_called_once_with(StackName='tc-hello-world-battle-team1')
        sdk.describe_stack_events.assert_called_once_with(StackName=RAW)

    def test_stack_diagnostics_keep_description_transport_failure_fatal(self):
        sdk, process = self.stack_fixture('CREATE_COMPLETE')
        sdk.create_stack.side_effect = SDK_TRANSPORT_TYPES['ConnectionClosedError'](RAW)
        failure = SDK_TRANSPORT_TYPES['ReadTimeoutError'](RAW)
        sdk.describe_stacks.side_effect = failure
        process.poll.side_effect = [None, 5]
        with self.assertRaises(type(failure)) as raised:
            self.h.initialize_stack(sdk, process)
        self.assertIs(raised.exception, failure)
        signals, checks = self.stack_signals()
        self.assertEqual(signals, {'createStack': 'connection-closed', 'describeStacks': 'read-timeout',
                                   'simulatorAtCreateReply': 'running', 'simulatorAtReadback': 'exited'})
        self.assertEqual(len(checks), 1)
        self.assertFalse(checks[0]['responded'])
        sdk.describe_stack_events.assert_not_called()

    def test_malformed_create_stack_error_stops_before_readback(self):
        for field in ('Code', 'Message'):
            with self.subTest(field=field):
                self.h.report['phases'] = []
                sdk, process = self.stack_fixture('CREATE_COMPLETE')
                failure = ClientError('InternalFailure', 500)
                del failure.response['Error'][field]
                sdk.create_stack.side_effect = failure
                with self.assertRaises(KeyError) as raised:
                    self.h.initialize_stack(sdk, process, expect_describe=False)
                self.assertEqual(raised.exception.args, (field,))
                signals, checks = self.stack_signals()
                self.assertEqual(signals, {'createStack': 'malformed-service-error',
                                           'simulatorAtCreateReply': 'running'})
                self.assertEqual(checks, [])
                sdk.create_stack.assert_called_once()
                sdk.describe_stacks.assert_not_called()
                sdk.describe_stack_events.assert_not_called()

    def test_malformed_describe_stacks_error_stops_before_completion(self):
        sdk, process = self.stack_fixture('CREATE_COMPLETE')
        failure = ClientError('InternalFailure', 500)
        del failure.response['Error']['Code']
        sdk.describe_stacks.side_effect = [failure, sdk.describe_stacks.return_value]
        with self.assertRaises(KeyError) as raised:
            self.h.initialize_stack(sdk, process)
        self.assertEqual(raised.exception.args, ('Code',))
        signals, checks = self.stack_signals()
        self.assertEqual(signals, {'createStack': 'response-confirmed',
                                   'describeStacks': 'malformed-service-error',
                                   'simulatorAtCreateReply': 'running', 'simulatorAtReadback': 'running'})
        self.assertEqual([c['name'] for c in checks], ['create-stack-response'])
        sdk.describe_stacks.assert_called_once_with(StackName=RAW)
        sdk.describe_stack_events.assert_not_called()

    def test_describe_stacks_keeps_existing_poll_for_error_with_code_only(self):
        sdk, process = self.stack_fixture('CREATE_COMPLETE')
        failure = ClientError('InternalFailure', 500)
        del failure.response['Error']['Message']
        sdk.describe_stacks.side_effect = [failure, sdk.describe_stacks.return_value]
        self.h.initialize_stack(sdk, process)
        signals, checks = self.stack_signals()
        self.assertEqual(signals['describeStacks'], 'stack-observed')
        self.assertEqual(checks[-1]['status'], 'CREATE_COMPLETE')
        self.assertEqual(sdk.describe_stacks.call_args_list, [call(StackName=RAW), call(StackName=RAW)])
        sdk.describe_stack_events.assert_called_once_with(StackName=RAW)

    def test_malformed_stack_event_shape_stops_before_completion(self):
        sdk, process = self.stack_fixture('CREATE_COMPLETE')
        sdk.describe_stack_events.return_value['StackEvents'].append(RAW)
        with self.assertRaises(AttributeError):
            self.h.initialize_stack(sdk, process)
        signals, checks = self.stack_signals()
        self.assertEqual(signals['describeStackEvents'], 'malformed-event-page')
        self.assertEqual([c['name'] for c in checks], ['create-stack-response'])
        sdk.describe_stack_events.assert_called_once_with(StackName=RAW)

    def test_stack_diagnostics_never_publish_unknown_codes_or_misattributed_events(self):
        sdk, process = self.stack_fixture('CREATE_FAILED')
        sdk.create_stack.side_effect = ClientError(RAW, 500)
        sdk.describe_stack_events.return_value['StackEvents'] += [
            {'StackId': 'unrelated-stack', 'ResourceType': 'AWS::EC2::Instance',
             'ResourceStatus': 'CREATE_FAILED', 'ResourceStatusReason': RAW}]
        with self.assertRaisesRegex(RuntimeError, '^stack-create-complete failed$'):
            self.h.initialize_stack(sdk, process)
        signals, _ = self.stack_signals()
        self.assertEqual(signals['createStack'], 'other-service-error')
        self.assertEqual(signals['describeStackEvents'], 'stack-create-failed')
        entry = self.h.report['phases'][0]
        entry['stackCreateDiagnostics'].update(createStack=RAW, simulatorAtReadback='read-timeout',
                                               describeStacks='instance-create-failed', extra=RAW)
        other = self.h.call('phase', 'operate')
        other['stackCreateDiagnostics'] = {'createStack': 'response-confirmed'}
        self.h.call('save')
        initialize, operate = self.h.saved()['phases']
        self.assertEqual(initialize['stackCreateDiagnostics'],
                         {'describeStackEvents': 'stack-create-failed', 'simulatorAtCreateReply': 'running'})
        self.assertNotIn('stackCreateDiagnostics', operate)
        self.assert_private_text_absent(self.h.saved())

    def test_known_status_survives_but_unknown_status_is_not_public(self):
        entry = self.h.call('phase', 'initialize')
        self.h.call('check', entry, 'stack-create-complete', True, status='CREATE_COMPLETE')
        self.assertEqual(self.h.saved()['phases'][0]['checks'][0]['status'], 'CREATE_COMPLETE')
        try:
            self.h.call('check', entry, 'stack-create-complete', True, status=RAW)
        except (ValueError, TypeError):
            pass
        self.h.call('save')
        self.assert_private_text_absent(self.h.saved())
        self.assert_private_text_absent(self.h.console.getvalue())

    def test_numeric_diagnostics_reject_wrong_types_nonfinite_and_range(self):
        invalid = [('seconds', True), ('seconds', float('nan')),
                   ('seconds', float('inf')), ('seconds', -1),
                   ('seconds', 10 ** 100), ('exitCode', True),
                   ('exitCode', 10 ** 100), ('httpStatus', True),
                   ('httpStatus', 9999), ('httpStatus', RAW)]
        for key, value in invalid:
            with self.subTest(key=key, value=value):
                self.h.report['phases'] = []
                self.h.console.seek(0)
                self.h.console.truncate()
                entry = self.h.call('phase', 'startup')
                try:
                    self.h.call('check', entry, 'simulator-ready-with-pinned-bundle', True,
                                **{key: value})
                    self.h.call('save')
                except (ValueError, TypeError):
                    self.assert_private_text_absent(self.h.console.getvalue())
                    self.assertNotRegex(self.h.console.getvalue(), rf'\b{key}\b')
                    continue
                checks = self.h.saved()['phases'][0]['checks']
                for item in checks:
                    self.assertNotIn(key, item, 'invalid numeric detail must be omitted or rejected')
                self.assertNotRegex(self.h.console.getvalue(), rf'\b{key}\b')

    def test_unknown_phase_and_round_labels_are_not_public(self):
        for kind in ('phase', 'score_round'):
            with self.subTest(kind=kind):
                try:
                    if kind == 'phase':
                        self.h.call(kind, CANARIES[4])
                    else:
                        self.h.call(kind, CANARIES[4], {}, {})
                    self.h.call('save')
                except (ValueError, TypeError):
                    continue
                self.assert_private_text_absent(self.h.saved())
                self.assert_private_text_absent(self.h.console.getvalue())

    def test_unknown_objects_are_never_stringified(self):
        conversions = []

        class Unrecognized:
            def __str__(self):
                conversions.append(True)
                return RAW

        entry = self.h.call('phase', 'operate')
        try:
            self.h.call('check', entry, 'participant-shell-executes-in-guest', True,
                        unknown=Unrecognized())
        except (ValueError, TypeError):
            pass
        self.assertEqual(conversions, [], 'unknown values cannot be stringified before filtering')
        self.assert_private_text_absent(self.h.console.getvalue())

    def test_score_preserves_round_arithmetic_without_raw_probe_diagnostics(self):
        observation = {'slot': 'frontend', 'httpStatus': 200, 'path': '/',
                       'latencyMs': 12, 'error': RAW, 'unknown': RAW}
        self.h.ns['probe_slot'] = lambda receipt, slot: dict(observation, slot=slot)
        result = self.h.call('score_round', 'healthy-1', {'frontend': True, 'api': True}, {})
        self.assertEqual(result['points'], 10)
        saved = self.h.saved()
        self.assertEqual(saved['score']['total'], 10)
        self.assertEqual(saved['score']['rounds'][0]['label'], 'healthy-1')
        self.assertEqual(saved['score']['rounds'][0]['points'], 10)
        self.assertEqual(saved['score']['rounds'][0]['observations'][0]['httpStatus'], 200)
        self.assertEqual(saved['score']['rounds'][0]['observations'][0]['latencyMs'], 12)
        self.assert_private_text_absent(saved)
        self.assert_private_text_absent(self.h.console.getvalue())

    def test_score_failure_penalty_and_unregistered_zero_are_preserved(self):
        self.h.ns['probe_slot'] = lambda receipt, slot: {'slot': slot, 'httpStatus': 503, 'path': '/'}
        zero = self.h.call('score_round', 'before-registration', {}, {})
        down = self.h.call('score_round', 'after-disruption', {'frontend': True, 'api': True}, {})
        self.assertEqual((zero['points'], down['points']), (0, -5))
        self.assertEqual(self.h.saved()['score']['total'], -5)

    def test_outer_exception_records_fixed_classification_and_failure_result(self):
        self.h.exception_path(RuntimeError(RAW))
        saved = self.h.saved()
        self.assertEqual(self.h.ns['exit_code'], 1)
        self.assertEqual(saved['result'], 'FAIL')
        self.assertIn('failure', saved)
        self.assertTrue(saved['failure'], 'failure classification must not disappear')
        self.assertIn('RuntimeError', json.dumps(saved['failure']))
        self.assert_private_text_absent(saved)
        self.assert_private_text_absent(self.h.console.getvalue())

    def test_unknown_exception_class_name_and_message_are_not_public(self):
        unknown_exception = type(CANARIES[4], (RuntimeError,), {})
        self.h.exception_path(unknown_exception(RAW))
        saved = self.h.saved()
        self.assertEqual(self.h.ns['exit_code'], 1)
        self.assertEqual(saved['result'], 'FAIL')
        self.assertTrue(saved.get('failure'), 'unknown errors still need fixed classification')
        self.assert_private_text_absent(saved)
        self.assert_private_text_absent(self.h.console.getvalue())

    def test_simulator_raw_capture_is_private_and_public_artifact_is_summary(self):
        self.h.child_capture()
        self.assertEqual(len(self.h.process_streams), 1)
        stream = self.h.process_streams[0]
        self.assertNotEqual(Path(stream.name), self.h.work / 'simulator.log',
                            'raw child output cannot target the uploaded artifact')
        self.assertEqual(os.stat(stream.name).st_mode & 0o777, 0o600)
        self.h.exception_path(RuntimeError(RAW))
        summary = self.h.work / 'simulator.log'
        self.assertTrue(summary.exists(), 'public simulator summary must exist')
        self.assertTrue(summary.read_text().strip(), 'summary must not be an empty/no-op sink')
        self.assert_private_text_absent(summary.read_text())
        workflow = SOURCE.parents[2] / '.github/workflows/battle-hosts.yml'
        upload_paths = workflow.read_text().split('      - name: Retain rehearsal evidence', 1)[1]
        self.assertNotIn(str(Path(stream.name).name), upload_paths)

    def test_noop_output_controls_are_detected(self):
        self.h.ns['save'] = lambda: None
        self.h.call('save')
        with self.assertRaises(AssertionError):
            self.h.saved()

    def test_empty_projection_control_is_detected(self):
        self.h.ns['save'] = lambda: (self.h.work / 'report.json').write_text('{}')
        with self.assertRaises(AssertionError):
            self.test_save_preserves_structured_success()

    def test_noop_console_control_is_detected(self):
        self.h.ns['log'] = lambda message: None
        with self.assertRaises(AssertionError):
            self.test_known_check_console_summary_survives()

    def test_swallowed_check_failure_control_is_detected(self):
        self.h.ns['check'] = lambda *args, **kwargs: None
        with self.assertRaises(AssertionError):
            self.test_failed_check_records_false_and_propagates()

    def test_noop_score_control_is_detected(self):
        self.h.ns['score_round'] = lambda *args: {'points': 10}
        self.h.call('save')
        with self.assertRaises(AssertionError):
            self.test_score_preserves_round_arithmetic_without_raw_probe_diagnostics()


if __name__ == '__main__':
    unittest.main()
