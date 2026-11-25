"""Offline controls for the actual cross-provider browser entry point.

These checks do not start Chromium, the simulator, namespaces or SDK acceptance.
"""
import ast
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent


def require_browser_sandbox(source):
    tree = ast.parse(source)
    launches = [node for node in ast.walk(tree) if isinstance(node, ast.Call) and
                isinstance(node.func, ast.Attribute) and node.func.attr == 'launch' and
                isinstance(node.func.value, ast.Attribute) and node.func.value.attr == 'chromium']
    if len(launches) != 1:
        raise ValueError('Keep one reviewed Chromium launch')
    launch = launches[0]
    sandbox = [node for node in launch.keywords if node.arg == 'chromium_sandbox']
    if len(sandbox) != 1 or not isinstance(sandbox[0].value, ast.Constant) or sandbox[0].value.value is not True:
        raise ValueError('Chromium sandbox must be explicitly enabled')
    if any(isinstance(node, ast.Constant) and isinstance(node.value, str) and
           node.value in {'--no-sandbox', '--disable-setuid-sandbox', '--disable-seccomp-filter-sandbox'}
           for node in ast.walk(tree)):
        raise ValueError('Do not disable any Chromium sandbox layer')
    if launch.args or {node.arg for node in launch.keywords} != {'executable_path', 'chromium_sandbox'}:
        raise ValueError('Keep launch options explicit')


class CrossproviderBrowserSandboxTests(unittest.TestCase):
    def test_actual_entrypoint_explicitly_preserves_chromium_sandbox(self):
        require_browser_sandbox((HERE / 'crossprovider_console.py').read_text())

    def test_removed_false_dynamic_or_overridden_sandbox_is_rejected(self):
        source = (HERE / 'crossprovider_console.py').read_text()
        mutations = [
            source.replace(', chromium_sandbox=True', ''),
            source.replace('chromium_sandbox=True', 'chromium_sandbox=False'),
            source.replace('chromium_sandbox=True', 'chromium_sandbox=bool(1)'),
            source.replace('chromium_sandbox=True', 'chromium_sandbox=True, **options'),
            source.replace('chromium_sandbox=True', 'chromium_sandbox=True, args=options'),
        ]
        for flag in ('--no-sandbox', '--disable-setuid-sandbox', '--disable-seccomp-filter-sandbox'):
            mutations.append(source.replace('chromium_sandbox=True', f'chromium_sandbox=True, args=[{flag!r}]'))
        for index, candidate in enumerate(mutations):
            with self.subTest(index=index), self.assertRaises(ValueError):
                require_browser_sandbox(candidate)

    def test_existing_required_conformance_path_runs_detector_and_real_fixture(self):
        tree = ast.parse((HERE / 'check.py').read_text())
        conformance = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == 'Conformance')
        method = next(node for node in conformance.body if isinstance(node, ast.FunctionDef) and
                      node.name == 'test_crossprovider_console_two_cli_state_and_authority')
        self.assertEqual(method.decorator_list, [])
        calls = [node for node in ast.walk(method) if isinstance(node, ast.Call) and
                 isinstance(node.func, ast.Attribute) and node.func.attr == 'run']
        for filename in ('test_crossprovider_browser_sandbox.py', 'crossprovider_console.py'):
            selected = [call for call in calls if any(isinstance(node, ast.Constant) and node.value == filename
                                                    for node in ast.walk(call))]
            self.assertEqual(len(selected), 1)
            self.assertTrue(any(node.arg == 'check' and isinstance(node.value, ast.Constant) and node.value.value is True
                                for node in selected[0].keywords))


if __name__ == '__main__':
    unittest.main()
