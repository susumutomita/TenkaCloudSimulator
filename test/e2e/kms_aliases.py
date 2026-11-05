"""Actual normal CLI customer-alias SDK/crypto/IAM/two-team/region/restart acceptance; no AWS calls."""
import ipaddress
import json
from pathlib import Path
import sys
import tempfile
from urllib.parse import urlsplit
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parent))
from check import running, boto3, Config, ClientError
from botocore.parsers import ResponseParserError

stats = []
clients = []
def guard(url, endpoint):
    expected, actual = urlsplit(endpoint), urlsplit(url)
    assert expected.scheme == 'http' and expected.port and ipaddress.ip_address(expected.hostname).is_loopback
    assert actual.scheme == 'http' and actual.netloc == expected.netloc and not actual.username and not actual.password and not actual.fragment
    assert ipaddress.ip_address(actual.hostname).is_loopback

def client(service, endpoint, token, region='us-east-1'):
    guard(endpoint, endpoint)
    item = {'calls': 0, 'sends': 0, 'responses': 0, 'parsed': 0, 'last_status': None}
    stats.append(item)
    c = boto3.client(service, endpoint_url=endpoint, region_name=region,
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
        config=Config(retries={'max_attempts': 0}, proxies={}))
    c.meta.events.register('before-sign.' + service, lambda request, **kw: request.headers.add_header('X-Simulator-Token', token))
    def called(**kw):
        item['calls'] += 1
        item['last_status'] = None
    def sent(request, **kw):
        guard(request.url, endpoint)
        item['sends'] += 1
    def response(response_dict, **kw):
        # Read-only standard botocore observation, BEFORE any error parser.
        # Never replace body/headers/customized_response_dict or grant success.
        item['responses'] += 1
        item['last_status'] = response_dict['status_code']
    def parsed(**kw):
        item['parsed'] += 1
    c.meta.events.register('before-call.' + service, called)
    c.meta.events.register('before-send.' + service, sent)
    c.meta.events.register('before-parse.' + service, response)
    c.meta.events.register('after-call.' + service, parsed)
    clients.append(c)
    return c, item

def error(code, invoke):
    try:
        invoke()
    except ClientError as e:
        assert e.response['Error']['Code'] == code
    else:
        raise AssertionError('Expected ' + code)

def participant(endpoint, operator, name):
    request = urllib.request.Request(endpoint + '/_simulator/v1/access',
        data=json.dumps({'Principal': name}).encode(),
        headers={'Content-Type': 'application/json', 'X-Simulator-Token': operator})
    guard(request.full_url, endpoint)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request) as response:
        return json.load(response)['AccessToken']

def aliases(c, **kwargs):
    result = c.list_aliases(**kwargs)
    assert result['ResponseMetadata']['HTTPStatusCode'] == 200
    return result

def policy(iam, resources, deny=None):
    statements = [{'Effect': 'Allow', 'Action': ['kms:CreateAlias', 'kms:UpdateAlias', 'kms:DeleteAlias', 'kms:Encrypt', 'kms:Decrypt'], 'Resource': resources}]
    if deny:
        statements.append({'Effect': 'Deny', 'Action': 'kms:*', 'Resource': deny})
    iam.put_role_policy(RoleName='alias-learner', PolicyName='AliasUse',
        PolicyDocument=json.dumps({'Version': '2012-10-17', 'Statement': statements}))

def main():
    with tempfile.TemporaryDirectory(prefix='kms-alias-sdk-') as directory:
        path = Path(directory) / 'team-a.db'
        with running(path, authenticated=True) as (endpoint, operator), running(authenticated=True) as (peer_url, peer_operator):
            a, _ = client('kms', endpoint, operator)
            b, _ = client('kms', peer_url, peer_operator)
            west, _ = client('kms', endpoint, operator, 'us-west-2')
            first = a.create_key(Description='dummy alias first')['KeyMetadata']
            second = a.create_key(Description='dummy alias second')['KeyMetadata']
            peer = b.create_key()['KeyMetadata']
            west_key = west.create_key()['KeyMetadata']
            name = 'alias/Acceptance'
            arn = 'arn:aws:kms:us-east-1:000000000000:alias/Acceptance'
            assert a.create_alias(AliasName=name, TargetKeyId=first['Arn'])['ResponseMetadata']['HTTPStatusCode'] == 200
            b.create_alias(AliasName=name, TargetKeyId=peer['KeyId'])
            west.create_alias(AliasName=name, TargetKeyId=west_key['KeyId'])
            row = aliases(a)['Aliases'][0]
            assert row['AliasName'] == name and row['AliasArn'] == arn and row['TargetKeyId'] == first['KeyId']
            assert row['CreationDate'] <= row['LastUpdatedDate']
            assert aliases(b)['Aliases'][0]['TargetKeyId'] == peer['KeyId']
            assert aliases(west)['Aliases'][0]['TargetKeyId'] == west_key['KeyId']
            plaintext, context = b'\x00\xffdummy alias data', {'purpose': 'local-test'}
            blob = a.encrypt(KeyId=name, Plaintext=plaintext, EncryptionContext=context)['CiphertextBlob']
            assert a.decrypt(KeyId=arn, CiphertextBlob=blob, EncryptionContext=context)['Plaintext'] == plaintext
            a.create_alias(AliasName='alias/Second', TargetKeyId=first['KeyId'])
            page = aliases(a, Limit=1)
            assert page['Truncated'] and len(page['Aliases']) == 1
            marker = page['NextMarker']
            next_page = aliases(a, Limit=1, Marker=marker)
            assert next_page['Aliases'][0]['AliasName'] != page['Aliases'][0]['AliasName']
            error('InvalidMarkerException', lambda: west.list_aliases(Limit=1, Marker=marker))
            error('InvalidMarkerException', lambda: b.list_aliases(Limit=1, Marker=marker))
            error('InvalidMarkerException', lambda: a.list_aliases(Limit=2, Marker=marker))
            error('AlreadyExistsException', lambda: a.create_alias(AliasName=name, TargetKeyId=first['KeyId']))
            error('InvalidAliasNameException', lambda: a.create_alias(AliasName='alias/aws/s3', TargetKeyId=first['KeyId']))
            error('ValidationException', lambda: a.create_alias(AliasName='alias/Cross', TargetKeyId=west_key['Arn']))
            iam, _ = client('iam', endpoint, operator)
            iam.create_role(RoleName='alias-learner', AssumeRolePolicyDocument=json.dumps({'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': 'sts:AssumeRole', 'Principal': {'Service': 'lambda.amazonaws.com'}}]}))
            token = participant(endpoint, operator, 'alias-learner')
            learner, _ = client('kms', endpoint, token)
            # Each native mutation needs exact alias and key resources. An alias
            # metadata grant is not a grant to use either key cryptographically.
            resources = [arn, first['Arn'], second['Arn']]
            for omitted in resources:
                policy(iam, [r for r in resources if r != omitted])
                error('AccessDeniedException', lambda: learner.update_alias(AliasName=name, TargetKeyId=second['KeyId']))
                assert aliases(a, KeyId=first['KeyId'])['Aliases'][0]['TargetKeyId'] == first['KeyId']
            policy(iam, resources)
            learner.update_alias(AliasName=name, TargetKeyId=second['KeyId'])
            error('IncorrectKeyException', lambda: a.decrypt(KeyId=name, CiphertextBlob=blob, EncryptionContext=context))
            assert a.decrypt(CiphertextBlob=blob, EncryptionContext=context)['Plaintext'] == plaintext
            new_blob = learner.encrypt(KeyId=name, Plaintext=plaintext, EncryptionContext=context)['CiphertextBlob']
            assert a.decrypt(KeyId=second['KeyId'], CiphertextBlob=new_blob, EncryptionContext=context)['Plaintext'] == plaintext
            policy(iam, resources, second['Arn'])
            error('AccessDeniedException', lambda: learner.encrypt(KeyId=name, Plaintext=plaintext))
            error('AccessDeniedException', lambda: learner.delete_alias(AliasName=name))
            a.disable_key(KeyId=second['KeyId'])
            # Disabled keys support alias management, but not crypto.
            a.update_alias(AliasName=name, TargetKeyId=second['KeyId'])
            error('DisabledException', lambda: a.encrypt(KeyId=name, Plaintext=plaintext))
            a.enable_key(KeyId=second['KeyId'])
            error('InvalidMarkerException', lambda: a.list_aliases(Limit=1, Marker=marker))
            persisted = aliases(a, KeyId=second['Arn'])['Aliases']
            wrong_token, transport = client('kms', peer_url, operator)
            try:
                wrong_token.list_aliases()
            except ClientError as e:
                assert transport['last_status'] == 401 and e.response['ResponseMetadata']['HTTPStatusCode'] == 401
            except ResponseParserError:
                assert transport['last_status'] == 401
            else:
                raise AssertionError('Expected actual cross-process HTTP401')
            iam.delete_role_policy(RoleName='alias-learner', PolicyName='AliasUse')
            iam.delete_role(RoleName='alias-learner')
            b.delete_alias(AliasName=name)
            west.delete_alias(AliasName=name)
        with running(path, authenticated=True) as (endpoint, operator):
            a, _ = client('kms', endpoint, operator)
            assert aliases(a, KeyId=second['KeyId'])['Aliases'] == persisted
            assert a.decrypt(KeyId=name, CiphertextBlob=new_blob, EncryptionContext=context)['Plaintext'] == plaintext
            a.delete_alias(AliasName=name)
            a.delete_alias(AliasName='alias/Second')
            assert aliases(a)['Aliases'] == []
            assert a.describe_key(KeyId=first['KeyId'])['KeyMetadata']['KeyId'] == first['KeyId']
            assert a.decrypt(CiphertextBlob=blob, EncryptionContext=context)['Plaintext'] == plaintext
    for item in stats:
        assert item['calls'] > 0 and item['calls'] == item['sends'] == item['responses']
    print(json.dumps({'nativeSDKExecuted': True, 'nativeAliases': 4,
        'sameDBActualCrypto': True, 'twoProcessesTwoRegionsRestart': True,
        'crossTokenObservedBeforeParse': 401,
        'calls': sum(i['calls'] for i in stats), 'sends': sum(i['sends'] for i in stats),
        'responses': sum(i['responses'] for i in stats),
        'keyDeletion': 'unsupported; alias-only cleanup and temporary fixture DB removal'}))

if __name__ == '__main__':
    try:
        main()
    finally:
        for c in clients:
            c.close()
