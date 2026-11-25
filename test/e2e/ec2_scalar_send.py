"""Actual embedded Scalar Send for the native EC2 Query lifecycle.

The served API reference client sends CreateVpc, CreateSubnet, RunInstances,
DescribeInstances, TerminateInstances, DeleteSubnet and DeleteVpc with the
browser session cookie. The official SDK reads the same state back after each
mutation. No substitute fetch, mocked response, VM or cloud call. Run after
make build with the dependencies in scripts/requirements-test.txt.
"""
import os
import re
import shutil
from urllib.parse import parse_qsl
from xml.etree import ElementTree

from check import running, boto3, Config, sync_playwright, expect

FORM = 'application/x-www-form-urlencoded'
VERSION = '2016-11-15'
NS = '{http://ec2.amazonaws.com/doc/2016-11-15/}'
IMAGE = 'ami-0123456789abcdef0'
FIELDS = ('CidrBlock', 'VpcId', 'SubnetId', 'ImageId', 'InstanceType', 'MinCount', 'MaxCount', 'InstanceId.1')
PRESETS = {
    'EC2CreateVpc': {'Action': 'CreateVpc', 'Version': VERSION, 'CidrBlock': '10.0.0.0/16'},
    'EC2CreateSubnet': {'Action': 'CreateSubnet', 'Version': VERSION, 'VpcId': '__VPC_ID__', 'CidrBlock': '10.0.1.0/24'},
    'EC2RunInstances': {'Action': 'RunInstances', 'Version': VERSION, 'ImageId': IMAGE, 'InstanceType': 't3.micro',
                        'MinCount': '1', 'MaxCount': '1', 'SubnetId': '__SUBNET_ID__'},
    'EC2DescribeInstances': {'Action': 'DescribeInstances', 'Version': VERSION},
    'EC2TerminateInstances': {'Action': 'TerminateInstances', 'Version': VERSION, 'InstanceId.1': '__INSTANCE_ID__'},
    'EC2DeleteSubnet': {'Action': 'DeleteSubnet', 'Version': VERSION, 'SubnetId': '__SUBNET_ID__'},
    'EC2DeleteVpc': {'Action': 'DeleteVpc', 'Version': VERSION, 'VpcId': '__VPC_ID__'},
}


def run():
    with running(authenticated=True) as (endpoint, operator), sync_playwright() as p:
        ec2 = boto3.client('ec2', endpoint_url=endpoint, region_name='us-east-1', aws_access_key_id='TCSIMSCALAREC2',
                           aws_secret_access_key='dummy-local-only', config=Config(retries={'max_attempts': 0}))
        ec2.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', operator))

        def state():
            return ([v['VpcId'] for v in ec2.describe_vpcs()['Vpcs']], [s['SubnetId'] for s in ec2.describe_subnets()['Subnets']],
                    [i for r in ec2.describe_instances()['Reservations'] for i in r['Instances']])
        assert state() == ([], [], [])
        browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
        context = browser.new_context(viewport={'width': 1440, 'height': 1000}, service_workers='block')
        assert context.request.post(endpoint + '/_simulator/login', form={'token': operator}, max_redirects=0).status == 303
        errors, outside, sends = [], [], []

        def scope(route):
            if route.request.url.startswith(endpoint + '/'):
                route.continue_()
            else:
                outside.append(route.request.url)
                route.abort()
        context.route('**/*', scope)
        page = context.new_page()
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.on('request', lambda q: sends.append(q) if q.method == 'POST' and q.url == endpoint + '/' else None)
        with page.expect_response(lambda response: response.url == endpoint + '/_simulator/docs/openapi.json') as served:
            page.goto(endpoint + '/_simulator/docs/#tag/kms-local-symmetric-keys/POST/')
        # The served document offers exactly the requests this test sends.
        root = served.value.json()['paths']['/']['post']
        presets = root['requestBody']['content'][FORM]['examples']
        for name, value in PRESETS.items():
            assert presets[name]['value'] == value, name
        assert 'ec2' in next(h for h in root['parameters'] if h.get('name') == 'X-Simulator-Service')['schema']['enum']
        page.get_by_role('button', name='Test Request (post /)', exact=True).click(timeout=60000)
        client = page.get_by_role('dialog').filter(has=page.get_by_role('button', name='Close Client', exact=True))
        expect(client).to_have_count(1)
        client.get_by_role('button', name=re.compile('x-amz-json-1|Form URL Encoded')).click()
        page.get_by_role('option', name='Form URL Encoded', exact=True).click()

        def row(key):
            found = client.locator('tr[id="%s"]' % key)
            expect(found).to_have_count(1)
            return found

        def include(key, on=True):
            row(key).get_by_role('checkbox', name='Include %s in request' % key, exact=True).set_checked(on, force=True)

        def choose(key, value):
            row(key).locator('button[aria-haspopup="menu"]').click()
            page.get_by_role('menuitem', name=value, exact=True).click()
            expect(row(key).locator('button[aria-haspopup="menu"]')).to_have_text(value)
            include(key)

        def text(key, value):
            editor = row(key).locator('[contenteditable="true"][aria-label=" Value"]')
            editor.click()
            page.keyboard.press('ControlOrMeta+A')
            page.keyboard.type(value)
            expect(editor).to_have_text(value)
            include(key)

        def send(fields):
            """Set exactly `fields`, press Send once and return the native response."""
            for key in FIELDS:
                include(key, key in fields)
            for key, value in fields.items():
                if key in ('Action', 'Version', 'InstanceType', 'MinCount', 'MaxCount'):
                    choose(key, value)
                else:
                    text(key, value)
            before = len(sends)
            with page.expect_response(lambda response: response.request.method == 'POST' and response.url == endpoint + '/') as capture:
                client.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
            response = capture.value
            assert len(sends) == before + 1 and response.request is sends[-1], 'expected exactly one native POST per Send'
            request = response.request
            headers = request.all_headers()
            assert headers['content-type'].split(';', 1)[0].strip().lower() == FORM
            assert headers.get('x-simulator-service') == 'ec2' and headers.get('cookie')
            assert not set(headers) & {'authorization', 'x-simulator-token', 'x-simulator-principal', 'x-amz-target'}
            pairs = parse_qsl(request.post_data, strict_parsing=True, keep_blank_values=True)
            assert len(pairs) == len(fields) and dict(pairs) == fields, pairs
            assert response.status == 200, response.text()
            assert response.headers['content-type'].split(';', 1)[0] == 'text/xml'
            document = ElementTree.fromstring(response.body())
            assert document.tag == NS + fields['Action'] + 'Response', document.tag
            return document

        choose('X-Simulator-Service', 'ec2')
        assert not sends, 'editing the request sent it before Send'
        vpc = send({**PRESETS['EC2CreateVpc'], 'CidrBlock': '10.80.0.0/16'}).findtext(NS + 'vpc/' + NS + 'vpcId')
        assert state() == ([vpc], [], [])
        subnet = send({**PRESETS['EC2CreateSubnet'], 'VpcId': vpc, 'CidrBlock': '10.80.1.0/24'}).findtext(NS + 'subnet/' + NS + 'subnetId')
        assert state() == ([vpc], [subnet], [])
        launched = send({**PRESETS['EC2RunInstances'], 'SubnetId': subnet})
        instance = launched.findtext(NS + 'instancesSet/' + NS + 'item/' + NS + 'instanceId')
        assert launched.find('.//' + NS + 'instanceState') is None
        assert launched.findtext('.//' + NS + 'simulatorRuntimeState') == 'not_launched'
        vpcs, subnets, instances = state()
        assert (vpcs, subnets, [i['InstanceId'] for i in instances]) == ([vpc], [subnet], [instance])
        assert (instances[0]['ImageId'], instances[0]['InstanceType'], instances[0]['SubnetId'], instances[0]['VpcId']) == (IMAGE, 't3.micro', subnet, vpc)
        assert 'State' not in instances[0]
        described = send(PRESETS['EC2DescribeInstances'])
        assert [n.text for n in described.iter(NS + 'instanceId')] == [instance]
        # The response panel shows the live record, not a static example.
        panel = client.locator('.response-section-content-body').filter(has=page.get_by_role('link', name='Download Response Body', exact=True))
        expect(panel).to_have_count(1)
        expect(panel).to_contain_text(instance)
        expect(panel).to_contain_text('not_launched')
        terminated = send({**PRESETS['EC2TerminateInstances'], 'InstanceId.1': instance})
        assert terminated.findtext('.//' + NS + 'currentState/' + NS + 'name') == 'terminated'
        assert state() == ([vpc], [subnet], [])
        assert send({**PRESETS['EC2DeleteSubnet'], 'SubnetId': subnet}).findtext(NS + 'return') == 'true'
        assert send({**PRESETS['EC2DeleteVpc'], 'VpcId': vpc}).findtext(NS + 'return') == 'true'
        assert state() == ([], [], [])
        assert len(sends) == 7 and not outside and not errors, (len(sends), outside, errors)
        context.close()
        browser.close()
    print('Scalar Send drove the native EC2 lifecycle against the same local state')


if __name__ == '__main__':
    run()
