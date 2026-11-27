"""Opt-in pinned native SDK proof for atomic subnet tags and the old untagged path."""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import tempfile

from ec2_launch_preflight_native import Local, run_default
from ec2_native_network_sdk import boto3, botocore, reject, sdk

REGION = 'us-east-1'
OTHER_REGION = 'ap-northeast-1'
TEST_TAGS = [{'Key': 'Name', 'Value': '学習 <local> & ampersand'}, {'Key': 'Stage', 'Value': ''}]


def tag_fields(rows=TEST_TAGS):
    return [{'ResourceType': 'subnet', 'Tags': rows}]


def subnets(client):
    return {row['SubnetId']: row for row in client.describe_subnets()['Subnets']}


def identity(port, token, name, vpc, tags=False, exact_deny=False):
    arn = f'arn:aws:ec2:{REGION}:000000000000:'
    resources = [arn+'vpc/'+vpc, arn+'subnet/*']
    statements = [
        {'Effect': 'Allow', 'Action': 'ec2:CreateSubnet', 'Resource': resources},
        {'Effect': 'Allow', 'Action': 'ec2:DescribeSubnets', 'Resource': '*'},
    ]
    if tags:
        statements.append({'Effect': 'Allow', 'Action': 'ec2:CreateTags', 'Resource': arn+'subnet/*'})
    if exact_deny:
        statements.append({'Effect': 'Deny', 'Action': 'ec2:CreateTags', 'Resource': arn+'subnet/subnet-'+'?'*17})
    headers = {'X-Simulator-Token': token, 'Content-Type': 'application/json'}
    local = Local(port)
    local.call('PUT', f'/_simulator/v1/iam/principals/{name}/policy',
               json.dumps({'Version': '2012-10-17', 'Statement': statements}), **headers)
    _, reply = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': name}), **headers)
    return json.loads(reply)['AccessToken']


def check(binary):
    assert __debug__ and (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    with tempfile.TemporaryDirectory(prefix='subnet-create-tags-') as directory:
        state = Path(directory) / 'native.sqlite'
        with run_default(binary, state=state) as (port, token), ExitStack() as pool:
            admin = sdk(pool, port, token)
            vpc = admin.create_vpc(CidrBlock='10.95.0.0/16')['Vpc']['VpcId']
            old = sdk(pool, port, identity(port, token, 'subnet-create-untagged', vpc))
            exact = sdk(pool, port, identity(port, token, 'subnet-create-deny-id', vpc, tags=True, exact_deny=True))
            tagged = sdk(pool, port, identity(port, token, 'subnet-create-tagged', vpc, tags=True))
            requested = dict(VpcId=vpc, CidrBlock='10.95.1.24/24', AvailabilityZone=REGION+'b')
            for client in (old, exact):
                reject(client.create_subnet, 'UnauthorizedOperation', **requested, TagSpecifications=tag_fields())
            assert not subnets(admin), 'refused tag-on-create left persistent state'
            first = tagged.create_subnet(**requested, TagSpecifications=tag_fields())['Subnet']
            assert first['VpcId'] == vpc and first['CidrBlock'] == '10.95.1.0/24'
            assert first['AvailabilityZone'] == REGION+'b' and first['MapPublicIpOnLaunch'] is False
            assert first['Tags'] == TEST_TAGS
            legacy = old.create_subnet(VpcId=vpc, CidrBlock='10.95.2.0/24')['Subnet']
            assert legacy['Tags'] == []
            ids = {row['SubnetId'] for row in (first, legacy)}
            baseline = {first['SubnetId']: first, legacy['SubnetId']: legacy}
            assert subnets(tagged) == subnets(admin) == baseline
            assert not subnets(sdk(pool, port, token, OTHER_REGION))

        with run_default(binary, state=state) as (port, token), ExitStack() as pool:
            admin = sdk(pool, port, token)
            assert subnets(admin) == baseline
            for subnet in ids:
                admin.delete_subnet(SubnetId=subnet)
            admin.delete_vpc(VpcId=vpc)
            assert not subnets(admin) and not admin.describe_vpcs()['Vpcs']
    print('PASS boto3 1.42.0/botocore 1.41.6: tag-on-create, wildcard/exact IAM denial, untagged legacy, region, restart and cleanup.')


if __name__ == '__main__':
    check(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
