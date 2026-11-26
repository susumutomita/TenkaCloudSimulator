"""Opt-in exact STANDARD/default equivalence over the pinned SDK and local SQLite.

Run with SIMULATOR_BINARY set to the current built simulator and the repository's
pinned test SDK. All requests stay on the owned numeric loopback process.
"""
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
    wire_classes, statuses = [], []
    bucket = 'explicit-standard-sdk-evidence'
    target = {'Bucket': bucket}
    reports = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
    reports.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='s3-standard-class-') as directory:
        team = Team(directory)

        def client(prefix=False):
            c = boto3.client('s3', endpoint_url=team.endpoint + ('/_simulator/providers/aws/' if prefix else ''),
                region_name='us-east-1', aws_access_key_id='TCSIMEXPLICITSTANDARD', aws_secret_access_key='dummy-local-only',
                config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5,
                              s3={'addressing_style': 'path'}, request_checksum_calculation='when_required',
                              response_checksum_validation='when_required'))

            def before_send(request, **_):
                url = urlsplit(request.url)
                assert url.scheme == 'http' and url.netloc == urlsplit(team.endpoint).netloc
                assert url.path.startswith('/_simulator/providers/aws/') == prefix
                request.headers['X-Simulator-Token'] = team.token
                value = request.headers.get('X-Amz-Storage-Class')
                wire_classes.append(value.decode('ascii') if isinstance(value, bytes) else value)

            def after_call(http_response, **_):
                assert not 300 <= http_response.status_code < 400
                statuses.append(http_response.status_code)

            c.meta.events.register('before-send.s3', before_send)
            c.meta.events.register('after-call.s3', after_call)
            team.clients.append(c)
            return c

        calls = 0

        def request(c, operation, **fields):
            nonlocal calls
            calls += 1
            before = len(wire_classes)
            try:
                return getattr(c, operation)(**fields)
            finally:
                assert len(wire_classes) == len(statuses) == before+1, 'Expected one native request and response'
                assert wire_classes[-1] == fields.get('StorageClass'), 'The real SDK header differs from the request'

        def rejected(c, operation, **fields):
            try:
                request(c, operation, **fields)
            except ClientError as error:
                assert error.response['Error']['Code'] == 'NotImplemented' and statuses[-1] == 501
            else:
                raise AssertionError('Unsupported storage operation succeeded')

        def uploads(c):
            return [(row['Key'], row['UploadId']) for row in
                    request(c, 'list_multipart_uploads', **target).get('Uploads', [])]

        def versions(c):
            return request(c, 'list_object_versions', **target).get('Versions', [])

        def check_object(c, key, version, body, etag):
            obj = request(c, 'get_object', **target, Key=key, VersionId=version)
            with closing(obj['Body']) as data:
                assert data.read() == body
            assert (obj['ETag'], obj['ContentType'], obj['VersionId']) == (etag, 'text/plain', version)
            assert 'x-amz-storage-class' not in obj['ResponseMetadata']['HTTPHeaders']

        try:
            team.start()
            root, alias = client(), client(True)
            request(root, 'create_bucket', **target)
            request(alias, 'put_bucket_versioning', **target, VersioningConfiguration={'Status': 'Enabled'})
            kept, pending = {}, {}
            for label, operate, option in (('implicit', root, {}), ('explicit', alias, {'StorageClass': 'STANDARD'})):
                ordinary, multipart = f'{label}/single & data', f'{label}/multi & data'
                put = request(operate, 'put_object', **target, Key=ordinary, Body=b'text body', ContentType='text/plain', **option)
                assert 'x-amz-storage-class' not in put['ResponseMetadata']['HTTPHeaders']
                kept[ordinary] = (put['VersionId'], put['ETag'])
                initiated = request(operate, 'create_multipart_upload', **target, Key=multipart, ContentType='text/plain', **option)
                upload = initiated['UploadId']
                assert 'x-amz-storage-class' not in initiated['ResponseMetadata']['HTTPHeaders']
                part = request(root, 'upload_part', **target, Key=multipart, UploadId=upload, PartNumber=3, Body=b'part body')
                pending[multipart] = (upload, part['ETag'])
            assert len({value[1] for value in kept.values()}) == 1
            assert len({value[0] for value in kept.values()}) == 2
            before = versions(alias), uploads(alias)
            assert len(before[0]) == len(kept) and {row['Key'] for row in before[0]} == set(kept)
            assert len(before[1]) == len(pending) and set(before[1]) == {(key, value[0]) for key, value in pending.items()}
            for storage in ('GLACIER', 'DEEP_ARCHIVE', 'GLACIER_IR', 'STANDARD_IA'):
                rejected(root, 'put_object', **target, Key='explicit/single & data', Body=b'must not replace', StorageClass=storage)
                rejected(alias, 'create_multipart_upload', **target, Key='explicit/multi & data', StorageClass=storage)
                assert (versions(alias), uploads(alias)) == before
            rejected(root, 'restore_object', **target, Key='explicit/single & data',
                     RestoreRequest={'Days': 1, 'GlacierJobParameters': {'Tier': 'Standard'}})
            assert (versions(alias), uploads(alias)) == before

            token = team.token
            team.stop()
            team.start()
            assert team.token == token
            root, alias = client(), client(True)
            assert (versions(root), uploads(root)) == before
            for key, (version, etag) in kept.items():
                check_object(alias, key, version, b'text body', etag)
            completed = {}
            for key, (upload, etag) in pending.items():
                parts = request(alias, 'list_parts', **target, Key=key, UploadId=upload)['Parts']
                assert [(p['PartNumber'], p['Size'], p['ETag']) for p in parts] == [(3, 9, etag)]
                result = request(root, 'complete_multipart_upload', **target, Key=key, UploadId=upload,
                                 MultipartUpload={'Parts': [{'PartNumber': 3, 'ETag': etag}]})
                assert 'x-amz-storage-class' not in result['ResponseMetadata']['HTTPHeaders']
                completed[key] = (result['VersionId'], result['ETag'])
                check_object(alias, key, result['VersionId'], b'part body', result['ETag'])
            assert len({value[1] for value in completed.values()}) == 1
            assert not uploads(root)
            rows = versions(alias)
            assert len(rows) == len(kept) + len(completed) and {row['Key'] for row in rows} == set(kept) | set(completed)
            for row in rows:
                key, version = row['Key'], row['VersionId']
                assert row['StorageClass'] == 'STANDARD' and (version, row['ETag']) == (kept | completed)[key]
                request(root, 'delete_object', **target, Key=key, VersionId=version)
            assert not versions(alias) and not uploads(alias)
            request(root, 'delete_bucket', **target)
            assert not request(alias, 'list_buckets')['Buckets']
        finally:
            team.stop()

    assert 0 < calls == len(wire_classes) == len(statuses)
    with BINARY.open('rb') as binary:
        sha = file_digest(binary, 'sha256').hexdigest()
    result = {'exactDefaultEquivalence': ['PutObject', 'CreateMultipartUpload'],
              'alternateClassesAndRestoreRefusedWithoutState': True, 'sameSQLiteAfterRestart': True,
              'canonicalListingAndUnchangedHeaders': True, 'cleanup': 'DELETED',
              'sdkCallsSendsResponses': [calls, len(wire_classes), len(statuses)], 'binarySHA256': sha,
              'liveAWS': False}
    (reports/'s3-standard-storage-class.json').write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(result))


if __name__ == '__main__':
    run()
