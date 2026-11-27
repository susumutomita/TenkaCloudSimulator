"""Opt-in native EC2 DNS settings check using real boto3 and a loopback-only simulator."""
from contextlib import ExitStack
from pathlib import Path
import os
import tempfile

from ec2_internet_gateway_sdk import allow, cfn_client, grant
from ec2_launch_preflight_native import run_default
from ec2_native_network_sdk import REGION, boto3, botocore, fresh_main_route_table, reject, sdk


def value(client, vpc, key):
    api = 'enable' + key[6:]
    result = client.describe_vpc_attribute(VpcId=vpc, Attribute=api)
    assert result['VpcId'] == vpc and set(result) == {'VpcId', 'ResponseMetadata', key}
    assert set(result[key]) == {'Value'} and type(result[key]['Value']) is bool
    return result[key]['Value']


def verify(binary):
    assert __debug__ and (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    with tempfile.TemporaryDirectory(prefix='sim-vpc-dns-') as td:
        path = Path(td) / 'vpc.sqlite'
        with run_default(binary, state=path) as (port, token), ExitStack() as pool:
            admin = sdk(pool, port, token)
            foreign_region = sdk(pool, port, token, 'us-west-2')
            vpc = admin.create_vpc(CidrBlock='10.18.0.0/16')['Vpc']['VpcId']
            other = admin.create_vpc(CidrBlock='10.28.0.0/16')['Vpc']['VpcId']
            main = fresh_main_route_table(admin, vpc, '10.18.0.0/16')
            cfn = cfn_client(pool, port, token)
            stack = cfn.create_stack(StackName='dns-stack-fixture', TemplateBody='''{
                "Resources":{"V":{"Type":"AWS::EC2::VPC","Properties":{
                    "CidrBlock":"10.38.0.0/16","EnableDnsHostnames":true}}},
                "Outputs":{"ID":{"Value":{"Ref":"V"}}}
            }''')['StackId']
            foreign = cfn.describe_stacks(StackName=stack)['Stacks'][0]['Outputs'][0]['OutputValue']
            assert value(admin, vpc, 'EnableDnsSupport')
            assert not value(admin, vpc, 'EnableDnsHostnames')
            assert value(admin, foreign, 'EnableDnsHostnames')
            reject(admin.modify_vpc_attribute, 'UnsupportedOperation',
                   VpcId=foreign, EnableDnsHostnames={'Value': False})
            arn = f'arn:aws:ec2:{REGION}:000000000000:vpc/{vpc}'
            scoped = sdk(pool, port, grant(port, token, 'dns-fixture-editor', [
                allow('DescribeVpcAttribute', arn), allow('ModifyVpcAttribute', arn)]))
            reject(scoped.modify_vpc_attribute, 'UnauthorizedOperation',
                   VpcId=other, EnableDnsHostnames={'Value': True})
            reject(foreign_region.describe_vpc_attribute, 'InvalidVpcID.NotFound',
                   VpcId=vpc, Attribute='enableDnsSupport')
            reject(scoped.modify_vpc_attribute, 'InvalidParameterCombination',
                   VpcId=vpc, EnableDnsSupport={'Value': False}, EnableDnsHostnames={'Value': True})
            scoped.modify_vpc_attribute(VpcId=vpc, EnableDnsSupport={'Value': False})
            reject(scoped.modify_vpc_attribute, 'InvalidParameterCombination',
                   VpcId=vpc, EnableDnsHostnames={'Value': True})
            scoped.modify_vpc_attribute(VpcId=vpc, EnableDnsSupport={'Value': True})
            scoped.modify_vpc_attribute(VpcId=vpc, EnableDnsHostnames={'Value': True})
            reject(scoped.describe_vpc_attribute, 'DryRunOperation',
                   VpcId=vpc, Attribute='enableDnsHostnames', DryRun=True)
            assert value(scoped, vpc, 'EnableDnsHostnames')
        with run_default(binary, state=path) as (port, token), ExitStack() as pool:
            admin = sdk(pool, port, token)
            assert value(admin, vpc, 'EnableDnsHostnames') and value(admin, vpc, 'EnableDnsSupport')
            assert value(admin, foreign, 'EnableDnsHostnames')
            assert not value(admin, other, 'EnableDnsHostnames')
            assert fresh_main_route_table(admin, vpc, '10.18.0.0/16') == main
            admin.modify_vpc_attribute(VpcId=vpc, EnableDnsHostnames={'Value': False})
            assert not value(admin, vpc, 'EnableDnsHostnames')
            cfn_client(pool, port, token).delete_stack(StackName=stack)
            for id_ in (vpc, other):
                admin.delete_vpc(VpcId=id_)
            assert admin.describe_vpcs()['Vpcs'] == [] and admin.describe_route_tables()['RouteTables'] == []


if __name__ == '__main__':
    verify(str(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)))
    print('PASS EC2 VPC DNS Query: actual SDK, exact-scope IAM, stack read/refusal, restart, main route, cleanup; no DNS traffic')
