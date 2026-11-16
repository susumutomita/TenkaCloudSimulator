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
if __name__=='__main__':unittest.main()
