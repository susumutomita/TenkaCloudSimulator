import contextlib
import io
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import launchd_owned_child as subject


class FailureEvidence(unittest.TestCase):
    def exercise(self, failure, initial="absent", bootout_failure=False):
        with tempfile.TemporaryDirectory() as root:
            calls=[]
            def fake(argv, **kwargs):
                calls.append(argv[1])
                if argv[1]=="bootstrap":
                    raise failure
                target=argv[-1]
                label=target.rsplit("/",1)[-1]
                if argv[1]=="print":
                    if initial=="timeout":
                        raise subprocess.TimeoutExpired(argv,10,stderr="state unavailable")
                    if initial=="unknown":
                        return subprocess.CompletedProcess(argv,1,"","Operation not permitted")
                    if initial=="present" and "bootout" not in calls:
                        return subprocess.CompletedProcess(argv,0,"state = waiting","")
                    return subprocess.CompletedProcess(argv,113,"",f'Could not find service "{label}"')
                if argv[1]=="bootout":
                    if bootout_failure:
                        raise subprocess.CalledProcessError(5,argv,stderr="cleanup denied")
                    return subprocess.CompletedProcess(argv,0,"","")
                raise AssertionError("unexpected operation: "+str(argv))
            with patch.object(subject.sys,"platform","darwin"), patch.object(subject.pathlib.Path,"home",return_value=pathlib.Path(root)), patch.object(subject.subprocess,"run",side_effect=fake), contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(RuntimeError): subject.run()
            evidence=list(pathlib.Path(root).glob("*/result.json"))
            self.assertEqual(len(evidence),1)
            data=json.loads(evidence[0].read_text())
            self.assertFalse(data["experimentPassed"])
            self.assertFalse(data["battleReady"])
            self.assertEqual(calls.count("bootstrap"),1)
            self.assertNotIn("kickstart",calls)
            return data,calls

    def test_failed_bootstrap_preserves_stderr_and_confirms_absent(self):
        data,calls=self.exercise(subprocess.CalledProcessError(5,["launchctl"],stderr="registration failed"))
        self.assertEqual(data["operationFailure"]["stderr"],"registration failed")
        self.assertTrue(data["serviceAbsentConfirmed"])
        self.assertNotIn("bootout",calls)

    def test_timeout_partial_registration_is_removed_without_retry(self):
        data,calls=self.exercise(subprocess.TimeoutExpired(["launchctl"],10),"present")
        self.assertTrue(data["registrationObserved"])
        self.assertTrue(data["serviceAbsentConfirmed"])
        self.assertEqual(calls,["bootstrap","print","bootout","print"])

    def test_cleanup_failure_still_preserves_both_errors(self):
        data,calls=self.exercise(subprocess.CalledProcessError(5,["launchctl"],stderr="original"),"present",True)
        self.assertEqual(data["operationFailure"]["stderr"],"original")
        self.assertEqual(data["cleanupFailure"]["stderr"],"cleanup denied")
        self.assertNotIn("serviceAbsentConfirmed",data)

    def test_unknown_registration_is_not_treated_as_absent(self):
        data,calls=self.exercise(subprocess.TimeoutExpired(["launchctl"],10),"unknown")
        self.assertIn("cleanupFailure",data)
        self.assertNotIn("serviceAbsentConfirmed",data)
        self.assertNotIn("bootout",calls)

    def test_print_timeout_preserves_unknown_state_without_cleanup_retry(self):
        data,calls=self.exercise(subprocess.TimeoutExpired(["launchctl"],10),"timeout")
        self.assertEqual(data["cleanupFailure"]["type"],"TimeoutExpired")
        self.assertNotIn("serviceAbsentConfirmed",data)
        self.assertEqual(calls,["bootstrap","print"])

    def test_error_text_is_bounded(self):
        data,_=self.exercise(subprocess.CalledProcessError(5,["x"*10000],stderr="e"*10000))
        self.assertLessEqual(len(data["operationFailure"]["message"]),2048)
        self.assertEqual(len(data["operationFailure"]["stderr"]),4096)

    def test_diskfull_never_emits_a_success_receipt(self):
        with tempfile.TemporaryDirectory() as root:
            calls=[]
            real_write=pathlib.Path.write_text
            def write(path,*args,**kwargs):
                if path.name=="result.json":
                    raise OSError(28,"No space left on device")
                return real_write(path,*args,**kwargs)
            def fake(argv,**kwargs):
                calls.append(argv[1])
                if argv[1]=="bootstrap":
                    raise subprocess.CalledProcessError(5,argv,stderr="initial failure")
                if argv[1]=="print":
                    label=argv[-1].rsplit("/",1)[-1]
                    return subprocess.CompletedProcess(argv,113,"",f'Could not find service "{label}"')
                raise AssertionError("unexpected operation")
            output=io.StringIO()
            with patch.object(subject.sys,"platform","darwin"), patch.object(subject.pathlib.Path,"home",return_value=pathlib.Path(root)), patch.object(subject.subprocess,"run",side_effect=fake), patch.object(pathlib.Path,"write_text",new=write), contextlib.redirect_stdout(output):
                with self.assertRaises(OSError) as error: subject.run()
            self.assertEqual(error.exception.errno,28)
            self.assertEqual(output.getvalue(),"")
            self.assertEqual(calls,["bootstrap","print"])
            self.assertEqual(list(pathlib.Path(root).glob("*/result.json")),[])

if __name__=="__main__": unittest.main()
