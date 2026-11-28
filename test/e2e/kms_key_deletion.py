"""Run the local KMS deletion lifecycle with the pinned official SDK and two CLIs."""
import argparse
from contextlib import ExitStack
from datetime import datetime, timedelta, timezone
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import sqlite3
import tempfile

from kms_grants import denied, kms_client, role_client
from sts_session_lifecycle import LocalClients, LocalSimulator, ROLE, TRUST, cleanup_role, successful


def check_owner(session, config, stack, team, peer, owner_kms, peer_kms):
    from botocore.exceptions import ClientError
    clients = LocalClients(session, team, stack, config)
    iam, sts = clients.client('iam'), clients.client('sts')
    role = successful(iam.create_role, RoleName=ROLE, AssumeRolePolicyDocument=json.dumps(TRUST))['Role']
    stack.callback(cleanup_role, iam, ClientError)
    key = successful(owner_kms.create_key, Description='local scheduled key')['KeyMetadata']
    other = successful(owner_kms.create_key, Description='retained local key')['KeyMetadata']
    peer_key = successful(peer_kms.create_key, Description='local scheduled key')['KeyMetadata']
    assert team.port != peer.port and team.token != peer.token and other['KeyId'] != key['KeyId'] != peer_key['KeyId']
    arn = key['Arn']
    for client, target in ((owner_kms, arn), (peer_kms, peer_key['Arn'])):
        successful(client.create_alias, AliasName='alias/deletion-practice', TargetKeyId=target)
    try:
        attrs = {'KeyId': arn, 'GranteePrincipal': role['Arn'], 'Operations': ['DescribeKey']}
        grant = successful(owner_kms.create_grant, **attrs)
        reader = role_client(session, team, stack, config, sts, role['RoleId'])
        blob = successful(owner_kms.encrypt, KeyId=arn, Plaintext=b'local key lifecycle')['CiphertextBlob']
        earliest = datetime.now(timezone.utc) + timedelta(days=7) - timedelta(milliseconds=1)
        scheduled = successful(owner_kms.schedule_key_deletion, KeyId=arn, PendingWindowInDays=7)
        assert earliest <= scheduled['DeletionDate'] <= datetime.now(timezone.utc) + timedelta(days=7)
        assert scheduled['KeyId'] == arn and scheduled['PendingWindowInDays'] == 7 and scheduled['KeyState'] == 'PendingDeletion'
        pending = successful(reader.describe_key, KeyId=arn, GrantTokens=[grant['GrantToken']])['KeyMetadata']
        assert pending['DeletionDate'] == scheduled['DeletionDate'] and not pending['Enabled']
        assert 'PendingDeletionWindowInDays' not in pending
        denied(lambda: owner_kms.schedule_key_deletion(KeyId=arn), 'KMSInvalidStateException', 400)
        denied(lambda: owner_kms.decrypt(CiphertextBlob=blob), 'KMSInvalidStateException', 400)
        denied(lambda: owner_kms.update_alias(AliasName='alias/deletion-practice', TargetKeyId=arn), 'KMSInvalidStateException', 400)
        successful(owner_kms.update_alias, AliasName='alias/deletion-practice', TargetKeyId=other['KeyId'])
        team.close()
        team.start()
        assert successful(owner_kms.describe_key, KeyId=key['KeyId'])['KeyMetadata'] == pending
        assert successful(owner_kms.cancel_key_deletion, KeyId=key['KeyId'])['KeyId'] == arn
        canceled = successful(owner_kms.describe_key, KeyId=arn)['KeyMetadata']
        assert canceled['KeyState'] == 'Disabled' and 'DeletionDate' not in canceled
        denied(lambda: owner_kms.decrypt(CiphertextBlob=blob), 'DisabledException', 400)
        successful(owner_kms.enable_key, KeyId=arn)
        assert successful(owner_kms.decrypt, CiphertextBlob=blob)['Plaintext'] == b'local key lifecycle'
        successful(owner_kms.create_alias, AliasName='alias/expire-with-key', TargetKeyId=arn)
        again = successful(owner_kms.schedule_key_deletion, KeyId=arn)
        assert again['PendingWindowInDays'] == 30
        # Advance only the owned, stopped fixture's saved interval into the past.
        # Both dates move together so the full 30-day wait remains represented;
        # the unchanged CLI clock then processes a real past-due row on startup.
        team.close()
        with sqlite3.connect(team.database) as database:
            shift = int(timedelta(days=31).total_seconds() * 1000)
            result = database.execute('UPDATE aws_kms_pending_deletions SET scheduled_ms=scheduled_ms-?, due_ms=due_ms-? WHERE region=? AND key_id=? AND window_days=30',
                                      (shift, shift, 'us-east-1', key['KeyId']))
            assert result.rowcount == 1
        team.start()
        denied(lambda: owner_kms.cancel_key_deletion(KeyId=arn), 'NotFoundException', 400)
        denied(lambda: owner_kms.list_grants(KeyId=arn), 'NotFoundException', 400)
        denied(lambda: owner_kms.decrypt(CiphertextBlob=blob), 'NotFoundException', 400)
        remaining = successful(owner_kms.list_aliases)['Aliases']
        assert [(row['AliasName'], row['TargetKeyId']) for row in remaining] == [('alias/deletion-practice', other['KeyId'])]
        assert {row['KeyArn'] for row in successful(owner_kms.list_keys)['Keys']} == {other['Arn']}
        assert successful(peer_kms.describe_key, KeyId=peer_key['Arn'])['KeyMetadata'] == peer_key
        assert successful(peer_kms.list_aliases)['Aliases'][0]['TargetKeyId'] == peer_key['KeyId']
    finally:
        if team.process is None:
            team.start()
        for client in (owner_kms, peer_kms):
            for row in successful(client.list_aliases)['Aliases']:
                successful(client.delete_alias, AliasName=row['AliasName'])
            assert successful(client.list_aliases)['Aliases'] == []
            for row in successful(client.list_keys)['Keys']:
                metadata = successful(client.describe_key, KeyId=row['KeyId'])['KeyMetadata']
                if metadata['KeyState'] == 'PendingDeletion':
                    successful(client.cancel_key_deletion, KeyId=row['KeyId'])
                for grant in successful(client.list_grants, KeyId=row['KeyId'])['Grants']:
                    successful(client.revoke_grant, KeyId=row['KeyId'], GrantId=grant['GrantId'])
                successful(client.disable_key, KeyId=row['KeyId'])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    assert not any(key.startswith(('AWS_', 'BOTO_')) for key in os.environ)
    assert all(not p.exists() and not p.is_symlink() for p in (
        Path.home() / '.aws/config', Path.home() / '.aws/credentials', Path.home() / '.boto', Path('/etc/boto.cfg')))
    assert version('boto3') == '1.42.0' and version('botocore') == '1.41.6'
    import boto3
    from botocore.config import Config
    binary = args.binary.resolve(strict=True)
    with binary.open('rb') as source:
        assert hashlib.file_digest(source, 'sha256').hexdigest() == args.binary_sha256
    session = boto3.Session()
    config = Config(connect_timeout=3, read_timeout=5, retries={'max_attempts': 0}, proxies={})
    with tempfile.TemporaryDirectory(prefix='kms-deletion-sdk-') as path, ExitStack() as stack:
        owner, peer = [LocalSimulator(Path(path) / name, binary) for name in ('owner', 'peer')]
        for team in owner, peer:
            stack.callback(team.close)
            team.start()
        check_owner(session, config, stack, owner, peer, kms_client(session, owner, stack, config),
                    kms_client(session, peer, stack, config))
    print('KMS SDK schedule, cancel, crypto, restart, past-due cleanup and isolated peer passed')


if __name__ == '__main__':
    main()
