"""Execute the four served KMS alias examples using the real SDK/root routes.
Templates are static documentation; SDK operation methods select native headers.
No placeholder example is mistaken for a current key or saved alias.
"""
import copy
import ipaddress
import json
from pathlib import Path
import sys
import urllib.request
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parent))
from check import running, boto3, Config

EXPECTED = {'CreateKey', 'DescribeKey', 'ListKeys', 'EnableKey', 'DisableKey', 'GetKeyPolicy', 'Encrypt', 'Decrypt', 'CreateAlias', 'ListAliases', 'UpdateAlias', 'DeleteAlias'}
ALIASES = {'CreateAlias', 'ListAliases', 'UpdateAlias', 'DeleteAlias'}
SCHEMAS = {'CreateAlias': 'KMSCreateAliasInput', 'ListAliases': 'KMSListAliasesInput', 'UpdateAlias': 'KMSUpdateAliasInput', 'DeleteAlias': 'KMSDeleteAliasInput'}
COUNTERS, CLIENTS, RAW_LISTS = [], [], []


def guard(url, endpoint):
    origin, target = urlsplit(endpoint), urlsplit(url)
    assert origin.scheme == 'http' and origin.port and ipaddress.ip_address(origin.hostname).is_loopback
    assert target.scheme == 'http' and target.netloc == origin.netloc and not target.username and not target.password and not target.fragment
    assert ipaddress.ip_address(target.hostname).is_loopback


def fetch(endpoint, path, token):
    request = urllib.request.Request(endpoint + path, headers={'X-Simulator-Token': token})
    guard(request.full_url, endpoint)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request) as response:
        assert response.status == 200
        return json.load(response)


def client(endpoint, token, route):
    target = endpoint + route
    guard(target, endpoint)
    count = {'calls': 0, 'sends': 0, 'responses': 0, 'targets': []}
    COUNTERS.append(count)
    c = boto3.client('kms', endpoint_url=target, region_name='us-east-1', aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy', config=Config(retries={'max_attempts': 0}, proxies={}))
    c.meta.events.register('before-sign.kms', lambda request, **kw: request.headers.add_header('X-Simulator-Token', token))
    def called(**kw): count['calls'] += 1
    def send(request, **kw):
        guard(request.url, endpoint)
        assert urlsplit(request.url).path == route
        count['sends'] += 1
        value = request.headers.get('X-Amz-Target')
        count['targets'].append(value.decode() if isinstance(value, bytes) else value)
    def before_parse(operation_model, response_dict, **kw):
        # Standard read-only observation: native empty bodies/numeric timestamps
        # are checked before the SDK converts timestamps to datetime objects.
        if response_dict['status_code'] == 200:
            if operation_model.name in {'CreateAlias', 'UpdateAlias', 'DeleteAlias'}:
                assert response_dict['body'] == b''
            elif operation_model.name == 'ListAliases':
                raw = json.loads(response_dict['body'])
                assert set(raw) <= {'Aliases', 'Truncated', 'NextMarker'} and type(raw['Truncated']) is bool
                for row in raw['Aliases']:
                    assert set(row) == {'AliasArn', 'AliasName', 'CreationDate', 'LastUpdatedDate', 'TargetKeyId'}
                    assert type(row['CreationDate']) in {int, float} and row['CreationDate'] > 0
                    assert type(row['LastUpdatedDate']) in {int, float} and row['LastUpdatedDate'] >= row['CreationDate']
                RAW_LISTS.append(raw)
    def response(**kw): count['responses'] += 1
    c.meta.events.register('before-call.kms', called)
    c.meta.events.register('before-send.kms', send)
    c.meta.events.register('before-parse.kms', before_parse)
    c.meta.events.register('after-call.kms', response)
    CLIENTS.append(c)
    return c


def main():
    with running(authenticated=True) as (endpoint, operator):
        spec = fetch(endpoint, '/_simulator/docs/openapi.json', operator)
        caps = fetch(endpoint, '/_simulator/v1/capabilities', operator)['services']['kms']
        assert len(caps['operations']) == 12 and len(set(caps['operations'])) == 12 and set(caps['operations']) == EXPECTED
        assert caps['aliasLimit'] == 1000 and caps['aliasesPerKey'] == 50 and caps['aliasNameCharacters'] == 256
        assert caps['aliasListDefault'] == 50 and caps['aliasListMaximum'] == 100 and caps['aliasCryptoIdentifiers'] == ['Encrypt', 'Decrypt']
        assert {'AWS-managed/predefined aliases', 'alias-based policy conditions', 'grants'} <= set(caps['unsupported'])
        op = spec['paths']['/']['post']
        templates = op['requestBody']['content']['application/x-amz-json-1.1']['examples']
        mapping = op['x-simulator-kms-operation-schemas']
        assert set(mapping) == EXPECTED and {name[3:] for name in templates if name.startswith('KMS')} == EXPECTED
        for name in ALIASES:
            assert mapping[name]['target'] == 'TrentService.' + name
            assert mapping[name]['request'] == {'$ref': '#/components/schemas/' + SCHEMAS[name]}
            assert templates['KMS' + name]['summary'] == 'X-Amz-Target: TrentService.' + name
        schema = spec['components']['schemas']
        assert schema['KMSListAliasesInput']['properties']['Limit'] == {'type': 'integer', 'minimum': 1, 'maximum': 100, 'default': 50}
        assert schema['KMSCreateAliasInput']['required'] == ['AliasName', 'TargetKeyId']
        assert schema['KMSUpdateAliasInput']['required'] == ['AliasName', 'TargetKeyId']
        assert schema['KMSDeleteAliasInput']['required'] == ['AliasName']
        assert all(schema[name]['additionalProperties'] is False for name in SCHEMAS.values())
        observer = client(endpoint, operator, '/')
        for route in ['/', '/_simulator/providers/aws/']:
            c = client(endpoint, operator, route)
            first, second = c.create_key()['KeyMetadata'], c.create_key()['KeyMetadata']
            def template(name): return copy.deepcopy(templates['KMS' + name]['value'])
            create = template('CreateAlias'); create['TargetKeyId'] = first['KeyId']
            assert set(create) == {'AliasName', 'TargetKeyId'}
            c.create_alias(**create)
            rows = observer.list_aliases(KeyId=first['Arn'])['Aliases']
            assert len(rows) == 1 and rows[0]['AliasName'] == create['AliasName'] and rows[0]['TargetKeyId'] == first['KeyId']
            assert rows[0]['AliasArn'] == 'arn:aws:kms:us-east-1:000000000000:' + create['AliasName']
            listed = c.list_aliases(**template('ListAliases'))
            assert listed['Aliases'] == rows and not listed['Truncated']
            update = template('UpdateAlias'); update['TargetKeyId'] = second['Arn']
            assert update['AliasName'] == create['AliasName']
            c.update_alias(**update)
            after = observer.list_aliases(KeyId=second['KeyId'])['Aliases']
            assert len(after) == 1 and after[0]['AliasName'] == create['AliasName'] and after[0]['TargetKeyId'] == second['KeyId']
            assert after[0]['CreationDate'] == rows[0]['CreationDate'] and after[0]['LastUpdatedDate'] >= rows[0]['LastUpdatedDate']
            # Same served alias and same SQLite are usable by real native crypto.
            blob = c.encrypt(KeyId=create['AliasName'], Plaintext=b'dummy served alias')['CiphertextBlob']
            assert observer.decrypt(CiphertextBlob=blob)['Plaintext'] == b'dummy served alias'
            delete = template('DeleteAlias'); assert delete['AliasName'] == create['AliasName']
            c.delete_alias(**delete)
            assert observer.list_aliases()['Aliases'] == []
            assert observer.describe_key(KeyId=first['KeyId'])['KeyMetadata']['KeyId'] == first['KeyId']
            assert observer.describe_key(KeyId=second['KeyId'])['KeyMetadata']['KeyId'] == second['KeyId']
        for count in COUNTERS:
            assert count['calls'] > 0 and count['calls'] == count['sends'] == count['responses']
        assert len(RAW_LISTS) >= 8
        assert {target for c in COUNTERS for target in c['targets']} >= {'TrentService.' + name for name in ALIASES}
        print(json.dumps({'servedKMSOperationSet': 12, 'actualAliasExamples': 4, 'routes': ['/', '/_simulator/providers/aws/'], 'sameSQLiteNativeCrypto': True, 'emptyBodiesAndNumericTimestamps': True, 'guardedSDKCounts': {key: sum(c[key] for c in COUNTERS) for key in ['calls', 'sends', 'responses']}, 'staticExamplesAreNotSavedState': True}))


if __name__ == '__main__':
    try:
        main()
    finally:
        for c in CLIENTS: c.close()
