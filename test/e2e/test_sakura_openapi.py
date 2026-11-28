"""Static Sakura schema/examples and exact existing reference preservation.

Uses existing Draft202012Validator locally; no HTTP/server, SDK, browser or
full OpenAPI-validator claim. Native parser lexical constraints are separate.
"""
import copy
import hashlib
import json
from pathlib import Path
import unittest

from jsonschema import ValidationError
from openapi_json10 import validator
from cloudwatch_reference_projection import project_monitoring
from route53_openapi import project_before_route53
from route53_ptr_reference import project_before_ptr
from batch_reference_projection import project_batches
from secret_reference_projection import project_secret_description
from ssm_document_reference import project_before_ssm_document_versions

ROOT = Path(__file__).resolve().parents[2]
COLLECTION = '/_simulator/providers/sakura/cloud/zone/{zone}/api/cloud/1.1/disk'
ITEM = COLLECTION + '/{diskId}'
NEW_IDS = {'SakuraListDisks','SakuraCreateDisk','SakuraGetDisk','SakuraUpdateDisk','SakuraDeleteDisk'}
MULTIPART_IDS = {'S3MultipartUpload'}
OLD_IDS = {'AddPermission', 'AzureCreateContainer', 'AzureDeleteBlob', 'AzureDeleteContainer', 'AzureGetBlob', 'AzureGetBlobProperties', 'AzureGetContainerProperties', 'AzureListBlobs', 'AzureListContainers', 'AzurePutBlob', 'Capabilities', 'CreateBucket', 'CreateDeployment', 'CreateFunction', 'CreateResource', 'CreateRestApi', 'CreateStage', 'DeleteBucket', 'DeleteFunction', 'DeleteIdentityPolicy', 'DeleteObject', 'DeleteResource', 'DeleteRestApi', 'DeleteStage', 'GCPCreateBucket', 'GCPDeleteBucket', 'GCPDeleteObject', 'GCPDownloadObject', 'GCPGetBucket', 'GCPGetObject', 'GCPListBuckets', 'GCPListObjects', 'GCPUploadObject', 'GetDeployment', 'GetFunctionConfiguration', 'GetIdentityPolicy', 'GetMethod', 'GetObject', 'GetPolicy', 'GetResource', 'GetResources', 'GetRestApi', 'GetRestApis', 'GetStage', 'HeadBucket', 'HeadObject', 'IAMRoleQueryOperation', 'Invoke', 'InvokeLocalRESTProxyGet', 'InvokeLocalRESTProxyPost', 'ListBuckets', 'ListFunctions', 'ListObjectsV2OrGetBucketLocation', 'ListPrincipals', 'PutIdentityPolicy', 'PutIntegration', 'PutMethod', 'PutObject', 'RemovePermission', 'UpdateFunctionCode', 'awsRegionalOperation'}
METHODS = {'get','post','put','delete','head','patch','options'}
JSON11 = 'application/x-amz-json-1.1'


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()


def operations(spec):
    return [(path,method,value) for path,item in spec['paths'].items() for method,value in item.items() if method in METHODS]



def project_simpledb_create_domain(spec):
    """Undo only the bounded CreateDomain preset before the historical guards."""
    form = spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']
    fields = form['schema']['properties']
    actions = fields['Action']['enum']
    assert actions.count('CreateDomain') == 1 and actions[-1] == 'CreateDomain', 'SimpleDB CreateDomain action changed'
    actions.pop()
    examples = form['examples']
    assert next(iter(examples)) == 'DescribeStacks', 'Default Query example changed'
    assert digest(examples.pop('CreateDomain', None)) == 'd6a8cee60d5cdb13ff54982c39ba0da3d68c6b9023daf96c2f6492b5eb64bdb6', 'SimpleDB CreateDomain preset changed'
    domain = fields.get('DomainName', {})
    assert domain.get('description') == 'For CreateDomain, choose a disposable local domain name. For DomainMetadata, GetAttributes or DeleteAttributes, use an existing local domain. Do not include DomainName for Select; use the domain in SelectExpression.', 'SimpleDB CreateDomain DomainName note changed'
    domain['description'] = 'Existing local domain for DomainMetadata, GetAttributes or DeleteAttributes. Do not include DomainName for Select; use the domain in SelectExpression.'


def project_ec2_describe_instances(spec):
    """Undo exactly the one EC2 Query preset, leaving all inherited defaults intact."""
    post = spec['paths']['/']['post']
    service, = (p for p in post['parameters'] if p.get('name') == 'X-Simulator-Service')
    form = post['requestBody']['content']['application/x-www-form-urlencoded']
    fields = form['schema']['properties']
    for choices, value in ((service['schema']['enum'], 'ec2'),
                           (fields['Action']['enum'], 'DescribeInstances'),
                           (fields['Version']['enum'], '2016-11-15')):
        assert choices.count(value) == 1 and choices[-1] == value, 'EC2 option changed: ' + value
        choices.pop()
    examples = form['examples']
    assert next(iter(examples)) == 'DescribeStacks', 'Default Query example changed'
    assert digest(examples.pop('DescribeInstances', None)) == '98e0dc514c8397bec15a2535638fc0a14778a98a42c97aa09b85e0d2950fa674', 'EC2 DescribeInstances preset changed'


def project_simpledb_get_attributes(spec):
    """Undo only the reviewed GetAttributes preset and its shared field notes."""
    form = spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']
    props = form['schema']['properties']
    actions = props['Action']['enum']
    assert actions.count('GetAttributes') == 1 and actions[-1] == 'GetAttributes', 'SimpleDB GetAttributes action changed'
    actions.pop()
    assert next(iter(form['examples'])) == 'DescribeStacks', 'Existing root POST default example changed'
    assert digest(form['examples'].pop('GetAttributes', None)) == '7d9e2594400ac49720ee8792e67549ebb027ac64aa5cb1489c389e0d6abe01fd', 'SimpleDB GetAttributes preset changed'
    assert digest(props.pop('ItemName', None)) == '4f3ce58d185af32421e5714428e0a11cebb1124e92add7445387ca04d831f516', 'SimpleDB ItemName field changed'
    domain = props.get('DomainName', {})
    assert domain.get('description') == 'Existing local domain for DomainMetadata, GetAttributes or DeleteAttributes. Do not include DomainName for Select; use the domain in SelectExpression.', 'SimpleDB GetAttributes domain note changed'
    domain['description'] = 'Existing local domain for DomainMetadata or DeleteAttributes. Do not include DomainName for Select; use the domain in SelectExpression.'
    consistent = props.get('ConsistentRead', {})
    assert consistent.get('description') == 'Optional for GetAttributes or Select. Keep the same value on every Select page.', 'SimpleDB GetAttributes consistency note changed'
    consistent['description'] = 'Optional for Select. Keep the same value on every page.'


def project_simpledb_select(spec):
    """Project only the precise reviewed additions, preserving all earlier guards."""
    form = spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']
    props = form['schema']['properties']
    actions = props['Action']['enum']
    # DeleteAttributes previously had a template but was missing from the enum.
    assert actions[-2:] == ['DeleteAttributes', 'Select'] and all(actions.count(a) == 1 for a in actions[-2:]), 'SimpleDB Select action list changed'
    del actions[-2:]
    fields = {k: props.pop(k, None) for k in ('SelectExpression', 'ConsistentRead', 'NextToken')}
    assert digest(fields) == '1f253a796e2b3f502a9bfd70be5f1c0140ea8cd974db9646497a7a743401111a', 'SimpleDB Select fields changed'
    assert digest(form['examples'].pop('Select', None)) == '2d5602fc395a166ae8ded1f65fb99b43e0aaee42ec97751cceb224e565d62c2c', 'SimpleDB Select example changed'
    domain = props.get('DomainName', {})
    assert domain.get('description') == 'Existing local domain for DomainMetadata or DeleteAttributes. Do not include DomainName for Select; use the domain in SelectExpression.', 'SimpleDB domain documentation changed'
    domain['description'] = 'Existing local SimpleDB domain name, required for DomainMetadata only in these presets.'


def project_simpledb_domain_metadata(spec):
    """Validate and remove the exact additive read-only metadata preset."""
    form = spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']
    props = form['schema']['properties']
    actions = props['Action']['enum']
    assert actions.count('DomainMetadata') == 1 and actions[-1] == 'DomainMetadata', 'SimpleDB metadata action changed'
    actions.pop()
    assert digest(props.pop('DomainName', None)) == 'd83344e74f00a68db4243541dc1c653b379c0dfcc964f9c6d932fc72a69bfe44', 'SimpleDB metadata domain input changed'
    assert digest(form['examples'].pop('DomainMetadata', None)) == 'd5fbab8045e59efb874fa56f007e41554cbc01660f44b57f34bc7e3637191c69', 'SimpleDB metadata example changed'


def project_simpledb_list_domains(spec):
    """Validate then remove only the reviewed root POST reference additions."""
    post = spec['paths']['/']['post']
    services = [p for p in post['parameters'] if p.get('name') == 'X-Simulator-Service']
    assert len(services) == 1, 'SimpleDB service selector changed'
    form = post['requestBody']['content']['application/x-www-form-urlencoded']
    props = form['schema']['properties']
    for values, choice in ((services[0]['schema']['enum'], 'sdb'),
                           (props['Action']['enum'], 'ListDomains'),
                           (props['Version']['enum'], '2009-04-15')):
        assert values.count(choice) == 1 and values[-1] == choice, 'SimpleDB option changed: ' + choice
        values.pop()
    example = form['examples'].pop('ListDomains', None)
    assert digest(example) == '46fb6adf3e85a3be1ce01bfb2aedf045387aae365b8715f1fe7b127d1ee536ef', 'SimpleDB ListDomains example changed'
    response = post['responses']['200']['content'].pop('text/xml', None)
    assert digest(response) == '11a7932f16b348011034c0f82be5a858922db9d92fcec924d3190612c5c6bc09', 'SimpleDB XML media/example changed'



def project_simpledb_delete_attributes(spec):
    """Validate the exact conditional single-value XML Query preset."""
    form = spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']
    example = form['examples'].pop('DeleteAttributes', None)
    assert digest(example) == '155821983ff943031d61913b8d87b4a4bf6ea735567231b1fb51bfff69e18536', 'SimpleDB DeleteAttributes preset changed'


SERVER_COLLECTION = '/_simulator/providers/sakura/cloud/zone/{zone}/api/cloud/1.1/server'


def project_sakura_server_list_only(spec):
    """Check and remove only the reviewed Sakura Server list path and tag."""
    spec = copy.deepcopy(spec)
    tag = {'name': 'Sakura Server metadata',
           'description': 'Read saved Server metadata in the configured local zone; no VM or network state.'}
    declared = [entry for entry in spec['tags'] if entry['name'] == tag['name']]
    grouped = [group for group in spec['x-tagGroups'] if tag['name'] in group['tags']]
    assert declared == [tag], 'Server List tag missing or changed'
    assert len(grouped) == 1 and grouped[0]['name'] == 'Sakura Cloud', 'Server List tag misgrouped'
    assert grouped[0]['tags'].count(tag['name']) == 1, 'Server List tag duplicated'
    spec['tags'].remove(tag)
    grouped[0]['tags'].remove(tag['name'])
    server = spec['paths'].pop(SERVER_COLLECTION, None)
    assert digest(server) == '81203e3d7065de93e30b5941964ba52f94a11c12888f61b85dbe7c542dc9a521', 'Server List reference changed'
    return spec


def project_server_list(spec):
    spec = project_sakura_server_list_only(spec)
    project_monitoring(spec)
    project_simpledb_create_domain(spec)
    project_ec2_describe_instances(spec)
    project_simpledb_delete_attributes(spec)
    project_simpledb_get_attributes(spec)
    project_simpledb_select(spec)
    project_simpledb_domain_metadata(spec)
    project_simpledb_list_domains(spec)
    inherited = copy.deepcopy(spec)
    project_before_ptr(inherited['paths'])
    # The older Sakura guard already covered the preceding SSM API. The
    # full SSM projection later still sees and validates all seven operations.
    project_before_ssm_document_versions(inherited)
    assert digest(inherited['paths']) == '1dce93acc032ee7c312d7a8bd9e7442368953b59403f494e5fb3ab8e20d2ede8', 'Pre-existing reference paths changed'
    assert digest(inherited) == '62dc82bc8496d80e92364df28c940012d22498553e779929dd10fa431a0a57c6', 'Pre-existing reference spec changed'
    return spec


def preserved(spec):
    spec = project_server_list(spec)
    spec = project_before_route53(spec)
    # Validate the three reviewed DeleteLogStream additions exactly, then remove
    # only those additions from a copy. Every pre-existing digest below is kept
    # unchanged. This projection was checked against public main36e72c4 in full.
    spec = copy.deepcopy(spec)
    project_secret_description(spec)
    project_batches(spec)
    media = spec['paths']['/']['post']['requestBody']['content'][JSON11]
    assert len(media['examples']) == 29
    example = media['examples'].pop('LogsDeleteLogStream', None)
    assert digest(example) == '4cdfa75bc2b88d7b001a0ac6b79335cbf71f149bcfc119d35f17dd9b2612ac9d', 'DeleteLogStream example changed'
    schema = spec['components']['schemas'].pop('LogsDeleteLogStreamRequest', None)
    assert digest(schema) == '6f9dfb1a9f99ef42ef8438e1d16f2460734216168877c7fe7792c416aa93d736', 'DeleteLogStream schema changed'
    ref = {'$ref': '#/components/schemas/LogsDeleteLogStreamRequest'}
    union = media['schema']['anyOf']
    assert union.count(ref) == 1 and union[-1] == ref, 'DeleteLogStream union binding changed'
    union.pop()
    # Exact main d166f56 projections plus reviewed bounded S3 multipart queries,
    # descriptions, regional/auth headers and object POST, then the seven native
    # IAM profile examples/action fields at root and the AWS alias. The reviewed
    # profile and pinned SSM builtin additions change only IAM descriptions and
    # PolicyArn fields. Lambda Tail changes only Invoke description/request/response
    # headers. All other paths and components stay exact.
    # The independent paths digest excludes only the two existing Sakura paths.
    original_paths = {path:value for path,value in spec['paths'].items() if path not in (COLLECTION,ITEM)}
    assert len(original_paths)==34
    assert digest(original_paths)=='378c3fd3366f4b824a0f74e613e45edf1663eda1d4281134e3f3fe03732676b0', 'Existing paths/operations/response examples changed'
    original = copy.deepcopy(spec['components'])
    original.pop('securitySchemes',None)
    for group in ('schemas','parameters'):
        original[group]={name:value for name,value in original[group].items() if not name.startswith('Sakura')}
    assert digest(original)=='666030651aa243e81d3c3a5e376473f2ec48725dfc4962fd563dbb1f3bb0998d', 'Existing components changed'
    media=spec['paths']['/']['post']['requestBody']['content'][JSON11]
    assert len(media['examples'])==28
    assert digest(media)=='b96fff5512a8e196907d650263e61c839c1275ca2ea4415991361021852d13a2', 'Shared JSON1.1 schema/templates changed'
    ids=[op['operationId'] for _,_,op in operations(spec)]
    assert len(ids)==67 and len(set(ids))==67 and set(ids)==OLD_IDS|NEW_IDS|MULTIPART_IDS


def native_shape(spec):
    assert {m for m in spec['paths'][COLLECTION] if m in METHODS}=={'get','post'}
    assert {m for m in spec['paths'][ITEM] if m in METHODS}=={'get','put','delete'}
    expected={'ID','Name','Description','SizeMB','Plan','Connection','CreatedAt','ModifiedAt'}
    disk=spec['components']['schemas']['SakuraDisk']
    assert set(disk['properties'])==expected and set(disk['required'])==expected
    assert disk['additionalProperties'] is False
    assert disk['properties']['SizeMB']['const']==20480
    assert disk['properties']['Plan']['properties']['ID']['const']==4
    assert disk['properties']['Connection']['const']=='virtio'
    for path,method,op in operations(spec):
        if op['operationId'] not in NEW_IDS: continue
        assert op['tags']==['Sakura Disk configuration']
        assert op['security']==[{'SakuraDummyProtocolMarker':[]}]
        refs=[p.get('$ref') for p in op['parameters']]
        assert len(refs)==len(set(refs)) and '#/components/parameters/SimulatorToken' in refs
        assert '#/components/parameters/SakuraExerciseIdentity' in refs
        assert not any(p.get('in')=='query' for p in op['parameters'])
        if method in ('get','delete'): assert 'requestBody' not in op
        assert 'disabled by default' in op['description']
        assert '404' in op['responses'] and 'text/plain' in op['responses']['404']['content']
        assert op['responses']['404']['content']['text/plain']['example']=='Provider not implemented\n'
    query=spec['paths'][COLLECTION]['get']['x-sakura-raw-json-query']
    assert query['standardQueryBuilderSupported'] is False
    assert query['example']=={'From':0,'Count':1}
    assert query['encodedSuffix']=='?%7B%22From%22%3A0%2C%22Count%22%3A1%7D'
    scheme=spec['components']['securitySchemes']['SakuraDummyProtocolMarker']
    assert scheme['type']=='http' and scheme['scheme']=='basic'
    assert 'does NOT authenticate' in scheme['description']
    assert not any('Sakura' in x.get('name','') and 'Token' in x.get('name','') for x in spec['components']['parameters'].values())


class SakuraReference(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.spec=json.loads((ROOT/'internal/server/apidocs/openapi.json').read_text())

    def test_simpledb_create_domain_adds_only_the_existing_bounded_operation(self):
        def form(spec):
            return spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']

        media = form(self.spec)
        value = media['examples']['CreateDomain']['value']
        validator(self.spec, media['schema']).validate(value)
        self.assertEqual(value, {'Action': 'CreateDomain', 'Version': '2009-04-15',
                                 'DomainName': 'scalar-practice-domain'})
        for label, change in (
            ('duplicate action', lambda f: f['schema']['properties']['Action']['enum'].append('CreateDomain')),
            ('extra operation', lambda f: f['examples'].update(DeleteDomain={'value': {'Action': 'DeleteDomain'}})),
            ('unbounded body', lambda f: f['examples']['CreateDomain']['value'].update(ItemName='unexpected')),
            ('misleading read hint', lambda f: f['examples']['CreateDomain'].update(summary='SimpleDB CreateDomain (read-only)')),
            ('hidden scope note', lambda f: f['schema']['properties']['DomainName'].update(description='Domain name')),
            ('default example reordered', lambda f: f['examples'].update(DescribeStacks=f['examples'].pop('DescribeStacks'))),
        ):
            with self.subTest(label=label), self.assertRaises(AssertionError):
                changed = copy.deepcopy(self.spec)
                change(form(changed))
                project_server_list(changed)
        preserved(self.spec)

    def test_ec2_preset_allows_only_bounded_read(self):
        post = self.spec['paths']['/']['post']
        media = 'application/x-www-form-urlencoded'
        form = post['requestBody']['content'][media]
        value = form['examples']['DescribeInstances']['value']
        self.assertEqual(value, {'Action': 'DescribeInstances', 'Version': '2016-11-15'})
        for label, change in (
            ('unsupported launch', lambda p, f: f['examples'].update(RunInstances={'value': {'Action': 'RunInstances'}})),
            ('unsupported filter', lambda p, f: f['examples']['DescribeInstances']['value'].update({'Filter.1.Name': 'tag:Name'})),
            ('missing service choice', lambda p, f: next(h for h in p['parameters'] if h.get('name') == 'X-Simulator-Service')['schema']['enum'].remove('ec2')),
            ('duplicate version', lambda p, f: f['schema']['properties']['Version']['enum'].append('2016-11-15')),
        ):
            with self.subTest(label=label):
                broken = copy.deepcopy(self.spec)
                p = broken['paths']['/']['post']
                change(p, p['requestBody']['content'][media])
                with self.assertRaises(AssertionError):
                    project_server_list(broken)
        project_server_list(self.spec)

    def test_single_server_list_preserves_every_old_path(self):
        server = self.spec['paths'][SERVER_COLLECTION]
        assert set(server) == {'parameters', 'get'}
        assert server['get']['operationId'] == 'SakuraListServers'
        for change in (
            lambda paths: paths.pop(SERVER_COLLECTION),
            lambda paths: paths[SERVER_COLLECTION].update(post={}),
            lambda paths: paths[COLLECTION]['get'].update(summary='mutated Disk'),
            lambda paths: paths.update({'/unrelated-new': {}}),
        ):
            broken = copy.deepcopy(self.spec)
            change(broken['paths'])
            with self.assertRaises(AssertionError):
                project_server_list(broken)
        project_server_list(self.spec)

    def test_server_list_navigation_rejects_missing_and_misgrouped_tag(self):
        for change in ('declaration', 'missing-group', 'wrong-group', 'duplicate'):
            broken = copy.deepcopy(self.spec)
            tag = 'Sakura Server metadata'
            owner = next(group for group in broken['x-tagGroups'] if group['name'] == 'Sakura Cloud')
            if change == 'declaration':
                broken['tags'] = [entry for entry in broken['tags'] if entry['name'] != tag]
            elif change == 'duplicate':
                owner['tags'].append(tag)
            else:
                owner['tags'].remove(tag)
                if change == 'wrong-group':
                    next(group for group in broken['x-tagGroups'] if group is not owner)['tags'].append(tag)
            with self.subTest(change=change), self.assertRaises(AssertionError):
                project_server_list(broken)

    def test_all_existing_operations_components_and_28_templates_are_exact(self):
        preserved(self.spec)

    def test_read_only_simpledb_select_projection(self):
        def form(d):
            return d['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']
        example = form(self.spec)['examples']['Select']['value']
        validator(self.spec, form(self.spec)['schema']).validate(example)
        self.assertEqual(example, {'Action': 'Select', 'Version': '2009-04-15',
            'SelectExpression': 'select itemName() from `example-domain` limit 25', 'ConsistentRead': 'true'})
        preceding = copy.deepcopy(self.spec)
        project_monitoring(preceding)
        project_simpledb_create_domain(preceding)
        project_ec2_describe_instances(preceding)
        project_simpledb_get_attributes(preceding)
        project_simpledb_select(copy.deepcopy(preceding))
        for change in (
            lambda f: f['schema']['properties']['Action']['enum'].append('Select'),
            lambda f: f['schema']['properties'].pop('ConsistentRead'),
            lambda f: f['schema']['properties']['NextToken'].update(type='integer'),
            lambda f: f['examples']['Select']['value'].update(Action='DeleteAttributes'),
            lambda f: f['examples']['Select']['value'].update(DomainName='elsewhere'),
        ):
            changed = copy.deepcopy(preceding)
            change(form(changed))
            with self.assertRaises(AssertionError):
                project_simpledb_select(changed)

    def test_read_only_simpledb_get_attributes_preserves_previous_presets(self):
        def form(spec):
            return spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']

        actual = form(self.spec)
        example = actual['examples']['GetAttributes']['value']
        validator(self.spec, actual['schema']).validate(example)
        self.assertEqual(example, {'Action': 'GetAttributes', 'Version': '2009-04-15',
                                   'DomainName': 'scalar-practice-domain', 'ItemName': 'scalar-practice-item',
                                   'ConsistentRead': 'true'})
        preceding = copy.deepcopy(self.spec)
        project_monitoring(preceding)
        project_simpledb_create_domain(preceding)
        project_ec2_describe_instances(preceding)
        project_simpledb_get_attributes(copy.deepcopy(preceding))
        preserved(self.spec)
        for label, change in (
            ('missing action', lambda f: f['schema']['properties']['Action']['enum'].remove('GetAttributes')),
            ('duplicate action', lambda f: f['schema']['properties']['Action']['enum'].append('GetAttributes')),
            ('foreign write', lambda f: f['examples']['GetAttributes']['value'].update(Action='PutAttributes')),
            ('write controls', lambda f: f['examples']['GetAttributes']['value'].update({'Expected.Exists': 'false'})),
            ('wrong default', lambda f: f['examples'].update(DescribeStacks=f['examples'].pop('DescribeStacks'))),
            ('unbounded item name', lambda f: f['schema']['properties']['ItemName'].pop('maxLength')),
            ('changed authority note', lambda f: f['examples']['GetAttributes'].update(description='unreviewed')),
            ('changed shared field', lambda f: f['schema']['properties']['DomainName'].update(description='unreviewed')),
            ('changed consistency note', lambda f: f['schema']['properties']['ConsistentRead'].update(description='unreviewed')),
        ):
            changed = copy.deepcopy(self.spec)
            change(form(changed))
            with self.subTest(change=label), self.assertRaises(AssertionError):
                preserved(changed)

    def test_simpledb_projection_cannot_hide_missing_changed_or_inherited_contracts(self):
        def post(spec):
            return spec['paths']['/']['post']

        def service(spec):
            return next(p for p in post(spec)['parameters'] if p.get('name') == 'X-Simulator-Service')

        def form(spec):
            return post(spec)['requestBody']['content']['application/x-www-form-urlencoded']

        def response(spec):
            return post(spec)['responses']['200']['content']

        for label, change in (
            ('missing selector', lambda spec: service(spec)['schema']['enum'].remove('sdb')),
            ('duplicate selector', lambda spec: service(spec)['schema']['enum'].append('sdb')),
            ('selector promoted to default', lambda spec: service(spec)['schema']['enum'].insert(
                0, service(spec)['schema']['enum'].pop())),
            ('missing action', lambda spec: form(spec)['schema']['properties']['Action']['enum'].remove('ListDomains')),
            ('wrong query version', lambda spec: form(spec)['schema']['properties']['Version']['enum'].__setitem__(-1, '2020-01-01')),
            ('missing form example', lambda spec: form(spec)['examples'].pop('ListDomains')),
            ('missing metadata action', lambda spec: form(spec)['schema']['properties']['Action']['enum'].remove('DomainMetadata')),
            ('missing metadata field', lambda spec: form(spec)['schema']['properties'].pop('DomainName')),
            ('wrong metadata domain field shape', lambda spec: form(spec)['schema']['properties']['DomainName'].update(type='integer')),
            ('missing metadata example', lambda spec: form(spec)['examples'].pop('DomainMetadata')),
            ('changed metadata action', lambda spec: form(spec)['examples']['DomainMetadata']['value'].update(Action='ListDomains')),
            ('extra metadata form field', lambda spec: form(spec)['examples']['DomainMetadata']['value'].update(Unexpected='bad')),
            ('changed metadata permission note', lambda spec: form(spec)['examples']['DomainMetadata'].update(description='changed')),
            ('extra form field', lambda spec: form(spec)['examples']['ListDomains']['value'].update(Unexpected='bad')),
            ('changed access note', lambda spec: form(spec)['examples']['ListDomains'].update(description='changed')),
            ('missing XML media', lambda spec: response(spec).pop('text/xml')),
            ('unreviewed XML addition', lambda spec: response(spec)['text/xml']['examples'].update(
                Unexpected={'value': '<unexpected/>'})),
            ('inherited CloudFormation example changed', lambda spec: form(spec)['examples']['DescribeStacks']['value'].update(
                StackName='unexpected')),
            # Keep the batch projection's required union structurally valid so
            # the inherited digest rejects the change as an assertion.
            ('inherited JSON media changed', lambda spec: response(spec)['application/x-amz-json-1.0'].update(
                description='unreviewed media description')),
            ('inherited non-root path changed', lambda spec: spec['paths']['/{bucket}']['put'].update(
                summary='unreviewed description')),
        ):
            broken = copy.deepcopy(self.spec)
            change(broken)
            with self.subTest(change=label), self.assertRaises(AssertionError):
                preserved(broken)

    def test_simpledb_delete_attributes_preset_is_one_conditional_pair(self):
        def examples(spec):
            return spec['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']['examples']

        value = examples(self.spec)['DeleteAttributes']['value']
        self.assertEqual(value, {
            'Action': 'DeleteAttributes', 'Version': '2009-04-15',
            'DomainName': 'example-domain', 'ItemName': 'example-item',
            'Attribute.1.Name': 'example-attribute', 'Attribute.1.Value': 'replace-with-current-value',
            'Expected.Name': 'example-attribute', 'Expected.Value': 'replace-with-current-value',
        })
        for label, change in (
            ('missing preset', lambda x: x.pop('DeleteAttributes')),
            ('missing row selector', lambda x: x['DeleteAttributes']['value'].pop('Attribute.1.Name')),
            ('unconditional delete', lambda x: x['DeleteAttributes']['value'].pop('Expected.Value')),
            ('wrong expected value', lambda x: x['DeleteAttributes']['value'].update({'Expected.Value': 'elsewhere'})),
            ('unreviewed extra input', lambda x: x['DeleteAttributes']['value'].update(Unexpected='extra')),
            ('changed authority note', lambda x: x['DeleteAttributes'].update(description='unreviewed')),
        ):
            broken = copy.deepcopy(self.spec)
            change(examples(broken))
            with self.subTest(change=label), self.assertRaisesRegex(AssertionError, 'SimpleDB DeleteAttributes preset changed'):
                project_server_list(broken)

    def test_document_version_projection_cannot_hide_reference_or_inherited_drift(self):
        name = 'SSMListDocumentVersions'

        def root(spec):
            return spec['paths']['/']['post']

        def request(spec):
            return root(spec)['requestBody']['content'][JSON11]

        def response(spec):
            return root(spec)['responses']['200']['content'][JSON11]

        def move_version(spec):
            union = response(spec)['schema']['anyOf']
            ref = {'$ref': '#/components/schemas/' + name + 'Output'}
            union.remove(ref)
            union.insert(0, ref)

        for label, change, expected in (
            ('missing request', lambda s: request(s)['examples'].pop(name), 'reference changed'),
            ('wrong target', lambda s: request(s)['examples'][name].update(summary='X-Amz-Target: other'), 'reference changed'),
            ('invented response', lambda s: response(s)['examples'][name]['value'].update(Content='new'), 'reference changed'),
            ('weakened input', lambda s: s['components']['schemas'][name + 'Input'].update(additionalProperties=True), 'reference changed'),
            ('widened version model', lambda s: s['components']['schemas']['SSMDocumentVersionInfo']['properties']['DocumentFormat'].update(enum=['JSON', 'YAML']), 'reference changed'),
            ('missing output model', lambda s: s['components']['schemas'].pop(name + 'Output'), 'reference changed'),
            ('wrong IAM permission', lambda s: root(s)['x-simulator-ssm-document-operation-schemas']['AmazonSSM.ListDocumentVersions'].update(permission='unreviewed'), 'reference changed'),
            ('duplicate request reference', lambda s: request(s)['schema']['anyOf'].append({'$ref': '#/components/schemas/' + name + 'Input'}), 'union binding changed'),
            ('moved response reference', move_version, 'union order changed'),
            ('changed operation text', lambda s: root(s).update(description=root(s)['description'].replace('the preset does not create anything', 'the preset writes a parent')), 'description changed'),
            ('changed previous SSM operation', lambda s: request(s)['examples']['SSMListDocuments']['value'].update(MaxResults=49), 'Pre-existing reference paths changed'),
            ('unreviewed similar schema', lambda s: s['components']['schemas'].update(SSMUnreviewedVersion={'type': 'object'}), 'Pre-existing reference spec changed'),
        ):
            broken = copy.deepcopy(self.spec)
            change(broken)
            error = expected if expected.startswith('Pre-existing') else 'SSM ListDocumentVersions ' + expected
            with self.subTest(change=label), self.assertRaisesRegex(AssertionError, error):
                project_server_list(broken)

    def test_sqs_batch_projection_cannot_hide_contract_or_inherited_drift(self):
        def media(spec):
            return spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.0']
        def schema(spec):
            return spec['components']['schemas']['SQSDeleteMessageBatchRequest']
        for label, change in (
            ('missing batch example', lambda spec: media(spec)['examples'].pop('DeleteMessageBatch')),
            ('wrong target', lambda spec: media(spec)['examples']['DeleteMessageBatch'].update(summary='wrong target')),
            ('missing Id', lambda spec: schema(spec)['properties']['Entries']['items']['required'].remove('Id')),
            ('relaxed entry bound', lambda spec: schema(spec)['properties']['Entries'].update(maxItems=11)),
            ('relaxed fields', lambda spec: schema(spec).update(additionalProperties=True)),
            ('missing result partition', lambda spec: spec['components']['schemas']['SQSDeleteMessageBatchResult']['required'].remove('Failed')),
            ('wrong IAM action', lambda spec: spec['paths']['/']['post']['x-simulator-sqs-operation-schemas']['DeleteMessageBatch'].update(permission='sqs:DeleteMessageBatch')),
            ('missing request branch', lambda spec: media(spec)['schema']['anyOf'].pop()),
            ('duplicate request branch', lambda spec: media(spec)['schema']['anyOf'].append({'$ref': '#/components/schemas/SQSDeleteMessageBatchRequest'})),
            ('missing response branch', lambda spec: spec['paths']['/']['post']['responses']['200']['content']['application/x-amz-json-1.0']['schema']['anyOf'].pop()),
            ('changed existing SQS', lambda spec: media(spec)['examples']['SendMessage']['value'].update(MessageBody='changed')),
            ('changed DynamoDB', lambda spec: media(spec)['examples']['DynamoDBGetItem']['value'].update(TableName='changed')),
            ('changed other reference', lambda spec: spec['paths']['/{bucket}']['put'].update(summary='changed')),
            ('unreviewed component', lambda spec: spec['components']['schemas'].update(UnreviewedBatchExtension={'type': 'string'})),
        ):
            broken = copy.deepcopy(self.spec)
            change(broken)
            with self.subTest(change=label), self.assertRaises(AssertionError):
                preserved(broken)

    def test_secret_update_projection_rejects_missing_changed_and_extra_contracts(self):
        def request(spec):
            return spec['paths']['/']['post']['requestBody']['content'][JSON11]
        def response(spec):
            return spec['paths']['/']['post']['responses']['200']['content'][JSON11]
        def mapping(spec):
            return spec['paths']['/']['post']['x-simulator-secrets-operation-schemas']
        for label, change in (
            ('changed service description', lambda spec: next(t for t in spec['tags'] if t['name'] == 'Secrets Manager').update(description='changed')),
            ('missing request', lambda spec: request(spec)['examples'].pop('UpdateSecret')),
            ('wrong target', lambda spec: request(spec)['examples']['UpdateSecret'].update(summary='wrong')),
            ('changed request', lambda spec: request(spec)['examples']['UpdateSecret']['value'].update(Description='changed')),
            ('missing response', lambda spec: response(spec)['examples'].pop('UpdateSecret')),
            ('version response', lambda spec: response(spec)['examples']['UpdateSecret']['value'].update(VersionId='fake')),
            ('missing input', lambda spec: spec['components']['schemas'].pop('SecretsUpdateSecretInput')),
            ('relaxed input', lambda spec: spec['components']['schemas']['SecretsUpdateSecretInput'].update(additionalProperties=True)),
            ('missing output', lambda spec: spec['components']['schemas'].pop('SecretsUpdateSecretOutput')),
            ('relaxed output', lambda spec: spec['components']['schemas']['SecretsUpdateSecretOutput'].update(additionalProperties=True)),
            ('missing mapping', lambda spec: mapping(spec).pop('secretsmanager.UpdateSecret')),
            ('wrong permission', lambda spec: mapping(spec)['secretsmanager.UpdateSecret'].update(permission='allow all')),
            ('missing request union', lambda spec: request(spec)['schema']['anyOf'].pop()),
            ('missing response union', lambda spec: response(spec)['schema']['anyOf'].pop()),
            ('duplicate request union', lambda spec: request(spec)['schema']['anyOf'].append({'$ref': '#/components/schemas/SecretsUpdateSecretInput'})),
            ('duplicate response union', lambda spec: response(spec)['schema']['anyOf'].append({'$ref': '#/components/schemas/SecretsUpdateSecretOutput'})),
            ('changed inherited secret', lambda spec: request(spec)['examples']['DescribeSecret']['value'].update(SecretId='changed')),
            ('unreviewed schema', lambda spec: spec['components']['schemas'].update(UnreviewedSecretsExtension={'type': 'string'})),
        ):
            broken = copy.deepcopy(self.spec)
            change(broken)
            with self.subTest(change=label), self.assertRaises(AssertionError):
                preserved(broken)

    def test_log_stream_addition_cannot_hide_missing_or_changed_contracts(self):
        def media(spec):
            return spec['paths']['/']['post']['requestBody']['content'][JSON11]
        for label, change in (
            ('missing example', lambda spec: media(spec)['examples'].pop('LogsDeleteLogStream')),
            ('wrong target', lambda spec: media(spec)['examples']['LogsDeleteLogStream'].update(summary='wrong target')),
            ('changed static resource', lambda spec: media(spec)['examples']['LogsDeleteLogStream']['value'].update(logStreamName='different')),
            ('missing schema', lambda spec: spec['components']['schemas'].pop('LogsDeleteLogStreamRequest')),
            ('relaxed schema', lambda spec: spec['components']['schemas']['LogsDeleteLogStreamRequest'].update(additionalProperties=True)),
            ('missing required field', lambda spec: spec['components']['schemas']['LogsDeleteLogStreamRequest']['required'].remove('logStreamName')),
            ('missing union branch', lambda spec: media(spec)['schema']['anyOf'].pop()),
            ('duplicate union branch', lambda spec: media(spec)['schema']['anyOf'].append({'$ref':'#/components/schemas/LogsDeleteLogStreamRequest'})),
            ('changed inherited stream operation', lambda spec: media(spec)['examples']['LogsCreateLogStream']['value'].update(logStreamName='changed')),
            ('unreviewed component', lambda spec: spec['components']['schemas'].update(UnreviewedLogsExtension={'type':'string'})),
        ):
            broken=copy.deepcopy(self.spec)
            change(broken)
            with self.subTest(change=label),self.assertRaises(AssertionError):
                preserved(broken)

    def test_five_native_routes_scope_auth_and_nonform_query(self):
        native_shape(self.spec)

    def test_every_new_request_and_response_example_validates(self):
        count=0
        for _,_,op in operations(self.spec):
            if op['operationId'] not in NEW_IDS:continue
            containers=[op['requestBody']] if 'requestBody' in op else []
            containers+=list(op['responses'].values())
            for container in containers:
                for media in container.get('content',{}).values():
                    schema=media['schema']
                    check=validator(self.spec,schema)
                    for example in media.get('examples',{}).values():
                        check.validate(example['value']);count+=1
                    if 'example' in media:check.validate(media['example']);count+=1
        self.assertGreater(count,40)

    def test_create_update_neutral_and_empty_description_contract(self):
        check=validator(self.spec,{'$ref':'#/components/schemas/SakuraCreateDiskRequest'})
        base={'Name':'dummy','Plan':{'ID':4},'SizeMB':20480}
        for icon in (None,{}, {'ID':0}, {'ID':0,'Tags':[]}):
            check.validate({'Disk':{**base,'Icon':icon}})
        check.validate({'Disk':{**base,'Plan':{'ID':'4'}},'KMSKey':{},'DistantFrom':[]})
        update=validator(self.spec,{'$ref':'#/components/schemas/SakuraUpdateDiskRequest'})
        update.validate({'Disk':{}})
        update.validate({'Disk':{'Description':''}})
        for bad in ({'Description':None},{'Name':''},{'Plan':{'ID':4}},{'SizeMB':20480}):
            with self.subTest(bad=bad),self.assertRaises(ValidationError):update.validate({'Disk':bad})

    def test_unsupported_options_and_default_lookalikes_are_rejected(self):
        check=validator(self.spec,{'$ref':'#/components/schemas/SakuraCreateDiskRequest'})
        base={'Name':'dummy','Plan':{'ID':4},'SizeMB':20480}
        for extra in ({'Plan':{'ID':2}}, {'SizeMB':True}, {'SizeMB':1}, {'Description':None}, {'Availability':'available'}, {'Icon':{'ID':'0'}}, {'Icon':{'ID':1}}, {'Icon':{'ID':0,'Tags':None}}, {'Icon':{'ID':0,'Tags':['tag']}}, {'Icon':{'ID':0,'Name':''}}, {'Tags':['tag']}, {'name':'wrongcase'}):
            with self.subTest(extra=extra),self.assertRaises(ValidationError):check.validate({'Disk':{**base,**extra}})
        with self.assertRaises(ValidationError):check.validate({'Disk':base,'KMSKey':{'ID':0}})
        # Structural JSON Schema cannot distinguish lexical JSON4 from4.0 or4e0;
        # native-parser tests own that stricter constraint, explicitly documented.
        self.assertIn('lexical',self.spec['components']['schemas']['SakuraNeutralIcon']['description'])

    def test_no_readiness_or_private_fields_and_actual_success_envelopes(self):
        disk=self.spec['paths'][COLLECTION]['post']['responses']['201']['content']['application/json']['examples']['StaticResult']['value']['Disk']
        check=validator(self.spec,{'$ref':'#/components/schemas/SakuraDisk'})
        for field in ('Availability','Server','Storage','Generation','Owner'):
            with self.subTest(field=field),self.assertRaises(ValidationError):check.validate({**disk,field:'fake'})
        created=validator(self.spec,{'$ref':'#/components/schemas/SakuraCreateDiskResult'})
        with self.assertRaises(ValidationError):created.validate({'Disk':disk,'Success':True,'is_ok':True})
        removed=validator(self.spec,{'$ref':'#/components/schemas/SakuraDeleteDiskResult'})
        removed.validate({'Success':True,'is_ok':True})
        with self.assertRaises(ValidationError):removed.validate({'Success':'Created','is_ok':True})

    def test_negative_controls_detect_legacy_changes_fake_query_and_readiness(self):
        broken=copy.deepcopy(self.spec)
        media=broken['paths']['/']['post']['requestBody']['content'][JSON11]
        media['examples']['ListSecretVersionIds']['value']['UnexpectedField']=True
        with self.assertRaises(AssertionError):preserved(broken)
        broken=copy.deepcopy(self.spec)
        broken['paths'][COLLECTION]['get']['parameters'].append({'name':'From','in':'query','schema':{'type':'integer'}})
        with self.assertRaises(AssertionError):native_shape(broken)
        broken=copy.deepcopy(self.spec)
        broken['components']['schemas']['SakuraDisk']['properties']['Availability']={'const':'available'}
        with self.assertRaises(AssertionError):native_shape(broken)
        broken=copy.deepcopy(self.spec)
        broken['paths'][ITEM]['delete']['requestBody']={'content':{}}
        with self.assertRaises(AssertionError):native_shape(broken)

    def test_multipart_extension_cannot_hide_unreviewed_native_changes(self):
        for change in (
            lambda paths: paths['/{bucket}/{key}'].pop('post'),
            lambda paths: paths['/{bucket}/{key}']['post'].update(tags=['S3']),
            lambda paths: paths['/{bucket}']['get']['parameters'].append({'name':'unexpected','in':'query','schema':{'type':'string'}}),
            lambda paths: paths['/{bucket}/{key}']['get']['responses'].update({'200':{'description':'fake static success'}}),
            lambda paths: next(p for p in paths['/{bucket}/{key}']['post']['parameters'] if p.get('name')=='X-Simulator-Region')['schema'].update(default='eu-west-1'),
        ):
            broken=copy.deepcopy(self.spec)
            change(broken['paths'])
            with self.assertRaises(AssertionError):preserved(broken)

    def test_profile_extension_cannot_hide_unreviewed_reference_changes(self):
        form='application/x-www-form-urlencoded'
        for path in ('/', '/_simulator/providers/aws/'):
            for operation in ('CreateInstanceProfile', 'GetInstanceProfile',
                              'ListInstanceProfiles', 'DeleteInstanceProfile',
                              'AddRoleToInstanceProfile', 'RemoveRoleFromInstanceProfile',
                              'ListInstanceProfilesForRole'):
                broken=copy.deepcopy(self.spec)
                examples=broken['paths'][path]['post']['requestBody']['content'][form]['examples']
                examples.pop(operation)
                with self.subTest(path=path,operation=operation),self.assertRaises(AssertionError):
                    preserved(broken)
        for change in (
            lambda paths: paths['/']['post']['requestBody']['content'][form]['schema']['properties']['Action']['enum'].remove('CreateInstanceProfile'),
            lambda paths: paths['/']['post']['requestBody']['content'][form]['schema']['properties']['InstanceProfileName'].update(maxLength=129),
            lambda paths: paths['/_simulator/providers/aws/']['post']['requestBody']['content'][form]['examples']['AddRoleToInstanceProfile']['value'].update(RoleName='unreviewed-role'),
            lambda paths: paths['/_simulator/providers/aws/']['post']['requestBody']['content'][form]['examples'].pop('CreateRole'),
        ):
            broken=copy.deepcopy(self.spec)
            change(broken['paths'])
            with self.assertRaises(AssertionError):preserved(broken)

    def test_builtin_extension_cannot_hide_other_contract_changes(self):
        form='application/x-www-form-urlencoded'
        for change in (
            lambda spec: spec['paths']['/2015-03-31/functions/{FunctionName}/invocations']['post']['responses'].update({'299': {'description': 'invented'}}),
            lambda spec: spec['paths']['/_simulator/providers/aws/']['post']['requestBody']['content'][form]['schema']['oneOf'][10]['properties']['PolicyArn'].update(pattern='.*'),
            lambda spec: spec['paths']['/_simulator/providers/aws/']['post']['requestBody']['content'][form]['schema']['oneOf'][11]['properties']['Scope'].update(enum=['Local','AWS','All']),
            lambda spec: spec['components']['schemas'].update(UnreviewedBuiltin={'type':'string'}),
            lambda spec: spec['paths']['/']['post']['responses'].update({'299': {'description':'invented'}}),
        ):
            broken=copy.deepcopy(self.spec)
            change(broken)
            with self.assertRaises(AssertionError):preserved(broken)


    def test_log_tail_extension_cannot_hide_unreviewed_reference_changes(self):
        path='/2015-03-31/functions/{FunctionName}/invocations'
        def log_type(op):
            return next(p for p in op['parameters'] if p.get('name')=='X-Amz-Log-Type')
        for label,change in (
            ('missing opt-in header', lambda op: op['parameters'].remove(log_type(op))),
            ('default disclosure', lambda op: log_type(op)['schema'].update(default='Tail')),
            ('lost None option', lambda op: log_type(op)['schema']['enum'].remove('None')),
            ('missing result header', lambda op: op['responses']['200']['headers'].pop('X-Amz-Log-Result')),
            ('unbounded output claim', lambda op: op.update(description=op['description'].replace('4096','8192'))),
            ('wrong result encoding', lambda op: op['responses']['200']['headers']['X-Amz-Log-Result']['schema'].update(type='object')),
            ('lost host authentication', lambda op: op['parameters'].remove({'$ref':'#/components/parameters/SimulatorToken'})),
            ('changed native operation', lambda op: op.update(operationId='UnreviewedInvoke')),
            ('changed response contract', lambda op: op['responses']['200'].update(description='Unreviewed response')),
        ):
            broken=copy.deepcopy(self.spec)
            change(broken['paths'][path]['post'])
            with self.subTest(change=label),self.assertRaises(AssertionError):
                preserved(broken)


if __name__=='__main__':unittest.main()
