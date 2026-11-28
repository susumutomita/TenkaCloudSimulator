"""Stock boto3, two owned CLIs: immutable local EC2 template version 1.

No instance allocation or PCS scheduler. Run in the approved loopback-isolated
runner with the normal pinned requirements and SIMULATOR_BINARY. The inherited
SDK profile is retained; no credential discovery or profile fallback is added.
"""
import contextlib
import json
from pathlib import Path
import tempfile
import sys
from urllib.parse import urlsplit

import boto3
from botocore.config import Config

from pcs_queue_associations import instance, refused


def client(endpoint, token):
    parsed = urlsplit(endpoint)
    assert parsed.scheme == 'http' and parsed.hostname == '127.0.0.1'
    assert parsed.port and not parsed.username and not parsed.password
    c = boto3.client('ec2', endpoint_url=endpoint, region_name='us-east-1',
                     aws_access_key_id='TCSIMLOCALVERSIONONE',
                     aws_secret_access_key='synthetic-local-only',
                     config=Config(retries={'max_attempts': 0}, connect_timeout=2, read_timeout=10))
    c.meta.events.register('before-sign.ec2', lambda request, **_: request.headers.add_header('X-Simulator-Token', token))

    def local_only(request, **_):
        actual = urlsplit(request.url)
        assert (actual.scheme, actual.netloc, actual.path, actual.query) == ('http', parsed.netloc, '/', '')
        assert request.method == 'POST'
    c.meta.events.register('before-send.ec2', local_only)
    return c


def seed(c):
    vpc = c.create_vpc(CidrBlock='10.91.0.0/16')['Vpc']['VpcId']
    try:
        group = c.create_security_group(VpcId=vpc, GroupName='template-web',
                                        Description='Owned immutable template')['GroupId']
    except BaseException:
        c.delete_vpc(VpcId=vpc)
        raise
    return vpc, group


def payload(group, token='version-one-create'):
    return dict(LaunchTemplateName='pcs-basic', ClientToken=token,
                VersionDescription='Immutable security groups',
                LaunchTemplateData={'SecurityGroupIds': [group]})


def saved(c):
    return c.describe_launch_templates()['LaunchTemplates']


def clean(c):
    # Only this fixture's own process, database and explicit resource names.
    for template in saved(c):
        assert template['LaunchTemplateName'] == 'pcs-basic'
        c.delete_launch_template(LaunchTemplateId=template['LaunchTemplateId'])
    for group in c.describe_security_groups()['SecurityGroups']:
        assert group['GroupName'] == 'template-web'
        c.delete_security_group(GroupId=group['GroupId'])
    for vpc in c.describe_vpcs()['Vpcs']:
        assert vpc['CidrBlock'] == '10.91.0.0/16'
        c.delete_vpc(VpcId=vpc['VpcId'])
    assert not saved(c) and not c.describe_vpcs()['Vpcs']


def snapshot(c):
    return dict(templates=saved(c), vpcs=c.describe_vpcs()['Vpcs'],
                groups=c.describe_security_groups()['SecurityGroups'])


def cleanup_preserving(c):
    original = sys.exception()
    try:
        clean(c)
    except BaseException as cleanup_error:
        if original is None:
            raise
        original.add_note('Owned template cleanup failed: ' + type(cleanup_error).__name__)


@contextlib.contextmanager
def cleanup_on_failure(c):
    try:
        yield
    except BaseException:
        cleanup_preserving(c)
        raise


def main():
    boto3.Session()  # Preserve the ordinary inherited SDK profile precondition.
    with tempfile.TemporaryDirectory(prefix='tenka-template-v1-') as temporary:
        root = Path(temporary)
        with instance(root / 'peer') as (peer_endpoint, peer_token), contextlib.closing(client(peer_endpoint, peer_token)) as peer:
            try:
                _, peer_group = seed(peer)
                peer_template = peer.create_launch_template(**payload(peer_group))['LaunchTemplate']
                peer_before = snapshot(peer)
                with instance(root / 'owner') as (endpoint, token), contextlib.closing(client(endpoint, token)) as owner, cleanup_on_failure(owner):
                    _, group = seed(owner)
                    original = owner.create_launch_template(**payload(group))['LaunchTemplate']
                    template_id = original['LaunchTemplateId']
                    assert original['DefaultVersionNumber'] == original['LatestVersionNumber'] == 1
                    assert owner.create_launch_template(**payload(group))['LaunchTemplate'] == original
                    version, = owner.describe_launch_template_versions(LaunchTemplateId=template_id, Versions=['$Latest'])['LaunchTemplateVersions']
                    assert version['LaunchTemplateId'] == template_id and version['VersionNumber'] == 1 and version['DefaultVersion']
                    assert version['LaunchTemplateData'] == {'SecurityGroupIds': [group]}
                    refused('IdempotentParameterMismatch', lambda: owner.create_launch_template(**dict(payload(group), VersionDescription='Changed')), 400)
                    refused('UnsupportedOperation', lambda: owner.create_launch_template(**dict(payload(group, 'unsupported'), LaunchTemplateData={'SecurityGroupIds': [group], 'UserData': 'unused'})), 400)
                    assert saved(owner) == [original] and snapshot(peer) == peer_before
                with instance(root / 'owner') as (endpoint, token), contextlib.closing(client(endpoint, token)) as owner, cleanup_on_failure(owner):
                    assert saved(owner) == [original]
                    assert owner.create_launch_template(**payload(group))['LaunchTemplate'] == original
                    owner.delete_launch_template(LaunchTemplateName='pcs-basic')
                    refused('InvalidLaunchTemplateId.NotFound', lambda: owner.describe_launch_templates(LaunchTemplateIds=[template_id]), 400)
                    assert owner.create_launch_template(**payload(group))['LaunchTemplate'] == original
                    assert not saved(owner)
                    replacement = owner.create_launch_template(**payload(group, 'distinct-new-lifetime'))['LaunchTemplate']
                    assert replacement['LaunchTemplateId'] != template_id
                    assert replacement['LaunchTemplateName'] == original['LaunchTemplateName']
                    assert snapshot(peer) == peer_before and peer_template in peer_before['templates']
                    clean(owner)
            finally:
                cleanup_preserving(peer)
    print(json.dumps({'templateVersion': 1, 'nativeOperations': 4, 'ownedProcesses': 2,
                      'restart': True, 'retiredReplayWithoutResurrection': True,
                      'sameNameFreshIdentity': True, 'peerUnchanged': True, 'cleanup': True}))


if __name__ == '__main__':
    main()
