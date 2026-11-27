"""Opt-in real authenticated simulator + pinned EC2 SDK; no guest or real AWS."""
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
import json
import os
from pathlib import Path
import re
import tempfile

os.environ['AWS_CONFIG_FILE'] = os.environ['AWS_SHARED_CREDENTIALS_FILE'] = '/dev/null'
os.environ['AWS_EC2_METADATA_DISABLED'] = 'true'
import boto3
import botocore
from botocore.config import Config
from botocore.exceptions import ClientError
from ec2_launch_preflight_native import Local, run_default

REGION = 'us-east-1'


def sdk(stack, port, token, region=REGION):
    assert 0 < port < 65536
    c = boto3.client('ec2', endpoint_url=f'http://127.0.0.1:{port}', region_name=region,
                     aws_access_key_id='TCSIM_NATIVE_CHILDREN', aws_secret_access_key='synthetic-fixture-only',
                     config=Config(retries={'max_attempts': 0}, proxies={}, connect_timeout=3, read_timeout=10))
    c.meta.events.register('before-sign.ec2', lambda request, **_: request.headers.add_header('X-Simulator-Token', token))
    stack.callback(c.close)
    return c


def state(c):
    return (c.describe_subnets()['Subnets'], c.describe_security_groups()['SecurityGroups'],
            c.describe_network_acls()['NetworkAcls'])


def native_local_route(cidr):
    return {'DestinationCidrBlock': cidr, 'GatewayId': 'local', 'Origin': 'CreateRouteTable', 'State': 'active'}


def fresh_main_route_table(client, vpc, cidr):
    row, = [table for table in client.describe_route_tables()['RouteTables'] if table['VpcId'] == vpc]
    assert re.fullmatch(r'rtb-[0-9a-f]{17}', row['RouteTableId'])
    assert row['Routes'] == [native_local_route(cidr)] and row['Tags'] == []
    main, = row['Associations']
    assert re.fullmatch(r'rtbassoc-[0-9a-f]{17}', main['RouteTableAssociationId'])
    assert main == {'Main': True, 'RouteTableId': row['RouteTableId'],
                    'RouteTableAssociationId': main['RouteTableAssociationId'],
                    'AssociationState': {'State': 'associated'}}
    assert client.describe_route_tables(RouteTableIds=[row['RouteTableId']])['RouteTables'] == [row]
    return row


def reject(operation, code, **fields):
    try:
        operation(**fields)
    except ClientError as e:
        assert e.response['Error']['Code'] == code, (operation.__name__, e.response['Error']['Code'])
    else:
        raise AssertionError(f'{operation.__name__}: expected {code}')


def participant(port, token, parent):
    local = Local(port)
    principal = 'network-builder'
    prefix = f'arn:aws:ec2:{REGION}:000000000000:'
    actions = {'CreateSubnet': 'subnet', 'CreateSecurityGroup': 'security-group', 'CreateNetworkAcl': 'network-acl'}
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': f'ec2:{action}', 'Resource': [prefix+'vpc/'+parent, prefix+kind+'/*']}
        for action, kind in actions.items()
    ] + [{'Effect': 'Allow', 'Action': ['ec2:DescribeSubnets', 'ec2:DescribeSecurityGroups', 'ec2:DescribeNetworkAcls'], 'Resource': '*'}]}
    headers = {'X-Simulator-Token': token, 'Content-Type': 'application/json'}
    local.call('PUT', f'/_simulator/v1/iam/principals/{principal}/policy', json.dumps(policy), **headers)
    _, issued = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': principal}), **headers)
    return json.loads(issued)['AccessToken']


def check(binary):
    assert boto3.__version__ == '1.42.0' and botocore.__version__ == '1.41.6'
    with tempfile.TemporaryDirectory(prefix='native-network-db-') as td:
        alpha, beta = Path(td)/'team-a.sqlite', Path(td)/'team-b.sqlite'
        with run_default(binary, state=beta) as (bp, bt), ExitStack() as bx:
            b = sdk(bx, bp, bt)
            bv = b.create_vpc(CidrBlock='10.87.0.0/16')['Vpc']['VpcId']
            bm = fresh_main_route_table(b, bv, '10.87.0.0/16')
            assert state(b) == ([], [], [])
            with run_default(binary, state=alpha) as (ap, at), ExitStack() as ax:
                assert ap != bp and at != bt and alpha != beta
                Local(ap).call('GET', '/_simulator/health', status=401, **{'X-Simulator-Token': bt})
                Local(bp).call('GET', '/_simulator/health', status=401, **{'X-Simulator-Token': at})
                a = sdk(ax, ap, at)
                parent = a.create_vpc(CidrBlock='10.87.0.0/16')['Vpc']['VpcId']
                assert parent != bv
                am = fresh_main_route_table(a, parent, '10.87.0.0/16')
                assert am['RouteTableId'] != bm['RouteTableId']
                builder = sdk(ax, ap, participant(ap, at, parent))
                with ThreadPoolExecutor(max_workers=3) as sends:
                    sf = sends.submit(builder.create_subnet, VpcId=parent, CidrBlock='10.87.1.24/24', AvailabilityZone=REGION+'b')
                    gf = sends.submit(builder.create_security_group, VpcId=parent, GroupName='app-workers', Description='Detached SG metadata')
                    af = sends.submit(builder.create_network_acl, VpcId=parent)
                    subnet = sf.result()['Subnet']
                    group = gf.result()['GroupId']
                    reply = af.result()
                acl, stable = reply['NetworkAcl'], reply['ClientToken']
                assert subnet['VpcId']==parent and subnet['CidrBlock']=='10.87.1.0/24' and subnet['AvailabilityZone']==REGION+'b'
                assert acl['VpcId']==parent and not acl['IsDefault'] and acl['Associations']==[]
                assert stable and acl['NetworkAclId'].startswith('acl-')
                subnet_id, acl_id = subnet['SubnetId'], acl['NetworkAclId']
                saved = state(builder)
                assert saved == state(a) and all(len(rows)==1 for rows in saved)
                assert saved[0][0]['SubnetId']==subnet_id
                assert saved[1][0]['GroupId']==group and saved[1][0]['GroupName']=='app-workers'
                assert saved[1][0]['IpPermissions']==[] and saved[2]==[acl]
                assert {v['VpcId'] for v in a.describe_vpcs()['Vpcs']}=={parent}
                assert a.describe_route_tables()['RouteTables']==[am] and b.describe_route_tables()['RouteTables']==[bm]
                selector = [{'Name':'vpc-id','Values':[parent]}]
                assert a.describe_security_groups(Filters=selector, GroupIds=[group])['SecurityGroups']==saved[1]
                assert a.describe_network_acls(Filters=selector, NetworkAclIds=[acl_id])['NetworkAcls']==saved[2]
                for key,call,id in (('SubnetId',builder.delete_subnet,subnet_id),
                                    ('GroupId',builder.delete_security_group,group),
                                    ('NetworkAclId',builder.delete_network_acl,acl_id)):
                    reject(call,'UnauthorizedOperation',**{key:id})
                reject(a.delete_vpc,'DependencyViolation',VpcId=parent)
                reject(b.delete_subnet,'InvalidSubnetID.NotFound',SubnetId=subnet_id)
                reject(b.delete_security_group,'InvalidGroup.NotFound',GroupId=group)
                reject(b.delete_network_acl,'InvalidNetworkAclID.NotFound',NetworkAclId=acl_id)
                far = sdk(ax,ap,at,'ap-northeast-1')
                assert state(b)==state(far)==([],[],[]) and far.describe_route_tables()['RouteTables']==[]
                assert state(a)==saved
            with run_default(binary,state=alpha) as (ap,at), ExitStack() as ax:
                a = sdk(ax,ap,at)
                assert state(a) == saved and a.describe_route_tables()['RouteTables']==[am]
                assert a.create_network_acl(VpcId=parent,ClientToken=stable)['NetworkAcl']==acl
                a.delete_subnet(SubnetId=subnet_id)
                assert state(a)==([],saved[1],saved[2])
                reject(a.delete_vpc,'DependencyViolation',VpcId=parent)
                a.delete_security_group(GroupId=group)
                assert state(a)==([],[],saved[2])
                reject(a.delete_vpc,'DependencyViolation',VpcId=parent)
                a.delete_network_acl(NetworkAclId=acl_id)
                reject(a.create_network_acl,'InvalidNetworkAclID.NotFound',VpcId=parent,ClientToken=stable)
                assert state(a)==([],[],[])
                a.delete_vpc(VpcId=parent)
                assert not a.describe_vpcs()['Vpcs'] and not a.describe_route_tables()['RouteTables']
            b.delete_vpc(VpcId=bv)
            assert not b.describe_vpcs()['Vpcs'] and state(b)==([],[],[]) and not b.describe_route_tables()['RouteTables']
    print('PASS exact native EC2 SDK: three concurrent child creates in one VPC, isolated reads, denied mutations, restart replay, per-child delete, parent dependencies and cleanup. Two local team DBs/tokens; no network attachment or guest.')


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    check(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
