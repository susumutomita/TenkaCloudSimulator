"""Pure event-detector controls, never product execution."""
import importlib.util
from pathlib import Path
import json
import contextlib
import io
import tempfile
import hashlib
import sys
from unittest.mock import patch
import unittest
spec=importlib.util.spec_from_file_location('leaf_runner',Path(__file__).with_name('runner.py'));r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
class RunnerControls(unittest.TestCase):
 def events(self,failed=None):
  out=[{'Action':'run','Package':r.PACKAGE,'Test':r.TEST}];cases=[]
  for case in r.REQUIRED:
   status='PRODUCT_CONTRACT_RED' if case==failed else 'PASS';name=r.TEST+'/'+case
   out.extend([{'Action':'run','Package':r.PACKAGE,'Test':name},{'Action':'fail' if case==failed else 'pass','Package':r.PACKAGE,'Test':name}]);cases.append({'id':case,'status':status})
  out.append({'Action':'output','Package':r.PACKAGE,'Test':r.TEST,'Output':'LEAF_CONTRACT_REPORT '+json.dumps({'surface':'leaf-store-contract-only','cases':cases})+'\n'})
  out.extend([{'Action':'fail' if failed else 'pass','Package':r.PACKAGE,'Test':r.TEST},{'Action':'fail' if failed else 'pass','Package':r.PACKAGE}]);return out
 def classify(self,e,code=0):return r.classify('\n'.join(json.dumps(x) for x in e),code)
 def test_complete_named_execution_only(self):
  e=self.events();self.assertEqual(self.classify(e),('PASS',0))
  for bad in ([],e[2:],e[:-1],e+e):self.assertEqual(self.classify(bad),('HARNESS_ERROR',2))
 def test_compile_failure_is_binding_not_red(self):self.assertEqual(self.classify([{'Action':'build-fail','ImportPath':r.PACKAGE}],1),('BINDING_UNESTABLISHED',2))
 def test_other_dependency_compile_failure_is_harness(self):self.assertEqual(self.classify([{'Action':'build-fail','ImportPath':'runtime'}],1),('HARNESS_ERROR',2))
 def test_skipped_or_harness_failure_not_product_red(self):
  for e in ([{'Action':'skip'}],[{'Output':'HARNESS_ERROR SQLite unavailable'}]):self.assertEqual(self.classify(e,1),('HARNESS_ERROR',2))
 def test_started_parent_without_case_report_is_not_red(self):self.assertEqual(self.classify([{'Action':'run','Package':r.PACKAGE,'Test':r.TEST},{'Action':'fail','Package':r.PACKAGE,'Test':r.TEST}],1),('HARNESS_ERROR',2))
 def test_nonexecution_failure_is_harness_error(self):self.assertEqual(self.classify([],1),('HARNESS_ERROR',2))
 def test_go_timeout_remains_failed_harness_evidence(self):self.assertEqual(self.classify([{'Output':'panic: test timed out after 60s'},{'Action':'fail'}],1),('HARNESS_ERROR',2))
 def test_report_backed_case_assertion_failure_is_red(self):self.assertEqual(self.classify(self.events('item-workflow'),1),('PRODUCT_CONTRACT_RED',1))
 def test_input_hash_set_and_tampering_fail_closed(self):
  with tempfile.TemporaryDirectory() as directory:
   here=Path(directory);files={}
   for name in r.EXPECTED_INPUTS:
    path=here/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_text(name);files[name]=hashlib.sha256(path.read_bytes()).hexdigest()
   manifest=here/'files.sha256.json';manifest.write_text(json.dumps({'files':files}))
   with patch.object(r,'HERE',here):
    r.verify_inputs();(here/'runner.py').write_text('changed')
    with self.assertRaises(ValueError):r.verify_inputs()
    (here/'runner.py').write_text('runner.py');files.pop('binding/subject_test.go');manifest.write_text(json.dumps({'files':files}))
    with self.assertRaises(ValueError):r.verify_inputs()
 def test_post_execution_head_change_is_harness_failure(self):
  with tempfile.TemporaryDirectory() as directory:
   base=Path(directory);root=base/'repo';store=root/'internal/aws/simpledb/store.go';store.parent.mkdir(parents=True);store.write_text('not executed')
   report=base/'report.json';log=base/'log.jsonl';argv=['runner.py','--report',str(report),'--log',str(log)]
   with patch.object(r,'ROOT',root),patch.object(r,'verify_inputs'),patch.object(r,'snapshot',side_effect=['a'*40,'b'*40]),patch.object(r,'execute',return_value=('\n'.join(json.dumps(e) for e in self.events()),'',0,['fixed'])),patch.object(sys,'argv',argv),contextlib.redirect_stdout(io.StringIO()):
    self.assertEqual(r.main(),2)
   self.assertEqual(json.loads(report.read_text())['status'],'HARNESS_ERROR')
if __name__=='__main__':unittest.main()
