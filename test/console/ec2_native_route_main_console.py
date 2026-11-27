"""Opt-in real native EC2 SDK and main-route Cloudscape DOM proof.

SIMULATOR_BINARY and JSDOM_MODULE required. --source uses an in-memory
console build for development; the default verifies the exact served asset.
"""
from contextlib import ExitStack
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

from ec2_launch_preflight_native import Local, run_default
from ec2_native_network_sdk import boto3, botocore, fresh_main_route_table, native_local_route, sdk


CIDR = '10.190.0.0/16'
LOCAL = native_local_route(CIDR)
ROOT = Path(__file__).resolve().parents[2]


def view_token(local, token):
    actor = 'route-main-read-except-subnets'
    auth = {'X-Simulator-Token': token, 'Content-Type': 'application/json'}
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': ['ec2:DescribeVpcs', 'ec2:DescribeRouteTables'], 'Resource': '*'},
    ]}
    local.call('PUT', f'/_simulator/v1/iam/principals/{actor}/policy', json.dumps(policy), **auth)
    _, issue = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': actor}), **auth)
    return json.loads(issue)['AccessToken']


def by_subnet(client, subnet):
    return client.describe_route_tables(Filters=[
        {'Name': 'association.subnet-id', 'Values': [subnet]},
    ])['RouteTables']


def check(binary, source=False):
    assert __debug__ and (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    with tempfile.TemporaryDirectory(prefix='native-main-console-') as tmp:
        with run_default(binary, state=Path(tmp)/'state.sqlite') as (port, token), ExitStack() as life:
            local = Local(port)
            client = sdk(life, port, token)
            vpc = client.create_vpc(CidrBlock=CIDR)['Vpc']['VpcId']
            system = fresh_main_route_table(client, vpc, CIDR)
            main = system['RouteTableId']
            primary, = system['Associations']
            explicit, implicit = [client.create_subnet(VpcId=vpc, CidrBlock=f'10.190.{i}.0/24')['Subnet']['SubnetId']
                                  for i in (1, 2)]
            table = client.create_route_table(VpcId=vpc)['RouteTable']
            custom = table['RouteTableId']
            assert table['Routes'] == [LOCAL] and not table.get('Associations')
            association = client.associate_route_table(RouteTableId=custom, SubnetId=explicit)['AssociationId']
            assert not by_subnet(client, implicit)
            assert [r['RouteTableId'] for r in by_subnet(client, explicit)] == [custom]
            result = subprocess.run(['node', str(Path(__file__).with_name('ec2_native_route_main_dom.cjs'))],
                                    input=json.dumps({'port': port, 'cookie': local.session(token),
                                         'viewer': local.session(view_token(local, token)), 'vpc': vpc, 'main': main,
                                         'custom': custom, 'implicit': implicit, 'explicit': explicit,
                                         'association': association, 'source': source,
                                         'asset': sha256((ROOT/'internal/server/web/app.js').read_bytes()).hexdigest()}),
                                    text=True, check=True, timeout=70, stdout=subprocess.PIPE)
            receipt = json.loads(result.stdout)
            assert receipt == {'mode': 'in-memory source' if source else 'exact served asset',
                               'oneAcknowledgedWrite': True, 'implicitNotWire': True}
            assert not by_subnet(client, implicit)
            after, = by_subnet(client, explicit)
            assert after['RouteTableId'] == main and after['Routes'] == [LOCAL] and primary in after['Associations']
            moved, = [row for row in after['Associations'] if row.get('SubnetId') == explicit]
            assert moved['Main'] is False and moved['RouteTableAssociationId'] != association
            client.disassociate_route_table(AssociationId=moved['RouteTableAssociationId'])
            client.delete_route_table(RouteTableId=custom)
            client.delete_subnet(SubnetId=implicit)
            client.delete_subnet(SubnetId=explicit)
            client.delete_vpc(VpcId=vpc)
            assert not client.describe_route_tables()['RouteTables']
            assert not client.describe_subnets()['Subnets'] and not client.describe_vpcs()['Vpcs']
    print('PASS '+receipt['mode']+' Cloudscape (jsdom) and real pinned EC2 SDK: verified implicit fallback, '
          'protected system rows, exact explicit move/readback, IAM refusal, VPC cleanup; no browser or packet claims.')


if __name__ == '__main__':
    assert sys.argv[1:] in ([], ['--source'])
    check(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True), source=bool(sys.argv[1:]))
