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


def preserved(spec):
    # Exact main d166f56 projections plus reviewed bounded S3 multipart queries,
    # descriptions, regional/auth headers and object POST, then the seven native
    # IAM profile examples/action fields at root and the AWS alias. The reviewed
    # profile addition changes only those IAM paths. The bounded Lambda Tail
    # extension changes only Invoke description/request/response headers; all
    # other paths and components stay exact.
    # The independent paths digest excludes only the two existing Sakura paths.
    original_paths = {path:value for path,value in spec['paths'].items() if path not in (COLLECTION,ITEM)}
    assert len(original_paths)==34
    assert digest(original_paths)=='91b11dc23d3a4bccd118b4c2ba2a67f8198e1db383f5367bbedb4c1a04b612dd', 'Existing paths/operations/response examples changed'
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

    def test_all_existing_operations_components_and_28_templates_are_exact(self):
        preserved(self.spec)

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
