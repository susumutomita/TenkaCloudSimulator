"""Four quota discovery reads through stock boto3 and two private local CLIs.

Inherited SDK profile/configuration bindings remain intact. The SDK receives
only fixture credentials and the owned numeric-loopback endpoint.
"""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit
import urllib.request

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve(strict=True)
REGIONS = ('us-east-1', 'us-west-2', 'eu-west-1')
KEY = {'ServiceCode': 'b2bi', 'QuotaCode': 'L-05F6A9EF'}
DEFAULT = {**KEY, 'QuotaName': 'Profiles', 'Value': 5.0, 'Adjustable': True, 'GlobalQuota': False}
READS = ('ListServices', 'GetAWSDefaultServiceQuota', 'ListAWSDefaultServiceQuotas', 'ListServiceQuotas')


class RefuseRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        raise AssertionError('owned control request refused a redirect')


class Team:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.process = None
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        self.endpoint = f'http://127.0.0.1:{self.port}'
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), RefuseRedirect())

    def start(self):
        token_file = self.directory / 'operator-token'
        self.process = subprocess.Popen(
            [str(BINARY), '--listen', f'127.0.0.1:{self.port}', '--state', str(self.directory / 'state.db'),
             '--token-file', str(token_file)], cwd=self.directory,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(500):
            assert self.process.poll() is None, 'owned CLI exited before readiness'
            if token_file.exists():
                self.token = token_file.read_text().strip()
                try:
                    self.control('GET', '/_simulator/health')
                    return
                except OSError:
                    pass
            time.sleep(0.05)
        raise AssertionError('owned CLI readiness deadline')

    def stop(self):
        process = self.process
        if process is None:
            return
        try:
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise AssertionError('owned CLI required forced cleanup')
            assert process.returncode == 0, 'owned CLI shutdown failed'
        finally:
            self.process = None

    def control(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.endpoint + path, data=data, method=method,
                                     headers={'X-Simulator-Token': self.token, 'Content-Type': 'application/json'})
        with self.opener.open(req, timeout=5) as response:
            assert response.url == self.endpoint + path, 'control request left its owned route'
            raw = response.read()
            return json.loads(raw) if raw else {}

    def client(self, service, region='us-east-1', token=None):
        client = boto3.client(service, endpoint_url=self.endpoint, region_name=region,
                              aws_access_key_id='TCSIMQUOTAFIXTURE', aws_secret_access_key='fixture-local-only',
                              config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2, read_timeout=10))
        event = client.meta.service_model.service_id.hyphenize()
        client.meta.events.register('before-sign.' + event,
                                    lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', token or self.token))

        def local_only(request, **kwargs):
            url = urlsplit(request.url)
            assert (url.scheme, url.netloc, url.path, url.query) == ('http', f'127.0.0.1:{self.port}', '/', '')
            assert request.method == 'POST'
            if service == 'service-quotas':
                target = request.headers['X-Amz-Target']
                if isinstance(target, bytes):
                    target = target.decode('ascii')
                assert target in {'ServiceQuotasV20190624.' + name for name in (*READS, 'GetServiceQuota')}
        client.meta.events.register('before-send.' + event, local_only)
        return client


def refused(operation, code, **kwargs):
    try:
        operation(**kwargs)
    except ClientError as error:
        assert error.response['Error']['Code'] == code
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == 400
        assert not {'Services', 'Quotas', 'Quota', 'NextToken'} & error.response.keys()
    else:
        raise AssertionError('required native refusal was absent')


def participant(team, name, statements):
    team.control('PUT', f'/_simulator/v1/iam/principals/{name}/policy',
                 {'Version': '2012-10-17', 'Statement': statements})
    return team.control('POST', '/_simulator/v1/access', {'Principal': name})['AccessToken']


def discover(team, region='us-east-1', token=None):
    client = team.client('service-quotas', region, token)
    arn = f'arn:aws:servicequotas:{region}:000000000000:b2bi/L-05F6A9EF'
    applied = {**DEFAULT, 'QuotaArn': arn, 'QuotaAppliedAtLevel': 'ACCOUNT'}
    for maximum in (1, 100):
        result = client.list_services(MaxResults=maximum)
        assert result['Services'] == [{'ServiceCode': 'b2bi', 'ServiceName': 'AWS B2B Data Interchange'}]
        assert 'NextToken' not in result
        result = client.list_aws_default_service_quotas(ServiceCode='b2bi', MaxResults=maximum)
        assert result['Quotas'] == [DEFAULT] and 'NextToken' not in result
        result = client.list_service_quotas(ServiceCode='b2bi', MaxResults=maximum)
        assert result['Quotas'] == [applied] and 'NextToken' not in result
    assert client.get_aws_default_service_quota(**KEY)['Quota'] == DEFAULT
    assert client.get_service_quota(**KEY)['Quota'] == applied
    for level in ('ACCOUNT', 'ALL'):
        assert client.list_service_quotas(ServiceCode='b2bi', QuotaAppliedAtLevel=level)['Quotas'] == [applied]
    result = client.list_service_quotas(ServiceCode='b2bi', QuotaAppliedAtLevel='RESOURCE')
    assert result['Quotas'] == [] and 'NextToken' not in result
    assert client.list_service_quotas(ServiceCode='b2bi', QuotaCode='L-00000000')['Quotas'] == []
    for operation, kwargs in [(client.list_services, {}), (client.list_service_quotas, {'ServiceCode': 'b2bi'}),
                              (client.list_aws_default_service_quotas, {'ServiceCode': 'b2bi'})]:
        refused(operation, 'InvalidPaginationTokenException', **kwargs, NextToken='QQ==')


def exercise(first, peer):
    clients = [first.client('b2bi'), peer.client('b2bi')]
    owned = [set(), set()]
    try:
        for region in REGIONS:
            discover(first, region)
        for index, client in enumerate(clients):
            for n in range(5 if index == 0 else 1):
                owned[index].add(client.create_profile(name=f'profile-{n}', businessName='Quota fixture',
                                                       phone='+1 555 0100', logging='DISABLED')['profileId'])
        peer_before = {item: clients[1].get_profile(profileId=item) for item in owned[1]}
        refused(clients[0].create_profile, 'ServiceQuotaExceededException', name='overflow',
                businessName='Quota fixture', phone='+1 555 0100', logging='DISABLED')
        discover(first)
        removed = owned[0].pop()
        clients[0].delete_profile(profileId=removed)
        replacement = clients[0].create_profile(name='replacement', businessName='Quota fixture',
                                                 phone='+1 555 0100', logging='DISABLED')['profileId']
        assert replacement != removed
        owned[0].add(replacement)
        discover(first)
        actions = ['servicequotas:' + name for name in READS]
        arn = 'arn:aws:servicequotas:us-east-1:000000000000:b2bi/L-05F6A9EF'
        exact = participant(first, 'discovery-arn-only', [{'Effect': 'Allow', 'Action': actions + ['servicequotas:GetServiceQuota'], 'Resource': arn}])
        wildcard = participant(first, 'discovery-reader', [{'Effect': 'Allow', 'Action': actions + ['servicequotas:GetServiceQuota'], 'Resource': '*'}])
        denied = participant(first, 'discovery-denied', [{'Effect': 'Allow', 'Action': actions, 'Resource': '*'},
                                                        {'Effect': 'Deny', 'Action': actions, 'Resource': '*'}])
        assert first.client('service-quotas', token=exact).get_service_quota(**KEY)['Quota']['Value'] == 5
        try:
            first.client('service-quotas', token=peer.token).list_services()
        except ClientError as error:
            assert error.response['ResponseMetadata']['HTTPStatusCode'] == 401
        else:
            raise AssertionError('peer token crossed the owning CLI boundary')
        for token in (exact, denied):
            client = first.client('service-quotas', token=token)
            for op, kwargs in [('list_services', {}), ('list_service_quotas', {'ServiceCode': 'b2bi'}),
                               ('list_aws_default_service_quotas', {'ServiceCode': 'b2bi'}), ('get_aws_default_service_quota', KEY)]:
                refused(getattr(client, op), 'AccessDeniedException', **kwargs)
        discover(first, token=wildcard)
        tokyo = first.client('service-quotas', 'ap-northeast-1')
        result = tokyo.list_services()
        assert result['Services'] == [] and 'NextToken' not in result
        for op in ('list_service_quotas', 'list_aws_default_service_quotas'):
            refused(getattr(tokyo, op), 'NoSuchResourceException', ServiceCode='b2bi')
        admin = first.client('service-quotas')
        refused(admin.get_aws_default_service_quota, 'NoSuchResourceException', ServiceCode='kms', QuotaCode=KEY['QuotaCode'])
        refused(admin.get_aws_default_service_quota, 'NoSuchResourceException', **{**KEY, 'QuotaCode': 'L-00000000'})
        first.stop()
        first.start()
        clients[0] = first.client('b2bi')
        assert {p['profileId'] for p in clients[0].list_profiles()['profiles']} == owned[0]
        discover(first)
        discover(peer)
        for item in owned[1]:
            after = clients[1].get_profile(profileId=item)
            assert {k: v for k, v in after.items() if k != 'ResponseMetadata'} == {
                k: v for k, v in peer_before[item].items() if k != 'ResponseMetadata'}
    finally:
        original = sys.exception()
        cleanup_errors = []
        for client, ids in zip(clients, owned):
            try:
                for profile_id in ids:
                    client.delete_profile(profileId=profile_id)
                assert client.list_profiles()['profiles'] == []
            except Exception as error:
                cleanup_errors.append(type(error).__name__)
        if cleanup_errors:
            if original is not None:
                original.add_note('Owned profile cleanup failed: ' + ', '.join(cleanup_errors))
            else:
                raise AssertionError('Owned profile cleanup failed: ' + ', '.join(cleanup_errors))


def main():
    with tempfile.TemporaryDirectory(prefix='quota-discovery-owner-') as owner, tempfile.TemporaryDirectory(prefix='quota-discovery-peer-') as other:
        teams = Team(owner), Team(other)
        try:
            for team in teams:
                team.start()
            exercise(*teams)
        finally:
            original = sys.exception()
            failures = []
            for team in teams:
                try:
                    team.stop()
                except Exception as error:
                    failures.append(type(error).__name__)
            if failures:
                if original is not None:
                    original.add_note('Owned CLI cleanup failed: ' + ', '.join(failures))
                else:
                    raise AssertionError('Owned CLI cleanup failed: ' + ', '.join(failures))
    print('Quota discovery SDK: defaults/applied, wildcard IAM, filters, typed empties, two teams, restart and cleanup passed')


if __name__ == '__main__':
    main()
