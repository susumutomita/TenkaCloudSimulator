"""Opt-in acceptance for real saved local EC2 route table associations.

Requires boto3 1.42.0/botocore 1.41.6 and the built simulator. All HTTP
and dummy signatures stay on numeric loopback; no traffic is routed.
"""
from contextlib import ExitStack
import json
import os
from pathlib import Path
import tempfile

from ec2_launch_preflight_native import Local, run_default
from ec2_native_network_sdk import REGION, boto3, botocore, reject, sdk

CAPABILITIES = '/_simulator/v1/capabilities'
ASSOCIATION_OPERATIONS = ['DescribeRouteTables', 'DescribeSubnets', 'AssociateRouteTable', 'DisassociateRouteTable']


def inventory(client):
    return {table['RouteTableId']: table for table in client.describe_route_tables()['RouteTables']}


def endpoint_token(local, operator, name, resources):
    headers = {'X-Simulator-Token': operator, 'Content-Type': 'application/json'}
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': ['ec2:DescribeRouteTables', 'ec2:DescribeSubnets'], 'Resource': '*'},
        {'Effect': 'Allow', 'Action': ['ec2:AssociateRouteTable', 'ec2:DisassociateRouteTable'], 'Resource': resources},
    ]}
    local.call('PUT', f'/_simulator/v1/iam/principals/{name}/policy', json.dumps(policy), **headers)
    _, body = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': name}), **headers)
    return json.loads(body)['AccessToken']


def check(binary):
    assert (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    with tempfile.TemporaryDirectory(prefix='native-route-association-db-') as directory:
        alpha, beta = Path(directory)/'team-a.sqlite', Path(directory)/'team-b.sqlite'
        with run_default(binary, state=beta) as (bp, bt), ExitStack() as bx:
            b = sdk(bx, bp, bt)
            bv = b.create_vpc(CidrBlock='10.67.0.0/16')['Vpc']['VpcId']
            bs = b.create_subnet(VpcId=bv, CidrBlock='10.67.1.0/24')['Subnet']['SubnetId']
            br = b.create_route_table(VpcId=bv)['RouteTable']['RouteTableId']
            b_before = inventory(b)
            with run_default(binary, state=alpha) as (ap, at), ExitStack() as ax:
                assert ap != bp and at != bt and alpha != beta
                local = Local(ap)
                local.call('GET', CAPABILITIES, status=401)
                _, caps = local.call('GET', CAPABILITIES, **{'X-Simulator-Token': at})
                ec2 = json.loads(caps)['services']['ec2']
                assert ec2['routeTableAssociationLifecycle'] == ASSOCIATION_OPERATIONS
                assert set(ASSOCIATION_OPERATIONS) <= set(ec2['operations'])
                assert 'native route tables' not in ec2['detachedConfigurationOnly']
                a = sdk(ax, ap, at)
                av = a.create_vpc(CidrBlock='10.67.0.0/16')['Vpc']['VpcId']
                asub = a.create_subnet(VpcId=av, CidrBlock='10.67.1.0/24')['Subnet']['SubnetId']
                ar = a.create_route_table(VpcId=av)['RouteTable']['RouteTableId']
                alternate = a.create_route_table(VpcId=av)['RouteTable']['RouteTableId']
                prefix = f'arn:aws:ec2:{REGION}:000000000000:'
                route_arn, subnet_arn = prefix+'route-table/'+ar, prefix+'subnet/'+asub
                editor = sdk(ax, ap, endpoint_token(local, at, 'route-editor', [route_arn, subnet_arn]))
                reader = sdk(ax, ap, endpoint_token(local, at, 'table-only', [route_arn]))
                pair = {'RouteTableId': ar, 'SubnetId': asub}
                before = inventory(a)
                assert len(before) == 2 and before[ar]['Associations'] == []
                reject(reader.associate_route_table, 'UnauthorizedOperation', **pair)
                assert inventory(editor) == inventory(a) == before
                reply = editor.associate_route_table(DryRun=False, **pair)
                assoc = reply['AssociationId']
                assert assoc.startswith('rtbassoc-') and reply['AssociationState']['State'] == 'associated'
                assert editor.associate_route_table(**pair)['AssociationId'] == assoc
                reject(a.associate_route_table, 'Resource.AlreadyAssociated', RouteTableId=alternate, SubnetId=asub)
                reject(reader.disassociate_route_table, 'UnauthorizedOperation', AssociationId=assoc)
                other_region = sdk(ax, ap, at, 'ap-northeast-1')
                reject(other_region.disassociate_route_table, 'InvalidAssociationID.NotFound', AssociationId=assoc)
                reject(b.disassociate_route_table, 'InvalidAssociationID.NotFound', AssociationId=assoc)
                reject(a.delete_route_table, 'DependencyViolation', RouteTableId=ar)
                reject(a.delete_subnet, 'DependencyViolation', SubnetId=asub)
                saved = inventory(editor)
                assert saved == inventory(a) and inventory(b) == b_before and not inventory(other_region)
                assert saved[alternate] == before[alternate] and saved[ar]['Routes'] == []
                assert saved[ar]['Associations'] == [
                    {'Main': False, 'RouteTableAssociationId': assoc, 'RouteTableId': ar, 'SubnetId': asub},
                ]
            with run_default(binary, state=alpha) as (ap, at), ExitStack() as ax:
                a = sdk(ax, ap, at)
                assert inventory(a) == saved and a.associate_route_table(**pair)['AssociationId'] == assoc
                response = a.disassociate_route_table(AssociationId=assoc)
                assert response['ResponseMetadata']['HTTPStatusCode'] == 200
                assert inventory(a) == before and inventory(b) == b_before
                a.delete_route_table(RouteTableId=alternate)
                a.delete_route_table(RouteTableId=ar)
                a.delete_subnet(SubnetId=asub)
                a.delete_vpc(VpcId=av)
                assert not inventory(a) and a.describe_subnets()['Subnets'] == [] and a.describe_vpcs()['Vpcs'] == []
            b.delete_route_table(RouteTableId=br)
            b.delete_subnet(SubnetId=bs)
            b.delete_vpc(VpcId=bv)
            assert not inventory(b) and b.describe_vpcs()['Vpcs'] == []
    print('PASS pinned EC2 SDK: explicit associate, regional Describe, replay and restart, exact grants, scope and dependencies, disassociate and cleanup. Metadata only; no forwarding.')


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    check(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
