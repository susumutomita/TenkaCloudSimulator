"""Exercise the shared ELBv2 Query forms in Scalar against two actual local teams."""

import hashlib
import io
import json
import os
import re
import shutil
import tempfile
import zipfile
from contextlib import ExitStack, contextmanager
from pathlib import Path
from urllib.parse import parse_qsl
from xml.etree import ElementTree

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import expect, sync_playwright

from autoscaling_plans_console import wire
from gcp_dns_sdk import LocalTeam, started
from simpledb_scalar_reference_live import ScalarQueryReference


SERVICE = 'elasticloadbalancing'
VERSION = '2015-12-01'
NS = {'e': 'http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/'}
FORM = 'application/x-www-form-urlencoded'
NAME = 'scalar-practice-targets'
PRESETS = {
    'CreateTargetGroup': {'Name': NAME, 'TargetType': 'lambda'},
    'DescribeLoadBalancers': {'LoadBalancerArns.member.1': 'REPLACE_WITH_LOAD_BALANCER_ARN', 'PageSize': 20},
    'DescribeTargetHealth': {'TargetGroupArn': 'REPLACE_WITH_TARGET_GROUP_ARN'},
    'DeleteTargetGroup': {'TargetGroupArn': 'REPLACE_WITH_TARGET_GROUP_ARN'},
}
PRESETS = {action: {'Action': action, 'Version': VERSION, **fields} for action, fields in PRESETS.items()}


def sdk(stack, team, service):
    client = boto3.client(
        service,
        endpoint_url=team.endpoint,
        region_name='us-east-1',
        aws_access_key_id='TCSIMLOCAL',
        aws_secret_access_key='dummy',
        config=Config(proxies={}, retries={'total_max_attempts': 1}),
    )
    stack.callback(client.close)
    client.meta.events.register(
        'before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', team.token)
    )

    def verify_origin(request, **_):
        assert request.url.startswith(team.endpoint + '/')

    client.meta.events.register('before-send', verify_origin)
    return client


@contextmanager
def reference(browser, team, token):
    ctx = browser.new_context(service_workers='block')
    outside, errors, requests = [], [], []
    try:

        def local(route):
            if route.request.url.startswith(team.endpoint + '/'):
                route.continue_()
            else:
                outside.append(route.request.resource_type)
                route.abort()

        ctx.route('**/*', local)
        page = ctx.new_page()
        page.on('pageerror', lambda err: errors.append(str(err)))
        page.goto(team.endpoint + '/_simulator/console/')
        page.get_by_label('Access token', exact=True).fill(token)
        page.get_by_role('button', name='Open sandbox', exact=True).click()
        expect(page.get_by_role('button', name='Services', exact=True)).to_be_visible()
        page.on(
            'request',
            lambda r: requests.append(r) if r.method == 'POST' and r.url == team.endpoint + '/' else None,
        )
        yield ScalarQueryReference(page, team.endpoint, PRESETS), requests
        assert not outside and not errors, (outside, errors)
    finally:
        ctx.close()


def send(ref, requests, action, edits, status, report):
    start = len(requests)
    ref.open_example(action, service=SERVICE, replacements=edits)
    assert len(requests) == start
    response = ref.send()
    assert requests[start:] == [response.request]
    request = response.request
    assert request.redirected_from is None and request.redirected_to is None
    headers = request.all_headers()
    assert headers.get('cookie') and headers['x-simulator-service'] == SERVICE
    assert headers.get('x-simulator-region', 'us-east-1') == 'us-east-1'
    assert headers['content-type'].split(';')[0] == FORM
    assert {'x-simulator-token', 'authorization', 'x-amz-target', 'x-simulator-principal'}.isdisjoint(headers)
    expected = {k: str(v) for k, v in (PRESETS[action] | edits).items()}
    pairs = parse_qsl(request.post_data, keep_blank_values=True, strict_parsing=True)
    assert len(pairs) == len(expected) and dict(pairs) == expected, (pairs, expected)
    assert response.status == status
    root = ElementTree.fromstring(response.body())
    if status == 403:
        assert root.tag == '{' + NS['e'] + '}ErrorResponse'
        assert root.findtext('e:Error/e:Code', namespaces=NS) == 'AccessDenied'
        expect(ref.response_panel).to_contain_text('AccessDenied')
    else:
        assert root.tag == '{' + NS['e'] + '}' + action + 'Response'
        assert re.fullmatch(
            r'elb-local-[a-f0-9]{16}', root.findtext('e:ResponseMetadata/e:RequestId', namespaces=NS)
        )
    assert requests[start:] == [request]
    report.append([action, status])
    return root.find('e:' + action + 'Result', NS)


def setup_listener(clean, elb, ec2, iam, lambdas):
    vpc = ec2.create_vpc(CidrBlock='10.24.0.0/16')['Vpc']['VpcId']
    clean.callback(ec2.delete_vpc, VpcId=vpc)
    subnets = []
    for n, zone in ((1, 'us-east-1a'), (2, 'us-east-1b')):
        subnet = ec2.create_subnet(VpcId=vpc, CidrBlock=f'10.24.{n}.0/24', AvailabilityZone=zone)['Subnet'][
            'SubnetId'
        ]
        clean.callback(ec2.delete_subnet, SubnetId=subnet)
        subnets.append(subnet)
    sg = ec2.create_security_group(VpcId=vpc, GroupName='scalar-elb', Description='local group')['GroupId']
    clean.callback(ec2.delete_security_group, GroupId=sg)
    lb = elb.create_load_balancer(Name='scalar-practice-alb', Subnets=subnets, SecurityGroups=[sg])[
        'LoadBalancers'
    ][0]
    clean.callback(elb.delete_load_balancer, LoadBalancerArn=lb['LoadBalancerArn'])
    trust = {
        'Version': '2012-10-17',
        'Statement': [
            {'Effect': 'Allow', 'Principal': {'Service': 'lambda.amazonaws.com'}, 'Action': 'sts:AssumeRole'}
        ],
    }
    role = iam.create_role(RoleName='scalar-elb-role', AssumeRolePolicyDocument=json.dumps(trust))['Role']
    clean.callback(iam.delete_role, RoleName=role['RoleName'])
    archive = io.BytesIO()
    with zipfile.ZipFile(archive, 'w') as script:
        script.writestr(
            'app.py', 'def handler(event, context):\n    return {"statusCode": 200, "body": "local"}\n'
        )
    name = 'scalar-elb-function'
    fn = lambdas.create_function(
        FunctionName=name,
        Runtime='python3.12',
        Role=role['Arn'],
        Handler='app.handler',
        Code={'ZipFile': archive.getvalue()},
    )['FunctionArn']
    clean.callback(lambdas.delete_function, FunctionName=name)
    tg = elb.create_target_group(Name='scalar-linked', TargetType='lambda')['TargetGroups'][0][
        'TargetGroupArn'
    ]
    clean.callback(elb.delete_target_group, TargetGroupArn=tg)
    lambdas.add_permission(
        FunctionName=name,
        StatementId='local-alb',
        Action='lambda:InvokeFunction',
        Principal='elasticloadbalancing.amazonaws.com',
        SourceArn=tg,
    )
    elb.register_targets(TargetGroupArn=tg, Targets=[{'Id': fn}])
    clean.callback(elb.deregister_targets, TargetGroupArn=tg, Targets=[{'Id': fn}])
    listener = elb.create_listener(
        LoadBalancerArn=lb['LoadBalancerArn'],
        Protocol='HTTP',
        Port=80,
        DefaultActions=[{'Type': 'forward', 'TargetGroupArn': tg}],
    )['Listeners'][0]
    clean.callback(elb.delete_listener, ListenerArn=listener['ListenerArn'])
    return lb, tg, fn


def check_placement(result, ref, actual):
    rows = result.findall('e:LoadBalancers/e:member', NS)
    assert len(rows) == 1
    row = rows[0]
    for key in ('LoadBalancerArn', 'VpcId', 'LoadBalancerName'):
        assert row.findtext('e:' + key, namespaces=NS) == actual[key]
        expect(ref.response_panel).to_contain_text(actual[key])
    assert [sg.text for sg in row.findall('e:SecurityGroups/e:member', NS)] == actual['SecurityGroups']
    zones = {
        (z.findtext('e:SubnetId', namespaces=NS), z.findtext('e:ZoneName', namespaces=NS))
        for z in row.findall('e:AvailabilityZones/e:member', NS)
    }
    assert len(zones) == 2 and zones == {(z['SubnetId'], z['ZoneName']) for z in actual['AvailabilityZones']}


def cleanup_unconfirmed_group(elb):
    """A failed browser assertion may follow a successful CreateTargetGroup."""
    try:
        groups = elb.describe_target_groups(Names=[NAME])['TargetGroups']
    except ClientError as error:
        if error.response['Error']['Code'] == 'TargetGroupNotFound':
            return
        raise
    assert len(groups) == 1 and groups[0]['TargetGroupName'] == NAME and groups[0]['TargetType'] == 'lambda'
    elb.delete_target_group(TargetGroupArn=groups[0]['TargetGroupArn'])


def exercise(team, peer, browser):
    sends = []
    with ExitStack() as clients:
        elb, ec2, iam, lambdas = [sdk(clients, team, s) for s in ('elbv2', 'ec2', 'iam', 'lambda')]
        other = sdk(clients, peer, 'elbv2')
        with ExitStack() as clean:
            peer_group = other.create_target_group(Name=NAME, TargetType='lambda', HealthCheckEnabled=True)[
                'TargetGroups'
            ][0]
            clean.callback(other.delete_target_group, TargetGroupArn=peer_group['TargetGroupArn'])
            lb, linked, fn = setup_listener(clean, elb, ec2, iam, lambdas)
            lb_args = {'LoadBalancerArns.member.1': lb['LoadBalancerArn']}
            assert all(g['TargetGroupName'] != NAME for g in elb.describe_target_groups()['TargetGroups'])
            clean.callback(cleanup_unconfirmed_group, elb)
            with reference(browser, team, team.token) as (ref, requests):
                created = send(ref, requests, 'CreateTargetGroup', {}, 200, sends)
                target = created.findtext('e:TargetGroups/e:member/e:TargetGroupArn', namespaces=NS)
                assert target
                clean.callback(elb.delete_target_group, TargetGroupArn=target)
                expect(ref.response_panel).to_contain_text(target)
                native = elb.describe_target_groups(TargetGroupArns=[target])['TargetGroups'][0]
                assert (
                    native['TargetGroupName'] == NAME
                    and native['TargetType'] == 'lambda'
                    and not native['HealthCheckEnabled']
                )
                for key in ('TargetGroupName', 'TargetGroupArn', 'TargetType'):
                    assert created.findtext('e:TargetGroups/e:member/e:' + key, namespaces=NS) == native[key]
                ref.close_client()
                content = send(ref, requests, 'DescribeLoadBalancers', lb_args, 200, sends)
                check_placement(
                    content,
                    ref,
                    elb.describe_load_balancers(LoadBalancerArns=[lb['LoadBalancerArn']])['LoadBalancers'][0],
                )
                ref.close_client()
                result = send(ref, requests, 'DescribeTargetHealth', {'TargetGroupArn': linked}, 200, sends)
                health = elb.describe_target_health(TargetGroupArn=linked)['TargetHealthDescriptions']
                assert len(health) == 1 and health[0]['Target']['Id'] == fn
                assert (
                    result.findtext('e:TargetHealthDescriptions/e:member/e:Target/e:Id', namespaces=NS) == fn
                )
                for key, value in (('State', 'unavailable'), ('Reason', 'Target.HealthCheckDisabled')):
                    assert (
                        result.findtext(
                            'e:TargetHealthDescriptions/e:member/e:TargetHealth/e:' + key, namespaces=NS
                        )
                        == health[0]['TargetHealth'][key]
                        == value
                    )
                    expect(ref.response_panel).to_contain_text(value)
                expect(ref.response_panel).to_contain_text(fn)
            principal = 'scalar-elb-reader'
            wire(
                team,
                'PUT',
                '/_simulator/v1/iam/principals/' + principal + '/policy',
                {
                    'Version': '2012-10-17',
                    'Statement': [
                        {
                            'Effect': 'Allow',
                            'Action': 'elasticloadbalancing:DescribeLoadBalancers',
                            'Resource': '*',
                        }
                    ],
                },
            )
            token = wire(team, 'POST', '/_simulator/v1/access', {'Principal': principal})['AccessToken']
            with reference(browser, team, token) as (ref, requests):
                result = send(ref, requests, 'DescribeLoadBalancers', lb_args, 200, sends)
                check_placement(result, ref, lb)
                ref.close_client()
                send(ref, requests, 'DeleteTargetGroup', {'TargetGroupArn': target}, 403, sends)
            team.stop()
            team.start()
            assert elb.describe_target_groups(TargetGroupArns=[target])['TargetGroups'] == [native]
            assert other.describe_target_groups(Names=[NAME])['TargetGroups'] == [peer_group]
            assert (
                elb.describe_listeners(LoadBalancerArn=lb['LoadBalancerArn'])['Listeners'][0][
                    'DefaultActions'
                ][0]['TargetGroupArn']
                == linked
            )
        assert (
            elb.describe_target_groups()['TargetGroups'] == []
            and other.describe_target_groups()['TargetGroups'] == []
        )
        assert elb.describe_load_balancers()['LoadBalancers'] == []
    return sends


def main():
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    with binary.open('rb') as f:
        digest = hashlib.file_digest(f, 'sha256').hexdigest()
    assert digest == os.environ['SIMULATOR_BINARY_SHA256']
    with tempfile.TemporaryDirectory(prefix='elb-scalar-') as directory, sync_playwright() as pw:
        root = Path(directory)
        for name in ('owner', 'peer'):
            (root / name).mkdir()
        owner, peer = [LocalTeam(root / name, binary) for name in ('owner', 'peer')]
        exe = os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
        with (
            started(owner),
            started(peer),
            pw.chromium.launch(executable_path=exe, chromium_sandbox=True) as browser,
        ):
            sends = exercise(owner, peer, browser)
    assert not Path(directory).exists()
    print(
        json.dumps(
            {
                'binary': digest,
                'scalarSends': sends,
                'stockSDKReadback': True,
                'peerPreserved': True,
                'reopened': True,
                'cleanup': True,
            }
        )
    )


if __name__ == '__main__':
    main()
