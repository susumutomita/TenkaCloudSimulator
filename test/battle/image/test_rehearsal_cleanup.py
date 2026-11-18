"""Offline acceptance for Battle rehearsal's stack-cleanup observation.

Oracle (checked 2026-11-18): CloudFormation DescribeStacks reports a missing
stack as ValidationError; permission/internal/throttling failures do not establish
absence. The simulator's named-stack absence is ValidationError / "Stack does
not exist." (internal/aws/cloudformation.go). DeleteStack acknowledgement alone
does not establish completion.
https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStacks.html
https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeleteStack.html
https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/CommonErrors.html

The script has top-level VM startup, so extract only its actual observation block
and check() function. No copy of its classification logic, boto dependency,
socket, simulator, VM, credential or cloud access is involved. Standard discovery
in make test-scripts already includes this file.
"""
import ast
import json
from pathlib import Path
import re
import types
import unittest
from unittest.mock import patch


REHEARSAL = Path(__file__).resolve().parents[1] / 'rehearsal.py'
STACK_ID = 'fixture-stack'


class ClientError(Exception):
    """The response interface read by rehearsal, with no SDK/transport."""
    def __init__(self, code, message, status=400):
        super().__init__(message)
        self.response = {'Error': {'Code': code, 'Message': message},
                         'ResponseMetadata': {'HTTPStatusCode': status}}


def is_stack_deleted_check(node):
    return (isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
            and isinstance(node.value.func, ast.Name)
            and node.value.func.id == 'check'
            and len(node.value.args) >= 2
            and isinstance(node.value.args[1], ast.Constant)
            and node.value.args[1].value == 'stack-deleted')


def observation_tree():
    tree = ast.parse(REHEARSAL.read_text(), filename=str(REHEARSAL))
    checks = [n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == 'check']
    # This is an extraction boundary, not an alternate implementation. Retain
    # any new assignments/branches between the observation and its real check.
    blocks = [n.body for n in tree.body if isinstance(n, ast.Try)
              and any(is_stack_deleted_check(x) for x in n.body)]
    if len(checks) != 1 or len(blocks) != 1:
        raise AssertionError('expected one real check function and cleanup block')
    body = blocks[0]
    end = next(i for i, n in enumerate(body) if is_stack_deleted_check(n))
    starts = [i for i, n in enumerate(body[:end]) if isinstance(n, ast.Assign)
              and any(isinstance(target, ast.Name) and target.id == 'final'
                      for target in n.targets)]
    if len(starts) != 1:
        raise AssertionError('cleanup observation extraction boundary changed')
    selected = body[starts[0]:end + 1]
    return ast.Module(body=[checks[0], *selected], type_ignores=[])


def observation_code():
    return compile(observation_tree(), str(REHEARSAL), 'exec')


class FakeTime:
    def __init__(self):
        self.now = 0.0
        self.calls = 0

    def time(self):
        self.calls += 1
        if self.calls > 1000:
            raise AssertionError('observation did not respect its bounded deadline')
        return self.now

    def sleep(self, seconds):
        if seconds <= 0:
            raise AssertionError('observation must make bounded deadline progress')
        self.now += seconds


class RehearsalCleanupObservationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.code = observation_code()

    def observe(self, response=None, error=None, delete_error=None):
        calls, saves, logs = [], [], []
        entry = {'checks': []}
        clock = FakeTime()

        def describe_stacks(*, StackName):
            self.assertEqual(StackName, STACK_ID)
            calls.append(StackName)
            if error is not None:
                raise error
            return response

        namespace = {
            'ClientError': ClientError, 'json': json, 're': re,
            'time': clock, 'describe': types.SimpleNamespace(describe_stacks=describe_stacks),
            'stack_id': STACK_ID, 'delete_error': delete_error,
            'started': 0.0, 'p': entry, 'stamp': lambda: 'fixture-time',
            'log': logs.append, 'save': lambda: saves.append(True),
        }
        failure = None
        try:
            exec(self.code, namespace)
        except AssertionError:
            raise  # Harness/extraction errors are never accepted as rejection.
        except BaseException as caught:
            failure = caught
        self.assertTrue(calls, 'the actual observation must run')
        return entry['checks'], failure, saves, logs, clock

    def assert_confirmed(self, **kwargs):
        checks, failure, saves, _, _ = self.observe(**kwargs)
        self.assertIsNone(failure)
        self.assertEqual(len(checks), 1)
        self.assertEqual(checks[0]['name'], 'stack-deleted')
        self.assertIs(checks[0]['passed'], True)
        self.assertTrue(saves, 'a confirmed outcome must be recorded')

    def assert_not_confirmed(self, **kwargs):
        checks, failure, saves, _, clock = self.observe(**kwargs)
        self.assertIsNotNone(failure, 'unknown/failed cleanup must propagate failure')
        self.assertFalse(any(c['passed'] for c in checks),
                         'unavailable observation must never record deletion success')
        if failure is not kwargs.get('error'):
            # A new programming error is not a cleanup rejection. A deliberate
            # check() failure must carry that function's exact recorded outcome.
            self.assertIs(type(failure), RuntimeError)
            self.assertEqual(str(failure), 'stack-deleted failed')
            self.assertEqual(len(checks), 1)
            self.assertEqual(checks[0]['name'], 'stack-deleted')
            self.assertIs(checks[0]['passed'], False)
            self.assertEqual(saves, [True])
        return checks, failure, clock

    def test_delete_complete_is_confirmed(self):
        self.assert_confirmed(response={'Stacks': [{'StackStatus': 'DELETE_COMPLETE'}]})

    def test_confirmed_absence_is_confirmed(self):
        self.assert_confirmed(error=ClientError('ValidationError', 'Stack does not exist.'))

    def test_delete_failed_is_rejected_and_recorded(self):
        checks, failure, _ = self.assert_not_confirmed(
            response={'Stacks': [{'StackStatus': 'DELETE_FAILED'}]})
        self.assertIsInstance(failure, RuntimeError)
        self.assertEqual([c['passed'] for c in checks], [False])

    def test_access_denied_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('AccessDenied', 'Not authorized.', 403))

    def test_access_denied_exception_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('AccessDeniedException', 'Not authorized.', 403))

    def test_internal_failure_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('InternalFailure', 'Internal failure.', 500))

    def test_expired_token_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('ExpiredTokenException', 'Token expired.', 403))

    def test_unrelated_validation_error_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('ValidationError', 'Invalid stack name.'))

    def test_throttling_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('ThrottlingException', 'Rate exceeded.'))

    def test_access_denied_with_missing_text_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('AccessDenied', 'Stack does not exist.', 403))

    def test_internal_failure_with_missing_text_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('InternalFailure', 'Stack does not exist.', 500))

    def test_missing_text_with_http_500_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('ValidationError', 'Stack does not exist.', 500))

    def test_missing_text_with_http_403_is_not_absence(self):
        self.assert_not_confirmed(error=ClientError('ValidationError', 'Stack does not exist.', 403))

    def test_named_empty_stack_list_is_not_absence(self):
        self.assert_not_confirmed(response={'Stacks': []})

    def test_transport_timeout_propagates_without_success(self):
        _, failure, _ = self.assert_not_confirmed(error=TimeoutError('DescribeStacks timed out'))
        self.assertIsInstance(failure, TimeoutError)

    def test_transport_failure_propagates_without_success(self):
        _, failure, _ = self.assert_not_confirmed(error=ConnectionError('Connection lost'))
        self.assertIsInstance(failure, ConnectionError)

    def test_observation_deadline_expires_without_success(self):
        checks, failure, clock = self.assert_not_confirmed(
            response={'Stacks': [{'StackStatus': 'DELETE_IN_PROGRESS'}]})
        self.assertIsInstance(failure, RuntimeError)
        self.assertGreaterEqual(clock.now, 120)
        self.assertEqual([c['passed'] for c in checks], [False])

    def test_delete_ack_error_cannot_be_hidden_by_absence(self):
        self.assert_not_confirmed(error=ClientError('ValidationError', 'Stack does not exist.'),
                                  delete_error='DeleteStack failed')

    def test_interruption_is_not_converted_to_cleanup_success(self):
        _, failure, _ = self.assert_not_confirmed(error=KeyboardInterrupt())
        self.assertIsInstance(failure, KeyboardInterrupt)

    def test_negative_oracle_rejects_conditional_programming_errors(self):
        # Negative controls: leave positive branches working, but introduce a
        # new exception only for the permission-denied branch. Such a bug must
        # not make an unavailable-observation assertion pass.
        for exception in ('NameError', 'TypeError', 'SystemExit', 'RuntimeError'):
            with self.subTest(exception=exception):
                tree = observation_tree()
                handlers = [n for n in ast.walk(tree) if isinstance(n, ast.ExceptHandler)
                            and isinstance(n.type, ast.Name) and n.type.id == 'ClientError']
                self.assertEqual(len(handlers), 1)
                handler = handlers[0]
                conditional_fault = ast.parse(
                    f"if {handler.name}.response['Error']['Code'] == 'AccessDenied':\n"
                    f"    raise {exception}('stack-deleted failed')\n").body[0]
                handler.body.insert(0, conditional_fault)
                mutant = compile(ast.fix_missing_locations(tree),
                                 '<conditional-programming-error-control>', 'exec')
                with patch.object(self, 'code', mutant):
                    self.assert_confirmed(response={'Stacks': [{'StackStatus': 'DELETE_COMPLETE'}]})
                    self.assert_confirmed(error=ClientError('ValidationError', 'Stack does not exist.'))
                    with self.assertRaises(AssertionError):
                        self.assert_not_confirmed(error=ClientError('AccessDenied', 'Not authorized.', 403))


if __name__ == '__main__':
    unittest.main()
