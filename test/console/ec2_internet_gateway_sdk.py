"""Pinned EC2/CFN Query using only disposable simulator processes on numeric loopback."""
from contextlib import ExitStack
from hashlib import sha256
import http.client
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from urllib.parse import urlencode
import xml.etree.ElementTree as ET

from ec2_launch_preflight_native import EC2, Local, REGIONS, run_default
from ec2_native_network_sdk import REGION, boto3, botocore, reject, sdk
from botocore.config import Config

ROOT = Path(__file__).resolve().parents[2]
OPERATIONS = ['DescribeInternetGateways', 'CreateInternetGateway', 'DeleteInternetGateway']
ID_SHAPE = re.compile(r'igw-[0-9a-f]{17}')
NOT_FOUND = 'InvalidInternetGatewayID.NotFound'
CFN_NAME = 'internet-gateway-contract'


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
            assert ID_SHAPE.fullmatch(other_id)
            with run_default(binary, state=one) as (ap, at), ExitStack() as ax:
                assert at != bt and ap != bp
                local = Local(ap)
                local.call('GET', '/_simulator/v1/capabilities', status=401)
                _, data = local.call('GET', '/_simulator/v1/capabilities', **{'X-Simulator-Token': at})
                spec = json.loads(data)['services']['ec2']
                assert all(action in spec['operations'] for action in OPERATIONS)
                assert spec['internetGatewayLifecycle'] == OPERATIONS
                assert spec['describeSelectors']['DescribeInternetGateways'] == ['InternetGatewayId.N']
                assert not any(action in spec['operations'] for action in ('AttachInternetGateway', 'DetachInternetGateway', 'CreateRoute'))
                a = sdk(ax, ap, at)
                cfn = cfn_client(ax, ap, at)
                assert a.describe_internet_gateways()['InternetGateways'] == []
                stack = create_stack(cfn)
                state = outputs(cfn, stack)
                g = state['Gateway']
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
                reject(a.describe_internet_gateways, 'UnsupportedOperation', MaxResults=5)
                assert inventory(a) == saved and inventory(other) == other_before
                assert outputs(cfn, stack) == state and a.describe_route_tables()['RouteTables'] == routes

            with run_default(binary, state=one) as (ap, at), ExitStack() as ax:
                a = sdk(ax, ap, at)
                cfn = cfn_client(ax, ap, at)
                assert inventory(a) == saved and outputs(cfn, stack) == state
                assert a.describe_route_tables()['RouteTables'] == routes
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
            assert not inventory(other)
    print('PASS pinned boto3 EC2/CFN: detached create, tags, exact regional read, restart, local IAM, ownership, two databases and cleanup.')


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
    assert preset == {'Action': 'DescribeInternetGateways', 'Version': '2016-11-15'}
    assert preset['Action'] in form['schema']['properties']['Action']['enum']
    assert not any(action in form['examples'] for action in ('CreateInternetGateway', 'DeleteInternetGateway'))
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
        try:
            state = outputs(client, stack)
            cookie = local.session(token)
            check_reference(local, cookie, state)
            script = Path(__file__).with_name('ec2_internet_gateway_dom.cjs')
            asset = sha256((ROOT / 'internal/server/web/app.js').read_bytes()).hexdigest()
            subprocess.run(['node', str(script)], check=True, text=True, timeout=50, input=json.dumps(
                {'port': port, 'cookie': cookie, 'state': state, 'asset': asset}))
            assert set(inventory(ec2)) == {state['Gateway']}
        finally:
            client.delete_stack(StackName=stack)
        assert not inventory(ec2)
    print('PASS served reference Query replay and console in JSDOM: owner readback, named create, state verification and exact delete confirmation. Not Chromium layout.')


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    if sys.argv[1:] == ['--console']:
        console_check(binary)
    elif not sys.argv[1:]:
        sdk_check(binary)
    else:
        raise SystemExit('Expected no argument or --console')
