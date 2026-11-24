#!/usr/bin/env python3
import copy
import json
from pathlib import Path
import shutil
import sys
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import factory_acceptance as gate

class AcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)/'repo'
        self.root.mkdir()
        shutil.copytree(gate.ROOT/'test/factory', self.root/'test/factory')
        subprocess.run(['git','init','-q',str(self.root)], check=True)
        self.git('config','user.email','test@example.invalid')
        self.git('config','user.name','test')
        self.git('add','.')
        self.git('commit','-qm','oracle')
        self.head = self.git('rev-parse','HEAD')
        spec = gate.SUITES['sqs-lifecycle']
        self.events = [dict(Action='run',Package=spec['package'],Test=spec['test']),
                       dict(Action='pass',Package=spec['package'],Test=spec['test']),
                       dict(Action='pass',Package=spec['package'])]
        self.path=Path(self.tmp.name)/'evidence.json'
        self.logpath=self.path.with_suffix('.jsonl')
        self.logpath.write_bytes(self.log())
        self.report={'version':1,'head':self.head,'source_sha256':gate.source_hash(self.root),'status':'passed',
                     'checks':{'sqs-lifecycle':{'status':'passed','argv':spec['argv'],'executed_argv':gate.executable_command(spec['argv']),'returncode':0,
                     'log':self.logpath.name,'log_sha256':gate.digest(self.log())}}}
        self.save()
    def git(self,*args):return gate.git(self.root,*args)
    def log(self):return ('\n'.join(json.dumps(e) for e in self.events)+'\n').encode()
    def save(self):self.path.write_text(json.dumps(self.report))
    def verify(self):return gate.verify(self.path,self.head,['sqs-lifecycle'],self.root)
    def test_exact_head_receipt(self):self.verify()
    def test_exit_zero_without_test_is_rejected(self):
        for events in [[], self.events[2:],self.events[:2],self.events+self.events]:
            with self.assertRaises(ValueError):gate.verdict(('\n'.join(json.dumps(e) for e in events)).encode(),'sqs-lifecycle',0)
    def test_skip_failure_timeout_wrong_package(self):
        for action in ['skip','fail']:
            self.events[1]['Action']=action
            with self.assertRaises(ValueError):gate.verdict(self.log(),'sqs-lifecycle',0)
        for code in [1,-1]:
            with self.assertRaises(ValueError):gate.verdict(self.log(),'sqs-lifecycle',code)
        for e in self.events:e['Package']='foreign'
        with self.assertRaises(ValueError):gate.verdict(self.log(),'sqs-lifecycle',0)
    def test_head_pin_set_command_status_are_bound(self):
        for field,value in [('head','a'*40),('source_sha256','x'),('status','skipped'),('version',0)]:
            old=self.report[field];self.report[field]=value;self.save()
            with self.assertRaises(ValueError):self.verify()
            self.report[field]=old
        for field,value in [('argv',['true']),('status','unrun'),('log','../foreign'),('returncode',1)]:
            old=self.report['checks']['sqs-lifecycle'][field];self.report['checks']['sqs-lifecycle'][field]=value;self.save()
            with self.assertRaises(ValueError):self.verify()
            self.report['checks']['sqs-lifecycle'][field]=old
        self.report['checks']={};self.save()
        with self.assertRaises(ValueError):self.verify()
    def test_tampering_and_dirty_checkout_refused(self):
        self.logpath.write_bytes(b'changed')
        with self.assertRaises(ValueError):self.verify()
        (self.root/'untracked').write_text('x')
        with self.assertRaises(ValueError):gate.clean_head(self.root)
    def test_upstream_pin_refused(self):
        (self.root/'test/factory/upstream/LICENSE').write_text('changed')
        with self.assertRaises(ValueError):gate.source_hash(self.root)
    def test_unknown_suite_and_tracked_output_refused_before_exec(self):
        with self.assertRaises(ValueError):gate.run(self.root,self.path,['arbitrary'])
        with self.assertRaises(ValueError):gate.run(self.root,self.root/'test/factory/upstream/LICENSE',['sqs-lifecycle'])
    def test_executor_records_real_process_and_refuses_zero_work(self):
        spec = copy.deepcopy(gate.SUITES['sqs-lifecycle'])
        spec['argv'] = [sys.executable, '-c', 'print('+repr(self.log().decode())+')']
        with patch.dict(gate.SUITES, {'sqs-lifecycle':spec}):
            result=gate.run(self.root,self.path,['sqs-lifecycle'])
            self.assertEqual(result['status'],'passed')
            self.verify()
            spec['argv']=[sys.executable,'-c','pass']
            with self.assertRaises(ValueError):gate.run(self.root,self.path,['sqs-lifecycle'])
            self.assertEqual(json.loads(self.path.read_text())['status'],'failed')
    def test_executor_timeout_fails_and_writes_failure(self):
        spec=copy.deepcopy(gate.SUITES['sqs-lifecycle'])
        spec['argv']=[sys.executable,'-c','import time; time.sleep(5)']
        with patch.dict(gate.SUITES, {'sqs-lifecycle':spec}):
            with self.assertRaises(ValueError):gate.run(self.root,self.path,['sqs-lifecycle'],timeout=.02)
        self.assertEqual(json.loads(self.path.read_text())['status'],'failed')

    def test_ambient_overlay_workspace_config_and_credentials_are_not_inherited(self):
        with patch.dict('os.environ', {'GOFLAGS':'-overlay=/tmp/other.json', 'GOWORK':'/tmp/go.work',
                                      'GOENV':'/tmp/go.env', 'GOTOOLCHAIN':'auto',
                                      'AWS_SECRET_ACCESS_KEY':'secret', 'HTTPS_PROXY':'https://proxy',
                                      'GODEBUG':'unexpected', 'GO111MODULE':'off'}):
            env=gate.execution_environment()
            self.assertEqual(env['GOFLAGS'],'')
            self.assertEqual(env['GOWORK'],'off')
            self.assertEqual(env['GOENV'],'off')
            self.assertEqual(env['GOTOOLCHAIN'],'local')
            for key in ('AWS_SECRET_ACCESS_KEY','HTTPS_PROXY','GODEBUG','GO111MODULE'):
                self.assertNotIn(key,env)
    def test_make_go_override_is_used_as_single_executable(self):
        with patch.dict('os.environ', {'GO':sys.executable}):
            self.assertEqual(gate.executable_command(['go','test'])[0],str(Path(sys.executable).resolve()))
        with patch.dict('os.environ', {'GO':'go -overlay=foreign'}):
            with self.assertRaises(ValueError):gate.executable_command(['go','test'])

    def test_git_overrides_cannot_redirect_head_or_index(self):
        with patch.dict('os.environ', {'GIT_DIR':'/nonexistent/git', 'GIT_WORK_TREE':'/nonexistent',
                                      'GIT_INDEX_FILE':'/nonexistent/index', 'GIT_CONFIG_COUNT':'1',
                                      'GIT_CONFIG_KEY_0':'core.worktree','GIT_CONFIG_VALUE_0':'/wrong'}):
            self.assertEqual(gate.clean_head(self.root),self.head)
            self.assertFalse(any(k.startswith('GIT_') for k in gate.git_environment()))
    def test_candidate_paths_from_real_git_and_scope_binding(self):
        (self.root/'new').write_text('data');self.git('add','.');self.git('commit','-qm','candidate')
        head=self.git('rev-parse','HEAD')
        self.assertEqual(gate.candidate_paths(self.head,head,self.root),['new'])
        with self.assertRaises(ValueError):gate.candidate_paths(head,self.head,self.root)
        gate.validate_scope(['sqs-lifecycle'],'aws','DeleteMessage',['internal/aws/sqs_store.go'])
        for operation,paths in [('RunInstances',['internal/aws/sqs_store.go']),('DeleteMessage',['internal/server/server.go']),('DeleteMessage',['internal/aws/sqs_x/foreign_test.go'])]:
            with self.assertRaises(ValueError):gate.validate_scope(['sqs-lifecycle'],'aws',operation,paths)

    def test_rename_keeps_both_deleted_and_added_ownership_paths(self):
        (self.root/'old').write_text('same');self.git('add','.');self.git('commit','-qm','old')
        base=self.git('rev-parse','HEAD')
        self.git('mv','old','new');self.git('commit','-qm','move')
        self.assertEqual(gate.candidate_paths(base,self.git('rev-parse','HEAD'),self.root),['new','old'])

    def test_empty_duplicate_suites_refused(self):
        for suites in [[],['sqs-lifecycle']*2]:
            with self.assertRaises(ValueError):gate.verify(self.path,self.head,suites,self.root)
    def test_subnet_registration_binds_fixed_command_and_scope(self):
        suite = 'ec2-describe-subnets'
        spec = gate.SUITES[suite]
        self.assertEqual(spec['argv'], ['go', 'test', '-json', '-count=1', '-run',
                                       '^TestEC2DescribeSubnetsContract$', './internal/aws'])
        vpc_dispatch = 'internal/aws/ec2_vpc.go'
        gate.validate_scope([suite], 'aws', 'DescribeSubnets',
                            ['internal/aws/ec2_read.go', vpc_dispatch])
        for operation, path in (('CreateVpc', vpc_dispatch),
                                ('DeleteVpc', vpc_dispatch),
                                ('DeleteMessage', vpc_dispatch),
                                ('CreateSubnet', 'internal/aws/ec2_read.go'),
                                ('DescribeSubnets', 'internal/aws/sqs_store.go'),
                                ('DeleteMessage', 'internal/aws/ec2_read.go'),
                                ('DescribeSubnets', 'internal/server/server.go')):
            with self.subTest(operation=operation, path=path), self.assertRaises(ValueError):
                gate.validate_scope([suite, 'sqs-lifecycle'], 'aws', operation, [path])

    def test_subnet_oracle_and_named_result_cannot_be_substituted(self):
        suite = 'ec2-describe-subnets'
        spec = dict(gate.SUITES[suite])
        name, pinned = spec['oracle']
        self.assertEqual(pinned, '6443bc90b6fec1c7b8bf277cc090b00f6dd431fac3481d120fdefa0ccb09b2d9')
        spec['oracle'] = name, gate.digest(b'fixed fixture')
        oracle = self.root / name
        oracle.parent.mkdir(parents=True, exist_ok=True)
        with patch.dict(gate.SUITES, {suite: spec}):
            for data in (None, b'changed fixture', b'fixed fixture'):
                if data is not None:
                    oracle.write_bytes(data)
                if data == b'fixed fixture':
                    gate.verify_suite_oracles(self.root, [suite])
                else:
                    with self.assertRaisesRegex(ValueError, 'oracle changed or missing'):
                        gate.verify_suite_oracles(self.root, [suite])
        package = spec['package']
        def log(test, outcome):
            return b'\n'.join(json.dumps(event).encode() for event in (
                {'Action': 'run', 'Package': package, 'Test': test},
                {'Action': outcome, 'Package': package, 'Test': test},
                {'Action': 'pass', 'Package': package},
            ))
        gate.verdict(log(spec['test'], 'pass'), suite, 0)
        for result in (log('OtherTest', 'pass'), log(spec['test'], 'skip'), log(spec['test'], 'fail')):
            with self.assertRaises(ValueError):
                gate.verdict(result, suite, 0)

    def test_sakura_receipt_binds_oracle_hash_and_all_tests(self):
        suite = 'sakura-server-metadata'
        spec = gate.SUITES[suite]
        oracle = self.root / spec['oracle']
        oracle.parent.mkdir(parents=True)
        oracle.write_bytes((gate.ROOT/spec['oracle']).read_bytes())
        self.git('add','.'); self.git('commit','-qm','reviewed Sakura oracle')
        head = self.git('rev-parse','HEAD')
        events = [dict(Action=action,Package=spec['package'],Test=test)
                  for test in spec['tests'] for action in ('run','pass')]
        events.append(dict(Action='pass',Package=spec['package']))
        log = ('\n'.join(json.dumps(e) for e in events)+'\n').encode()
        self.logpath.write_bytes(log)
        entry = {'status':'passed','argv':spec['argv'],'executed_argv':gate.executable_command(spec['argv']),
                 'returncode':0,'log':self.logpath.name,'log_sha256':gate.digest(log),
                 'oracle_sha256':spec['oracle_sha256']}
        report = {'version':1,'head':head,'source_sha256':gate.source_hash(self.root),
                  'status':'passed','checks':{suite:entry}}
        self.path.write_text(json.dumps(report))
        gate.verify(self.path,head,[suite],self.root)
        for invalid in (None, 'changed'):
            entry['oracle_sha256'] = invalid
            self.path.write_text(json.dumps(report))
            with self.assertRaises(ValueError): gate.verify(self.path,head,[suite],self.root)
        entry['oracle_sha256'] = spec['oracle_sha256']
        self.path.write_text(json.dumps(report))
        oracle.write_text('changed'); self.git('add','.'); self.git('commit','-qm','altered oracle')
        report['head'] = self.git('rev-parse','HEAD'); self.path.write_text(json.dumps(report))
        with self.assertRaises(ValueError): gate.verify(self.path,report['head'],[suite],self.root)

class CloudDNSFactoryRegistrationTests(unittest.TestCase):
    suite = 'gcp-dns-atomic-lifecycle'
    paths = {
        'internal/gcp/dns/engine.go', 'internal/gcp/dns/model.go',
        'internal/gcp/dns/records.go', 'internal/gcp/dns/store.go',
        'internal/gcp/dns/operations.go', 'internal/gcp/dns/pagination.go',
        'internal/gcp/dns/factory_lifecycle_test.go', 'internal/gcp/dns/factory_sqlfault_test.go',
        'internal/gcp/dns/factory_observation_test.go', 'internal/gcp/dns/dns_test.go',
        'internal/gcp/dns/sql_faults_test.go',
    }

    def test_dns_is_opt_in_and_existing_defaults_unchanged(self):
        self.assertEqual(gate.DEFAULT_SUITES, ('sqs-lifecycle', 'ec2-describe-subnets'))
        for argv, wanted in (
            (['factory_acceptance.py'], ['sqs-lifecycle', 'ec2-describe-subnets']),
            (['factory_acceptance.py', '--suite', self.suite], [self.suite]),
        ):
            with patch.object(sys, 'argv', argv), patch.object(gate, 'run') as run, patch('builtins.print'):
                gate.main()
                self.assertEqual(run.call_args.args[2], wanted)
        spec = gate.SUITES[self.suite]
        self.assertEqual(spec['argv'], ['go', 'test', '-json', '-count=1', '-run',
                                       '^TestCloudDNSFactoryAtomicLifecycle$', './internal/gcp/dns'])
        self.assertEqual(spec['provider'], 'gcp-dns')
        self.assertEqual(spec['test'], 'TestCloudDNSFactoryAtomicLifecycle')
        self.assertEqual(spec['package'], 'github.com/susumutomita/TenkaCloudSimulator/internal/gcp/dns')
        self.assertEqual(gate.GCP_DNS_LEAF_PATHS, self.paths)
        self.assertEqual(spec['operations'], [
            'dns.managedZones.create', 'dns.managedZones.get', 'dns.managedZones.list',
            'dns.managedZones.delete', 'dns.changes.create', 'dns.changes.get',
            'dns.changes.list', 'dns.resourceRecordSets.get', 'dns.resourceRecordSets.list',
        ])

    def test_mixed_receipt_cannot_widen_operation_or_paths(self):
        suites = [*gate.DEFAULT_SUITES, self.suite]
        leaf = 'internal/gcp/dns/engine.go'
        for operation in gate.SUITES[self.suite]['operations']:
            gate.validate_scope(suites, 'gcp-dns', operation, sorted(self.paths))
        gate.validate_scope(suites, 'aws', 'SendMessage', ['internal/aws/sqs.go'])
        gate.validate_scope(suites, 'aws', 'DescribeSubnets', ['internal/aws/ec2_read.go'])
        for provider, operation, paths in (
            ('gcp', 'dns.changes.create', [leaf]),
            ('aws', 'dns.changes.create', [leaf]),
            ('gcp-dns', 'dns.policies.create', [leaf]),
            ('aws', 'SendMessage', ['internal/aws/sqs.go', leaf]),
            ('aws', 'DescribeSubnets', [leaf]),
            ('gcp-dns', 'dns.changes.create', [leaf, 'internal/server/cloud_storage.go']),
            ('gcp-dns', 'dns.changes.create', ['internal/gcp/dns/new_unreviewed.go']),
            ('gcp-dns', 'dns.changes.create', ['internal/gcp/dns/nested/engine.go']),
            ('gcp-dns', 'dns.changes.create', ['scripts/factory_acceptance.py']),
            ('gcp-dns', 'dns.changes.create', ['formal/applicability.json']),
            ('gcp-dns', 'dns.changes.create', ['test/e2e/gcp_dns_sdk.py']),
        ):
            with self.subTest(provider=provider, operation=operation, paths=paths):
                with self.assertRaises(ValueError):
                    gate.validate_scope(suites, provider, operation, paths)

    def test_receipt_requires_actual_primary_result(self):
        package = 'github.com/susumutomita/TenkaCloudSimulator/internal/gcp/dns'
        test = 'TestCloudDNSFactoryAtomicLifecycle'
        run = dict(Action='run', Package=package, Test=test)
        success = dict(Action='pass', Package=package, Test=test)
        end = dict(Action='pass', Package=package)
        def verdict(events):
            log = ('\n'.join(map(json.dumps, events)) + '\n').encode()
            gate.verdict(log, self.suite, 0)
        verdict([run, success, end])
        for wrong in ([end], [run, success], [run, success, success, end],
                      [run, dict(success, Action='skip'), end],
                      [run, dict(success, Test='AnotherTest'), end],
                      [run, dict(success, Package='wrong'), end]):
            with self.assertRaises(ValueError):
                verdict(wrong)

    def test_each_frozen_leaf_oracle_is_checked(self):
        spec = gate.SUITES[self.suite]
        actual = (spec['oracle'], *spec['support_oracles'])
        self.assertEqual(actual, (
            ('internal/gcp/dns/factory_lifecycle_test.go',
             'dfd5becd421b289cd942e388579e82c656fad37fb2096f609ebf547f1c961d7e'),
            ('internal/gcp/dns/factory_observation_test.go',
             '7a3ce38d150e7bc6cb6f0148b8446359e963c55c03e5750a81fa2f7924dfee00'),
            ('internal/gcp/dns/factory_sqlfault_test.go',
             'b9271b011769868f03da4753733b9e92b1c57fec1779ccdc19adad495d72b0e8'),
        ))
        root = Path('/synthetic-factory-fixture')
        data = {root / name: name.encode() for name, _ in actual}
        pins = [(name, gate.digest(data[root / name])) for name, _ in actual]
        mock_spec = {**spec, 'oracle': pins[0], 'support_oracles': tuple(pins[1:])}
        with patch.dict(gate.SUITES, {self.suite: mock_spec}), patch.object(
                Path, 'is_file', autospec=True, return_value=True), patch.object(
                Path, 'is_symlink', autospec=True, return_value=False), patch.object(
                Path, 'read_bytes', autospec=True, side_effect=data.__getitem__):
            gate.verify_suite_oracles(root, [self.suite])
            for name, _ in pins:
                path = root / name
                before = data[path]
                data[path] = b'changed oracle'
                try:
                    with self.assertRaisesRegex(ValueError, 'registered suite oracle changed or missing'):
                        gate.verify_suite_oracles(root, [self.suite])
                finally:
                    data[path] = before

class SakuraRegistrationTests(unittest.TestCase):
    suite = 'sakura-server-metadata'

    def events(self):
        spec = gate.SUITES[self.suite]
        return ([dict(Action=action, Package=spec['package'], Test=test)
                 for test in spec['tests'] for action in ('run', 'pass')]
                + [dict(Action='pass', Package=spec['package'])])

    def verdict(self, events, code=0):
        gate.verdict(('\n'.join(json.dumps(e) for e in events)).encode(), self.suite, code)

    def test_exact_command_names_path_and_default(self):
        spec = gate.SUITES[self.suite]
        self.assertEqual(gate.DEFAULT_SUITES, ('sqs-lifecycle', 'ec2-describe-subnets'))
        self.assertEqual(set(gate.SUITES), {'sqs-lifecycle', 'ec2-describe-subnets', 'gcp-dns-atomic-lifecycle', self.suite})
        self.assertEqual(spec['tests'], ['TestSakuraServerMetadataAcceptanceCRUD',
            'TestSakuraServerMetadataAcceptanceRefusals', 'TestSakuraServerMetadataComparatorControls'])
        self.assertEqual(spec['argv'], ['go', 'test', '-json', '-count=1', '-run',
            '^TestSakuraServerMetadata(AcceptanceCRUD|AcceptanceRefusals|ComparatorControls)$', './internal/sakura'])
        self.assertEqual(spec['oracle'], 'internal/sakura/server_metadata_acceptance_test.go')
        self.assertEqual(spec['operations'], ['CreateServer', 'ReadServer', 'FindServers', 'UpdateServer', 'DeleteServer'])
        self.assertEqual(gate.oracle_hash(gate.ROOT, self.suite), spec['oracle_sha256'])
        self.assertEqual(gate.SUITES['sqs-lifecycle']['argv'], ['go','test','-json','-count=1','-run','^TestSQSFactoryFidelityLifecycle$','./internal/aws'])

    def test_every_named_test_must_run_and_pass_once(self):
        events = self.events()
        self.verdict(events)
        for index in range(len(events)):
            with self.assertRaises(ValueError): self.verdict(events[:index]+events[index+1:])
        for index in range(len(events)-1):
            with self.assertRaises(ValueError): self.verdict(events+[events[index]])

    def test_wrong_package_skip_fail_and_nonzero_rejected(self):
        for action in ('skip', 'fail'):
            with self.assertRaises(ValueError): self.verdict(self.events()+[dict(Action=action)])
        for code in (1, -1):
            with self.assertRaises(ValueError): self.verdict(self.events(), code)
        events = self.events()
        for event in events: event['Package'] = 'foreign'
        with self.assertRaises(ValueError): self.verdict(events)
        with self.assertRaises(ValueError): self.verdict(self.events()[-1:])

    def test_leaf_paths_only_and_no_cross_suite_borrowing(self):
        allowed = {'internal/sakura/iaas/server_store.go','internal/sakura/iaas/server_store_test.go',
                   'internal/sakura/iaas/server_http.go','internal/sakura/iaas/server_http_test.go'}
        self.assertEqual(gate.SAKURA_SERVER_PATHS, allowed)
        for op in gate.SUITES[self.suite]['operations']:
            gate.validate_scope([self.suite], 'sakura', op, sorted(allowed))
        forbidden = ['internal/sakura/provider.go','internal/sakura/server_metadata_acceptance_test.go',
            'internal/sakura/iaas/disk_store.go','internal/server/sakura_provider.go','formal/applicability.json',
            'scripts/factory_acceptance.py','internal/aws/sqs_store.go','internal/aws/simpledb.go',
            'internal/azure/queue/store.go','internal/gcp/dns/store.go','internal/sakura/iaas/../iaas/server_store.go']
        for suites in ([self.suite], [self.suite,'sqs-lifecycle','ec2-describe-subnets']):
            for path in forbidden:
                with self.assertRaises(ValueError): gate.validate_scope(suites,'sakura','CreateServer',[path])
        for provider, op, path in [('aws','DeleteMessage',next(iter(allowed))),
                                   ('aws','DescribeSubnets',next(iter(allowed))),
                                   ('sakura','CreateServer','internal/aws/ec2_vpc.go'),
                                   ('sakura','PowerOnServer',next(iter(allowed))),
                                   ('sakura','CreateServer','internal/aws/sqs_store.go')]:
            with self.assertRaises(ValueError): gate.validate_scope([self.suite,'sqs-lifecycle','ec2-describe-subnets'],provider,op,[path])
        gate.validate_scope(['sqs-lifecycle'],'aws','DeleteMessage',['formal/applicability.json'])

    def test_missing_altered_or_symlink_oracle_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / gate.SUITES[self.suite]['oracle']
            path.parent.mkdir(parents=True)
            with self.assertRaises(OSError): gate.oracle_hash(root,self.suite)
            path.write_text('changed')
            with self.assertRaises(ValueError): gate.oracle_hash(root,self.suite)
            path.unlink(); path.symlink_to(gate.ROOT / gate.SUITES[self.suite]['oracle'])
            with self.assertRaises(ValueError): gate.oracle_hash(root,self.suite)

    def test_opt_in_default_cli_selection(self):
        with patch.object(sys,'argv',['factory_acceptance.py']), patch.object(gate,'run') as run:
            gate.main()
            self.assertEqual(run.call_args.args[2], ['sqs-lifecycle', 'ec2-describe-subnets'])
        with patch.object(sys,'argv',['factory_acceptance.py','--suite',self.suite]), patch.object(gate,'run') as run:
            gate.main()
            self.assertEqual(run.call_args.args[2], [self.suite])

if __name__=='__main__':unittest.main()
