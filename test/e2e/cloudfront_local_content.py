"""Official CloudFront/S3 SDK control API and bounded simulator-only delivery.

No origin network connection, cloud credentials, external DNS or live CDN.
Run on an isolated approved host with the stock pinned E2E requirements.
"""
import argparse
from contextlib import ExitStack
import hashlib
from http.client import HTTPConnection
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit
from xml.etree import ElementTree as ET

from sts_session_lifecycle import LocalSimulator

BUCKET = 'cloudfront-local-origin'
KEY = 'assets/message.txt'
PREFIX = '/2020-05-31/distribution'


def successful(method, status=200, **fields):
    result = method(**fields)
    assert result['ResponseMetadata']['HTTPStatusCode'] == status
    assert result['ResponseMetadata'].get('RetryAttempts', 0) == 0
    return result


def config(caller, enabled=True, ttl=60):
    return {'CallerReference': caller, 'Comment': 'owned local content', 'Enabled': enabled,
            'Origins': {'Quantity': 1, 'Items': [{'Id': 'saved', 'DomainName': BUCKET + '.s3.amazonaws.com',
                                                'S3OriginConfig': {'OriginAccessIdentity': ''}}]},
            'DefaultCacheBehavior': {'TargetOriginId': 'saved', 'ViewerProtocolPolicy': 'allow-all',
                                    'AllowedMethods': {'Quantity': 2, 'Items': ['GET', 'HEAD'],
                                                       'CachedMethods': {'Quantity': 2, 'Items': ['GET', 'HEAD']}},
                                    'ForwardedValues': {'QueryString': False, 'Cookies': {'Forward': 'none'}},
                                    'MinTTL': 0, 'DefaultTTL': ttl, 'MaxTTL': 86400}}


def local(team, method, path, status, body=None, token=None):
    assert path.startswith(('/_simulator/cloudfront/distributions/', '/_simulator/v1/'))
    assert not path.startswith('//') and team.endpoint == f'http://127.0.0.1:{team.port}'
    connection = HTTPConnection('127.0.0.1', team.port, timeout=5)
    try:
        connection.request(method, path, body=None if body is None else json.dumps(body),
                           headers={'X-Simulator-Token': token or team.token, 'Content-Type': 'application/json'})
        response = connection.getresponse()
        assert response.status == status, 'Unexpected local response; body withheld'
        data = response.read((1 << 20) + 1)
        assert len(data) <= 1 << 20
        return data, {key.lower(): value for key, value in response.getheaders()}
    finally:
        connection.close()


def client(session, service, team, stack, settings, stats, key=KEY):
    assert service in ('cloudfront', 's3')
    if service == 's3':
        from botocore.config import Config
        # Match the existing S3 fixture subset: optional GetObject ChecksumMode
        # is unsupported. Keep normal SDK serialization and exact byte readback.
        settings = settings.merge(Config(response_checksum_validation='when_required'))
    result = session.client(service, endpoint_url=team.endpoint, region_name='us-east-1', config=settings,
                            aws_access_key_id='TCSIMLOCALCONTENT', aws_secret_access_key='local-fixture-only')
    stack.callback(result.close)
    result.meta.events.register('before-sign.' + service,
                               lambda request, **_: request.headers.add_header('X-Simulator-Token', team.token))

    def guard(request, **_):
        target = urlsplit(request.url)
        assert target.scheme == 'http' and target.netloc == f'127.0.0.1:{team.port}' and not target.fragment
        assert request.method in ('GET', 'POST', 'PUT', 'HEAD', 'DELETE')
        if service == 'cloudfront':
            assert target.path == PREFIX or target.path.startswith(PREFIX + '/')
        else:
            assert target.path in ('/' + BUCKET, '/' + BUCKET + '/' + key)
        headers = {name.lower(): value for name, value in request.headers.items()}
        assert 'x-simulator-token' in headers and 'authorization' in headers
        assert not {'cookie', 'x-simulator-principal', 'x-amz-security-token'} & headers.keys()
        stats['sends'] += 1
        assert stats['sends'] <= 200

    def received(http_response, **_):
        assert not 300 <= http_response.status_code < 400
        if service == 'cloudfront' and http_response.content:
            root = ET.fromstring(http_response.content)
            assert all(node.tag.startswith('{http://cloudfront.amazonaws.com/doc/2020-05-31/}')
                       for node in root.iter()), 'native CloudFront XML reset its namespace'
        stats['responses'] += 1

    result.meta.events.register('before-send.' + service, guard)
    result.meta.events.register('after-call.' + service, received)
    return result


def cleanup(front, s3, identifier, key=KEY):
    current = successful(front.get_distribution_config, Id=identifier)
    value = current['DistributionConfig']
    value['Enabled'] = False
    disabled = successful(front.update_distribution, Id=identifier, IfMatch=current['ETag'], DistributionConfig=value)
    successful(front.delete_distribution, status=204, Id=identifier, IfMatch=disabled['ETag'])
    assert not successful(front.list_distributions)['DistributionList'].get('Items', [])
    successful(s3.delete_object, status=204, Bucket=BUCKET, Key=key)
    successful(s3.delete_bucket, status=204, Bucket=BUCKET)


def exercise(session, settings, binary, directory, stack):
    teams = []
    stats = {'sends': 0, 'responses': 0}
    for name, payload in [('owner', b'old owner'), ('peer', b'peer content')]:
        team = LocalSimulator(Path(directory) / name, binary)
        stack.callback(team.close)
        team.start()
        s3 = client(session, 's3', team, stack, settings, stats)
        front = client(session, 'cloudfront', team, stack, settings, stats)
        successful(s3.create_bucket, Bucket=BUCKET)
        successful(s3.put_object, Bucket=BUCKET, Key=KEY, Body=payload, ContentType='text/plain')
        created = successful(front.create_distribution, status=201, DistributionConfig=config('local-content'))
        identifier = created['Distribution']['Id']
        assert re.fullmatch('D[a-f0-9]{24}', identifier)
        assert created['Distribution']['ARN'] == 'arn:aws:cloudfront::000000000000:distribution/' + identifier
        assert created['Distribution']['DomainName'] == identifier + '.cloudfront.invalid'
        stack.callback(cleanup, front, s3, identifier)
        path = f'/_simulator/cloudfront/distributions/{identifier}/objects/{KEY}'
        first, headers = local(team, 'GET', path, 200)
        assert first == payload and headers['x-cache'].startswith('Miss') and headers['cache-control'] == 'no-store'
        assert headers['content-disposition'] == 'attachment' and headers['x-content-type-options'] == 'nosniff'
        assert headers['content-security-policy'] == "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
        teams.append((team, s3, front, identifier, path, created['Distribution']))
    owner, s3, front, identifier, path, saved = teams[0]
    peer, _, peer_front, peer_id, peer_path, peer_saved = teams[1]
    assert owner.port != peer.port and owner.token != peer.token and identifier != peer_id
    local(peer, 'GET', path, 404)
    local(owner, 'GET', path, 401, token=peer.token)
    successful(s3.put_object, Bucket=BUCKET, Key=KEY, Body=b'fresh owner', ContentType='text/plain')
    cached, headers = local(owner, 'GET', path, 200)
    assert cached == b'old owner' and headers['x-cache'].startswith('Hit')
    batch = {'CallerReference': 'refresh', 'Paths': {'Quantity': 1, 'Items': ['/' + KEY]}}
    invalidation = successful(front.create_invalidation, status=201, DistributionId=identifier, InvalidationBatch=batch)['Invalidation']
    assert invalidation['Status'] == 'Completed'
    assert successful(front.get_invalidation, DistributionId=identifier, Id=invalidation['Id'])['Invalidation'] == invalidation
    assert successful(front.create_invalidation, status=201, DistributionId=identifier, InvalidationBatch=batch)['Invalidation'] == invalidation
    assert local(owner, 'GET', path, 200)[0] == b'fresh owner'
    body, head = local(owner, 'HEAD', path, 200)
    assert body == b'' and head['content-length'] == str(len(b'fresh owner')) and head['x-cache'].startswith('Hit')
    assert head['content-disposition'] == 'attachment' and head['x-content-type-options'] == 'nosniff'
    assert head['content-security-policy'] == "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
    resource = saved['ARN']
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': 'cloudfront:GetDistribution', 'Resource': resource},
        {'Effect': 'Allow', 'Action': 's3:GetObject', 'Resource': 'arn:aws:s3:::' + BUCKET + '/' + KEY}]}
    local(owner, 'PUT', '/_simulator/v1/iam/principals/content-reader/policy', 200, policy)
    token = json.loads(local(owner, 'POST', '/_simulator/v1/access', 200, {'Principal': 'content-reader'})[0])['AccessToken']
    assert local(owner, 'GET', path, 200, token=token)[0] == b'fresh owner'
    policy['Statement'].append({'Effect': 'Deny', 'Action': 's3:GetObject', 'Resource': '*'})
    local(owner, 'PUT', '/_simulator/v1/iam/principals/content-reader/policy', 200, policy)
    local(owner, 'GET', path, 403, token=token)
    owner.close()
    owner.start()
    assert successful(front.get_distribution, Id=identifier)['Distribution'] == saved
    assert local(owner, 'GET', path, 200)[0] == b'fresh owner'
    assert local(peer, 'GET', peer_path, 200)[0] == b'peer content'
    assert successful(peer_front.get_distribution, Id=peer_id)['Distribution'] == peer_saved
    assert stats['sends'] == stats['responses'] and stats['sends'] > 0


def ordinary_session():
    # Keep ordinary SDK configuration guards. Do not sanitize inherited profiles.
    assert not any(key.startswith(('AWS_', 'BOTO_')) for key in os.environ)
    assert all(not path.exists() and not path.is_symlink() for path in (
        Path.home() / '.aws/config', Path.home() / '.aws/credentials', Path.home() / '.boto', Path('/etc/boto.cfg')))
    assert version('boto3') == '1.42.0' and version('botocore') == '1.41.6'
    import boto3
    from botocore.config import Config
    return boto3.Session(), Config(connect_timeout=3, read_timeout=5, retries={'max_attempts': 0}, proxies={},
                                   s3={'addressing_style': 'path'})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    session, settings = ordinary_session()
    binary = args.binary.resolve(strict=True)
    with binary.open('rb') as data:
        assert hashlib.file_digest(data, 'sha256').hexdigest() == args.binary_sha256
    with tempfile.TemporaryDirectory(prefix='cloudfront-local-sdk-') as directory, ExitStack() as stack:
        exercise(session, settings, binary, directory, stack)
    assert not Path(directory).exists()
    print(json.dumps({'ok': True, 'nativeCloudFrontOperations': 8, 'origin': 'saved local S3 bytes',
                      'cacheHitAndInvalidation': True, 'freshHitAuthorization': True,
                      'peerUnchanged': True, 'restart': True, 'cleanup': True, 'externalOriginFetch': False}))


if __name__ == '__main__':
    main()
