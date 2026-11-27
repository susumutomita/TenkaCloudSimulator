"""Opt-in pinned EC2 SDK + served read-only API Reference, numeric loopback only."""
from contextlib import ExitStack, closing
import http.client
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlencode
import xml.etree.ElementTree as ET

from ec2_launch_preflight_native import CFN, EC2, Local, run_default
from ec2_native_network_sdk import REGION, boto3, botocore, fresh_main_route_table, native_local_route, reject, sdk

MIME = 'application/x-www-form-urlencoded'
PAGE_BYTES = 16 * 1024 * 1024
EXACT_BYTES = 512 * 1024


def reader_token(local, operator, name):
    headers = {'X-Simulator-Token': operator, 'Content-Type': 'application/json'}
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': 'ec2:DescribeRouteTables', 'Resource': '*'},
    ]}
    local.call('PUT', f'/_simulator/v1/iam/principals/{name}/policy', json.dumps(policy), **headers)
    _, raw = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': name}), **headers)
    return json.loads(raw)['AccessToken']


def create_stack(local, cookie):
    template = {'Resources': {
        'Vpc': {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.72.0.0/16'}},
        'Subnet': {'Type': 'AWS::EC2::Subnet', 'Properties': {'VpcId': {'Ref': 'Vpc'}, 'CidrBlock': '10.72.1.0/24'}},
        'Table': {'Type': 'AWS::EC2::RouteTable', 'Properties': {'VpcId': {'Ref': 'Vpc'}}},
        'Association': {'Type': 'AWS::EC2::SubnetRouteTableAssociation', 'Properties': {
            'RouteTableId': {'Ref': 'Table'}, 'SubnetId': {'Ref': 'Subnet'}}},
    }, 'Outputs': {name: {'Value': {'Ref': name}} for name in ('Vpc', 'Table', 'Subnet')}}
    created = local.query('cloudformation', 'CreateStack', cookie, StackName='route-page-contract',
                          TemplateBody=json.dumps(template))
    stack = created.findtext(CFN + 'CreateStackResult/' + CFN + 'StackId')
    assert stack
    found = local.query('cloudformation', 'DescribeStacks', cookie, StackName=stack)
    row = found.find(CFN + 'DescribeStacksResult/' + CFN + 'Stacks/' + CFN + 'member')
    assert row.findtext(CFN + 'StackStatus') == 'CREATE_COMPLETE'
    outputs = {item.findtext(CFN + 'OutputKey'): item.findtext(CFN + 'OutputValue')
               for item in row.findall(CFN + 'Outputs/' + CFN + 'member')}
    return stack, outputs


def rows(pages):
    return [row for page in pages for row in page['RouteTables']]


def sdk_pages(client, **selection):
    return list(client.get_paginator('describe_route_tables').paginate(
        PaginationConfig={'PageSize': 100}, **selection))


def scalar_pages(port, cookie, exact):
    with closing(http.client.HTTPConnection('127.0.0.1', port, timeout=20)) as connection:
        connection.request('GET', '/_simulator/docs/openapi.json', headers={'Cookie': cookie})
        doc = connection.getresponse()
        assert doc.status == 200
        raw_doc = doc.read(1024 * 1024 + 1)
        assert len(raw_doc) <= 1024 * 1024
        forms = json.loads(raw_doc)['paths']['/']['post']['requestBody']['content'][MIME]
        preset = forms['examples']['DescribeRouteTables']['value']
        assert preset == {'Action': 'DescribeRouteTables', 'Version': '2016-11-15', 'MaxResults': 100}
        assert 'DescribeRouteTables' in forms['schema']['properties']['Action']['enum']
        assert 'SimpleDB Select' in forms['schema']['properties']['NextToken']['description']
        assert not any(k in forms['examples'] for k in ('CreateRoute', 'DeleteRoute', 'ReplaceRoute'))

        def send(request, limit):
            connection.request('POST', '/', urlencode(request), headers={
                'Cookie': cookie, 'Content-Type': MIME, 'X-Simulator-Service': 'ec2', 'X-Simulator-Region': REGION,
            })
            response = connection.getresponse()
            assert response.status == 200 and response.headers.get_content_type() == 'text/xml'
            raw = response.read(limit + 1)
            assert len(raw) <= limit
            view = ET.fromstring(raw)
            assert view.tag == EC2 + 'DescribeRouteTablesResponse'
            return view, len(raw)

        first, first_bytes = send(preset, PAGE_BYTES)
        token = first.findtext(EC2 + 'nextToken')
        assert token and len(token) == 48
        tail, tail_bytes = send({**preset, 'NextToken': token}, PAGE_BYTES)
        assert tail.find(EC2 + 'nextToken') is None
        all_ids = [node.findtext(EC2+'routeTableId') for page in (first, tail)
                   for node in page.findall(EC2+'routeTableSet/'+EC2+'item')]
        one = {key: value for key, value in preset.items() if key != 'MaxResults'}
        selected, exact_bytes = send({**one, 'RouteTableId.1': exact}, EXACT_BYTES)
        selected_row, = selected.findall(EC2+'routeTableSet/'+EC2+'item')
        assert selected_row.findtext(EC2 + 'routeTableId') == exact
        assert selected.find(EC2 + 'nextToken') is None
        print(f'PASS served authenticated reference example: two pages {first_bytes}/{tail_bytes} bytes, '
              f'exact {exact_bytes} bytes, same read-only endpoint.', flush=True)
        return all_ids


def check(binary):
    assert (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    with tempfile.TemporaryDirectory(prefix='route-pages-contract-') as tmp:
        state = Path(tmp) / 'inventory.sqlite'
        with run_default(binary, state=state) as (port, operator), ExitStack() as stack:
            local = Local(port)
            cookie = local.session(operator)
            client = sdk(stack, port, operator)
            cidr = '10.71.0.0/16'
            vpc = client.create_vpc(CidrBlock=cidr)['Vpc']['VpcId']
            main = fresh_main_route_table(client, vpc, cidr)
            cfn, stack_out = create_stack(local, cookie)
            tags = [{'Key': f'{n:02d}'+chr(34)*126, 'Value': chr(34)*256} for n in range(50)]
            spec = [{'ResourceType': 'route-table', 'Tags': tags}]
            native = [client.create_route_table(VpcId=vpc, TagSpecifications=spec)['RouteTable']['RouteTableId']
                      for _ in range(180)]
            subnets, associations = [], []
            for n, table in enumerate(native[:6], 1):
                subnet = client.create_subnet(VpcId=vpc, CidrBlock=f'10.71.{n}.0/24')['Subnet']['SubnetId']
                subnets.append(subnet)
                associations.append(client.associate_route_table(RouteTableId=table, SubnetId=subnet)['AssociationId'])
            known = sorted([*native, main['RouteTableId'], stack_out['Table']])
            lengths = []
            client.meta.events.register('after-call.ec2.DescribeRouteTables',
                                        lambda http_response, **_: lengths.append(len(http_response.content)))
            unpaged = client.describe_route_tables()
            assert not unpaged.get('NextToken') and lengths[-1] > PAGE_BYTES
            assert [row['RouteTableId'] for row in unpaged['RouteTables']] == known

            pages = sdk_pages(client)
            assert [len(p['RouteTables']) for p in pages] == [100, 82]
            assert pages[0].get('NextToken') and not pages[1].get('NextToken')
            assert max(lengths[-2:]) < PAGE_BYTES and rows(pages) == unpaged['RouteTables'] and main in rows(pages)
            exact = client.describe_route_tables(RouteTableIds=[native[-1]])
            assert exact['RouteTables'] == [row for row in rows(pages) if row['RouteTableId'] == native[-1]]
            assert not exact.get('NextToken') and lengths[-1] < EXACT_BYTES
            assert exact['RouteTables'][0]['Tags'] == tags and exact['RouteTables'][0]['Routes'] == [native_local_route(cidr)]
            assert scalar_pages(port, cookie, native[-1]) == known
            r1 = sdk(stack, port, reader_token(local, operator, 'route-page-reader'))
            r2 = sdk(stack, port, reader_token(local, operator, 'route-page-other'))
            selector = [{'Name': 'association.subnet-id', 'Values': [*subnets, stack_out['Subnet']]}]
            filtered = list(r1.get_paginator('describe_route_tables').paginate(
                Filters=selector, PaginationConfig={'PageSize': 5}))
            assert [len(p['RouteTables']) for p in filtered] == [5, 2]
            assert [r['RouteTableId'] for r in rows(filtered)] == sorted([*native[:6], stack_out['Table']])
            filter_token = filtered[0]['NextToken']
            assert r1.describe_route_tables(NextToken=filter_token, Filters=selector)['RouteTables'] == filtered[1]['RouteTables']
            reject(r1.describe_route_tables, 'InvalidNextToken', NextToken=filter_token)
            reject(r2.describe_route_tables, 'InvalidNextToken', Filters=selector, NextToken=filter_token)
            reject(client.describe_route_tables, 'InvalidParameterCombination', MaxResults=5, RouteTableIds=[native[-1]])
            empty_region = sdk(stack, port, operator, 'ap-northeast-1')
            assert rows(sdk_pages(empty_region)) == []
            reject(empty_region.describe_route_tables, 'InvalidNextToken', NextToken=pages[0]['NextToken'])
            # A locally authorized write to a row not selected by the filter
            # makes both continuations stale, without changing the filter's matches.
            client.delete_route_table(RouteTableId=native[-1])
            reject(r1.describe_route_tables, 'InvalidNextToken', Filters=selector, NextToken=filter_token)
            reject(client.describe_route_tables, 'InvalidNextToken', NextToken=pages[0]['NextToken'])
            current = sdk_pages(client)
            retained = rows(current)
            restart_token = current[0]['NextToken']

        with run_default(binary, state=state) as (port, operator), ExitStack() as stack:
            local = Local(port)
            client = sdk(stack, port, operator)
            reject(client.describe_route_tables, 'InvalidNextToken', NextToken=restart_token)
            assert rows(sdk_pages(client)) == retained
            for association in associations:
                client.disassociate_route_table(AssociationId=association)
            for subnet in subnets:
                client.delete_subnet(SubnetId=subnet)
            for table in native[:-1]:
                client.delete_route_table(RouteTableId=table)
            client.delete_vpc(VpcId=vpc)
            local.query('cloudformation', 'DeleteStack', local.session(operator), StackName=cfn)
            assert rows(sdk_pages(client)) == [] and client.describe_vpcs()['Vpcs'] == []
    print('PASS actual pinned SDK: 182 mixed tables against 16 MiB, complete 100+82 pages including the native main, '
          '7 explicit filtered matches, separately authenticated readers/region, durable restart, scoped refusal and cleanup.')


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    check(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
