#!/usr/bin/env python3
import copy
import tempfile
import unittest
from unittest.mock import patch
from software_factory import Factory

BASE = 'a' * 40
HEAD = 'b' * 40

class FactoryTests(unittest.TestCase):
    def setUp(self):
        self.acceptance = patch('software_factory.verify_acceptance', return_value={})
        self.acceptance.start()
        self.diff = patch('software_factory.candidate_paths', return_value=['internal/aws/sqs_store.go'])
        self.diff.start()
        self.addCleanup(self.diff.stop)
        self.addCleanup(self.acceptance.stop)
        self.tmp = tempfile.TemporaryDirectory()
        self.f = Factory(self.tmp.name + '/ledger.db')
        self.finding = dict(provider='aws', operation='DeleteMessage', scenario='missing resource',
            expected='not found', actual='success', reference='official specification URL',
            reproduction='local regression test', base=BASE, inventory_checked_at='2026-11-12T00:00:00Z',
            related_work=[], oracle='official-specification', paths=['internal/aws/sqs_store.go'],
            required_checks=['make before-commit', 'focused regression'], acceptance=['sqs-lifecycle'])
        self.key = self.f.add(copy.deepcopy(self.finding))['id']

    def tearDown(self):
        self.f.db.close()
        self.tmp.cleanup()

    def candidate(self):
        self.f.claim(self.key, 'fixer', BASE)
        self.f.attempt(self.key, 'fixer')
        self.f.candidate(self.key, 'fixer', HEAD)

    def report(self):
        return dict(head=HEAD, acceptance_report='separately-tested-receipt.json', reviewer='reviewer', findings=[], changed_paths=['internal/aws/sqs_store.go'],
                    checks={name: dict(status='passed', evidence='log at exact head')
                            for name in self.finding['required_checks']})

    def test_success_is_ready_not_merged(self):
        self.candidate()
        self.assertEqual(self.f.review(self.key, self.report())['state'], 'ready')

    def test_execution_receipt_required_and_failure_cannot_ready(self):
        self.candidate()
        report = self.report()
        del report['acceptance_report']
        with self.assertRaises(ValueError): self.f.review(self.key, report)
        with patch('software_factory.verify_acceptance', side_effect=ValueError('skip')):
            with self.assertRaises(ValueError): self.f.review(self.key, self.report())
        self.assertEqual(self.f.get(self.key)['state'], 'review')

    def test_unrelated_suite_and_invented_diff_are_rejected(self):
        for fields in ({'operation':'RunInstances'}, {'provider':'azure'}, {'paths':['internal/server/server.go']}):
            finding=copy.deepcopy(self.finding);finding.update(fields)
            with self.assertRaises(ValueError):self.f.add(finding)
        self.candidate()
        with patch('software_factory.candidate_paths', return_value=['internal/aws/sqs.go']):
            with self.assertRaises(ValueError):self.f.review(self.key,self.report())

    def test_dedup_independent_of_base_and_prose(self):
        f = copy.deepcopy(self.finding)
        f.update(base=HEAD, actual='different detail')
        self.assertTrue(self.f.add(f)['duplicate'])

    def test_claim_atomic_across_connections_and_paths(self):
        self.f.claim(self.key, 'fixer', BASE)
        other = Factory(self.tmp.name + '/ledger.db')
        try:
            with self.assertRaises(ValueError): other.claim(self.key, 'other', BASE)
            f = copy.deepcopy(self.finding)
            f.update(scenario='different', paths=['internal/aws/sqs_store.go'])
            key = other.add(f)['id']
            with self.assertRaises(ValueError): other.claim(key, 'other', BASE)
        finally:
            other.db.close()

    def test_stale_or_related_work_rejected(self):
        with self.assertRaises(ValueError): self.f.claim(self.key, 'fixer', HEAD)
        f = copy.deepcopy(self.finding)
        f.update(scenario='other', related_work=['PR 1'])
        key = self.f.add(f)['id']
        with self.assertRaises(ValueError): self.f.claim(key, 'fixer', BASE)

    def test_review_rejects_missing_stale_self_and_out_of_scope(self):
        self.candidate()
        for change in ({'head': BASE}, {'reviewer': 'fixer'}, {'checks': {}},
                       {'changed_paths': ['internal/else/test.go']}):
            report = self.report()
            report.update(change)
            with self.assertRaises(ValueError): self.f.review(self.key, report)
            self.assertEqual(self.f.get(self.key)['state'], 'review')

    def test_unrun_never_ready_and_budget_bounded(self):
        self.candidate()
        report = self.report()
        report['checks']['focused regression']['status'] = 'unrun'
        self.assertEqual(self.f.review(self.key, report)['state'], 'claimed')
        self.f.attempt(self.key, 'fixer')
        self.f.candidate(self.key, 'fixer', HEAD)
        self.assertEqual(self.f.review(self.key, report)['state'], 'blocked')
        with self.assertRaises(ValueError): self.f.attempt(self.key, 'fixer')

    def test_denial_stops_no_automatic_reclaim(self):
        self.f.claim(self.key, 'fixer', BASE)
        self.assertEqual(self.f.stop(self.key, 'fixer', 'permission denied')['state'], 'blocked')
        with self.assertRaises(ValueError): self.f.attempt(self.key, 'fixer')

    def test_close_releases_paths_and_preserves_evidence(self):
        self.candidate()
        self.f.review(self.key, self.report())
        f = copy.deepcopy(self.finding)
        f['scenario'] = 'next issue'
        key = self.f.add(f)['id']
        with self.assertRaises(ValueError): self.f.claim(key, 'next', BASE)
        for owner, head in (('wrong', HEAD), ('fixer', BASE)):
            with self.assertRaises(ValueError): self.f.close(self.key, owner, head, 'verified PR')
        closed = self.f.close(self.key, 'fixer', HEAD, 'verified PR URL and merge SHA')
        self.assertEqual(closed['state'], 'closed')
        self.assertIsNotNone(closed['evidence'])
        self.assertEqual(self.f.claim(key, 'next', BASE)['state'], 'claimed')
        self.assertEqual(self.f.db.execute('SELECT action FROM audit').fetchall(), [('close',)])
        with self.assertRaises(ValueError): self.f.claim(self.key, 'fixer', BASE)

    def test_refresh_reconciles_base_and_related_work_with_audit(self):
        f = copy.deepcopy(self.finding)
        f.update(scenario='blocked by related work', related_work=['PR1'])
        key = self.f.add(f)['id']
        old = self.f.get(key)
        f.update(base=HEAD, related_work=[], inventory_checked_at='2026-11-13T00:00:00Z')
        self.f.refresh(key, f, 'integrator', 'PR1 closed, scope reviewed on new base', old['fingerprint'])
        with self.assertRaises(ValueError):
            self.f.refresh(key, f, 'integrator', 'stale update', old['fingerprint'])
        self.assertEqual(self.f.claim(key, 'fixer', HEAD)['state'], 'claimed')
        self.assertEqual(self.f.db.execute('SELECT action FROM audit').fetchall(), [('refresh',)])

    def test_refresh_cannot_reset_attempts_or_denial(self):
        self.f.claim(self.key, 'fixer', BASE)
        self.f.attempt(self.key, 'fixer')
        for state in ('active', 'denied'):
            if state == 'denied': self.f.stop(self.key, 'fixer', 'permission denied')
            old = self.f.get(self.key)
            with self.assertRaises(ValueError):
                self.f.refresh(self.key, copy.deepcopy(self.finding), 'integrator', 'retry', old['fingerprint'])
            self.assertEqual(self.f.get(self.key)['attempts'], 1)

    def test_paths_fail_closed(self):
        for p in ('../secret', '/tmp/file', '.env', 'internal/../x', 'internal//x'):
            f = copy.deepcopy(self.finding)
            f['paths'] = [p]
            with self.assertRaises(ValueError): self.f.add(f)

if __name__ == '__main__':
    unittest.main()
