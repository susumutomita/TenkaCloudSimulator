"""Verify the unqualified EC2 launch boundary using a disposable authenticated CLI.

Optional local acceptance, not the GUI launch or a VM test. No endpoint or runtime
configuration can be supplied; only numeric loopback and memory-only state are used.
"""
import base64
from contextlib import contextmanager
import http.client
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import tempfile
import time
from urllib.parse import urlencode
import xml.etree.ElementTree as ET

EC2 = '{http://ec2.amazonaws.com/doc/2016-11-15/}'
CFN = '{http://cloudformation.amazonaws.com/doc/2010-05-15/}'
REGIONS = ('us-east-1', 'ap-northeast-1')
STACK = 'local-client-ec2-preflight'
READER = 'ec2-template-preview-only'
VPC = {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.91.0.0/16'}}
ENDPOINT = re.compile(rb'^AWS simulation endpoint http://127\.0\.0\.1:([1-9][0-9]{0,4})\r?\n', re.M)


def launch_declaration():
    def ref(logical_id):
        return {'Ref': logical_id}

    return json.dumps({'Resources': {
        'Vpc': VPC,
        'Subnet': {'Type': 'AWS::EC2::Subnet', 'Properties': {'VpcId': {'Ref': 'Vpc'}, 'CidrBlock': '10.91.1.0/24'}},
        'Group': {'Type': 'AWS::EC2::SecurityGroup', 'Properties': {'GroupDescription': 'No ingress', 'VpcId': {'Ref': 'Vpc'}}},
        'Role': {'Type': 'AWS::IAM::Role', 'Properties': {'RoleName': 'local-ec2-preflight-role',
                 'AssumeRolePolicyDocument': {'Version': '2012-10-17', 'Statement': [
                     {'Effect': 'Allow', 'Principal': {'Service': 'ec2.amazonaws.com'}, 'Action': 'sts:AssumeRole'}]}}},
        'Profile': {'Type': 'AWS::IAM::InstanceProfile', 'Properties': {
            'InstanceProfileName': 'local-ec2-preflight-profile', 'Roles': [ref('Role')]}},
        'Instance': {'Type': 'AWS::EC2::Instance', 'Properties': {
            'ImageId': 'ami-00000000000000001', 'InstanceType': 't3.micro', 'IamInstanceProfile': ref('Profile'),
            'SubnetId': ref('Subnet'), 'SecurityGroupIds': [ref('Group')], 'UserData': base64.b64encode(b'#cloud-config\n{}\n').decode()}},
    }, 'Outputs': {'Instance': {'Value': ref('Instance')}}})


@contextmanager
def run_default(binary):
    with tempfile.TemporaryDirectory(prefix='ec2-launch-preflight-') as directory:
        token_file = Path(directory) / 'operator-token'
        with subprocess.Popen([binary, '--listen', '127.0.0.1:0', '--state', ':memory:', '--token-file', token_file],
                              cwd=directory, env={'HOME': directory, 'PATH': '/nonexistent'},
                              stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) as proc:
            try:
                deadline, output = time.monotonic() + 12, b''
                with selectors.DefaultSelector() as selector:
                    selector.register(proc.stdout, selectors.EVENT_READ)
                    while time.monotonic() < deadline and selector.select(max(0, deadline - time.monotonic())):
                        chunk = os.read(proc.stdout.fileno(), 4096)
                        if not chunk:
                            break
                        output = (output + chunk)[-8192:]
                        match = ENDPOINT.search(output)
                        if match:
                            yield int(match[1]), token_file.read_text().strip()
                            return
                raise AssertionError('Default authenticated simulator did not become ready')
            finally:
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(8)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(4)
                        raise
                assert proc.returncode == 0, f'Local simulator did not shut down cleanly ({proc.returncode})'


class Local:
    def __init__(self, port):
        self.port = port

    def call(self, method, path, body='', status=200, **headers):
        conn = http.client.HTTPConnection('127.0.0.1', self.port, timeout=8)
        try:
            conn.request(method, path, body, headers)
            reply = conn.getresponse()
            data = reply.read(262145)
            assert len(data) <= 262144, 'Unexpectedly large local response'
            assert reply.status == status, (method, path, reply.status, status)
            return reply.headers, data
        finally:
            conn.close()

    def session(self, token):
        headers, _ = self.call('POST', '/_simulator/login', urlencode({'token': token}), 303,
                               **{'Content-Type': 'application/x-www-form-urlencoded'})
        cookie = headers['Set-Cookie']
        assert cookie and 'HttpOnly' in cookie
        return cookie.split(';', 1)[0]

    def preview_only_session(self, admin):
        policy = json.dumps({'Version': '2012-10-17', 'Statement': [
            {'Effect': 'Allow', 'Action': ['cloudformation:ValidateTemplate'], 'Resource': ['*']}]})
        self.call('PUT', f'/_simulator/v1/iam/principals/{READER}/policy', policy,
                  Cookie=admin, **{'Content-Type': 'application/json'})
        _, issue = self.call('POST', '/_simulator/v1/access', json.dumps({'Principal': READER}),
                             Cookie=admin, **{'Content-Type': 'application/json'})
        return self.session(json.loads(issue)['AccessToken'])

    def query(self, service, action, cookie, region=REGIONS[0], status=200, **fields):
        version = '2010-05-15' if service == 'cloudformation' else '2016-11-15'
        _, data = self.call('POST', '/', urlencode(dict(Action=action, Version=version, **fields)), status,
                            Cookie=cookie, **{'Content-Type': 'application/x-www-form-urlencoded',
                                             'X-Simulator-Service': service, 'X-Simulator-Region': region})
        return ET.fromstring(data)


def state(local, operator):
    def visible(region):
        stacks = local.query('cloudformation', 'DescribeStacks', operator, region)
        vpcs = local.query('ec2', 'DescribeVpcs', operator, region)
        instances = local.query('ec2', 'DescribeInstances', operator, region)
        return tuple(tuple((x.text or '') for x in doc.iter(tag)) for doc, tag in (
            (stacks, CFN+'StackId'), (vpcs, EC2+'vpcId'), (instances, EC2+'instanceId')))
    return tuple(visible(r) for r in REGIONS)


def check(local, operator_token):
    local.call('GET', '/_simulator/v1/capabilities', status=401)
    admin = local.session(operator_token)
    previewer = local.preview_only_session(admin)
    for cookie, can_select in ((admin, True), (previewer, False)):
        headers, body = local.call('GET', '/_simulator/v1/capabilities', Cookie=cookie)
        caps = json.loads(body)
        assert headers['X-Simulator-Authentication'] == 'required'
        assert caps['identitySelection'] is can_select and not caps.get('consoleScope')
        operations = set(caps['services']['ec2']['operations'])
        assert 'DescribeInstances' in operations
        assert not {'RunInstances', 'DescribeImages', 'StartInstances', 'StopInstances', 'TerminateInstances'} & operations

    template = launch_declaration()
    cloud = 'cloudformation'
    positive = local.query(cloud, 'CreateStack', admin, StackName='unrelated-local-network',
                           TemplateBody=json.dumps({'Resources': {'Vpc': VPC}}))
    saved_id = positive.findtext(f'{CFN}CreateStackResult/{CFN}StackId')
    assert saved_id
    try:
        before = state(local, admin)
        assert before[0][0] == (saved_id,) and len(before[0][1]) == 1 and not before[0][2]
        assert not any(before[1])
        for cookie in (admin, previewer):
            checked = local.query(cloud, 'ValidateTemplate', cookie, TemplateBody=template)
            assert checked.findtext(f'{CFN}ValidateTemplateResult/{CFN}Capabilities/{CFN}member') == 'CAPABILITY_NAMED_IAM'
        assert state(local, admin) == before, 'Template validation changed saved state'

        for action, fields in [('RunInstances', {'ImageId': 'ami-00000000000000001', 'MinCount': 1, 'MaxCount': 1}),
                               ('DescribeImages', {})]:
            refused = local.query('ec2', action, admin, status=400, **fields)
            assert refused.findtext('Errors/Error/Code') == 'UnsupportedOperation'

        request = {'StackName': STACK, 'TemplateBody': template, 'Capabilities.member.1': 'CAPABILITY_NAMED_IAM',
                   'ClientRequestToken': 'local-no-runtime-stable'}
        denied = local.query(cloud, 'CreateStack', previewer, status=403, **request)
        assert denied.findtext(f'{CFN}Error/{CFN}Code') == 'AccessDenied'
        for region in REGIONS:
            # The stable token must not replay a fake success or leak the other region.
            for _ in range(2):
                missing = local.query(cloud, 'CreateStack', admin, region, 501, **request)
                assert missing.findtext(f'{CFN}Error/{CFN}Code') == 'NotImplemented'
                assert missing.findtext(f'{CFN}Error/{CFN}Message') == 'A qualified local instance runtime is not configured.'
            no_events = local.query(cloud, 'DescribeStackEvents', admin, region, 400, StackName=STACK)
            assert no_events.findtext(f'{CFN}Error/{CFN}Code') == 'ValidationError'
        assert state(local, admin) == before, 'Failed launch left a stack, network resource or instance'
    finally:
        local.query(cloud, 'DeleteStack', admin, StackName=saved_id)
    assert not any(row for region in state(local, admin) for row in region), 'Control stack cleanup did not complete'


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    with run_default(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)) as (port, token):
        check(Local(port), token)
    print('PASS authenticated CLI: preview is not launch; no native operation or missing-runtime stack state; local cleanup verified.')
