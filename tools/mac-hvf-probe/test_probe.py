import json
import unittest
import subprocess
import sys
from unittest.mock import Mock
import probe


class Contract(unittest.TestCase):
    def setUp(self):
        self.records = [{"QMP": {}}, {"id": "capabilities", "return": {}},
                        {"id": "status", "return": {"status": "prelaunch", "running": False}},
                        {"event": "SHUTDOWN", "data": {"guest": False, "reason": "host-qmp-quit"}},
                        {"id": "quit", "return": {}}]

    def check(self, records=None, stderr="", code=0):
        probe.verify("\n".join(map(json.dumps, self.records if records is None else records)), stderr, code)

    def test_expected(self):
        self.check()

    def test_every_record_required(self):
        for index in range(len(self.records)):
            with self.subTest(index=index), self.assertRaises(ValueError):
                self.check(self.records[:index] + self.records[index + 1:])

    def test_running_rejected(self):
        self.records[2]["return"] = {"status": "running", "running": True}
        with self.assertRaises(ValueError):
            self.check()

    def test_failure_and_diagnostic_rejected(self):
        for stderr, code in [("denied", 0), ("", 1)]:
            with self.subTest(stderr=stderr, code=code), self.assertRaises(ValueError):
                self.check(stderr=stderr, code=code)

    def test_duplicate_and_error_rejected(self):
        for extra in [self.records[1], {"error": {"desc": "unsupported"}}]:
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                self.check(self.records + [extra])

    def test_no_guest_devices_or_acceleration_fallback(self):
        self.assertIn("virt,accel=hvf", probe.ARGS)
        self.assertNotIn("tcg", " ".join(probe.ARGS))
        for option in ["-drive", "-kernel", "-netdev", "-fsdev", "-device"]:
            self.assertNotIn(option, probe.ARGS)


class OwnedLifecycle(unittest.TestCase):
    def test_real_timeout_reaps_only_owned_child(self):
        child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                 stderr=subprocess.PIPE, text=True)
        unrelated = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])
        try:
            with self.assertRaises(subprocess.TimeoutExpired):
                probe.exchange_owned(child, "", timeout=0.1)
            self.assertIsNotNone(child.returncode)
            self.assertTrue(child.stdout.closed)
            self.assertIsNone(unrelated.poll())
        finally:
            if child.poll() is None:
                child.kill()
            child.wait(timeout=2)
            unrelated.kill()
            unrelated.wait(timeout=2)

    def test_interrupt_preserved_after_confirmed_stop(self):
        child = Mock()
        child.communicate.side_effect = KeyboardInterrupt()
        with self.assertRaises(KeyboardInterrupt):
            probe.exchange_owned(child, "")
        child.kill.assert_called_once_with()
        child.wait.assert_called_once_with(timeout=2)
        child.stdout.close.assert_called_once_with()

    def test_failed_stop_is_uncertain_not_success(self):
        for error in [subprocess.TimeoutExpired("owned", 2), PermissionError("denied")]:
            with self.subTest(error=error):
                child = Mock()
                child.wait.side_effect = error
                with self.assertRaises(probe.TeardownUncertain):
                    probe.stop_owned(child)
                child.wait.assert_called_once_with(timeout=2)



if __name__ == "__main__":
    unittest.main()
