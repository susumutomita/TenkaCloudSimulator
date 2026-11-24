"""Pure SDK-harness detector controls. No boto3, runtime, socket or cloud execution."""
import importlib.util
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('simpledb_sdk_wire', Path(__file__).with_name('sdk_wire.py'))
wire = importlib.util.module_from_spec(spec)
spec.loader.exec_module(wire)

class HarnessControls(unittest.TestCase):
    def test_missing_attribute_fields_are_product_failure(self):
        for response in ({'Attributes': [{'Name': 'x'}]}, {'Attributes': [{'Value': 'y'}]}, {'Attributes': 'not-a-list'}, {'Attributes': [{'Name': None, 'Value': 'y'}]}):
            with self.subTest(response=response), self.assertRaises(wire.ProductFailure):
                wire.decoded_attributes(response)
        self.assertEqual(wire.decoded_attributes({'Attributes': [{'Name': 'x', 'Value': 'y'}]}), [('x', 'y')])
        self.assertEqual(wire.decoded_attributes({}), [])

    def test_cleanup_failure_preserves_primary_product_failure(self):
        class FakeSocket:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def bind(self, address): pass
            def getsockname(self): return ('127.0.0.1', 4123)
        class FakeResponse:
            status = 200
            def __enter__(self): return self
            def __exit__(self, *args): pass
        def fake_start(args, **kwargs):
            Path(args[-1]).write_text('local-test-token')
            return SimpleNamespace(pid=12345, poll=lambda: None, wait=lambda timeout: 7, returncode=7)
        report = {}
        with tempfile.TemporaryDirectory() as directory, patch.object(wire.socket, 'socket', return_value=FakeSocket()), patch.object(wire.subprocess, 'Popen', side_effect=fake_start), patch.object(wire.os, 'killpg'), patch.object(wire.urllib.request, 'build_opener', return_value=SimpleNamespace(open=lambda *a, **k: FakeResponse())):
            with self.assertRaisesRegex(wire.ProductFailure, '^original product mismatch$'):
                with wire.running(Path('/not-executed'), Path(directory)/'state.db', report):
                    raise wire.ProductFailure('original product mismatch')
        self.assertEqual(report['cleanup_errors'], ['RuntimeError: CLI cleanup exit=7'])

    def test_sdk_origin_guard_rejects_any_other_origin(self):
        callbacks = {}
        events = SimpleNamespace(register=lambda name, callback: callbacks.__setitem__(name, callback))
        fake_client = SimpleNamespace(meta=SimpleNamespace(service_model=SimpleNamespace(metadata={'signatureVersion': 'v2'}), events=events))
        boto = SimpleNamespace(client=lambda *a, **k: fake_client)
        wire.client_for(boto, lambda **k: None, 'http://127.0.0.1:4123', 'local-test-token')
        guard = callbacks['before-send.sdb']
        guard(SimpleNamespace(url='http://127.0.0.1:4123/?Action=ListDomains'))
        for url in ('https://127.0.0.1:4123/', 'http://localhost:4123/', 'http://127.0.0.2:4123/', 'http://127.0.0.1:4124/', 'https://sdb.amazonaws.com/'):
            with self.subTest(url=url), self.assertRaises(RuntimeError):
                guard(SimpleNamespace(url=url))

if __name__ == '__main__':
    unittest.main()
