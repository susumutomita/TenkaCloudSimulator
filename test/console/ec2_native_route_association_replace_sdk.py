"""Opt-in saved association replacement: pinned local SDK, optional served JSDOM.

No AWS endpoint, forwarding, guest, CI browser workflow, or real credentials.
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
from ec2_native_network_sdk import REGION, boto3, botocore, reject, sdk
from ec2_route_table_pages_sdk import create_stack, rows, sdk_pages

ACTION = 'ReplaceRouteTableAssociation'
ROOT = Path(__file__).resolve().parents[2]


def inventory(c):
    return {r['RouteTableId']: r for r in rows(sdk_pages(c))}


def replacement_token(local, operator, name, resources):
    headers = {'X-Simulator-Token': operator, 'Content-Type': 'application/json'}
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': 'ec2:DescribeRouteTables', 'Resource': '*'},
        {'Effect': 'Allow', 'Action': 'ec2:'+ACTION, 'Resource': resources},
    ]}
    local.call('PUT', f'/_simulator/v1/iam/principals/{name}/policy', json.dumps(policy), **headers)
    _, issue = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': name}), **headers)
    return json.loads(issue)['AccessToken']


def fixture(c):
    vpc = c.create_vpc(CidrBlock='10.68.0.0/16')['Vpc']['VpcId']
    subnets = [c.create_subnet(VpcId=vpc, CidrBlock=f'10.68.{i}.0/24')['Subnet']['SubnetId']
               for i in (1, 2, 3)]
    tables = [c.create_route_table(VpcId=vpc, TagSpecifications=[{
        'ResourceType': 'route-table', 'Tags': [{'Key': 'Name', 'Value': name}],
    }])['RouteTable']['RouteTableId'] for name in ('Source route table', 'Destination route table')]
    association = [c.associate_route_table(SubnetId=s, RouteTableId=t)['AssociationId']
                   for s, t in zip(subnets, (tables[0], tables[0], tables[1]))]
    gateway = c.create_internet_gateway()['InternetGateway']['InternetGatewayId']
    c.attach_internet_gateway(InternetGatewayId=gateway, VpcId=vpc)
    for t, cidr in zip(tables, ('192.0.2.0/24', '198.51.100.0/24')):
        assert c.create_route(RouteTableId=t, DestinationCidrBlock=cidr, GatewayId=gateway)['Return']
    return vpc, subnets, tables, association, gateway


def reconcile(c, before, tables, subnet, old, new):
    source, destination = tables
    found = inventory(c)
    assert found.keys() == before.keys() and new != old and new.startswith('rtbassoc-')
    after_source = [a for a in before[source]['Associations'] if a['RouteTableAssociationId'] != old]
    after_dest = [*before[destination]['Associations'], {
        'Main': False, 'RouteTableAssociationId': new, 'RouteTableId': destination, 'SubnetId': subnet,
    }]
    assert found == {**before, source: {**before[source], 'Associations': after_source},
                     destination: {**before[destination], 'Associations': sorted(
                         after_dest, key=lambda a: a['RouteTableAssociationId'])}}
    for table in (source, destination):
        assert c.describe_route_tables(RouteTableIds=[table])['RouteTables'] == [found[table]]
    selection = [{'Name': 'association.subnet-id', 'Values': [subnet]}]
    assert rows(sdk_pages(c, Filters=selection)) == [found[destination]]
    return found


def cleanup(c, vpc, subnets, tables, gateway):
    wanted = inventory(c)
    for table in tables:
        for a in wanted[table]['Associations']:
            c.disassociate_route_table(AssociationId=a['RouteTableAssociationId'])
        for r in wanted[table]['Routes']:
            c.delete_route(RouteTableId=table, DestinationCidrBlock=r['DestinationCidrBlock'])
        c.delete_route_table(RouteTableId=table)
    c.detach_internet_gateway(InternetGatewayId=gateway, VpcId=vpc)
    c.delete_internet_gateway(InternetGatewayId=gateway)
    for subnet in subnets:
        c.delete_subnet(SubnetId=subnet)
    c.delete_vpc(VpcId=vpc)


def check(binary):
    with tempfile.TemporaryDirectory(prefix='native-route-replacement-') as td:
        alpha, beta = Path(td)/'a.sqlite', Path(td)/'b.sqlite'
        with run_default(binary, state=beta) as (bp, bt), ExitStack() as life_b:
            b = sdk(life_b, bp, bt)
            vpc_b, subnets_b, tables_b, _, gateway_b = fixture(b)
            before_b = inventory(b)
            with run_default(binary, state=alpha) as (port, token), ExitStack() as life:
                assert port != bp and token != bt and alpha != beta
                local = Local(port)
                cookie = local.session(token)
                a = sdk(life, port, token)
                _, cap_body = local.call('GET', '/_simulator/v1/capabilities', Cookie=cookie)
                caps = json.loads(cap_body)['services']['ec2']
                assert caps['routeTableAssociationReplacement'] == ACTION and ACTION in caps['operations']
                cfn, cfn_outputs = create_stack(local, cookie)
                vpc, subnets, tables, ids, gateway = fixture(a)
                before = inventory(a)
                arn = f'arn:aws:ec2:{REGION}:000000000000:'
                resources = [arn+'route-table/'+t for t in tables] + [arn+'subnet/'+subnets[0]]
                editor = sdk(life, port, replacement_token(local, token, 'route-replacer', resources))
                limited = sdk(life, port, replacement_token(local, token, 'route-source-only', resources[::2]))
                far = sdk(life, port, token, 'ap-northeast-1')
                pair = {'AssociationId': ids[0], 'RouteTableId': tables[1]}
                reject(limited.replace_route_table_association, 'UnauthorizedOperation', **pair)
                reject(limited.replace_route_table_association, 'UnauthorizedOperation', DryRun=True, **pair)
                reject(editor.replace_route_table_association, 'DryRunOperation', DryRun=True, **pair)
                reject(a.replace_route_table_association, 'UnsupportedOperation',
                       AssociationId=ids[0], RouteTableId=cfn_outputs['Table'])
                cfn_id = before[cfn_outputs['Table']]['Associations'][0]['RouteTableAssociationId']
                reject(a.replace_route_table_association, 'InvalidAssociationID.NotFound',
                       AssociationId=cfn_id, RouteTableId=tables[1])
                reject(far.replace_route_table_association, 'InvalidAssociationID.NotFound', **pair)
                reject(b.replace_route_table_association, 'InvalidAssociationID.NotFound', **pair)
                assert inventory(a) == inventory(editor) == before and inventory(b) == before_b and not inventory(far)
                reply = editor.replace_route_table_association(DryRun=False, **pair)
                new = reply['NewAssociationId']
                assert reply['ResponseMetadata']['HTTPStatusCode'] == 200
                assert reply['AssociationState'] == {'State': 'associated'}
                saved = reconcile(editor, before, tables, subnets[0], ids[0], new)
                reject(editor.replace_route_table_association, 'UnauthorizedOperation', **pair)
                reject(a.replace_route_table_association, 'InvalidAssociationID.NotFound', **pair)
                reject(a.disassociate_route_table, 'InvalidAssociationID.NotFound', AssociationId=ids[0])
                assert inventory(a) == saved and inventory(b) == before_b
            with run_default(binary, state=alpha) as (port, token), ExitStack() as life:
                a = sdk(life, port, token)
                assert reconcile(a, before, tables, subnets[0], ids[0], new) == saved
                reject(a.delete_route_table, 'DependencyViolation', RouteTableId=tables[1])
                reject(a.delete_subnet, 'DependencyViolation', SubnetId=subnets[0])
                reject(a.disassociate_route_table, 'InvalidAssociationID.NotFound', AssociationId=ids[0])
                cleanup(a, vpc, subnets, tables, gateway)
                local = Local(port)
                local.query('cloudformation', 'DeleteStack', local.session(token), StackName=cfn)
                assert not inventory(a) and not a.describe_vpcs()['Vpcs']
                assert not a.describe_subnets()['Subnets'] and not a.describe_internet_gateways()['InternetGateways']
            assert inventory(b) == before_b
            cleanup(b, vpc_b, subnets_b, tables_b, gateway_b)
            assert not inventory(b) and not b.describe_vpcs()['Vpcs']
    print('PASS pinned EC2 SDK: atomic explicit replacement, fresh/retired IDs, retained native routes, siblings and '
          'CFN, exact IAM, permission-only DryRun, region/team isolation, persisted restart, dependency-safe cleanup.')


def console(binary):
    with run_default(binary) as (port, token), ExitStack() as life:
        local = Local(port)
        cookie = local.session(token)
        c = sdk(life, port, token)
        vpc, subnets, tables, association, gateway = fixture(c)
        cfn, legacy = create_stack(local, cookie)
        asset = sha256((ROOT/'internal/server/web/app.js').read_bytes()).hexdigest()
        script = Path(__file__).with_name('ec2_native_route_association_replace_dom.cjs')
        proc = subprocess.run(['node', str(script)], text=True, check=True, timeout=85, stdout=subprocess.PIPE,
                              input=json.dumps({'port': port, 'cookie': cookie, 'vpc': vpc, 'subnets': subnets,
                                                'tables': tables, 'ids': association, 'legacy': legacy, 'asset': asset}))
        result = json.loads(proc.stdout)
        final = inventory(c)
        assert result['confirmed'] != association[0] and result['movedBack'] not in [*association, result['confirmed']]
        assert {a['RouteTableAssociationId'] for a in final[tables[0]]['Associations']} == {
            association[1], result['movedBack']}
        assert {a['RouteTableAssociationId'] for a in final[tables[1]]['Associations']} == {association[2]}
        assert final[legacy['Table']]['Associations'][0]['SubnetId'] == legacy['Subnet']
        cleanup(c, vpc, subnets, tables, gateway)
        local.query('cloudformation', 'DeleteStack', cookie, StackName=cfn)
        assert not inventory(c) and not c.describe_vpcs()['Vpcs']
    print('PASS served English/Japanese Cloudscape JSDOM: selected same-VPC targets, held complete readback, '
          'incomplete acknowledgement unconfirmed, refresh without replay, read-only reference and SDK cleanup.')


if __name__ == '__main__':
    assert __debug__ and (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    if sys.argv[1:] == ['--console']:
        console(binary)
    else:
        assert not sys.argv[1:]
        check(binary)
