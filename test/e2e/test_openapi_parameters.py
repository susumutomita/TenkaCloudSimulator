"""Static detector controls; no simulator, browser, AWS or dependencies needed."""
import copy
import json
from pathlib import Path
import unittest
from openapi_parameters import validate_parameter_lists

TOKEN = {'name': 'X-Simulator-Token', 'in': 'header', 'schema': {'type': 'string'}}
REF = {'$ref': '#/components/parameters/Token'}


def fixture(parameters):
    return {'components': {'parameters': {'Token': copy.deepcopy(TOKEN)}},
            'paths': {'/': {'get': {'parameters': parameters}}}}


class ParameterLists(unittest.TestCase):
    def test_inline_ref_duplicate_rejected(self):
        with self.assertRaisesRegex(ValueError, 'duplicate parameter'):
            validate_parameter_lists(fixture([TOKEN, REF]))

    def test_inline_and_ref_duplicates_each_rejected(self):
        for entries in ([TOKEN, TOKEN], [REF, REF]):
            with self.subTest(entries=entries), self.assertRaisesRegex(ValueError, 'duplicate parameter'):
                validate_parameter_lists(fixture(entries))

    def test_distinct_name_or_location_and_path_override_accepted(self):
        spec = fixture([REF, {**TOKEN, 'in': 'query'}, {**TOKEN, 'name': 'Other'}])
        spec['paths']['/']['parameters'] = [{**TOKEN, 'required': False}]
        validate_parameter_lists(spec)

    def test_path_list_duplicate_rejected(self):
        spec = fixture([])
        spec['paths']['/']['parameters'] = [TOKEN, REF]
        with self.assertRaisesRegex(ValueError, 'duplicate parameter'):
            validate_parameter_lists(spec)

    def test_unknown_external_cycle_and_wrong_target_rejected(self):
        for target in ('#/components/parameters/Missing', 'https://example.invalid/params.json',
                       '#/components/parameters/Cycle', '#/components/parameters/Bad'):
            spec = fixture([{'$ref': target}])
            spec['components']['parameters'].update({'Cycle': {'$ref': '#/components/parameters/Cycle'}, 'Bad': []})
            with self.subTest(target=target), self.assertRaises(ValueError):
                validate_parameter_lists(spec)

    def test_escaped_reference_chain_resolved(self):
        spec = fixture([REF])
        spec['components']['parameters']['Token'] = {'$ref': '#/components/parameters/a~1b~0c%20d'}
        spec['components']['parameters']['a/b~c d'] = TOKEN
        validate_parameter_lists(spec)

    def test_unused_component_unknown_reference_rejected(self):
        spec = fixture([])
        spec['components']['parameters']['Unused'] = {'$ref': '#/missing'}
        with self.assertRaisesRegex(ValueError, 'unknown reference'):
            validate_parameter_lists(spec)

    def test_callbacks_webhooks_and_reusable_paths_checked(self):
        for container in ('callbacks', 'webhooks', 'pathItems'):
            spec = fixture([])
            bad = {'post': {'parameters': [TOKEN, REF]}}
            if container == 'callbacks':
                spec['paths']['/']['get']['callbacks'] = {'Event': {'{$request.body#/url}': bad}}
            elif container == 'webhooks':
                spec['webhooks'] = {'Event': bad}
            else:
                spec['components']['pathItems'] = {'Reusable': bad}
            with self.subTest(container=container), self.assertRaisesRegex(ValueError, 'duplicate parameter'):
                validate_parameter_lists(spec)

    def test_invalid_parameter_shape_rejected(self):
        for value in (None, [], {}, {'name': 'x', 'in': 'invalid'}, {'name': '', 'in': 'header'}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_parameter_lists(fixture([value]))
        spec = fixture({})
        with self.assertRaisesRegex(ValueError, 'must be an array'):
            validate_parameter_lists(spec)

    def test_actual_spec_and_reintroduced_inline_token(self):
        root = Path(__file__).resolve().parents[2]
        repaired = json.loads((root / 'internal/server/apidocs/openapi.json').read_text())
        validate_parameter_lists(repaired)
        ids = [op['operationId'] for path in repaired['paths'].values() for op in path.values()
               if isinstance(op, dict) and 'operationId' in op]
        added = {'GCPDownloadObject', 'GCPListBuckets', 'GCPCreateBucket', 'GCPGetBucket', 'GCPDeleteBucket',
                 'GCPListObjects', 'GCPGetObject', 'GCPDeleteObject', 'GCPUploadObject',
                 'AzureListContainers', 'AzureCreateContainer', 'AzureGetContainerProperties',
                 'AzureListBlobs', 'AzureDeleteContainer', 'AzurePutBlob', 'AzureGetBlob',
                 'AzureGetBlobProperties', 'AzureDeleteBlob'}
        self.assertEqual(len(ids), 61)
        self.assertEqual(len(set(ids)), 61)
        self.assertEqual({name for name in ids if name.startswith(('GCP', 'Azure'))}, added)
        self.assertEqual(len(set(ids) - added), 43)
        for item in repaired['paths'].values():
            for operation in item.values():
                if isinstance(operation, dict) and {'$ref': '#/components/parameters/SimulatorToken'} in operation.get('parameters', []):
                    operation['parameters'].append(copy.deepcopy(TOKEN))
                    with self.assertRaisesRegex(ValueError, 'duplicate parameter'):
                        validate_parameter_lists(repaired)
                    return
        self.fail('No operation with the shared SimulatorToken reference')


if __name__ == '__main__':
    unittest.main()
