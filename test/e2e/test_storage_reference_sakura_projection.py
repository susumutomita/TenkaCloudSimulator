"""Pin the inherited AWS reference inventory after the approved Sakura Server GET."""
import copy
import json
from pathlib import Path
import unittest

from cloudwatch_logs import validate_reference_contract as validate_logs
from logs_secrets_reference_inventory import EXPECTED_ROUTES
from secrets_manager_docs import validate_reference_contract as validate_secrets
from s3_multipart_docs import validate_contract as validate_multipart
from scalar_s3_docs import validate_reference_contract as validate_scalar_s3
from sqs_docs import validate_reference_contract as validate_sqs
from test_sakura_openapi import SERVER_COLLECTION

ROOT = Path(__file__).resolve().parents[2]
VALIDATORS = (validate_multipart, validate_scalar_s3, validate_sqs, validate_logs, validate_secrets)
SERVER_TAG = 'Sakura Server metadata'
OBJECT_PATH = '/{bucket}/{key}'


class StorageReferenceSakuraProjection(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.spec = json.loads((ROOT / 'internal/server/apidocs/openapi.json').read_bytes())

    def test_current_inventory_still_uses_strict_fixtures(self):
        for validate in VALIDATORS:
            with self.subTest(module=validate.__module__):
                document = copy.deepcopy(self.spec)
                validate(document)
                self.assertEqual(document, self.spec)

    def test_rejects_integrity_changes_in_sakura_or_inherited_inventory(self):
        def duplicate_tag(spec):
            tag, = (entry for entry in spec['tags'] if entry['name'] == SERVER_TAG)
            spec['tags'].append(copy.deepcopy(tag))

        def regroup_tag(spec):
            origin, = (group for group in spec['x-tagGroups'] if SERVER_TAG in group['tags'])
            aws, = (group for group in spec['x-tagGroups'] if group['name'] == 'AWS')
            origin['tags'].remove(SERVER_TAG)
            aws['tags'].append(SERVER_TAG)

        mutations = (
            ('missing Sakura GET',
             lambda s: s['paths'].pop(SERVER_COLLECTION)),
            ('changed Sakura GET request',
             lambda s: s['paths'][SERVER_COLLECTION]['get'].update(parameters=[])),
            ('changed Sakura GET response',
             lambda s: s['paths'][SERVER_COLLECTION]['get']['responses']['200'].update(description='changed')),
            ('duplicate Sakura ID on another path',
             lambda s: s['paths'].update({'/unlisted-sakura': copy.deepcopy(s['paths'][SERVER_COLLECTION])})),
            ('duplicate Sakura tag', duplicate_tag),
            ('regrouped Sakura tag', regroup_tag),
            ('changed inherited operation',
             lambda s: s['paths']['/{bucket}']['get'].update(operationId='ChangedInheritedOperation')),
            ('missing inherited operation',
             lambda s: s['paths'][OBJECT_PATH].pop('post')),
        )
        for validate in VALIDATORS:
            for name, mutate in mutations:
                with self.subTest(module=validate.__module__, mutation=name):
                    document = copy.deepcopy(self.spec)
                    mutate(document)
                    with self.assertRaises(AssertionError):
                        validate(document)

    def test_logs_and_secrets_require_the_original_operation_at_each_route(self):
        ids = [ident for operations in EXPECTED_ROUTES.values() for ident in operations.values()]
        self.assertEqual((len(EXPECTED_ROUTES), len(ids), len(set(ids))), (36, 67, 67))

        def swap_ids(spec):
            bucket = spec['paths']['/{bucket}']['delete']
            role = spec['paths']['/2015-03-31/functions/{FunctionName}']['delete']
            bucket['operationId'], role['operationId'] = role['operationId'], bucket['operationId']

        def move_method(spec):
            resource = spec['paths'][OBJECT_PATH]
            resource['patch'] = resource.pop('head')

        mutations = (
            ('swap IDs on two valid routes', swap_ids),
            ('move an existing operation to a different HTTP method', move_method),
            ('duplicate a valid ID', lambda s: s['paths']['/']['get'].update(operationId='HeadBucket')),
            ('duplicate an ID in extension metadata',
             lambda s: s['paths'][OBJECT_PATH].update({'x-copy': {'operationId': 'GetObject'}})),
            ('omit an ID', lambda s: s['paths'][OBJECT_PATH]['post'].pop('operationId')),
            ('add an operation without an ID', lambda s: s['paths'][OBJECT_PATH].update(trace={})),
            ('add a path', lambda s: s['paths'].update({'/new-test-path': {}})),
            ('omit a path', lambda s: s['paths'].pop('/2015-03-31/functions')),
        )
        for validate in (validate_logs, validate_secrets):
            for name, mutate in mutations:
                with self.subTest(module=validate.__module__, mutation=name):
                    document = copy.deepcopy(self.spec)
                    mutate(document)
                    with self.assertRaises(AssertionError):
                        validate(document)

    def test_multipart_payload_guard_is_not_projected_away(self):
        document = copy.deepcopy(self.spec)
        complete = document['paths'][OBJECT_PATH]['post']['requestBody']['content']['application/xml']['examples']['Complete']
        complete['value'] = complete['value'].replace('actual-stored-part-md5', 'changed-etag')
        with self.assertRaisesRegex(AssertionError, 'Static placeholder'):
            validate_multipart(document)


if __name__ == '__main__':
    unittest.main()
