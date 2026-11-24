#!/usr/bin/env python3
"""Registration/detector tests. Synthetic event controls are not product evidence."""
import copy
import importlib.util
import os
import select
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import factory_acceptance as gate

class SimpleDBRegistrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)/'repo';self.root.mkdir()
        shutil.copytree(gate.ROOT/'test/factory',self.root/'test/factory')
        (self.root/'scripts').mkdir()
        shutil.copyfile(gate.ROOT/'scripts/test-simpledb-factory-sdk.py',self.root/'scripts/test-simpledb-factory-sdk.py')
        subprocess.run(['git','init','-q',str(self.root)],check=True)
        self.git('config','user.email','test@example.invalid');self.git('config','user.name','test')
        self.git('add','.');self.git('commit','-qm','independent fixture')
        self.head=self.git('rev-parse','HEAD')
    def git(self,*args):return gate.git(self.root,*args)
    def events(self):
        spec=gate.SUITES['simpledb-first-native'];events=[]
        for name in spec['tests']:
            events.extend([dict(Action='run',Package=spec['package'],Test=name),dict(Action='pass',Package=spec['package'],Test=name)])
        return events+[dict(Action='pass',Package=spec['package'])]
    def log(self,events):return ('\n'.join(json.dumps(e) for e in events)+'\n').encode()
    def sdk(self):return {'factory_sdk_v1':True,'head':self.head,'status':'PASS','passed_cases':['sdk-domain-items','sdk-restart-cascade','sdk-list-pages','sdk-negative-cases'],'sdk_pins':{'boto3':'1.42.0','botocore':'1.41.6'},'binary_sha256':'a'*64,'python':gate.executable_command(['python3'])[0],'go':gate.executable_command(['go'])[0]}
    def test_union_and_default_activation(self):
        dns = 'gcp-dns-atomic-lifecycle'
        self.assertEqual({'sqs-lifecycle', 'ec2-describe-subnets', dns, *gate.SIMPLEDB_SUITES},
                         set(gate.SUITES))
        self.assertEqual(gate.SUITES[dns]['provider'], 'gcp-dns')
        self.assertNotIn('azure-queue-lifecycle', gate.SUITES)
        self.assertEqual(gate.DEFAULT_SUITES,('sqs-lifecycle','ec2-describe-subnets'))
        self.assertFalse((self.root/'internal/azure/queue').exists())
        self.assertFalse((self.root/'internal/gcp/dns').exists())
        self.assertEqual(gate.source_hash(self.root),gate.legacy_source_hash(self.root))
    def test_exact_fixed_commands_and_operations(self):
        self.assertEqual(gate.SUITES['simpledb-first-native']['argv'],['go','test','-p=1','-json','-count=1','-timeout=60s','./test/factory/testdata/simpledb/native'])
        self.assertEqual(gate.SUITES['simpledb-first-sdk-wire']['argv'],['python3','-B','scripts/test-simpledb-factory-sdk.py'])
        self.assertEqual(gate.SIMPLEDB_OPERATIONS,['CreateDomain','ListDomains','DeleteDomain','PutAttributes','GetAttributes'])
        for op in gate.SIMPLEDB_OPERATIONS:
            gate.validate_scope(list(gate.SIMPLEDB_SUITES),'aws',op,['internal/aws/simpledb/store.go'])
    def test_wrong_service_path_operation_and_partial_surfaces_rejected(self):
        all_suites=list(gate.SUITES)
        for path in ['internal/aws/sqs_store.go','internal/aws/simpledb/unknown.go','internal/aws/simpledb/../sqs_store.go','internal/aws/simpledb/nested/store.go','internal/aws/s3.go','internal/server/server.go','scripts/factory_acceptance.py','test/factory/testdata/simpledb/oracle/fixtures.json','internal/azure/queue/store.go','internal/gcp/dns/store.go','formal/applicability.json']:
            with self.subTest(path=path),self.assertRaises(ValueError):
                gate.validate_scope(all_suites,'aws','CreateDomain',[path])
        for suites,provider,op,paths in [(list(gate.SIMPLEDB_SUITES),'aws','Select',['internal/aws/simpledb/store.go']),(['simpledb-first-native'],'aws','CreateDomain',['internal/aws/simpledb/store.go']),(all_suites,'aws','DeleteMessage',['internal/aws/simpledb/store.go']),(all_suites,'azure-queue','DeleteMessage',['internal/aws/simpledb/store.go']),(all_suites,'gcp-dns','dns.changes.create',['internal/aws/simpledb/store.go']),(['simpledb-first-oracle'],'aws','CreateDomain',['internal/aws/simpledb/store.go']),(list(gate.SIMPLEDB_SUITES),'aws','CreateDomain',[])]:
            with self.subTest(op=op,provider=provider),self.assertRaises(ValueError):gate.validate_scope(suites,provider,op,paths)
    def test_native_all_required_tests_and_subtests(self):
        events=self.events();gate.verdict(self.log(events),'simpledb-first-native',0,self.head)
        for bad in [[],events[2:],events[:-1],events+events,events[:-3]+events[-1:]]:
            with self.assertRaises(ValueError):gate.verdict(self.log(bad),'simpledb-first-native',0,self.head)
        for action in ['skip','fail','build-fail']:
            bad=copy.deepcopy(events);bad[1]['Action']=action
            with self.assertRaises(ValueError):gate.verdict(self.log(bad),'simpledb-first-native',0,self.head)
        for code in [1,-1]:
            with self.assertRaises(ValueError):gate.verdict(self.log(events),'simpledb-first-native',code,self.head)
    def test_pin_covers_all_nine_immutable_files_and_sdk_helper(self):
        pin=gate.source_hash(self.root,list(gate.SIMPLEDB_SUITES));self.assertRegex(pin,r'^[0-9a-f]{64}$')
        data=json.loads((self.root/'test/factory/simpledb-registration.json').read_text())
        for name in data['files']:
            path=self.root/name;original=path.read_bytes();path.write_bytes(original+b'changed')
            with self.subTest(name=name),self.assertRaises(ValueError):gate.source_hash(self.root,list(gate.SIMPLEDB_SUITES))
            path.write_bytes(original)
        self.assertEqual(pin,gate.source_hash(self.root,list(gate.SIMPLEDB_SUITES)))
    def test_sdk_preflight_or_missing_case_never_product_acceptance(self):
        valid=self.sdk();gate.verdict(json.dumps(valid).encode(),'simpledb-first-sdk-wire',0,self.head)
        for field,value in [('head','b'*40),('status','PREFLIGHT_ONLY_NOT_ACCEPTANCE'),('status','HARNESS_ERROR'),('status','PRODUCT_CONTRACT_RED'),('passed_cases',valid['passed_cases'][:-1]),('passed_cases',valid['passed_cases']*2),('sdk_pins',{}),('binary_sha256',''),('python','/wrong'),('go','/wrong'),('cleanup_errors',['timeout'])]:
            bad={**valid,field:value}
            with self.subTest(field=field,value=value),self.assertRaises(ValueError):gate.verdict(json.dumps(bad).encode(),'simpledb-first-sdk-wire',0,self.head)
        for code in [1,2,-1]:
            with self.assertRaises(ValueError):gate.verdict(json.dumps(valid).encode(),'simpledb-first-sdk-wire',code,self.head)
    def test_sdk_harness_names_skips_and_completion(self):
        spec=gate.SUITES['simpledb-first-sdk-harness-controls']
        log='\n'.join(name+' (module.Test.'+name+') ... ok' for name in spec['tests'])+'\nRan 3 tests in 0.001s\nOK\n'
        gate.verdict(log.encode(),'simpledb-first-sdk-harness-controls',0,self.head)
        for bad in ['',log.replace(' ... ok',' ... skipped',1),log.replace(spec['tests'][0],'foreign'),log.replace('Ran 3 tests','Ran 0 tests'),log+'OK\n',log.replace('OK','FAILED')]:
            with self.assertRaises(ValueError):gate.verdict(bad.encode(),'simpledb-first-sdk-harness-controls',0,self.head)
    def report(self):
        suite='simpledb-first-native';log=self.log(self.events());path=Path(self.tmp.name)/'receipt.json';lp=path.with_suffix('.jsonl');lp.write_bytes(log)
        report={'version':1,'head':self.head,'status':'passed','source_sha256':gate.source_hash(self.root,[suite]),'checks':{suite:{'status':'passed','argv':gate.SUITES[suite]['argv'],'executed_argv':gate.executable_command(gate.SUITES[suite]['argv']),'returncode':0,'log':lp.name,'log_sha256':gate.digest(log)}}};return path,lp,report
    def test_wrong_command_head_log_or_oracle_receipt_rejected(self):
        path,lp,report=self.report();suite='simpledb-first-native'
        path.write_text(json.dumps(report));gate.verify(path,self.head,[suite],self.root)
        for field,value in [('argv',['true']),('executed_argv',['true']),('status','skipped'),('returncode',-1),('log','../foreign'),('log_sha256','wrong')]:
            bad=copy.deepcopy(report);bad['checks'][suite][field]=value;path.write_text(json.dumps(bad))
            with self.subTest(field=field),self.assertRaises(ValueError):gate.verify(path,self.head,[suite],self.root)
        for field,value in [('head','a'*40),('source_sha256','wrong'),('status','failed')]:
            bad=copy.deepcopy(report);bad[field]=value;path.write_text(json.dumps(bad))
            with self.assertRaises(ValueError):gate.verify(path,self.head,[suite],self.root)
        path.write_text(json.dumps(report));lp.write_bytes(b'tampered')
        with self.assertRaises(ValueError):gate.verify(path,self.head,[suite],self.root)
    def test_real_timeout_records_failed_receipt(self):
        suite='simpledb-first-native';spec=copy.deepcopy(gate.SUITES[suite]);spec['argv']=[sys.executable,'-c','import time;time.sleep(5)']
        output=Path(self.tmp.name)/'timeout.json'
        with patch.dict(gate.SUITES,{suite:spec}):
            with self.assertRaises(ValueError):gate.run(self.root,output,[suite],timeout=.02)
        report=json.loads(output.read_text());self.assertEqual(report['status'],'failed');self.assertEqual(report['checks'][suite]['returncode'],-1)
    def test_sdk_build_timeout_kills_owned_descendant_session(self):
        spec=importlib.util.spec_from_file_location('simpledb_factory_sdk_control',gate.ROOT/'scripts/test-simpledb-factory-sdk.py')
        helper=importlib.util.module_from_spec(spec);spec.loader.exec_module(helper)
        reader,writer=os.pipe()
        try:
            child="import time; print('child-ready',flush=True); time.sleep(10)"
            parent="import subprocess,sys,time; subprocess.Popen([sys.executable,'-c',"+repr(child)+"]); time.sleep(10)"
            with self.assertRaises(subprocess.TimeoutExpired):
                helper.run_owned_process([sys.executable,'-c',parent],self.root,1,stdout=writer)
            os.close(writer);writer=None
            chunks=[]
            while True:
                ready,_,_=select.select([reader],[],[],1)
                self.assertTrue(ready,'owned descendant retained stdout after build timeout')
                chunk=os.read(reader,4096)
                if not chunk:break
                chunks.append(chunk)
            self.assertIn(b'child-ready',b''.join(chunks),'descendant control never started')
        finally:
            os.close(reader)
            if writer is not None:os.close(writer)

    def test_python_venv_invocation_symlink_is_preserved(self):
        executable=Path(self.tmp.name)/'venv/bin/python3';executable.parent.mkdir(parents=True)
        executable.symlink_to(Path(sys.executable).resolve())
        (executable.parents[1]/'pyvenv.cfg').write_text('home = '+str(Path(sys.executable).resolve().parent)+'\ninclude-system-site-packages = false\n')
        with patch.dict('os.environ',{'FACTORY_SDK_PYTHON':str(executable)}):
            command=gate.executable_command(['python3','-B','-c','import sys;print(sys.executable)'])
            self.assertEqual(command[0],str(executable.parent.resolve()/executable.name))
            self.assertNotEqual(command[0],str(executable.resolve()))
            result=subprocess.check_output(command,text=True).strip()
            self.assertEqual(result,str(executable.parent.resolve()/executable.name))

    def test_python_override_must_be_one_executable(self):
        for value in ['python3 -c pass','/missing/python','python3\ntrue']:
            with patch.dict('os.environ',{'FACTORY_SDK_PYTHON':value}),self.assertRaises(ValueError):gate.executable_command(['python3'])

if __name__=='__main__':unittest.main()
