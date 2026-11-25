"""Focused VPC form wire check: real login, CloudFormation, EC2 reads, restart.

Run with SIMULATOR_BINARY pointing at the candidate binary and an isolated local
runner. Uses the production form's request builder, not a browser or real AWS.
"""
import http.client
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import time
import urllib.parse
import xml.etree.ElementTree as ET
from contextlib import contextmanager

ROOT = Path(__file__).resolve().parents[2]
CF = '{http://cloudformation.amazonaws.com/doc/2010-05-15/}'
EC = '{http://ec2.amazonaws.com/doc/2016-11-15/}'
REGION = 'us-east-1'
FORM = dict(stackName='console-net-test', name='Local network', cidr='10.20.0.0/16', subnetCIDR='10.20.1.0/24')


@contextmanager
def running(binary, directory):
    log_path = directory / 'startup.log'
    with log_path.open('w+b') as log:
        proc = subprocess.Popen([binary, '--listen', '127.0.0.1:0',
                                 '--state', 'state.db', '--token-file', 'operator-token'],
                                cwd=directory, env={'PATH': '/nonexistent'}, stdout=log, stderr=log)
        try:
            for _ in range(250):
                if proc.poll() is not None:
                    raise AssertionError(f'Simulator exited during startup ({proc.returncode})')
                match = re.search(r'^AWS simulation endpoint http://127\.0\.0\.1:([1-9][0-9]*)$',
                                  log_path.read_text(), flags=re.M)
                if match:
                    yield int(match[1]), (directory / 'operator-token').read_text().strip()
                    break
                time.sleep(.05)
            else:
                raise AssertionError('Local simulator did not start')
        finally:
            if proc.poll() is None:
                proc.send_signal(signal.SIGTERM)
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                    raise
            assert proc.returncode == 0, f'Simulator exit {proc.returncode}'


def call(port, path, body, headers, status=200, method='POST'):
    conn = http.client.HTTPConnection('127.0.0.1', port, timeout=8)
    try:
        conn.request(method, path, body, headers)
        rsp = conn.getresponse()
        data = rsp.read()
        assert rsp.status == status, (path, rsp.status, data[:120])
        return rsp.getheader('Set-Cookie'), data
    finally:
        conn.close()


def authenticate(port, token):
    cookie, _ = call(port, '/_simulator/login', urllib.parse.urlencode({'token': token}),
                     {'Content-Type': 'application/x-www-form-urlencoded'}, 303)
    assert cookie and 'HttpOnly' in cookie
    return cookie.split(';', 1)[0]


def query(port, cookie, body, service, status=200, principal='simulator-admin', region=REGION):
    _, data = call(port, '/', body, {'Content-Type': 'application/x-www-form-urlencoded',
                   'X-Simulator-Region': region, 'X-Simulator-Service': service,
                   'X-Simulator-Principal': principal, 'Cookie': cookie}, status)
    return ET.fromstring(data)


def wizard_query(form):
    return subprocess.check_output(['node', '--input-type=module', '-e', '''
      import {vpcSubnetParameters} from './tools/console/src/ec2-vpc-subnet.mjs';
      process.stdout.write(vpcSubnetParameters(JSON.parse(process.argv[1])).toString());
    ''', json.dumps(form)], cwd=ROOT)


def cfn(port, cookie, action, identifier, status=200):
    return query(port, cookie, urllib.parse.urlencode({'Action': action, 'StackName': identifier,
                                                       'Version': '2010-05-15'}), 'cloudformation', status)


def ec2(port, cookie, action, **kwargs):
    return query(port, cookie, urllib.parse.urlencode({'Action': action, 'Version': '2016-11-15'}), 'ec2', **kwargs)


def rows(xml, set_name):
    return xml.findall(f'{EC}{set_name}/{EC}item')


def assert_saved(port, cookie, stack_id):
    result = cfn(port, cookie, 'DescribeStacks', stack_id)
    stack, = result.findall(f'{CF}DescribeStacksResult/{CF}Stacks/{CF}member')
    assert stack.findtext(CF+'StackId') == stack_id
    assert stack.findtext(CF+'StackStatus') == 'CREATE_COMPLETE'
    output = {x.findtext(CF+'OutputKey'): x.findtext(CF+'OutputValue') for x in stack.findall(f'{CF}Outputs/{CF}member')}
    vpc, = rows(ec2(port, cookie, 'DescribeVpcs'), 'vpcSet')
    subnet, = rows(ec2(port, cookie, 'DescribeSubnets'), 'subnetSet')
    assert vpc.findtext(EC+'vpcId') == output['VpcId']
    assert vpc.findtext(EC+'cidrBlock') == FORM['cidr']
    assert vpc.findtext(EC+'simulatorManagedBy') == 'cloudformation'
    assert subnet.findtext(EC+'subnetId') == output['SubnetId']
    assert subnet.findtext(EC+'vpcId') == output['VpcId']
    assert subnet.findtext(EC+'cidrBlock') == FORM['subnetCIDR']
    assert not rows(ec2(port, cookie, 'DescribeSubnets', region='ap-northeast-1'), 'subnetSet')
    return output


def main():
    assert __debug__, 'Run without optimization'
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix='vpc-subnet-console-') as name:
        directory = Path(name)
        with running(binary, directory) as (port, token):
            admin = authenticate(port, token)
            api_headers = {'Content-Type': 'application/json', 'X-Simulator-Token': token}
            actions = ['cloudformation:CreateStack', 'ec2:CreateVpc', 'ec2:ModifyVpcAttribute',
                       'ec2:ModifySubnetAttribute']
            policy = json.dumps({'Version':'2012-10-17', 'Statement':[
                {'Effect':'Allow', 'Action':actions, 'Resource':'*'},
                {'Effect':'Deny', 'Action':'ec2:CreateSubnet', 'Resource':'*'},
            ]})
            call(port, '/_simulator/v1/iam/principals/no-subnet/policy', policy, api_headers, 200, 'PUT')
            _, response = call(port, '/_simulator/v1/access', '{"Principal":"no-subnet"}', api_headers)
            restricted = authenticate(port, json.loads(response)['AccessToken'])
            for session, selectable in ((admin, True), (restricted, False)):
                _, response = call(port, '/_simulator/v1/capabilities', None, {'Cookie':session}, method='GET')
                assert json.loads(response)['identitySelection'] is selectable
            # Direct native policy exercise, not the operator-only Console form.
            refused = query(port, restricted, wizard_query(dict(FORM, name='')), 'cloudformation', 403, principal='no-subnet')
            assert refused.findtext(f'{CF}Error/{CF}Code') == 'AccessDenied'
            assert 'not allow ec2:CreateSubnet on ' in refused.findtext(f'{CF}Error/{CF}Message')
            assert not cfn(port, admin, 'DescribeStacks', '').findall(f'{CF}DescribeStacksResult/{CF}Stacks/{CF}member')
            assert not rows(ec2(port, admin, 'DescribeVpcs'), 'vpcSet')
            assert not rows(ec2(port, admin, 'DescribeSubnets'), 'subnetSet')
            fields = urllib.parse.parse_qs(wizard_query(FORM).decode())
            template = json.loads(fields['TemplateBody'][0])
            template['Resources']['Subnet']['Properties']['CidrBlock'] = '10.21.0.0/24'
            fields['TemplateBody'] = [json.dumps(template)]
            # The browser rejects this upfront. The current native transaction
            # returns InternalFailure for containment errors and rolls back both resources.
            query(port, admin, urllib.parse.urlencode(fields, doseq=True), 'cloudformation', 500)
            assert not rows(ec2(port, admin, 'DescribeVpcs'), 'vpcSet')
            assert not rows(ec2(port, admin, 'DescribeSubnets'), 'subnetSet')
            response = query(port, admin, wizard_query(FORM), 'cloudformation')
            stack_id = response.findtext(f'{CF}CreateStackResult/{CF}StackId')
            assert stack_id and stack_id.startswith(f'arn:aws:cloudformation:{REGION}:000000000000:stack/{FORM["stackName"]}/')
            expected = assert_saved(port, admin, stack_id)

        with running(binary, directory) as (port, token):
            admin = authenticate(port, token)
            assert assert_saved(port, admin, stack_id) == expected
            cfn(port, admin, 'DeleteStack', stack_id)
            assert not rows(ec2(port, admin, 'DescribeVpcs'), 'vpcSet')
            assert not rows(ec2(port, admin, 'DescribeSubnets'), 'subnetSet')
    print('PASS: role denial and out-of-VPC rollback, exact CloudFormation receipt, native two-resource readback, restart and cleanup')


if __name__ == '__main__':
    main()
