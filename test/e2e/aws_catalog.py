"""Model-driven AWS catalog through the real binary, boto3 and two teams.

Each team owns a separate CLI process, SQLite file and token authority. The
flows exercise every bound operation of every catalog service, explicit 501
refusals of unbound official operations, participant identity policies,
restart persistence and cleanup. No AWS account, credentials or network.
"""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request

os.environ.update(AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null',
                  AWS_EC2_METADATA_DISABLED='true')
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve()
CALLED = {}
DEFINITION = json.dumps({'StartAt': 'Done', 'States': {'Done': {'Type': 'Succeed'}}})
ROLE = 'arn:aws:iam::000000000000:role/catalog-e2e'


class Team:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.database = self.directory / 'state.sqlite'
        self.token_file = self.directory / 'operator-token'
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        self.endpoint = f'http://127.0.0.1:{self.port}'
        self.process = None
        self.token = None

    def start(self):
        self.process = subprocess.Popen(
            [str(BINARY), '--listen', f'127.0.0.1:{self.port}', '--state', str(self.database),
             '--token-file', str(self.token_file)], cwd=self.directory,
            env={**os.environ, 'PATH': '/nonexistent'}, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            assert self.process.poll() is None, 'simulator exited before readiness'
            if self.token_file.exists():
                self.token = self.token_file.read_text().strip()
                try:
                    with urllib.request.urlopen(urllib.request.Request(
                            self.endpoint + '/_simulator/health', headers={'X-Simulator-Token': self.token}), timeout=1) as r:
                        if r.status == 200:
                            return
                except OSError:
                    pass
            time.sleep(0.05)
        raise AssertionError('simulator did not become ready')

    def stop(self):
        process, self.process = self.process, None
        process.send_signal(signal.SIGTERM)
        process.communicate(timeout=10)
        assert process.returncode == 0, 'graceful shutdown failed'

    def control(self, method, path, body=None, token=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.endpoint + path, data=data, method=method,
                                         headers={'X-Simulator-Token': token or self.token, 'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=5) as response:
            text = response.read()
            return json.loads(text) if text else {}

    def client(self, name, token=None, region='us-east-1'):
        client = boto3.client(name, endpoint_url=self.endpoint, region_name=region,
                              aws_access_key_id='TCSIMCATALOG', aws_secret_access_key='dummy-local-only',
                              config=Config(retries={'max_attempts': 0}, connect_timeout=2, read_timeout=10))
        event = client.meta.service_model.service_id.hyphenize()
        client.meta.events.register('before-sign.' + event,
                                    lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', token or self.token))

        def loopback(request, **kwargs):
            target = urllib.parse.urlsplit(request.url)
            assert target.netloc == f'127.0.0.1:{self.port}', 'SDK left the owned loopback origin'

        client.meta.events.register('before-send.' + event, loopback)
        return client


def call(client, operation, **kwargs):
    result = getattr(client, operation)(**kwargs)
    CALLED.setdefault(client.meta.service_model.service_name, set()).add(client.meta.method_to_api_mapping[operation])
    return result


def refused(code, function, status=None):
    try:
        function()
    except ClientError as error:
        assert error.response['Error']['Code'] == code, error.response['Error']
        if status:
            assert error.response['ResponseMetadata']['HTTPStatusCode'] == status, error.response['ResponseMetadata']
        return
    raise AssertionError('expected ' + code)


def codeconnections(team, token=None):
    cc = team.client('codeconnections', token)
    host = call(cc, 'create_host', Name='ghe', ProviderType='GitHubEnterpriseServer', ProviderEndpoint='https://ghe.example.invalid')['HostArn']
    assert call(cc, 'get_host', HostArn=host)['Status'] == 'PENDING'
    assert len(call(cc, 'list_hosts')['Hosts']) == 1
    call(cc, 'update_host', HostArn=host, ProviderEndpoint='https://ghe2.example.invalid')
    arn = call(cc, 'create_connection', ConnectionName='repo', ProviderType='GitHub', Tags=[{'Key': 'team', 'Value': 'a'}])['ConnectionArn']
    hosted = call(cc, 'create_connection', ConnectionName='hosted', HostArn=host)['ConnectionArn']
    connection = call(cc, 'get_connection', ConnectionArn=arn)['Connection']
    assert connection['ConnectionStatus'] == 'PENDING' and connection['OwnerAccountId'] == '000000000000'
    assert [c['ConnectionName'] for c in call(cc, 'list_connections', HostArnFilter=host)['Connections']] == ['hosted']
    call(cc, 'tag_resource', ResourceArn=arn, Tags=[{'Key': 'extra', 'Value': 'x'}])
    call(cc, 'untag_resource', ResourceArn=arn, TagKeys=['extra'])
    assert call(cc, 'list_tags_for_resource', ResourceArn=arn)['Tags'] == [{'Key': 'team', 'Value': 'a'}]
    refused('UnknownOperationException', lambda: cc.list_repository_links(), 501)
    refused('ValidationException', lambda: cc.create_connection(ConnectionName='neither'))
    call(cc, 'delete_connection', ConnectionArn=hosted)
    scratch = call(cc, 'create_host', Name='scratch', ProviderType='GitLabSelfManaged', ProviderEndpoint='https://gitlab.example.invalid')['HostArn']
    call(cc, 'delete_host', HostArn=scratch)
    refused('ResourceNotFoundException', lambda: cc.get_host(HostArn=scratch))
    return arn, host


def stepfunctions(team, token=None):
    sf = team.client('stepfunctions', token)
    machine = call(sf, 'create_state_machine', name='flow', definition=DEFINITION, roleArn=ROLE, tags=[{'key': 'k', 'value': 'v'}])['stateMachineArn']
    for index in range(3):
        call(sf, 'create_activity', name=f'act-{index}')
    pages = list(sf.get_paginator('list_activities').paginate(PaginationConfig={'PageSize': 2}))
    assert [a['name'] for p in pages for a in p['activities']] == ['act-0', 'act-1', 'act-2'] and len(pages) == 2
    CALLED.setdefault('stepfunctions', set()).add('ListActivities')
    described = call(sf, 'describe_state_machine', stateMachineArn=machine)
    assert described['status'] == 'ACTIVE' and described['definition'] == DEFINITION
    before = described['revisionId']
    assert call(sf, 'update_state_machine', stateMachineArn=machine, definition=DEFINITION)['revisionId'] != before
    activity = call(sf, 'describe_activity', activityArn='arn:aws:states:us-east-1:000000000000:activity:act-2')
    assert activity['name'] == 'act-2'
    call(sf, 'delete_activity', activityArn=activity['activityArn'])
    assert [m['name'] for m in call(sf, 'list_state_machines')['stateMachines']] == ['flow']
    call(sf, 'tag_resource', resourceArn=machine, tags=[{'key': 'x', 'value': 'y'}])
    call(sf, 'untag_resource', resourceArn=machine, tagKeys=['x'])
    assert call(sf, 'list_tags_for_resource', resourceArn=machine)['tags'] == [{'key': 'k', 'value': 'v'}]
    refused('StateMachineAlreadyExists', lambda: sf.create_state_machine(name='flow', definition=DEFINITION, roleArn=ROLE))
    refused('InvalidDefinition', lambda: sf.create_state_machine(name='bad', definition='{}', roleArn=ROLE))
    refused('UnknownOperationException', lambda: sf.start_execution(stateMachineArn=machine), 501)
    temporary = call(sf, 'create_state_machine', name='temporary', definition=DEFINITION, roleArn=ROLE)['stateMachineArn']
    call(sf, 'delete_state_machine', stateMachineArn=temporary)
    refused('StateMachineDoesNotExist', lambda: sf.describe_state_machine(stateMachineArn=temporary))
    return machine


def ecr(team, token=None):
    registry = team.client('ecr', token)
    repository = call(registry, 'create_repository', repositoryName='web/app', tags=[{'Key': 'k', 'Value': 'v'}])['repository']
    assert repository['repositoryUri'] == '000000000000.dkr.ecr.us-east-1.amazonaws.com/web/app'
    assert call(registry, 'describe_repositories', repositoryNames=['web/app'])['repositories'][0]['imageTagMutability'] == 'MUTABLE'
    call(registry, 'put_image_tag_mutability', repositoryName='web/app', imageTagMutability='IMMUTABLE')
    call(registry, 'put_image_scanning_configuration', repositoryName='web/app', imageScanningConfiguration={'scanOnPush': True})
    call(registry, 'tag_resource', resourceArn=repository['repositoryArn'], tags=[{'Key': 'x', 'Value': 'y'}])
    call(registry, 'untag_resource', resourceArn=repository['repositoryArn'], tagKeys=['x'])
    assert call(registry, 'list_tags_for_resource', resourceArn=repository['repositoryArn'])['tags'] == [{'Key': 'k', 'Value': 'v'}]
    refused('RepositoryNotFoundException', lambda: registry.describe_repositories(repositoryNames=['missing']))
    refused('InvalidParameterException', lambda: registry.describe_repositories(registryId='123456789012'))
    refused('UnknownOperationException', lambda: registry.get_authorization_token(), 501)
    call(registry, 'create_repository', repositoryName='scratch')
    assert call(registry, 'delete_repository', repositoryName='scratch', force=True)['repository']['repositoryName'] == 'scratch'
    return repository['repositoryArn']


def amplify(team, token=None):
    hosting = team.client('amplify', token)
    app = call(hosting, 'create_app', name='site', tags={'k': 'v'})['app']
    call(hosting, 'create_branch', appId=app['appId'], branchName='main', stage='PRODUCTION')
    assert call(hosting, 'get_branch', appId=app['appId'], branchName='main')['branch']['stage'] == 'PRODUCTION'
    call(hosting, 'update_branch', appId=app['appId'], branchName='main', description='updated')
    assert [b['branchName'] for b in call(hosting, 'list_branches', appId=app['appId'])['branches']] == ['main']
    call(hosting, 'update_app', appId=app['appId'], description='docs')
    assert call(hosting, 'get_app', appId=app['appId'])['app']['description'] == 'docs'
    assert [a['name'] for a in call(hosting, 'list_apps')['apps']] == ['site']
    refused('BadRequestException', lambda: hosting.delete_app(appId=app['appId']))
    refused('BadRequestException', lambda: hosting.create_app(name='leak', accessToken='secret-token-value'))
    refused('UnknownOperationException', lambda: hosting.start_job(appId=app['appId'], branchName='main', jobType='RELEASE'), 501)
    call(hosting, 'tag_resource', resourceArn=app['appArn'], tags={'x': 'y'})
    call(hosting, 'untag_resource', resourceArn=app['appArn'], tagKeys=['x'])
    assert call(hosting, 'list_tags_for_resource', resourceArn=app['appArn'])['tags'] == {'k': 'v'}
    scratch = call(hosting, 'create_app', name='scratch')['app']
    call(hosting, 'create_branch', appId=scratch['appId'], branchName='tmp')
    call(hosting, 'delete_branch', appId=scratch['appId'], branchName='tmp')
    assert call(hosting, 'delete_app', appId=scratch['appId'])['app']['name'] == 'scratch'
    refused('NotFoundException', lambda: hosting.get_app(appId=scratch['appId']), 404)
    return app['appArn']


def main():
    with tempfile.TemporaryDirectory(prefix='tcsim-catalog-a-') as a_dir, tempfile.TemporaryDirectory(prefix='tcsim-catalog-b-') as b_dir:
        a, b = Team(a_dir), Team(b_dir)
        a.start()
        b.start()
        try:
            capabilities = a.control('GET', '/_simulator/v1/capabilities')['services']['catalog']
            expected = {s['model']: set(s['operations']) for s in capabilities['services']}
            assert set(expected) == {'amplify', 'codeconnections', 'ecr', 'stepfunctions'}, expected
            connection, host = codeconnections(a)
            machine = stepfunctions(a)
            repository = ecr(a)
            app = amplify(a)
            assert CALLED == expected, {k: expected[k] - CALLED.get(k, set()) for k in expected}
            # Team B has its own database and token authority.
            assert b.client('codeconnections').list_connections()['Connections'] == []
            assert b.client('ecr').describe_repositories()['repositories'] == []
            try:
                b.client('ecr', token=a.token).describe_repositories()
                raise AssertionError('a token from another team was accepted')
            except ClientError as error:
                assert error.response['ResponseMetadata']['HTTPStatusCode'] == 401
            # A participant identity policy is evaluated before any change.
            a.control('PUT', '/_simulator/v1/iam/principals/catalog-reader/policy',
                      {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': ['ecr:DescribeRepositories', 'states:Describe*'], 'Resource': '*'}]})
            reader = a.control('POST', '/_simulator/v1/access', {'Principal': 'catalog-reader'})['AccessToken']
            limited = a.client('ecr', token=reader)
            assert len(limited.describe_repositories()['repositories']) == 1
            refused('AccessDeniedException', lambda: limited.delete_repository(repositoryName='web/app'), 403)
            refused('AccessDeniedException', lambda: limited.create_repository(repositoryName='denied'), 403)
            assert a.client('stepfunctions', token=reader).describe_state_machine(stateMachineArn=machine)['name'] == 'flow'
            refused('AccessDeniedException', lambda: a.client('stepfunctions', token=reader).delete_state_machine(stateMachineArn=machine), 403)
            # State survives a restart of the same process database.
            a.stop()
            a.start()
            assert a.client('codeconnections').get_connection(ConnectionArn=connection)['Connection']['ConnectionName'] == 'repo'
            assert len(a.client('ecr').describe_repositories()['repositories']) == 1
            assert a.client('amplify').list_tags_for_resource(resourceArn=app)['tags'] == {'k': 'v'}
            # Cleanup removes every resource through native operations.
            cc = a.client('codeconnections')
            cc.delete_connection(ConnectionArn=connection)
            cc.delete_host(HostArn=host)
            a.client('stepfunctions').delete_state_machine(stateMachineArn=machine)
            for activity in a.client('stepfunctions').list_activities()['activities']:
                a.client('stepfunctions').delete_activity(activityArn=activity['activityArn'])
            a.client('ecr').delete_repository(repositoryName='web/app')
            hosting = a.client('amplify')
            app_id = app.rsplit('/', 1)[1]
            hosting.delete_branch(appId=app_id, branchName='main')
            hosting.delete_app(appId=app_id)
            assert repository and cc.list_connections()['Connections'] == [] and cc.list_hosts()['Hosts'] == []
            assert hosting.list_apps()['apps'] == [] and a.client('stepfunctions').list_activities()['activities'] == []
        finally:
            for team in (a, b):
                if team.process is not None:
                    team.stop()
    print('Catalog native SDK lifecycle passed for', ', '.join(sorted(CALLED)))


if __name__ == '__main__':
    main()
