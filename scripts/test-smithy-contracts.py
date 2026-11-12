#!/usr/bin/env python3
"""Offline generator regression tests; no SDK, credentials or network."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('smithy_contracts', Path(__file__).with_name('smithy-contracts.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class ContractsTest(unittest.TestCase):
    def setUp(self):
        self.model = json.loads((module.DATA / 'sqs.json').read_text())

    def test_deterministic_and_closed(self):
        output = module.extract(self.model)
        self.assertEqual(output, module.extract(copy.deepcopy(self.model)))
        self.assertEqual(set(output['operations']), set(module.OPERATIONS))
        self.assertNotIn('smithy.api#documentation', json.dumps(output))
        self.assertIn('smithy.api#Unit', output['shapes'])
        self.assertNotIn('com.amazonaws.sqs#StartMessageMoveTask', output['shapes'])
        for operation in output['operations'].values():
            for target in operation.values():
                self.assertIn(target, output['shapes'])

    def test_required_member_preserved(self):
        shape = module.extract(self.model)['shapes']['com.amazonaws.sqs#SendMessageRequest']
        self.assertIn('smithy.api#required', shape['members']['MessageBody']['traits'])

    def test_missing_shape_fails(self):
        del self.model['shapes']['com.amazonaws.sqs#SendMessageRequest']
        with self.assertRaises(KeyError):
            module.extract(self.model)

    def test_protocol_and_version_fail_closed(self):
        self.model['smithy'] = '9.0'
        with self.assertRaises(ValueError):
            module.extract(self.model)
        self.model['smithy'] = '2.0'
        del self.model['shapes']['com.amazonaws.sqs#AmazonSQS']['traits']['aws.protocols#awsJson1_0']
        with self.assertRaises(ValueError):
            module.extract(self.model)

    def test_selected_operation_must_belong_to_service(self):
        self.model['shapes']['com.amazonaws.sqs#AmazonSQS']['operations'] = []
        with self.assertRaises(ValueError):
            module.extract(self.model)

if __name__ == '__main__':
    unittest.main()
