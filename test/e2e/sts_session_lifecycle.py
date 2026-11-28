"""Ordinary local STS SDK sessions across two independent simulator processes.

Use the pinned boto3 environment and an independently verified clean build:
  python test/e2e/sts_session_lifecycle.py --binary /absolute/simulator \
      --binary-sha256 <verified-sha256>

Each CLI generates its own private operator token. The documented native key
TCSIMOPERATOR signs with that token; temporary credentials stay in memory.
All SDK endpoints are fixed numeric loopback. No cloud, challenge, grading,
credential discovery, profile override or SDK authentication hook is used.
An inherited SDK profile/configuration failure is a hard preflight blocker.
"""
import argparse
from contextlib import ExitStack
from datetime import datetime, timezone
import hashlib
from http.client import HTTPConnection
import importlib.metadata
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
from urllib.parse import parse_qsl, urlsplit

ROLE = 'LocalSessionLifecycleRole'
ROLE_ARN = 'arn:aws:iam::000000000000:role/' + ROLE
POLICY_NAME = 'ReadOwnRole'
SESSION_NAME = 'local-session'
TRUST = {'Version': '2012-10-17', 'Statement': [{
    'Effect': 'Allow', 'Principal': {'AWS': 'arn:aws:iam::999999999999:root'}, 'Action': 'sts:AssumeRole',
}]}
POLICY = {'Version': '2012-10-17', 'Statement': [{
    'Effect': 'Allow', 'Action': 'iam:GetRole', 'Resource': ROLE_ARN,
}]}
ACTIONS = {
    'sts': {'GetCallerIdentity', 'AssumeRole'},
    'iam': {'CreateRole', 'PutRolePolicy', 'GetRole', 'GetRolePolicy',
            'DeleteRolePolicy', 'DeleteRole', 'ListRoles'},
}
VERSIONS = {'sts': '2011-06-15', 'iam': '2010-05-08'}


class FixtureFailure(RuntimeError):
    pass


def need(condition, message):
    if not condition:
        raise FixtureFailure(message)


def checked_sdk_request(request, port, service):
    """Inspect the already signed normal SDK request without rewriting it."""
    url = urlsplit(request.url)
    need(service in ACTIONS and type(port) is int and 0 < port < 65536,
         'unsupported local SDK target')
    need(url.scheme == 'http' and url.netloc == f'127.0.0.1:{port}'
         and url.path == '/' and not url.query and not url.fragment and request.method == 'POST',
         'SDK request left the owned loopback root')
    headers = {key.lower(): value for key, value in request.headers.items()}
    need(not {'cookie', 'x-simulator-token', 'x-simulator-principal', 'x-simulator-service', 'x-amz-target'}
         .intersection(headers), 'SDK request unexpectedly used another access mechanism')
    authorization = headers.get('authorization', b'')
    if isinstance(authorization, bytes):
        authorization = authorization.decode('ascii')
    need(authorization.startswith('AWS4-HMAC-SHA256 '), 'normal SDK SigV4 signing required')
    body = request.body.encode('utf-8') if isinstance(request.body, str) else request.body
    need(isinstance(body, bytes) and 0 < len(body) <= 32768, 'unexpected SDK request size')
    pairs = parse_qsl(body.decode('utf-8'), keep_blank_values=True, strict_parsing=True)
    fields = dict(pairs)
    need(len(pairs) == len(fields) and fields.get('Version') == VERSIONS[service]
         and fields.get('Action') in ACTIONS[service], 'unsupported local SDK operation')
    return fields['Action']


def successful(method, **fields):
    result = method(**fields)
    need(result['ResponseMetadata']['HTTPStatusCode'] == 200, 'native SDK operation failed')
    need(result['ResponseMetadata'].get('RetryAttempts', 0) == 0, 'unexpected SDK retry')
    return result


class LocalSimulator:
    def __init__(self, directory, binary):
        self.directory = Path(directory)
        self.directory.mkdir(mode=0o700)
        self.binary = binary
        self.token_path = self.directory / 'operator-token'
        self.database = self.directory / 'state.db'
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        self.endpoint = f'http://127.0.0.1:{self.port}'
        self.token = None
        self.process = None
        self.sends = self.responses = 0

    def start(self):
        need(self.process is None, 'simulator is already running')
        self.process = subprocess.Popen([
            str(self.binary), '--listen', f'127.0.0.1:{self.port}', '--state', str(self.database),
            '--token-file', str(self.token_path),
        ], cwd=self.directory, env={**os.environ, 'PATH': '/nonexistent'},
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            need(self.process.poll() is None, 'simulator stopped during startup')
            if self.token_path.is_file():
                need(not self.token_path.is_symlink() and not self.token_path.stat().st_mode & 0o077,
                     'operator fixture token is not private')
                token = self.token_path.read_text().strip()
                need(re.fullmatch('[a-f0-9]{64}', token), 'unexpected local operator token')
                need(self.token is None or token == self.token, 'restart changed operator authority')
                self.token = token
                connection = HTTPConnection('127.0.0.1', self.port, timeout=1)
                try:
                    connection.request('GET', '/_simulator/health', headers={'X-Simulator-Token': token})
                    response = connection.getresponse()
                    need(response.status == 200, 'local health check failed')
                    response.read(4096)
                    return
                except OSError:
                    pass
                finally:
                    connection.close()
            time.sleep(.05)
        raise FixtureFailure('local simulator readiness timeout')

    def close(self):
        process, self.process = self.process, None
        if process is None:
            return
        if process.poll() is None:
            process.terminate()
        try:
            code = process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
            raise FixtureFailure('simulator required forced termination') from None
        need(code == 0, 'simulator did not shut down cleanly')


class LocalClients:
    def __init__(self, session, team, stack, config):
        self.session, self.team, self.stack, self.config = session, team, stack, config

    def client(self, service, credentials=None):
        need(service in ACTIONS, 'unsupported fixture service')
        if credentials is None:
            credentials = {'aws_access_key_id': 'TCSIMOPERATOR', 'aws_secret_access_key': self.team.token}
        client = self.session.client(service, endpoint_url=self.team.endpoint, region_name='us-east-1',
                                     config=self.config, **credentials)
        self.stack.callback(client.close)

        def before_send(request, **kwargs):
            checked_sdk_request(request, self.team.port, service)
            self.team.sends += 1
            need(self.team.sends <= 64, 'unexpected SDK request count')

        def after_call(http_response, **kwargs):
            need(not 300 <= http_response.status_code < 400, 'native SDK response tried to redirect')
            self.team.responses += 1

        client.meta.events.register('before-send.' + service, before_send)
        client.meta.events.register('after-call.' + service, after_call)
        return client


def assume(clients, sts, role_id):
    result = successful(sts.assume_role, RoleArn=ROLE_ARN, RoleSessionName=SESSION_NAME, DurationSeconds=900)
    value = result['Credentials']
    need(value['AccessKeyId'].startswith('TCSIMASIA') and value['SecretAccessKey'] and value['SessionToken'],
         'local temporary credential fields missing')
    need(value['Expiration'] > datetime.now(timezone.utc), 'new session is already expired')
    arn = 'arn:aws:sts::000000000000:assumed-role/' + ROLE + '/' + SESSION_NAME
    need(result['AssumedRoleUser'] == {'Arn': arn, 'AssumedRoleId': role_id + ':' + SESSION_NAME},
         'unexpected assumed-role identity')
    credentials = {'aws_access_key_id': value['AccessKeyId'], 'aws_secret_access_key': value['SecretAccessKey'],
                   'aws_session_token': value['SessionToken']}
    return clients.client('sts', credentials), clients.client('iam', credentials), value['AccessKeyId']


def read_identity(sts, iam, role_id):
    identity = successful(sts.get_caller_identity)
    need(identity['Account'] == '000000000000'
         and identity['Arn'] == 'arn:aws:sts::000000000000:assumed-role/' + ROLE + '/' + SESSION_NAME
         and identity['UserId'] == role_id + ':' + SESSION_NAME, 'session identity changed')
    role = successful(iam.get_role, RoleName=ROLE)['Role']
    need(role['RoleId'] == role_id and role['Arn'] == ROLE_ARN, 'allowed role readback changed')
    return {key: identity[key] for key in ('Account', 'Arn', 'UserId')}


def bootstrap(clients, sts, iam):
    caller = successful(sts.get_caller_identity)
    need(caller['Account'] == '999999999999' and caller['Arn'] == 'arn:aws:iam::999999999999:root',
         'unexpected synthetic operator identity')
    need(successful(iam.list_roles)['Roles'] == [], 'new process has unexpected roles')
    role = successful(iam.create_role, RoleName=ROLE, AssumeRolePolicyDocument=json.dumps(TRUST))['Role']
    need(role['Arn'] == ROLE_ARN and role['RoleName'] == ROLE, 'unexpected fixture role')
    successful(iam.put_role_policy, RoleName=ROLE, PolicyName=POLICY_NAME, PolicyDocument=json.dumps(POLICY))
    temporary_sts, temporary_iam, key = assume(clients, sts, role['RoleId'])
    identity = read_identity(temporary_sts, temporary_iam, role['RoleId'])
    return temporary_sts, temporary_iam, role['RoleId'], key, identity


def cleanup_role(iam, client_error):
    try:
        successful(iam.get_role, RoleName=ROLE)
    except client_error as error:
        need(error.response['Error']['Code'] == 'NoSuchEntity', 'unexpected cleanup role read failure')
        need(successful(iam.list_roles)['Roles'] == [], 'role cleanup left native state')
        return
    try:
        successful(iam.get_role_policy, RoleName=ROLE, PolicyName=POLICY_NAME)
    except client_error as error:
        need(error.response['Error']['Code'] == 'NoSuchEntity', 'unexpected cleanup policy read failure')
    else:
        successful(iam.delete_role_policy, RoleName=ROLE, PolicyName=POLICY_NAME)
    successful(iam.delete_role, RoleName=ROLE)
    need(successful(iam.list_roles)['Roles'] == [], 'role cleanup left native state')


def run(binary, expected):
    need(importlib.metadata.version('boto3') == '1.42.0'
         and importlib.metadata.version('botocore') == '1.41.6', 'use the pinned official SDK')
    import boto3
    from botocore.config import Config
    from botocore.exceptions import ClientError
    session = boto3.Session()  # Preserve the normal inherited profile/configuration checks.
    need(re.fullmatch('[a-f0-9]{64}', expected), 'verified simulator SHA-256 required')
    binary = Path(binary).resolve(strict=True)
    with binary.open('rb') as data:
        need(hashlib.file_digest(data, 'sha256').hexdigest() == expected, 'binary digest mismatch')
    config = Config(connect_timeout=3, read_timeout=5, retries={'max_attempts': 0}, proxies={})
    with tempfile.TemporaryDirectory(prefix='sts-session-sdk-') as directory, ExitStack() as stack:
        owner, peer = [LocalSimulator(Path(directory) / name, binary) for name in ('owner', 'peer')]
        for team in (owner, peer):
            stack.callback(team.close)
            team.start()
        need(owner.token != peer.token and owner.port != peer.port, 'process authorities are not independent')
        a, b = [LocalClients(session, team, stack, config) for team in (owner, peer)]
        owner_sts, owner_iam = a.client('sts'), a.client('iam')
        peer_operator_sts, peer_iam = b.client('sts'), b.client('iam')
        try:
            old_sts, _, role_id, old_key, identity = bootstrap(a, owner_sts, owner_iam)
            peer_sts, peer_session_iam, peer_role_id, peer_key, peer_identity = bootstrap(b, peer_operator_sts, peer_iam)
            need(old_key != peer_key, 'sessions were not independently issued')
            peer_process = peer.process
            owner.close()
            owner.start()
            need(successful(owner_sts.get_caller_identity)['Account'] == '999999999999',
                 'persistent operator authority did not reopen')
            need(successful(owner_iam.get_role, RoleName=ROLE)['Role']['RoleId'] == role_id,
                 'restart replaced the persistent IAM role')
            need(successful(owner_iam.get_role_policy, RoleName=ROLE, PolicyName=POLICY_NAME)['PolicyDocument'] == POLICY,
                 'restart lost the committed role policy')
            try:
                old_sts.get_caller_identity()
            except ClientError as error:
                need(error.response['ResponseMetadata']['HTTPStatusCode'] == 403
                     and error.response['Error']['Code'] == 'InvalidClientTokenId',
                     'old session failed for an unexpected reason')
            else:
                raise FixtureFailure('restart retained an old temporary credential')
            fresh_sts, fresh_iam, fresh_key = assume(a, owner_sts, role_id)
            need(fresh_key != old_key and read_identity(fresh_sts, fresh_iam, role_id) == identity,
                 'fresh assumption changed role identity or reused credentials')
            need(peer.process is peer_process and peer_process.poll() is None
                 and read_identity(peer_sts, peer_session_iam, peer_role_id) == peer_identity,
                 'owner restart affected the peer process/session')
        finally:
            cleanup_errors = []
            for iam in (owner_iam, peer_iam):
                try:
                    cleanup_role(iam, ClientError)
                except Exception:
                    cleanup_errors.append(True)
            need(not cleanup_errors, 'native fixture role cleanup failed')
        need(owner.sends == owner.responses and peer.sends == peer.responses,
             'a native SDK request has no matching response')
    need(not Path(directory).exists(), 'temporary process state was not removed')
    return {'status': 'passed', 'binary_sha256': expected, 'sdk': 'boto3 1.42.0 / botocore 1.41.6',
            'two_processes': True, 'operator_and_assumed_identity': True, 'allowed_role_readback': True,
            'persistent_role_after_restart': True, 'old_session_invalidated': True,
            'peer_session_preserved': True, 'native_cleanup': True, 'temporary_state_removed': True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    try:
        result = run(args.binary, args.binary_sha256)
    except Exception as error:
        # Do not expose SDK request bodies, temporary credentials or token-bearing logs.
        name = type(error).__name__
        result = {'status': 'blocked' if name == 'ProfileNotFound' else 'failed', 'error_type': name}
        if name == 'ProfileNotFound':
            result['stage'] = 'sdk-preflight'
        if isinstance(error, FixtureFailure):
            result['reason'] = str(error)
        print(json.dumps(result))
        return 1
    print(json.dumps(result))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
