"""Small real-filesystem tests of gate failure handling; these do not run TLC."""
import contextlib
import io
import hashlib
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from formal_model_gate import GateError, apply_mutant, load_registry, run_suite, validate_result, invariants

ROOT = Path(__file__).resolve().parents[1]


class FormalGateTests(unittest.TestCase):
    def setUp(self):
        self.output_capture = contextlib.redirect_stdout(io.StringIO())
        self.output_capture.__enter__()
        self.addCleanup(self.output_capture.__exit__, None, None, None)
        self.temporary = tempfile.TemporaryDirectory(prefix='formal-gate-unit-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        shutil.copytree(ROOT / 'formal', self.root / 'formal')
        for path in ('go.mod', 'go.sum'):
            shutil.copyfile(ROOT / path, self.root / path)
        registry = json.loads((ROOT / 'formal/models.json').read_text())
        for path in {p for m in registry['models'] for p in m['source_scope']}:
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, target)

    def test_complete_registry_and_exact_mutations(self):
        registry = load_registry(self.root)
        self.assertEqual(len(registry['models']), 6)
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

    def test_cfg_view_constraints_and_unknown_keywords_are_not_invariants(self):
        for directive in ('VIEW V', 'CONSTRAINT Bound', 'CONSTRAINTS Bound', 'ACTION_CONSTRAINT Bound', 'ACTION_CONSTRAINTS Bound', 'TYPE T', 'TYPE_CONSTRAINT T', 'SYMMETRY Permutations'):
            with self.subTest(directive=directive), self.assertRaises(GateError):
                invariants('INIT Init\nNEXT Next\nINVARIANT Safe\n' + directive + '\n')
        with self.assertRaises(GateError):
            invariants('CONSTANT X = {a,b} VIEW V\nINVARIANT Safe')
        with self.assertRaises(GateError):
            invariants('INVARIANTS TypeOK Safe VIEW V')

    def test_named_existing_safety_obligation_cannot_disappear(self):
        config = self.root / 'formal/ObjectStore.cfg'
        config.write_text(config.read_text().replace(' NoUnauthorizedMutation', ''))
        path = self.root / 'formal/models.json'
        registry = json.loads(path.read_text())
        registry['models'][0]['properties'].remove('NoUnauthorizedMutation')
        path.write_text(json.dumps(registry))
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_merge_mode_uses_head_snapshots_and_rejects_changes_during_run(self):
        # Controlled Java results exercise input protection only. This test does
        # not use the official tool or claim that a model was actually checked.
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'unit classification fixture')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        (self.root / '.gitignore').write_text('reports/\n')
        for command in (['git','init','-q'], ['git','config','user.name','Fixture'], ['git','config','user.email','fixture@example.invalid'], ['git','add','.'], ['git','commit','-q','-m','controlled immutable tree']):
            subprocess.run(command, cwd=self.root, check=True, capture_output=True)
        registry = load_registry(self.root)
        expected = {m['id'] + '_' + mutation['id']: mutation['expect_invariant'] for m in registry['models'] for mutation in m['mutants']}
        real_run = subprocess.run
        tamper = [False]
        def controlled(command, **kwargs):
            if command[0] != '/fixture/java':
                return real_run(command, **kwargs)
            self.assertEqual(Path(command[2]).read_bytes(), jar.read_bytes())
            name = Path(command[-1]).stem
            if tamper[0] and name == 'ObjectStore':
                source = self.root / 'formal/ObjectStore.tla'
                source.write_text(source.read_text() + '\nchanged while process was running\n')
            if name in expected:
                return subprocess.CompletedProcess(command, 12, 'Invariant ' + expected[name] + ' is violated.', '')
            return subprocess.CompletedProcess(command, 0, 'No error has been found.', '')
        with patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.subprocess.run', side_effect=controlled):
            result = run_suite(self.root, jar, self.root / 'reports/formal', merge_gate=True)
            self.assertEqual(result['input_mode'], 'immutable-head')
            self.assertTrue(result['success'])
            tamper[0] = True
            with self.assertRaises(GateError):
                run_suite(self.root, jar, self.root / 'reports/formal', merge_gate=True)
        self.assertFalse(json.loads((self.root / 'reports/formal/evidence.json').read_text())['success'])



    def test_browser_session_model_and_revocation_obligation_are_required(self):
        path = self.root / 'formal/models.json'
        registry = json.loads(path.read_text())
        browser = next(m for m in registry['models'] if m['id'] == 'BrowserSessions')
        browser['properties'].remove('RevokedCookiesDenied')
        config = self.root / browser['config']
        config.write_text(config.read_text().replace('INVARIANT RevokedCookiesDenied\n', ''))
        path.write_text(json.dumps(registry))
        with self.assertRaises(GateError):
            load_registry(self.root)


if __name__ == '__main__':
    unittest.main()
