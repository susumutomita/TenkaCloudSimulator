import json
import unittest
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


if __name__ == "__main__":
    unittest.main()
