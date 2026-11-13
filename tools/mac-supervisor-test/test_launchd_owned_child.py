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

if __name__=="__main__": unittest.main()
