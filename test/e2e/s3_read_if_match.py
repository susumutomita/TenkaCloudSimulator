"""Opt-in pinned SDK verification of S3 read If-Match on owned loopback state."""
from contextlib import closing
from hashlib import file_digest
import json
import os
from pathlib import Path
import tempfile
from urllib.parse import urlsplit

from sqs import BINARY, Team
import boto3
from botocore import __version__ as botocore_version
from botocore.config import Config
from botocore.exceptions import ClientError


def run():
    assert __debug__ and (boto3.__version__, botocore_version) == ('1.42.0', '1.41.6')
    bucket, key = 's3-read-if-match-owned', 'read/retained & version'
    target = {'Bucket': bucket, 'Key': key}
    bodies = (b'old representation', b'new representation')
    statuses, fields = [], []
    calls = 0

    with tempfile.TemporaryDirectory(prefix='s3-read-if-match-') as folder:
        team = Team(folder)

        def connect(prefixed):
            c = boto3.client('s3', endpoint_url=team.endpoint + ('/_simulator/providers/aws/' if prefixed else ''),
                region_name='us-east-1', aws_access_key_id='TCSIMIFMATCHREAD', aws_secret_access_key='dummy-local-only',
                config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=3, read_timeout=6,
                    s3={'addressing_style': 'path'}, request_checksum_calculation='when_required',
                    response_checksum_validation='when_required'))

            def send(request, **_):
                target_url = urlsplit(request.url)
                assert target_url.scheme == 'http' and target_url.netloc == urlsplit(team.endpoint).netloc
                assert target_url.path.startswith('/_simulator/providers/aws/') == prefixed
                request.headers['X-Simulator-Token'] = team.token
                match = request.headers.get('If-Match')
                fields.append(match.decode('ascii') if isinstance(match, bytes) else match)

            def response(http_response, **_):
                assert not 300 <= http_response.status_code < 400
                statuses.append(http_response.status_code)

            c.meta.events.register('before-send.s3', send)
            c.meta.events.register('after-call.s3', response)
            team.clients.append(c)
            return c

        def sdk(c, method, status=200, **kwargs):
            nonlocal calls
            expected = len(fields) + 1
            calls += 1
            try:
                result = getattr(c, method)(**kwargs)
            except ClientError as err:
                assert err.response['ResponseMetadata']['HTTPStatusCode'] == status, (method, status, err)
                if status == 412 and method == 'get_object':
                    assert err.response['Error']['Code'] == 'PreconditionFailed'
                result = None
            finally:
                assert len(fields) == len(statuses) == expected
                assert fields[-1] == kwargs.get('IfMatch')
            assert statuses[-1] == status and (result is None) == (status >= 400)
            return result

        try:
            team.start()
            c = connect(False)
            sdk(c, 'create_bucket', Bucket=bucket)
            sdk(c, 'put_bucket_versioning', Bucket=bucket, VersioningConfiguration={'Status': 'Enabled'})
            old, new = [sdk(c, 'put_object', **target, Body=body, ContentType='text/plain') for body in bodies]
            assert old['ETag'] != new['ETag'] and old['VersionId'] != new['VersionId']
            inventory = sdk(c, 'list_object_versions', Bucket=bucket)['Versions']

            def verify(c):
                for method in ('get_object', 'head_object'):
                    for version, body, extra in [(new, bodies[1], {}), (old, bodies[0], {'VersionId': old['VersionId']})]:
                        actual = version['ETag']
                        other = old['ETag'] if version is new else new['ETag']
                        for match in (actual, '*', actual + ', ' + other, other + ', ' + actual):
                            reply = sdk(c, method, **target, IfMatch=match, **extra)
                            assert (reply['ETag'], reply['VersionId'], reply['ContentType'], reply['ContentLength']) == (
                                actual, version['VersionId'], 'text/plain', len(body))
                            if method == 'get_object':
                                with closing(reply['Body']) as stream:
                                    assert stream.read() == body
                            else:
                                assert 'Body' not in reply
                        for reject in (other, 'W/' + actual + ', ' + other, actual + ', malformed', '*, ' + actual):
                            sdk(c, method, status=412, **target, IfMatch=reject, **extra)
                assert sdk(c, 'list_object_versions', Bucket=bucket)['Versions'] == inventory

            verify(c)
            token = team.token
            team.stop()
            team.start()
            assert team.token == token
            c = connect(True)
            verify(c)
            for version in inventory:
                sdk(c, 'delete_object', status=204, **target, VersionId=version['VersionId'])
            assert not sdk(c, 'list_object_versions', Bucket=bucket).get('Versions')
            sdk(c, 'delete_bucket', status=204, Bucket=bucket)
            assert not sdk(c, 'list_buckets')['Buckets']
        finally:
            team.stop()

    assert 0 < calls == len(fields) == len(statuses)
    with BINARY.open('rb') as binary:
        sha = file_digest(binary, 'sha256').hexdigest()
    receipt = {'nativeOperations':['GetObject','HeadObject'], 'strongMatchAnyPosition': True,
               'currentAndSelectedRetainedVersions': True, 'weakAndMalformedRefused': True,
               'existingWildcardSingleAndResponse': True, 'versionsUnchangedAfterRestart': True,
               'rootAndProviderAlias': True, 'cleanup': 'DELETED', 'liveAWS': False,
               'sdkCallsSendsResponses': [calls, len(fields), len(statuses)], 'binarySHA256': sha}
    directory = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    (directory/'s3-read-if-match.json').write_text(json.dumps(receipt, indent=2)+'\n')
    print(json.dumps(receipt))


if __name__ == '__main__':
    run()
