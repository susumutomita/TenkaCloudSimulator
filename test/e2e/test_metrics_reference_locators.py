"""Exercise exact Scalar button names with the installed Playwright serializer.

No browser, connection, SDK request or package acquisition is created. The real
helper function is extracted from its AST to avoid its runtime imports/main.
"""
import ast
import json
from pathlib import Path
import re
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from playwright._impl._locator import Locator, get_by_role_selector


ROOT = Path(__file__).resolve().parents[2]
HELPER = Path(__file__).with_name('cloudwatch_metrics_reference.py')
tree = ast.parse(HELPER.read_text())
functions = [node for node in tree.body if isinstance(node, ast.FunctionDef)
             and node.name == 'exact_named_button']
assert len(functions) == 1
namespace = {}
exec(compile(ast.Module(body=functions, type_ignores=[]), str(HELPER), 'exec'), namespace)
exact_named_button = namespace['exact_named_button']


class OfflinePage:
    """Only selector construction; actual Locator.or_ never sends a command."""
    def __init__(self):
        self.frame = SimpleNamespace(_loop=None, _connection=SimpleNamespace(_dispatcher_fiber=None))
        self.calls = []

    def get_by_role(self, role, *, name, exact):
        self.calls.append((role, name, exact))
        return Locator(self.frame, get_by_role_selector(role, name=name, exact=exact))


class ExactScalarButtonNames(unittest.TestCase):
    def assert_exact_inventory(self, labels):
        page = OfflinePage()
        locator = exact_named_button(page, labels)
        self.assertEqual(page.calls, [('button', name, True) for name in labels])
        # Real Playwright string names serialize as quoted, case-sensitive
        # attribute values, not slash-delimited regular expressions.
        first = get_by_role_selector('button', name=labels[0], exact=True)
        self.assertTrue(first.startswith('internal:role=button[name="'))
        self.assertTrue(first.endswith('"s]'))
        serialized = first
        for name in labels[1:]:
            literal = get_by_role_selector('button', name=name, exact=True)
            self.assertTrue(literal.startswith('internal:role=button[name="'))
            self.assertTrue(literal.endswith('"s]'))
            serialized += ' >> internal:or=' + json.dumps(literal)
        self.assertEqual(locator._selector, serialized)
        return page, locator

    def test_served_media_inventory_uses_exact_literal_names(self):
        spec = json.loads((ROOT/'internal/server/apidocs/openapi.json').read_text())
        values = tuple(spec['paths']['/']['post']['requestBody']['content'])
        self.assertTrue({'application/x-amz-json-1.1',
                         'application/x-www-form-urlencoded',
                         'application/x-amz-json-1.0'}.issubset(values))
        labels = ['Selected Content Type: ' + value for value in values]
        _, locator = self.assert_exact_inventory(labels)
        self.assertNotIn('[name=/', locator._selector)

    def test_reviewed_form_example_names_use_same_exact_contract(self):
        spec = json.loads((ROOT/'internal/server/apidocs/openapi.json').read_text())
        examples = spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']['examples']
        labels = sorted({example.get('summary', key) for key, example in examples.items()})
        self.assertGreater(len(labels), 3)
        self.assert_exact_inventory(labels)

    def test_slashes_quotes_backslashes_and_regexp_metacharacters_stay_literal(self):
        self.assert_exact_inventory(['Selected Content Type: application/x-example',
                                     'Literal / [a-z]+ (x|y) ^$ "quote" \\path'])

    def test_single_name_does_not_create_broad_or_partial_selector(self):
        page, locator = self.assert_exact_inventory(['DescribeStacks'])
        self.assertEqual(len(page.calls), 1)
        self.assertNotIn('internal:or=', locator._selector)

    def test_empty_inventory_and_empty_name_refuse_before_locator_creation(self):
        for labels in ([], ['']):
            with self.subTest(labels=labels):
                page = OfflinePage()
                with self.assertRaises(AssertionError):
                    exact_named_button(page, labels)
                self.assertEqual(page.calls, [])

    def test_duplicate_names_refuse_before_locator_creation(self):
        page = OfflinePage()
        with self.assertRaises(AssertionError):
            exact_named_button(page, ['ListMetrics', 'ListMetrics'])
        self.assertEqual(page.calls, [])

    def test_nonstrings_and_pattern_names_cannot_reintroduce_regexp_serialization(self):
        for labels in ('ListMetrics', b'ListMetrics', [None], [re.compile('application/x')]):
            with self.subTest(kind=type(labels).__name__):
                page = OfflinePage()
                with self.assertRaises(AssertionError):
                    exact_named_button(page, labels)
                self.assertEqual(page.calls, [])


class AuditCaptureOrdering(unittest.TestCase):
    def fixture(self, reports, violations, *, reflow=True):
        observation = {'violations': violations, 'incomplete': [], 'passes': 12,
                       'boundedTargets': {'details': [], 'privacy': 'controlled fixture'}}
        class Page:
            def __init__(self):
                self.calls = []
                self.screenshots = []

            def evaluate(self, script):
                self.calls.append(script)
                if script == '/* controlled empty axe injection */':
                    return None
                if 'axe.run(document' in script:
                    return observation
                if script == 'document.documentElement.scrollWidth <= innerWidth + 1':
                    return reflow
                raise AssertionError('Unexpected operation in controlled audit fixture')

            def screenshot(self, **kwargs):
                self.screenshots.append(kwargs)

        page = Page()
        axe_path = reports/'dummy-axe.js'
        axe_path.write_text('/* controlled empty axe injection */')
        result = {'audits': []}
        nodes = [node for node in ast.walk(tree) if isinstance(node, ast.FunctionDef)
                 and node.name == 'audit']
        self.assertEqual(len(nodes), 1)
        scope = {'page': page, 'axe_path': axe_path, 'width': 1440,
                 'reports': reports, 'result': result, 'json': json}
        exec(compile(ast.Module(body=nodes, type_ignores=[]), str(HELPER), 'exec'), scope)
        return scope['audit'], observation, result, page

    def test_bounded_observation_is_saved_before_original_strict_assertion(self):
        with tempfile.TemporaryDirectory(prefix='metrics-audit-unit-') as temporary:
            reports = Path(temporary)
            audit, observation, result, page = self.fixture(reports, [{'id': 'aria-required-attr', 'nodes': 2}])
            with self.assertRaisesRegex(AssertionError, 'Strict reference axe violation'):
                audit('put-form')
            self.assertEqual(json.loads((reports/'metrics-reference-1440-put-form-axe.json').read_text()), observation)
            self.assertEqual(result['audits'], [])
            self.assertEqual(page.screenshots, [])

    def test_capture_io_error_does_not_mask_existing_strict_axe_failure(self):
        with tempfile.TemporaryDirectory(prefix='metrics-audit-unit-') as temporary:
            reports = Path(temporary)
            audit, _, result, page = self.fixture(reports, [{'id': 'aria-required-attr', 'nodes': 2}])
            with patch.object(Path, 'write_text', side_effect=OSError('controlled receipt write failure')):
                with self.assertRaisesRegex(AssertionError, 'Strict reference axe violation'):
                    audit('put-form')
            self.assertEqual(result['auditCaptureErrors'], ['OSError'])
            self.assertEqual(result['audits'], [])
            self.assertEqual(page.screenshots, [])

    def test_capture_io_error_does_not_mask_existing_strict_reflow_failure(self):
        with tempfile.TemporaryDirectory(prefix='metrics-audit-unit-') as temporary:
            audit, _, result, page = self.fixture(Path(temporary), [], reflow=False)
            with patch.object(Path, 'write_text', side_effect=OSError('controlled receipt write failure')):
                with self.assertRaisesRegex(AssertionError, 'Reference horizontal reflow failure'):
                    audit('put-form')
            self.assertEqual(result['auditCaptureErrors'], ['OSError'])
            self.assertEqual(result['audits'], [])
            self.assertEqual(page.screenshots, [])

    def test_successful_audit_keeps_original_acceptance_receipt_shape(self):
        with tempfile.TemporaryDirectory(prefix='metrics-audit-unit-') as temporary:
            audit, _, result, page = self.fixture(Path(temporary), [])
            audit('put-form')
            self.assertEqual(result['audits'], [{'width': 1440, 'state': 'put-form',
                                                'violations': [], 'passes': 12}])
            self.assertEqual(len(page.screenshots), 1)


if __name__ == '__main__':
    unittest.main(verbosity=2)
