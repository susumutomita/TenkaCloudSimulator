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

from formal_model_gate import BASE_MODELS, REQUIRED_MUTANTS, GateError, apply_mutant, load_registry, run_suite, validate_result, invariants, exact_head_inputs

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
        self.assertEqual({model['id'] for model in registry['models']}, {'ObjectStore', 'StackOwnership', 'S3Versions', 'NativeIAMOwnership', 'RESTDeploymentSnapshot', 'SSMParameterState', 'StartupTokenVisibility', 'SQSLifecycle', 'SQSStorageBudget', 'BrowserSessions', 'ProviderActor', 'GCPStorageState', 'AzureBlobState', 'ProjectAuthority', 'BlobRange', 'ProviderPath', 'KMSKeyState', 'KMSProtocolAdmission', 'CloudWatchLogsLifecycle', 'CloudWatchLogsViewEpoch'})
        for model in registry['models']:
            source = (self.root / model['model']).read_text()
            for mutant in model['mutants']:
                name, mutated = apply_mutant(source, model['id'], mutant)
                self.assertNotEqual(mutated, source)
                self.assertIn('MODULE ' + name, mutated)

    def test_startup_required_model_and_properties_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        registry = json.loads(original)
        registry['models'] = [m for m in registry['models'] if m['id'] != 'StartupTokenVisibility']
        path.write_text(json.dumps(registry))
        # Remove the files too: required inventory protection must detect this,
        # rather than relying only on the unregistered-artifact check.
        model = self.root / 'formal/StartupTokenVisibility.tla'
        config = self.root / 'formal/StartupTokenVisibility.cfg'
        model_bytes, config_bytes = model.read_bytes(), config.read_bytes()
        model.unlink(); config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model.write_bytes(model_bytes); config.write_bytes(config_bytes)
        registry = json.loads(original)
        startup = next(m for m in registry['models'] if m['id'] == 'StartupTokenVisibility')
        startup['properties'].remove('HistoryVisibility')
        startup['mutants'] = [m for m in startup['mutants'] if m['expect_invariant'] != 'HistoryVisibility']
        config.write_text(config.read_text().replace('INVARIANT HistoryVisibility\n',''))
        # Updating both declaration and CFG still cannot remove a required rule.
        path.write_text(json.dumps(registry))
        with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
            load_registry(self.root)

    def test_missing_model_config_checksum_each_fail(self):
        for path in ('formal/ObjectStore.tla', 'formal/ObjectStore.cfg',
                     'formal/SSMParameterState.tla', 'formal/SSMParameterState.cfg',
                     'formal/SQSLifecycle.tla', 'formal/SQSLifecycle.cfg',
                     'formal/SQSStorageBudget.tla', 'formal/SQSStorageBudget.cfg',
                     'formal/tla2tools.sha256'):
            with self.subTest(path=path):
                original = (self.root / path).read_bytes()
                (self.root / path).unlink()
                with self.assertRaises(GateError):
                    load_registry(self.root)
                (self.root / path).write_bytes(original)

    def test_ssm_changed_config_cannot_remove_a_required_property(self):
        config = self.root / 'formal/SSMParameterState.cfg'
        config.write_text(config.read_text().replace('INVARIANT ReadIsolation', 'INVARIANT TypeOK'))
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_ssm_changed_cfg_is_not_exact_head_evidence(self):
        for command in (['git','init','-q'], ['git','config','user.name','Fixture'],
                        ['git','config','user.email','fixture@example.invalid'],
                        ['git','add','.'], ['git','commit','-q','-m','immutable SSM fixture']):
            subprocess.run(command, cwd=self.root, check=True, capture_output=True)
        exact_head_inputs(self.root)
        config = self.root / 'formal/SSMParameterState.cfg'
        config.write_text(config.read_text() + '\n\\* Changed after immutable input capture\n')
        with self.assertRaises(GateError):
            exact_head_inputs(self.root)

    def test_ssm_wrong_expected_invariant_is_not_a_sensitivity_pass(self):
        # Result-classification fixture only; actual TLC sensitivity is checked
        # separately. A different valid registered invariant is still wrong.
        registry = load_registry(self.root)
        model = next(m for m in registry['models'] if m['id'] == 'SSMParameterState')
        mutation = next(m for m in model['mutants'] if m['id'] == 'ReadWrongName')
        self.assertEqual(mutation['expect_invariant'], 'ReadIsolation')
        validate_result(12, 'Invariant ReadIsolation is violated.', mutation['expect_invariant'])
        with self.assertRaises(GateError):
            validate_result(12, 'Invariant ReadIsolation is violated.', 'PutVersion')

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


    def test_required_ssm_inventory_and_property_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        registry = json.loads(original)
        registry['models'] = [m for m in registry['models'] if m['id'] != 'SSMParameterState']
        path.write_text(json.dumps(registry))
        model = self.root / 'formal/SSMParameterState.tla'
        config = self.root / 'formal/SSMParameterState.cfg'
        saved_model, saved_config = model.read_bytes(), config.read_bytes()
        model.unlink(); config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model.write_bytes(saved_model); config.write_bytes(saved_config)
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'SSMParameterState')
        property_name = next(p for p in entry['properties'] if p != 'TypeOK')
        entry['properties'].remove(property_name)
        entry['mutants'] = [m for m in entry['mutants'] if m['expect_invariant'] != property_name]
        config.write_text(config.read_text().replace('INVARIANT ' + property_name + '\n', ''))
        path.write_text(json.dumps(registry))
        with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
            load_registry(self.root)

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


    def test_sqs_required_inventory_and_all_properties_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('SQSLifecycle', 'SQSStorageBudget'):
            model = self.root / ('formal/' + name + '.tla')
            config = self.root / ('formal/' + name + '.cfg')
            saved_model, saved_config = model.read_bytes(), config.read_bytes()
            registry = json.loads(original)
            registry['models'] = [m for m in registry['models'] if m['id'] != name]
            path.write_text(json.dumps(registry))
            model.unlink(); config.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            model.write_bytes(saved_model); config.write_bytes(saved_config)
            for prop in next(m for m in json.loads(original)['models'] if m['id'] == name)['properties']:
                with self.subTest(model=name, property=prop):
                    registry = json.loads(original)
                    entry = next(m for m in registry['models'] if m['id'] == name)
                    entry['properties'].remove(prop)
                    entry['mutants'] = [m for m in entry['mutants'] if m['expect_invariant'] != prop]
                    config.write_text(saved_config.decode().replace('INVARIANT ' + prop + '\n', ''))
                    path.write_text(json.dumps(registry))
                    with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                        load_registry(self.root)
            path.write_text(original); config.write_bytes(saved_config)

    def test_actor_model_properties_and_all_six_controls_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        registry = json.loads(original)
        actor = next(m for m in registry['models'] if m['id'] == 'ProviderActor')
        model = self.root / actor['model']
        config = self.root / actor['config']
        model_bytes, config_bytes = model.read_bytes(), config.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != 'ProviderActor']
        path.write_text(json.dumps(removed))
        model.unlink(); config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model.write_bytes(model_bytes); config.write_bytes(config_bytes)
        for name in actor['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == 'ProviderActor')
            target['properties'].remove(name)
            config.write_text(config_bytes.decode().replace(' ' + name, '', 1))
            path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
        config.write_bytes(config_bytes)
        for name in [m['id'] for m in actor['mutants']]:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == 'ProviderActor')
            target['mutants'] = [m for m in target['mutants'] if m['id'] != name]
            path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                load_registry(self.root)
        path.write_text(original)

    def test_gcp_azure_models_properties_and_controls_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('GCPStorageState', 'AzureBlobState'):
            entry = next(m for m in json.loads(original)['models'] if m['id'] == name)
            model = self.root / entry['model']
            config = self.root / entry['config']
            saved_model, saved_config = model.read_bytes(), config.read_bytes()
            with self.subTest(model=name, omission='model-and-artifacts'):
                registry = json.loads(original)
                registry['models'] = [m for m in registry['models'] if m['id'] != name]
                path.write_text(json.dumps(registry))
                model.unlink(); config.unlink()
                with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                    load_registry(self.root)
            model.write_bytes(saved_model); config.write_bytes(saved_config)
            for prop in entry['properties']:
                with self.subTest(model=name, property=prop):
                    registry = json.loads(original)
                    changed = next(m for m in registry['models'] if m['id'] == name)
                    changed['properties'].remove(prop)
                    changed['mutants'] = [m for m in changed['mutants'] if m['expect_invariant'] != prop]
                    config.write_text(saved_config.decode().replace('INVARIANT ' + prop + '\n', ''))
                    path.write_text(json.dumps(registry))
                    with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                        load_registry(self.root)
            config.write_bytes(saved_config)
            for mutant in entry['mutants']:
                with self.subTest(model=name, control=mutant['id']):
                    registry = json.loads(original)
                    changed = next(m for m in registry['models'] if m['id'] == name)
                    changed['mutants'] = [m for m in changed['mutants'] if m['id'] != mutant['id']]
                    path.write_text(json.dumps(registry))
                    with self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                        load_registry(self.root)
            path.write_text(original)

    def test_azure_owner_binding_is_exact_and_never_generic_substitution(self):
        config = (self.root / 'formal/AzureBlobState.cfg').read_text()
        self.assertEqual(set(invariants(config, 'AzureBlobState')),
                         set(next(m for m in load_registry(self.root)['models']
                                  if m['id'] == 'AzureBlobState')['properties']))
        for model in (None, 'ObjectStore', 'GCPStorageState', 'ProviderActor'):
            with self.subTest(model=model), self.assertRaisesRegex(GateError, 'unsupported constant declaration'):
                invariants(config, model)
        for old, new in (('Owners <- AccountOwners', 'Owners <- OtherOwners'),
                         ('Owners <- AccountOwners', 'Other <- AccountOwners'),
                         ('Owners <- AccountOwners', 'Owners <- TypeOK'),
                         ('Owners <- AccountOwners', 'Owners <- AccountOwners VIEW Prune'),
                         ('CONSTANT Owners <- AccountOwners', 'CONSTANT Owners <- AccountOwners\nCONSTANT Owners <- AccountOwners'),
                         ('CONSTANT Owners <- AccountOwners', 'CONSTANT Owners <- AccountOwners\nCONSTANT Other <- AccountOwners')):
            with self.subTest(replacement=new), self.assertRaises(GateError):
                invariants(config.replace(old, new), 'AzureBlobState')
        for extra in ('VIEW Prune', 'CONSTRAINT Prune', 'ACTION_CONSTRAINT Prune',
                      'INVARIANT TypeOK VIEW Prune'):
            with self.subTest(directive=extra), self.assertRaises(GateError):
                invariants(config + '\n' + extra, 'AzureBlobState')
        with self.assertRaisesRegex(GateError, 'Azure finite owner binding is required'):
            invariants(config.replace('CONSTANT Owners <- AccountOwners\n', ''), 'AzureBlobState')

    def test_project_authority_model_properties_and_controls_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('ProjectAuthority',):
            entry = next(m for m in json.loads(original)['models'] if m['id'] == name)
            model = self.root / entry['model']
            config = self.root / entry['config']
            saved_model, saved_config = model.read_bytes(), config.read_bytes()
            with self.subTest(model=name, omission='model-and-artifacts'):
                registry = json.loads(original)
                registry['models'] = [m for m in registry['models'] if m['id'] != name]
                path.write_text(json.dumps(registry))
                model.unlink(); config.unlink()
                with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                    load_registry(self.root)
            model.write_bytes(saved_model); config.write_bytes(saved_config)
            for prop in entry['properties']:
                with self.subTest(model=name, property=prop):
                    registry = json.loads(original)
                    changed = next(m for m in registry['models'] if m['id'] == name)
                    changed['properties'].remove(prop)
                    changed['mutants'] = [m for m in changed['mutants'] if m['expect_invariant'] != prop]
                    config.write_text(saved_config.decode().replace('INVARIANT ' + prop + '\n', ''))
                    path.write_text(json.dumps(registry))
                    with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                        load_registry(self.root)
            config.write_bytes(saved_config)
            for mutant in entry['mutants']:
                with self.subTest(model=name, control=mutant['id']):
                    registry = json.loads(original)
                    changed = next(m for m in registry['models'] if m['id'] == name)
                    changed['mutants'] = [m for m in changed['mutants'] if m['id'] != mutant['id']]
                    path.write_text(json.dumps(registry))
                    with self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                        load_registry(self.root)
            path.write_text(original)

    def test_blob_range_provider_path_models_properties_and_controls_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('BlobRange', 'ProviderPath'):
            entry = next(m for m in json.loads(original)['models'] if m['id'] == name)
            model = self.root / entry['model']
            config = self.root / entry['config']
            saved_model, saved_config = model.read_bytes(), config.read_bytes()
            with self.subTest(model=name, omission='model-and-artifacts'):
                registry = json.loads(original)
                registry['models'] = [m for m in registry['models'] if m['id'] != name]
                path.write_text(json.dumps(registry))
                model.unlink(); config.unlink()
                with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                    load_registry(self.root)
            model.write_bytes(saved_model); config.write_bytes(saved_config)
            for prop in entry['properties']:
                with self.subTest(model=name, property=prop):
                    registry = json.loads(original)
                    changed = next(m for m in registry['models'] if m['id'] == name)
                    changed['properties'].remove(prop)
                    changed['mutants'] = [m for m in changed['mutants'] if m['expect_invariant'] != prop]
                    config.write_text(saved_config.decode().replace('INVARIANT ' + prop + '\n', ''))
                    path.write_text(json.dumps(registry))
                    with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                        load_registry(self.root)
            config.write_bytes(saved_config)
            for mutant in entry['mutants']:
                with self.subTest(model=name, control=mutant['id']):
                    registry = json.loads(original)
                    changed = next(m for m in registry['models'] if m['id'] == name)
                    changed['mutants'] = [m for m in changed['mutants'] if m['id'] != mutant['id']]
                    path.write_text(json.dumps(registry))
                    with self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                        load_registry(self.root)
            path.write_text(original)

    def test_logs_required_models_properties_and_all_seven_controls(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for model_id in ('CloudWatchLogsLifecycle', 'CloudWatchLogsViewEpoch'):
            registry = json.loads(original)
            model = next(m for m in registry['models'] if m['id'] == model_id)
            tla, cfg = self.root / model['model'], self.root / model['config']
            tla_bytes, cfg_bytes = tla.read_bytes(), cfg.read_bytes()
            registry['models'] = [m for m in registry['models'] if m['id'] != model_id]
            path.write_text(json.dumps(registry))
            tla.unlink(); cfg.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            tla.write_bytes(tla_bytes); cfg.write_bytes(cfg_bytes)
            for prop in model['properties']:
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model_id)
                target['properties'].remove(prop)
                cfg.write_text('\n'.join(line for line in cfg_bytes.decode().splitlines() if line != 'INVARIANT ' + prop) + '\n')
                path.write_text(json.dumps(registry))
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
            cfg.write_bytes(cfg_bytes)
            for control in [m['id'] for m in model['mutants']]:
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model_id)
                target['mutants'] = [m for m in target['mutants'] if m['id'] != control]
                path.write_text(json.dumps(registry))
                with self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                    load_registry(self.root)
            path.write_text(original)

    def test_each_established_mutation_control_cannot_be_removed(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        baseline = json.loads(original)
        for model in baseline['models']:
            for mutant in model['mutants']:
                with self.subTest(model=model['id'], mutant=mutant['id']):
                    changed = json.loads(original)
                    entry = next(m for m in changed['models'] if m['id'] == model['id'])
                    entry['mutants'] = [m for m in entry['mutants'] if m['id'] != mutant['id']]
                    path.write_text(json.dumps(changed))
                    with self.assertRaisesRegex(GateError, 'required mutation control was removed|model needs a sensitivity control'):
                        load_registry(self.root)
        path.write_text(original)

    def test_each_required_mutation_control_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        registry = json.loads(original)
        self.assertEqual(set(REQUIRED_MUTANTS), BASE_MODELS)
        self.assertEqual(sum(map(len, REQUIRED_MUTANTS.values())), 101)
        self.assertEqual({m['id']: {c['id'] for c in m['mutants']} for m in registry['models']}, REQUIRED_MUTANTS)
        for model in registry['models']:
            for mutant in model['mutants']:
                with self.subTest(model=model['id'], mutant=mutant['id']):
                    changed = json.loads(original)
                    target = next(m for m in changed['models'] if m['id'] == model['id'])
                    target['mutants'] = [m for m in target['mutants'] if m['id'] != mutant['id']]
                    path.write_text(json.dumps(changed))
                    expected = 'required mutation control was removed' if target['mutants'] else 'model needs a sensitivity control'
                    with self.assertRaisesRegex(GateError, expected):
                        load_registry(self.root)
        path.write_text(original)

if __name__ == '__main__':
    unittest.main()
