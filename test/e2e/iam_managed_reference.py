"""Execute the twelve SERVED customer managed IAM Query examples unchanged.

The static examples are requests, never live state. Actual native XML and
independent official boto3 reads share the owned CLI's SQLite database. Fixture
SDK actions and negative requests are reported separately from template sends.
Run against an exact integrated embedded binary; no real AWS or optional skip.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
import urllib.error
import urllib.request
from urllib.parse import parse_qs, quote, unquote, urlencode, urlsplit
import xml.etree.ElementTree as ET

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from check import running, REPORTS

NAMESPACE = 'https://iam.amazonaws.com/doc/2010-05-08/'
PATH = '/_simulator/providers/aws/'
OPERATIONS = {'CreatePolicy', 'GetPolicy', 'ListPolicies', 'DeletePolicy',
              'CreatePolicyVersion', 'GetPolicyVersion', 'ListPolicyVersions',
              'SetDefaultPolicyVersion', 'DeletePolicyVersion', 'AttachRolePolicy',
              'DetachRolePolicy', 'ListAttachedRolePolicies'}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, newurl):
        raise AssertionError('Native reference request attempted redirect')


def form_value(value):
    # JSON booleans must match Scalar/JavaScript String(true), not Python True.
    if isinstance(value, bool):
        return 'true' if value else 'false'
    assert isinstance(value, (str, int)), 'Unsupported static form value type'
    return str(value)


def validate_example(schema, value):
    assert schema['type'] == 'object' and schema['additionalProperties'] is False
    properties = schema['properties']
    assert set(schema['required']) <= set(value) <= set(properties)
    for key, item in value.items():
        field = properties[key]
        kind = field['type']
        assert ((kind == 'string' and isinstance(item, str)) or
                (kind == 'boolean' and isinstance(item, bool)) or
                (kind == 'integer' and type(item) is int)), 'Template schema type mismatch'
        if 'const' in field:
            assert item == field['const']
        if 'enum' in field:
            assert item in field['enum']
        if isinstance(item, str):
            assert len(item) >= field.get('minLength', 0)
            assert len(item) <= field.get('maxLength', len(item))
            if 'pattern' in field:
                assert re.fullmatch(field['pattern'], item) is not None
        if type(item) is int:
            assert field.get('minimum', item) <= item <= field.get('maximum', item)


def run():
    http_counts = {'calls': 0, 'responses': 0}
    sdk_counts = []
    executed, failures, fixtures = [], [], []
    with tempfile.TemporaryDirectory(prefix='iam-managed-reference-') as directory:
        with running(db=Path(directory) / 'state.sqlite', authenticated=True) as (endpoint, token):
            origin = urlsplit(endpoint)
            assert origin.scheme == 'http' and origin.hostname == '127.0.0.1' and origin.port
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
            clients = []

            def wire(path, value=None, status=200, principal=None):
                target = urlsplit(endpoint + path)
                assert path.startswith('/') and not path.startswith('//')
                assert target.scheme == origin.scheme and target.netloc == origin.netloc
                headers = {'X-Simulator-Token': token}
                body = None
                if value is not None:
                    fields = {key: form_value(item) for key, item in value.items()}
                    body = urlencode(fields).encode('ascii')
                    # Exactly one form decode must preserve every original JSON
                    # string, including any literal plus or quoted whitespace.
                    assert parse_qs(body.decode('ascii'), keep_blank_values=True) == {
                        key: [item] for key, item in fields.items()}
                    headers.update({'Content-Type': 'application/x-www-form-urlencoded',
                                    'X-Simulator-Service': 'iam'})
                if principal:
                    headers['X-Simulator-Principal'] = principal
                request = urllib.request.Request(endpoint + path, data=body, headers=headers,
                                                 method='POST' if body is not None else 'GET')
                http_counts['calls'] += 1
                try:
                    response = opener.open(request, timeout=5)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    http_counts['responses'] += 1
                    assert response.status == status, 'Unexpected native reference status'
                    data = response.read(1024 * 1024 + 1)
                    assert len(data) <= 1024 * 1024, 'Native reference response exceeded bound'
                    if value is None:
                        return json.loads(data)
                    assert response.headers['Content-Type'].split(';', 1)[0] == ('text/xml' if status == 200 else 'application/xml')
                    assert b'<!DOCTYPE' not in data.upper() and b'<!ENTITY' not in data.upper()
                    root = ET.fromstring(data)
                    assert root.tag == '{' + NAMESPACE + '}' + (
                        value['Action'] + 'Response' if status == 200 else 'ErrorResponse')
                    assert root.find('{'+NAMESPACE+'}ResponseMetadata') is not None or status != 200
                    return root

            def sdk(service, principal=None):
                counts = {'calls': 0, 'sends': 0, 'responses': 0}
                client = boto3.client(service, endpoint_url=endpoint, region_name='us-east-1',
                    aws_access_key_id='TCSIMREFERENCEDUMMY', aws_secret_access_key='dummy-local-only',
                    config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2,
                        read_timeout=5, s3={'addressing_style': 'path'},
                        request_checksum_calculation='when_required',
                        response_checksum_validation='when_required'))
                def signing(request, **_):
                    request.headers.add_header('X-Simulator-Token', token)
                    if principal:
                        request.headers.add_header('X-Simulator-Principal', principal)
                def called(**_):
                    counts['calls'] += 1
                def sent(request, **_):
                    parsed = urlsplit(request.url)
                    assert parsed.scheme == origin.scheme and parsed.netloc == origin.netloc
                    counts['sends'] += 1
                def received(http_response, **_):
                    counts['responses'] += 1
                    assert not 300 <= http_response.status_code < 400
                client.meta.events.register('before-sign.'+service, signing)
                client.meta.events.register('before-call.'+service, called)
                client.meta.events.register('before-send.'+service, sent)
                client.meta.events.register('after-call.'+service, received)
                clients.append(client); sdk_counts.append(counts)
                return client

            def result(root, action):
                node = root.find('{'+NAMESPACE+'}'+action+'Result')
                assert node is not None, 'Missing native action result'
                return node

            def text(node, name):
                value = node.find('{'+NAMESPACE+'}'+name)
                assert value is not None and value.text is not None
                return value.text

            def refused_object(client, bucket, key):
                try:
                    response = client.get_object(Bucket=bucket, Key=key)
                except ClientError as error:
                    assert error.response['ResponseMetadata']['HTTPStatusCode'] == 403
                else:
                    response['Body'].close()
                    raise AssertionError('Role unexpectedly read fixture object')

            def object_bytes(client, bucket, key):
                response = client.get_object(Bucket=bucket, Key=key)
                with response['Body'] as stream:
                    return stream.read()

            try:
                spec = wire('/_simulator/docs/openapi.json')
                assert spec == json.loads(Path('internal/server/apidocs/openapi.json').read_text()), 'Served spec differs from exact source'
                operation = spec['paths'][PATH]['post']
                assert operation['operationId'] == 'IAMRoleQueryOperation'
                content = operation['requestBody']['content']['application/x-www-form-urlencoded']
                branches = content['schema']['oneOf']
                schemas = {entry['title']: entry for entry in branches}
                assert len(schemas) == len(branches) == 28 and OPERATIONS <= set(schemas)
                examples = {name: entry['value'] for name, entry in content['examples'].items()}
                for name in OPERATIONS | {'CreateRole', 'DeleteRole'}:
                    validate_example(schemas[name], examples[name])
                    assert examples[name]['Action'] == name and examples[name]['Version'] == '2010-05-08'
                assert content['schema']['discriminator']['propertyName'] == 'Action'
                assert type(examples['CreatePolicyVersion']['SetAsDefault']) is bool
                assert examples['CreatePolicyVersion']['SetAsDefault'] is True
                assert form_value(True) == 'true'
                assert examples['ListPolicies']['Scope'] == 'Local'
                role = examples['CreateRole']['RoleName']
                arn = examples['GetPolicy']['PolicyArn']
                policy_name = examples['CreatePolicy']['PolicyName']
                original = examples['CreatePolicy']['PolicyDocument']
                document = json.loads(original)
                version_document = examples['CreatePolicyVersion']['PolicyDocument']
                version_json = json.loads(version_document)
                resource = document['Statement'][0]['Resource']
                prefix = 'arn:aws:s3:::'
                assert resource.startswith(prefix) and resource.endswith('/*')
                bucket, key, payload = resource[len(prefix):-2], 'reference-object', b'dummy-reference-body'
                assert arn == 'arn:aws:iam::000000000000:policy/'+policy_name
                for name in OPERATIONS:
                    value = examples[name]
                    assert value.get('PolicyArn', arn) == arn
                    assert value.get('RoleName', role) == role
                iam, objects, as_role = sdk('iam'), sdk('s3'), sdk('s3', role)
                assert iam.list_policies(Scope='Local')['Policies'] == []
                assert iam.list_roles()['Roles'] == []
                wire(PATH, examples['CreateRole']); fixtures.append('served CreateRole setup')
                objects.create_bucket(Bucket=bucket)
                objects.put_object(Bucket=bucket, Key=key, Body=payload)
                refused_object(as_role, bucket, key)

                def template(name):
                    root = wire(PATH, examples[name]); executed.append(name)
                    return root

                created = result(template('CreatePolicy'), 'CreatePolicy').find('{'+NAMESPACE+'}Policy')
                assert created is not None and text(created, 'Arn') == arn
                assert text(created, 'DefaultVersionId') == 'v1'
                policy = iam.get_policy(PolicyArn=arn)['Policy']
                assert policy['Arn'] == arn and policy['AttachmentCount'] == 0
                assert iam.get_policy_version(PolicyArn=arn, VersionId='v1')['PolicyVersion']['Document'] == document
                got = result(template('GetPolicy'), 'GetPolicy').find('{'+NAMESPACE+'}Policy')
                assert text(got, 'PolicyId') == policy['PolicyId']
                listed = result(template('ListPolicies'), 'ListPolicies').find('{'+NAMESPACE+'}Policies')
                assert len(listed) == 1 and text(listed[0], 'Arn') == arn
                assert listed[0].find('{'+NAMESPACE+'}Description') is None
                template('AttachRolePolicy')
                assert iam.list_attached_role_policies(RoleName=role)['AttachedPolicies'] == [{'PolicyName': policy_name, 'PolicyArn': arn}]
                assert object_bytes(as_role, bucket, key) == payload
                attached = result(template('ListAttachedRolePolicies'), 'ListAttachedRolePolicies').find('{'+NAMESPACE+'}AttachedPolicies')
                assert len(attached) == 1 and text(attached[0], 'PolicyArn') == arn
                # Independent fixture verifies explicit Deny still wins over the
                # real template's attached Allow; remove it before continuing.
                deny = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Deny', 'Action': 's3:GetObject', 'Resource': resource}]}
                iam.put_role_policy(RoleName=role, PolicyName='reference-deny', PolicyDocument=json.dumps(deny))
                refused_object(as_role, bucket, key)
                iam.delete_role_policy(RoleName=role, PolicyName='reference-deny')
                assert object_bytes(as_role, bucket, key) == payload
                fixtures.append('SDK inline Deny union and removal')
                version = result(template('CreatePolicyVersion'), 'CreatePolicyVersion').find('{'+NAMESPACE+'}PolicyVersion')
                assert text(version, 'VersionId') == 'v2' and text(version, 'IsDefaultVersion') == 'true'
                assert version.find('{'+NAMESPACE+'}Document') is None
                assert iam.get_policy(PolicyArn=arn)['Policy']['DefaultVersionId'] == 'v2'
                version = result(template('GetPolicyVersion'), 'GetPolicyVersion').find('{'+NAMESPACE+'}PolicyVersion')
                encoded = text(version, 'Document')
                assert encoded == quote(version_document, safe='-_.~')
                assert unquote(encoded) == version_document and json.loads(unquote(encoded)) == version_json
                assert iam.get_policy_version(PolicyArn=arn, VersionId='v2')['PolicyVersion']['Document'] == version_json
                listed = result(template('ListPolicyVersions'), 'ListPolicyVersions').find('{'+NAMESPACE+'}Versions')
                assert {text(item, 'VersionId') for item in listed} == {'v1', 'v2'}
                assert all(item.find('{'+NAMESPACE+'}Document') is None for item in listed)
                # The served SetDefault template targets v2; deliberately return
                # to v1 first so sending the unchanged example has a real effect.
                iam.set_default_policy_version(PolicyArn=arn, VersionId='v1')
                fixtures.append('SDK reset v1 before served SetDefault v2')
                template('SetDefaultPolicyVersion')
                assert iam.get_policy(PolicyArn=arn)['Policy']['DefaultVersionId'] == 'v2'
                before = iam.list_policy_versions(PolicyArn=arn)['Versions']
                for name, bad, status, code, principal in [
                    ('CreatePolicyVersion', {**examples['CreatePolicyVersion'], 'SetAsDefault': 'True'}, 400, 'InvalidInput', None),
                    ('GetPolicy', examples['GetPolicy'], 403, 'AccessDenied', role),
                    ('DeletePolicyVersion', examples['DeletePolicyVersion'], 409, 'DeleteConflict', None),
                    ('DeletePolicy', examples['DeletePolicy'], 409, 'DeleteConflict', None),
                    ('ListPolicies', {**examples['ListPolicies'], 'Scope': 'All'}, 501, 'NotImplemented', None),
                ]:
                    error = wire(PATH, bad, status, principal).find('{'+NAMESPACE+'}Error')
                    assert error is not None and text(error, 'Code') == code
                    assert iam.list_policy_versions(PolicyArn=arn)['Versions'] == before
                    assert iam.list_attached_role_policies(RoleName=role)['AttachedPolicies'][0]['PolicyArn'] == arn
                    assert object_bytes(as_role, bucket, key) == payload
                    failures.append({'operation': name, 'status': status, 'code': code})
                iam.set_default_policy_version(PolicyArn=arn, VersionId='v1')
                fixtures.append('SDK reset v1 before served DeletePolicyVersion v2')
                template('DeletePolicyVersion')
                assert [v['VersionId'] for v in iam.list_policy_versions(PolicyArn=arn)['Versions']] == ['v1']
                template('DetachRolePolicy')
                assert iam.list_attached_role_policies(RoleName=role)['AttachedPolicies'] == []
                refused_object(as_role, bucket, key)
                template('DeletePolicy')
                assert iam.list_policies(Scope='Local')['Policies'] == []
                wire(PATH, examples['DeleteRole']); fixtures.append('served DeleteRole cleanup')
                assert iam.list_roles()['Roles'] == []
                objects.delete_object(Bucket=bucket, Key=key); objects.delete_bucket(Bucket=bucket)
                assert objects.list_buckets()['Buckets'] == []
                assert set(executed) == OPERATIONS and len(executed) == 12
                assert http_counts['calls'] == http_counts['responses']
                assert all(c['calls'] > 0 and c['calls'] == c['sends'] == c['responses'] for c in sdk_counts)
                REPORTS.mkdir(parents=True, exist_ok=True)
                report = {'servedSpecEqualsSource': True, 'servedIAMBranches': 28,
                          'unchangedStaticExamplesExecuted': executed, 'staticExamplesAreNotLiveState': True,
                          'lowercaseFormBooleans': True, 'singleFormDecodePreservesValues': True,
                          'GetVersionDocumentEncodedExactlyOnce': True, 'GetOnlyVersionDocument': True,
                          'sameSQLiteOfficialBoto3StateAndS3Authority': True,
                          'negativeRequests': failures, 'nonTemplateFixtureActions': fixtures,
                          'HTTPCounts': http_counts, 'SDKCounts': sdk_counts,
                          'cleanup': 'DELETED', 'actualCloud': False, 'browserTested': False}
                (REPORTS/'iam-managed-reference.json').write_text(json.dumps(report, indent=2)+'\n')
                print(json.dumps(report))
            finally:
                for client in clients:
                    client.close()


if __name__ == '__main__':
    run()
