"""Offline lifecycle contract for the Battle driver's own (not CloudFormation's) role.

DeleteRole requires removing the role's inline policy first. Only a 404
NoSuchEntity on the named GetRole establishes absence, not an arbitrary SDK error.
https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeleteRole.html
https://docs.aws.amazon.com/IAM/latest/APIReference/API_GetRole.html
The containing CLI normally starts a VM at module scope. Exercise the actual
helper without starting the simulator or requiring the development SDK here.
"""
import ast
from contextlib import contextmanager
import json
from pathlib import Path
import secrets
import unittest
from unittest.mock import Mock, call


SOURCE = Path(__file__).resolve().parents[1] / 'rehearsal.py'


class ClientError(Exception):
    def __init__(self, code, status):
        self.response = {'Error': {'Code': code}, 'ResponseMetadata': {'HTTPStatusCode': status}}


class DisruptionRoleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        tree = ast.parse(SOURCE.read_text())
        helper = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == 'disruption_role')
        ns = dict(contextmanager=contextmanager, json=json, secrets=secrets, ClientError=ClientError)
        exec(compile(ast.Module(body=[helper], type_ignores=[]), str(SOURCE), 'exec'), ns)
        cls.disruption_role = staticmethod(ns['disruption_role'])

    def setUp(self):
        self.iam = Mock()
        self.record = {'Role': {'RoleId': 'stable-id', 'Arn': 'fixture:role'}}
        self.missing = ClientError('NoSuchEntity', 404)
        self.iam.create_role.return_value = self.record
        self.iam.get_role.side_effect = [self.record, self.missing]

    def run_grade(self, fail=False):
        with self.disruption_role(self.iam, {}, {}) as arn:
            self.assertEqual(arn, 'fixture:role')
            if fail:
                raise RuntimeError('grade failed')

    def assert_owned_cleanup(self):
        role = self.iam.create_role.call_args.kwargs['RoleName']
        self.iam.delete_role_policy.assert_called_once_with(RoleName=role, PolicyName='Disruption')
        self.iam.delete_role.assert_called_once_with(RoleName=role)
        self.assertEqual(self.iam.mock_calls[-4:],
                         [call.get_role(RoleName=role), call.delete_role_policy(RoleName=role, PolicyName='Disruption'),
                          call.delete_role(RoleName=role), call.get_role(RoleName=role)])

    def test_success_removes_policy_then_role_and_confirms_absence(self):
        self.run_grade()
        self.assert_owned_cleanup()

    def test_grade_error_still_releases_and_preserves_error(self):
        with self.assertRaisesRegex(RuntimeError, 'grade failed'):
            self.run_grade(fail=True)
        self.assert_owned_cleanup()

    def test_put_with_ambiguous_outcome_still_removes_owned_role(self):
        self.iam.put_role_policy.side_effect = TimeoutError('lost put response')
        for missing_policy in (False, True):
            with self.subTest(missing_policy=missing_policy):
                self.iam.get_role.side_effect = [self.record, self.missing]
                self.iam.delete_role_policy.side_effect = self.missing if missing_policy else None
                with self.assertRaisesRegex(TimeoutError, 'lost put response'):
                    self.run_grade()
                self.assert_owned_cleanup()
                self.iam.reset_mock()

    def test_create_conflict_never_modifies_existing_role(self):
        self.iam.create_role.side_effect = ClientError('EntityAlreadyExists', 409)
        with self.assertRaises(ClientError):
            self.run_grade()
        self.iam.put_role_policy.assert_not_called()
        self.iam.delete_role_policy.assert_not_called()
        self.iam.delete_role.assert_not_called()

    def test_unconfirmed_creation_never_uses_or_mutates_the_role(self):
        self.iam.create_role.return_value = {'Role': {'RoleId': None, 'Arn': 'fixture:role'}}
        with self.assertRaisesRegex(RuntimeError, 'identity'):
            self.run_grade()
        self.iam.put_role_policy.assert_not_called()
        self.iam.delete_role_policy.assert_not_called()
        self.iam.delete_role.assert_not_called()

    def test_driver_wraps_the_actual_operator_commands_before_reporting_removal(self):
        body = next(n.body for n in ast.parse(SOURCE.read_text()).body if isinstance(n, ast.Try)
                    and any(isinstance(h.type, ast.Name) and h.type.id == 'BaseException' for h in n.handlers))
        scope, = (n for n in body if isinstance(n, ast.With)
                  and any(isinstance(i.context_expr, ast.Call) and isinstance(i.context_expr.func, ast.Name)
                          and i.context_expr.func.id == 'disruption_role' for i in n.items))
        commands = {n.args[3].value for n in ast.walk(scope) if isinstance(n, ast.Call)
                    and isinstance(n.func, ast.Name) and n.func.id == 'send_command'}
        self.assertEqual(commands, {'operator-disruption-run-shell-script', 'operator-second-disruption',
                                    'operator-revert-run-shell-script'})
        after = body[body.index(scope) + 1]
        self.assertIsInstance(after, ast.Expr)
        self.assertEqual(after.value.func.id, 'check')
        self.assertEqual(after.value.args[1].value, 'disruption-operator-role-removed')

    def test_changed_identity_is_not_ours_to_delete(self):
        self.iam.get_role.side_effect = [{'Role': {'RoleId': 'replacement', 'Arn': 'fixture:role'}}]
        with self.assertRaisesRegex(RuntimeError, 'identity'):
            self.run_grade()
        self.iam.delete_role_policy.assert_not_called()
        self.iam.delete_role.assert_not_called()

    def test_existence_and_failed_lookup_do_not_establish_absence(self):
        for observed in (self.record, ClientError('AccessDenied', 403), ClientError('NoSuchEntity', 500)):
            with self.subTest(observed=observed):
                self.iam.get_role.side_effect = [self.record, observed]
                with self.assertRaises(RuntimeError if observed is self.record else ClientError):
                    self.run_grade()

    def test_unknown_policy_delete_error_does_not_delete_role(self):
        self.iam.delete_role_policy.side_effect = ClientError('AccessDenied', 403)
        with self.assertRaises(ClientError):
            self.run_grade()
        self.iam.delete_role.assert_not_called()


if __name__ == '__main__':
    unittest.main()
