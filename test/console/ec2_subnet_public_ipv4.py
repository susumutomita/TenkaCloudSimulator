"""Opt-in exact SDK and served Console proof for saved native subnet settings."""
from contextlib import ExitStack
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess
import tempfile

from ec2_launch_preflight_native import CFN, Local, run_default
from ec2_native_network_sdk import boto3, botocore, reject, sdk

REGION = 'us-east-1'
STACK = 'subnet-settings-read-only'
PRINCIPAL = 'exact-subnet-settings-fixture'


def subnet_settings_port(port, token, subnet):
    local = Local(port)
    parent = local.session(token)
    statements = [
        {'Effect': 'Allow', 'Action': ['ec2:DescribeVpcs', 'ec2:DescribeSubnets', 'ec2:DescribeSecurityGroups',
                                       'ec2:DescribeRouteTables', 'ec2:DescribeNetworkAcls', 'ec2:DescribeInternetGateways'],
         'Resource': '*'},
        {'Effect': 'Allow', 'Action': 'ec2:ModifySubnetAttribute',
         'Resource': f'arn:aws:ec2:{REGION}:000000000000:subnet/{subnet}'},
    ]
    content = {'Content-Type': 'application/json'}
    policy = json.dumps({'Version': '2012-10-17', 'Statement': statements})
    local.call('PUT', '/_simulator/v1/iam/principals/'+PRINCIPAL+'/policy', policy, Cookie=parent, **content)
    _, issued = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': PRINCIPAL}), Cookie=parent, **content)
    actor = json.loads(issued)['AccessToken']
    return actor, local.session(actor)


def subnets(c):
    return {row['SubnetId']: row for row in c.describe_subnets()['Subnets']}


def verify(binary):
    assert __debug__ and (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    asset = sha256((Path(__file__).resolve().parents[2] / 'internal/server/web/app.js').read_bytes()).hexdigest()
    driver = Path(__file__).with_suffix('.cjs')
    with tempfile.TemporaryDirectory(prefix='subnet-public-ipv4-proof-') as td:
        state = Path(td) / 'subnet-settings.sqlite'

        def drive(port, cookie, enabled, locale):
            payload = dict(port=port, cookie=cookie, enabled=enabled, locale=locale, principal=PRINCIPAL,
                           asset=asset, vpc=vpc, subnet=target, neighbor=neighbor, stackVpc=legacy_vpc,
                           stackSubnet=legacy)
            subprocess.run(['node', str(driver)], input=json.dumps(payload), text=True, check=True, timeout=80)

        with run_default(binary, state=state) as (port, token), ExitStack() as pool:
            operator = sdk(pool, port, token)
            vpc = operator.create_vpc(CidrBlock='10.104.0.0/16')['Vpc']['VpcId']
            a = operator.create_subnet(VpcId=vpc, CidrBlock='10.104.1.0/24')['Subnet']
            b = operator.create_subnet(VpcId=vpc, CidrBlock='10.104.2.0/24')['Subnet']
            target, neighbor = a['SubnetId'], b['SubnetId']
            assert (a['MapPublicIpOnLaunch'], b['MapPublicIpOnLaunch']) == (False, False)
            local = Local(port)
            cookie = local.session(token)
            template = {'Resources': {
                'V': {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.106.0.0/16'}},
                'S': {'Type': 'AWS::EC2::Subnet', 'Properties': {
                    'VpcId': {'Ref': 'V'}, 'CidrBlock': '10.106.1.0/24',
                    'MapPublicIpOnLaunch': True, 'Tags': [{'Key': 'Name', 'Value': 'Stack-owned fixture'}]}}
            }}
            assert local.query('cloudformation', 'CreateStack', cookie, REGION, StackName=STACK,
                               TemplateBody=json.dumps(template)).findtext('.//'+CFN+'StackId')
            baseline = subnets(operator)
            legacy, = baseline.keys() - {target, neighbor}
            legacy_row = baseline[legacy]
            legacy_vpc = legacy_row['VpcId']
            assert 'MapPublicIpOnLaunch' not in legacy_row and legacy_row['Tags'] == [{'Key': 'Name', 'Value': 'Stack-owned fixture'}]
            actor, browser_cookie = subnet_settings_port(port, token, target)
            principal = sdk(pool, port, actor)
            reject(principal.modify_subnet_attribute, 'UnauthorizedOperation', SubnetId=neighbor, MapPublicIpOnLaunch={'Value': True})
            reject(operator.modify_subnet_attribute, 'UnsupportedOperation', SubnetId=legacy, MapPublicIpOnLaunch={'Value': False})
            reject(operator.modify_subnet_attribute, 'UnsupportedOperation', SubnetId=target, AssignIpv6AddressOnCreation={'Value': True})
            assert subnets(operator) == baseline
            drive(port, browser_cookie, True, 'en')
            enabled = subnets(principal)
            assert enabled == {**baseline, target: {**baseline[target], 'MapPublicIpOnLaunch': True}}

        with run_default(binary, state=state) as (port, token), ExitStack() as pool:
            operator = sdk(pool, port, token)
            assert subnets(operator) == enabled
            actor, browser_cookie = subnet_settings_port(port, token, target)
            drive(port, browser_cookie, False, 'ja')
            assert subnets(sdk(pool, port, actor)) == baseline

        with run_default(binary, state=state) as (port, token), ExitStack() as pool:
            operator = sdk(pool, port, token)
            assert subnets(operator) == baseline
            response = operator.modify_subnet_attribute(SubnetId=target, MapPublicIpOnLaunch={'Value': True})
            assert response['ResponseMetadata']['HTTPStatusCode'] == 200
            assert subnets(operator) == enabled
            operator.delete_subnet(SubnetId=target)
            operator.delete_subnet(SubnetId=neighbor)
            operator.delete_vpc(VpcId=vpc)
            local = Local(port)
            local.query('cloudformation', 'DeleteStack', local.session(token), REGION, StackName=STACK)
            assert not subnets(operator) and not operator.describe_vpcs()['Vpcs']

    print('PASS pinned native SDK and served bilingual JSDOM: off/true/false and each restart, scoped denial, no false notice/replay, CFN unchanged, and cleanup. Configuration only; no Chromium, guest or cloud.')


if __name__ == '__main__':
    verify(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
