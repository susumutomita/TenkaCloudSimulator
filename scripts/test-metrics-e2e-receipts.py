"""Controlled-child regression tests for the real Metrics E2E receipt wrappers.

These are deliberately fake receipt fixtures, NOT SDK, HTTP, CLI or browser
acceptance. Only two actual validator functions and three actual wrapper method
ASTs are compiled. check.py is never imported or executed; its SDK/Playwright
imports, global REPORTS mkdir and all other cases are excluded.
"""
import ast
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'test/e2e/check.py'
METHODS = {
    'sdk': 'test_cloudwatch_metrics_native_sdk_two_clis_current_policy_and_restart',
    'console': 'test_cloudwatch_metrics_console_native_statistics_context_and_boundaries',
    'reference': 'test_cloudwatch_metrics_served_scalar_query_examples_and_native_state',
}
SCRIPTS = {'sdk': 'cloudwatch_metrics.py', 'console': 'cloudwatch_metrics_console.py',
           'reference': 'cloudwatch_metrics_reference.py'}
HELPERS = {'_metrics_acceptance_receipt', '_metrics_exact_counts'}

# This child writes only controlled dummy JSON in the fresh directory supplied
# by the ACTUAL wrapper. It does not import or execute any acceptance helper.
CHILD = '''import hashlib,json,os,sys
from pathlib import Path
configuration=json.loads(Path(os.environ['METRICS_RECEIPT_FIXTURE']).read_text())
mode=configuration['mode']
if mode in ('noop','stale'):raise SystemExit(0)
if mode=='nonzero':raise SystemExit(7)
out=Path(os.environ['SIMULATOR_REPORTS']);out.mkdir(parents=True,exist_ok=False)
if mode=='malformed':
 (out/configuration['name']).write_text('{');raise SystemExit(0)
r=configuration['receipt']
r['binarySHA256']=hashlib.sha256(Path(os.environ['SIMULATOR_BINARY']).read_bytes()).hexdigest()
r['helperSHA256']=hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
if mode=='wrong-binary':r['binarySHA256']='0'*64
if mode=='wrong-helper':r['helperSHA256']='0'*64
if mode=='binary-post-false':r['binaryPostExact']=False
if mode=='helper-post-false':r['helperPostExact']=False
if mode=='integer-postguard':r['helperPostExact']=1
if mode=='cleanup-false':r['cleanup']=False
if mode=='partial':r['status']='RUNNING'
if mode=='bool-counter':r['caseCounters']['transportFetches']=True
if mode=='missing-counter':del r['caseCounters']['transportFetches']
if mode=='extra-counter':r['caseCounters']['unreviewed']=1
if mode=='cleanup-detail-false':r['cleanupDetails']['cliA']=False
if mode=='secondary-cleanup':r['secondaryCleanupTypes']=['DummyFailure']
if mode=='spec-post-false':r['specPostExact']=False
if mode=='wrong-spec':r['specSHA256']='0'*64
if mode=='wrong-action':r['sends'][0]['action']='ListMetrics'
if mode=='missing-send':r['sends'].pop()
if mode=='audit-violation':r['audits'][0]['violations']=[{'id':'dummy'}]
if mode=='sdk-unbalanced':r['SDKCounters'][0]['responses']-=1
(out/configuration['name']).write_text(json.dumps(r))
(out/'fixture-invocation.json').write_text(json.dumps({'python':sys.executable,'argv':sys.argv[1:],'binary':os.environ['SIMULATOR_BINARY']}))
'''


def extract_actual_wrappers():
    source = SOURCE.read_text()
    tree = ast.parse(source, filename=str(SOURCE))
    functions = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name in HELPERS]
    assert {node.name for node in functions} == HELPERS
    conformance = next(node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == 'Conformance')
    methods = [node for node in conformance.body if isinstance(node, ast.FunctionDef) and node.name in METHODS.values()]
    assert {node.name for node in methods} == set(METHODS.values())
    extracted = ast.Module(body=[*copy.deepcopy(functions), ast.ClassDef(
        name='ActualWrappers', bases=[ast.Name(id='TestCase', ctx=ast.Load())],
        keywords=[], body=copy.deepcopy(methods), decorator_list=[], type_params=[])], type_ignores=[])
    ast.fix_missing_locations(extracted)
    # Prove the actual selected bodies were copied, never reimplemented or
    # weakened in this test harness. No imports/global statements are included.
    assert [ast.dump(n, include_attributes=False) for n in extracted.body[:2]] == [ast.dump(n, include_attributes=False) for n in functions]
    assert [ast.dump(n, include_attributes=False) for n in extracted.body[2].body] == [ast.dump(n, include_attributes=False) for n in methods]
    return source, extracted


def positive_receipt(kind, spec_sha):
    receipt = {'status': 'PASS', 'binaryPostExact': True, 'helperPostExact': True, 'cleanup': True}
    if kind == 'sdk':
        counts = [3] * 11 + [2] * 4  # Independent literal41 across15 fake clients.
        receipt.update(caseCounters={'groups': 5, 'calls': 41, 'sends': 41, 'responses': 41, 'controls': 3},
                       SDKModelSHA256='855d223991b77b224ac0a4079a75a17bd51fa7df57a89d9ab2979fbf6738539a',
                       checks=['dummy'] * 5, controlCalls=[{}] * 3,
                       cleanup='allclientsclosed/CLIsexited0reaped/ownedDB+tokenfilesremoved',
                       SDKCounters=[{'calls': n, 'sends': n, 'responses': n, 'statuses': [200] * n,
                                     'encoding': ['gzip'] + [None] * (n - 1)} for n in counts])
    elif kind == 'console':
        receipt.update(screens=[{'width': n, 'violations': [], 'passes': 1} for n in (1440, 390)],
            caseCounters={'lateXMLCompletions': 3, 'crossTokens401': 2, 'paginationSeries': 501,
                          'nativeAcceptedPoints': 1440, 'syntheticRejectedPoints': 1441,
                          'nativeRejectedGroups': 1441, 'countRecoveryPoints': 720,
                          'period120RecoveryPoints': 721, 'transportFetches': 1,
                          'transportFulfills': 1, 'desktopMobileWidths': 2},
            nativeCalls=17, nativeResponses=17, labelledTransportFaultFulfillments=1,
            nativeOperations={'PutMetricData': 9, 'ListMetrics': 2, 'GetMetricStatistics': 5, 'HostPolicySetup': 1},
            capabilityOrPresentationFulfillmentUsed=False,
            cleanupDetails={k: True for k in ('contextA', 'peerContext', 'browser', 'cliA', 'cliB', 'playwright', 'temporaryDirectory')},
            secondaryCleanupTypes=[], strengthened={'lateXMLCompletions': 3, 'crossTokens401': 2, 'paginationSeries': 501})
    else:
        assert kind == 'reference'
        sequence = [('PutMetricData', 200), ('ListMetrics', 200), ('GetMetricStatistics', 400),
                    ('GetMetricStatistics', 200), ('PutMetricData', 403), ('ListMetrics', 200)]
        receipt.update(specPostExact=True, specSHA256=spec_sha,
            sends=[{'width': w, 'action': a, 'status': s} for w in (1440, 390) for a, s in sequence],
            audits=[{'width': w, 'state': state, 'violations': [], 'passes': 1} for w in (1440, 390)
                    for state in ('put-form', 'invalid-time', 'statistics', 'reader-denied')],
            browserCounts=[{'width': w, 'nativePOSTResponses': 6, 'outside': 0, 'pageErrors': 0} for w in (1440, 390)],
            SDKCounters=[{'calls': n, 'sends': n, 'responses': n} for n in (1, 2, 2, 1, 2, 2)])
    return receipt


class MetricsReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='metrics-receipt-fixture-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.reports = self.root / 'reports'
        self.reports.mkdir()
        self.binary = self.root / 'dummy-not-executable-cli'
        self.binary.write_bytes(b'CONTROLLED WRAPPER FIXTURE ONLY; NEVER EXECUTE')
        self.source, self.extracted = extract_actual_wrappers()
        fake_script_dir = self.root / 'test/e2e'
        fake_script_dir.mkdir(parents=True)
        for name in SCRIPTS.values():
            (fake_script_dir / name).write_text(CHILD)
        self.fixture = self.root / 'fixture.json'
        self.spec = self.root / 'internal/server/apidocs/openapi.json'
        self.spec.parent.mkdir(parents=True)
        self.spec.write_text('{"fixture": "NOT an OpenAPI or native API proof"}')
        self.namespace = {'Path': Path, 'sys': sys, 'hashlib': hashlib, 'tempfile': tempfile,
                          'os': os, 'subprocess': subprocess, 'json': json, 'TestCase': unittest.TestCase,
                          '__file__': str(fake_script_dir / 'check.py'), '__name__': 'controlled_metrics_wrapper_fixture',
                          'BINARY': self.binary, 'REPORTS': self.reports}
        exec(compile(self.extracted, str(SOURCE) + ':selected-AST-only', 'exec'), self.namespace)
        self.prior_cwd = Path.cwd()
        os.chdir(self.root)
        self.addCleanup(os.chdir, self.prior_cwd)
        self.environment = patch.dict(os.environ, {'METRICS_RECEIPT_FIXTURE': str(self.fixture), 'PYTHONDONTWRITEBYTECODE': '1'})
        self.environment.start()
        self.addCleanup(self.environment.stop)

    def invoke(self, kind, mode='positive'):
        name = 'cloudwatch-metrics-a11y.json' if kind == 'console' else 'result.json'
        receipt = positive_receipt(kind, hashlib.sha256(self.spec.read_bytes()).hexdigest())
        self.fixture.write_text(json.dumps({'mode': mode, 'name': name, 'receipt': receipt}))
        instance = self.namespace['ActualWrappers'](METHODS[kind])
        getattr(instance, METHODS[kind])()
        return list(self.reports.glob('*/helper/' + name))

    def test_exact_source_extraction_excludes_imports_and_global_side_effects(self):
        self.assertEqual(len(self.extracted.body), 3)
        self.assertTrue(all(isinstance(n, (ast.FunctionDef, ast.ClassDef)) for n in self.extracted.body))
        self.assertNotIn('boto3', self.namespace)
        self.assertNotIn('sync_playwright', self.namespace)
        self.assertEqual(SOURCE.read_text(), self.source)

    def test_positive_literal41_sdk_receipt_through_actual_wrapper(self):
        paths = self.invoke('sdk')
        self.assertEqual(len(paths), 1)
        invocation = json.loads((paths[0].parent / 'fixture-invocation.json').read_text())
        self.assertEqual(invocation['python'], sys.executable)
        self.assertIn('--binary-sha256', invocation['argv'])
        self.assertEqual(invocation['binary'], str(self.binary))

    def test_positive_console_exact_boundary_and_cleanup_receipt(self):
        self.assertEqual(len(self.invoke('console')), 1)

    def test_positive_literal12_scalar_sends_through_actual_wrapper(self):
        self.assertEqual(len(self.invoke('reference')), 1)

    def test_zero_exit_no_output_is_not_acceptance(self):
        for kind in METHODS:
            with self.subTest(kind=kind), self.assertRaises(FileNotFoundError):
                self.invoke(kind, 'noop')

    def test_preexisting_stale_receipt_cannot_satisfy_fresh_invocation(self):
        old = self.reports / 'stale/helper/result.json'
        old.parent.mkdir(parents=True)
        old.write_text('{"status":"PASS","fixture":"STALE"}')
        before = old.read_bytes()
        with self.assertRaises(FileNotFoundError):
            self.invoke('sdk', 'stale')
        self.assertEqual(old.read_bytes(), before)

    def test_nonzero_exit_and_malformed_or_partial_receipt_refused(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke('sdk', 'nonzero')
        with self.assertRaises(json.JSONDecodeError):
            self.invoke('sdk', 'malformed')
        with self.assertRaises(AssertionError):
            self.invoke('sdk', 'partial')

    def test_wrong_current_binary_or_helper_identity_refused(self):
        for kind in METHODS:
            for mode in ('wrong-binary', 'wrong-helper'):
                with self.subTest(kind=kind, mode=mode), self.assertRaises(AssertionError):
                    self.invoke(kind, mode)

    def test_false_or_integer_postguards_and_cleanup_refused(self):
        for kind in METHODS:
            for mode in ('binary-post-false', 'helper-post-false', 'integer-postguard', 'cleanup-false'):
                with self.subTest(kind=kind, mode=mode), self.assertRaises(AssertionError):
                    self.invoke(kind, mode)

    def test_boolean_counter_and_missing_extra_counter_keys_refused(self):
        for mode in ('bool-counter', 'missing-counter', 'extra-counter'):
            with self.subTest(mode=mode), self.assertRaises(AssertionError):
                self.invoke('console', mode)

    def test_console_partial_or_secondary_cleanup_failure_refused(self):
        for mode in ('cleanup-detail-false', 'secondary-cleanup'):
            with self.subTest(mode=mode), self.assertRaises(AssertionError):
                self.invoke('console', mode)

    def test_reference_spec_action_send_and_audit_failures_refused(self):
        for mode in ('spec-post-false', 'wrong-spec', 'wrong-action', 'missing-send', 'audit-violation'):
            with self.subTest(mode=mode), self.assertRaises(AssertionError):
                self.invoke('reference', mode)

    def test_unbalanced_sdk_transport_counters_refused(self):
        for kind in ('sdk', 'reference'):
            with self.subTest(kind=kind), self.assertRaises(AssertionError):
                self.invoke(kind, 'sdk-unbalanced')

    def test_actual_counter_validator_rejects_bool_zero_and_wrong_shape(self):
        validate = self.namespace['_metrics_exact_counts']
        validate({'one': 1}, {'one': 1})
        for actual in ({'one': True}, {'one': 0}, {'one': 1.0}, {'one': 1, 'extra': 0}, {}, None):
            with self.subTest(actual=actual), self.assertRaises(AssertionError):
                validate(actual, {'one': 1})


if __name__ == '__main__':
    unittest.main(verbosity=2)
