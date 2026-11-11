"""Standalone real boto3 DeleteMessageBatch acceptance on owned loopback servers.

No browser/check.py import and no real AWS. Reports omit tokens, receipts and row
payloads. SQL triggers/backdated rows are explicit local fault fixtures, distinct
from the actual 60-second retention and public API checks.
"""
import contextlib
import copy
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
from urllib.parse import urlsplit

os.environ.update(AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null',
                  AWS_EC2_METADATA_DISABLED='true', AWS_DEFAULT_REGION='us-east-1')
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from openapi_json10 import validate_json10_examples, validate_json10_response, validator, request_schema

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve()
REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
REGION = 'us-east-1'
PREFIX = '/_simulator/providers/aws/'
FAILURE = dict(Code='ReceiptHandleIsInvalid', Message='The input receipt handle is invalid.', SenderFault=True)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class Team:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir()
        self.database = self.directory / 'state.sqlite'
        self.token_file = self.directory / 'operator-token'
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        self.endpoint = f'http://127.0.0.1:{self.port}'
        self.token = None
        self.process = None
        self.clients = []
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        self.planned = []
        self.calls = []
        self.sends = []
        self.responses = []
        self.paths = []
        self.raw_calls = self.raw_responses = 0

    def guard(self, url):
        parsed = urlsplit(url)
        assert parsed.scheme == 'http' and parsed.netloc == f'127.0.0.1:{self.port}'
        assert parsed.path in ('', '/', PREFIX) or parsed.path.startswith('/_simulator/')
        return parsed

    def start(self):
        assert self.process is None
        self.process = subprocess.Popen([str(BINARY), '--listen', f'127.0.0.1:{self.port}',
            '--state', str(self.database), '--token-file', str(self.token_file)],
            cwd=self.directory, env={**os.environ, 'PATH': '/nonexistent'},
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            assert self.process.poll() is None, 'Owned simulator exited before readiness'
            if self.token_file.exists():
                self.token = self.token_file.read_text().strip()
                try:
                    self.wire('/_simulator/health')
                    return
                except OSError:
                    pass
            time.sleep(0.05)
        raise AssertionError('Owned simulator readiness timed out')

    def stop(self):
        for client in self.clients:
            client.close()
        self.clients.clear()
        if self.process is None:
            return
        process, self.process = self.process, None
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
        try:
            process.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate(timeout=10)
            raise AssertionError('Owned simulator timed out during shutdown')
        assert process.returncode == 0, 'Owned simulator shutdown failed'

    def wire(self, path, payload=None, operation=None, status=200, token=None, method=None,
             extra=None, encoded=None):
        assert path.startswith('/') and not path.startswith('//') and '://' not in path
        url = self.endpoint + path
        self.guard(url)
        headers = {'X-Simulator-Token': self.token if token is None else token}
        if operation:
            headers.update({'Content-Type': 'application/x-amz-json-1.0',
                'X-Amz-Target': 'AmazonSQS.' + operation, 'X-Simulator-Service': 'sqs',
                'X-Simulator-Region': REGION})
        else:
            headers['Content-Type'] = 'application/json'
        headers.update(extra or {})
        data = encoded if encoded is not None else (json.dumps(payload).encode() if payload is not None else None)
        request = urllib.request.Request(url, data=data, headers=headers,
            method=method or ('POST' if data is not None else 'GET'))
        self.raw_calls += 1
        try:
            response = self.opener.open(request, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        except OSError:
            # Readiness probes may fail before the listener binds. They are
            # reported separately from completed API exchanges.
            self.raw_calls -= 1
            raise
        with response:
            self.raw_responses += 1
            assert response.status == status, f'Unexpected local HTTP status {response.status}'
            body = response.read()
            if status in (401, 403) and not response.headers.get('Content-Type', '').startswith('application/x-amz-json-1.0'):
                return body
            if operation:
                assert response.headers['Content-Type'].startswith('application/x-amz-json-1.0')
            return json.loads(body)

    def sdk(self, region=REGION, prefixed=False, token=None):
        client = boto3.client('sqs', endpoint_url=self.endpoint + (PREFIX if prefixed else ''),
            region_name=region, aws_access_key_id='TCSIMBATCHLOCAL',
            aws_secret_access_key='dummy-local-only',
            config=Config(proxies={}, retries={'total_max_attempts': 1},
                          connect_timeout=2, read_timeout=5))
        authority = self.token if token is None else token
        client.meta.events.register('before-sign.sqs', lambda request, **_:
            request.headers.add_header('X-Simulator-Token', authority))
        def called(model, **_):
            self.calls.append(model.name)
        def sent(request, **_):
            parsed = self.guard(request.url)
            assert parsed.path == (PREFIX if prefixed else '/'), 'Unexpected SDK route'
            target = request.headers['X-Amz-Target']
            if isinstance(target, bytes):
                target = target.decode('ascii')
            assert target.startswith('AmazonSQS.')
            self.sends.append(target.removeprefix('AmazonSQS.'))
            self.paths.append(parsed.path)
        def received(model, http_response, parsed, **_):
            assert not 300 <= http_response.status_code < 400
            if model.name == 'DeleteMessageBatch':
                raw = json.loads(http_response.content)
                if http_response.status_code == 200:
                    assert set(raw) == {'Successful', 'Failed'}
                    assert isinstance(raw['Successful'], list) and isinstance(raw['Failed'], list)
                    assert raw == {key: value for key, value in parsed.items() if key != 'ResponseMetadata'}
                elif http_response.status_code == 500:
                    # Botocore can discard unknown fields. Check actual bytes
                    # too, so a leaked success list or diagnostic cannot hide.
                    assert raw == {'__type': 'InternalError',
                                   'Message': 'Simulator queue state operation failed.'}
            self.responses.append(model.name)
        client.meta.events.register('before-call.sqs', called)
        client.meta.events.register('before-send.sqs', sent)
        client.meta.events.register('after-call.sqs', received)
        self.clients.append(client)
        return client

    def call(self, client, operation, **kwargs):
        native = client.meta.method_to_api_mapping[operation]
        self.planned.append(native)
        return getattr(client, operation)(**kwargs)

    def rows(self):
        with sqlite3.connect('file:' + str(self.database) + '?mode=ro', uri=True) as db:
            return {table: db.execute('SELECT * FROM ' + table + ' ORDER BY 1,2,3').fetchall()
                    for table in ('aws_sqs_queues', 'aws_sqs_messages')}

    def sql(self, script, values=()):
        with sqlite3.connect(self.database) as db:
            db.execute(script, values)

    def participant(self, name, statements):
        self.wire('/_simulator/v1/iam/principals/' + name + '/policy',
                  dict(Version='2012-10-17', Statement=statements), method='PUT')
        return self.wire('/_simulator/v1/access', {'Principal': name})['AccessToken']

    def observed(self):
        assert self.planned and self.planned == self.calls == self.sends == self.responses
        assert self.raw_calls == self.raw_responses and self.raw_calls > 0
        return dict(calls=len(self.calls), sends=len(self.sends), responses=len(self.responses),
                    operations=sorted(set(self.calls)), batchCalls=self.calls.count('DeleteMessageBatch'),
                    rawCalls=self.raw_calls, rawResponses=self.raw_responses)


def refusal(team, client, code, status=400, **payload):
    before = team.rows()
    try:
        team.call(client, 'delete_message_batch', **payload)
    except ClientError as error:
        assert error.response['Error']['Code'] == code
        assert error.response['ResponseMetadata']['HTTPStatusCode'] == status
        assert 'Successful' not in error.response and 'Failed' not in error.response
        if status == 500:
            assert error.response['Error']['Message'] == 'Simulator queue state operation failed.'
    else:
        raise AssertionError('Expected native batch refusal ' + code)
    assert team.rows() == before, 'Refused batch changed persisted rows'


def partition(result, good, bad):
    assert result['ResponseMetadata']['HTTPStatusCode'] == 200
    assert result['Successful'] == [{'Id': value} for value in good]
    assert result['Failed'] == [{'Id': value, **FAILURE} for value in bad]
    assert set(result) == {'Successful', 'Failed', 'ResponseMetadata'}
    return {key: value for key, value in result.items() if key != 'ResponseMetadata'}


def entry(name, message):
    return {'Id': name, 'ReceiptHandle': message['ReceiptHandle']}


def seed(team, client, queue, body):
    sent = team.call(client, 'send_message', QueueUrl=queue, MessageBody=body)
    received = team.call(client, 'receive_message', QueueUrl=queue, VisibilityTimeout=3600,
                         MaxNumberOfMessages=1, WaitTimeSeconds=0)['Messages']
    assert len(received) == 1 and received[0]['MessageId'] == sent['MessageId']
    assert received[0]['Body'] == body
    return received[0]


def main():
    REPORTS.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='sqs-batch-sdk-') as directory:
        one, two = Team(Path(directory) / 'one'), Team(Path(directory) / 'two')
        try:
            one.start(); two.start()
            assert one.token != two.token
            a, west, b = one.sdk(), one.sdk('us-west-2'), two.sdk()
            prefixed = one.sdk(prefixed=True)
            cells = [(one, a, 'primary'), (one, west, 'primary'),
                     (one, a, 'peer-queue'), (two, b, 'primary')]
            queues = [team.call(client, 'create_queue', QueueName=name,
                               Attributes={'MessageRetentionPeriod': '300'})['QueueUrl']
                      for team, client, name in cells]
            anchors = [seed(team, client, queue, f'boundary-{i}')
                       for i, ((team, client, _), queue) in enumerate(zip(cells, queues))]
            q = queues[0]
            # Actual expiry is timed against an API-created minimum-retention queue.
            expiry = one.call(a, 'create_queue', QueueName='retention',
                             Attributes={'MessageRetentionPeriod': '60'})['QueueUrl']
            expiring = seed(one, a, expiry, 'actual-retention')
            expiry_start = time.monotonic()
            first = seed(one, a, q, 'first')
            one.call(a, 'change_message_visibility', QueueUrl=q,
                     ReceiptHandle=first['ReceiptHandle'], VisibilityTimeout=0)
            rotated = one.call(a, 'receive_message', QueueUrl=q, VisibilityTimeout=3600)['Messages'][0]
            assert rotated['MessageId'] == first['MessageId'] and rotated['ReceiptHandle'] != first['ReceiptHandle']
            second, third = seed(one, a, q, 'second'), seed(one, a, q, 'third')
            mixed = [dict(Id='unknown', ReceiptHandle='unknown-local-receipt'), entry('second', second),
                     entry('duplicate-handle', second), entry('stale', first), entry('first', rotated),
                     entry('third', third), dict(Id='message-id', ReceiptHandle=third['MessageId'])]
            result = one.call(a, 'delete_message_batch', QueueUrl=q, Entries=mixed)
            partition(result, ['second', 'first', 'third'], ['unknown', 'duplicate-handle', 'stale', 'message-id'])
            remaining = one.rows()['aws_sqs_messages']
            assert not {first['MessageId'], second['MessageId'], third['MessageId']} & {row[0] for row in remaining}
            assert anchors[0]['MessageId'] in {row[0] for row in remaining}
            ten = [seed(one, a, q, f'bounded-{i}') for i in range(10)]
            ten_entries = [entry(f'entry-{i}', message) for i, message in enumerate(ten)]
            partition(one.call(a, 'delete_message_batch', QueueUrl=q, Entries=ten_entries),
                      [value['Id'] for value in ten_entries], [])
            before = one.rows()
            partition(one.call(a, 'delete_message_batch', QueueUrl=q, Entries=ten_entries),
                      [], [value['Id'] for value in ten_entries])
            assert one.rows() == before
            refusal(one, a, 'BatchEntryIdsNotDistinct', QueueUrl=q,
                    Entries=[entry('same', anchors[0]), entry('same', anchors[0])])
            before = one.rows()
            partition(one.call(prefixed, 'delete_message_batch', QueueUrl=q,
                Entries=[entry('peer-region', anchors[1]), entry('peer-queue', anchors[2]), entry('peer-team', anchors[3])]),
                [], ['peer-region', 'peer-queue', 'peer-team'])
            assert one.rows() == before
            # Both authorities reject the other team's token on the actual batch API.
            for team, foreign, queue, message in ((one, two.token, q, anchors[0]),
                                                  (two, one.token, queues[3], anchors[3])):
                before = team.rows()
                team.wire('/', dict(QueueUrl=queue, Entries=[entry('denied', message)]),
                          'DeleteMessageBatch', status=401, token=foreign)
                assert team.rows() == before
            arn = 'arn:aws:sqs:us-east-1:000000000000:primary'
            allow = dict(Effect='Allow', Action='sqs:DeleteMessage', Resource=arn)
            cases = [('wrong-action', [dict(Effect='Allow', Action='sqs:DeleteMessageBatch', Resource=arn)]),
                     ('explicit-deny', [allow, dict(Effect='Deny', Action='sqs:DeleteMessage', Resource=arn)]),
                     ('implicit-deny', [dict(Effect='Allow', Action='sqs:GetQueueAttributes', Resource=arn)])]
            for name, statements in cases:
                token = one.participant(name, statements)
                refusal(one, one.sdk(token=token), 'AccessDenied', 403,
                        QueueUrl=q, Entries=[entry('denied', anchors[0])])
            token = one.participant('consumer', [allow])
            consumer = one.sdk(token=token, prefixed=True)
            refusal(one, consumer, 'AccessDenied', 403, QueueUrl=queues[2],
                    Entries=[entry('wrong-resource', anchors[2])])
            before = one.rows()
            one.wire('/', dict(QueueUrl=q, Entries=[entry('override', anchors[0])]),
                     'DeleteMessageBatch', status=403, token=token,
                     extra={'X-Simulator-Principal': 'admin'})
            assert one.rows() == before
            partition(one.call(consumer, 'delete_message_batch', QueueUrl=q,
                               Entries=[entry('authorized', anchors[0])]), ['authorized'], [])
            # QueueUrl remains a local name selector, not an outbound destination.
            before = one.rows()
            partition(one.call(a, 'delete_message_batch', QueueUrl='https://unreachable.invalid/000000000000/primary',
                               Entries=[entry('foreign-host-is-not-authority', anchors[3])]),
                      [], ['foreign-host-is-not-authority'])
            assert one.rows() == before
            # Raw malformed requests exercise admission that an SDK may reject locally.
            for payload, code in ((dict(QueueUrl=q, Entries=[]), 'EmptyBatchRequest'),
                    (dict(QueueUrl=q, Entries=[dict(Id=f'id-{i}', ReceiptHandle='invalid') for i in range(11)]), 'TooManyEntriesInBatchRequest'),
                    (dict(QueueUrl=q, Entries=[dict(Id='bad.id', ReceiptHandle='invalid')]), 'InvalidBatchEntryId'),
                    (dict(QueueUrl=q, Entries=[dict(Id='null', ReceiptHandle=None)]), 'InvalidParameterValue')):
                before = one.rows()
                response = one.wire('/', payload, 'DeleteMessageBatch', status=400)
                assert response['__type'] == code and one.rows() == before
            refusal(one, a, 'AWS.SimpleQueueService.NonExistentQueue', QueueUrl='absent',
                    Entries=[dict(Id='missing-parent', ReceiptHandle='invalid')])
            # The served template is validated unchanged, then executed with two
            # actual current receipts from the same persisted queue.
            spec = one.wire('/_simulator/docs/openapi.json')
            source = json.loads((Path(__file__).resolve().parents[2] / 'internal/server/apidocs/openapi.json').read_text())
            assert spec == source and validate_json10_examples(spec) == 17
            caps = one.wire('/_simulator/v1/capabilities')['services']['sqs']
            from openapi_json10 import SQS_OPERATIONS
            assert len(caps['operations']) == 10 and set(caps['operations']) == set(SQS_OPERATIONS)
            assert caps['deleteMessageBatchIAMAction'] == 'sqs:DeleteMessage'
            example = spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.0']['examples']['DeleteMessageBatch']
            assert example['x-simulator-static-template'] is True and example['x-simulator-operation'] == 'DeleteMessageBatch'
            body = copy.deepcopy(example['value'])
            validator(spec, request_schema(spec, 'DeleteMessageBatch')).validate(body)
            live = [seed(one, a, q, 'template-first'), seed(one, a, q, 'template-second')]
            assert body['QueueUrl'] == '__QUEUE_URL__'
            body['QueueUrl'] = q
            assert [value['ReceiptHandle'] for value in body['Entries']] == ['__RECEIPT_HANDLE__', '__SECOND_RECEIPT_HANDLE__']
            for value, message in zip(body['Entries'], live):
                value['ReceiptHandle'] = message['ReceiptHandle']
            response = one.wire(PREFIX, body, 'DeleteMessageBatch')
            assert response == {'Successful': [{'Id': value['Id']} for value in body['Entries']], 'Failed': []}
            validate_json10_response(spec, 200, response, sqs_operation='DeleteMessageBatch')
            assert not {m['MessageId'] for m in live} & {row[0] for row in one.rows()['aws_sqs_messages']}
            # Existing received handle and committed row removal survive restart.
            persisted = seed(one, a, q, 'restart-current-receipt')
            token_before, snapshot = one.token, one.rows()
            one.stop(); one.start()
            assert one.token == token_before and one.rows() == snapshot
            a, prefixed = one.sdk(), one.sdk(prefixed=True)
            partition(one.call(prefixed, 'delete_message_batch', QueueUrl=q,
                               Entries=[entry('persisted', persisted)]), ['persisted'], [])
            one.stop(); one.start()
            assert persisted['MessageId'] not in {row[0] for row in one.rows()['aws_sqs_messages']}
            a = one.sdk()
            # Actual retention cleanup via an all-invalid batch must commit, not
            # roll back just because no entry succeeds. Do not read/purge first.
            elapsed = time.monotonic() - expiry_start
            assert elapsed < 55, 'Setup exceeded the before-expiry evidence window'
            assert expiring['MessageId'] in {row[0] for row in one.rows()['aws_sqs_messages']}
            time.sleep(max(0, 60.2 - elapsed))
            before = one.rows()
            partition(one.call(a, 'delete_message_batch', QueueUrl=expiry,
                               Entries=[entry('expired', expiring)]), [], ['expired'])
            after = one.rows()
            assert after['aws_sqs_queues'] == before['aws_sqs_queues']
            assert after['aws_sqs_messages'] == [row for row in before['aws_sqs_messages'] if row[0] != expiring['MessageId']]
            assert time.monotonic() - expiry_start >= 60
            # Required second-delete ABORT and silent IGNORE are infrastructure
            # failures, rolling back the first delete and scoped expiry cleanup.
            faultq = one.call(a, 'create_queue', QueueName='faults',
                             Attributes={'MessageRetentionPeriod': '60'})['QueueUrl']
            expired = seed(one, a, faultq, 'fixture-expiry')
            first, second = seed(one, a, faultq, 'fault-first'), seed(one, a, faultq, 'fault-second')
            one.sql('UPDATE aws_sqs_messages SET sent=0 WHERE id=?', (expired['MessageId'],))
            for action in ("RAISE(ABORT, 'private SQL diagnostic')", 'RAISE(IGNORE)'):
                # IDs come from owned native responses and are hex-only; SQL
                # literals below are confined to this disposable fault fixture.
                assert all(char in '0123456789abcdef' for char in second['MessageId'])
                one.sql("CREATE TRIGGER batch_fault BEFORE DELETE ON aws_sqs_messages WHEN OLD.id='" +
                        second['MessageId'] + "' BEGIN SELECT " + action + '; END')
                try:
                    refusal(one, a, 'InternalError', 500, QueueUrl=faultq,
                            Entries=[entry('first', first), entry('second', second)])
                finally:
                    one.sql('DROP TRIGGER batch_fault')
            partition(one.call(a, 'delete_message_batch', QueueUrl=faultq,
                               Entries=[entry('first', first), entry('second', second)]), ['first', 'second'], [])
            assert not {expired['MessageId'], first['MessageId'], second['MessageId']} & {
                row[0] for row in one.rows()['aws_sqs_messages']}
            # Delete queue, recreate it, and reject the retired receipt.
            old = seed(one, a, faultq, 'retired-queue-receipt')
            one.call(a, 'delete_queue', QueueUrl=faultq)
            faultq = one.call(a, 'create_queue', QueueName='faults')['QueueUrl']
            partition(one.call(a, 'delete_message_batch', QueueUrl=faultq,
                               Entries=[entry('retired', old)]), [], ['retired'])
            # Boundary messages were neither claimed nor deleted by any other scope.
            for team, message in ((one, anchors[1]), (one, anchors[2]), (two, anchors[3])):
                assert message['MessageId'] in {row[0] for row in team.rows()['aws_sqs_messages']}
            for region, name in ((REGION, 'primary'), ('us-west-2', 'primary'), (REGION, 'peer-queue'),
                                 (REGION, 'retention'), (REGION, 'faults')):
                one.call(one.sdk(region), 'delete_queue', QueueUrl=name)
            two.call(b, 'delete_queue', QueueUrl=queues[3])
            assert all(not rows for team in (one, two) for rows in team.rows().values())
            observed = [one.observed(), two.observed()]
            expected = {'CreateQueue', 'SendMessage', 'ReceiveMessage', 'ChangeMessageVisibility',
                        'DeleteMessageBatch', 'DeleteQueue'}
            assert set(one.calls) == expected and one.calls.count('DeleteMessageBatch') == 18
            assert set(two.calls) == {'CreateQueue', 'SendMessage', 'ReceiveMessage', 'DeleteQueue'}
            assert '/' in one.paths and PREFIX in one.paths
            report = dict(nativeSDK='boto3 ' + boto3.__version__, twoAuthenticatedProcesses=True,
                twoSQLiteDatabases=True, mixedOrderedPartitions=True, duplicateIDsRefused=True,
                duplicateHandlesFirstOnly=True, exactSingleDeleteIAMAction=True,
                tokenTeamRegionQueueBoundaries=True, bothRoutes=True, servedTemplateExecuted=True,
                currentReceiptSurvivesRestart=True, committedRowRemovalSurvivesRestart=True,
                actualRetentionSeconds=60, allInvalidScopedExpiryCommitted=True,
                SQLFaultFixtures=['second-delete ABORT', 'second-delete IGNORE', 'backdated scoped expiry'],
                atomicDeletionAndPurgeRollback=True, retiredQueueReceiptRefused=True,
                observations=observed, cleanup='DELETED', realAWS=False, browser=False,
                receiptTokenAndPayloadValuesOmitted=True)
            (REPORTS / 'sqs-batch-delete.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report))
        finally:
            with contextlib.ExitStack() as cleanup:
                cleanup.callback(two.stop)
                cleanup.callback(one.stop)


if __name__ == '__main__':
    main()
