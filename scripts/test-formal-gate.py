"""Small real-filesystem tests of gate failure handling; these do not run TLC."""
import hashlib
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from formal_model_gate import GateError, apply_mutant, load_registry, run_suite, validate_result

ROOT = Path(__file__).resolve().parents[1]


class FormalGateTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='formal-gate-unit-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        shutil.copytree(ROOT / 'formal', self.root / 'formal')
        registry = json.loads((ROOT / 'formal/models.json').read_text())
        for path in {p for m in registry['models'] for p in m['source_scope']}:
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, target)

    def test_complete_registry_and_exact_mutations(self):
        registry = load_registry(self.root)
        self.assertEqual(len(registry['models']), 5)
        for model in registry['models']:
            source = (self.root / model['model']).read_text()
            for mutant in model['mutants']:
                name, mutated = apply_mutant(source, model['id'], mutant)
                self.assertNotEqual(mutated, source)
                self.assertIn('MODULE ' + name, mutated)

    def test_missing_model_config_checksum_each_fail(self):
        for path in ('formal/ObjectStore.tla', 'formal/ObjectStore.cfg', 'formal/tla2tools.sha256'):
            with self.subTest(path=path):
                original = (self.root / path).read_bytes()
                (self.root / path).unlink()
                with self.assertRaises(GateError):
                    load_registry(self.root)
                (self.root / path).write_bytes(original)

    def test_unregistered_or_silently_removed_model_fails(self):
        orphan = self.root / 'formal/Unknown.tla'
        orphan.write_text('---- MODULE Unknown ----\n====')
        with self.assertRaises(GateError):
            load_registry(self.root)
        orphan.unlink()
        path = self.root / 'formal/models.json'
        registry = json.loads(path.read_text())
        registry['models'].pop()
        path.write_text(json.dumps(registry))
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_missing_invariant_or_stale_mutation_cannot_pass(self):
        config = self.root / 'formal/ObjectStore.cfg'
        config.write_text(config.read_text().replace('INVARIANTS TypeOK NoOrphans NoUnauthorizedMutation DenyWins', 'INVARIANT TypeOK'))
        with self.assertRaises(GateError):
            load_registry(self.root)
        shutil.copyfile(ROOT / 'formal/ObjectStore.cfg', config)
        source = self.root / 'formal/ObjectStore.tla'
        source.write_text(source.read_text().replace('ObjectsFor(b) = {}', 'ObjectsFor(b) # {k1}'))
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_actual_exit_class_and_intended_invariant_required(self):
        validate_result(0, 'No error has been found.')
        validate_result(12, 'Invariant NoOrphans is violated.', 'NoOrphans')
        for code, text, expected in [(12, 'Invariant NoOrphans is violated.', None),
                                     (0, 'No error has been found.', 'NoOrphans'),
                                     (150, 'Parsing failed', 'NoOrphans'),
                                     (137, 'Invariant NoOrphans is violated.', 'NoOrphans'),
                                     (12, 'Invariant TypeOK is violated.', 'NoOrphans'),
                                     (0, 'Tool stopped without completing', None)]:
            with self.subTest(code=code, expected=expected), self.assertRaises(GateError):
                validate_result(code, text, expected)

    def test_missing_jar_checksum_and_java_fail_before_execution(self):
        reports = self.root / 'reports'
        with patch('formal_model_gate.subprocess.run') as process:
            with self.assertRaises(GateError):
                run_suite(self.root, self.root / 'absent.jar', reports)
            wrong = self.root / 'wrong.jar'
            wrong.write_bytes(b'not-the-pinned-tool')
            with self.assertRaises(GateError):
                run_suite(self.root, wrong, reports)
            process.assert_not_called()
        # Use a controlled fixture digest only to isolate Java absence; this is
        # not TLC/tool evidence and is never written into the repository assets.
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(wrong.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        with patch('formal_model_gate.shutil.which', return_value=None), patch('formal_model_gate.subprocess.run') as process:
            with self.assertRaises(GateError):
                run_suite(self.root, wrong, reports)
            process.assert_not_called()
        self.assertFalse(json.loads((reports / 'evidence.json').read_text())['success'])

    def test_failed_or_timed_out_process_is_not_a_counterexample(self):
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'controlled process-classification fixture only')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        bad = subprocess.CompletedProcess([], 150, 'Parsing failed', '')
        for output in (bad, subprocess.TimeoutExpired('java', 1)):
            with patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.subprocess.run', **({'return_value': output} if isinstance(output, subprocess.CompletedProcess) else {'side_effect': output})):
                with self.assertRaises(GateError):
                    run_suite(self.root, jar, self.root / 'reports')
                self.assertFalse(json.loads((self.root / 'reports/evidence.json').read_text())['success'])


if __name__ == '__main__':
    unittest.main()
