"""Execute the SERVED multipart request contract against an authenticated CLI.

The completion XML is a static template: only real response ETags/UploadIds may
identify saved state. Native boto3 reads the same SQLite. No browser or AWS calls.
"""
import hashlib
import json
import os
from pathlib import Path
import tempfile
import urllib.error
import urllib.request
from urllib.parse import quote, urlencode, urlsplit
import xml.etree.ElementTree as ET

from sqs import BINARY, Team, NoRedirect
from sqs_docs import MAIN_OPERATION_IDS, MAIN_PATHS
from openapi_parameters import validate_parameter_lists
from test_sakura_openapi import NEW_IDS as SAKURA_IDS, COLLECTION as SAKURA_COLLECTION, ITEM as SAKURA_ITEM
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


MULTIPART = {'CreateMultipartUpload', 'UploadPart', 'ListParts',
             'ListMultipartUploads', 'CompleteMultipartUpload', 'AbortMultipartUpload'}
OBJECT_PATH = '/{bucket}/{key}'
BUCKET_PATH = '/{bucket}'
METHODS = {'get', 'put', 'post', 'delete', 'head', 'patch', 'options', 'trace'}


def xml(body, expected):
    root = ET.fromstring(body)
    for node in root.iter():
        node.tag = node.tag.rsplit('}', 1)[-1]
    assert root.tag == expected, 'Unexpected native S3 XML root'
    return root


def parameters(spec, path, method):
    result = {}
    for parameter in spec['paths'][path].get('parameters', []) + spec['paths'][path][method].get('parameters', []):
        if '$ref' in parameter:
            assert parameter['$ref'].startswith('#/components/parameters/')
            parameter = spec['components']['parameters'][parameter['$ref'].rsplit('/', 1)[-1]]
        result[(parameter['name'], parameter['in'])] = parameter
    return result


def validate_contract(spec):
    # Preserve the 61-operation baseline plus current main's five opt-in Sakura
    # routes. Both are literal fixtures, never read from the served document.
    assert len(MAIN_OPERATION_IDS) == len(set(MAIN_OPERATION_IDS)) == 61
    assert len(MAIN_PATHS) == len(set(MAIN_PATHS)) == 34
    expected_ids = set(MAIN_OPERATION_IDS) | SAKURA_IDS
    expected_paths = set(MAIN_PATHS) | {SAKURA_COLLECTION, SAKURA_ITEM}
    assert len(expected_ids) == 66 and len(expected_paths) == 36
    entries = [(path, method, entry['operationId'])
               for path, item in spec['paths'].items()
               for method, entry in item.items() if method in METHODS]
    ids = [entry[2] for entry in entries]
    assert len(ids) == len(set(ids)) == 67, 'Duplicated or missing OpenAPI operation ID'
    assert set(ids) == expected_ids | {'S3MultipartUpload'}, 'Existing operation inventory changed'
    assert set(spec['paths']) == expected_paths, 'Existing path inventory changed'
    assert [(path, method) for path, method, name in entries if name == 'S3MultipartUpload'] == [(OBJECT_PATH, 'post')]
    validate_parameter_lists(spec)
    required = {
        (OBJECT_PATH, 'post'): {'uploads': 'string', 'uploadId': 'string'},
        (OBJECT_PATH, 'put'): {'uploadId': 'string', 'partNumber': 'integer'},
        (OBJECT_PATH, 'get'): {'uploadId': 'string', 'max-parts': 'integer', 'part-number-marker': 'integer'},
        (OBJECT_PATH, 'delete'): {'uploadId': 'string'},
        (BUCKET_PATH, 'get'): {'uploads': 'string', 'prefix': 'string', 'max-uploads': 'integer',
                             'key-marker': 'string', 'upload-id-marker': 'string'},
    }
    for (path, method), fields in required.items():
        actual = parameters(spec, path, method)
        for name, kind in fields.items():
            assert actual[(name, 'query')]['schema']['type'] == kind
    post = spec['paths'][OBJECT_PATH]['post']
    assert post['tags'] == ['S3 basic object storage'], 'Multipart must remain discoverable in the AWS S3 section'
    aws = next(group for group in spec['x-tagGroups'] if group['name'] == 'AWS')
    assert set(post['tags']) <= set(aws['tags'])
    assert post['requestBody']['required'] is False, 'Create must allow an empty body'
    assert '?uploads' in post['description'] and '?uploadId=' in post['description']
    template = post['requestBody']['content']['application/xml']['examples']['Complete']
    assert 'static' in template['summary'].lower() and 'actual' in template['summary'].lower()
    root = xml(template['value'].encode(), 'CompleteMultipartUpload')
    assert len(root) == 1 and root[0].tag == 'Part'
    assert [node.tag for node in root[0]] == ['PartNumber', 'ETag']
    assert root[0].findtext('PartNumber') == '1'
    assert root[0].findtext('ETag') == '"actual-stored-part-md5"', 'Static placeholder silently became live state'
    return template['value']


def run():
    binary_sha256 = hashlib.sha256(BINARY.read_bytes()).hexdigest()
    reports = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
    reports.mkdir(parents=True, exist_ok=True)
    http_counts = {'calls': 0, 'responses': 0}
    sdk_counts = {'calls': 0, 'sends': 0, 'responses': 0}
    executed = []
    with tempfile.TemporaryDirectory(prefix='tcsim-multipart-reference-') as directory:
        team = Team(directory)
        try:
            team.start()
            origin = urlsplit(team.endpoint)
            opener = urllib.request.build_opener(NoRedirect())

            def wire(method, path, body=None, content_type=None, status=200, operation=None):
                assert path.startswith('/') and not path.startswith('//') and '://' not in path
                target = team.endpoint + path
                parsed = urlsplit(target)
                assert parsed.scheme == 'http' and parsed.netloc == origin.netloc
                headers = {'X-Simulator-Token': team.token, 'X-Simulator-Region': 'us-east-1'}
                if content_type:
                    headers['Content-Type'] = content_type
                request = urllib.request.Request(target, data=body, headers=headers, method=method)
                http_counts['calls'] += 1
                try:
                    response = opener.open(request, timeout=5)
                except urllib.error.HTTPError as failure:
                    response = failure
                with response:
                    http_counts['responses'] += 1
                    actual, raw, response_headers = response.status, response.read(), response.headers
                assert actual == status, f'Unexpected native HTTP status for {operation or method}: {actual}'
                assert not 300 <= actual < 400
                if operation and 200 <= actual < 300:
                    executed.append(operation)
                return response_headers, raw

            _, raw_spec = wire('GET', '/_simulator/docs/openapi.json')
            spec = json.loads(raw_spec)
            source_bytes = Path('internal/server/apidocs/openapi.json').read_bytes()
            assert spec == json.loads(source_bytes), 'Served embedded OpenAPI differs from source'
            template = validate_contract(spec)

            def resource(path, method, query, bucket, key=None):
                documented = parameters(spec, path, method)
                assert all((name, 'query') in documented for name in query), 'Undocumented query in acceptance request'
                actual = path.replace('{bucket}', quote(bucket, safe=''))
                if key is not None:
                    actual = actual.replace('{key}', quote(key, safe=''))
                assert '{' not in actual and '}' not in actual
                # Empty string parameters are actual native flags, not the word
                # "true" or a second incompatible selector copied from defaults.
                encoded = '&'.join(quote(name, safe='') if value == '' else urlencode({name: value})
                                   for name, value in query.items())
                return actual + ('?' + encoded if encoded else '')

            sdk = boto3.client('s3', endpoint_url=team.endpoint, region_name='us-east-1',
                aws_access_key_id='TCSIMMULTIPARTDOCS', aws_secret_access_key='dummy-local-only',
                config=Config(s3={'addressing_style': 'path'}, retries={'max_attempts': 0},
                    connect_timeout=2, read_timeout=5, request_checksum_calculation='when_required',
                    response_checksum_validation='when_required'))
            team.clients.append(sdk)
            sdk.meta.events.register('before-sign.s3',
                lambda request, **_: request.headers.add_header('X-Simulator-Token', team.token))

            def before_call(**_):
                sdk_counts['calls'] += 1

            def before_send(request, **_):
                parsed = urlsplit(request.url)
                assert parsed.scheme == 'http' and parsed.netloc == origin.netloc, 'SDK left owned loopback'
                sdk_counts['sends'] += 1

            def after_call(http_response, **_):
                sdk_counts['responses'] += 1
                assert not 300 <= http_response.status_code < 400, 'Unexpected native SDK redirect'

            sdk.meta.events.register('before-call.s3', before_call)
            sdk.meta.events.register('before-send.s3', before_send)
            sdk.meta.events.register('after-call.s3', after_call)

            def read_object(**kwargs):
                result = sdk.get_object(**kwargs)
                with result['Body'] as stream:
                    data = stream.read()
                return result, data

            def missing_upload(key, upload):
                try:
                    sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload)
                except ClientError as failure:
                    assert failure.response['Error']['Code'] == 'NoSuchUpload'
                else:
                    raise AssertionError('Removed upload remained visible to native SDK')

            defaults = parameters(spec, OBJECT_PATH, 'post')
            bucket = defaults[('bucket', 'path')]['schema']['default']
            key = defaults[('key', 'path')]['schema']['default']
            sdk.create_bucket(Bucket=bucket)
            sdk.put_bucket_versioning(Bucket=bucket, VersioningConfiguration={'Status': 'Enabled'})
            previous_body = b'previous-version-preserved'
            previous = sdk.put_object(Bucket=bucket, Key=key, Body=previous_body, ContentType='text/plain')
            assert not sdk.list_multipart_uploads(Bucket=bucket).get('Uploads')

            def start(target_key):
                path = resource(OBJECT_PATH, 'post', {'uploads': ''}, bucket, target_key)
                _, raw = wire('POST', path, b'', 'text/plain', operation='CreateMultipartUpload')
                result = xml(raw, 'InitiateMultipartUploadResult')
                assert result.findtext('Bucket') == bucket and result.findtext('Key') == target_key
                upload = result.findtext('UploadId')
                assert upload and len(upload) == 32 and all(char in '0123456789abcdef' for char in upload)
                return upload

            upload = start(key)
            static_part = spec['paths'][OBJECT_PATH]['put']['requestBody']['content']['text/plain']['example']
            assert isinstance(static_part, str) and static_part
            part_body = static_part.encode()
            etags = {}
            for number, payload in ((1, part_body), (3, b'saved-but-omitted-from-completion')):
                path = resource(OBJECT_PATH, 'put', {'uploadId': upload, 'partNumber': number}, bucket, key)
                headers, raw = wire('PUT', path, payload, 'text/plain', operation='UploadPart')
                assert raw == b''
                etags[number] = headers['ETag']
                assert etags[number] == '"' + hashlib.md5(payload).hexdigest() + '"'
            actual_parts = sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload)['Parts']
            assert [(part['PartNumber'], part['ETag'], part['Size']) for part in actual_parts] == [
                (1, etags[1], len(part_body)), (3, etags[3], 33)]

            path = resource(OBJECT_PATH, 'get', {'uploadId': upload, 'max-parts': 1,
                                               'part-number-marker': 0}, bucket, key)
            _, raw = wire('GET', path, operation='ListParts')
            page = xml(raw, 'ListPartsResult')
            assert page.findtext('UploadId') == upload and page.findtext('MaxParts') == '1'
            assert page.findtext('PartNumberMarker') == '0' and page.findtext('IsTruncated') == 'true'
            assert [(part.findtext('PartNumber'), part.findtext('ETag')) for part in page.findall('Part')] == [('1', etags[1])]
            marker = page.findtext('NextPartNumberMarker')
            assert marker == '1'
            path = resource(OBJECT_PATH, 'get', {'uploadId': upload, 'max-parts': 1,
                                               'part-number-marker': marker}, bucket, key)
            _, raw = wire('GET', path, operation='ListParts')
            page = xml(raw, 'ListPartsResult')
            assert page.findtext('IsTruncated') == 'false'
            assert [(part.findtext('PartNumber'), part.findtext('ETag')) for part in page.findall('Part')] == [('3', etags[3])]

            other = start(key)
            abort_key = key + '-abort'
            abort_upload = start(abort_key)
            expected_uploads = [(key, upload), (key, other), (abort_key, abort_upload)]
            listed, cursor = [], {}
            for index in range(len(expected_uploads)):
                path = resource(BUCKET_PATH, 'get', {'uploads': '', 'prefix': key,
                                                   'max-uploads': 1, **cursor}, bucket)
                _, raw = wire('GET', path, operation='ListMultipartUploads')
                page = xml(raw, 'ListMultipartUploadsResult')
                entries = [(item.findtext('Key'), item.findtext('UploadId')) for item in page.findall('Upload')]
                assert entries == [expected_uploads[index]], 'Native upload cursor skipped/repeated a saved upload'
                listed.extend(entries)
                assert page.findtext('Prefix') == key and page.findtext('MaxUploads') == '1'
                assert page.findtext('IsTruncated') == ('true' if index < 2 else 'false')
                if index < 2:
                    cursor = {'key-marker': page.findtext('NextKeyMarker'),
                              'upload-id-marker': page.findtext('NextUploadIdMarker')}
                    assert tuple(cursor.values()) == entries[0]
            assert listed == expected_uploads
            assert [(item['Key'], item['UploadId']) for item in sdk.list_multipart_uploads(Bucket=bucket)['Uploads']] == listed

            # The served static XML is not a live ETag. Sending it unchanged
            # must fail and leave both the previous object and every part intact.
            path = resource(OBJECT_PATH, 'post', {'uploadId': upload}, bucket, key)
            _, raw = wire('POST', path, template.encode(), 'application/xml', status=400)
            assert xml(raw, 'Error').findtext('Code') == 'InvalidPart'
            assert read_object(Bucket=bucket, Key=key)[1] == previous_body
            assert sdk.list_parts(Bucket=bucket, Key=key, UploadId=upload)['Parts'] == actual_parts
            document = xml(template.encode(), 'CompleteMultipartUpload')
            document.find('Part/ETag').text = etags[1]
            headers, raw = wire('POST', path, ET.tostring(document, encoding='utf-8'),
                                'application/xml', operation='CompleteMultipartUpload')
            result = xml(raw, 'CompleteMultipartUploadResult')
            expected_etag = '"' + hashlib.md5(hashlib.md5(part_body).digest()).hexdigest() + '-1"'
            assert result.findtext('Bucket') == bucket and result.findtext('Key') == key
            assert result.findtext('ETag') == expected_etag
            completed, data = read_object(Bucket=bucket, Key=key)
            assert data == part_body and completed['ContentType'] == 'text/plain'
            assert completed['ETag'] == expected_etag and completed['VersionId'] == headers['x-amz-version-id']
            assert completed['VersionId'] != previous['VersionId']
            assert read_object(Bucket=bucket, Key=key, VersionId=previous['VersionId'])[1] == previous_body
            missing_upload(key, upload)

            path = resource(OBJECT_PATH, 'put', {'uploadId': abort_upload, 'partNumber': 1}, bucket, abort_key)
            wire('PUT', path, b'abort-removes-this-part', 'text/plain', operation='UploadPart')
            assert sdk.list_parts(Bucket=bucket, Key=abort_key, UploadId=abort_upload)['Parts'][0]['Size'] == 23
            for target_key, target_upload in ((key, other), (abort_key, abort_upload)):
                path = resource(OBJECT_PATH, 'delete', {'uploadId': target_upload}, bucket, target_key)
                _, raw = wire('DELETE', path, status=204, operation='AbortMultipartUpload')
                assert raw == b''
                missing_upload(target_key, target_upload)
            assert not sdk.list_multipart_uploads(Bucket=bucket).get('Uploads')
            assert read_object(Bucket=bucket, Key=key)[1] == part_body, 'Abort changed the completed object'
            versions = sdk.list_object_versions(Bucket=bucket)
            assert not versions.get('DeleteMarkers')
            assert {item['VersionId'] for item in versions['Versions']} == {previous['VersionId'], completed['VersionId']}
            for item in versions['Versions']:
                sdk.delete_object(Bucket=bucket, Key=item['Key'], VersionId=item['VersionId'])
            sdk.delete_bucket(Bucket=bucket)
            assert sdk.list_buckets()['Buckets'] == []
            assert MULTIPART <= set(executed)
            assert http_counts['calls'] == http_counts['responses'] and http_counts['calls'] > len(MULTIPART)
            assert 0 < sdk_counts['calls'] == sdk_counts['sends'] == sdk_counts['responses']
            assert hashlib.sha256(BINARY.read_bytes()).hexdigest() == binary_sha256, 'Binary artifact changed during acceptance'
            report = {'servedSpecEqualsSource': True, 'preservedOperationIDs': 66, 'totalOperationIDs': 67,
                'preservedPaths': 36, 'sixNativeHTTPActions': sorted(set(executed)),
                'servedCompleteTemplateExecuted': True, 'liveSubstitutions': ['UploadId', 'ETag', 'pagination markers'],
                'unsubstitutedStaticETagRefused': True, 'nativeSDKSharedStateAndVersionReadbacks': True,
                'partsAndUploadsPagination': True, 'omittedPartNotInObject': True,
                'completedAndAbortedUploadsGone': True, 'cleanup': 'DELETED', 'HTTPCounts': http_counts,
                'SDKCounts': sdk_counts, 'binarySHA256': binary_sha256,
                'sourceOpenAPISHA256': hashlib.sha256(source_bytes).hexdigest(),
                'credentialAndIDValuesOmitted': True, 'realAWS': False, 'browserTested': False}
            (reports / 's3-multipart-reference.json').write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report))
        finally:
            team.stop()


if __name__ == '__main__':
    run()
