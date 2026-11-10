"""Real local SNS-to-SQS fanout: SDK wire, restart, isolation and refusal frames."""
import json
import sqlite3
import tempfile
import urllib.request
from pathlib import Path

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from check import running


counts = dict(calls=0, sends=0, responses=0)


def sdk(service, endpoint, token, region='us-east-1'):
    client = boto3.client(service, endpoint_url=endpoint, region_name=region,
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
        config=Config(proxies={}, retries={'max_attempts': 0}))
    client.meta.events.register('before-sign.' + service, lambda request, **kwargs:
        request.headers.add_header('X-Simulator-Token', token))
    def increment(name):
        def record(**kwargs):
            counts[name] += 1
        return record
    for event, name in [('before-call', 'calls'), ('before-send', 'sends'), ('after-call', 'responses')]:
        client.meta.events.register(event + '.' + service, increment(name))
    return client


def refuse(code, operation):
    try:
        operation()
    except ClientError as error:
        assert error.response['Error']['Code'] == code, error.response
    else:
        raise AssertionError('Expected ' + code)


def control(endpoint, token, path, payload, method='POST'):
    request = urllib.request.Request(endpoint + path, data=json.dumps(payload).encode(),
        headers={'Content-Type': 'application/json', 'X-Simulator-Token': token}, method=method)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request) as response:
        return json.load(response)


def consume(sqs, queue, topic, sns_id, body):
    messages = sqs.receive_message(QueueUrl=queue, MaxNumberOfMessages=10).get('Messages', [])
    assert len(messages) == 1, messages
    message = messages[0]
    envelope = json.loads(message['Body'])
    assert envelope == dict(Type='Notification', TopicArn=topic, MessageId=sns_id,
        Message=body, Subject='native fanout', Timestamp=envelope['Timestamp']), envelope
    assert message['MessageId'] != sns_id
    sqs.delete_message(QueueUrl=queue, ReceiptHandle=message['ReceiptHandle'])
    assert sqs.receive_message(QueueUrl=queue).get('Messages', []) == []


with tempfile.TemporaryDirectory(prefix='sns-sqs-native-') as directory:
    state = Path(directory) / 'team-a.db'
    with running(state, authenticated=True) as (endpoint, token), running(authenticated=True) as (other, peer_token):
        sns, sqs = sdk('sns', endpoint, token), sdk('sqs', endpoint, token)
        peer_sns, peer_sqs = sdk('sns', other, peer_token), sdk('sqs', other, peer_token)
        topic = sns.create_topic(Name='fanout')['TopicArn']
        peer_topic = peer_sns.create_topic(Name='fanout')['TopicArn']
        assert peer_topic == topic
        queues = [sqs.create_queue(QueueName=name)['QueueUrl'] for name in ('first', 'second')]
        peer_queue = peer_sqs.create_queue(QueueName='first')['QueueUrl']
        arns = [sqs.get_queue_attributes(QueueUrl=q, AttributeNames=['QueueArn'])['Attributes']['QueueArn'] for q in queues]
        for arn in arns:
            sub = sns.subscribe(TopicArn=topic, Protocol='sqs', Endpoint=arn)['SubscriptionArn']
            assert sns.subscribe(TopicArn=topic, Protocol='sqs', Endpoint=arn)['SubscriptionArn'] == sub
        assert len(sns.list_subscriptions_by_topic(TopicArn=topic)['Subscriptions']) == 2
        result = sns.publish(TopicArn=topic, Message='日本語 <message>', Subject='native fanout')
        for queue in queues:
            consume(sqs, queue, topic, result['MessageId'], '日本語 <message>')
        assert peer_sqs.receive_message(QueueUrl=peer_queue).get('Messages', []) == []
        assert peer_sns.list_subscriptions_by_topic(TopicArn=peer_topic)['Subscriptions'] == []
        west = sdk('sqs', endpoint, token, 'us-west-2')
        west_queue = west.create_queue(QueueName='first')['QueueUrl']
        west_arn = west.get_queue_attributes(QueueUrl=west_queue, AttributeNames=['QueueArn'])['Attributes']['QueueArn']
        refuse('InvalidParameter', lambda: sns.subscribe(TopicArn=topic, Protocol='sqs', Endpoint=west_arn))
        assert west.receive_message(QueueUrl=west_queue).get('Messages', []) == []
        policy = {'Version': '2012-10-17', 'Statement': [
            {'Effect': 'Allow', 'Action': 'sns:*', 'Resource': topic},
            {'Effect': 'Allow', 'Action': 'sqs:SendMessage', 'Resource': arns[0]}]}
        control(endpoint, token, '/_simulator/v1/iam/principals/partial/policy', policy, 'PUT')
        partial_token = control(endpoint, token, '/_simulator/v1/access', {'Principal': 'partial'})['AccessToken']
        partial = sdk('sns', endpoint, partial_token)
        refuse('AuthorizationError', lambda: partial.publish(TopicArn=topic, Message='not authorized for both'))
        for queue in queues:
            assert sqs.receive_message(QueueUrl=queue).get('Messages', []) == []
        sqs.delete_queue(QueueUrl=queues[1])
        refuse('NotFound', lambda: sns.publish(TopicArn=topic, Message='missing second queue'))
        assert sqs.receive_message(QueueUrl=queues[0]).get('Messages', []) == []
        queues[1] = sqs.create_queue(QueueName='second')['QueueUrl']
        pending = sns.publish(TopicArn=topic, Message='survives restart', Subject='native fanout')['MessageId']
        west.delete_queue(QueueUrl=west_queue)
        peer_sns.delete_topic(TopicArn=peer_topic)
        peer_sqs.delete_queue(QueueUrl=peer_queue)
    # Successful and rejected publications have exact durable audit counts.
    with sqlite3.connect('file:' + str(state) + '?mode=ro', uri=True) as database:
        assert database.execute('SELECT count(*) FROM aws_sns_messages').fetchone()[0] == 2
        assert database.execute('SELECT count(*) FROM aws_sns_deliveries').fetchone()[0] == 4
        assert database.execute('SELECT count(*) FROM aws_sqs_messages').fetchone()[0] == 2
    with running(state, authenticated=True) as (endpoint, token):
        sns, sqs = sdk('sns', endpoint, token), sdk('sqs', endpoint, token)
        queues = [sqs.get_queue_url(QueueName=name)['QueueUrl'] for name in ('first', 'second')]
        for queue in queues:
            consume(sqs, queue, topic, pending, 'survives restart')
        for sub in sns.list_subscriptions_by_topic(TopicArn=topic)['Subscriptions']:
            sns.unsubscribe(SubscriptionArn=sub['SubscriptionArn'])
        assert sns.publish(TopicArn=topic, Message='no subscribers')['MessageId']
        for queue in queues:
            assert sqs.receive_message(QueueUrl=queue).get('Messages', []) == []
        sns.delete_topic(TopicArn=topic)
        for queue in queues:
            sqs.delete_queue(QueueUrl=queue)
    with sqlite3.connect('file:' + str(state) + '?mode=ro', uri=True) as database:
        for table in ('aws_sns_topics', 'aws_sns_subscriptions', 'aws_sns_messages', 'aws_sns_deliveries', 'aws_sqs_queues', 'aws_sqs_messages'):
            assert database.execute('SELECT count(*) FROM ' + table).fetchone()[0] == 0

assert counts['calls'] == counts['sends'] == counts['responses'] and counts['calls'] > 50, counts
print('Native SNS/SQS SDK fanout, envelope correlation, complete refusal, two-process isolation, restart and cleanup passed:', counts)
