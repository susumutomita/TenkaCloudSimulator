"""Offline request-scope detector checks; no SDK success or network is simulated."""
from types import SimpleNamespace
import unittest
from urllib.parse import urlencode

from sts_session_lifecycle import FixtureFailure, checked_sdk_request, successful


class LocalSDKScope(unittest.TestCase):
    def request(self, **changes):
        fields = {'url': 'http://127.0.0.1:12345/', 'method': 'POST',
                  'headers': {'Authorization': b'AWS4-HMAC-SHA256 synthetic-detector-only'},
                  'body': urlencode({'Action': 'GetCallerIdentity', 'Version': '2011-06-15'})}
        fields.update(changes)
        return SimpleNamespace(**fields)

    def test_normal_scope_shape(self):
        self.assertEqual(checked_sdk_request(self.request(), 12345, 'sts'), 'GetCallerIdentity')
        iam = self.request(body=urlencode({'Action': 'GetRole', 'Version': '2010-05-08', 'RoleName': 'fixture'}))
        self.assertEqual(checked_sdk_request(iam, 12345, 'iam'), 'GetRole')

    def test_wrong_origin_or_transport_is_refused(self):
        for changes in ({'method': 'GET'}, {'url': 'https://127.0.0.1:12345/'},
                        {'url': 'http://localhost:12345/'}, {'url': 'http://127.0.0.1:12346/'},
                        {'url': 'http://127.0.0.1:12345/other'}, {'url': 'http://127.0.0.1:12345/?Action=GetCallerIdentity'},
                        {'url': 'http://127.0.0.1:12345/#fragment'}, {'url': 'http://user@127.0.0.1:12345/'}):
            with self.subTest(changes=changes), self.assertRaises(FixtureFailure):
                checked_sdk_request(self.request(**changes), 12345, 'sts')

    def test_control_auth_cannot_replace_normal_sdk_signing(self):
        for name in ('Cookie', 'X-Simulator-Token', 'X-Simulator-Principal', 'X-Simulator-Service', 'X-Amz-Target'):
            request = self.request()
            request.headers[name] = 'synthetic'
            with self.subTest(header=name), self.assertRaises(FixtureFailure):
                checked_sdk_request(request, 12345, 'sts')
        with self.assertRaises(FixtureFailure):
            checked_sdk_request(self.request(headers={}), 12345, 'sts')

    def test_unexpected_or_duplicate_operation_is_refused(self):
        for body in (urlencode({'Action': 'UnsupportedFixtureAction', 'Version': '2011-06-15'}),
                     urlencode({'Action': 'GetCallerIdentity', 'Version': 'wrong-version'}),
                     'Action=GetCallerIdentity&Action=AssumeRole&Version=2011-06-15', 'x' * 32769):
            with self.subTest(body_length=len(body)), self.assertRaises(FixtureFailure):
                checked_sdk_request(self.request(body=body), 12345, 'sts')

    def test_native_success_or_retry_cannot_be_inferred(self):
        self.assertEqual(successful(lambda: {'ResponseMetadata': {'HTTPStatusCode': 200}})
                         ['ResponseMetadata']['HTTPStatusCode'], 200)
        for metadata in ({'HTTPStatusCode': 403}, {'HTTPStatusCode': 200, 'RetryAttempts': 1}):
            with self.subTest(metadata=metadata), self.assertRaises(FixtureFailure):
                successful(lambda: {'ResponseMetadata': metadata})


if __name__ == '__main__':
    unittest.main()
