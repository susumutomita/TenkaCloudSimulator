"""Pinned actual EC2/CloudFormation SDK and optional served JSDOM, numeric loopback only."""
from contextlib import ExitStack
from hashlib import sha256
import http.client
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from ec2_internet_gateway_sdk import allow, cfn_client, create_stack, grant, outputs
from ec2_launch_preflight_native import Local, REGIONS, run_default
from ec2_native_network_sdk import REGION, boto3, botocore, fresh_main_route_table, native_local_route, reject, sdk

ROOT = Path(__file__).resolve().parents[2]
CIDR = '0.0.0.0/0'
VPC_CIDR = '10.91.0.0/16'
LOCAL = native_local_route(VPC_CIDR)
EXACT_NOT_FOUND = 'InvalidRouteTableID.NotFound'


def exact(client, table):
    result = client.describe_route_tables(RouteTableIds=[table])
    row, = result['RouteTables']
    assert row['RouteTableId'] == table and 'NextToken' not in result
    return row


def expect_route(gateway, destination=CIDR):
    return {'DestinationCidrBlock': destination, 'GatewayId': gateway, 'Origin': 'CreateRoute', 'State': 'active'}


def native_network(client, name):
    vpc = client.create_vpc(CidrBlock=VPC_CIDR)['Vpc']['VpcId']
    fresh_main_route_table(client, vpc, VPC_CIDR)
    tags = [{'ResourceType': 'route-table', 'Tags': [{'Key': 'Name', 'Value': name}]}]
    table = client.create_route_table(VpcId=vpc, TagSpecifications=tags)['RouteTable']['RouteTableId']
    gateway = client.create_internet_gateway()['InternetGateway']['InternetGatewayId']
    assert exact(client, table)['Routes'] == [LOCAL]
    client.attach_internet_gateway(InternetGatewayId=gateway, VpcId=vpc)
    return vpc, table, gateway


def erase_network(client, vpc, table, gateway):
    client.detach_internet_gateway(InternetGatewayId=gateway, VpcId=vpc)
    client.delete_internet_gateway(InternetGatewayId=gateway)
    client.delete_route_table(RouteTableId=table)
    client.delete_vpc(VpcId=vpc)
    reject(client.describe_route_tables, EXACT_NOT_FOUND, RouteTableIds=[table])


def check(binary):
    assert (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    with tempfile.TemporaryDirectory(prefix='native-default-route-') as td:
        path, second = Path(td)/'a.sqlite', Path(td)/'b.sqlite'
        with run_default(binary, state=second) as (pb, tb), ExitStack() as bx:
            b = sdk(bx, pb, tb)
            other_vpc, other_table, other_gateway = native_network(b, 'Separate team')
            other_baseline = exact(b, other_table)
            with run_default(binary, state=path) as (pa, ta), ExitStack() as ax:
                assert pb != pa and tb != ta
                c = sdk(ax, pa, ta)
                cfn = cfn_client(ax, pa, ta)
                stack = create_stack(cfn)
                resources = outputs(cfn, stack)
                original_cfn = exact(c, resources['Table'])
                assert original_cfn['Routes'] == [expect_route(resources['Gateway'])]
                vpc, table, gateway = native_network(c, 'Native & 練習')
                subnet = c.create_subnet(VpcId=vpc, CidrBlock='10.91.1.0/24')['Subnet']['SubnetId']
                assoc = c.associate_route_table(RouteTableId=table, SubnetId=subnet)['AssociationId']
                payload = {'RouteTableId': table, 'DestinationCidrBlock': CIDR}
                arn = f'arn:aws:ec2:{REGION}:000000000000:route-table/'
                allowed = [allow('DescribeRouteTables', '*'), allow('CreateRoute', arn+table), allow('DeleteRoute', arn+table)]
                r = sdk(ax, pa, grant(pa, ta, 'native-route-allow', allowed))
                wrong = sdk(ax, pa, grant(pa, ta, 'native-route-deny', [allowed[0], allow('CreateRoute', arn+other_table)]))
                reject(wrong.create_route, 'UnauthorizedOperation', GatewayId=gateway, **payload)
                reject(wrong.create_route, 'UnauthorizedOperation', DryRun=True, GatewayId=gateway, **payload)
                reject(r.create_route, 'DryRunOperation', DryRun=True, GatewayId=gateway, **payload)
                reject(r.delete_route, 'DryRunOperation', DryRun=True, **payload)
                reject(r.create_route, 'DryRunOperation', DryRun=True, GatewayId=gateway, RouteTableId=table,
                       DestinationCidrBlock='198.51.100.199/25')
                before = exact(r, table)
                assert before['Routes'] == [LOCAL] and before['Associations'][0]['RouteTableAssociationId'] == assoc
                reject(c.create_route, 'UnsupportedOperation', RouteTableId=resources['Table'], DestinationCidrBlock=CIDR, GatewayId=resources['Gateway'])
                reject(c.create_route, 'UnsupportedOperation', GatewayId=resources['Gateway'], **payload)
                reject(c.create_route, 'InvalidInternetGatewayID.NotFound', GatewayId=other_gateway, **payload)
                reject(b.describe_route_tables, EXACT_NOT_FOUND, RouteTableIds=[table])
                west = sdk(ax, pa, ta, REGIONS[1])
                reject(west.describe_route_tables, EXACT_NOT_FOUND, RouteTableIds=[table])
                reject(west.create_route, EXACT_NOT_FOUND, GatewayId=gateway, **payload)
                reject(c.delete_route, 'InvalidRoute.NotFound', **payload)
                for protected in ('10.91.0.0/16', '10.91.3.0/24', '10.90.0.0/15', '169.254.168.0/22', '169.254.169.1/32'):
                    reject(c.create_route, 'InvalidParameterValue', GatewayId=gateway, RouteTableId=table, DestinationCidrBlock=protected)
                assert r.create_route(RouteTableId=table, DestinationCidrBlock='192.0.2.123/0',
                                      GatewayId=gateway, DryRun=False)['ResponseMetadata']['HTTPStatusCode'] == 200
                external = {'GatewayId': gateway, 'RouteTableId': table}
                first, narrower = '198.51.100.0/24', '198.51.100.128/25'
                assert r.create_route(DestinationCidrBlock='198.51.100.199/25', **external)['ResponseMetadata']['HTTPStatusCode'] == 200
                assert r.create_route(DestinationCidrBlock='198.51.100.111/24', **external)['ResponseMetadata']['HTTPStatusCode'] == 200
                reject(c.create_route, 'RouteAlreadyExists', DestinationCidrBlock='198.51.100.151/24', **external)
                reject(c.delete_route, 'InvalidRoute.NotFound', RouteTableId=table, DestinationCidrBlock='198.51.100.151/24')
                saved = {**before, 'Routes': [expect_route(gateway, CIDR), LOCAL,
                                              expect_route(gateway, first), expect_route(gateway, narrower)]}
                assert exact(c, table) == exact(r, table) == saved
                assert r.describe_route_tables(Filters=[{'Name':'association.subnet-id','Values':[subnet]}])['RouteTables'] == [saved]
                assert c.describe_internet_gateways(InternetGatewayIds=[gateway])['InternetGateways'][0]['Attachments'] == [
                    {'State': 'available', 'VpcId': vpc},
                ]
                reject(c.create_route, 'RouteAlreadyExists', GatewayId=gateway, **payload)
                reject(c.replace_route, 'UnsupportedOperation', GatewayId=gateway, **payload)
                reject(wrong.delete_route, 'UnauthorizedOperation', **payload)
                reject(c.delete_route_table, 'DependencyViolation', RouteTableId=table)
                reject(c.detach_internet_gateway, 'DependencyViolation', InternetGatewayId=gateway, VpcId=vpc)
                reject(c.delete_route, 'InvalidRoute.NotFound', RouteTableId=table, DestinationCidrBlock='192.0.2.123/0')
                assert exact(c, table) == saved and exact(c, resources['Table']) == original_cfn
            with run_default(binary, state=path) as (pa, ta), ExitStack() as ax:
                c, cfn = sdk(ax, pa, ta), cfn_client(ax, pa, ta)
                assert exact(c, table) == saved and exact(c, resources['Table']) == original_cfn
                assert outputs(cfn, stack) == resources
                assert c.delete_route(**payload)['ResponseMetadata']['HTTPStatusCode'] == 200
                assert exact(c, table) == {**before, 'Routes': [LOCAL, expect_route(gateway, first), expect_route(gateway, narrower)]}
                reject(c.detach_internet_gateway, 'DependencyViolation', InternetGatewayId=gateway, VpcId=vpc)
                c.delete_route(RouteTableId=table, DestinationCidrBlock=first)
                assert exact(c, table) == {**before, 'Routes': [LOCAL, expect_route(gateway, narrower)]}
                c.delete_route(RouteTableId=table, DestinationCidrBlock=narrower)
                assert exact(c, table) == before
                c.disassociate_route_table(AssociationId=assoc)
                c.delete_subnet(SubnetId=subnet)
                erase_network(c, vpc, table, gateway)
                assert c.describe_route_tables()['RouteTables'] == [original_cfn]
                cfn.delete_stack(StackName=stack)
                assert c.describe_route_tables()['RouteTables'] == []
            with run_default(binary, state=path) as (pa, ta), ExitStack() as ax:
                c = sdk(ax, pa, ta)
                assert c.describe_route_tables()['RouteTables'] == []
                assert c.describe_internet_gateways()['InternetGateways'] == []
                assert c.describe_vpcs()['Vpcs'] == []
            assert exact(b, other_table) == other_baseline
            erase_network(b, other_vpc, other_table, other_gateway)
    print('PASS actual Boto3 1.42.0/Botocore 1.41.6: native IPv4 route set, canonical prefix independence, default compatibility, exact and subnet-filter reads, IAM, refusals, restart, team separation and cleanup.')


def console(binary):
    with tempfile.TemporaryDirectory(prefix='native-default-route-ui-') as td:
        with run_default(binary, state=Path(td)/'ui.sqlite') as (port, token), ExitStack() as life:
            c = sdk(life, port, token)
            vpc, table, gateway = native_network(c, 'Console same-state table')
            boot_hash = sha256((ROOT/'internal/server/web/app.js').read_bytes()).hexdigest()
            local = Local(port)
            cookie = local.session(token)
            connection = http.client.HTTPConnection('127.0.0.1', port, timeout=8)
            try:
                connection.request('GET', '/_simulator/docs/openapi.json', headers={'Cookie': cookie})
                response = connection.getresponse()
                assert response.status == 200
                document = response.read(1024 * 1024 + 1)
                assert len(document) <= 1024 * 1024
            finally:
                connection.close()
            ref = json.loads(document)['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']
            preset = ref['examples']['DescribeRouteTables']['value']
            assert preset == {'Action':'DescribeRouteTables','Version':'2016-11-15','MaxResults':100}
            assert preset['Action'] in ref['schema']['properties']['Action']['enum']
            assert 'RouteTableId.1' in ref['schema']['properties']
            assert 'CreateRoute' not in ref['examples'] and 'DeleteRoute' not in ref['examples']
            script = Path(__file__).with_name('ec2_native_default_route_dom.cjs')
            subprocess.run(['node', str(script)], check=True, text=True, timeout=90, input=json.dumps({
                'port': port, 'cookie': cookie, 'preset': preset, 'vpc': vpc, 'table': table, 'gateway': gateway, 'asset': boot_hash,
            }))
            assert exact(c, table)['Routes'] == [LOCAL]
            erase_network(c, vpc, table, gateway)
            assert c.describe_route_tables()['RouteTables'] == []


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    if sys.argv[1:] == ['--console']:
        console(binary)
    elif not sys.argv[1:]:
        check(binary)
    else:
        raise SystemExit('Expected no argument or --console')
