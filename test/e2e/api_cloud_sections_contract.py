"""Static navigation checks; native contract comparison needs an explicit baseline.

This detector never interprets a documentation-only tag as an implemented API.
"""
import argparse
import copy
import json
from pathlib import Path
import unittest

METHODS = {'get', 'head', 'put', 'post', 'delete', 'patch', 'options'}
GROUPS = ['AWS', 'GCP', 'Azure', 'Sakura', 'Simulator']
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


def validate_document(document):
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
    assert group_for['Google Cloud Storage'] == 'GCP'
    assert group_for['Azure Blob Storage'] == 'Azure'
    assert group_for[SAKURA_TAG] == 'Sakura'
    assert set(groups[3]['tags']) == {SAKURA_TAG}
    assert {'Simulator control', 'Simulator IAM state'} == set(groups[4]['tags'])
    assert GUIDES <= set(groups[0]['tags'])
    assert tags_by_name['KMS local symmetric keys']['x-displayName'] == 'Shared AWS JSON/Query endpoint'
    for guide in GUIDES:
        description = tags_by_name[guide].get('description', '')
        assert 'existing shared AWS root POST' in description
        assert 'static' in description and 'not saved state' in description
        assert 'headers' in description and 'Test Request' in description
    status = tags_by_name[SAKURA_TAG].get('description', '')
    assert '**Not implemented.**' in status and 'no simulator API operations' in status
    ops = operations(document)
    ids = [operation['operationId'] for _, _, operation in ops]
    assert len(document['paths']) == 34 and len(ids) == 61
    assert len(ids) == len(set(ids)) and set(ids) == EXPECTED_IDS
    for path, _, operation in ops:
        assert operation.get('tags'), 'An operation must remain discoverable'
        expected = ('GCP' if path.startswith('/_simulator/providers/gcp/') else
                    'Azure' if path.startswith('/_simulator/providers/azure/') else
                    'Simulator' if path.startswith('/_simulator/v1/') else 'AWS')
        for tag in operation['tags']:
            assert tag in group_for and group_for[tag] == expected, (path, tag, expected)
            assert tag != SAKURA_TAG and tag not in GUIDES, 'A guide must not invent API operations'
    return {'cloudGroups': GROUPS[:4], 'controlGroup': 'Simulator', 'operationIDs': len(ids),
            'paths': 34, 'documentationOnlyServiceGuides': len(GUIDES), 'SakuraOperations': 0}


class ContractTests(unittest.TestCase):
    def setUp(self):
        self.document = json.loads(Path('internal/server/apidocs/openapi.json').read_text())

    def rejected(self, change):
        bad = copy.deepcopy(self.document)
        change(bad)
        with self.assertRaises(AssertionError):
            validate_document(bad)

    def test_current_navigation(self):
        self.assertEqual(validate_document(self.document)['SakuraOperations'], 0)

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

    def test_sakura_is_not_an_api(self):
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
