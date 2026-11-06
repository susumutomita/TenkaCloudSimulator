"""SNS PR413: actual local binary/official SDK and adversarial Query requests.

No cloud calls. Whole-suite results do not substitute for these SNS checks.
"""
import json
import sqlite3
import tempfile
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from check import running


def sdk(endpoint, token, region='us-east-1'):
    client = boto3.client('sns', endpoint_url=endpoint, region_name=region,
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
        config=Config(proxies={}, retries={'max_attempts': 0}))
    client.meta.events.register('before-sign.sns', lambda request, **kwargs:
        request.headers.add_header('X-Simulator-Token', token))
    return client


def raw(endpoint, token, fields, want=200):
    request = urllib.request.Request(endpoint + '/',
        data=urllib.parse.urlencode(fields, doseq=True).encode(),
        headers={'Content-Type': 'application/x-www-form-urlencoded',
                 'X-Simulator-Service': 'sns', 'X-Simulator-Token': token})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        response = opener.open(request)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        status, body = response.status, response.read()
    assert status == want, (want, status, body)
    if status != 401:
        root = ET.fromstring(body)
        assert root.tag.startswith('{http://sns.amazonaws.com/doc/2010-03-31/}')
        if status != 200:
            assert root.find('.//{*}Error/{*}Code') is not None
    return body


def denied(client, exception_name, operation):
    expected = getattr(client.exceptions, exception_name)
    wire_code = client.meta.service_model.shape_for(exception_name).metadata['error']['code']
    try:
        operation()
    except expected as error:
        assert type(error) is expected
        assert error.response['Error']['Code'] == wire_code, error.response
    except ClientError as error:
        raise AssertionError('Expected modeled ' + exception_name + ', got ' + type(error).__name__) from error
    else:
        raise AssertionError('Expected ' + exception_name)


with tempfile.TemporaryDirectory(prefix='sns-413-') as directory:
    state = Path(directory) / 'team-a.db'
    with running(state, authenticated=True) as (endpoint, operator), running(authenticated=True) as (other, peer):
        client, second = sdk(endpoint, operator), sdk(other, peer)
        topic = client.create_topic(Name='admission')['TopicArn']
        assert client.create_topic(Name='admission')['TopicArn'] == topic
        sub = client.subscribe(TopicArn=topic, Protocol='email', Endpoint='ops@example.com',
                               ReturnSubscriptionArn=True)['SubscriptionArn']
        west = sdk(endpoint, operator, 'us-west-2')
        denied(west, 'NotFoundException', lambda: west.unsubscribe(SubscriptionArn=sub))
        assert [row['SubscriptionArn'] for row in client.list_subscriptions_by_topic(TopicArn=topic)['Subscriptions']] == [sub]
        before = client.get_topic_attributes(TopicArn=topic)['Attributes']
        ns = [('Action', 'CreateTopic'), ('Version', '2010-03-31'), ('Name', 'rejected')]
        for extra in (
            [('Name', 'duplicate')],
            [('Action', 'DeleteTopic')],
            [('Version', 'invalid')],
            [('Attributes.entry.2.key', 'Policy'), ('Attributes.entry.2.value', '{}')],
            [('Attributes.entry.1.extra', 'ignored')],
            [('Attributes.entry.2.value', 'unpaired')],
            [('Attributes.entry.01.key', 'Policy'), ('Attributes.entry.01.value', '{}')],
        ):
            raw(endpoint, operator, ns + extra, 400)
        assert [row['TopicArn'] for row in client.list_topics()['Topics']] == [topic]
        raw(endpoint, operator, [('Action', 'Publish'), ('Version', '2010-03-31'),
            ('TopicArn', topic), ('Message', 'one'), ('Message', 'two')], 400)
        raw(endpoint, operator, [('Action', 'Subscribe'), ('Version', '2010-03-31'),
            ('TopicArn', topic), ('Protocol', 'email'), ('Endpoint', 'bad@example.com'),
            ('ReturnSubscriptionArn', 'perhaps')], 400)
        for subject in ('bad\nsubject', 'bad\x00subject', 'a' * 100):
            denied(client, 'InvalidParameterException', lambda: client.publish(TopicArn=topic, Message='test', Subject=subject))
        assert client.get_topic_attributes(TopicArn=topic)['Attributes'] == before
        control_headers = {'Content-Type': 'application/json', 'X-Simulator-Token': operator}
        policy = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Deny', 'Action': ['sns:Publish', 'sns:DeleteTopic'], 'Resource': topic}]}
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        request = urllib.request.Request(endpoint + '/_simulator/v1/iam/principals/sns-reader/policy', data=json.dumps(policy).encode(), headers=control_headers, method='PUT')
        with opener.open(request) as response:
            assert response.status == 200
        request = urllib.request.Request(endpoint + '/_simulator/v1/access', data=json.dumps({'Principal': 'sns-reader'}).encode(), headers=control_headers)
        with opener.open(request) as response:
            reader_token = json.load(response)['AccessToken']
        reader = sdk(endpoint, reader_token)
        denied(reader, 'AuthorizationErrorException', lambda: reader.publish(TopicArn=topic, Message='denied'))
        denied(reader, 'AuthorizationErrorException', lambda: reader.delete_topic(TopicArn='arn:aws:sns:us-east-1:000000000000:absent'))
        assert client.publish(TopicArn=topic, Message='actual UTF-8', Subject='日' * 99)['MessageId']
        raw(endpoint, operator, [('Action', 'CreateTopic'), ('Version', '2010-03-31'),
            ('Name', 'sparse'), ('Attributes.entry.2.key', 'DisplayName'),
            ('Attributes.entry.2.value', '日' * 99)])
        sparse = 'arn:aws:sns:us-east-1:000000000000:sparse'
        assert client.get_topic_attributes(TopicArn=sparse)['Attributes']['DisplayName'] == '日' * 99
        fifo = client.create_topic(Name='events.fifo', Attributes={'FifoTopic': 'true'})['TopicArn']
        denied(client, 'InvalidParameterException', lambda: client.publish(TopicArn=fifo, Message='unsupported'))
        # These are separate real processes/DBs/token authorities, with no peer state.
        assert second.list_topics()['Topics'] == []
        denied(second, 'NotFoundException', lambda: second.get_topic_attributes(TopicArn=topic))
        raw(other, operator, [('Action', 'ListTopics'), ('Version', '2010-03-31')], 401)
        retry = client.create_topic(Name='delete-retry')['TopicArn']
        peer_retry = second.create_topic(Name='delete-retry', Attributes={'DisplayName': 'peer'})['TopicArn']
        assert retry == peer_retry
        client.delete_topic(TopicArn=retry)
        client.delete_topic(TopicArn=retry)
        assert second.get_topic_attributes(TopicArn=peer_retry)['Attributes']['DisplayName'] == 'peer'
        second.delete_topic(TopicArn=peer_retry)
        second.delete_topic(TopicArn=peer_retry)
    with sqlite3.connect('file:' + str(state) + '?mode=ro', uri=True) as database:
        assert database.execute('SELECT count(*) FROM aws_sns_messages').fetchone()[0] == 1
        assert database.execute('SELECT count(*) FROM aws_sns_deliveries').fetchone()[0] == 1
    with running(state, authenticated=True) as (endpoint, operator):
        client = sdk(endpoint, operator)
        assert client.get_topic_attributes(TopicArn=sparse)['Attributes']['DisplayName'] == '日' * 99
        assert client.list_subscriptions_by_topic(TopicArn=topic)['Subscriptions'][0]['SubscriptionArn'] == sub
        client.unsubscribe(SubscriptionArn=sub)
        denied(client, 'NotFoundException', lambda: client.unsubscribe(SubscriptionArn=sub))
        for arn in (topic, sparse, fifo):
            client.delete_topic(TopicArn=arn)
            client.delete_topic(TopicArn=arn)
        assert client.list_topics()['Topics'] == []
    with sqlite3.connect('file:' + str(state) + '?mode=ro', uri=True) as database:
        for table in ('aws_sns_topics', 'aws_sns_subscriptions', 'aws_sns_messages', 'aws_sns_deliveries'):
            assert database.execute('SELECT count(*) FROM ' + table).fetchone()[0] == 0


print('SNS real Query/boto3 admission, Unicode subject, modeled exceptions, idempotent deletion, regional deletion guard, two-host isolation, restart and cleanup passed.')
