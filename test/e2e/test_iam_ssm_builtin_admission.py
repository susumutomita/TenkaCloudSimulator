"""Pure strict-refusal assertion detectors; no SDK, CLI or guest is started."""
import ast
import copy
from pathlib import Path
import unittest


class FakeClientError(Exception):
    def __init__(self, response):
        self.response = response


class BuiltinAdmissionAssertionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Isolate the assertion helper without importing the live acceptance harness.
        source = Path(__file__).with_name("iam_ssm_builtin.py").read_text()
        tree = ast.parse(source)
        function = next(node for node in tree.body if isinstance(node, ast.FunctionDef)
                        and node.name == "native_participant_required")
        scope = {"ClientError": FakeClientError}
        exec(compile(ast.Module(body=[function], type_ignores=[]), "assertion-helper", "exec"), scope)
        cls.check = staticmethod(scope["native_participant_required"])

    def test_exact_boundary_and_refusal_detectors(self):
        for message in ("A verified participant session is required.", "A verified assumed-role request is required."):
            exact = {"ResponseMetadata": {"HTTPStatusCode": 403},
                     "Error": {"Code": "AccessDeniedException", "Message": message}}
            def raises(response):
                def invoke():
                    raise FakeClientError(response)
                return invoke
            self.check(raises(exact), message)
            for parent, key, wrong in (("ResponseMetadata", "HTTPStatusCode", 501),
                                       ("Error", "Code", "UnknownOperationException"),
                                       ("Error", "Message", "The current policy does not allow this action.")):
                mutated = copy.deepcopy(exact)
                mutated[parent][key] = wrong
                with self.assertRaises(AssertionError):
                    self.check(raises(mutated), message)
            with self.assertRaises(AssertionError):
                self.check(lambda: {}, message)


if __name__ == "__main__":
    unittest.main()
