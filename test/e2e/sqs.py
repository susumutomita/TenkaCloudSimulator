"""Native SQS SDK against two authenticated local CLI processes and SQLite files.

Source-only handoff until the integration owner runs the actual binary. No UI,
real AWS parity, FIFO, distributed delivery or full-SQS completion is claimed.
The minimum supported retention is 60s; this lifecycle takes at least that long.
"""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

os.environ.update(AWS_CONFIG_FILE='/dev/null', AWS_SHARED_CREDENTIALS_FILE='/dev/null',
                  AWS_EC2_METADATA_DISABLED='true')
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve()
REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
REGIONS = ('us-east-1', 'ap-northeast-1')
OPERATIONS = {'create_queue', 'list_queues', 'get_queue_url', 'delete_queue',
              'send_message', 'receive_message', 'delete_message',
              'change_message_visibility', 'get_queue_attributes'}
COUNTS = {'calls': 0, 'sends': 0, 'responses': 0}
SUCCEEDED = set()
PREFIX_REQUESTS = []


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class Team:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.token_file = self.directory / 'operator-token'
        self.database = self.directory / 'state.sqlite'
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            self.port = listener.getsockname()[1]
        self.endpoint = f'http://127.0.0.1:{self.port}'
        self.process = None
        self.token = None
        self.clients = []

    def start(self):
        assert self.process is None
        self.process = subprocess.Popen(
            [str(BINARY), '--listen', f'127.0.0.1:{self.port}',
             '--state', str(self.database), '--token-file', str(self.token_file)],
            cwd=self.directory, env={**os.environ, 'PATH': '/nonexistent'},
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise AssertionError('Owned simulator exited before readiness')
            if self.token_file.exists():
                self.token = self.token_file.read_text().strip()
                request = urllib.request.Request(self.endpoint + '/_simulator/health',
                                                 headers={'X-Simulator-Token': self.token})
                try:
                    with urllib.request.build_opener(NoRedirect()).open(request, timeout=1) as response:
                        assert response.status == 200
                    return
                except OSError:
                    pass
            time.sleep(0.05)
        raise AssertionError('Owned simulator did not become ready')

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
            raise AssertionError('Owned simulator timed out; killed and reaped')
        assert process.returncode == 0, 'Owned simulator shutdown failed'

    def sdk(self, region=REGIONS[0], prefixed=False):
        prefix = '/_simulator/providers/aws/' if prefixed else ''
        client = boto3.client(
            'sqs', endpoint_url=self.endpoint + prefix, region_name=region,
            aws_access_key_id='TCSIMSQSLOCAL', aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5))
        client.meta.events.register('before-sign.sqs',
            lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', self.token))

        def local_only(request, **kwargs):
            target = urllib.parse.urlsplit(request.url)
            assert target.scheme == 'http' and target.netloc == f'127.0.0.1:{self.port}', \
                'SDK attempted to leave the owned loopback origin'
            COUNTS['sends'] += 1
            if prefixed:
                PREFIX_REQUESTS.append(target.path)

        def response(http_response, **kwargs):
            COUNTS['responses'] += 1
            assert not 300 <= http_response.status_code < 400, 'Unexpected native redirect'

        client.meta.events.register('before-send.sqs', local_only)
        client.meta.events.register('after-call.sqs', response)
        self.clients.append(client)
        return client


def invoke(client, operation, **kwargs):
    COUNTS['calls'] += 1
    result = getattr(client, operation)(**kwargs)
    SUCCEEDED.add(operation)
    return result


def error(code, operation):
    try:
        operation()
    except ClientError as failure:
        assert failure.response['Error']['Code'] == code, failure.response['Error']['Code']
    else:
        raise AssertionError('Expected native error ' + code)


def counts(client, queue, visible, inflight):
    attributes = invoke(client, 'get_queue_attributes', QueueUrl=queue,
                        AttributeNames=['All'])['Attributes']
    assert int(attributes['ApproximateNumberOfMessages']) == visible, attributes
    assert int(attributes['ApproximateNumberOfMessagesNotVisible']) == inflight, attributes
    return attributes


def receive(client, queue, visibility=30):
    return invoke(client, 'receive_message', QueueUrl=queue, MaxNumberOfMessages=1,
                  VisibilityTimeout=visibility, WaitTimeSeconds=0,
                  MessageAttributeNames=['All'], MessageSystemAttributeNames=['All']).get('Messages', [])


def one_message(client, queue, body, visibility=30):
    messages = receive(client, queue, visibility)
    assert len(messages) == 1 and messages[0]['Body'] == body, 'Wrong isolated message body'
    assert messages[0]['MD5OfBody'] == hashlib.md5(body.encode()).hexdigest()
    return messages[0]


def cross_token_rejected(team, foreign_token):
    # Use the actual native API; plain auth errors need not be SQS JSON errors.
    request = urllib.request.Request(team.endpoint + '/', data=b'{}', method='POST',
        headers={'X-Simulator-Token': foreign_token, 'Content-Type': 'application/x-amz-json-1.0',
                 'X-Amz-Target': 'AmazonSQS.ListQueues'})
    try:
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=2):
            pass
    except urllib.error.HTTPError as failure:
        assert failure.code == 401
        failure.close()
    else:
        raise AssertionError('Another team token was accepted')


def main():
    REPORTS.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='tcsim-sqs-sdk-') as directory:
        first_path, second_path = Path(directory) / 'one', Path(directory) / 'two'
        first_path.mkdir(); second_path.mkdir()
        first, second = Team(first_path), Team(second_path)
        try:
            first.start(); second.start()
            assert first.token != second.token
            cross_token_rejected(first, second.token); cross_token_rejected(second, first.token)
            clients = [first.sdk(region) for region in REGIONS] + [second.sdk(region) for region in REGIONS]
            queue_name = 'sdk-same-name'
            attributes = {'VisibilityTimeout': '2', 'DelaySeconds': '0', 'MessageRetentionPeriod': '300'}
            queues = [invoke(client, 'create_queue', QueueName=queue_name,
                             Attributes=attributes)['QueueUrl'] for client in clients]
            for index, (client, queue) in enumerate(zip(clients, queues)):
                assert invoke(client, 'get_queue_url', QueueName=queue_name)['QueueUrl'] == queue
                assert invoke(client, 'list_queues')['QueueUrls'] == [queue]
                actual = counts(client, queue, 0, 0)
                assert actual['QueueArn'] == f'arn:aws:sqs:{REGIONS[index % 2]}:000000000000:{queue_name}'
                assert all(actual[key] == value for key, value in attributes.items())
                assert invoke(client, 'create_queue', QueueName=queue_name, Attributes=attributes)['QueueUrl'] == queue
                error('QueueNameExists', lambda c=client: invoke(c, 'create_queue', QueueName=queue_name))

            retention = invoke(clients[0], 'create_queue', QueueName='sdk-retention',
                               Attributes={'MessageRetentionPeriod': '60'})['QueueUrl']
            retention_start = time.monotonic()
            invoke(clients[0], 'send_message', QueueUrl=retention, MessageBody='retention-only')
            counts(clients[0], retention, 1, 0)
            error('InvalidAttributeValue', lambda: invoke(clients[0], 'create_queue',
                  QueueName='invalid-retention', Attributes={'MessageRetentionPeriod': '1'}))

            # Same queue name in all four process/region cells holds distinct data.
            for index, (client, queue) in enumerate(zip(clients, queues)):
                body = f'cell-{index}-雪'
                sent = invoke(client, 'send_message', QueueUrl=queue, MessageBody=body,
                              MessageAttributes={'label': {'DataType': 'String', 'StringValue': str(index)},
                                                 'bytes': {'DataType': 'Binary', 'BinaryValue': b'dummy'}})
                assert sent['MD5OfMessageBody'] == hashlib.md5(body.encode()).hexdigest()
            for index, (client, queue) in enumerate(zip(clients, queues)):
                message = one_message(client, queue, f'cell-{index}-雪')
                assert message['MessageAttributes']['label']['StringValue'] == str(index)
                assert message['MessageAttributes']['bytes']['BinaryValue'] == b'dummy'
                invoke(client, 'delete_message', QueueUrl=queue, ReceiptHandle=message['ReceiptHandle'])
                counts(client, queue, 0, 0)

            # Queue metadata, in-flight visibility and its current receipt persist.
            invoke(clients[0], 'send_message', QueueUrl=queues[0], MessageBody='restart-persistent')
            persistent = one_message(clients[0], queues[0], 'restart-persistent', visibility=120)
            counts(clients[0], queues[0], 0, 1)
            error('ReceiptHandleIsInvalid', lambda: invoke(clients[1], 'delete_message',
                  QueueUrl=queues[1], ReceiptHandle=persistent['ReceiptHandle']))
            error('ReceiptHandleIsInvalid', lambda: invoke(clients[2], 'delete_message',
                  QueueUrl=queues[2], ReceiptHandle=persistent['ReceiptHandle']))
            old_token = first.token
            first.stop(); first.start()
            assert first.token == old_token and first.database.is_file()
            clients[:2] = [first.sdk(region) for region in REGIONS]
            assert invoke(clients[0], 'get_queue_url', QueueName=queue_name)['QueueUrl'] == queues[0]
            assert counts(clients[0], queues[0], 0, 1)['MessageRetentionPeriod'] == '300'
            assert receive(clients[0], queues[0]) == []
            invoke(clients[0], 'change_message_visibility', QueueUrl=queues[0],
                   ReceiptHandle=persistent['ReceiptHandle'], VisibilityTimeout=0)
            redelivered = one_message(clients[0], queues[0], 'restart-persistent')
            assert redelivered['ReceiptHandle'] != persistent['ReceiptHandle']
            assert int(redelivered['Attributes']['ApproximateReceiveCount']) == 2
            error('ReceiptHandleIsInvalid', lambda: invoke(clients[0], 'delete_message',
                  QueueUrl=queues[0], ReceiptHandle=persistent['ReceiptHandle']))
            invoke(clients[0], 'delete_message', QueueUrl=queues[0], ReceiptHandle=redelivered['ReceiptHandle'])
            for client, queue in zip(clients, queues):
                counts(client, queue, 0, 0)

            # Short native timing tests use supported seconds, not DB/clock edits.
            invoke(clients[0], 'send_message', QueueUrl=queues[0], MessageBody='delayed', DelaySeconds=1)
            counts(clients[0], queues[0], 0, 0)  # Initial delay is not a delivered/in-flight message.
            assert receive(clients[0], queues[0]) == []
            delay_deadline = time.monotonic() + 4
            delayed = []
            while not delayed and time.monotonic() < delay_deadline:
                time.sleep(0.1)
                delayed = receive(clients[0], queues[0], visibility=1)
            assert len(delayed) == 1 and delayed[0]['Body'] == 'delayed'
            counts(clients[0], queues[0], 0, 1)
            time.sleep(1.2)
            error('MessageNotInflight', lambda: invoke(clients[0], 'change_message_visibility',
                  QueueUrl=queues[0], ReceiptHandle=delayed[0]['ReceiptHandle'], VisibilityTimeout=1))
            again = one_message(clients[0], queues[0], 'delayed')
            assert again['ReceiptHandle'] != delayed[0]['ReceiptHandle']
            invoke(clients[0], 'delete_message', QueueUrl=queues[0], ReceiptHandle=again['ReceiptHandle'])
            counts(clients[0], queues[0], 0, 0)

            prefixed = first.sdk(REGIONS[0], prefixed=True)
            assert invoke(prefixed, 'get_queue_url', QueueName=queue_name)['QueueUrl'] == queues[0]
            assert queues[0] in invoke(prefixed, 'list_queues')['QueueUrls']
            assert any(path.startswith('/_simulator/providers/aws/') for path in PREFIX_REQUESTS)

            # A 60s retention queue must survive before expiry and disappear after
            # the actual API deadline. This is intentionally a real ~60s check.
            assert time.monotonic() - retention_start < 55, 'Setup consumed retention evidence window'
            counts(clients[0], retention, 1, 0)
            deadline = retention_start + 66
            expired = False
            while time.monotonic() < deadline:
                values = invoke(clients[0], 'get_queue_attributes', QueueUrl=retention,
                                AttributeNames=['ApproximateNumberOfMessages'])['Attributes']
                if values['ApproximateNumberOfMessages'] == '0':
                    # The native store uses millisecond epoch precision.
                    assert time.monotonic() - retention_start >= 59.99, 'Message expired before supported retention'
                    expired = True
                    break
                time.sleep(0.25)
            assert expired and receive(clients[0], retention) == []
            invoke(clients[0], 'delete_queue', QueueUrl=retention)
            for client, queue in zip(clients, queues):
                invoke(client, 'delete_queue', QueueUrl=queue)
                error('AWS.SimpleQueueService.NonExistentQueue',
                      lambda c=client: invoke(c, 'get_queue_url', QueueName=queue_name))
                assert invoke(client, 'list_queues').get('QueueUrls', []) == []
            assert SUCCEEDED == OPERATIONS
            assert COUNTS['calls'] > 0 and COUNTS['calls'] == COUNTS['sends'] == COUNTS['responses']
            report = {'nativeSDK': 'boto3 ' + boto3.__version__, 'operations': sorted(SUCCEEDED),
                      'twoAuthenticatedProcesses': True, 'twoSQLiteDatabases': True, 'regions': REGIONS,
                      'sameNameIsolation': True, 'crossToken401': True, 'restartMetadataVisibilityReceipt': True,
                      'receiptRotationAndRegionalRejection': True, 'delayAndVisibilitySeconds': True,
                      'actualRetentionSeconds': 60, 'prefixScope': 'GetQueueUrl and ListQueues',
                      'counts': COUNTS, 'cleanup': 'DELETED', 'realAWS': False, 'uiTested': False}
            (REPORTS / 'sqs-sdk.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report))
        finally:
            # Close both owned processes even when one shutdown raises.
            with contextlib.ExitStack() as cleanup:
                cleanup.callback(second.stop)
                cleanup.callback(first.stop)


if __name__ == '__main__':
    main()
