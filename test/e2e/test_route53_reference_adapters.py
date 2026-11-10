"""Execute every inherited reference inventory with the exact Route53 adapter."""
import copy
import importlib
import json
from pathlib import Path
import unittest
from unittest.mock import patch

from route53_openapi import project_before_route53

VALIDATORS = (
    ('api_cloud_sections_contract', 'validate_document'),
    ('dynamodb_docs', 'validate_reference_contract'),
    ('s3_multipart_docs', 'validate_contract'),
    ('secrets_manager_docs', 'validate_reference_contract'),
    ('scalar_s3_docs', 'validate_reference_contract'),
    ('sqs_docs', 'validate_reference_contract'),
    ('sakura_reference_live', 'validate_reference_contract'),
)


class ReferenceAdapters(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.spec = json.loads((Path(__file__).resolve().parents[2] /
            'internal/server/apidocs/openapi.json').read_text())

    def test_actual_validators_require_exact_adapter_and_preserve_input(self):
        for name, function in VALIDATORS:
            module = importlib.import_module(name)
            candidate = copy.deepcopy(self.spec)
            with self.subTest(validator=name), patch.object(module,
                    'project_before_route53', wraps=project_before_route53) as adapter:
                getattr(module, function)(candidate)
                adapter.assert_called_once_with(candidate)
                self.assertEqual(candidate, self.spec)

    def test_actual_validators_reject_missing_changed_and_extra_contract(self):
        collection = '/2013-04-01/hostedzone'
        mutations = (
            ('missing Route53 path', lambda s: s['paths'].pop(collection)),
            ('changed Route53 response', lambda s: s['paths'][collection]['post']['responses']['201'].update(description='changed')),
            ('changed Route53 request', lambda s: s['paths'][collection]['post']['requestBody']['content']['application/xml'].update(example='<wrong/>')),
            ('duplicate Route53 operation', lambda s: s['paths'].update({'/extra-route': {'get': copy.deepcopy(s['paths'][collection]['get'])}})),
            ('unknown Route53-prefixed path', lambda s: s['paths'].update({'/2013-04-01/fiction': {'get': {'operationId': 'Fiction'}}})),
            ('missing inherited operation', lambda s: s['paths']['/{bucket}/{key}'].pop('post')),
            ('changed inherited operation', lambda s: s['paths']['/{bucket}']['get'].update(operationId='WrongInheritedID')),
        )
        for name, function in VALIDATORS:
            validate = getattr(importlib.import_module(name), function)
            for label, mutate in mutations:
                candidate = copy.deepcopy(self.spec)
                mutate(candidate)
                with self.subTest(validator=name, mutation=label), self.assertRaises((AssertionError, ValueError)):
                    validate(candidate)


if __name__ == '__main__':
    unittest.main()
