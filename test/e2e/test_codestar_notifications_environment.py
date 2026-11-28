import unittest

from codestar_notifications import check_sdk_environment


class NotificationSDKEnvironment(unittest.TestCase):
    canonical = {
        "AWS_CONFIG_FILE": "/dev/null",
        "AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
        "AWS_EC2_METADATA_DISABLED": "true",
    }

    def test_standalone_and_ordinary_suite_have_distinct_contracts(self):
        check_sdk_environment({"LANG": "C"}, conformance=False)
        check_sdk_environment({**self.canonical, "LANG": "C"}, conformance=True)
        with self.assertRaises(AssertionError):
            check_sdk_environment(self.canonical, conformance=False)
        with self.assertRaises(AssertionError):
            check_sdk_environment({}, conformance=True)

    def test_ordinary_suite_requires_each_exact_value(self):
        for key in self.canonical:
            with self.subTest(key=key, condition="missing"), self.assertRaises(AssertionError):
                check_sdk_environment({k: v for k, v in self.canonical.items() if k != key}, True)
            with self.subTest(key=key, condition="changed"), self.assertRaises(AssertionError):
                check_sdk_environment({**self.canonical, key: "different"}, True)

    def test_both_modes_reject_additional_sdk_settings(self):
        for key in ("AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
                    "AWS_SESSION_TOKEN", "AWS_ENDPOINT_URL", "BOTO_CONFIG", "BOTO_UNRECOGNIZED"):
            for mode, expected in ((False, {}), (True, self.canonical)):
                for value in ("", "test-value"):
                    with self.subTest(key=key, conformance=mode, value=value), self.assertRaises(AssertionError):
                        check_sdk_environment({**expected, key: value}, mode)


if __name__ == "__main__":
    unittest.main()
