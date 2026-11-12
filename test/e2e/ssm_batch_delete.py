"""Real local binary and boto3 batch deletion; no browser or cloud requests."""
import contextlib
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

os.environ['AWS_CONFIG_FILE'] = '/dev/null'
os.environ['AWS_SHARED_CREDENTIALS_FILE'] = '/dev/null'
os.environ['AWS_EC2_METADATA_DISABLED'] = 'true'
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve()
AUDIT = []
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def request(endpoint, token, path, body=None, method='GET'):
    headers = {'X-Simulator-Token': token}
    if body is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(endpoint + path, headers=headers, method=method,
                                 data=None if body is None else json.dumps(body).encode())
    with OPENER.open(req, timeout=5) as response:
        return json.load(response)


@contextlib.contextmanager
def running(database):
    with tempfile.TemporaryDirectory(prefix='ssm-batch-runtime-') as directory:
        home = Path(directory)
        token_file = home / 'fixture-token'
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        endpoint = f'http://127.0.0.1:{port}'
        process = subprocess.Popen([str(BINARY), '--listen', f'127.0.0.1:{port}',
                                    '--state', str(database), '--token-file', str(token_file)],
                                   cwd=home, env={**os.environ, 'PATH': '/nonexistent'},
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 30
            while True:
                if process.poll() is not None:
                    raise AssertionError('Local binary exited before readiness; output withheld')
                try:
                    token = token_file.read_text().strip()
                    request(endpoint, token, '/_simulator/health')
                    break
                except (OSError, ValueError):
                    if time.monotonic() >= deadline:
                        raise AssertionError('Local binary did not become ready') from None
                    time.sleep(.05)
            yield endpoint, token
        finally:
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
            try:
                process.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.communicate(timeout=5)
                raise AssertionError('Local binary did not stop gracefully') from None
            assert process.returncode == 0, 'Local binary exited unsuccessfully'


def client(endpoint, token, region='us-east-1'):
    sdk = boto3.client('ssm', endpoint_url=endpoint, region_name=region,
                       aws_access_key_id='TCSIMBATCH', aws_secret_access_key='dummy-local-only',
                       config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5))
    sdk.meta.events.register('before-sign', lambda request, **kwargs:
                             request.headers.add_header('X-Simulator-Token', token))
    counts = {'calls': 0, 'sends': 0, 'responses': 0, 'operations': {}}
    def called(model, **kwargs):
        counts['calls'] += 1
        counts['operations'][model.name] = counts['operations'].get(model.name, 0) + 1
    def sent(request, **kwargs):
        assert request.url in (endpoint.rstrip('/'), endpoint.rstrip('/') + '/'), 'SDK escaped its owned endpoint'
        counts['sends'] += 1
    def received(**kwargs):
        counts['responses'] += 1
    sdk.meta.events.register('before-call', called)
    sdk.meta.events.register('before-send', sent)
    sdk.meta.events.register('after-call', received)
    AUDIT.append(counts)
    return sdk


def error(code, action):
    try:
        action()
    except ClientError as failure:
        assert failure.response['Error']['Code'] == code, 'Unexpected native error code'
        assert 'DeletedParameters' not in failure.response and 'InvalidParameters' not in failure.response
    else:
        raise AssertionError('Expected native ' + code)


def result(value, deleted, invalid):
    assert value['ResponseMetadata']['HTTPStatusCode'] == 200
    assert value['DeletedParameters'] == deleted and value['InvalidParameters'] == invalid
    assert set(value) == {'DeletedParameters', 'InvalidParameters', 'ResponseMetadata'}


def frame(database):
    with sqlite3.connect(database) as db:
        rows = db.execute('SELECT * FROM aws_ssm_parameters ORDER BY region,name').fetchall()
    return hashlib.sha256(json.dumps(rows).encode()).hexdigest()


def put(sdk, name, value='dummy-value'):
    sdk.put_parameter(Name=name, Type='String', Value=value)


def main():
    with tempfile.TemporaryDirectory(prefix='ssm-batch-sdk-') as directory:
        database = Path(directory) / 'team-a.db'
        peer_database = Path(directory) / 'team-b.db'
        with running(database) as (endpoint, operator), running(peer_database) as (peer_url, peer_operator):
            root, provider = client(endpoint, operator), client(endpoint + '/_simulator/providers/aws', operator)
            peer, west = client(peer_url, peer_operator), client(endpoint, operator, 'us-west-2')
            capabilities = request(endpoint, operator, '/_simulator/v1/capabilities')['services']['ssm']
            assert set(capabilities['operations']) == {'PutParameter', 'GetParameter', 'GetParameters', 'GetParametersByPath', 'DescribeParameters', 'DeleteParameter', 'DeleteParameters', 'CreateDocument', 'GetDocument', 'DescribeDocument', 'ListDocuments', 'UpdateDocument', 'DeleteDocument'}
            spec = request(endpoint, operator, '/_simulator/docs/openapi.json')
            mapping = spec['paths']['/']['post']['x-simulator-ssm-operation-schemas']['DeleteParameters']
            assert mapping['target'] == 'AmazonSSM.DeleteParameters' and mapping['iamAction'] == 'ssm:DeleteParameters'
            for sdk in (root, peer, west):
                put(sdk, '/batch/a')
                put(sdk, '/batch/b')
            result(provider.delete_parameters(Names=['/batch/b', '/batch/missing', '/batch/a', '/batch/b']),
                   ['/batch/b', '/batch/a'], ['/batch/missing'])
            result(root.delete_parameters(Names=['/batch/a', '/batch/b']), [], ['/batch/a', '/batch/b'])
            for sdk in (peer, west):
                assert sdk.get_parameter(Name='/batch/a')['Parameter']['Value'] == 'dummy-value'
            for token in ('', peer_operator):
                try:
                    request(endpoint, token, '/_simulator/v1/capabilities')
                except urllib.error.HTTPError as denied:
                    assert denied.code == 401
                    denied.close()
                else:
                    raise AssertionError('Missing/foreign host token accepted')
            for name in ('/batch/allowed', '/batch/denied'):
                put(root, name)
            policy_path = '/_simulator/v1/iam/principals/batch-learner/policy'
            allow = {'Version': '2012-10-17', 'Statement': [
                {'Effect': 'Allow', 'Action': 'ssm:DeleteParameters', 'Resource': 'arn:aws:ssm:us-east-1:000000000000:parameter/batch/*'},
                {'Effect': 'Deny', 'Action': 'ssm:DeleteParameters', 'Resource': 'arn:aws:ssm:us-east-1:000000000000:parameter/batch/denied'}]}
            request(endpoint, operator, policy_path, allow, 'PUT')
            learner_token = request(endpoint, operator, '/_simulator/v1/access', {'Principal': 'batch-learner'}, 'POST')['AccessToken']
            learner = client(endpoint, learner_token)
            before = frame(database)
            error('AccessDeniedException', lambda: learner.delete_parameters(Names=['/batch/allowed', '/batch/denied']))
            assert frame(database) == before, 'Denied batch partially mutated state'
            result(learner.delete_parameters(Names=['/batch/allowed', '/batch/missing']), ['/batch/allowed'], ['/batch/missing'])
            request(endpoint, operator, policy_path, {'Version': '2012-10-17', 'Statement': [
                {'Effect': 'Allow', 'Action': 'ssm:DeleteParameter', 'Resource': '*'}]}, 'PUT')
            error('AccessDeniedException', lambda: learner.delete_parameters(Names=['/batch/denied']))
            assert root.get_parameter(Name='/batch/denied')['Parameter']['Value'] == 'dummy-value'
            for name in ('/batch/fault-a', '/batch/fault-b'):
                put(root, name)
            with sqlite3.connect(database) as db:
                db.execute("CREATE TRIGGER ssm_batch_fixture_abort BEFORE DELETE ON aws_ssm_parameters WHEN old.name='/batch/fault-b' BEGIN SELECT RAISE(ABORT, 'synthetic fault'); END")
            before = frame(database)
            error('InternalServerError', lambda: root.delete_parameters(Names=['/batch/fault-a', '/batch/fault-b']))
            assert frame(database) == before, 'Storage failure partially committed batch'
            with sqlite3.connect(database) as db:
                db.execute('DROP TRIGGER ssm_batch_fixture_abort')
            result(root.delete_parameters(Names=['/batch/fault-a', '/batch/fault-b']), ['/batch/fault-a', '/batch/fault-b'], [])
            names = [f'/batch/ten-{index}' for index in range(10)]
            for name in names:
                put(root, name)
            result(root.delete_parameters(Names=names), names, [])
            example = spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']['examples']['SSMDeleteParameters']['value']
            for name in example['Names']:
                put(root, name)
            result(root.delete_parameters(**example), example['Names'], [])
        with running(database) as (endpoint, operator):
            sdk = client(endpoint, operator)
            error('ParameterNotFound', lambda: sdk.get_parameter(Name='/batch/a'))
            assert sdk.get_parameter(Name='/batch/denied')['Parameter']['Value'] == 'dummy-value'
            result(sdk.delete_parameters(Names=['/batch/denied']), ['/batch/denied'], [])
        operations = Counter()
        for counts in AUDIT:
            assert counts['calls'] > 0, 'SDK observation hook was never exercised'
            assert counts['calls'] == counts['sends'] == counts['responses'], 'Incomplete actual SDK observation'
            operations.update(counts['operations'])
        assert operations == {'PutParameter': 22, 'DeleteParameters': 10, 'GetParameter': 5}, 'Missing expected SDK operation'
        summary = {'sdkCalls': sum(x['calls'] for x in AUDIT), 'sends': sum(x['sends'] for x in AUDIT),
                   'responses': sum(x['responses'] for x in AUDIT), 'rootAndProvider': True,
                   'restart': True, 'twoTeams': True, 'regionIsolation': True,
                   'IAMBatchRefusal': True, 'databaseFailureRollback': True,
                   'servedExampleExecuted': True, 'browserAssertions': 0}
        reports = Path(os.environ.get('SIMULATOR_REPORTS', 'reports'))
        reports.mkdir(parents=True, exist_ok=True)
        (reports / 'ssm-batch-delete.json').write_text(json.dumps(summary, indent=2) + '\n')
        print(json.dumps(summary))


if __name__ == '__main__':
    main()
