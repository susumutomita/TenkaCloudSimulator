"""Pinned EC2/CFN Query using only disposable simulator processes on numeric loopback."""
from contextlib import ExitStack
from hashlib import sha256
import http.client
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import sys
import tempfile
from urllib.parse import urlencode
import xml.etree.ElementTree as ET

from ec2_launch_preflight_native import EC2, Local, REGIONS, run_default
from ec2_native_network_sdk import REGION, boto3, botocore, fresh_main_route_table, reject, sdk
from botocore.config import Config

ROOT = Path(__file__).resolve().parents[2]
OPERATIONS = ['DescribeInternetGateways', 'CreateInternetGateway', 'DeleteInternetGateway',
              'AttachInternetGateway', 'DetachInternetGateway']
ID_SHAPE = re.compile(r'igw-[0-9a-f]{17}')
NOT_FOUND = 'InvalidInternetGatewayID.NotFound'
CFN_NAME = 'internet-gateway-contract'
UNRELATED_BUCKET = 'igwattach-reports'


def cfn_client(lifetime, port, token):
    client = boto3.client('cloudformation', endpoint_url=f'http://127.0.0.1:{port}', region_name=REGION,
                          aws_access_key_id='TCSIM_IGW_CFN', aws_secret_access_key='synthetic-fixture-only',
                          config=Config(retries={'max_attempts': 0}, proxies={}, connect_timeout=3, read_timeout=10))
    client.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', token))
    lifetime.callback(client.close)
    return client


def create_stack(client):
    fixture = json.loads((ROOT / 'test/e2e/fixtures/ec2-network-contract.json').read_text())
    fixture['Resources']['Gateway'].setdefault('Properties', {})['Tags'] = [{'Key': 'Name', 'Value': 'Owned by stack'}]
    fixture['Outputs']['Gateway'] = {'Value': {'Ref': 'Gateway'}}
    fixture['Resources']['Reports'] = {'Type': 'AWS::S3::Bucket', 'Properties': {'BucketName': UNRELATED_BUCKET}}
    fixture['Outputs']['Reports'] = {'Value': {'Ref': 'Reports'}}
    return client.create_stack(StackName=CFN_NAME, TemplateBody=json.dumps(fixture))['StackId']


def outputs(client, stack):
    record, = client.describe_stacks(StackName=stack)['Stacks']
    assert record['StackStatus'] == 'CREATE_COMPLETE'
    return {item['OutputKey']: item['OutputValue'] for item in record['Outputs']}


def inventory(client):
    result = client.describe_internet_gateways()['InternetGateways']
    rows = {gateway['InternetGatewayId']: gateway for gateway in result}
    assert len(rows) == len(result)
    return rows


def grant(port, operator, name, statements):
    local = Local(port)
    auth = {'X-Simulator-Token': operator, 'Content-Type': 'application/json'}
    body = json.dumps({'Version': '2012-10-17', 'Statement': statements})
    local.call('PUT', f'/_simulator/v1/iam/principals/{name}/policy', body, **auth)
    _, issued = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': name}), **auth)
    return json.loads(issued)['AccessToken']


def allow(action, resource):
    return {'Effect': 'Allow', 'Action': 'ec2:' + action, 'Resource': resource}


def tags(name):
    return [{'ResourceType': 'internet-gateway', 'Tags': [{'Key': 'Name', 'Value': name}]}]


def sdk_check(binary):
    assert (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    assert REGIONS[0] == REGION
    with tempfile.TemporaryDirectory(prefix='native-igw-') as directory:
        one, two = Path(directory)/'team-a.sqlite', Path(directory)/'team-b.sqlite'
        with run_default(binary, state=two) as (bp, bt), ExitStack() as bx:
            other = sdk(bx, bp, bt)
            other_id = other.create_internet_gateway()['InternetGateway']['InternetGatewayId']
            other_before = inventory(other)
            other_vpc = other.create_vpc(CidrBlock='10.72.0.0/16')['Vpc']['VpcId']
            assert ID_SHAPE.fullmatch(other_id)
            with run_default(binary, state=one) as (ap, at), ExitStack() as ax:
                assert at != bt and ap != bp
                local = Local(ap)
                local.call('GET', '/_simulator/v1/capabilities', status=401)
                _, data = local.call('GET', '/_simulator/v1/capabilities', **{'X-Simulator-Token': at})
                spec = json.loads(data)['services']['ec2']
                assert all(action in spec['operations'] for action in OPERATIONS)
                assert spec['internetGatewayLifecycle'] == OPERATIONS
                assert spec['internetGatewayPageLimit'] == 100
                assert spec['describeSelectors']['DescribeInternetGateways'] == ['InternetGatewayId.N', 'MaxResults', 'NextToken']
                assert 'ReplaceRoute' not in spec['operations'] and 'RunInstances' not in spec['operations']
                a = sdk(ax, ap, at)
                cfn = cfn_client(ax, ap, at)
                assert a.describe_internet_gateways()['InternetGateways'] == []
                stack = create_stack(cfn)
                state = outputs(cfn, stack)
                g = state['Gateway']
                assert state['Reports'] == UNRELATED_BUCKET
                baseline = inventory(a)
                assert set(baseline) == {g}
                assert baseline[g]['Attachments'] == [{'State': 'available', 'VpcId': state['Vpc']}]
                assert baseline[g]['Tags'] == [{'Key': 'Name', 'Value': 'Owned by stack'}]
                routes = a.describe_route_tables()['RouteTables']
                assert len(routes) == 1 and routes[0]['Routes'][0]['GatewayId'] == g
                arn = f'arn:aws:ec2:{REGION}:000000000000:internet-gateway/'
                reader = allow('DescribeInternetGateways', '*')
                create_only = [reader, allow('CreateInternetGateway', arn+'*')]
                tags_and_create = create_only + [allow('CreateTags', arn+'*')]
                sparse = sdk(ax, ap, grant(ap, at, 'untagged-builder', create_only))
                editor = sdk(ax, ap, grant(ap, at, 'tagged-builder', tags_and_create))
                name = 'Native & 練習'
                reject(sparse.create_internet_gateway, 'UnauthorizedOperation', TagSpecifications=tags(name))
                reject(editor.create_internet_gateway, 'UnsupportedOperation', DryRun=True, TagSpecifications=tags(name))
                assert inventory(editor) == baseline
                made = editor.create_internet_gateway(DryRun=False, TagSpecifications=tags(name))['InternetGateway']
                native = made['InternetGatewayId']
                assert ID_SHAPE.fullmatch(native) and native != g and native != other_id
                assert made == {'InternetGatewayId': native, 'OwnerId': '000000000000',
                                'Attachments': [], 'Tags': [{'Key': 'Name', 'Value': name}]}
                saved = inventory(a)
                assert saved == inventory(editor) == {**baseline, native: made}
                exact = editor.describe_internet_gateways(InternetGatewayIds=[native, g])['InternetGateways']
                assert {row['InternetGatewayId']: row for row in exact} == saved
                different_region = sdk(ax, ap, at, REGIONS[1])
                assert inventory(different_region) == {}
                reject(different_region.describe_internet_gateways, NOT_FOUND, InternetGatewayIds=[native])
                reject(other.describe_internet_gateways, NOT_FOUND, InternetGatewayIds=[native])
                reject(editor.delete_internet_gateway, 'UnauthorizedOperation', InternetGatewayId=native)
                reject(a.delete_internet_gateway, 'UnsupportedOperation', InternetGatewayId=g)
                reject(a.attach_internet_gateway, 'UnsupportedOperation', InternetGatewayId=native, VpcId=state['Vpc'])
                reject(a.detach_internet_gateway, 'UnsupportedOperation', InternetGatewayId=g, VpcId=state['Vpc'])
                reject(a.describe_internet_gateways, 'UnsupportedOperation', Filters=[{'Name': 'attachment.vpc-id', 'Values': [state['Vpc']]}])
                small_page = a.describe_internet_gateways(MaxResults=5)
                assert 'NextToken' not in small_page
                assert {row['InternetGatewayId']: row for row in small_page['InternetGateways']} == saved
                reject(a.describe_internet_gateways, 'InvalidParameterCombination', MaxResults=5, InternetGatewayIds=[native])
                assert inventory(a) == saved and inventory(other) == other_before
                assert outputs(cfn, stack) == state and a.describe_route_tables()['RouteTables'] == routes

                # Actual AWS SDK operations, same native graph across the two
                # APIs; operator sets narrowly scoped rights on both ends.
                vpc = a.create_vpc(CidrBlock='10.73.0.0/16')['Vpc']['VpcId']
                main = fresh_main_route_table(a, vpc, '10.73.0.0/16')
                alternate_vpc = a.create_vpc(CidrBlock='10.74.0.0/16')['Vpc']['VpcId']
                alternate_main = fresh_main_route_table(a, alternate_vpc, '10.74.0.0/16')
                expected_routes = sorted([*routes, main, alternate_main], key=lambda r: r['RouteTableId'])
                extra = [a.create_internet_gateway()['InternetGateway']['InternetGatewayId'] for _ in range(4)]
                spare = extra[0]
                vpc_arn = f'arn:aws:ec2:{REGION}:000000000000:vpc/{vpc}'
                both_side = [allow(action, arn+native) for action in ('AttachInternetGateway', 'DetachInternetGateway')]
                lacks_vpc = sdk(ax, ap, grant(ap, at, 'gateway-side-only', [reader, *both_side]))
                both_side += [allow(action, vpc_arn) for action in ('AttachInternetGateway', 'DetachInternetGateway')]
                linked_actor = sdk(ax, ap, grant(ap, at, 'both-gateway-ends', [reader, *both_side]))
                pair = {'InternetGatewayId': native, 'VpcId': vpc}
                full_detached = inventory(a)
                previous_page = a.describe_internet_gateways(MaxResults=5)['NextToken']
                reject(lacks_vpc.attach_internet_gateway, 'UnauthorizedOperation', **pair)
                reject(lacks_vpc.attach_internet_gateway, 'UnauthorizedOperation', DryRun=True, **pair)
                reject(linked_actor.attach_internet_gateway, 'DryRunOperation', DryRun=True, **pair)
                reject(a.attach_internet_gateway, 'InvalidVpcID.NotFound', InternetGatewayId=native, VpcId=other_vpc)
                assert inventory(a) == full_detached
                assert linked_actor.attach_internet_gateway(DryRun=False, **pair)['ResponseMetadata']['HTTPStatusCode'] == 200
                reject(a.describe_internet_gateways, 'InvalidNextToken', NextToken=previous_page)
                attached = {**made, 'Attachments': [{'State': 'available', 'VpcId': vpc}]}
                persisted = {**full_detached, native: attached}
                assert inventory(a) == persisted == inventory(linked_actor)
                assert {item['VpcId'] for item in a.describe_vpcs()['Vpcs']} == {state['Vpc'], vpc, alternate_vpc}
                for gw, dest in ((native, vpc), (native, alternate_vpc), (spare, vpc)):
                    reject(a.attach_internet_gateway, 'Resource.AlreadyAssociated', InternetGatewayId=gw, VpcId=dest)
                reject(a.detach_internet_gateway, 'Gateway.NotAttached', InternetGatewayId=native, VpcId=alternate_vpc)
                reject(lacks_vpc.detach_internet_gateway, 'UnauthorizedOperation', **pair)
                reject(linked_actor.detach_internet_gateway, 'DryRunOperation', DryRun=True, **pair)
                reject(a.delete_vpc, 'DependencyViolation', VpcId=vpc)
                reject(a.delete_internet_gateway, 'DependencyViolation', InternetGatewayId=native)
                assert a.describe_internet_gateways(InternetGatewayIds=[native])['InternetGateways'] == [attached]
                assert inventory(a) == persisted and inventory(other) == other_before
                assert a.describe_route_tables()['RouteTables'] == expected_routes

            with run_default(binary, state=one) as (ap, at), ExitStack() as ax:
                a = sdk(ax, ap, at)
                cfn = cfn_client(ax, ap, at)
                assert inventory(a) == persisted and outputs(cfn, stack) == state
                assert a.describe_route_tables()['RouteTables'] == expected_routes
                assert a.describe_internet_gateways(InternetGatewayIds=[native])['InternetGateways'] == [attached]
                assert a.detach_internet_gateway(**pair)['ResponseMetadata']['HTTPStatusCode'] == 200
                assert inventory(a) == full_detached
                reject(a.detach_internet_gateway, 'Gateway.NotAttached', **pair)
                a.attach_internet_gateway(InternetGatewayId=native, VpcId=alternate_vpc)
                assert inventory(a)[native]['Attachments'] == [{'State': 'available', 'VpcId': alternate_vpc}]
                a.detach_internet_gateway(InternetGatewayId=native, VpcId=alternate_vpc)
                for parent in (vpc, alternate_vpc):
                    a.delete_vpc(VpcId=parent)
                assert a.describe_route_tables()['RouteTables'] == routes
                for extra_id in extra:
                    a.delete_internet_gateway(InternetGatewayId=extra_id)
                cleaner = sdk(ax, ap, grant(ap, at, 'one-gateway-cleaner', [reader, allow('DeleteInternetGateway', arn+native)]))
                reject(cleaner.delete_internet_gateway, 'UnauthorizedOperation', InternetGatewayId=g)
                reject(cleaner.delete_internet_gateway, 'UnsupportedOperation', InternetGatewayId=native, DryRun=True)
                assert inventory(cleaner) == saved
                assert cleaner.delete_internet_gateway(InternetGatewayId=native)['ResponseMetadata']['HTTPStatusCode'] == 200
                assert inventory(cleaner) == baseline
                reject(a.delete_internet_gateway, NOT_FOUND, InternetGatewayId=native)
                cfn.delete_stack(StackName=stack)
                assert not inventory(a) and not a.describe_route_tables()['RouteTables'] and not a.describe_vpcs()['Vpcs']
                assert not cfn.describe_stacks()['Stacks'] and inventory(other) == other_before
            other.delete_internet_gateway(InternetGatewayId=other_id)
            other.delete_vpc(VpcId=other_vpc)
            assert not inventory(other) and not other.describe_vpcs()['Vpcs']
    print('PASS pinned boto3 EC2/CFN: create, pair IAM and DryRun, one-to-one attach/detach and readback, dependent parent refusal, page fence, restart, two stores and cleanup.')


def check_reference(local, cookie, expected):
    mime = 'application/x-www-form-urlencoded'
    connection = http.client.HTTPConnection('127.0.0.1', local.port, timeout=8)
    try:
        connection.request('GET', '/_simulator/docs/openapi.json', headers={'Cookie': cookie})
        response = connection.getresponse()
        assert response.status == 200
        raw = response.read(1024 * 1024 + 1)
        assert len(raw) <= 1024 * 1024
        form = json.loads(raw)['paths']['/']['post']['requestBody']['content'][mime]
    finally:
        connection.close()
    preset = form['examples']['DescribeInternetGateways']['value']
    assert preset == {'Action': 'DescribeInternetGateways', 'Version': '2016-11-15', 'MaxResults': 100}
    assert preset['Action'] in form['schema']['properties']['Action']['enum']
    assert not any(action in form['examples'] for action in ('CreateInternetGateway', 'DeleteInternetGateway',
                                                                  'AttachInternetGateway', 'DetachInternetGateway'))
    _, wire = local.call('POST', '/', urlencode(preset), Cookie=cookie, **{
        'Content-Type': mime, 'X-Simulator-Service': 'ec2', 'X-Simulator-Region': REGION})
    result = ET.fromstring(wire)
    assert result.tag == EC2+'DescribeInternetGatewaysResponse'
    rows = result.findall(f'{EC2}internetGatewaySet/{EC2}item')
    assert [row.findtext(EC2+'internetGatewayId') for row in rows] == [expected['Gateway']]
    assert rows[0].findtext(f'{EC2}attachmentSet/{EC2}item/{EC2}vpcId') == expected['Vpc']


def console_check(binary):
    with run_default(binary) as (port, token), ExitStack() as lifetime:
        local = Local(port)
        client = cfn_client(lifetime, port, token)
        ec2 = sdk(lifetime, port, token)
        stack = create_stack(client)
        native_vpc = ec2.create_vpc(CidrBlock='10.75.0.0/16')['Vpc']['VpcId']
        try:
            state = outputs(client, stack)
            state['NativeVpc'] = native_vpc
            assert state['Reports'] == UNRELATED_BUCKET
            cookie = local.session(token)
            check_reference(local, cookie, state)
            script = Path(__file__).with_name('ec2_internet_gateway_dom.cjs')
            asset = sha256((ROOT / 'internal/server/web/app.js').read_bytes()).hexdigest()
            subprocess.run(['node', str(script)], check=True, text=True, timeout=50, input=json.dumps(
                {'port': port, 'cookie': cookie, 'state': state, 'asset': asset}))
            assert set(inventory(ec2)) == {state['Gateway']}
        finally:
            ec2.delete_vpc(VpcId=native_vpc)
            client.delete_stack(StackName=stack)
        assert not inventory(ec2)
    print('PASS served reference and console in JSDOM: create, owned-only attach/detach, exact and complete readback, delete. Not Chromium layout.')


def wide_inventory(path, exemplar):
    """Populate an offline test fixture by copying the actual native row schema.

    This sets up a lawful large inventory without hundreds of create requests.
    Only the disposable test DB is written directly. All reads and mutations
    asserted below go through the authenticated compiled simulator.
    """
    ids = [f'igw-{serial:017x}' for serial in range(1, 701)]
    pattern = ('"<&> rollout log & source ', "Audit & report > internal / 監査", 'Normal gateway operations: ')
    with sqlite3.connect(path) as db:
        raw, = db.execute('SELECT document FROM aws_ec2_network_resources WHERE id=?', (exemplar,)).fetchone()
        template = json.loads(raw)
        for serial, gw_id in enumerate(ids, 1):
            text = pattern[serial % len(pattern)]
            filled = (text * 260).encode('utf-8')
            labels = {f'Name': f'Fixture gateway {serial:03d}'}
            for index in range(49):
                tail = filled[:124-(index % 7)].decode('utf-8', 'ignore')
                value = filled[:256-(index % 13)].decode('utf-8', 'ignore')
                labels[f'{index:02d}-{tail}'] = value
            row = {**template, 'ID': gw_id, 'Tags': labels}
            db.execute('INSERT INTO aws_ec2_network_resources VALUES(?,?,?,?,?,?)',
                       (row['Region'], gw_id, row['StackID'], row['Kind'], json.dumps(row), row['Revision']))
    return ids


def unpaged_wire_bytes(port, token):
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=20)
    try:
        connection.request('POST', '/', 'Action=DescribeInternetGateways&Version=2016-11-15', headers={
            'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Token': token,
            'X-Simulator-Service': 'ec2', 'X-Simulator-Region': REGION})
        reply = connection.getresponse()
        assert reply.status == 200 and reply.headers.get_content_type() == 'text/xml'
        total = 0
        while chunk := reply.read(128*1024):
            total += len(chunk)
            assert total < 128*1024*1024
        return total
    finally:
        connection.close()


def paged_console_check(binary):
    with tempfile.TemporaryDirectory(prefix='legitimate-igw-capacity-') as directory:
        path = Path(directory)/'state.sqlite'
        with run_default(binary, state=path) as (port, token), ExitStack() as life:
            client = sdk(life, port, token)
            sample = client.create_internet_gateway(TagSpecifications=tags('Gateway exemplar'))['InternetGateway']['InternetGatewayId']
        ids = wide_inventory(path, sample)
        expected = set(ids) | {sample}
        with run_default(binary, state=path) as (port, token), ExitStack() as life:
            local, client = Local(port), sdk(life, port, token)
            legacy_bytes = unpaged_wire_bytes(port, token)
            assert legacy_bytes > 16*1024*1024, legacy_bytes

            def snapshot():
                saved = []
                last_id = ''
                for page in client.get_paginator('describe_internet_gateways').paginate(PaginationConfig={'PageSize': 1000}):
                    batch = page['InternetGateways']
                    assert 0 < len(batch) <= 100
                    for row in batch:
                        rid = row['InternetGatewayId']
                        assert rid > last_id and row['Attachments'] == []
                        last_id = rid
                        saved.append(rid)
                return set(saved)

            assert snapshot() == expected
            single = client.describe_internet_gateways(InternetGatewayIds=[ids[0]])['InternetGateways'][0]
            assert len(single['Tags']) == 50 and {'Key': 'Name', 'Value': 'Fixture gateway 001'} in single['Tags']
            prev = client.describe_internet_gateways(MaxResults=100)['NextToken']
            changed = client.create_internet_gateway(TagSpecifications=tags('Capacity refresh checkpoint'))['InternetGateway']['InternetGatewayId']
            reject(client.describe_internet_gateways, 'InvalidNextToken', NextToken=prev)
            client.delete_internet_gateway(InternetGatewayId=changed)

            script = Path(__file__).with_name('ec2_internet_gateway_dom.cjs')
            asset = sha256((ROOT/'internal/server/web/app.js').read_bytes()).hexdigest()
            subprocess.run(['node', str(script)], check=True, text=True, timeout=200, input=json.dumps({
                'port': port, 'cookie': local.session(token), 'state': None,
                'large': {'first': ids[0], 'second': ids[1], 'total': len(expected)}, 'asset': asset,
            }))
            actual = snapshot()
            new = actual - expected
            assert len(new) == 1 and expected - actual == set(ids[:2])
            added, = new
            assert client.describe_internet_gateways(InternetGatewayIds=[added])['InternetGateways'][0]['Tags'] == [
                {'Key': 'Name', 'Value': 'Created in a large region'},
            ]
            client.delete_internet_gateway(InternetGatewayId=added)
            assert snapshot() == expected - set(ids[:2])
            # The large synthetic inventory is scoped to the disposable DB,
            # which is deleted at context exit, not a host resource.
            print(f'PASS {len(expected)} legal gateways, {legacy_bytes} unpaged XML bytes, all SDK/console pages and actual write readbacks.')


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    if sys.argv[1:] == ['--console']:
        console_check(binary)
    elif sys.argv[1:] == ['--paged-console']:
        paged_console_check(binary)
    elif not sys.argv[1:]:
        sdk_check(binary)
    else:
        raise SystemExit('Expected no argument, --console or --paged-console')
