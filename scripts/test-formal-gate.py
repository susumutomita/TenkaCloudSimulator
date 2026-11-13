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

from formal_model_gate import validate_guesttcp_module_pins, require, BASE_MODELS, REQUIRED_MUTANTS, GateError, apply_mutant, load_registry, run_suite, validate_result, invariants, exact_head_inputs

from formal_seed_replay import SPECS, prepare_seed_replays, run_seed_replays

ROOT = Path(__file__).resolve().parents[1]


NIC_PROPERTIES = ['TypeOK', 'TupleTombstones', 'ImmutableTuples', 'LiveAccounting', 'DrainRequiresJoins', 'SingleDelivery', 'RevocationRetainsDebt', 'NoClosedAdmission', 'BudgetMonotone', 'ChargedAttempts', 'TupleDirectionGuard', 'IncomingIsolation', 'ClosedQueuesEmpty', 'LateSuccessRejected', 'RewrapPreservesState', 'EffectNotCleanupProof']
NIC_MUTANTS = ['ReuseFirstTuple', 'ForgetLifetimeAllocation', 'ReleaseWithoutWatcher', 'ReleaseWithPendingCallback', 'ReleaseWithPendingRead', 'ReleaseLiveOnRevoke', 'NICDrainWithoutChildren', 'ResetSharedBudget', 'SkipFrameCharge', 'SkipByteCharge', 'AcceptWrongIncomingTuple', 'CrossSessionQueue', 'KeepRevokedQueue', 'PublishLateWriteSuccess', 'RewrapDropsPendingQueue', 'ErasePossibleEffect', 'AdmitSecondDelivery', 'MutateAllocatedService', 'ReadAdmittedAfterClose', 'RevokeErasesReadDebt']
NIC_BOUNDS = 'Eight explicitly reachable bookkeeping seeds, three transition suffixes, two lifetime tuples/two active sessions, one callback/one queued frame per session, three cumulative frames/four byte units.'


PROBE_PROPERTIES = ['TypeOK', 'ScopeImmutable', 'RetainedExact', 'RetainedBound', 'ValidatedAdmission', 'ScopedIdentity', 'ImmutableBinding', 'WorkerBound', 'WorkerDebt', 'AtMostOnce', 'RouteBeforeDispatch', 'RouteRetained', 'ResultSticky', 'SettlementEvidence', 'TimelySuccess', 'WireUnknown', 'DuplicateIsolation', 'LookupIsolation', 'WireIdentity', 'WireRoute', 'WireResult', 'WireSettlement', 'PoisonSticky', 'PoisonExclusion', 'NoLateHostAcceptance']
PROBE_MUTANTS = ['RebindScope', 'EvictSettledHandle', 'OmitHandleBudget', 'RetainInvalidContext', 'AdmitZeroKey', 'UnsealedScopedStart', 'RenewDuplicateDeadline', 'AdoptForeignDuplicate', 'RepeatDuplicateDispatch', 'OmitWorkerBudget', 'ReleaseTimeoutWorker', 'DispatchBeforeRoute', 'ForgetFailedRoute', 'OverwriteReadyResult', 'SettleRunningWorker', 'PublishLateSuccess', 'UnknownMeansSettled', 'IgnoreLookupOwnership', 'WireWrongIdentity', 'WireDropsAllocatedRoute', 'WireRehabilitatesFailure', 'WireReadyMeansSettled', 'ClearPoisonOnValidRead', 'ScorePoisonedResult', 'AcceptAfterHostDeadline']
PROBE_SCOPES = ['internal/workload/guesttcp/http_probe_operation.go', 'internal/workload/guesttcp/http_probe_operation_test.go', 'internal/workload/guesttcp/http_probe_scope.go', 'internal/workload/guesttcp/http_probe_scope_test.go', 'internal/workload/guesttcp/http_probe_wire.go', 'internal/workload/guesttcp/http_probe_wire_test.go', 'internal/workload/guesttcp/http_probe_observation.go', 'internal/workload/guesttcp/http_probe_observation_test.go', 'internal/workload/guesttcp/http_probe_context_test.go', 'docs/private-http-probe-operations.md']
PROBE_BOUNDS = 'Three nonzero keys plus invalid zero; two opaque scope-owner labels and two service targets; two symbolic backend deadlines; per-NIC retained-handle bound2 and independent worker bound1;21 replay-verified reachable seeds with all three-transition suffixes; separate per-key monotonic Host expiry. Dispatch counts one observer/Dial invocation, not packets/SYN retransmissions.'
PROBE_SEEDS = 'SeedTargets == <<EmptyState, Sealed, One, ScopedOne, Worker, Routed, Dispatched,\n TimedOut, Ended, Published, Settled, Rejected, FailedEnded, FailedSettled,\n WireSuccess, WireFailure, PoisonedSuccess, ExpiredSuccess, FullRetired, BusySecond, ExpiredRetained>>\n'


class FormalGateTests(unittest.TestCase):
    def setUp(self):
        self.output_capture = contextlib.redirect_stdout(io.StringIO())
        self.output_capture.__enter__()
        self.addCleanup(self.output_capture.__exit__, None, None, None)
        self.temporary = tempfile.TemporaryDirectory(prefix='formal-gate-unit-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        shutil.copytree(ROOT / 'formal', self.root / 'formal')
        shutil.copytree(ROOT / 'scripts/formal_support', self.root / 'scripts/formal_support')
        for path in ('go.mod', 'go.sum'):
            shutil.copyfile(ROOT / path, self.root / path)
        registry = json.loads((ROOT / 'formal/models.json').read_text())
        for path in {p for m in registry['models'] for p in m['source_scope']}:
            target = self.root / path
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / path, target)

    def test_ssm_batch_inventory_and_boundaries_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SSMBatchDeletion')
        self.assertEqual(len(entry['properties']), 11)
        self.assertEqual(len(entry['mutants']), 23)
        self.assertEqual(set(entry['source_scope']), {
            'internal/aws/parameters.go', 'internal/aws/ssm.go'})
        config = self.root / entry['config']
        cfg = config.read_text()
        load_registry(self.root)
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                if field == 'properties':
                    # Also remove matching config lines/controls: schema matching
                    # must not hide removal of an independently required oracle.
                    config.write_text(cfg.replace('INVARIANT ' + item + '\n', ''))
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                path.write_text(json.dumps(changed))
                expected = {'properties': 'existing required invariant was removed',
                            'mutants': 'required mutation control was removed',
                            'source_scope': 'required exact source scope changed'}[field]
                with self.subTest(field=field, item=item), self.assertRaisesRegex(GateError, expected):
                    load_registry(self.root)
                config.write_text(cfg)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == entry['id'])
        target['source_scope'].append('internal/core/store.go')
        path.write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        (self.root / entry['model']).unlink()
        config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_ssm_batch_bounds_and_controls_preserve_independent_oracles(self):
        entry = next(m for m in load_registry(self.root)['models'] if m['id'] == 'SSMBatchDeletion')
        source = (self.root / entry['model']).read_text()
        for anchor in ('n \\in 0..3', 'i \\in 1..10', 'i \\in 1..11',
                       'SUBSET RequestedKeys', '"implicit-deny", "explicit-deny"',
                       '"valid", "bad-shape", "bad-first", "bad-last"',
                       'Unique(xs)', 'FirstIndices == SelectSeq', 'Snapshot(present)',
                       'Abort("canceled")', 'Abort("storage-error")', 'commitOK \\in BOOLEAN'):
            self.assertIn(anchor, source)
        oracles = source.split('TypeOK ==', 1)[1]
        response_oracle = source.split('FirstIndices ==', 1)[1].split('Init ==', 1)[0]
        for mutant in entry['mutants']:
            with self.subTest(mutant=mutant['id']):
                name, changed = apply_mutant(source, entry['id'], mutant)
                self.assertEqual(name, entry['id'] + '_' + mutant['id'])
                self.assertNotEqual(source, changed)
                self.assertEqual(changed.split('TypeOK ==', 1)[1], oracles)
                self.assertEqual(changed.split('FirstIndices ==', 1)[1].split('Init ==', 1)[0], response_oracle)
                broken = json.loads(json.dumps(mutant))
                broken['edits'][0]['old'] += 'missing-anchor'
                with self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                    apply_mutant(source, entry['id'], broken)

    def test_sqs_batch_deletion_required_inventory_and_scope(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SQSBatchDeletion')
        self.assertEqual(len(entry['properties']), 19)
        self.assertEqual(len(entry['mutants']), 39)
        self.assertEqual(set(entry['source_scope']), {
            'internal/aws/sqs.go', 'internal/aws/sqs_batch_delete.go', 'internal/aws/sqs_store.go'})
        config = self.root / entry['config']
        cfg = config.read_text()
        load_registry(self.root)
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                if field == 'properties':
                    config.write_text(cfg.replace('INVARIANT ' + item + '\n', ''))
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config.write_text(cfg)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        (self.root / entry['model']).unlink()
        config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_sqs_batch_deletion_anchors_and_oracle_are_independent(self):
        entry = next(m for m in json.loads((self.root / 'formal/models.json').read_text())['models']
                     if m['id'] == 'SQSBatchDeletion')
        source = (self.root / entry['model']).read_text()
        for anchor in ('Rows == 1..7', 'n \\in 0..2', 'i \\in 1..10', 'i \\in 1..11',
                       '"recreated"', '"missing-queue"', '"duplicate"', '"ignored-purge"',
                       '"queue-drift"', '"reinsert-id"', '"rebind-receipt"', '"late-expiry"',
                       '"database-b"', '"region-b"', '"queue-b"', '"sqs:DeleteMessage"'):
            self.assertIn(anchor, source)
        oracle = source.split('RawValid ==', 1)[1].split('Init ==', 1)[0]
        for forbidden in ('Valid /', 'Matches(Request', 'selected[', 'good[', 'bad[', 'work['):
            self.assertNotIn(forbidden, oracle)
        self.assertIn('before[r].receipt = Request[i].receipt', oracle)
        self.assertIn('Request[j].receipt # Request[i].receipt', oracle)
        for mutant in entry['mutants']:
            with self.subTest(mutant=mutant['id']):
                module, changed = apply_mutant(source, entry['id'], mutant)
                self.assertNotEqual(source, changed)
                self.assertEqual(module, entry['id'] + '_' + mutant['id'])
                broken = json.loads(json.dumps(mutant))
                broken['edits'][0]['old'] += 'missing-anchor'
                with self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                    apply_mutant(source, entry['id'], broken)

    def test_native_sns_fanout_inventory_and_boundaries_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SNSQueueFanout')
        self.assertEqual(len(entry['properties']), 10)
        self.assertEqual(len(entry['mutants']), 9)
        load_registry(self.root)
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_autoscaling_plan_targets_required_contract(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'AutoScalingPlanTargets')
        self.assertEqual(set(entry['properties']), {'TypeOK', 'BoundedWhileOwned', 'OnlyAuthorizedValid', 'NoOwnershipSteal', 'DeleteKeepsCapacity', 'RejectionFrame', 'OtherTeamUnchanged', 'PendingInvisible'})
        self.assertEqual({m['id'] for m in entry['mutants']}, {'OmitAuthorization', 'OmitValidation', 'SkipCapacityBound', 'ClampLowerOnly', 'StealOwnership', 'RevertOnDelete', 'WriteOtherTeam', 'ExposePendingState', 'ReuseTableGeneration', 'RejectWritesCapacity', 'RejectClaimsOwner'})
        self.assertEqual(set(entry['source_scope']), {'internal/aws/autoscalingplans_store.go', 'internal/aws/autoscalingplans_http.go', 'internal/aws/autoscalingplans_dispatch.go', 'internal/aws/s3.go', 'internal/aws/ssm.go', 'internal/server/server.go'})
        config = self.root / entry['config']
        cfg = config.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                if field == 'properties':
                    config.write_text(cfg.replace('INVARIANT ' + item + '\n', ''))
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config.write_text(cfg)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        (self.root / entry['model']).unlink()
        config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_secret_description_workflow_required_contract(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretDescriptionWorkflow')
        self.assertEqual(len(entry['properties']), 14)
        self.assertEqual(len(entry['mutants']), 18)
        config = self.root / entry['config']
        cfg = config.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                if field == 'properties':
                    config.write_text(cfg.replace('INVARIANT ' + item + '\n', ''))
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config.write_text(cfg)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        (self.root / entry['model']).unlink()
        config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_secret_metadata_update_required_contract(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SecretMetadataUpdate')
        self.assertEqual(len(entry['properties']), 13)
        self.assertEqual(len(entry['mutants']), 14)
        config = self.root / entry['config']
        cfg = config.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                if field == 'properties':
                    config.write_text(cfg.replace('INVARIANT ' + item + '\n', ''))
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config.write_text(cfg)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        (self.root / entry['model']).unlink()
        config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_ssm_durable_command_required_contract(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'SSMDurableCommands')
        self.assertEqual(len(entry['properties']), 15)
        self.assertEqual(len(entry['mutants']), 23)
        config = self.root / entry['config']
        cfg = config.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                if field == 'properties':
                    config.write_text(cfg.replace('INVARIANT ' + item + '\n', ''))
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config.write_text(cfg)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        (self.root / entry['model']).unlink()
        config.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_ssm_durable_command_domains_and_mutation_anchors(self):
        entry = next(m for m in json.loads((self.root / 'formal/models.json').read_text())['models']
                     if m['id'] == 'SSMDurableCommands')
        source = (self.root / entry['model']).read_text()
        for anchor in ('Targets == {1,2}', '"dispatch","reconcile","page"',
                       's.version=s.snapshot', '!.frozen=s.items',
                       'last.before.version=last.before.snapshot',
                       'last.before.restarted', 's.status=last.before.status'):
            self.assertIn(anchor, source)
        for mutant in entry['mutants']:
            with self.subTest(mutant=mutant['id']):
                self.assertNotEqual(source, apply_mutant(source, entry['id'], mutant))
                broken = json.loads(json.dumps(mutant))
                broken['edits'][0]['old'] += 'missing-anchor'
                with self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                    apply_mutant(source, entry['id'], broken)

    def test_durable_guest_lease_required_contract(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'DurableGuestLease')
        config = self.root / entry['config']
        cfg = config.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == entry['id'])
                target[field].remove(item)
                if field == 'properties':
                    config.write_text(cfg.replace('INVARIANT ' + item + '\n', ''))
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config.write_text(cfg)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(changed))
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_durable_guest_lease_domains_and_mutation_anchors(self):
        entry = next(m for m in json.loads((self.root / 'formal/models.json').read_text())['models']
                     if m['id'] == 'DurableGuestLease')
        source = (self.root / entry['model']).read_text()
        config = (self.root / entry['config']).read_text()
        self.assertIn('CONSTANT Ops = {1, 2, 3}', config)
        self.assertIn('ASSUME Ops = {1, 2, 3}', source)
        for domain in ('"owner"', '"region"', '"instance"', '"deployment"', '"profile"',
                       '"record"', '"operation"', '"lease"', '"unknown"', '"alias"', '"ack"',
                       '"obs-owner"', '"obs-fence"', '"obs-operation"', '"obs-action"',
                       '"obs-requestlease"', '"obs-lease"', '"obs-state"'):
            self.assertIn(domain, source)
        for anchor in ('!.queue=2', 'ConcurrentAdvance ==', 'Crash ==', 'Reopen ==',
                       'InitialRecords(op)', 'OtherBegin(k) ==', 'OtherCommit ==',
                       'room == Cardinality(Retained(s.records))<2',
                       'last.before.db=last.before.work', 's.expected=s.db', 'exact == s.db=s.snapshot'):
            self.assertIn(anchor, source)
        for mutant in entry['mutants']:
            with self.subTest(mutant=mutant['id']):
                self.assertNotEqual(source, apply_mutant(source, entry['id'], mutant))
                broken = json.loads(json.dumps(mutant))
                broken['edits'][0]['old'] += 'missing-anchor'
                with self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                    apply_mutant(source, entry['id'], broken)

    def test_guest_command_registry_guards_and_source_scope_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        baseline = json.loads(original)
        entry = next(m for m in baseline['models'] if m['id'] == 'GuestCommandLifecycle')
        model_path, config_path = self.root / entry['model'], self.root / entry['config']
        model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != 'GuestCommandLifecycle']
        path.write_text(json.dumps(removed))
        model_path.unlink(); config_path.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model_path.write_bytes(model_bytes); config_path.write_bytes(config_bytes)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == 'GuestCommandLifecycle')
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            path.write_text(json.dumps(changed))
            config_path.write_text(config_bytes.decode().replace('INVARIANT ' + prop + '\n', ''))
            with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
        config_path.write_bytes(config_bytes)
        for scope in (entry['source_scope'][1:], entry['source_scope'] + ['internal/core/store.go']):
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == 'GuestCommandLifecycle')['source_scope'] = scope
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
    def test_waf_models_controls_and_scopes_cannot_be_removed(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        for name in ('WAFRequestEvaluation', 'WAFAssociationAuthorization'):
            registry = json.loads(original)
            model = next(m for m in registry['models'] if m['id'] == name)
            self.assertIn(name, BASE_MODELS)
            self.assertEqual({m['id'] for m in model['mutants']}, REQUIRED_MUTANTS[name])
            registry['models'] = [m for m in registry['models'] if m['id'] != name]
            path.write_text(json.dumps(registry))
            sources = {name: (self.root / name).read_bytes() for name in (model['model'], model['config'])}
            for name in sources:
                (self.root / name).unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            for name, content in sources.items():
                (self.root / name).write_bytes(content)
            path.write_text(original)
            for index in range(len(model['mutants'])):
                registry = json.loads(original)
                entry = next(m for m in registry['models'] if m['id'] == model['id'])
                entry['mutants'].pop(index)
                path.write_text(json.dumps(registry))
                with self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                    load_registry(self.root)
            path.write_text(original)
            registry = json.loads(original)
            next(m for m in registry['models'] if m['id'] == model['id'])['source_scope'].pop()
            path.write_text(json.dumps(registry))
            with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
            path.write_text(original)
    def test_lambda_log_tail_registry_controls_and_scope_are_mandatory(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'LambdaLogTailDisclosure')
        self.assertEqual(set(entry['properties']), {'TypeOK', 'ResponseOptIn', 'ResponseSync', 'ResponseAuthorized', 'TailBound', 'DisplayCurrent', 'DisplayHasResponse'})
        self.assertEqual(len(entry['mutants']), 11)
        self.assertIn('internal/server/web/app.js', entry['source_scope'])
        self.assertIn('internal/server/server.go', entry['source_scope'])
        model_path, config_path = self.root / entry['model'], self.root / entry['config']
        source, config = model_path.read_bytes(), config_path.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != entry['id']]
        path.write_text(json.dumps(removed)); model_path.unlink(); config_path.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model_path.write_bytes(source); config_path.write_bytes(config)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            config_path.write_text(config.decode().replace('INVARIANT '+prop+'\n', ''))
            path.write_text(json.dumps(changed))
            with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
        config_path.write_bytes(config)
        for scope in [[p for p in entry['source_scope'] if p != removed] for removed in entry['source_scope']] + [entry['source_scope']+['internal/core/store.go']]:
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == entry['id'])['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.subTest(scope=scope), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
        for control in entry['mutants']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == entry['id'])
            target['mutants'] = [m for m in target['mutants'] if m['id'] != control['id']]
            path.write_text(json.dumps(changed))
            with self.subTest(control=control['id']), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                load_registry(self.root)
            broken = json.loads(json.dumps(control))
            broken['edits'][0]['old'] += 'missing-anchor'
            with self.subTest(anchor=control['id']), self.assertRaisesRegex(GateError, 'mutation anchor count mismatch'):
                apply_mutant(source.decode(), entry['id'], broken)
        path.write_text(original)
        load_registry(self.root)

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

    def test_console_handoff_registry_guards_and_source_scope_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        baseline = json.loads(original)
        entry = next(m for m in baseline['models'] if m['id'] == 'ConsoleHandoff')
        self.assertEqual(set(entry['properties']), {'ParentLifetimeBound', 'ExactDestination', 'AttemptConsumesCode', 'OneTimeRedemption', 'RedeemRequiresFreshParent', 'OperatorRefused', 'UnrelatedSessionsPreserved', 'LogoutRejectsReplay', 'TypeOK', 'SDKAuthorityPreserved', 'UseRequiresFreshAuthority'})
        self.assertEqual({m['id'] for m in entry['mutants']}, {'SkipParameter', 'RevokeSDK', 'SkipRedeemExpiry', 'SkipUseGeneration', 'CachePolicy', 'ExtendHandoff', 'SkipRedeemGeneration', 'SkipCookie', 'SkipRegion', 'SkipBrowserExpiry', 'ReuseCode', 'SkipAction', 'SkipRedeemParent', 'RevokePeer', 'KeepLoggedOutCookie', 'ExtendBrowser', 'IgnoreConsumedCode', 'SkipUseParent', 'GrantBrowserSDK', 'AllowOperator'})
        model_path, config_path = self.root / entry['model'], self.root / entry['config']
        model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
        removed = json.loads(original)
        removed['models'] = [m for m in removed['models'] if m['id'] != 'ConsoleHandoff']
        path.write_text(json.dumps(removed))
        model_path.unlink(); config_path.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)
        model_path.write_bytes(model_bytes); config_path.write_bytes(config_bytes)
        for prop in entry['properties']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == 'ConsoleHandoff')
            target['properties'].remove(prop)
            target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != prop]
            path.write_text(json.dumps(changed))
            config_path.write_text(config_bytes.decode().replace('INVARIANT ' + prop + '\n', ''))
            with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                load_registry(self.root)
        config_path.write_bytes(config_bytes)
        for scope in (entry['source_scope'][1:], entry['source_scope'] + ['internal/core/store.go']):
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == 'ConsoleHandoff')['source_scope'] = scope
            path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
        path.write_text(original)
        for mutant in entry['mutants']:
            changed = json.loads(original)
            target = next(m for m in changed['models'] if m['id'] == 'ConsoleHandoff')
            target['mutants'] = [m for m in target['mutants'] if m['id'] != mutant['id']]
            path.write_text(json.dumps(changed))
            with self.subTest(removed_mutant=mutant['id']), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
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

    def test_bedrock_runtime_model_properties_controls_and_source_scopes_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        model = next(m for m in json.loads(original)['models'] if m['id'] == 'BedrockRuntimeAdmission')
        expected_properties = {'AuthorizationFirst', 'BackendOnlyAfterAdmission', 'ConcurrencyBound', 'NoCallAfterClose', 'NoFallback', 'NoPersistence', 'SuccessOnlyFromBackend', 'TruthfulFailure', 'TypeOK'}
        expected_controls = {'AdmitAfterClose', 'CallUnsupportedOperation', 'FallbackBackend', 'IgnoreConcurrency', 'LeakSlot', 'MapBeforeAuthorization', 'MisreportMissingModel', 'OmitAuthorization', 'OmitEnablement', 'OmitMapping', 'OmitValidation', 'PersistPrompt', 'ShutdownClaimsBackendFailure', 'SyntheticDisabledSuccess'}
        expected_scope = {'cmd/simulator/bedrock_flags.go', 'cmd/simulator/main.go', 'internal/aws/bedrock_runtime.go', 'internal/aws/bedrock_runtime_messages.go', 'internal/aws/s3.go', 'internal/aws/ssm.go', 'internal/inference/apple.go', 'internal/inference/inference.go', 'internal/inference/ollama.go', 'internal/server/bedrock_runtime.go', 'internal/server/sakura_provider.go', 'internal/server/server.go'}
        self.assertEqual(set(model['properties']), expected_properties)
        self.assertEqual({m['id'] for m in model['mutants']}, expected_controls)
        self.assertEqual(set(model['source_scope']), expected_scope)
        tla, cfg = self.root / model['model'], self.root / model['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        try:
            registry = json.loads(original)
            registry['models'] = [m for m in registry['models'] if m['id'] != model['id']]
            path.write_text(json.dumps(registry))
            tla.unlink(); cfg.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            tla.write_bytes(source); cfg.write_bytes(config)
            for prop in sorted(expected_properties):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['properties'].remove(prop)
                cfg.write_text('\n'.join(line for line in config.decode().splitlines() if line != 'INVARIANT ' + prop) + '\n')
                path.write_text(json.dumps(registry))
                with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
            cfg.write_bytes(config)
            for control in sorted(expected_controls):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['mutants'] = [m for m in target['mutants'] if m['id'] != control]
                path.write_text(json.dumps(registry))
                with self.subTest(control=control), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                    load_registry(self.root)
            for source_path in sorted(expected_scope):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['source_scope'].remove(source_path)
                path.write_text(json.dumps(registry))
                with self.subTest(source_scope=source_path), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        finally:
            path.write_text(original)
            tla.write_bytes(source); cfg.write_bytes(config)



    def test_appengine_model_properties_controls_and_source_scopes_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        model = next(m for m in json.loads(original)['models'] if m['id'] == 'AppEngineTraffic')
        expected_properties = {'AllocationsReferenceVersions', 'ApplicationParent', 'ApplicationPermanent', 'DefaultFirst', 'DenialAtomic', 'OtherTeamUnchanged', 'ServedThroughRouteAndFirewall', 'SplitComplete', 'TypeOK'}
        expected_controls = {'ChangeOtherTeam', 'DeleteApplication', 'DeleteDefaultService', 'DeleteVersionWithTraffic', 'DeployWithoutApplication', 'FirstVersionWithoutTraffic', 'KeepSplitOfDeletedService', 'OmitDefaultFirst', 'OmitOwnerCheck', 'PartialSplit', 'ServePastFirewall', 'ServeUnroutedVersion', 'SplitNamesMissingVersion'}
        expected_scope = {'internal/gcp/appengine/apps.go', 'internal/gcp/appengine/deploy.go', 'internal/gcp/appengine/engine.go', 'internal/gcp/appengine/firewall.go', 'internal/gcp/appengine/model.go', 'internal/gcp/appengine/model.json', 'internal/gcp/appengine/openapi.go', 'internal/gcp/appengine/serve.go', 'internal/gcp/appengine/services.go', 'internal/gcp/appengine/store.go', 'internal/gcp/appengine/wsgi.py', 'internal/gcp/storage/source.go', 'internal/server/cloud_storage.go', 'internal/server/server.go', 'tools/gcpmodels/appengine.py'}
        self.assertEqual(set(model['properties']), expected_properties)
        self.assertEqual({m['id'] for m in model['mutants']}, expected_controls)
        self.assertEqual(set(model['source_scope']), expected_scope)
        cfg = self.root / model['config']
        config = cfg.read_bytes()
        try:
            for prop in sorted(expected_properties):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['properties'].remove(prop)
                cfg.write_text('\n'.join(line for line in config.decode().splitlines() if line != 'INVARIANT ' + prop) + '\n')
                path.write_text(json.dumps(registry))
                with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
            cfg.write_bytes(config)
            for control in sorted(expected_controls):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['mutants'] = [m for m in target['mutants'] if m['id'] != control]
                path.write_text(json.dumps(registry))
                with self.subTest(control=control), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                    load_registry(self.root)
            for source_path in sorted(expected_scope):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['source_scope'].remove(source_path)
                path.write_text(json.dumps(registry))
                with self.subTest(source_scope=source_path), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        finally:
            path.write_text(original)
            cfg.write_bytes(config)

    def test_route53_model_properties_six_controls_and_source_scopes_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        model = next(m for m in json.loads(original)['models'] if m['id'] == 'Route53AtomicBatch')
        expected_properties = {'TypeOK', 'AtomicPair', 'PreserveSentinel', 'OnlyAuthorizedValid', 'OtherTeamUnchanged', 'PendingInvisible'}
        expected_controls = {'OmitAuthorization', 'OmitValidation', 'HalfBatch', 'ChangeSentinel', 'ChangeOtherTeam', 'ExposePendingState'}
        expected_scope = {'internal/aws/route53_store.go', 'internal/aws/route53_http.go', 'internal/aws/route53_dispatch.go', 'internal/aws/s3.go', 'internal/aws/ssm.go', 'internal/server/server.go'}
        self.assertEqual(set(model['properties']), expected_properties)
        self.assertEqual({m['id'] for m in model['mutants']}, expected_controls)
        self.assertEqual(set(model['source_scope']), expected_scope)
        tla, cfg = self.root / model['model'], self.root / model['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        try:
            registry = json.loads(original)
            registry['models'] = [m for m in registry['models'] if m['id'] != model['id']]
            path.write_text(json.dumps(registry))
            tla.unlink(); cfg.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            tla.write_bytes(source); cfg.write_bytes(config)
            for prop in sorted(expected_properties):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['properties'].remove(prop)
                cfg.write_text('\n'.join(line for line in config.decode().splitlines() if line != 'INVARIANT ' + prop) + '\n')
                path.write_text(json.dumps(registry))
                with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
            cfg.write_bytes(config)
            for control in sorted(expected_controls):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['mutants'] = [m for m in target['mutants'] if m['id'] != control]
                path.write_text(json.dumps(registry))
                with self.subTest(control=control), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                    load_registry(self.root)
            for source_path in sorted(expected_scope):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['source_scope'].remove(source_path)
                path.write_text(json.dumps(registry))
                with self.subTest(source_scope=source_path), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        finally:
            path.write_text(original)
            tla.write_bytes(source); cfg.write_bytes(config)

    def test_elb_model_properties_eight_controls_and_source_scopes_cannot_disappear(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        model = next(m for m in json.loads(original)['models'] if m['id'] == 'ELBTargetLifecycle')
        expected_properties = {'TypeOK', 'ListenerReferencesExist', 'TargetNeedsGroup', 'HealthCurrent', 'ReportedHealthInUse', 'OnlyAuthorizedChanges', 'OtherTeamUnchanged'}
        expected_controls = {'OmitInUseGuard', 'OmitListenerCascade', 'OmitTargetCascade', 'OmitEpochGuard', 'OmitUnuseReset', 'CheckUnusedGroup', 'OmitAuthorization', 'ChangeOtherTeam'}
        expected_scope = {'internal/aws/elb_store.go', 'internal/aws/elb_dispatch.go', 'internal/aws/elb_query.go', 'internal/aws/elb_target.go', 'internal/aws/apirouting/alb_headers.go', 'internal/aws/lambda_permissions.go', 'internal/aws/s3.go', 'internal/aws/ssm.go', 'internal/server/server.go'}
        self.assertEqual(set(model['properties']), expected_properties)
        self.assertEqual({m['id'] for m in model['mutants']}, expected_controls)
        self.assertEqual(set(model['source_scope']), expected_scope)
        tla, cfg = self.root / model['model'], self.root / model['config']
        source, config = tla.read_bytes(), cfg.read_bytes()
        try:
            registry = json.loads(original)
            registry['models'] = [m for m in registry['models'] if m['id'] != model['id']]
            path.write_text(json.dumps(registry))
            tla.unlink(); cfg.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            tla.write_bytes(source); cfg.write_bytes(config)
            for prop in sorted(expected_properties):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['properties'].remove(prop)
                cfg.write_text('\n'.join(line for line in config.decode().splitlines() if line != 'INVARIANT ' + prop) + '\n')
                path.write_text(json.dumps(registry))
                with self.subTest(property=prop), self.assertRaisesRegex(GateError, 'existing required invariant was removed'):
                    load_registry(self.root)
            cfg.write_bytes(config)
            for control in sorted(expected_controls):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['mutants'] = [m for m in target['mutants'] if m['id'] != control]
                path.write_text(json.dumps(registry))
                with self.subTest(control=control), self.assertRaisesRegex(GateError, 'required mutation control was removed'):
                    load_registry(self.root)
            for source_path in sorted(expected_scope):
                registry = json.loads(original)
                target = next(m for m in registry['models'] if m['id'] == model['id'])
                target['source_scope'].remove(source_path)
                path.write_text(json.dumps(registry))
                with self.subTest(source_scope=source_path), self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                    load_registry(self.root)
        finally:
            path.write_text(original)
            tla.write_bytes(source); cfg.write_bytes(config)

    def test_ec2_discovery_fixed_inventory_cannot_be_removed(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        baseline = json.loads(original)
        entry = next(m for m in baseline['models'] if m['id'] == 'EC2ReadDiscovery')
        # Independent expectations, not copied from the gate's required maps.
        self.assertEqual(set(entry['properties']), {'TypeOK', 'AuthorizedRead', 'RegionBound', 'OwnedRead'})
        self.assertEqual({m['id'] for m in entry['mutants']}, {'MissingAllow', 'MissingDeny', 'MissingRegion', 'MissingOwner'})
        self.assertEqual(set(entry['source_scope']), {'internal/aws/ec2_read.go', 'internal/aws/ssm.go', 'internal/server/server.go'})
        try:
            changed = json.loads(original)
            changed['models'] = [m for m in changed['models'] if m['id'] != 'EC2ReadDiscovery']
            path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            for field, names in (
                ('properties', ['TypeOK', 'AuthorizedRead', 'RegionBound', 'OwnedRead']),
                ('mutants', ['MissingAllow', 'MissingDeny', 'MissingRegion', 'MissingOwner']),
                ('source_scope', ['internal/aws/ec2_read.go', 'internal/aws/ssm.go', 'internal/server/server.go']),
            ):
                for name in names:
                    with self.subTest(field=field, removed=name):
                        changed = json.loads(original)
                        target = next(m for m in changed['models'] if m['id'] == 'EC2ReadDiscovery')
                        target[field] = [item for item in target[field] if (item['id'] if field == 'mutants' else item) != name]
                        path.write_text(json.dumps(changed))
                        with self.assertRaises(GateError):
                            load_registry(self.root)
        finally:
            path.write_text(original)

    def test_lambda_execution_role_bridge_fixed_inventory_cannot_be_removed(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        baseline = json.loads(original)
        entry = next(m for m in baseline['models'] if m['id'] == 'LambdaExecutionRoleBridge')
        # Independent expectations, not copied from the gate's required maps.
        properties = ['TypeOK', 'NoCallerEscalation', 'NoCallerNarrowing', 'CanceledNotDispatched']
        mutants = ['InheritCallerSession', 'IntersectCallerSession', 'DispatchAfterCancel']
        scope = ['internal/aws/lambda_bridge.go', 'internal/aws/lambda_bridge_test.go', 'internal/aws/lambda_bridge_cancel_test.go']
        self.assertEqual(set(entry['properties']), set(properties))
        self.assertEqual({m['id'] for m in entry['mutants']}, set(mutants))
        self.assertEqual(set(entry['source_scope']), set(scope))
        try:
            changed = json.loads(original)
            changed['models'] = [m for m in changed['models'] if m['id'] != 'LambdaExecutionRoleBridge']
            path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            for field, names in (('properties', properties), ('mutants', mutants), ('source_scope', scope)):
                for name in names:
                    with self.subTest(field=field, removed=name):
                        changed = json.loads(original)
                        target = next(m for m in changed['models'] if m['id'] == 'LambdaExecutionRoleBridge')
                        target[field] = [item for item in target[field] if (item['id'] if field == 'mutants' else item) != name]
                        path.write_text(json.dumps(changed))
                        with self.assertRaises(GateError):
                            load_registry(self.root)
        finally:
            path.write_text(original)

    def test_complete_registry_and_exact_mutations(self):
        registry = load_registry(self.root)
        # Keep this inventory independent of the implementation's required set.
        # A newly registered model must be reviewed and named here explicitly.
        self.assertEqual({model['id'] for model in registry['models']}, {'ARPTrustedReply', 'PersistentIngressPolicy', 'AppEngineTraffic', 'AutoScalingPlanTargets', 'AzureBlobState', 'AzurePropertiesEpoch', 'AzurePropertiesTarget', 'BedrockRuntimeAdmission', 'BlobRange', 'BrowserSessions', 'CFNConditionAdmission', 'CFNConditionMemoDepth', 'CFNEventHistory', 'CFNFinalCleanupACK', 'CFNNativeIAMTransaction', 'CFNParameterSnapshot', 'CFNPhysicalResourceACK', 'CanonicalVMComposition', 'CatalogConsoleEpoch', 'CatalogResourceLifecycle', 'CloudWatchLogsLifecycle', 'CloudWatchLogsViewEpoch', 'CommandDispatchLifecycle', 'ConsoleHandoff', 'DurableGuestLease', 'DynamoDBAdmission', 'DynamoDBConsoleEpoch', 'DynamoDBLifecycle', 'EC2GuestAttachmentLifecycle', 'EC2ReadDiscovery', 'ELBTargetLifecycle', 'EngineLifecycle', 'FrameCodecLifecycle', 'GCPStorageState', 'GuestCommandLifecycle', 'HTTPObservationLifecycle', 'IAMBuiltinPinTransaction', 'IAMConditionEvaluation', 'IAMInstanceProfiles', 'IAMManagedPolicies', 'InstanceMetadataTokens', 'KMSAliasCatalog', 'KMSAliasCrypto', 'KMSAliasesViewEpoch', 'KMSBootstrapTransaction', 'KMSKeyState', 'KMSProtocolAdmission', 'LambdaExecutionRoleBridge', 'LambdaLogTailDisclosure', 'LandingAccessAdmission', 'LandingTopologyState', 'ListenerLifecycle', 'LoginInputIntent', 'NICSessionBookkeeping', 'NativeCommandIssuer', 'NativeIAMOwnership', 'NativeWireProjection', 'ObjectStore', 'OwnedClockQualification', 'OwnedProbeAttachmentLifecycle', 'OwnedProbeDispatchPolicy', 'OwnedProbeSessionLifecycle', 'PacketAdmission', 'ParticipantSessionLifecycle', 'PassiveServiceBoundary', 'PersistentTransportLifecycle', 'ProbeOperationLifecycle', 'ProjectAuthority', 'ProviderActor', 'ProviderPath', 'RESTDeploymentSnapshot', 'RESTProxyEventBoundary', 'RESTRouteMatching', 'Route53AtomicBatch', 'S3MultipartState', 'S3Versions', 'SNSAdmission', 'SNSLifecycle', 'SNSQueueFanout', 'SQSBatchDeletion', 'SQSConsoleEpoch', 'SQSLifecycle', 'SQSStorageBudget', 'SSMBatchDeletion', 'SSMDocumentState', 'SSMDurableCommands', 'SSMParameterState', 'STSSessionLifecycle', 'SakuraDiskAdmission', 'SakuraDiskMetadata', 'SakuraDisksViewEpoch', 'SakuraStartupAdmission', 'SecretDescriptionWorkflow', 'SecretMetadataUpdate', 'SecretMetadataViewEpoch', 'SecretVersionListing', 'SecretsDisclosureChannels', 'SecretsMutationRefresh', 'SecretsRevealEpoch', 'SecretsVersionLifecycle', 'StackOwnership', 'StartupTokenVisibility', 'TCPRouteClassification', 'TerminalClientEpoch', 'WAFAssociationAuthorization', 'WAFRequestEvaluation'})
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
            if name.endswith('_InvalidTarget'):
                return subprocess.CompletedProcess(command, 12,
                    'Invariant SeedTargetReached is violated.\nState 1:\nState 2:', '')
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


    def test_iam_builtin_pin_exact_action_domain(self):
        import re
        model = (self.root / 'formal/IAMBuiltinPinTransaction.tla').read_text()
        action_block = model.split('PinnedActions == {', 1)[1].split('}\nProbeAction', 1)[0]
        actual = re.findall(r'"([^"\n]+)"', action_block)
        expected = {
            'ssm:DescribeAssociation', 'ssm:GetDeployablePatchSnapshotForInstance',
            'ssm:GetDocument', 'ssm:DescribeDocument', 'ssm:GetManifest',
            'ssm:GetParameter', 'ssm:GetParameters', 'ssm:ListAssociations',
            'ssm:ListInstanceAssociations', 'ssm:PutInventory', 'ssm:PutComplianceItems',
            'ssm:PutConfigurePackageResult', 'ssm:UpdateAssociationStatus',
            'ssm:UpdateInstanceAssociationStatus', 'ssm:UpdateInstanceInformation',
            'ssmmessages:CreateControlChannel', 'ssmmessages:CreateDataChannel',
            'ssmmessages:OpenControlChannel', 'ssmmessages:OpenDataChannel',
            'ec2messages:AcknowledgeMessage', 'ec2messages:DeleteMessage',
            'ec2messages:FailMessage', 'ec2messages:GetEndpoint',
            'ec2messages:GetMessages', 'ec2messages:SendReply',
        }
        self.assertEqual(len(actual), 25)
        self.assertEqual(set(actual), expected)
        source = (self.root / 'internal/aws/iam_builtin.go').read_text()
        raw = source.split('const iamSSMCoreDocument = `', 1)[1].split('`', 1)[0]
        policy = json.loads(raw)
        actions = [a for statement in policy['Statement'] for a in statement['Action']]
        self.assertEqual(len(actions), 25)
        self.assertEqual(set(actions), expected)
        self.assertTrue(all(x['Effect'] == 'Allow' and x['Resource'] == '*'
                            for x in policy['Statement']))
        self.assertIn('const iamSSMCoreARN = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"', source)


    def test_iam_builtin_pin_transaction_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'IAMBuiltinPinTransaction')
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


    def test_iam_builtin_pin_transaction_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'IAMBuiltinPinTransaction')
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


    def test_iam_builtin_pin_transaction_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'IAMBuiltinPinTransaction')
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


    def test_cfn_native_iam_transaction_model_and_every_property_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNNativeIAMTransaction')
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


    def test_cfn_native_iam_transaction_each_control_rejects_removal_and_placeholder(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNNativeIAMTransaction')
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


    def test_cfn_native_iam_transaction_exact_scope_and_semantic_oracles_are_required(self):
        path = self.root / 'formal/models.json'
        original = path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CFNNativeIAMTransaction')
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

    def test_nic_exact_inventory_and_compact_bounds(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "NICSessionBookkeeping")
        load_registry(self.root)
        self.assertEqual(entry["properties"], NIC_PROPERTIES)
        self.assertEqual([m["id"] for m in entry["mutants"]], NIC_MUTANTS)
        self.assertEqual(entry["source_scope"], ["internal/workload/guestnet/tcp_sessions.go"])
        self.assertEqual(entry["bounds"], NIC_BOUNDS)
        cfg = (self.root / entry["config"]).read_text()
        self.assertEqual(cfg.splitlines()[0], "CONSTANT MaxDepth = 3")
        source = (self.root / entry["model"]).read_text()
        for sentinel in (
            "IDs == 1..2", "MaxFrames == 3", "MaxBytes == 4", "MaxQueue == 1",
            "Seeds == {Empty, One, Two, Queued, Writing, ClosingWrite, Retired, ClosingRead}",
            r"/\ depth < MaxDepth",
        ):
            self.assertIn(sentinel, source)
        for limit in ("no unbounded", "physical ownership", "Persistent transport"):
            self.assertIn(limit, entry["limits"])

    def test_nic_gate_rejects_each_missing_property_control_or_scope(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "NICSessionBookkeeping")
        for field in ("properties", "mutants", "source_scope"):
            for item in entry[field]:
                changed = json.loads(json.dumps(registry))
                target = next(m for m in changed["models"] if m["id"] == entry["id"])
                target[field].remove(item)
                (self.root / "formal/models.json").write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)

    def test_nic_gate_rejects_removed_model_even_if_artifacts_removed(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "NICSessionBookkeeping")
        changed = json.loads(json.dumps(registry))
        changed["models"] = [m for m in changed["models"] if m["id"] != entry["id"]]
        (self.root / "formal/models.json").write_text(json.dumps(changed))
        (self.root / entry["model"]).unlink()
        (self.root / entry["config"]).unlink()
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_nic_gate_rejects_widened_scope_and_missing_bounds(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "NICSessionBookkeeping")
        for field, value in (("source_scope", entry["source_scope"] + ["go.mod"]),
                             ("bounds", ""), ("limits", "")):
            changed = json.loads(json.dumps(registry))
            target = next(m for m in changed["models"] if m["id"] == entry["id"])
            target[field] = value
            (self.root / "formal/models.json").write_text(json.dumps(changed))
            with self.subTest(field=field), self.assertRaises(GateError):
                load_registry(self.root)

    def test_probe_exact_inventory_and_compact_bounds(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "ProbeOperationLifecycle")
        load_registry(self.root)
        self.assertEqual(entry["properties"], PROBE_PROPERTIES)
        self.assertEqual([m["id"] for m in entry["mutants"]], PROBE_MUTANTS)
        self.assertEqual(entry["source_scope"], PROBE_SCOPES)
        self.assertEqual(entry["bounds"], PROBE_BOUNDS)
        cfg = (self.root / entry["config"]).read_text()
        for line in ("CONSTANT HandleCap = 2", "CONSTANT WorkerCap = 1", "CONSTANT MaxDepth = 3"):
            self.assertIn(line, cfg.splitlines())
        source = (self.root / entry["model"]).read_text()
        for sentinel in ("Keys == 1..3", "Owners == {1,2}", "Targets == {1,2}", "Deadlines == {1, 2}", r"/\ depth < MaxDepth"):
            self.assertIn(sentinel, source)
        self.assertIn(PROBE_SEEDS, source)
        self.assertNotIn("SYMMETRY", cfg)
        for limit in ("three-transition suffixes", "not all unbounded histories", "not a Simulator runtime implementation claim"):
            self.assertIn(limit, entry["bounds"] + entry["limits"])

    def test_probe_gate_rejects_each_missing_property_control_or_scope(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "ProbeOperationLifecycle")
        for field in ("properties", "mutants", "source_scope"):
            for item in entry[field]:
                changed = json.loads(json.dumps(registry))
                target = next(m for m in changed["models"] if m["id"] == entry["id"])
                target[field].remove(item)
                (self.root / "formal/models.json").write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)

    def test_probe_gate_rejects_removed_model_and_artifacts(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "ProbeOperationLifecycle")
        registry["models"] = [m for m in registry["models"] if m["id"] != entry["id"]]
        (self.root / "formal/models.json").write_text(json.dumps(registry))
        (self.root / entry["model"]).unlink()
        (self.root / entry["config"]).unlink()
        with self.assertRaises(GateError):
            load_registry(self.root)

    def test_probe_gate_rejects_coordinated_property_config_control_removal(self):
        registry = json.loads((self.root / "formal/models.json").read_text())
        entry = next(m for m in registry["models"] if m["id"] == "ProbeOperationLifecycle")
        original_cfg = (self.root / entry["config"]).read_text()
        for prop in entry["properties"]:
            changed = json.loads(json.dumps(registry))
            target = next(m for m in changed["models"] if m["id"] == entry["id"])
            target["properties"].remove(prop)
            target["mutants"] = [m for m in target["mutants"] if m["expect_invariant"] != prop]
            (self.root / "formal/models.json").write_text(json.dumps(changed))
            (self.root / entry["config"]).write_text(original_cfg.replace("INVARIANT " + prop + "\n", ""))
            with self.subTest(property=prop), self.assertRaises(GateError):
                load_registry(self.root)

    def test_seed_replay_all_inputs_and_required_mapping(self):
        registry = load_registry(self.root)
        entries = prepare_seed_replays(self.root, registry, None, require)
        self.assertEqual([s["model"] for s, _ in entries],
                         ["NICSessionBookkeeping", "ProbeOperationLifecycle"])
        self.assertEqual(len(entries), 2)
        for count in (0, 1):
            with patch("formal_seed_replay.SPECS", SPECS[:count]):
                with self.assertRaisesRegex(GateError, "mapping changed"):
                    prepare_seed_replays(self.root, registry, None, require)

    def test_seed_replay_missing_or_changed_inputs_fail_closed(self):
        registry = load_registry(self.root)
        for spec in SPECS:
            for path in spec["files"]:
                target = self.root / path
                original = target.read_bytes()
                try:
                    target.unlink()
                    with self.subTest(path=path, action="removed"), self.assertRaises(GateError):
                        prepare_seed_replays(self.root, registry, None, require)
                    target.write_bytes(original + b"\nchanged\n")
                    with self.subTest(path=path, action="changed"), self.assertRaises(GateError):
                        prepare_seed_replays(self.root, registry, None, require)
                finally:
                    target.write_bytes(original)

    def test_seed_replay_cannot_fallback_from_incomplete_head_snapshot(self):
        registry = load_registry(self.root)
        with self.assertRaisesRegex(GateError, "absent from immutable HEAD"):
            prepare_seed_replays(self.root, registry, {"content": {}}, require)

    def test_seed_replay_required_properties_are_checked(self):
        registry = load_registry(self.root)
        specs = json.loads(json.dumps(SPECS))
        path = next(p for p in specs[0]["files"] if p.endswith(".cfg"))
        target = self.root / path
        target.write_text(target.read_text().replace("INVARIANT SeedTargetReached\n", ""))
        specs[0]["files"][path] = hashlib.sha256(target.read_bytes()).hexdigest()
        with patch("formal_seed_replay.SPECS", tuple(specs)):
            with self.assertRaisesRegex(GateError, "properties missing"):
                prepare_seed_replays(self.root, registry, None, require)

    def test_seed_replay_invalid_target_must_have_transition_counterexample(self):
        registry = load_registry(self.root)
        reports = self.root / "seed-reports"
        reports.mkdir()
        for bad in (subprocess.CompletedProcess([], 0, "No error has been found", ""),
                    subprocess.CompletedProcess([], 12, "Invariant SeedTargetReached is violated.", ""),
                    subprocess.CompletedProcess([], 12, "Invariant PrefixHasNext is violated.\nState 2:", "")):
            with tempfile.TemporaryDirectory() as directory:
                evidence = {"runs": []}
                baseline = subprocess.CompletedProcess([], 0, "No error has been found", "")
                with patch("formal_seed_replay.subprocess.run", side_effect=[baseline, bad]):
                    with self.assertRaises(GateError):
                        run_seed_replays(self.root, registry, None, Path(directory),
                                         Path("/fixture.jar"), "/fixture/java", reports,
                                         5, evidence, require, validate_result)
                self.assertFalse(evidence["runs"][-1]["success"])

    def test_standard_formal_suite_requires_seed_replay_success(self):
        jar = self.root / "seed-fixture.jar"
        jar.write_bytes(b"controlled registration fixture, not TLC")
        (self.root / "formal/tla2tools.sha256").write_text(
            hashlib.sha256(jar.read_bytes()).hexdigest() + "  tla2tools.jar\n")
        registry = load_registry(self.root)
        expected = {m["id"] + "_" + c["id"]: c["expect_invariant"]
                    for m in registry["models"] for c in m["mutants"]}
        def completed(command, **kwargs):
            name = Path(command[-1]).stem
            if name in expected:
                return subprocess.CompletedProcess(command, 12,
                    "Invariant " + expected[name] + " is violated.", "")
            return subprocess.CompletedProcess(command, 0, "No error has been found.", "")
        reports = self.root / "mandatory-replay"
        with patch("formal_model_gate.shutil.which", return_value="/fixture/java"), \
             patch("formal_model_gate.subprocess.run", side_effect=completed), \
             patch("formal_seed_replay.run_seed_replays", side_effect=GateError("required seed failure")) as replay:
            with self.assertRaisesRegex(GateError, "required seed failure"):
                run_suite(self.root, jar, reports)
            replay.assert_called_once()
        self.assertFalse(json.loads((reports / "evidence.json").read_text())["success"])

    def test_tcprouteclassification_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'TCPRouteClassification')
        self.assertEqual(entry['properties'], ['TypeOK', 'ConstructorGuard', 'CopiedConfiguration', 'NoAmbiguousRoutes', 'FailClosed', 'NonzeroPorts', 'ExactDirectionalTuple', 'SoleIndex', 'CompleteSelection'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['AcceptOverlappingConstructor', 'SkipSyntax', 'SkipDirection', 'ZeroPeerPort', 'ReverseDirection', 'Ignore_lip', 'Ignore_pip', 'Ignore_lm', 'Ignore_pm', 'Ignore_lp', 'IgnoreFixedPeerPort', 'LoseWildcard', 'AlwaysFirstIndex', 'DiscardCopiedRouteList'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guestnet/tcp_routes.go'])
        self.assertEqual(entry['bounds'], 'Exhaustive acyclic construction and one classification over 43 route lists (0..2 entries from six route profiles), 729 independent six-field packet tuples (0..2), three directions and two parser outcomes. No seeded starts.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'TCPRouteClassification')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'TCPRouteClassification')
        target['source_scope'].append('go.mod')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'TCPRouteClassification']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_framecodeclifecycle_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'FrameCodecLifecycle')
        self.assertEqual(entry['properties'], ['TypeOK', 'DecoderPoison', 'EncoderPoison', 'DecoderNoRetry', 'EncoderNoRetry', 'HeaderBeforeBody', 'DecoderSuccess', 'DecoderSerialization', 'EncoderSerialization', 'BoundedReads', 'OneWrite', 'LocalRejectNoIO', 'EncoderSuccess', 'UncertainSticky', 'NoBodyForInvalidHeader'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['IgnoreInvalidLength', 'IgnorePartialHeader', 'IgnoreWriteError', 'IgnoreShortWrite', 'DecoderForgetsPoison', 'EncoderForgetsPoison', 'DecoderRetriesPoison', 'EncoderRetriesPoison', 'HeaderFailureEntersBody', 'PartialBodySuccess', 'ConcurrentDecoderStart', 'ConcurrentEncoderStart', 'DuplicateHeaderRead', 'DuplicateWrite', 'InvalidLocalWrites', 'InvalidLocalPoison', 'FalseWriteSuccess', 'ForgetPartialEffect'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guestnet/frame.go'])
        self.assertEqual(entry['bounds'], 'Exhaustive finite graph from one empty decoder and encoder, two calls per codec, Boolean full/valid/error outcomes and serialized header/body/write phases; no symmetry or seeded suffix.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'FrameCodecLifecycle')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'FrameCodecLifecycle')
        target['source_scope'].append('go.mod')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'FrameCodecLifecycle']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_persistenttransportlifecycle_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'PersistentTransportLifecycle')
        self.assertEqual(entry['properties'], ['TypeOK', 'NoInvalidAdmission', 'QueueSealed', 'QueueOwnership', 'SelectedDebt', 'ProbePumpDebt', 'ReadJoin', 'ChildJoin', 'CompletionJoin', 'CapacityRelease', 'NoLateSuccess', 'EffectRetained'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['ExitWithSelectedDebt', 'LoseQueuedAcknowledgement', 'EnqueueAfterClosure', 'AdmitAfterRevocation', 'ForgetPendingSend', 'ForgetPendingRead', 'ForgetPendingDial', 'FinishBeforeWriter', 'FinishBeforeOwnedClose', 'ReleaseUncertainResources', 'PublishLateWriteSuccess', 'EraseUncertainWriteEffect', 'ExitWithoutQueueSeal', 'SelectAfterProbeClose', 'EnqueueAfterSendJoined'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guesttcp/transport_persistent.go', 'internal/workload/guesttcp/transport_persistent_test.go', 'internal/workload/guesttcp/transport.go'])
        self.assertEqual(entry['bounds'], 'Exhaustive from one admitted transport/child; two one-shot producers, queue capacity one, one selected write, one read and Dial call; independent stream/service close outcomes. No seeds, symmetry or depth cutoff.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'PersistentTransportLifecycle')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'PersistentTransportLifecycle')
        target['source_scope'].append('go.mod')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'PersistentTransportLifecycle']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_packetadmission_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'PacketAdmission')
        self.assertEqual(entry['properties'], ['HeaderSound', 'OptionSound', 'OptionComplete', 'Progress', 'BoundedCursor'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['SkipFrameMax', 'SkipEtherType', 'SkipVersionIHL', 'SkipProtocol', 'SkipTTL', 'SkipTotalMax', 'SkipFragment', 'SkipIPChecksum', 'SkipTCPHeaderMin', 'SkipTCPHeaderMax', 'SkipReserved', 'SkipSourcePort', 'SkipDestinationPort', 'SkipTCPChecksum', 'IgnoreEOLTail', 'RejectNOP', 'AllowDuplicate', 'RejectMSS', 'ZeroMSS', 'MSSWithoutSYN', 'LargeScale', 'ScaleWithoutSYN', 'SACKPermWithoutSYN', 'SACKWithSYN', 'SACKWithoutACK', 'SACKInvalidBlockWidth', 'TimestampWrongLength', 'AllowUnknown', 'RejectAllOptions', 'AllowTruncatedTimestamp', 'MSSWrongLength', 'SACKTooShort', 'SACKPermWrongLength'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guestnet/packet.go'])
        self.assertEqual(entry['bounds'], '26 numeric header witnesses; all option byte sequences from zero/one/two finite chunks up to40bytes; four SYN/ACK combinations; actual bounded scanner to terminal state, no seeded starts.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'PacketAdmission')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'PacketAdmission')
        target['source_scope'].append('go.mod')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'PacketAdmission']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_enginelifecycle_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'EngineLifecycle')
        self.assertEqual(entry['properties'], ['TypeOK', 'WorkerBound', 'WorkerDebt', 'WorkerReservation', 'RejectedRollback', 'SingleDial', 'NoInvalidAdmission', 'EndpointJoin', 'ConnectionOwnership', 'RegistrationBeforeDone', 'CloseWaitsMutation', 'DeadlineBound', 'NoTimerAfterReset', 'DialJoin', 'CaptureRetained', 'ConnectionJoin', 'TimerJoin', 'ReleaseAfterDestroy', 'LateConnectionJoin'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['ExceedWorkerBudget', 'LoseReservedSlot', 'LeakRejectedSlot', 'ReadmitDial', 'DialAfterCancellation', 'ForgetRegistration', 'CloseOverDeadlineMutation', 'ExtendAbsoluteLifetime', 'RetainDeadlineTimer', 'DestroyBeforeDialDone', 'DestroyBeforeConnectionClosed', 'CloseBeforeTimerReset', 'ReleaseWorkersEarly', 'ReleaseSlotEarly', 'ForgetLateConnectionClose', 'ForgetConnectionPointer', 'DoneBeforeEndpointReturn', 'MutationAfterAbsoluteExpiry', 'MutationAfterCancellation', 'RepeatCloseCapture'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guesttcp/engine.go', 'go.mod', 'go.sum'])
        self.assertEqual(entry['bounds'], 'Complete finite graph: one constructor/engine, worker requests1or3 against budget2, one accepted Dial with second-attempt sensitivity, one deadline mutation, absolute clock0..2 and requested deadline0..3. No seeds, depth cutoff or symmetry.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'EngineLifecycle')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'EngineLifecycle')
        target['source_scope'].append('go.work')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'EngineLifecycle']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_arptrustedreply_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'ARPTrustedReply')
        self.assertEqual(entry['properties'], ['ConstructorExact', 'ImmutableBinding', 'AdmissionSound', 'AdmissionComplete', 'TrustedReply', 'ReplyFormat'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['AcceptBadBinding', 'RetainCallerAlias', 'SkipMinimumLength', 'SkipMaximumLength', 'SkipEtherType', 'SkipPadding', 'SkipDestination', 'SkipEtherSource', 'SkipHardware', 'SkipProtocol', 'SkipHardwareLength', 'SkipProtocolLength', 'SkipOpcode', 'SkipSenderMAC', 'SkipSenderIP', 'SkipTargetIP', 'SkipTargetMAC', 'RejectBroadcast', 'RejectDirectedRefresh', 'WrongReplyDestination', 'WrongReplySource', 'WrongReplySenderMAC', 'WrongReplySenderIP', 'WrongReplyTargetMAC', 'WrongReplyTargetIP', 'WrongReplyLength', 'WrongReplyPadding', 'WrongReplyOpcode'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guestnet/arp.go'])
        self.assertEqual(entry['bounds'], 'Unseeded finite construction/caller-mutation/reply; seven symbolic binding profiles,23 request profiles(8allowed/15single-guard-invalid),all seven post-binding caller mutations.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'ARPTrustedReply')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'ARPTrustedReply')
        target['source_scope'].append('go.mod')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'ARPTrustedReply']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_passiveserviceboundary_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'PassiveServiceBoundary')
        self.assertEqual(entry['properties'], ['DeadlineExact', 'ExpiryExact', 'ConstructorExact', 'NoOwnershipTransfer', 'DelegationExact', 'ReceiveExact', 'AccountRejected', 'OutputExact', 'PreserveFailure', 'ErrorPrecedence'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['IgnoreParentPresence', 'IgnoreParentCancellation', 'AcceptExpiredRequest', 'ClampBeforeWindowValidation', 'IgnoreElapsedParent', 'IgnoreEarlierParent', 'MissingService', 'SkipRouteValidation', 'SkipMixedBinding', 'AllowOverlappingRoute', 'RequireSameLocalIP', 'PrematureOwnership', 'WrongReceiveEngine', 'WrongServiceReceive', 'UnaccountedOutput', 'IgnoreOutputBudget', 'CrossRouteOutput', 'BypassPersistentQueue', 'EraseWrappedFailure', 'IgnoreServiceRevocation', 'IgnoreContextCause'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guesttcp/passive_lifetime.go', 'internal/workload/guesttcp/transport_passive.go', 'internal/workload/guesttcp/transport_service.go'])
        self.assertEqual(entry['bounds'], 'Independent one-step finite scenarios: fixed now2/window3, requested0..7 and parent0..6; 13 symbolic route profiles; classified indices -1/0/1, two-unit budget, exact/ wrapped/distinct errors. Exhaustive from inputs, no seeds or symmetry.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'PassiveServiceBoundary')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'PassiveServiceBoundary')
        target['source_scope'].append('go.mod')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'PassiveServiceBoundary']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_httpobservationlifecycle_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'HTTPObservationLifecycle')
        self.assertEqual(entry['properties'], ['TypeOK', 'RequestBound', 'DeadlineImmutable', 'RawAccounting', 'InfoAccounting', 'RawHeaderBound', 'InformationalBound', 'NoBodyConsumption', 'RetainedWorkerDebt', 'PublicationEvidence', 'NoPostTimeoutPublication', 'StatusPreserved', 'ReceivedAtObserved', 'TimeoutRequestsClose', 'CleanupRequested'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['AcceptArbitraryPath', 'IgnoreRawByteLimit', 'IgnoreCumulativeFields', 'AcceptBareOrFoldedLines', 'AcceptUnsupportedVersion', 'AcceptSwitchingProtocols', 'AcceptOutOfRangeStatus', 'IgnoreInformationalLimit', 'ConsumeResponseBody', 'RenewOriginalDeadline', 'ReleaseOnCallerTimeout', 'PublishUncertainCleanup', 'AcceptJoinedCancellationError', 'AcceptOwnerFailure', 'IgnoreFinalDeadline', 'PromoteErrorStatus', 'LateWorkerPublishes', 'RetryShortWrite', 'ForgeHeaderTimestamp', 'TimeoutOmitsClose', 'FailureOmitsCleanup', 'ResetCumulativeFieldCount', 'ResetSharedByteBudget', 'ResetInformationalHistory', 'AcceptUnsupportedMinor'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guesttcp/http_observation.go'])
        self.assertEqual(entry['bounds'], 'One observer, two permitted paths plus invalid path; fixed minimum deadline0or1or2 (zero represents expiry during admission), clock0..2; cumulative rawbytes3/rawfields2/informational1 scaled from8192/64/8; separate CRLF/folding/parser-error/version/status classes. Complete finite graph without depth cutoff, seeds or symmetry.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'HTTPObservationLifecycle')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'HTTPObservationLifecycle')
        target['source_scope'].append('go.mod')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'HTTPObservationLifecycle']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_listenerlifecycle_reviewed_registration_is_required(self):
        original = (self.root / 'formal/models.json').read_text()
        registry = json.loads(original)
        entry = next(m for m in registry['models'] if m['id'] == 'ListenerLifecycle')
        self.assertEqual(entry['properties'], ['AttemptRetention', 'OwnerRetention', 'OnePendingAccept', 'BudgetAccounting', 'NoLateAdmission', 'OneAcceptAdmission', 'JoinBeforeDestroy', 'CapacityAfterJoin', 'SharedCapacity', 'NoEarlyRelease', 'BoundedDeadline', 'TimerResetAttempted', 'SequenceRetention'])
        self.assertEqual([m['id'] for m in entry['mutants']], ['ParallelAccept', 'LateAcceptStart', 'LateAcceptPublish', 'AcceptUnrecordedPort', 'ForgetAttempt', 'ForgetOwnedWrapper', 'SkipAggregateFrameCharge', 'SkipAggregateByteCharge', 'AcceptBadSYNFlags', 'AcceptSYNPayload', 'AcceptBadTuple', 'AllowChangedSYN', 'AllowSealedSYN', 'UnrecordedData', 'DestroyWithAcceptDebt', 'DestroyWithConnectionDebt', 'DestroyBeforeShutdown', 'ReleaseBeforeDestroy', 'EraseExternalWorkers', 'SkipCleanupTimer', 'SkipConnectionTimer', 'UnclampedDeadline', 'ForgetWrapperOnCloseError', 'ForgetPortOnConnectionClose', 'ResetBudgetOnClose', 'IgnoreRegisteredAcceptJoin', 'ClosedFlagIsNotReturn', 'RewriteFirstSequence'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guesttcp/listener.go', 'go.mod', 'go.sum'])
        self.assertEqual(entry['bounds'], 'One constructed listener, one fixed external worker reservation, two lifetime Accept calls, one retained connection/port capacity, two source ports/sequences, two frames/three byte units, absolute deadline1; finite unseeded lifecycle.')
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'ListenerLifecycle')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                (self.root / 'formal/models.json').write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        changed = json.loads(original)
        target = next(m for m in changed['models'] if m['id'] == 'ListenerLifecycle')
        target['source_scope'].append('go.work')
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'ListenerLifecycle']
        (self.root / 'formal/models.json').write_text(json.dumps(changed))
        for path in (entry['model'], entry['config']):
            (self.root / path).unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)

    def test_guesttcp_exact_dependency_pins_and_checksums_are_required(self):
        expected = {'gvisor.dev/gvisor': ['gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8 h1:Zy8IV/+FMLxy6j6p87vk/vQGKcdnbprwjTxc8UiUtsA=', 'gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8/go.mod h1:QkHjoMIBaYtpVufgwv3keYAbln78mBoCuShZrPrer1Q='], 'github.com/google/btree': ['github.com/google/btree v1.1.2 h1:xf4v41cLI2Z6FxbKm+8Bu+m8ifhj15JuZ9sa0jZCMUU=', 'github.com/google/btree v1.1.2/go.mod h1:qOPhT0dTNdNzV6Z/lhRX0YXUafgPLFUh+gZMl761Gm4='], 'golang.org/x/exp': ['golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa h1:FRnLl4eNAQl8hwxVVC17teOw8kdjVDVAiFMtgUdTSRQ=', 'golang.org/x/exp v0.0.0-20231110203233-9a3e6036ecaa/go.mod h1:zk2irFbV9DP96SEBUUAy67IdHUaZuSnrz1n472HUCLE='], 'golang.org/x/time': ['golang.org/x/time v0.12.0 h1:ScB/8o8olJvc+CQPWrK3fPZNfh7qgwCrY0zJmoEQLSE=', 'golang.org/x/time v0.12.0/go.mod h1:CDIdPxbZBQxdj6cxyCIdrNogrJKMJ7pr37NYpMcMDSg=']}
        original_mod = (self.root / 'go.mod').read_text()
        original_sum = (self.root / 'go.sum').read_text()
        for module, lines in expected.items():
            version = lines[0].split()[1]
            with self.subTest(module=module, case='version'):
                (self.root / 'go.mod').write_text(original_mod.replace(module + ' ' + version, module + ' v0.0.0'))
                with self.assertRaisesRegex(GateError, 'reviewed module pin changed'):
                    validate_guesttcp_module_pins(self.root)
                (self.root / 'go.mod').write_text(original_mod)
            for line in lines:
                with self.subTest(module=module, checksum=line):
                    (self.root / 'go.sum').write_text(original_sum.replace(line + '\n', ''))
                    with self.assertRaisesRegex(GateError, 'reviewed module checksum changed'):
                        validate_guesttcp_module_pins(self.root)
                    (self.root / 'go.sum').write_text(original_sum)
        (self.root / 'go.mod').write_text(original_mod + '\nreplace gvisor.dev/gvisor => ./unreviewed\n')
        with self.assertRaisesRegex(GateError, 'replacement or exclusion requires review'):
            validate_guesttcp_module_pins(self.root)

    def test_guesttcp_module_obligations_preserve_waf(self):
        registry = load_registry(self.root)
        by_id = {m['id']: m for m in registry['models']}
        for name in ('WAFRequestEvaluation', 'EngineLifecycle', 'ListenerLifecycle'):
            for path in ('go.mod', 'go.sum'):
                self.assertIn(path, by_id[name]['source_scope'])

    def test_guesttcp_require_parser_rejects_commented_pin_spoof(self):
        original = (self.root / 'go.mod').read_text()
        module = 'gvisor.dev/gvisor'
        version = 'v0.0.0-20260224225140-573d5e7127a8'
        pin = module + ' ' + version
        for comment, expected in (('/*\n' + pin + '\n*/', 'malformed guesttcp module comment'), ('// ' + pin, 'reviewed module pin changed')):
            altered = original.replace(pin, '') + '\n' + comment + '\nrequire ' + module + ' v0.0.0\n'
            (self.root / 'go.mod').write_text(altered)
            with self.subTest(comment=comment), self.assertRaisesRegex(GateError, expected):
                validate_guesttcp_module_pins(self.root)

    def test_guesttcp_require_parser_accepts_real_single_line(self):
        original = (self.root / 'go.mod').read_text()
        pin = 'gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8'
        (self.root / 'go.mod').write_text(original.replace(pin, '') + '\nrequire ' + pin + ' // selected\n')
        validate_guesttcp_module_pins(self.root)

    def test_guesttcp_require_parser_rejects_duplicates_and_malformed_input(self):
        original = (self.root / 'go.mod').read_text()
        pin = 'gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8'
        for suffix in ('require ' + pin, 'require (', '/* unclosed', 'require extra tokens v1.0.0', 'unknown directive'):
            (self.root / 'go.mod').write_text(original + '\n' + suffix + '\n')
            with self.subTest(suffix=suffix), self.assertRaises(GateError):
                validate_guesttcp_module_pins(self.root)

    def test_guesttcp_altered_pin_blocks_suite_before_tlc(self):
        original = (self.root / 'go.mod').read_text()
        (self.root / 'go.mod').write_text(original.replace('gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8', 'gvisor.dev/gvisor v0.0.0'))
        # Registry/schema and historical applicability do not enforce current pins.
        load_registry(self.root)
        with patch('formal_model_gate.subprocess.run') as process:
            with self.assertRaisesRegex(GateError, 'reviewed module pin changed'):
                run_suite(self.root, self.root / 'absent.jar', self.root / 'pin-reports')
            process.assert_not_called()

    def test_guesttcp_required_suite_guard_removal_is_detected(self):
        import inspect
        import formal_model_gate as gate
        source = inspect.getsource(gate.run_suite)
        call = "        validate_guesttcp_module_pins(root, snapshot['content'] if snapshot else None)\n"
        self.assertEqual(source.count(call), 1)
        namespace = dict(vars(gate))
        exec(compile(source.replace(call, ''), '<removed-pin-guard>', 'exec'), namespace)
        original = (self.root / 'go.mod').read_text()
        (self.root / 'go.mod').write_text(original.replace('gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8', 'gvisor.dev/gvisor v0.0.0'))
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'controlled guard-removal fixture only')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        with patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.subprocess.run', side_effect=OSError('fixture process reached')) as process:
            with self.assertRaisesRegex(GateError, 'TLC could not finish'):
                namespace['run_suite'](self.root, jar, self.root / 'removed-pin-reports')
            process.assert_called_once()

    def test_guesttcp_pin_guard_uses_immutable_module_content(self):
        content = {name: (self.root / name).read_bytes() for name in ('go.mod', 'go.sum')}
        (self.root / 'go.mod').write_text('unrelated current worktree')
        validate_guesttcp_module_pins(self.root, content)
        content['go.mod'] = content['go.mod'].replace(b'gvisor.dev/gvisor v0.0.0-20260224225140-573d5e7127a8', b'gvisor.dev/gvisor v0.0.0')
        with self.assertRaisesRegex(GateError, 'reviewed module pin changed'):
            validate_guesttcp_module_pins(self.root, content)

    def test_command_dispatch_registration_scope_and_controls_are_required(self):
        registry_path = self.root / 'formal/models.json'
        original = registry_path.read_text()
        entry = next(m for m in json.loads(original)['models'] if m['id'] == 'CommandDispatchLifecycle')
        self.assertEqual(entry['properties'], ['TypeOK', 'Admission', 'OriginalDeadline', 'ReadOnly', 'UnknownNotSettled', 'DuplicatePreserved', 'ExactDomain', 'ReadAfterExpiry', 'AtMostOnce', 'ReadAdmission'])
        self.assertEqual([x['id'] for x in entry['mutants']], ['IgnoreShape', 'IgnoreBinding', 'IgnoreService', 'IgnoreDomain', 'RenewFromReceipt', 'FreshCheckBeforeDuplicate', 'ReadRequiresLiveClock', 'UnknownMeansSettled', 'IgnoreExpiry', 'IgnoreQualification', 'RepeatDuplicateExecution', 'ReadEntersStart', 'StartRefusalMeansSettled', 'ReadIgnoresShape', 'ReadIgnoresBinding', 'ReadIgnoresService'])
        self.assertEqual(entry['source_scope'], ['internal/workload/guesttcp/http_probe_command.go'])
        config_path = self.root / entry['config']
        config = config_path.read_text()
        for field in ('properties', 'mutants', 'source_scope'):
            for item in entry[field]:
                changed = json.loads(original)
                target = next(m for m in changed['models'] if m['id'] == 'CommandDispatchLifecycle')
                target[field].remove(item)
                if field == 'properties':
                    target['mutants'] = [x for x in target['mutants'] if x['expect_invariant'] != item]
                    config_path.write_text(config.replace('INVARIANT ' + item + '\n', ''))
                registry_path.write_text(json.dumps(changed))
                with self.subTest(field=field, item=item), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
        registry_path.write_text(original)
        changed = json.loads(original)
        next(m for m in changed['models'] if m['id'] == 'CommandDispatchLifecycle')['source_scope'].append('go.mod')
        registry_path.write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
            load_registry(self.root)

    def test_command_dispatch_bounds_configuration_and_artifacts_are_required(self):
        registry_path = self.root / 'formal/models.json'
        original = registry_path.read_text()
        changed = json.loads(original)
        entry = next(m for m in changed['models'] if m['id'] == 'CommandDispatchLifecycle')
        self.assertEqual(entry['bounds'], 'One selected capability/key, two sequential commands, clock0..3 and original requested deadlines1..4; existing key classes none/exact/foreign, independent lower-layer admission/refusal. Forwarded deadline is not effective scope-capped deadline.')
        entry['bounds'] = 'unreviewed bounds'
        registry_path.write_text(json.dumps(changed))
        with self.assertRaisesRegex(GateError, 'command dispatch bounds changed'):
            load_registry(self.root)
        registry_path.write_text(original)
        config_path = self.root / entry['config']
        config = config_path.read_text()
        config_path.write_text(config.replace('NEXT Next', 'NEXT Tick'))
        with self.assertRaisesRegex(GateError, 'command dispatch configuration changed'):
            load_registry(self.root)
        config_path.write_text(config)
        for name in (entry['model'], entry['config']):
            path = self.root / name
            contents = path.read_bytes()
            path.unlink()
            with self.subTest(path=name), self.assertRaises(GateError):
                load_registry(self.root)
            path.write_bytes(contents)
        changed = json.loads(original)
        changed['models'] = [m for m in changed['models'] if m['id'] != 'CommandDispatchLifecycle']
        registry_path.write_text(json.dumps(changed))
        (self.root / entry['model']).unlink()
        config_path.unlink()
        with self.assertRaisesRegex(GateError, 'required existing model was removed'):
            load_registry(self.root)



    def test_owned_composition_registration_and_sensitivity_are_required(self):
        expected = {'OwnedProbeSessionLifecycle': {'properties': ['TypeOK', 'NoInvalidAdmission', 'NoInvalidPublication', 'TentativeHidden', 'CloseOnce', 'CloseBeforeJoin', 'DebtUntilDrain', 'ImmutableBinding', 'OneApply', 'TerminalSeal', 'EvidenceHasSuccess', 'NoUnsafeException', 'NoTokenReplay', 'FailedPacketNotPublished', 'NoNeedlessSeal'], 'source_scope': ['internal/workload/guesttcp/owned_probe_session.go', 'internal/workload/guesttcp/local_injection_outcome.go', 'internal/workload/guesttcp/transport.go', 'internal/workload/guesttcp/transport_persistent.go'], 'bounds': 'Original one-session/transaction and two facade calls retained, plus finite private-token provenance, three cancellation windows and exact provider propagation. No liveness.', 'mutants': ['AdmitAfterSeal', 'IOAfterSeal', 'PublishAfterRevoke', 'PublishProviderError', 'PublishFailedApply', 'ReadTentative', 'ReleaseBeforeIODrain', 'ReleaseBeforeGateDrain', 'ReleaseCloseError', 'JoinBeforeCloseReturn', 'RebindOnRevoke', 'ExposeFailedTentative', 'RepeatClose', 'DuplicateApply', 'SealWithoutClose', 'ReuseSuccessfulContext', 'IgnoreClaimedToken', 'FailedInjectionCreatesProof', 'IgnoreMixedTransportFailure', 'AcceptForeignMarker', 'AcceptProviderWrap', 'AcceptIndependentProviderError', 'AcceptSwallowedCallback', 'IgnoreOriginalCancellation', 'IgnoreOwnerSeal', 'OutboundException', 'ObservationException', 'DiscardCompletedInboundException', 'ResetTokenAfterSuccessfulCall']}, 'OwnedProbeAttachmentLifecycle': {'properties': ['TypeOK', 'NoInvalidAdmission', 'SingleAttach', 'TransferredOnlySuccess', 'ReturnedTransferExact', 'PublishedOnlyBound', 'NoLatePublish', 'RetainedFailureHandle', 'IndependentJoins', 'OwnerDebt', 'TransportDebt', 'ImmutableDescriptor', 'AdmissionOnlyContext'], 'source_scope': ['internal/workload/guesttcp/owned_probe_attachment.go'], 'bounds': 'One preissued owner and immutable descriptor, two attachment attempts, one constructor, independent pending/ok/error owner and transport joins. Exhaustive finite safety abstraction.', 'mutants': ['IgnoreRevokedAdmission', 'IgnoreExpiredAdmission', 'ReuseReservedOwner', 'TransferOnFailedConstruction', 'ForgetTransferredService', 'PublishFailedBinding', 'PublishAfterRevoke', 'PublishAfterCallerExpiry', 'DropFailureHandle', 'InferTransportFromOwner', 'InferOwnerFromTransport', 'ReleaseFailedOwner', 'ReleaseFailedTransport', 'RebindAtConstruction', 'UseCallerLifetime', 'RepeatConstructor']}, 'OwnedProbeDispatchPolicy': {'properties': ['ImmutableCanonical', 'ExactReplyMatch', 'DispatchSound', 'DispatchComplete'], 'source_scope': ['internal/workload/guestnet/arp.go', 'internal/workload/guesttcp/owned_probe_session.go'], 'bounds': 'Canonical reply plus14single-field corruptions, immutable original/caller mutation; explicit normalized probe/passive tuples including source port boundaries; five frame classes, three directions and independent opt-in/presence/readiness booleans.', 'mutants': ['IgnoreReply_length', 'IgnoreReply_eth', 'IgnoreReply_padding', 'IgnoreReply_dst', 'IgnoreReply_src', 'IgnoreReply_htype', 'IgnoreReply_ptype', 'IgnoreReply_hlen', 'IgnoreReply_plen', 'IgnoreReply_op', 'IgnoreReply_smac', 'IgnoreReply_sip', 'IgnoreReply_tmac', 'IgnoreReply_tip', 'IgnoreResponderPresence', 'IgnoreResponderReady', 'UseMutatedCallerBinding', 'IgnoreARPOptIn', 'ReverseARPDirection', 'RemoveReplyDirection', 'DropARPEarlyBranch', 'IgnorePassiveOptIn', 'IgnoreTCPDirection', 'AllowMalformedTCP', 'IgnoreProbe_localIP', 'IgnoreProbe_peerIP', 'IgnoreProbe_localMAC', 'IgnoreProbe_peerMAC', 'IgnoreProbe_peerPort', 'IgnoreProbeLowerPort', 'IgnoreProbeUpperPort', 'IgnorePassive_localIP', 'IgnorePassive_peerIP', 'IgnorePassive_localMAC', 'IgnorePassive_peerMAC', 'IgnorePassive_localPort', 'CaptureWrongCanonical']}}
        registry_path = self.root / 'formal/models.json'
        original = registry_path.read_text()
        for name, pins in expected.items():
            entry = next(m for m in json.loads(original)['models'] if m['id'] == name)
            for field in ('properties', 'source_scope', 'bounds'):
                self.assertEqual(entry[field], pins[field])
            self.assertEqual([x['id'] for x in entry['mutants']], pins['mutants'])
            config_path = self.root / entry['config']
            config = config_path.read_text()
            for field in ('properties', 'mutants', 'source_scope'):
                for item in entry[field]:
                    changed = json.loads(original)
                    target = next(m for m in changed['models'] if m['id'] == name)
                    target[field].remove(item)
                    if field == 'properties':
                        target['mutants'] = [x for x in target['mutants'] if x['expect_invariant'] != item]
                        changed_config = config.replace('INVARIANTS ' + ' '.join(entry['properties']), 'INVARIANTS ' + ' '.join(p for p in entry['properties'] if p != item))
                        self.assertNotEqual(changed_config, config)
                        config_path.write_text(changed_config)
                    registry_path.write_text(json.dumps(changed))
                    with self.subTest(model=name, field=field, item=item), self.assertRaises(GateError):
                        load_registry(self.root)
                    config_path.write_text(config)
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == name)['source_scope'].append('go.mod')
            registry_path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
            registry_path.write_text(original)

    def test_owned_composition_bounds_artifacts_and_registration_are_required(self):
        for name in ['OwnedProbeSessionLifecycle', 'OwnedProbeAttachmentLifecycle', 'OwnedProbeDispatchPolicy']:
            registry_path = self.root / 'formal/models.json'
            original = registry_path.read_text()
            changed = json.loads(original)
            entry = next(m for m in changed['models'] if m['id'] == name)
            entry['bounds'] = 'unreviewed smaller bounds'
            registry_path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'owned composition bounds changed'):
                load_registry(self.root)
            registry_path.write_text(original)
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == name)['limits'] = ''
            registry_path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'model bounds/limits must be explicit'):
                load_registry(self.root)
            registry_path.write_text(original)
            config_path = self.root / entry['config']
            config = config_path.read_text()
            changed_config = config.replace('SPECIFICATION Spec', 'SPECIFICATION MissingSpec')
            self.assertNotEqual(changed_config, config)
            config_path.write_text(changed_config)
            with self.assertRaises(GateError):
                load_registry(self.root)
            config_path.write_text(config)
            for path in (self.root / entry['model'], config_path):
                content = path.read_bytes()
                path.unlink()
                with self.subTest(model=name, path=path), self.assertRaises(GateError):
                    load_registry(self.root)
                path.write_bytes(content)
            changed = json.loads(original)
            changed['models'] = [m for m in changed['models'] if m['id'] != name]
            registry_path.write_text(json.dumps(changed))
            model_path = self.root / entry['model']
            model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
            model_path.unlink()
            config_path.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            model_path.write_bytes(model_bytes)
            config_path.write_bytes(config_bytes)
            registry_path.write_text(original)


    def test_dynamodb_count_indicator_domain_and_equivalence(self):
        import itertools
        source = (self.root / 'formal/DynamoDBAdmission.tla').read_text()
        keys = ['N1', 'N2', 'S1', 'B1', 'invalid']
        self.assertIn('Keys == {"N1","N2","S1","B1","invalid"}', source)
        start = source.index('Count(values) ==')
        end = source.index('Sum(values) ==', start)
        expression = source[start:end]
        for key in keys:
            self.assertEqual(expression.count('(IF values["' + key + '"] # 0 THEN 1 ELSE 0)'), 1)
        self.assertEqual(expression.count('THEN 1 ELSE 0'), len(keys))
        # Every combination of empty/nonempty classes is included; representative
        # nonzero magnitudes also ensure counting is not sum-of-item-sizes.
        for values in itertools.product((0, 1, 8), repeat=len(keys)):
            cardinality = len({key for key, value in zip(keys, values) if value != 0})
            indicators = sum(1 if value != 0 else 0 for value in values)
            self.assertEqual(cardinality, indicators)

    def test_ec2_attachment_registration_and_sensitivity_are_required(self):
        expected = {'EC2GuestAttachmentLifecycle': {'properties': ['TypeOK', 'PreparationExact', 'DurableBeforeConsumption', 'RegisteredBeforeIO', 'PublicationAdmission', 'SealBeforeAck', 'DurableSealAck', 'BackendIndependent', 'IndependentDrain', 'AdvanceGuard', 'ProfileGuard', 'ImmutableAssociation', 'RetainedScope', 'NoReconstruction'], 'source_scope': ['internal/aws/ec2_guest_attachment.go', 'internal/aws/ec2_guest_lease.go', 'internal/aws/iam_instance_profiles.go'], 'bounds': 'One held lease, one retained association/reservation, one owner and transport construction, one seal, one profile mutation, one held-to-contain advance and one restart. Symbolic fixed deadlines and exact identity; all finite schedules from empty journal, no seeds or depth cutoff.', 'mutants': ['IgnoreAssociation', 'PrepareAfterLeaseAdvance', 'PrepareDeletedProfile', 'ConsumeBeforeJournal', 'AdoptUnknownPrepare', 'TransportBeforeRegistration', 'TransportAfterSeal', 'PublishAfterSeal', 'PublishAfterCancellation', 'PublishUnknownCommit', 'SealWithoutOwnerRevoke', 'AcknowledgeUnwrittenSeal', 'DrainWithoutOwnerJoin', 'DrainWithoutTransportJoin', 'DrainDuringConstruction', 'LocalDrainClaimsBackend', 'OrdinaryAdvanceBypass', 'AdvanceBeforeSeal', 'AdvanceWrongIdentity', 'ProfileGuardRemoved', 'RestartRestoresReceipt', 'RenewAttachmentDeadline']}}
        registry_path = self.root / 'formal/models.json'
        original = registry_path.read_text()
        for name, pins in expected.items():
            entry = next(m for m in json.loads(original)['models'] if m['id'] == name)
            for field in ('properties', 'source_scope', 'bounds'):
                self.assertEqual(entry[field], pins[field])
            self.assertEqual([x['id'] for x in entry['mutants']], pins['mutants'])
            config_path = self.root / entry['config']
            config = config_path.read_text()
            for field in ('properties', 'mutants', 'source_scope'):
                for item in entry[field]:
                    changed = json.loads(original)
                    target = next(m for m in changed['models'] if m['id'] == name)
                    target[field].remove(item)
                    if field == 'properties':
                        target['mutants'] = [x for x in target['mutants'] if x['expect_invariant'] != item]
                        changed_config = config.replace('INVARIANTS ' + ' '.join(entry['properties']), 'INVARIANTS ' + ' '.join(p for p in entry['properties'] if p != item))
                        self.assertNotEqual(changed_config, config)
                        config_path.write_text(changed_config)
                    registry_path.write_text(json.dumps(changed))
                    with self.subTest(model=name, field=field, item=item), self.assertRaises(GateError):
                        load_registry(self.root)
                    config_path.write_text(config)
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == name)['source_scope'].append('go.mod')
            registry_path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'required exact source scope changed'):
                load_registry(self.root)
            registry_path.write_text(original)

    def test_ec2_attachment_bounds_artifacts_and_registration_are_required(self):
        for name in ['EC2GuestAttachmentLifecycle']:
            registry_path = self.root / 'formal/models.json'
            original = registry_path.read_text()
            changed = json.loads(original)
            entry = next(m for m in changed['models'] if m['id'] == name)
            entry['bounds'] = 'unreviewed smaller bounds'
            registry_path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'EC2 attachment bounds changed'):
                load_registry(self.root)
            registry_path.write_text(original)
            changed = json.loads(original)
            next(m for m in changed['models'] if m['id'] == name)['limits'] = ''
            registry_path.write_text(json.dumps(changed))
            with self.assertRaisesRegex(GateError, 'model bounds/limits must be explicit'):
                load_registry(self.root)
            registry_path.write_text(original)
            config_path = self.root / entry['config']
            config = config_path.read_text()
            changed_config = config.replace('SPECIFICATION Spec', 'SPECIFICATION MissingSpec')
            self.assertNotEqual(changed_config, config)
            config_path.write_text(changed_config)
            with self.assertRaises(GateError):
                load_registry(self.root)
            config_path.write_text(config)
            for path in (self.root / entry['model'], config_path):
                content = path.read_bytes()
                path.unlink()
                with self.subTest(model=name, path=path), self.assertRaises(GateError):
                    load_registry(self.root)
                path.write_bytes(content)
            changed = json.loads(original)
            changed['models'] = [m for m in changed['models'] if m['id'] != name]
            registry_path.write_text(json.dumps(changed))
            model_path = self.root / entry['model']
            model_bytes, config_bytes = model_path.read_bytes(), config_path.read_bytes()
            model_path.unlink()
            config_path.unlink()
            with self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            model_path.write_bytes(model_bytes)
            config_path.write_bytes(config_bytes)
            registry_path.write_text(original)

    def test_battle_integration_exact_registration_and_removal_detectors(self):
        expected = {'CanonicalVMComposition': {'bounds': 'Five disjoint initial modes: one canonical launch, one '
                                              'network ownership operation, one asynchronous creator, or '
                                              'one explicit runtime configuration, or one saved typed '
                                              'provider/Host association and operation. One immutable '
                                              'identity compared with a foreign identity; one '
                                              'cancellation, restart, child, pending stack generation and '
                                              'cleanup. All finite schedules, no depth cutoff.',
                                    'mutants': ['UnpinnedPlan',
                                                'ForeignStackPlan',
                                                'UnknownJournalReadback',
                                                'UnwrittenReservation',
                                                'WrongPreparedIdentity',
                                                'ForeignControl',
                                                'RunningBeforeAttachment',
                                                'ForeignAttachment',
                                                'UnknownAttachmentCommit',
                                                'ActivateAfterSeal',
                                                'ForeignAgent',
                                                'UnreadyAgent',
                                                'ForeignPublication',
                                                'LatePublication',
                                                'SkipOwnerJoin',
                                                'SkipTransportJoin',
                                                'AssumeContainment',
                                                'ReleaseBeforeContainment',
                                                'ForeignNetwork',
                                                'MutateLeasedNetwork',
                                                'UnauthorizedSeal',
                                                'ForeignDelete',
                                                'UnqualifiedChildFD',
                                                'UnboundedCreators',
                                                'LateChildPublication',
                                                'RestartRestoresAuthority',
                                                'LoseLateChildDebt',
                                                'UnsupportedPlatform',
                                                'InvalidOperatorFile',
                                                'UnpinnedRuntime',
                                                'ImplicitRuntime',
                                                'ConfiguredAdvertisesReady',
                                                'UntypedProvider',
                                                'UnreadyProvider',
                                                'ForeignProviderService',
                                                'ForeignHostOwner',
                                                'ForeignHostBinding',
                                                'ForeignHostService',
                                                'ReuseSourceNewKey',
                                                'RebindSavedService',
                                                'StartAfterRegistrySeal',
                                                'StartForeignTarget',
                                                'ReadForeignOperation',
                                                'ReadWithoutOperator',
                                                'UnknownPreparedReadback',
                                                'UnwrittenPreparedRecord'],
                                    'properties': ['TypeOK',
                                                   'PinnedPlan',
                                                   'JournalBeforeLaunch',
                                                   'PreparedIdentity',
                                                   'PausedExactControl',
                                                   'ExactAttachment',
                                                   'AttachBeforeActivation',
                                                   'PrivateAgentReadiness',
                                                   'PublicationAdmission',
                                                   'IndependentCleanup',
                                                   'ObservedContainment',
                                                   'ReleaseAfterContainment',
                                                   'NetworkOwnership',
                                                   'FrozenNetwork',
                                                   'AuthorizedDelete',
                                                   'CreatorAdmission',
                                                   'NoReconstruction',
                                                   'UncertainDebtRetained',
                                                   'ConfigurationAdmission',
                                                   'ConfiguredIsNotReady',
                                                   'TypedProvider',
                                                   'SavedAssociation',
                                                   'NoRebinding',
                                                   'SavedOperationAuthority'],
                                    'source_scope': ['internal/aws/ec2_cloudformation_adapter_linux.go',
                                                     'internal/aws/ec2_cloudformation_runtime_linux.go',
                                                     'internal/aws/ec2_vm_backend_linux.go',
                                                     'internal/aws/ec2_vm_nic_guard.go',
                                                     'internal/aws/ec2_vm_nic_linux.go',
                                                     'internal/aws/ec2_vm_qga_readiness_linux.go',
                                                     'internal/workload/guestbundle/bundle.go',
                                                     'internal/workload/guestbundle/embedded.go',
                                                     'internal/workload/guestprocess/attempt_linux.go',
                                                     'internal/workload/guestprocess/attempt_other.go',
                                                     'internal/workload/guestprocess/nic_linux.go',
                                                     'internal/workload/guestprocess/process.go',
                                                     'internal/workload/guestprocess/process_linux.go',
                                                     'internal/workload/guestprocess/process_other.go',
                                                     'internal/workload/guestprocess/supervisor_linux.go',
                                                     'internal/workload/guestseed/network.go',
                                                     'internal/workload/guestseed/seed.go',
                                                     'internal/workload/guestvm/control.go',
                                                     'internal/workload/guestvm/launch.go',
                                                     'internal/workload/guestvm/network.go',
                                                     'internal/workload/guestvm/nic_linux.go',
                                                     'internal/workload/guestvm/prepare.go',
                                                     'internal/workload/guestvm/recover.go',
                                                     'internal/workload/qmp/client.go',
                                                     'internal/workload/qmp/protocol.go',
                                                     'internal/aws/cloudformation_instance.go',
                                                     'internal/aws/ec2_instance_cloudformation.go',
                                                     'internal/aws/ec2_network.go',
                                                     'internal/aws/ec2_network_actor.go',
                                                     'internal/aws/ec2_network_cloudformation.go',
                                                     'cmd/simulator/battle_runtime_linux.go',
                                                     'cmd/simulator/battle_runtime_other.go',
                                                     'internal/server/battle_runtime_linux.go',
                                                     'internal/server/ssm_runtime_capabilities.go',
                                                     'internal/server/battle_capabilities_linux.go',
                                                     'cmd/simulator/main.go',
                                                     'internal/aws/cloudformation.go',
                                                     'internal/server/server.go',
                                                     'internal/server/sakura_provider.go']},
         'IAMConditionEvaluation': {'bounds': '15 immutable finite input witnesses, '
                                              'parse/context/match/resolve phases, two context keys, '
                                              'literal/variable/glob representatives, matching explicit '
                                              'Deny and legacy nil-context Deny case. No request loop.',
                                    'mutants': ['AdmitOperator',
                                                'AdmitNull',
                                                'AdmitDuplicate',
                                                'AdmitVariable',
                                                'MissingContextAccepted',
                                                'MissingContextDropsDeny',
                                                'RequestContextOverride',
                                                'VariableExpandedAsWildcard',
                                                'EqualsBecomesGlob',
                                                'DenyLosesPrecedence',
                                                'LikeDisabled',
                                                'MissingClauseDropped'],
                                    'properties': ['TypeOK',
                                                   'StrictAdmission',
                                                   'TrustedContextRequired',
                                                   'AbsentContextDenyNotDiscarded',
                                                   'MissingOrDuplicateKeyRefuses',
                                                   'AllTermsRequired',
                                                   'ProviderContextOnly',
                                                   'VariableIsLiteral',
                                                   'EqualityNotGlob',
                                                   'ExplicitDenyPrecedence',
                                                   'AllowedWitness'],
                                    'source_scope': ['internal/core/policy_conditions.go',
                                                     'internal/core/policy.go']},
         'InstanceMetadataTokens': {'bounds': 'One instance-owned token slot (abstracts capacity eight), '
                                              'one request, one foreign token, time 0..2, lifetime2, '
                                              'TTL1or2. Every finite interleaving; no depth cutoff. '
                                              'Issue/read checks linearize at request admission, not '
                                              'response emission.',
                                    'mutants': ['ForeignToken',
                                                'ExpiredToken',
                                                'CredentialEndpoint',
                                                'WrongReadMethod',
                                                'WrongTokenMethod',
                                                'InvalidTTL',
                                                'ForwardedToken',
                                                'RandomFailure',
                                                'IssueAfterClose',
                                                'ReadAfterClose',
                                                'UnboundedExpiry'],
                                    'properties': ['TypeOK',
                                                   'TokenAdmission',
                                                   'InstanceBoundRead',
                                                   'NoCredentials',
                                                   'BoundedExpiry'],
                                    'source_scope': ['internal/aws/ec2_metadata_service_linux.go',
                                                     'internal/aws/ec2_instance_alias.go']},
         'NativeCommandIssuer': {'bounds': 'Three disjoint finite scenarios: one parent '
                                           'intent/token/worker with two delivery opportunities and a '
                                           'single crash/restart, two lease incarnations and three reply '
                                           'classes; three immutable policy revisions for document+target; '
                                           'one two-item page with one append or binding/expiry/record '
                                           'change.',
                                 'mutants': ['IgnoreOwnerSeal',
                                             'IgnoreLeaseIncarnation',
                                             'OrphanWorkerRestart',
                                             'InventSuccessFromLostReply',
                                             'InventSuccessFromPending',
                                             'MixedPolicyRevisions',
                                             'ReserveAfterEffect',
                                             'DuplicateGuestEffect',
                                             'PageRole',
                                             'PageRegion',
                                             'PageFilter',
                                             'PageSize',
                                             'PageExpiry',
                                             'PageRetainedRequest',
                                             'PageUsesLiveItems'],
                                 'properties': ['TypeOK',
                                                'ReservationBeforeEffect',
                                                'AtMostOneEffect',
                                                'CurrentOwnerFence',
                                                'RestartNoWorker',
                                                'WorkerRequiresReservation',
                                                'NoReplayAfterRestart',
                                                'KnownExitOnly',
                                                'NoCleanupFromLostReply',
                                                'JoinAfterPublication',
                                                'SinglePolicySnapshot',
                                                'PageScope',
                                                'PageRecordRevalidated',
                                                'PageSnapshotImmutable'],
                                 'source_scope': ['internal/aws/ec2_command_backend_linux.go',
                                                  'internal/aws/ssm_commands_native.go',
                                                  'internal/aws/ssm_commands_native_state.go',
                                                  'internal/aws/ssm_commands_native_wire.go']},
         'OwnedClockQualification': {'bounds': 'One owned process and pending sample, two process '
                                               'incarnations, logical time0..5, expiry4 and original '
                                               'deadline3; Boolean namespace/FD/platform/cryptographic '
                                               'witnesses; one retained command.',
                                     'mutants': ['ArbitraryHTTPProof',
                                                 'ForeignNamespace',
                                                 'UncheckedInheritedFD',
                                                 'UnsupportedPlatform',
                                                 'SampleWrongIncarnation',
                                                 'WrongSampleMAC',
                                                 'ReplayedSampleNonce',
                                                 'ForeignSampleDomain',
                                                 'UnboundedSample',
                                                 'ExpiredSample',
                                                 'LateSampleAfterStop',
                                                 'WrongResponseMAC',
                                                 'ReplayedResponseNonce',
                                                 'ForeignResponseDomain',
                                                 'ChangedResponseRequest',
                                                 'StopKeepsQualification',
                                                 'V1WallAccepted',
                                                 'StartAfterOwnerRotation',
                                                 'UnqualifiedStart',
                                                 'RenewAtReceipt',
                                                 'RoundDeadlineLater',
                                                 'ExpiredReadBlocked',
                                                 'ReadAfterStop',
                                                 'ReadAfterRotation'],
                                     'properties': ['TypeOK',
                                                    'OwnedProof',
                                                    'QualifiedOnlyCurrent',
                                                    'AuthenticatedResponses',
                                                    'StopInvalidates',
                                                    'V2Admission',
                                                    'ConservativeOriginalDeadline',
                                                    'RetainedReadAfterExpiry',
                                                    'ReadRequiresCurrentOwner'],
                                     'source_scope': ['internal/workload/probeclock/bootstrap_linux.go',
                                                      'internal/workload/probeclock/bootstrap_other.go',
                                                      'internal/workload/probeclock/clock.go',
                                                      'internal/workload/probeclock/clock_linux.go',
                                                      'internal/workload/probeclock/clock_other.go',
                                                      'internal/workload/probeclock/http.go',
                                                      'internal/workload/guesttcp/http_probe_command_v2.go',
                                                      'cmd/simulator/main.go']},
         'ParticipantSessionLifecycle': {'bounds': 'One session with two driver generations, one Resume '
                                                   'per generation, two-valued identity/role/target/origin '
                                                   'witnesses, one-use expiring tokens, three '
                                                   'independently draining guest tasks, Boolean reviewed '
                                                   'image pin/private channel/explicit opt-in and '
                                                   'independent exclusive overlay packaging verdicts. No '
                                                   'pruning constraints or fairness.',
                                         'mutants': ['MissingImagePin',
                                                     'QGAFallback',
                                                     'ImplicitSessionOptIn',
                                                     'UnverifiedOverlay',
                                                     'OverwriteOverlay',
                                                     'StaleChannelToken',
                                                     'ExpiredHandoff',
                                                     'OperatorAdmission',
                                                     'CredentialRebind',
                                                     'RoleGenerationRebind',
                                                     'TargetRebind',
                                                     'CrossOriginChannel',
                                                     'RevokedAdmission',
                                                     'ChannelReplay',
                                                     'ExpiredChannel',
                                                     'HandoffReplay',
                                                     'WrongHello',
                                                     'ReadyAfterClose',
                                                     'PublishAfterRevoke',
                                                     'ResumeNewDriver',
                                                     'ResumeAfterShutdown',
                                                     'ExitBeforeDrain',
                                                     'ExitOnUncertainClose',
                                                     'TerminalBeforeJoin',
                                                     'FabricatedExit',
                                                     'FreshBeforeJoin'],
                                         'properties': ['TypeOK',
                                                        'PreparedEvidence',
                                                        'PreparationOptIn',
                                                        'PackageEvidence',
                                                        'BoundAdmission',
                                                        'CurrentAdmission',
                                                        'ChannelTokenBinding',
                                                        'HandoffExpiry',
                                                        'SingleUse',
                                                        'HelloEvidence',
                                                        'ReadyEvidence',
                                                        'NoPostRevokeOutput',
                                                        'ResumeKeepsDriver',
                                                        'NoResurrection',
                                                        'GuestDrainEvidence',
                                                        'TerminalEvidence',
                                                        'FreshEvidence',
                                                        'RevokeIsNotExit'],
                                         'source_scope': ['guest/ssm-relay/io_linux.go',
                                                          'guest/ssm-relay/main_linux.go',
                                                          'guest/ssm-relay/tenkacloud-session-relay.service',
                                                          'internal/aws/ec2_session_backend_linux.go',
                                                          'internal/aws/ssm_session_auth.go',
                                                          'internal/aws/ssm_sessions.go',
                                                          'internal/aws/ssm_sessions_native.go',
                                                          'internal/aws/ssm_sessions_shutdown.go',
                                                          'internal/aws/ssm_sessions_state.go',
                                                          'internal/aws/sts_terminal_binding.go',
                                                          'internal/server/terminal_channel.go',
                                                          'internal/server/terminal_handoff.go',
                                                          'internal/server/terminal_json.go',
                                                          'internal/workload/guestsession/driver.go',
                                                          'internal/workload/guestsession/protocol.go',
                                                          'internal/workload/guestvm/session.go',
                                                          'scripts/guest-image/session-overlay.py',
                                                          'internal/aws/ssm.go',
                                                          'internal/aws/sts_signature.go',
                                                          'internal/server/access.go',
                                                          'internal/server/server.go']},
         'TerminalClientEpoch': {'bounds': 'Two sequential mounted components, at most two explicitly '
                                           'invoked native operations, one retained pending response and '
                                           'one current channel with stale-callback input. No fairness or '
                                           'state pruning. Remote result and local disposition '
                                           'independent.',
                                 'mutants': ['LateNativeReceipt',
                                             'ReplacementReceipt',
                                             'StaleSocketCallback',
                                             'OpenTokenReplay',
                                             'UnmountedUI',
                                             'ReplacementUI',
                                             'DisposeFabricatesTermination',
                                             'AmbiguousStartRetry',
                                             'DiscardClearsKnownSession'],
                                 'properties': ['TypeOK',
                                                'ReceiptEvidence',
                                                'SocketEvidence',
                                                'SingleOpen',
                                                'UIEvidence',
                                                'DisposeNotRemoteTermination',
                                                'NoAutomaticStartRetry',
                                                'DiscardPreservesIdentity'],
                                 'source_scope': ['tools/console/src/ssm-terminal-client.mjs',
                                                  'tools/console/src/ssm-terminal.jsx',
                                                  'tools/console/src/main.jsx',
                                                  'internal/server/web/app.js']}}
        registry_path = self.root / 'formal/models.json'
        original = registry_path.read_text()
        for name, pins in expected.items():
            entry = next(m for m in json.loads(original)['models'] if m['id'] == name)
            for field in ('properties', 'source_scope', 'bounds'):
                self.assertEqual(entry[field], pins[field])
            self.assertEqual([m['id'] for m in entry['mutants']], pins['mutants'])
            config_path = self.root / entry['config']
            config = config_path.read_text()
            for field in ('properties', 'mutants', 'source_scope'):
                for item in entry[field]:
                    changed = json.loads(original)
                    target = next(m for m in changed['models'] if m['id'] == name)
                    target[field].remove(item)
                    if field == 'properties':
                        target['mutants'] = [m for m in target['mutants'] if m['expect_invariant'] != item]
                        modified = config.replace('INVARIANTS ' + ' '.join(entry['properties']), 'INVARIANTS ' + ' '.join(p for p in entry['properties'] if p != item))
                        if modified == config:
                            modified = config.replace('INVARIANT ' + item + '\n', '')
                        self.assertNotEqual(modified, config)
                        config_path.write_text(modified)
                    registry_path.write_text(json.dumps(changed))
                    with self.subTest(model=name, field=field, item=item), self.assertRaises(GateError):
                        load_registry(self.root)
                    config_path.write_text(config)
                    registry_path.write_text(original)
            for field, value in (('bounds', 'smaller unreviewed universe'), ('limits', ''), ('source_scope', entry['source_scope'] + ['go.mod'])):
                changed = json.loads(original)
                next(m for m in changed['models'] if m['id'] == name)[field] = value
                registry_path.write_text(json.dumps(changed))
                with self.subTest(model=name, field=field), self.assertRaises(GateError):
                    load_registry(self.root)
                registry_path.write_text(original)
            spec_changed = config.replace('SPECIFICATION Spec', 'SPECIFICATION MissingSpec') if 'SPECIFICATION Spec' in config else config.replace('INIT Init', 'INIT MissingInit')
            for altered in (spec_changed, config + '\nCONSTANT Unreviewed = 1\n', config.replace('CHECK_DEADLOCK FALSE', 'CHECK_DEADLOCK TRUE')):
                self.assertNotEqual(config, altered)
                config_path.write_text(altered)
                with self.subTest(model=name, configuration=altered), self.assertRaises(GateError):
                    load_registry(self.root)
                config_path.write_text(config)
            for relative in (entry['model'], entry['config']):
                path = self.root / relative
                content = path.read_bytes()
                path.unlink()
                with self.subTest(model=name, missing=relative), self.assertRaises(GateError):
                    load_registry(self.root)
                path.write_bytes(content)
            changed = json.loads(original)
            changed['models'] = [m for m in changed['models'] if m['id'] != name]
            registry_path.write_text(json.dumps(changed))
            model_path = self.root / entry['model']
            source = model_path.read_bytes()
            model_path.unlink()
            config_path.unlink()
            with self.subTest(model=name, whole_registration=True), self.assertRaisesRegex(GateError, 'required existing model was removed'):
                load_registry(self.root)
            model_path.write_bytes(source)
            config_path.write_text(config)
            registry_path.write_text(original)

    def test_battle_integration_corrupted_controls_are_rejected(self):
        names = ['CanonicalVMComposition',
         'InstanceMetadataTokens',
         'IAMConditionEvaluation',
         'NativeCommandIssuer',
         'ParticipantSessionLifecycle',
         'OwnedClockQualification',
         'TerminalClientEpoch']
        registry_path = self.root / 'formal/models.json'
        original = registry_path.read_text()
        for name in names:
            for corruption in ('anchor', 'count', 'replacement', 'invariant'):
                changed = json.loads(original)
                entry = next(m for m in changed['models'] if m['id'] == name)
                mutant = entry['mutants'][0]
                if corruption == 'anchor':
                    mutant['edits'][0]['old'] = 'UNREVIEWED MISSING ANCHOR'
                elif corruption == 'count':
                    mutant['edits'][0]['count'] += 1
                elif corruption == 'replacement':
                    # Still a nonempty different replacement: schema-only checks
                    # cannot substitute for the exact reviewed control.
                    mutant['edits'][0]['new'] += ' \\* unreviewed replacement'
                else:
                    mutant['expect_invariant'] = 'TypeOK'
                registry_path.write_text(json.dumps(changed))
                with self.subTest(model=name, corruption=corruption), self.assertRaises(GateError):
                    load_registry(self.root)
                registry_path.write_text(original)

    def test_dynamodb_workers_are_bounded_for_whole_family_without_timeout_fallback(self):
        jar = self.root / 'fixture.jar'
        jar.write_bytes(b'controlled worker-command fixture only, not TLC')
        (self.root / 'formal/tla2tools.sha256').write_text(hashlib.sha256(jar.read_bytes()).hexdigest() + '  tla2tools.jar\n')
        validated = load_registry(self.root)
        # Reorder a fully validated registry only to inspect each family's first
        # baseline/control invocation. No actual TLC acceptance is claimed.
        for name, workers in (('DynamoDBAdmission', '2'), ('ObjectStore', '1')):
            registry = dict(validated)
            registry['models'] = sorted(validated['models'], key=lambda m: m['id'] != name)
            reports = self.root / ('worker-fixture-' + name)
            results = [subprocess.CompletedProcess([], 0, 'Model checking completed. No error has been found.', ''), subprocess.TimeoutExpired('fixture', 60)]
            with patch('formal_model_gate.load_registry', return_value=registry), patch('formal_model_gate.shutil.which', return_value='/fixture/java'), patch('formal_model_gate.subprocess.run', side_effect=results) as process:
                with self.assertRaisesRegex(GateError, 'TLC could not finish'):
                    run_suite(self.root, jar, reports)
            self.assertEqual(process.call_count, 2)  # No retry or fallback.
            for call in process.call_args_list:
                command = call.args[0]
                self.assertEqual(command[command.index('-workers') + 1], workers)
                self.assertEqual(command.count('-workers'), 1)
                self.assertEqual(call.kwargs['timeout'], 60)
                self.assertNotIn('-depth', command)
                self.assertNotIn('-simulate', command)
            evidence = json.loads((reports / 'evidence.json').read_text())
            self.assertFalse(evidence['success'])
            self.assertEqual(len(evidence['runs']), 1)
            self.assertEqual(evidence['failure']['deadline_seconds'], 60)



if __name__ == '__main__':
    unittest.main()
