"""Static navigation checks; native contract comparison needs an explicit baseline.

This detector never interprets a documentation-only tag as an implemented API.
"""
import argparse
import copy
import json
from pathlib import Path
import unittest

from test_sakura_openapi import NEW_IDS, COLLECTION, ITEM
from metrics_openapi import preserved_metrics_reference

METHODS = {'get', 'head', 'put', 'post', 'delete', 'patch', 'options'}
GROUPS = ['AWS', 'Google Cloud', 'Azure', 'Sakura Cloud', 'Simulator']
EXPECTED_IDS = set('''AddPermission AzureCreateContainer AzureDeleteBlob AzureDeleteContainer
AzureGetBlob AzureGetBlobProperties AzureGetContainerProperties AzureListBlobs
AzureListContainers AzurePutBlob Capabilities CreateBucket CreateDeployment
CreateFunction CreateResource CreateRestApi CreateStage DeleteBucket DeleteFunction
DeleteIdentityPolicy DeleteObject DeleteResource DeleteRestApi DeleteStage
GCPCreateBucket GCPDeleteBucket GCPDeleteObject GCPDownloadObject GCPGetBucket
GCPGetObject GCPListBuckets GCPListObjects GCPUploadObject GetDeployment
GetFunctionConfiguration GetIdentityPolicy GetMethod GetObject GetPolicy GetResource
GetResources GetRestApi GetRestApis GetStage HeadBucket HeadObject IAMRoleQueryOperation
Invoke InvokeLocalRESTProxyGet InvokeLocalRESTProxyPost ListBuckets ListFunctions
ListObjectsV2OrGetBucketLocation ListPrincipals PutIdentityPolicy PutIntegration
PutMethod PutObject RemovePermission UpdateFunctionCode awsRegionalOperation'''.split())
GUIDES = {'Systems Manager Parameter Store', 'CloudFormation stacks', 'KMS service guide',
          'SQS queues', 'CloudWatch Logs', 'Secrets Manager', 'DynamoDB tables and items'}
SAKURA_TAG = 'Sakura Cloud status'
SAKURA_DISK_TAG = 'Sakura Disk configuration'


def operations(document):
    return [(path, method, operation)
            for path, item in document['paths'].items()
            for method, operation in item.items() if method in METHODS]


def native_projection(document):
    return {key: value for key, value in document.items()
            if key not in ('tags', 'x-tagGroups')}


def compare_native(document, baseline):
    assert native_projection(document) == native_projection(baseline), (
        'Cloud navigation changed native paths, operations, tags, schemas, examples, '
        'headers, statuses, security or another non-navigation field')


def validated_main_projection(document):
    # Admit the exact three Metrics form branches before observing immutable public66/36.
    return preserved_metrics_reference(document)


def validate_document(document):
    document = validated_main_projection(document)
    assert document['openapi'] == '3.1.0'
    tags = document['tags']
    names = [tag['name'] for tag in tags]
    assert len(names) == len(set(names)), 'Duplicate declared tag'
    tags_by_name = {tag['name']: tag for tag in tags}
    groups = document['x-tagGroups']
    assert [group['name'] for group in groups] == GROUPS
    assigned = [tag for group in groups for tag in group['tags']]
    assert len(assigned) == len(set(assigned)) and set(assigned) == set(names), (
        'Every tag must appear in exactly one cloud/control group')
    assert all(group['tags'] for group in groups), 'An empty group is not discoverable'
    group_for = {tag: group['name'] for group in groups for tag in group['tags']}
    assert group_for['Google Cloud Storage'] == 'Google Cloud'
    assert group_for['Azure Blob Storage'] == 'Azure'
    assert group_for[SAKURA_TAG] == 'Sakura Cloud'
    assert set(groups[3]['tags']) == {SAKURA_TAG, SAKURA_DISK_TAG}
    assert {'Simulator control', 'Simulator IAM state'} == set(groups[4]['tags'])
    assert GUIDES <= set(groups[0]['tags'])
    assert tags_by_name['KMS local symmetric keys']['x-displayName'] == 'Shared AWS JSON/Query endpoint'
    for guide in GUIDES:
        description = tags_by_name[guide].get('description', '')
        assert 'existing shared AWS root POST' in description
        assert 'static' in description and 'not saved state' in description
        assert 'headers' in description and 'Test Request' in description
    status = tags_by_name[SAKURA_TAG].get('description', '')
    assert 'Disk configuration API only' in status and 'disabled by default' in status
    assert 'real VM/block storage' in status and 'not claim console workflow/fidelity' in status
    ops = operations(document)
    ids = [operation['operationId'] for _, _, operation in ops]
    assert len(document['paths']) == 36 and len(ids) == 66
    assert len(ids) == len(set(ids)) and set(ids) == EXPECTED_IDS | NEW_IDS
    for path, _, operation in ops:
        assert operation.get('tags'), 'An operation must remain discoverable'
        expected = ('Google Cloud' if path.startswith('/_simulator/providers/gcp/') else
                    'Azure' if path.startswith('/_simulator/providers/azure/') else
                    'Sakura Cloud' if path in (COLLECTION, ITEM) else
                    'Simulator' if path.startswith('/_simulator/v1/') else 'AWS')
        for tag in operation['tags']:
            assert tag in group_for and group_for[tag] == expected, (path, tag, expected)
            assert tag != SAKURA_TAG and tag not in GUIDES, 'A guide must not invent API operations'
    return {'cloudGroups': GROUPS[:4], 'controlGroup': 'Simulator', 'operationIDs': len(ids),
            'paths': 36, 'documentationOnlyServiceGuides': len(GUIDES), 'SakuraOperations': len([op for _,_,op in ops if op['operationId'] in NEW_IDS])}


class ContractTests(unittest.TestCase):
    def setUp(self):
        self.document = json.loads(Path('internal/server/apidocs/openapi.json').read_text())

    def rejected(self, change):
        bad = copy.deepcopy(self.document)
        change(bad)
        with self.assertRaises(AssertionError):
            validate_document(bad)

    def test_current_navigation(self):
        self.assertEqual(validate_document(self.document)['SakuraOperations'], 5)

    def test_missing_group(self):
        self.rejected(lambda d: d['x-tagGroups'].pop(0))

    def test_unknown_group(self):
        self.rejected(lambda d: d['x-tagGroups'][0].update(name='Another cloud'))

    def test_empty_group(self):
        self.rejected(lambda d: d['x-tagGroups'][3].update(tags=[]))

    def test_unassigned_tag(self):
        self.rejected(lambda d: d['x-tagGroups'][0]['tags'].pop())

    def test_duplicate_assignment(self):
        self.rejected(lambda d: d['x-tagGroups'][1]['tags'].append('S3 basic object storage'))

    def test_wrong_provider(self):
        def change(d):
            d['x-tagGroups'][1]['tags'], d['x-tagGroups'][2]['tags'] = (
                d['x-tagGroups'][2]['tags'], d['x-tagGroups'][1]['tags'])
        self.rejected(change)

    def test_sakura_status_tag_is_not_an_api(self):
        self.rejected(lambda d: d['paths']['/']['get'].update(tags=[SAKURA_TAG]))

    def test_service_guide_is_not_an_api(self):
        self.rejected(lambda d: d['paths']['/']['post'].update(tags=['SQS queues']))

    def test_missing_operation(self):
        self.rejected(lambda d: d['paths']['/'].pop('get'))

    def test_duplicate_operation(self):
        self.rejected(lambda d: d['paths']['/']['get'].update(operationId='awsRegionalOperation'))

    def test_native_projection_ignores_only_navigation_metadata(self):
        baseline = copy.deepcopy(self.document)
        baseline.pop('x-tagGroups')
        baseline['tags'] = []
        compare_native(self.document, baseline)

    def test_native_schema_mutation_rejected(self):
        changed = copy.deepcopy(self.document)
        changed['components']['schemas']['Unexpected'] = {'type': 'string'}
        with self.assertRaises(AssertionError):
            compare_native(changed, self.document)

    def test_native_example_mutation_rejected(self):
        changed = copy.deepcopy(self.document)
        changed['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']['examples']['SSMGetParameter']['value'] = {'Name': '/changed'}
        with self.assertRaises(AssertionError):
            compare_native(changed, self.document)

    def test_native_operation_tag_mutation_rejected(self):
        changed = copy.deepcopy(self.document)
        changed['paths']['/']['post']['tags'] = ['IAM roles']
        with self.assertRaises(AssertionError):
            compare_native(changed, self.document)


class ClosedMetricsExtensionTests(unittest.TestCase):
    """Both real reader guards must refuse changed documents without incidental errors."""
    def setUp(self):
        from sqs_docs import validate_reference_inventory
        self.document = json.loads(Path('internal/server/apidocs/openapi.json').read_text())
        self.guards = (validate_document, validate_reference_inventory)

    def reject_both(self, change):
        for guard in self.guards:
            bad = copy.deepcopy(self.document)
            change(bad)
            unchanged = copy.deepcopy(bad)
            with self.subTest(guard=guard.__name__):
                with self.assertRaises(AssertionError):
                    guard(bad)
                self.assertEqual(bad, unchanged)

    def test_exact_inverse_and_counts(self):
        import hashlib
        from metrics_openapi import canonical, contract
        before = copy.deepcopy(self.document)
        self.assertEqual((len(operations(self.document)), len(self.document['paths']),
                          len(self.document['components']['schemas'])), (66, 36, 121))
        for guard in self.guards:
            guard(self.document)
        original = preserved_metrics_reference(self.document)
        self.assertEqual(hashlib.sha256(canonical(original).encode()).hexdigest(), contract()['base66CanonicalSHA256'])
        self.assertEqual(self.document, before)

    def test_each_missing_old_operation_rejected(self):
        for path, method, _ in operations(preserved_metrics_reference(self.document)):
            with self.subTest(path=path, method=method):
                self.reject_both(lambda d: d['paths'][path].pop(method))

    def test_missing_root_post_rejected(self):
        self.reject_both(lambda d: d['paths']['/'].pop('post'))

    def test_extra_operation_rejected(self):
        self.reject_both(lambda d: d['paths'].update({'/unexpected': {'get': copy.deepcopy(d['paths']['/']['get'])}}))

    def test_duplicate_operation_rejected(self):
        self.reject_both(lambda d: d['paths']['/']['get'].update(operationId='awsRegionalOperation'))

    def test_old_method_id_mapping_swap_rejected(self):
        def change(d):
            a,b = d['paths']['/']['get'],d['paths']['/']['post']
            a['operationId'],b['operationId'] = b['operationId'],a['operationId']
        self.reject_both(change)

    def test_moved_old_path_rejected(self):
        self.reject_both(lambda d: d['paths'].update({'/renamed-functions': d['paths'].pop('/2015-03-31/functions')}))

    def test_missing_unknown_duplicate_aws_group_rejected(self):
        def missing(d): d['x-tagGroups'][:] = [g for g in d['x-tagGroups'] if g['name']!='AWS']
        def unknown(d): next(g for g in d['x-tagGroups'] if g['name']=='AWS')['name']='Another cloud'
        def duplicate(d): d['x-tagGroups'].append(copy.deepcopy(next(g for g in d['x-tagGroups'] if g['name']=='AWS')))
        for edit in (missing,unknown,duplicate): self.reject_both(edit)

    def test_metrics_provider_assignment_rejected(self):
        def change(d):
            next(g for g in d['x-tagGroups'] if g['name']=='AWS')['tags'].remove('CloudWatch Metrics')
            next(g for g in d['x-tagGroups'] if g['name']=='Azure')['tags'].append('CloudWatch Metrics')
        self.reject_both(change)

    def test_each_metrics_example_and_schema_required(self):
        from metrics_openapi import contract, MEDIA
        for name in contract()['examples']:
            self.reject_both(lambda d: d['paths']['/']['post']['requestBody']['content'][MEDIA]['examples'].pop(name))
        for name in contract()['schemaNames']:
            self.reject_both(lambda d: d['components']['schemas'].pop(name))

    def test_metric_version_and_routing_drift_rejected(self):
        from metrics_openapi import MEDIA
        self.reject_both(lambda d: d['paths']['/']['post']['requestBody']['content'][MEDIA]['examples']['CloudWatchPutMetricData']['value'].update(Version='wrong'))
        def routing(d):
            next(p for p in d['paths']['/']['post']['parameters'] if p.get('name')=='X-Simulator-Service')['schema']['enum'].remove('monitoring')
        self.reject_both(routing)

    def test_auth_reference_required(self):
        def change(d):
            parameters=d['paths']['/']['post']['parameters']
            parameters[:]=[p for p in parameters if p.get('$ref')!='#/components/parameters/SimulatorToken']
        self.reject_both(change)

    def test_unknown_schema_rejected(self):
        self.reject_both(lambda d: d['components']['schemas'].update(Unexpected={'type':'string'}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--spec', default='internal/server/apidocs/openapi.json')
    parser.add_argument('--baseline')
    parser.add_argument('--self-test', action='store_true')
    args = parser.parse_args()
    if args.self_test:
        unittest.main(argv=['api_cloud_sections_contract'], exit=True)
    document = json.loads(Path(args.spec).read_text())
    result = validate_document(document)
    if args.baseline:
        compare_native(document, json.loads(Path(args.baseline).read_text()))
        result['nativeProjectionPreserved'] = True
    else:
        result['nativeProjectionPreserved'] = 'not checked: explicit baseline required'
    print(json.dumps(result))
