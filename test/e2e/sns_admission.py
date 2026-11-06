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


def denied(code, operation):
    try:
        operation()
    except ClientError as error:
        assert error.response['Error']['Code'] == code, error.response
    else:
        raise AssertionError('Expected ' + code)


with tempfile.TemporaryDirectory(prefix='sns-413-') as directory:
    state = Path(directory) / 'team-a.db'
    with running(state, authenticated=True) as (endpoint, operator), running(authenticated=True) as (other, peer):
        client, second = sdk(endpoint, operator), sdk(other, peer)
        topic = client.create_topic(Name='admission')['TopicArn']
        assert client.create_topic(Name='admission')['TopicArn'] == topic
        sub = client.subscribe(TopicArn=topic, Protocol='email', Endpoint='ops@example.com',
                               ReturnSubscriptionArn=True)['SubscriptionArn']
        denied('NotFoundException', lambda: sdk(endpoint, operator, 'us-west-2').unsubscribe(SubscriptionArn=sub))
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
            denied('InvalidParameterException', lambda: client.publish(TopicArn=topic, Message='test', Subject=subject))
        assert client.get_topic_attributes(TopicArn=topic)['Attributes'] == before
        control_headers = {'Content-Type': 'application/json', 'X-Simulator-Token': operator}
        policy = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Deny', 'Action': 'sns:Publish', 'Resource': topic}]}
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        request = urllib.request.Request(endpoint + '/_simulator/v1/iam/principals/sns-reader/policy', data=json.dumps(policy).encode(), headers=control_headers, method='PUT')
        with opener.open(request) as response:
            assert response.status == 200
        request = urllib.request.Request(endpoint + '/_simulator/v1/access', data=json.dumps({'Principal': 'sns-reader'}).encode(), headers=control_headers)
        with opener.open(request) as response:
            reader_token = json.load(response)['AccessToken']
        denied('AuthorizationErrorException', lambda: sdk(endpoint, reader_token).publish(TopicArn=topic, Message='denied'))
        assert client.publish(TopicArn=topic, Message='actual UTF-8', Subject='日' * 99)['MessageId']
        raw(endpoint, operator, [('Action', 'CreateTopic'), ('Version', '2010-03-31'),
            ('Name', 'sparse'), ('Attributes.entry.2.key', 'DisplayName'),
            ('Attributes.entry.2.value', '日' * 99)])
        sparse = 'arn:aws:sns:us-east-1:000000000000:sparse'
        assert client.get_topic_attributes(TopicArn=sparse)['Attributes']['DisplayName'] == '日' * 99
        fifo = client.create_topic(Name='events.fifo', Attributes={'FifoTopic': 'true'})['TopicArn']
        denied('InvalidParameterException', lambda: client.publish(TopicArn=fifo, Message='unsupported'))
        # These are separate real processes/DBs/token authorities, with no peer state.
        assert second.list_topics()['Topics'] == []
        denied('NotFoundException', lambda: second.get_topic_attributes(TopicArn=topic))
        raw(other, operator, [('Action', 'ListTopics'), ('Version', '2010-03-31')], 401)
    with sqlite3.connect('file:' + str(state) + '?mode=ro', uri=True) as database:
        assert database.execute('SELECT count(*) FROM aws_sns_messages').fetchone()[0] == 1
        assert database.execute('SELECT count(*) FROM aws_sns_deliveries').fetchone()[0] == 1
    with running(state, authenticated=True) as (endpoint, operator):
        client = sdk(endpoint, operator)
        assert client.get_topic_attributes(TopicArn=sparse)['Attributes']['DisplayName'] == '日' * 99
        assert client.list_subscriptions_by_topic(TopicArn=topic)['Subscriptions'][0]['SubscriptionArn'] == sub
        client.unsubscribe(SubscriptionArn=sub)
        for arn in (topic, sparse, fifo):
            client.delete_topic(TopicArn=arn)
        assert client.list_topics()['Topics'] == []
    with sqlite3.connect('file:' + str(state) + '?mode=ro', uri=True) as database:
        for table in ('aws_sns_topics', 'aws_sns_subscriptions', 'aws_sns_messages', 'aws_sns_deliveries'):
            assert database.execute('SELECT count(*) FROM ' + table).fetchone()[0] == 0


print('SNS real Query/boto3 admission, Unicode subject, regional deletion guard, two-host isolation, restart and cleanup passed.')
