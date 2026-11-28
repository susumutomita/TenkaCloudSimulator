"""Verify local KMS grant management and a real STS grantee's DescribeKey."""
import argparse
from contextlib import ExitStack
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlsplit

from sts_session_lifecycle import (LocalClients, LocalSimulator, POLICY_NAME, ROLE,
                                   ROLE_ARN, TRUST, cleanup_role, successful)


def denied(call, code, status):
    from botocore.exceptions import ClientError
    try:
        call()
    except ClientError as error:
        assert error.response['Error']['Code'] == code
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == status
        return
    raise AssertionError('expected native refusal')


def kms_client(session, team, stack, config, credentials=None):
    signed_role = credentials is not None
    options = credentials or {'aws_access_key_id': 'TCSIMKMSLOCAL', 'aws_secret_access_key': 'fixture-local-only'}
    client = session.client('kms', endpoint_url=team.endpoint, region_name='us-east-1', config=config, **options)
    stack.callback(client.close)
    if not signed_role:
        client.meta.events.register('before-sign.kms', lambda request, **_: request.headers.add_header('X-Simulator-Token', team.token))

    def guard(request, **_):
        assert request.method == 'POST' and request.url == team.endpoint + '/'
        headers = {key.lower(): val for key, val in request.headers.items()}
        assert 'authorization' in headers and 'x-simulator-principal' not in headers and 'cookie' not in headers
        assert ('x-simulator-token' in headers) != signed_role
        assert ('x-amz-security-token' in headers) == signed_role

    client.meta.events.register('before-send.kms', guard)
    return client


def role_client(session, team, stack, config, sts, role_id):
    value = successful(sts.assume_role, RoleArn=ROLE_ARN, RoleSessionName='kms-grantee', DurationSeconds=900)
    assert value['AssumedRoleUser']['AssumedRoleId'] == role_id + ':kms-grantee'
    creds = value['Credentials']
    return kms_client(session, team, stack, config, {
        'aws_access_key_id': creds['AccessKeyId'], 'aws_secret_access_key': creds['SecretAccessKey'],
        'aws_session_token': creds['SessionToken']})


def clean_key(client, key):
    for grant in successful(client.list_grants, KeyId=key)['Grants']:
        successful(client.revoke_grant, KeyId=key, GrantId=grant['GrantId'])
    assert successful(client.list_grants, KeyId=key)['Grants'] == []
    successful(client.disable_key, KeyId=key)
    assert successful(client.describe_key, KeyId=key)['KeyMetadata']['KeyState'] == 'Disabled'


def exercise(session, config, binary, root, stack):
    from botocore.exceptions import ClientError
    owner, peer = [LocalSimulator(root / name, binary) for name in ('owner', 'peer')]
    teams = []
    for team in owner, peer:
        stack.callback(team.close)
        team.start()
        assert urlsplit(team.endpoint).hostname == '127.0.0.1'
        clients = LocalClients(session, team, stack, config)
        iam, sts = clients.client('iam'), clients.client('sts')
        manager = kms_client(session, team, stack, config)
        role = successful(iam.create_role, RoleName=ROLE, AssumeRolePolicyDocument=json.dumps(TRUST))['Role']
        stack.callback(cleanup_role, iam, ClientError)
        key = successful(manager.create_key, Description='local grant fixture')['KeyMetadata']['Arn']
        stack.callback(clean_key, manager, key)
        reader = role_client(session, team, stack, config, sts, role['RoleId'])
        denied(lambda: reader.describe_key(KeyId=key), 'AccessDeniedException', 403)
        args = {'KeyId': key, 'GranteePrincipal': role['Arn'], 'Operations': ['DescribeKey'], 'Name': 'practice-read'}
        grant = successful(manager.create_grant, **args)
        repeated = successful(manager.create_grant, **args)
        assert grant['GrantId'] == repeated['GrantId'] and grant['GrantToken'] != repeated['GrantToken']
        assert successful(reader.describe_key, KeyId=key, GrantTokens=[grant['GrantToken'], repeated['GrantToken']])['KeyMetadata']['Arn'] == key
        entry = successful(manager.list_grants, KeyId=key)['Grants']
        assert len(entry) == 1 and entry[0]['GrantId'] == grant['GrantId'] and entry[0]['GranteePrincipal'] == role['Arn']
        assert entry[0]['Operations'] == ['DescribeKey'] and 'GrantToken' not in entry[0]
        teams.append((manager, iam, sts, reader, role['RoleId'], key, grant))
    manager, iam, sts, reader, role_id, key, grant = teams[0]
    peer_manager, _, _, peer_reader, _, peer_key, peer_grant = teams[1]
    assert owner.token != peer.token and owner.port != peer.port and key != peer_key
    denied(lambda: reader.describe_key(KeyId=peer_key), 'AccessDeniedException', 403)
    denied(lambda: reader.list_keys(), 'UnrecognizedClientException', 403)
    policy = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Deny', 'Action': 'kms:DescribeKey', 'Resource': key}]}
    successful(iam.put_role_policy, RoleName=ROLE, PolicyName=POLICY_NAME, PolicyDocument=json.dumps(policy))
    denied(lambda: reader.describe_key(KeyId=key), 'AccessDeniedException', 403)
    successful(iam.delete_role_policy, RoleName=ROLE, PolicyName=POLICY_NAME)
    assert successful(reader.describe_key, KeyId=key)['KeyMetadata']['Arn'] == key
    owner.close()
    owner.start()
    denied(lambda: reader.describe_key(KeyId=key), 'UnrecognizedClientException', 403)
    fresh = role_client(session, owner, stack, config, sts, role_id)
    assert successful(fresh.describe_key, KeyId=key)['KeyMetadata']['Arn'] == key
    successful(manager.disable_key, KeyId=key)
    assert successful(fresh.describe_key, KeyId=key)['KeyMetadata']['KeyState'] == 'Disabled'
    denied(lambda: manager.create_grant(KeyId=key, GranteePrincipal=ROLE_ARN, Operations=['DescribeKey']), 'DisabledException', 400)
    successful(manager.revoke_grant, KeyId=key, GrantId=grant['GrantId'])
    denied(lambda: fresh.describe_key(KeyId=key), 'AccessDeniedException', 403)
    assert successful(manager.list_grants, KeyId=key)['Grants'] == []
    assert successful(peer_reader.describe_key, KeyId=peer_key)['KeyMetadata']['Arn'] == peer_key
    assert successful(peer_manager.list_grants, KeyId=peer_key)['Grants'][0]['GrantId'] == peer_grant['GrantId']


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    assert not any(key.startswith(('AWS_', 'BOTO_')) for key in os.environ)
    assert all(not path.exists() and not path.is_symlink() for path in (
        Path.home() / '.aws/config', Path.home() / '.aws/credentials', Path.home() / '.boto', Path('/etc/boto.cfg')))
    assert version('boto3') == '1.42.0' and version('botocore') == '1.41.6'
    import boto3
    from botocore.config import Config
    binary = args.binary.resolve(strict=True)
    with binary.open('rb') as data:
        assert hashlib.file_digest(data, 'sha256').hexdigest() == args.binary_sha256
    session = boto3.Session()
    config = Config(connect_timeout=3, read_timeout=5, retries={'max_attempts': 0}, proxies={})
    with tempfile.TemporaryDirectory(prefix='kms-grant-sdk-') as directory, ExitStack() as stack:
        exercise(session, config, binary, Path(directory), stack)
    print('KMS grant lifecycle, effective signed role metadata, peer, restart, revocation and cleanup passed')


if __name__ == '__main__':
    main()
