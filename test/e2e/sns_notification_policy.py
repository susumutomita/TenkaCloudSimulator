"""Exercise the bounded notification topic policy with official boto3 and two local CLIs."""
import argparse
import contextlib
import hashlib
import json
import tempfile
from pathlib import Path
from urllib.parse import urlsplit

import check
from autoscaling_plans import control
from botocore.exceptions import ClientError

NAME = 'notification-policy-sdk'
ARN = f'arn:aws:sns:us-east-1:000000000000:{NAME}'


def policy(effect):
    statement = {
        'Sid': 'AWSCodeStarNotifications_publish', 'Effect': effect,
        'Principal': {'Service': ['codestar-notifications.amazonaws.com']},
        'Action': 'SNS:Publish', 'Resource': ARN,
    }
    return json.dumps({'Version': '2008-10-17', 'Statement': [statement]}, indent=2) + '\n'


@contextlib.contextmanager
def client(endpoint, token, observed):
    origin = urlsplit(endpoint)
    assert origin.scheme == 'http' and origin.hostname == '127.0.0.1' and origin.port
    sns = check.boto3.client('sns', endpoint_url=endpoint, region_name='us-east-1',
        aws_access_key_id='TCSIMNOTIFICATION', aws_secret_access_key='synthetic-local-only',
        config=check.Config(connect_timeout=3, read_timeout=8, retries={'total_max_attempts': 1}, proxies={}))

    def local(request, **kwargs):
        assert request.method == 'POST' and request.url == endpoint + '/'
        observed['sent'] += 1

    def received(model, http_response, **kwargs):
        observed['responses'].append([model.name, http_response.status_code])

    sns.meta.events.register('before-sign.sns', lambda request, **_: request.headers.add_header('X-Simulator-Token', token))
    sns.meta.events.register('before-send.sns', local)
    sns.meta.events.register('after-call.sns', received)
    try:
        yield sns
    finally:
        sns.close()


def get(sns):
    return sns.get_topic_attributes(TopicArn=ARN)['Attributes']


def put(sns, value, arn=ARN):
    return sns.set_topic_attributes(TopicArn=arn, AttributeName='Policy', AttributeValue=value)


def expect_error(code, status, action):
    try:
        action()
    except ClientError as error:
        assert error.response['Error']['Code'] == code and error.response['ResponseMetadata']['HTTPStatusCode'] == status
        return
    raise AssertionError(f'Expected {code}')


def run(previous_binary=None):
    candidate = check.BINARY
    observed = {'sent': 0, 'responses': []}
    with tempfile.TemporaryDirectory(prefix='sns-policy-e2e-') as directory:
        root = Path(directory)
        owner_db, peer_db = root / 'owner.sqlite', root / 'peer.sqlite'
        # An optional historical CLI produces the original on-disk format.
        # Both teams have exactly the same topic ARN.
        try:
            if previous_binary:
                check.BINARY = previous_binary.resolve(strict=True)
                assert check.BINARY != candidate
            with check.running(owner_db, authenticated=True) as (owner, token), client(owner, token, observed) as sns:
                assert sns.create_topic(Name=NAME)['TopicArn'] == ARN
                original = get(sns)
                assert 'Policy' not in original
        finally:
            check.BINARY = candidate
        with check.running(peer_db, authenticated=True) as (peer, peer_token), client(peer, peer_token, observed) as peer_sns:
            assert peer_sns.create_topic(Name=NAME)['TopicArn'] == ARN
            with check.running(owner_db, authenticated=True) as (owner, token), client(owner, token, observed) as sns:
                assert get(sns) == original
                put(sns, policy('Allow'))
                assert get(sns) == dict(original, Policy=policy('Allow'))
                expect_error('InvalidParameter', 400, lambda: put(sns, policy('Allow').replace(ARN, '*')))
                assert get(sns)['Policy'] == policy('Allow') and get(peer_sns) == original
                grant = {'Version': '2012-10-17', 'Statement': [
                    {'Effect': 'Allow', 'Action': ['sns:GetTopicAttributes'], 'Resource': ARN}]}
                control(owner, token, '/_simulator/v1/iam/principals/topic-reader/policy', grant)
                reader = control(owner, token, '/_simulator/v1/access', {'Principal': 'topic-reader'})['AccessToken']
                with client(owner, reader, observed) as limited:
                    assert get(limited)['Policy'] == policy('Allow')
                    expect_error('AuthorizationError', 403, lambda: put(limited, policy('Deny')))
                    expect_error('AuthorizationError', 403, lambda: put(limited, policy('Deny'), ARN+'-missing'))
                    expect_error('AuthorizationError', 403, lambda: limited.publish(TopicArn=ARN, Message='ordinary request'))
                put(sns, policy('Deny'))
            with check.running(owner_db, authenticated=True) as (owner, token), client(owner, token, observed) as sns:
                assert get(sns)['Policy'] == policy('Deny') and get(peer_sns) == original
                sns.delete_topic(TopicArn=ARN)
                expect_error('NotFound', 404, lambda: get(sns))
                assert sns.create_topic(Name=NAME)['TopicArn'] == ARN and get(sns) == original
                put(sns, policy('Allow'))
                assert get(sns)['Policy'] == policy('Allow')
                sns.delete_topic(TopicArn=ARN)
            peer_sns.delete_topic(TopicArn=ARN)
            expect_error('NotFound', 404, lambda: get(peer_sns))
        assert observed['sent'] == len(observed['responses']) and observed['sent'] > 20
        assert all(p.is_file() and p.parent == root for p in root.iterdir())
    assert not root.exists()
    return dict(observed, historicalBinarySha256=hashlib.sha256(previous_binary.read_bytes()).hexdigest() if previous_binary else None,
                candidateBinarySha256=hashlib.sha256(candidate.read_bytes()).hexdigest(), historicalUpgrade=bool(previous_binary),
                exactReadback=True, restoredBothLifetimes=True, twoTeams=True, cleanup=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--previous-binary', type=Path)
    print(json.dumps(run(parser.parse_args().previous_binary), indent=2))
