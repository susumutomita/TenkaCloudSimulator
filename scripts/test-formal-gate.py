"""Small real-filesystem tests of gate failure handling; these do not run TLC."""
import ast
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

    def test_sts_registry_guards_and_source_scope_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        baseline = json.loads(original)
        entry = next(m for m in baseline['models'] if m['id'] == 'STSSessionLifecycle')
        self.assertEqual(set(entry['properties']), {'TypeOK', 'IssueRequiresAllGuards', 'UseRequiresAllGuards'})
        self.assertEqual({m['id'] for m in entry['mutants']}, {'SkipGeneration', 'SkipExpiry', 'SkipSignature', 'SkipTrust', 'SkipExternal', 'SkipPermission', 'SkipTenant', 'SkipChain'})
        model_path, config_path = self.root / entry['model'], self.root / entry['config']
        model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != 'STSSessionLifecycle']
        path.write_text(json.dumps(removed))
        model_path.unlink(); config_path.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model_path.write_bytes(model_bytes); config_path.write_bytes(config_bytes)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == 'STSSessionLifecycle')
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            path.write_text(json.dumps(changed))
            config_path.write_text(config_bytes.decode().replace('INVARIANT ' + prop + '\n', ''))
            with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
        config_path.write_bytes(config_bytes)
        for scope in (entry['source_scope'][1:], entry['source_scope'] + ['internal/core/store.go']):
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == 'STSSessionLifecycle')['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
        path.write_text(original)
        source = model_bytes.decode()
        for mutant in entry['mutants']:
            broken = json.loads(json.dumps(mutant))
            broken['edits'][0]['old'] += 'missing-anchor'
            with self.subTest(mutant=mutant['id']), self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                apply_mutant(source, entry['id'], broken)

    def test_cfn_event_history_inventory_properties_controls_and_scopes_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNEventHistory')
        self.assertEqual(set(entry['properties']), {
            'TypeOK', 'PendingFrameUnchanged', 'FailureAtomic', 'LivePeerPreserved',
            'EventParentExists', 'RetentionBound', 'LatestDeletedRetained',
            'SuccessfulLifecycleExact', 'HistoryTracksLatest', 'CleanupAfterAllWrites',
            'ReadSnapshotUnchanged', 'NoUnauthorizedDisclosure', 'ResolvedGenerationExact',
            'CursorScoped', 'PageExact', 'NextCursorExact'})
        self.assertEqual({m['id'] for m in entry['mutants']}, {
            'PublishPending', 'PublishFailedTransaction', 'SkipEventWriteACK',
            'SkipHistoryWriteACK', 'SkipDeletedCountACK', 'PruneLiveHistory',
            'LeaveOrphanEvents', 'SkipSelectedHistoryACK', 'AcknowledgeBeforeFinalWrites',
            'SkipAuthorization', 'ResolveDeletedByName', 'SkipRegion',
            'SkipCursorGeneration', 'IgnoreCursorBound', 'ReversePageOrder', 'ReturnFirstRowCursor'})
        model_path, config_path = self.root / entry['model'], self.root / entry['config']
        model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        model_path.unlink(); config_path.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model_path.write_bytes(model_bytes); config_path.write_bytes(config_bytes)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            path.write_text(json.dumps(changed))
            config_path.write_text(config_bytes.decode().replace('INVARIANT ' + prop + '\n', ''))
            with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
        config_path.write_bytes(config_bytes)
        for control in entry['mutants']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['mutants'] = [m for m in target['mutants'] if m['id'] != control['id']]
            path.write_text(json.dumps(changed))
            with self.subTest(control=control['id']), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                load_registry(self.root)
        for scope in ([p for p in entry['source_scope'] if p != removed] for removed in entry['source_scope']):
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == entry['id'])['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
        changed = json.loads(original)
        next(m for m in changed['models'] if m['id'] == entry['id'])['source_scope'].append('internal/core/store.go')
        path.write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        path.write_text(original)

    def test_cfn_event_controls_preserve_independent_oracles_and_exact_anchors(self):
        registry = load_registry(self.root)
        entry = next(m for m in registry['models'] if m['id'] == 'CFNEventHistory')
        source = (self.root / entry['model']).read_text()
        oracle = source[source.index('FrameOK(s) =='):]
        for control in entry['mutants']:
            _, mutated = apply_mutant(source, entry['id'], control)
            self.assertEqual(mutated[mutated.index('FrameOK(s) =='):], oracle)
            broken = json.loads(json.dumps(control))
            broken['edits'][0]['old'] += 'missing-anchor'
            with self.subTest(control=control['id']), self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                apply_mutant(source, entry['id'], broken)

    def test_cfn_parameter_snapshot_required_inventory(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            registry = json.loads(original)
            entry = next(m for m in registry['models'] if m['id'] == 'CFNParameterSnapshot')
            for value in list(entry[field]):
                candidate = json.loads(original)
                changed = next(m for m in candidate['models'] if m['id'] == 'CFNParameterSnapshot')
                changed[field].remove(value)
                path.write_text(json.dumps(candidate))
                try:
                    with self.subTest(field=field, value=value), self.assertRaises(GateError):
                        load_registry(self.root)
                finally:
                    path.write_text(original)
        registry = json.loads(original)
        registry['models'] = [m for m in registry['models'] if m['id'] != 'CFNParameterSnapshot']
        path.write_text(json.dumps(registry))
        try:
            with self.assertRaises(GateError):
                load_registry(self.root)
        finally:
            path.write_text(original)

    def test_complete_registry_and_exact_mutations(self):
        registry = load_registry(self.root)
        self.assertEqual({model['id'] for model in registry['models']}, {'CFNParameterSnapshot', 'CFNPhysicalResourceACK', 'CFNConditionAdmission', 'CFNConditionMemoDepth', 'CFNFinalCleanupACK', 'SQSStorageBudget', 'SecretsRevealEpoch', 'AzureBlobState', 'ProjectAuthority', 'RESTRouteMatching', 'KMSAliasCatalog', 'SakuraDiskMetadata', 'SQSConsoleEpoch', 'ProviderActor', 'DynamoDBAdmission', 'IAMManagedPolicies', 'AzurePropertiesTarget', 'StartupTokenVisibility', 'RESTDeploymentSnapshot', 'KMSProtocolAdmission', 'KMSAliasesViewEpoch', 'NativeIAMOwnership', 'DynamoDBConsoleEpoch', 'SSMParameterState', 'AzurePropertiesEpoch', 'SecretMetadataViewEpoch', 'DynamoDBLifecycle', 'SakuraDiskAdmission', 'CloudWatchLogsLifecycle', 'CloudWatchLogsViewEpoch', 'BrowserSessions', 'SecretVersionListing', 'SakuraDisksViewEpoch', 'KMSAliasCrypto', 'StackOwnership', 'SecretsDisclosureChannels', 'RESTProxyEventBoundary', 'NativeWireProjection', 'SQSLifecycle', 'BlobRange', 'KMSBootstrapTransaction', 'SecretsVersionLifecycle', 'SNSAdmission', 'SNSLifecycle', 'ObjectStore', 'LandingAccessAdmission', 'LandingTopologyState', 'GCPStorageState', 'KMSKeyState', 'SakuraStartupAdmission', 'S3Versions', 'ProviderPath', 'SecretsMutationRefresh', 'CatalogResourceLifecycle', 'CatalogConsoleEpoch', 'LoginInputIntent', 'STSSessionLifecycle', 'CFNEventHistory'})
        for model in registry['models']:
            source = (self.root / model['model']).read_text()
            for mutant in model['mutants']:
                name, mutated = apply_mutant(source, model['id'], mutant)
                self.assertNotEqual(mutated, source)
                self.assertIn('MODULE ' + name, mutated)

    def test_sakura_metadata_admission_and_startup_inventory_and_properties_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('SakuraDiskMetadata', 'SakuraDiskAdmission', 'SakuraStartupAdmission'):
            registry = json.loads(original)
            entry = next(m for m in registry['models'] if m['id'] == name)
            model_path, config_path = self.root / entry['model'], self.root / entry['config']
            model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
            registry['models'] = [m for m in registry['models'] if m['id'] != name]
            path.write_text(json.dumps(registry))
            model_path.unlink(); config_path.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            model_path.write_bytes(model_bytes); config_path.write_bytes(config_bytes)
            for property_name in entry['properties']:
                registry = json.loads(original)
                changed = next(m for m in registry['models'] if m['id'] == name)
                changed['properties'].remove(property_name)
                changed['mutants'] = [m for m in changed['mutants'] if m['expect_invariant'] != property_name]
                path.write_text(json.dumps(registry))
                config_path.write_text(config_bytes.decode().replace('INVARIANT '+property_name+'\n', ''))
                with self.subTest(model=name, invariant=property_name):
                    with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                        load_registry(self.root)
                config_path.write_bytes(config_bytes)
            path.write_text(original)

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

    def test_timeout_retains_exact_partial_bytes_without_success_or_command(self):
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'controlled diagnostic fixture, not TLC')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        stdout, stderr = b'partial-progress\x00\xff', b'partial-error\xfe'
        error = subprocess.TimeoutExpired('PRIVATE_COMMAND_SENTINEL', 60, output=stdout, stderr=stderr)
        reports = self.root / 'timeout-reports'
        with patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.time.monotonic', side_effect=[100.0, 162.5]), patch('formal_model_gate.subprocess.run', side_effect=error) as process:
            with self.assertRaisesRegex(GateError, 'TLC could not finish: ObjectStore') as raised:
                run_suite(self.root, jar, reports)
        self.assertIs(raised.exception.__cause__.__cause__, error)
        process.assert_called_once()
        evidence = json.loads((reports / 'evidence.json').read_text())
        self.assertFalse(evidence['success'])
        self.assertEqual(evidence['runs'], [])
        failure = evidence['failure']
        self.assertFalse(failure['success'])
        self.assertEqual((failure['exception_type'], failure['errno'], failure['timeout_seconds']), ('TimeoutExpired', None, 60))
        self.assertEqual((failure['deadline_seconds'], failure['elapsed_seconds']), (60, 62.5))
        self.assertIsNone(failure['exit_code'])
        self.assertEqual(failure['exit_code_status'], 'unavailable_from_process_exception')
        self.assertEqual(process.call_args.kwargs['timeout'], 60)
        self.assertEqual(json.loads((reports / 'ObjectStore.failure.json').read_text()), failure)
        for channel, raw in [('stdout', stdout), ('stderr', stderr)]:
            stream = failure[channel]
            self.assertEqual((reports / stream['path']).read_bytes(), raw)
            self.assertEqual(stream['sha256'], hashlib.sha256(raw).hexdigest())
            self.assertEqual(stream['bytes'], len(raw))
            self.assertTrue(stream['available'])
            self.assertEqual(stream['source_type'], 'bytes')
        self.assertNotIn('PRIVATE_COMMAND_SENTINEL', (reports / 'evidence.json').read_text())

    def test_oserror_retains_errno_and_available_streams_without_message(self):
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'controlled diagnostic fixture, not TLC')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        error = OSError(28, 'PRIVATE_ERROR_SENTINEL')
        error.stdout = 'partial unicode progress \u2603'
        error.stderr = b'partial-oserror\x80'
        reports = self.root / 'oserror-reports'
        with patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.time.monotonic', side_effect=[100.0, 162.5]), patch('formal_model_gate.subprocess.run', side_effect=error) as process:
            with self.assertRaisesRegex(GateError, 'TLC could not finish: ObjectStore'):
                run_suite(self.root, jar, reports)
        process.assert_called_once()
        evidence = json.loads((reports / 'evidence.json').read_text())
        self.assertFalse(evidence['success'])
        self.assertEqual(evidence['runs'], [])
        failure = evidence['failure']
        self.assertEqual((failure['exception_type'], failure['errno'], failure['timeout_seconds']), ('OSError', 28, None))
        self.assertEqual((failure['deadline_seconds'], failure['elapsed_seconds']), (60, 62.5))
        self.assertIsNone(failure['exit_code'])
        self.assertEqual(failure['exit_code_status'], 'unavailable_from_process_exception')
        self.assertEqual(process.call_args.kwargs['timeout'], 60)
        self.assertEqual((reports / failure['stdout']['path']).read_bytes(), error.stdout.encode())
        self.assertEqual((reports / failure['stderr']['path']).read_bytes(), error.stderr)
        self.assertNotIn('PRIVATE_ERROR_SENTINEL', (reports / 'evidence.json').read_text())
        self.assertFalse(failure['success'])

    def test_diagnostic_write_failure_does_not_mask_primary_process_error(self):
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'controlled diagnostic fixture, not TLC')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        error = subprocess.TimeoutExpired('PRIVATE_COMMAND_SENTINEL', 60, output=b'partial stdout', stderr=b'partial stderr')
        original_write = Path.write_bytes
        def refuse_stdout(path, value):
            if path.name.endswith('.failure.stdout.bin'):
                raise OSError(28, 'PRIVATE_DIAGNOSTIC_SENTINEL')
            return original_write(path, value)
        reports = self.root / 'write-failure-reports'
        with patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.subprocess.run', side_effect=error), patch.object(Path, 'write_bytes', refuse_stdout):
            with self.assertRaisesRegex(GateError, 'TLC could not finish: ObjectStore') as raised:
                run_suite(self.root, jar, reports)
        self.assertIs(raised.exception.__cause__.__cause__, error)
        evidence = json.loads((reports / 'evidence.json').read_text())
        self.assertFalse(evidence['success'])
        self.assertEqual(evidence['runs'], [])
        failure = evidence['failure']
        self.assertEqual(failure['exception_type'], 'TimeoutExpired')
        self.assertFalse(failure['stdout']['retained'])
        self.assertEqual(failure['stdout']['write_errno'], 28)
        self.assertTrue(failure['stderr']['retained'])
        self.assertEqual((reports / failure['stderr']['path']).read_bytes(), b'partial stderr')
        self.assertNotIn('PRIVATE_DIAGNOSTIC_SENTINEL', (reports / 'evidence.json').read_text())

    def test_diagnostic_metadata_write_failure_keeps_primary_error_in_evidence(self):
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'controlled metadata failure fixture, not TLC')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        error = FileNotFoundError(2, 'PRIVATE_PROCESS_MESSAGE')
        original_write = Path.write_text
        def refuse_metadata(path, value, *args, **kwargs):
            if path.name.endswith('.failure.json'):
                raise OSError(28, 'PRIVATE_METADATA_MESSAGE')
            return original_write(path, value, *args, **kwargs)
        reports = self.root / 'metadata-failure-reports'
        with patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.subprocess.run', side_effect=error), patch('formal_model_gate.time.monotonic', side_effect=[10.0, 10.125]), patch.object(Path, 'write_text', refuse_metadata):
            with self.assertRaisesRegex(GateError, 'TLC could not finish: ObjectStore') as raised:
                run_suite(self.root, jar, reports)
        self.assertIs(raised.exception.__cause__.__cause__, error)
        evidence = json.loads((reports / 'evidence.json').read_text())
        failure = evidence['failure']
        self.assertFalse(evidence['success'])
        self.assertEqual(evidence['runs'], [])
        self.assertEqual((failure['exception_type'], failure['errno'], failure['elapsed_seconds']), ('FileNotFoundError', 2, 0.125))
        self.assertEqual((failure['metadata_write_error_type'], failure['metadata_write_errno']), ('OSError', 28))
        for channel in ('stdout', 'stderr'):
            self.assertFalse(failure[channel]['available'])
            self.assertEqual(failure[channel]['bytes'], 0)
            self.assertTrue(failure[channel]['retained'])
        self.assertNotIn('PRIVATE_PROCESS_MESSAGE', (reports / 'evidence.json').read_text())
        self.assertNotIn('PRIVATE_METADATA_MESSAGE', (reports / 'evidence.json').read_text())

    def test_landing_models_properties_and_exact_sources_cannot_be_removed(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        baseline = json.loads(original)
        for name in ('LandingAccessAdmission', 'LandingTopologyState'):
            entry = next(m for m in baseline['models'] if m['id'] == name)
            model_path = self.root / entry['model']
            config_path = self.root / entry['config']
            model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
            changed = json.loads(original)
            changed['models'] = [m for m in changed['models'] if m['id'] != name]
            path.write_text(json.dumps(changed))
            model_path.unlink(); config_path.unlink()
            with self.subTest(model=name, removal='model'), self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            model_path.write_bytes(model_bytes); config_path.write_bytes(config_bytes)
            for prop in entry['properties']:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == name)
                target['properties'].remove(prop)
                target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
                lines = config_bytes.decode().splitlines(keepends=True)
                removed = 0
                for index, line in enumerate(lines):
                    words = line.split()
                    if words and words[0] in ('INVARIANT', 'INVARIANTS') and prop in words[1:]:
                        words.remove(prop); removed += 1
                        lines[index] = ' '.join(words) + '\n' if len(words) > 1 else ''
                self.assertEqual(removed, 1)
                changed_config = ''.join(lines)
                self.assertEqual(set(invariants(changed_config, name)), set(target['properties']))
                config_path.write_text(changed_config)
                path.write_text(json.dumps(changed))
                with self.subTest(model=name, property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
                config_path.write_bytes(config_bytes)
            for source in entry['source_scope']:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == name)
                target['source_scope'].remove(source)
                path.write_text(json.dumps(changed))
                with self.subTest(model=name, source=source), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
            path.write_text(original)

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
        for name in ('BlobRange', 'ProviderPath', 'AzurePropertiesEpoch', 'AzurePropertiesTarget'):
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
        self.assertEqual(sum(map(len, REQUIRED_MUTANTS.values())), sum(len(m['mutants']) for m in registry['models']))
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

    def test_every_required_control_is_protected_against_single_removal(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        self.assertEqual(sum(len(v) for v in REQUIRED_MUTANTS.values()), sum(len(m['mutants']) for m in json.loads(original)['models']))
        for model_id, controls in REQUIRED_MUTANTS.items():
            for control in controls:
                with self.subTest(model=model_id, control=control):
                    registry = json.loads(original)
                    model = next(m for m in registry['models'] if m['id'] == model_id)
                    model['mutants'] = [m for m in model['mutants'] if m['id'] != control]
                    path.write_text(json.dumps(registry))
                    with self.assertRaises(GateError):
                        load_registry(self.root)
        path.write_text(original)

    def test_sqs_view_required_model_and_each_property_are_protected(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        registry = json.loads(original)
        registry['models'] = [m for m in registry['models'] if m['id'] != 'SQSConsoleEpoch']
        path.write_text(json.dumps(registry))
        with self.assertRaises(GateError):
            load_registry(self.root)
        path.write_text(original)
        source = json.loads(original)
        model = next(m for m in source['models'] if m['id'] == 'SQSConsoleEpoch')
        cfg_path = self.root / model['config']
        cfg_original = cfg_path.read_text()
        for prop in model['properties']:
            with self.subTest(property=prop):
                registry = json.loads(original)
                candidate = next(m for m in registry['models'] if m['id'] == 'SQSConsoleEpoch')
                candidate['properties'].remove(prop)
                # Remove both declarations to test the literal minimum invariant
                # guard, rather than only registry/CFG mismatch detection.
                cfg_path.write_text(cfg_original.replace(' ' + prop, ''))
                path.write_text(json.dumps(registry))
                with self.assertRaises(GateError):
                    load_registry(self.root)
        cfg_path.write_text(cfg_original)
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

    def test_secrets_required_models_and_properties_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for mid in ('SecretsVersionLifecycle', 'SecretsRevealEpoch', 'SecretsDisclosureChannels'):
            with self.subTest(model=mid):
                registry = json.loads(original)
                registry['models'] = [m for m in registry['models'] if m['id'] != mid]
                path.write_text(json.dumps(registry))
                with self.assertRaises(GateError):
                    load_registry(self.root)
                registry = json.loads(original)
                model = next(m for m in registry['models'] if m['id'] == mid)
                model['properties'].remove(next(p for p in model['properties'] if p != 'TypeOK'))
                path.write_text(json.dumps(registry))
                with self.assertRaises(GateError):
                    load_registry(self.root)
        path.write_text(original)

    def test_dynamodb_models_properties_and_all_controls_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('DynamoDBLifecycle', 'DynamoDBAdmission'):
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

    def test_test_methods_cannot_silently_shadow_each_other(self):
        tree = ast.parse(Path(__file__).read_text())
        for node in ast.walk(tree):
            if isinstance(node, ast.ClassDef):
                names = [item.name for item in node.body
                         if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef))
                         and item.name.startswith('test_')]
                self.assertEqual(len(names), len(set(names)),
                                 'duplicate test methods in ' + node.name)

    def test_mutation_refresh_model_and_each_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        registry = json.loads(original)
        model = next(m for m in registry['models'] if m['id'] == 'SecretsMutationRefresh')
        cfg = self.root / model['config']
        original_cfg = cfg.read_text()
        registry['models'] = [m for m in registry['models'] if m['id'] != model['id']]
        path.write_text(json.dumps(registry))
        with self.assertRaisesRegex(GateError, 'required existing model'):
            load_registry(self.root)
        for prop in model['properties']:
            with self.subTest(property=prop):
                candidate = json.loads(original)
                target = next(m for m in candidate['models'] if m['id'] == model['id'])
                target['properties'].remove(prop)
                cfg.write_text('\n'.join(line for line in original_cfg.splitlines() if line != 'INVARIANT ' + prop) + '\n')
                path.write_text(json.dumps(candidate))
                with self.assertRaisesRegex(GateError, 'existing required invariant'):
                    load_registry(self.root)
        cfg.write_text(original_cfg)
        path.write_text(original)


    def test_projection_models_and_each_safety_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('RESTRouteMatching', 'RESTProxyEventBoundary', 'NativeWireProjection'):
            entry = next(m for m in json.loads(original)['models'] if m['id'] == name)
            tla, cfg = self.root / entry['model'], self.root / entry['config']
            source, config = tla.read_bytes(), cfg.read_bytes()
            missing = json.loads(original)
            missing['models'] = [m for m in missing['models'] if m['id'] != name]
            path.write_text(json.dumps(missing)); tla.unlink(); cfg.unlink()
            with self.subTest(model=name, missing='entry-and-artifacts'):
                with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                    load_registry(self.root)
            tla.write_bytes(source); cfg.write_bytes(config)
            for prop in entry['properties']:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == name)
                target['properties'].remove(prop)
                target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
                cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                         if line != 'INVARIANT '+prop) + '\n')
                path.write_text(json.dumps(changed))
                with self.subTest(model=name, missing=prop):
                    with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                        load_registry(self.root)
            cfg.write_bytes(config); path.write_text(original)

    def test_projection_source_bindings_cannot_be_dropped_or_reassigned(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('RESTRouteMatching', 'RESTProxyEventBoundary', 'NativeWireProjection'):
            for scope in ([], ['internal/aws/iam_roles.go'],
                          ['internal/aws/apirouting/path.go', 'internal/aws/apirouting/event.go']):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == name)
                target['source_scope'] = scope
                path.write_text(json.dumps(changed))
                with self.subTest(model=name, scope=scope):
                    with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                        load_registry(self.root)
        path.write_text(original)

    def test_projection_controls_need_exact_anchors_and_named_semantic_results(self):
        registry = load_registry(self.root)
        for name in ('RESTRouteMatching', 'RESTProxyEventBoundary', 'NativeWireProjection'):
            model = next(m for m in registry['models'] if m['id'] == name)
            source = (self.root / model['model']).read_text()
            for control in model['mutants']:
                with self.subTest(model=name, control=control['id']):
                    broken = json.loads(json.dumps(control))
                    broken['edits'][0]['old'] += ' stale anchor'
                    with self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                        apply_mutant(source, name, broken)
                    expected = control['expect_invariant']
                    validate_result(12, 'Invariant '+expected+' is violated.', expected)
                    for code, output in ((12, 'Invariant TypeOK is violated.'),
                                         (0, 'No error has been found.'),
                                         (1, 'Semantic errors: fixture')):
                        with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                            validate_result(code, output, expected)

    def test_secret_version_listing_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretVersionListing')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)

    def test_secret_version_listing_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretVersionListing')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)

    def test_secret_version_listing_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretVersionListing')
        for scope in ([], ['internal/aws/secret_http.go'], ['internal/server/server.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)
    def test_sakura_disks_view_epoch_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SakuraDisksViewEpoch')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)

    def test_sakura_disks_view_epoch_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SakuraDisksViewEpoch')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)

    def test_sakura_disks_view_epoch_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SakuraDisksViewEpoch')
        for scope in ([], ['tools/console/src/main.jsx'], ['tools/console/src/sakura-disks.jsx', 'tools/console/src/main.jsx'], ['internal/server/server.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)

    def test_dynamodb_console_inventory_and_artifacts_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'DynamoDBConsoleEpoch')
        model, config = self.root / entry['model'], self.root / entry['config']
        for artifact in (model, config):
            saved = artifact.read_bytes()
            artifact.unlink()
            with self.subTest(missing=artifact.name), self.assertRaises(GateError):
                load_registry(self.root)
            artifact.write_bytes(saved)
        model_bytes, config_bytes = model.read_bytes(), config.read_bytes()
        registry = json.loads(original)
        registry['models'] = [m for m in registry['models'] if m['id'] != 'DynamoDBConsoleEpoch']
        path.write_text(json.dumps(registry)); model.unlink(); config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model.write_bytes(model_bytes); config.write_bytes(config_bytes); path.write_text(original)


    def test_dynamodb_console_all_properties_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        properties = {'AckBelongsToCurrentView', 'TypeOK', 'AckClosesDraft', 'CancelNoOrphanLoading', 'ReadRetryNeverWrites', 'DisplayedKeyBound', 'ReadGenerationVerified', 'LocalePreservesDraft', 'NoStalePublication', 'SingleSendBeforeACK'}
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'DynamoDBConsoleEpoch')
        self.assertEqual(set(entry['properties']), properties)
        config = self.root / entry['config']; config_bytes = config.read_bytes()
        for prop in properties:
            registry = json.loads(original)
            target = next(m for m in registry['models'] if m['id'] == 'DynamoDBConsoleEpoch')
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            config.write_text(config_bytes.decode().replace('INVARIANT ' + prop + '\n', ''))
            path.write_text(json.dumps(registry))
            with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
            config.write_bytes(config_bytes)
        path.write_text(original)


    def test_dynamodb_console_each_literal_control_is_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        controls = {'IgnoreReadViewGuard', 'ACKKeepsDraft', 'LocaleClearsDirtyDraft', 'IgnorePostReadGeneration', 'RemoveSynchronousOwnerLock', 'CancelKeepsLoading', 'ACKKeepsForm', 'IgnoreWriteViewGuard', 'RetryRepeatsWrite', 'KeepDisplayedItemOnKeyEdit'}
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'DynamoDBConsoleEpoch')
        self.assertEqual({m['id'] for m in entry['mutants']}, controls)
        for control in controls:
            registry = json.loads(original)
            target = next(m for m in registry['models'] if m['id'] == 'DynamoDBConsoleEpoch')
            target['mutants'] = [m for m in target['mutants'] if m['id'] != control]
            path.write_text(json.dumps(registry))
            with self.subTest(control=control), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                load_registry(self.root)
        path.write_text(original)


    def test_dynamodb_console_source_bindings_cannot_be_descoped(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        scopes = {'tools/console/src/dynamodb.jsx', 'tools/console/src/main.jsx', 'internal/server/web/app.js'}
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'DynamoDBConsoleEpoch')
        self.assertEqual(set(entry['source_scope']), scopes)
        for omitted in scopes:
            registry = json.loads(original)
            target = next(m for m in registry['models'] if m['id'] == 'DynamoDBConsoleEpoch')
            target['source_scope'].remove(omitted)
            path.write_text(json.dumps(registry))
            with self.subTest(scope=omitted), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
        path.write_text(original)


    def test_secret_metadata_view_epoch_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretMetadataViewEpoch')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)

    def test_secret_metadata_view_epoch_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretMetadataViewEpoch')
        for scope in ([], ['internal/aws/secret_http.go'], ['internal/server/server.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)

    def test_secret_metadata_view_epoch_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretMetadataViewEpoch')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)


    def test_alias_models_and_each_required_property_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('KMSAliasCatalog', 'KMSAliasCrypto', 'KMSAliasesViewEpoch'):
            registry = json.loads(original)
            model = next(m for m in registry['models'] if m['id'] == name)
            config_path = self.root / model['config']
            original_config = config_path.read_text()
            for property_name in model['properties']:
                with self.subTest(model=name, property=property_name):
                    changed = json.loads(original)
                    target = next(m for m in changed['models'] if m['id'] == name)
                    target['properties'].remove(property_name)
                    path.write_text(json.dumps(changed))
                    config_path.write_text(original_config.replace('INVARIANT ' + property_name + '\n', ''))
                    with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                        load_registry(self.root)
                    config_path.write_text(original_config)
            registry['models'] = [m for m in registry['models'] if m['id'] != name]
            path.write_text(json.dumps(registry))
            with self.assertRaises(GateError):
                load_registry(self.root)
        path.write_text(original)

    def test_kms_bootstrap_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'KMSBootstrapTransaction')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)

    def test_kms_bootstrap_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'KMSBootstrapTransaction')
        for scope in ([], ['internal/aws/kms_aliases.go'], ['internal/server/server.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)

    def test_kms_bootstrap_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'KMSBootstrapTransaction')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)

    def test_cfn_condition_admission_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNConditionAdmission')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)


    def test_cfn_condition_admission_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNConditionAdmission')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)


    def test_cfn_condition_admission_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNConditionAdmission')
        for scope in ([], ['internal/aws/cloudformation_conditions.go'], ['internal/server/server.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)


    def test_cfn_final_cleanup_ack_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNFinalCleanupACK')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)


    def test_cfn_final_cleanup_ack_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNFinalCleanupACK')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)


    def test_cfn_final_cleanup_ack_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNFinalCleanupACK')
        for scope in ([], ['internal/aws/cloudformation.go'], ['internal/server/server.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)


    def test_cfn_physical_resource_ack_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNPhysicalResourceACK')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)


    def test_cfn_physical_resource_ack_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNPhysicalResourceACK')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)


    def test_cfn_physical_resource_ack_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNPhysicalResourceACK')
        for scope in ([], ['internal/aws/cloudformation.go'], ['internal/server/server.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)


    def test_cfn_condition_memo_depth_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNConditionMemoDepth')
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            cfg.write_text('\n'.join(line for line in config.decode().splitlines()
                                     if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop):
                with self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
        cfg.write_bytes(config); path.write_text(original)


    def test_cfn_condition_memo_depth_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNConditionMemoDepth')
        for control in entry['mutants']:
            for fault in ('remove', 'noop', 'stale', 'wrong-invariant'):
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                candidate = next(m for m in target['mutants'] if m['id'] == control['id'])
                if fault == 'remove':
                    target['mutants'].remove(candidate)
                    error = 'required mutation control was removed'
                elif fault == 'noop':
                    candidate['edits'][0]['new'] = candidate['edits'][0]['old']
                    error = 'mutation must change a nonempty exact anchor'
                elif fault == 'stale':
                    candidate['edits'][0]['old'] += ' absent anchor'
                    error = 'mutation anchor count mismatch'
                else:
                    candidate['expect_invariant'] = 'UncheckedPlaceholder'
                    error = 'mutant expects unchecked invariant'
                path.write_text(json.dumps(changed))
                with self.subTest(control=control['id'], fault=fault):
                    with self.assertRaisesRegex(GateError, error):
                        load_registry(self.root)
        path.write_text(original)


    def test_cfn_condition_memo_depth_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNConditionMemoDepth')
        for scope in ([], ['internal/aws/cloudformation.go'], ['internal/server/server.go'], ['internal/aws/cloudformation_conditions.go', 'internal/aws/cloudformation.go']):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope):
                with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        path.write_text(original)
        for control in entry['mutants']:
            expected = control['expect_invariant']
            validate_result(12, 'Invariant '+expected+' is violated.', expected)
            for code, text in ((12, 'Invariant TypeOK is violated.'),
                               (0, 'No error has been found.'),
                               (1, 'Semantic errors: placeholder')):
                with self.subTest(control=control['id'], code=code):
                    with self.assertRaisesRegex(GateError, 'required invariant counterexample'):
                        validate_result(code, text, expected)




    def test_login_input_required_model_properties_and_exact_scope(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'LoginInputIntent')
        self.assertEqual(set(entry['source_scope']), {
            'tools/console/src/login.jsx', 'test/console/login-native-input.test.mjs',
            'test/e2e/login_paste.py', 'test/e2e/login_token_file.py',
            'internal/server/web/app.js'})
        tla, cfg = self.root / entry['model'], self.root / entry['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed)); tla.unlink(); cfg.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        tla.write_bytes(source); cfg.write_bytes(config)
        # Remove each declaration AND its CFG/controls consistently: mandatory
        # properties must refuse this, not merely an incidental CFG mismatch.
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            lines = config.decode().splitlines()
            self.assertEqual(lines.count('INVARIANT '+prop), 1)
            cfg.write_text('\n'.join(line for line in lines if line != 'INVARIANT '+prop)+'\n')
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
        cfg.write_bytes(config)
        # Every exact path, including the compiled-login artifact, is required.
        scopes = [[path for path in entry['source_scope'] if path != removed]
                  for removed in entry['source_scope']]
        scopes += [entry['source_scope']+['tools/console/src/main.jsx'],
                   ['tools/console/src/main.jsx']]
        for scope in scopes:
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == entry['id'])['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
        path.write_text(original)


    def test_login_input_every_control_and_unambiguous_transition_anchor_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'LoginInputIntent')
        self.assertEqual(len(entry['mutants']), 14)
        for control in entry['mutants']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['mutants'] = [m for m in target['mutants'] if m['id'] != control['id']]
            path.write_text(json.dumps(changed))
            with self.subTest(control=control['id']), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                load_registry(self.root)
        # The Ready suffix also occurs in ExpectedReady. Removing the reviewed
        # leading newline must fail whole-source anchor validation, as caught
        # during scoped-run preparation; a prefix-only fixture is insufficient.
        for name in ('SubmitIgnoresBusy', 'SubmitIgnoresFileGate'):
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            control = next(m for m in target['mutants'] if m['id'] == name)
            control['edits'][0]['old'] = control['edits'][0]['old'].lstrip('\n')
            path.write_text(json.dumps(changed))
            with self.subTest(control=name), self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                load_registry(self.root)
        path.write_text(original)


    def test_login_focus_control_preserves_independent_adoption_oracle(self):
        registry = load_registry(self.root)
        entry = next(m for m in registry['models'] if m['id'] == 'LoginInputIntent')
        source = (self.root / entry['model']).read_text()
        control = next(m for m in entry['mutants'] if m['id'] == 'FocusSkipsNativeAdoption')
        self.assertEqual(control['expect_invariant'], 'AdoptedValueExact')
        name, mutated = apply_mutant(source, entry['id'], control)
        self.assertEqual(name, 'LoginInputIntent_FocusSkipsNativeAdoption')
        self.assertIn('Focus(s) == LET adopted == s IN [adopted EXCEPT !.dom = adopted.token]', mutated)
        # Mutation must touch the actual focus transition, not its independent
        # before-state oracle or the unchanged ordinary Sync operation.
        self.assertIn(' [] e.op = "focus" -> Focus(s)', mutated)
        self.assertIn(' [] k = "native-focus-submit" -> <<Event("native","A"),Event("focus","empty"),Event("submit","empty")>>', mutated)
        self.assertIn('Sync(s) == IF s.connected /\\ s.dom # s.token THEN Adopt(s,s.dom) ELSE s', mutated)
        for before_state_oracle in (
            '(Op \\in {"sync","focus","keydown"} /\\ Before.connected /\\ Before.dom # Before.token)',
            'ELSE IF Op \\in {"sync","focus","keydown"} /\\ Before.connected THEN Before.dom',
            '"edit","sync","focus","keydown","flush","submit","fileok","fileerr"',
        ):
            self.assertIn(before_state_oracle, mutated)



    def test_login_keydown_native_clear_control_preserves_independent_oracles(self):
        registry = load_registry(self.root)
        entry = next(m for m in registry['models'] if m['id'] == 'LoginInputIntent')
        old_controls = {'FlushDoesNotAdoptNativeValue', 'LocaleInvalidatesCurrentFile', 'EmptyDOMReusesOldCredential', 'OldPasteSurvivesNewIntent', 'LocaleClearsIntent', 'FocusSkipsNativeAdoption', 'NonemptyDOMUsesControlledState', 'NativeAdoptionSkipsNewIntent', 'FileCompletionIgnoresRevision', 'SubmitIgnoresFileGate', 'SubmitIgnoresBusy', 'UnmountedFileCompletionPublishes', 'CurrentFileErrorDiscarded'}
        self.assertEqual({m['id'] for m in entry['mutants']},
                         old_controls | {'KeyDownSkipsNativeAdoption'})
        source = (self.root / entry['model']).read_text()
        control = next(m for m in entry['mutants'] if m['id'] == 'KeyDownSkipsNativeAdoption')
        self.assertEqual(control['expect_invariant'], 'AdoptedValueExact')
        name, mutated = apply_mutant(source, entry['id'], control)
        self.assertEqual(name, 'LoginInputIntent_KeyDownSkipsNativeAdoption')
        self.assertIn('KeyDown(s) == LET adopted == s IN [adopted EXCEPT !.dom = adopted.token]', mutated)
        self.assertNotIn('KeyDown(s) == LET adopted == Sync(s)', mutated)
        self.assertIn(' [] e.op = "keydown" -> KeyDown(s)', mutated)
        self.assertIn(' [] k = "focused-key-clear-submit" -> <<Event("paste","A"),Event("flush","empty"),Event("native","empty"),Event("keydown","empty"),Event("submit","empty")>>', mutated)
        # The mutant changes the actual key capture, never the independent
        # before-state oracle, ordinary input synchronization or focus guard.
        for unchanged in (
            'Sync(s) == IF s.connected /\\ s.dom # s.token THEN Adopt(s,s.dom) ELSE s',
            'Focus(s) == LET adopted == Sync(s) IN [adopted EXCEPT !.dom = adopted.token]',
            '(Op \\in {"sync","focus","keydown"} /\\ Before.connected /\\ Before.dom # Before.token)',
            'ELSE IF Op \\in {"sync","focus","keydown"} /\\ Before.connected THEN Before.dom',
            '"edit","sync","focus","keydown","flush","submit","fileok","fileerr"',
        ):
            self.assertIn(unchanged, mutated)


if __name__ == '__main__':
    unittest.main()
