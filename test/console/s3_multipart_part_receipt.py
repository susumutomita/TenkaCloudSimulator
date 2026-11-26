"""Actual served Cloudscape multipart readback in JSDOM, not Chromium.

Run opt-in against the current built simulator with the repository-pinned
Boto3/Botocore and an installed JSDOM. Nothing leaves numeric loopback.
"""
from contextlib import closing
import hashlib
import http.client
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlencode, urlsplit

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'test/e2e'))
from check import running, boto3, Config
from botocore import __version__ as botocore_version


def sdk(endpoint, token):
    origin = urlsplit(endpoint)
    assert origin.scheme == 'http' and origin.hostname == '127.0.0.1' and origin.port
    c = boto3.client('s3', endpoint_url=endpoint, region_name='us-east-1',
                     aws_access_key_id='TCSIM_MULTIPART_RECEIPT', aws_secret_access_key='dummy-fixture-only',
                     config=Config(s3={'addressing_style': 'path'}, retries={'max_attempts': 0}, proxies={},
                                   request_checksum_calculation='when_required',
                                   response_checksum_validation='when_required'))

    def local_only(request, **_):
        request_origin = urlsplit(request.url)
        assert (request_origin.scheme, request_origin.netloc) == (origin.scheme, origin.netloc)
        request.headers['X-Simulator-Token'] = token

    c.meta.events.register('before-send.s3', local_only)
    return c


def run():
    with running(authenticated=True) as (endpoint, token), closing(sdk(endpoint, token)) as client:
        bucket, key = 'multipart-receipt-fixture', 'folder/receipt & proof.bin'
        client.create_bucket(Bucket=bucket)
        upload = client.create_multipart_upload(Bucket=bucket, Key=key)['UploadId']
        fields = {'Bucket': bucket, 'Key': key, 'UploadId': upload}
        conn = http.client.HTTPConnection('127.0.0.1', urlsplit(endpoint).port, timeout=8)
        with closing(conn):
            conn.request('POST', '/_simulator/login', urlencode({'token': token}),
                         {'Content-Type': 'application/x-www-form-urlencoded'})
            response = conn.getresponse()
            assert response.status == 303
            cookie = response.headers['Set-Cookie'].split(';', 1)[0]
            response.read()

        app = ROOT / 'internal/server/web/app.js'
        subprocess.run(['node', str(Path(__file__).with_suffix('.cjs'))], text=True, check=True, timeout=65,
                       input=json.dumps({'endpoint': endpoint, 'token': token, 'cookie': cookie,
                                         'bucket': bucket, 'key': key, 'upload': upload,
                                         'asset': hashlib.sha256(app.read_bytes()).hexdigest(),
                                         'driver': str(Path(__file__).resolve())}))
        saved, = client.list_parts(**fields)['Parts']
        digest = hashlib.md5(b'sdk-new', usedforsecurity=False).hexdigest()
        assert (saved['PartNumber'], saved['Size'], saved['ETag']) == (1, 7, '"'+digest+'"')
        client.abort_multipart_upload(**fields)
        assert not client.list_multipart_uploads(Bucket=bucket).get('Uploads')
        client.delete_bucket(Bucket=bucket)
        assert not client.list_buckets()['Buckets']
    print('PASS served JSDOM + pinned Boto3: exact part confirmation, same-size concurrent replacement, refresh/no replay and cleanup.')


if __name__ == '__main__':
    assert __debug__ and (boto3.__version__, botocore_version) == ('1.42.0', '1.41.6')
    if sys.argv[1:] == ['--replace-fixture-part']:
        p = json.load(sys.stdin)
        with closing(sdk(p['endpoint'], p['token'])) as peer:
            part = peer.upload_part(Bucket=p['bucket'], Key=p['key'], UploadId=p['upload'], PartNumber=1,
                                    Body=b'sdk-new')
        print(part['ETag'])
    else:
        assert not sys.argv[1:]
        run()
