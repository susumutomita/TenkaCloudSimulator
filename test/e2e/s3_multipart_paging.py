"""Opt-in browser → exact local binary → native S3 ListParts acceptance.

SIMULATOR_BINARY=/absolute/build/path SIMULATOR_BINARY_SHA256=<trusted build digest>
CHROMIUM_EXECUTABLE=/absolute/chromium python test/e2e/s3_multipart_paging.py

The seeded 1001 tiny parts intentionally cannot be combined under the
simulator's 5 MiB minimum per nonfinal part and 8 MiB object limit. This
fixture proves complete pagination, stale-review refusal before POST, and
native refusal of a fully reviewed request without partial state changes.
No remote services, mocked responses, or database writes by the fixture.

Run only in an independently isolated Linux runner with loopback enabled and
no non-loopback network interface, such as an already approved container
configured with network mode none. Build/provision the pinned tools before
isolating. Do not use host networking, attach other network interfaces, or
mount host networking sockets into the fixture runner. The runner isolation
is the egress control; browser request observation and /proc preflight are
fail-closed diagnostics, not network firewall configuration or proof of
operator provenance. Run with normal interpreter assertions enabled.
Use a runner that supports Chromium sandboxing; do not disable the sandbox.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import parse_qs, urlsplit
import xml.etree.ElementTree as ET

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import sync_playwright, expect

from sqs import BINARY, Team


BUCKET = 'paging-local-only'
KEY = '1001-part-fixture'
PART_COUNT = 1001
ORIGINAL = b'p'
REPLACEMENT = b'changed-final-part'
BROWSER_REPLACEMENT = b'browser-uploaded-final-part'


def require(condition, reason):
    if not condition:
        raise AssertionError(reason)


def loopback_only_interfaces(proc_net_dev):
    """Check visible Linux namespace interfaces, not operator provenance."""
    rows = proc_net_dev.splitlines()
    require(len(rows) >= 3 and 'Inter-' in rows[0] and 'face' in rows[1],
            'Expected Linux /proc/net/dev interface records')
    names = []
    for row in rows[2:]:
        name, sep, data = row.partition(':')
        require(sep and name.strip() and len(data.split()) == 16
                and all(field.isascii() and field.isdecimal() for field in data.split()),
                'Invalid Linux interface record')
        names.append(name.strip())
    require(names == ['lo'], 'Runner must have loopback and no other network interface')


def exact_origin(url, endpoint):
    try:
        actual, allowed = urlsplit(url), urlsplit(endpoint)
    except ValueError:
        return False
    return (allowed.scheme == 'http' and allowed.hostname == '127.0.0.1'
            and actual.scheme == allowed.scheme and actual.netloc == allowed.netloc)


def redirect_chain_owned(request, endpoint):
    while request is not None:
        if not exact_origin(request.url, endpoint):
            return False
        request = request.redirected_from
    return True


def run_preflight():
    require(__debug__, 'Run without Python optimization')
    try:
        interfaces = Path('/proc/net/dev').read_text()
    except OSError as error:
        raise AssertionError('An isolated Linux runner is required') from error
    loopback_only_interfaces(interfaces)


def binary_checksum():
    expected = os.environ.get('SIMULATOR_BINARY_SHA256', '')
    require('SIMULATOR_BINARY' in os.environ and re.fullmatch('[a-f0-9]{64}', expected),
            'An explicit simulator binary and trusted SHA-256 are required')
    with BINARY.open('rb') as stream:
        observed = hashlib.file_digest(stream, 'sha256').hexdigest()
    require(observed == expected, 'Simulator executable digest does not match')
    return observed


def expect_aws_error(code, operation, **kwargs):
    try:
        operation(**kwargs)
    except ClientError as failure:
        require(failure.response['Error']['Code'] == code, 'Unexpected native S3 error')
    else:
        raise AssertionError('The expected native S3 refusal did not happen')


def list_page(body, marker, upload):
    root = ET.fromstring(body)
    require(root.tag.rsplit('}', 1)[-1] == 'ListPartsResult', 'Unexpected ListParts XML')
    require(root.findtext('{*}UploadId') == upload, 'Response did not belong to the owned upload')
    require(int(root.findtext('{*}PartNumberMarker')) == marker, 'Response marker did not echo request')
    numbers = [int(item.findtext('{*}PartNumber')) for item in root.findall('{*}Part')]
    truncated = root.findtext('{*}IsTruncated')
    if marker == 0:
        require(truncated == 'true' and root.findtext('{*}NextPartNumberMarker') == '1000'
                and numbers == list(range(1, 1001)), 'First native page is not 1–1000')
    else:
        require(marker == 1000 and truncated == 'false' and numbers == [1001],
                'Final native page did not contain part 1001')
    return marker


def submitted_parts(body):
    root = ET.fromstring(body)
    require(root.tag == 'CompleteMultipartUpload', 'Unexpected native completion envelope')
    return [(int(item.findtext('PartNumber')), item.findtext('ETag'))
            for item in root.findall('Part')]


def run():
    run_preflight()
    checksum = binary_checksum()
    chromium = os.environ.get('CHROMIUM_EXECUTABLE', '')
    require(Path(chromium).is_absolute() and Path(chromium).is_file(), 'Explicit Chromium is required')
    os.environ['NO_PROXY'] = '127.0.0.1,localhost'
    os.environ['no_proxy'] = os.environ['NO_PROXY']
    with tempfile.TemporaryDirectory(prefix='s3-listparts-live-') as directory:
        team = Team(Path(directory))
        try:
            team.start()
            inspect = run_s3(team, chromium)
        finally:
            team.stop()
    print(json.dumps({'binarySha256': checksum, 'nativeBrowserListMarkers': inspect,
                      'parts': PART_COUNT, 'staleReviewNoPost': True, 'completePostIncludesAllParts': True,
                      'nativeSizeRefusalPreservedUpload': True, 'nativeBrowserPartSave': True,
                      'visibleFinalPartRefresh': True, 'nativeAbortAndDelete': True,
                      'realAWS': False}))


def run_s3(team, chromium):
    def owned(url):
        return exact_origin(url, team.endpoint)

    client = boto3.client('s3', endpoint_url=team.endpoint, region_name='us-east-1',
                          aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy-local-only',
                          config=Config(proxies={}, connect_timeout=2, read_timeout=10,
                                        s3={'addressing_style': 'path'}, retries={'max_attempts': 0},
                                        request_checksum_calculation='when_required',
                                        response_checksum_validation='when_required'))
    team.clients.append(client)

    def before_send(request, **_):
        require(owned(request.url), 'Refusing to send the SDK request off loopback')
        request.headers['X-Simulator-Token'] = team.token

    def after_call(http_response, **_):
        require(not 300 <= http_response.status_code < 400, 'Unexpected native SDK redirect')

    client.meta.events.register('before-send.s3', before_send)
    client.meta.events.register('after-call.s3', after_call)
    client.create_bucket(Bucket=BUCKET)
    upload = client.create_multipart_upload(Bucket=BUCKET, Key=KEY)['UploadId']
    target = {'Bucket': BUCKET, 'Key': KEY, 'UploadId': upload}
    original = []
    for n in range(1, PART_COUNT + 1):
        etag = client.upload_part(**target, PartNumber=n, Body=ORIGINAL)['ETag']
        original.append((n, etag))

    def snapshot():
        a = client.list_parts(**target, MaxParts=1000)
        require(a['IsTruncated'] and a['NextPartNumberMarker'] == 1000, 'Native SDK first page mismatch')
        b = client.list_parts(**target, PartNumberMarker=1000)
        require(not b['IsTruncated'], 'Unexpected third page')
        return [(part['PartNumber'], part['ETag']) for part in a['Parts'] + b['Parts']]

    require(snapshot() == original, 'Local initial native state did not round trip')
    foreign, errors, redirected, parts_requests, completions = [], [], [], [], []
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(executable_path=chromium, chromium_sandbox=True, args=['--no-proxy-server'])
        try:
            context = browser.new_context(viewport={'width': 1440, 'height': 1000}, service_workers='block')
            page = context.new_page()
            page.set_default_timeout(30000)
            expect.set_options(timeout=30000)
            page.on('pageerror', lambda _: errors.append(True))

            def request_kind(request):
                url = urlsplit(request.url)
                if not owned(request.url) or url.path != f'/{BUCKET}/{KEY}':
                    return {}
                args = parse_qs(url.query)
                return args if args.get('uploadId') == [upload] else {}

            def capture_request(req):
                # Inspect every direct hop and its chain, including legitimate
                # same-origin bootstrap redirects. Only the external runner
                # network policy prevents egress; an event is observational.
                if not redirect_chain_owned(req, team.endpoint):
                    foreign.append(True)
                    return
                args = request_kind(req)
                if req.redirected_from is not None and (args or request_kind(req.redirected_from)):
                    redirected.append(True)
                if req.method == 'POST' and args:
                    try:
                        completions.append(submitted_parts(req.post_data))
                    except (ValueError, TypeError, ET.ParseError, AssertionError):
                        errors.append(True)

            context.on('request', capture_request)

            def capture(resp):
                args = request_kind(resp.request)
                if args and 300 <= resp.status < 400:
                    redirected.append(True)
                if resp.request.method == 'GET' and 'part-number-marker' in args:
                    parts_requests.append(resp)

            page.on('response', capture)
            page.goto(team.endpoint + '/_simulator/console/')
            page.get_by_label('Access token', exact=True).fill(team.token)
            page.get_by_role('button', name='Open sandbox', exact=True).click()
            page.get_by_role('button', name='Services', exact=True).click()
            page.get_by_role('menuitem', name='S3', exact=True).click()
            page.get_by_role('link', name=BUCKET, exact=True).click()
            page.get_by_role('tab', name='Management', exact=True).click()
            row = page.get_by_role('radio', name='Select upload ' + KEY, exact=True)
            row.click()
            expect(row).to_be_checked()
            confirm = page.get_by_label('Confirmation word', exact=True)
            complete = page.get_by_role('button', name='Complete upload', exact=True)

            def check_pages(start):
                observed = []
                for response in parts_requests[start:]:
                    marker = int(request_kind(response.request)['part-number-marker'][0])
                    require(response.status == 200, 'Native browser ListParts failed')
                    observed.append(list_page(response.body(), marker, upload))
                require(observed == [0, 1000], 'Browser did not fetch exactly both native pages')
                return observed

            # Check just beyond the page boundary, and scope to the row so a
            # receipt or equally sized earlier part cannot satisfy the check.
            last_row = page.get_by_role('row').filter(
                has=page.get_by_role('cell', name=str(PART_COUNT), exact=True))

            def check_visible_tail(size, etag):
                expect(last_row).to_have_count(1)
                expect(last_row.get_by_role('cell', name=f'{size} B', exact=True)).to_be_visible()
                expect(last_row.get_by_role('cell', name=etag, exact=True)).to_be_visible()

            first = check_pages(0)
            check_visible_tail(len(ORIGINAL), original[-1][1])
            changed_etag = client.upload_part(**target, PartNumber=PART_COUNT, Body=REPLACEMENT)['ETag']
            require(changed_etag != original[-1][1], 'Late part replacement did not change ETag')
            current = original[:-1] + [(PART_COUNT, changed_etag)]
            index = len(parts_requests)
            confirm.fill('complete')
            complete.click()
            expect(page.get_by_text('Parts changed. Review the refreshed list and type complete again.',
                                    exact=True)).to_be_visible()
            expect(confirm).to_have_value('')
            refreshed = check_pages(index)
            check_visible_tail(len(REPLACEMENT), changed_etag)
            require(not completions, 'Stale preview caused a completion POST')
            require(snapshot() == current, 'Stale review altered local multipart state')
            index = len(parts_requests)
            confirm.fill('complete')
            with page.expect_response(lambda response: response.request.method == 'POST'
                                      and bool(request_kind(response.request))) as refusal:
                complete.click()
            response = refusal.value
            require(response.status == 400 and ET.fromstring(response.body()).findtext('{*}Code') == 'EntityTooSmall',
                    'The fully reviewed set did not reach native minimum-size validation')
            expect(complete).to_be_enabled()
            on_retry = check_pages(index)
            require(completions == [current], 'Completion payload did not contain all 1001 reviewed parts')
            require(snapshot() == current, 'Native size refusal altered multipart state')
            expect_aws_error('NoSuchKey', client.get_object, Bucket=BUCKET, Key=KEY)
            index = len(parts_requests)
            page.get_by_label('Part number', exact=True).fill(str(PART_COUNT))
            page.get_by_label('Part file', exact=True).set_input_files(
                {'name': 'replacement.bin', 'mimeType': 'application/octet-stream',
                 'buffer': BROWSER_REPLACEMENT})
            with page.expect_response(lambda r: r.request.method == 'PUT'
                                      and request_kind(r.request).get('partNumber') == [str(PART_COUNT)]) as saved:
                page.get_by_role('button', name='Save part', exact=True).click()
            put = saved.value
            require(put.status == 200, 'Native browser UploadPart failed')
            browser_etag = put.headers.get('etag')
            # The exact native UploadPart derives a quoted MD5 ETag from the
            # received bytes; check these bytes without browser body capture.
            expected_etag = '"' + hashlib.md5(BROWSER_REPLACEMENT, usedforsecurity=False).hexdigest() + '"'
            require(browser_etag == expected_etag and browser_etag != changed_etag,
                    'Native browser replacement did not store the selected bytes')
            expect(page.get_by_text('Part saved.', exact=True)).to_be_visible()
            after_save = check_pages(index)
            check_visible_tail(len(BROWSER_REPLACEMENT), browser_etag)
            require(snapshot() == current[:-1] + [(PART_COUNT, browser_etag)],
                    'Browser replacement did not persist in native multipart state')
            confirm.fill('abort')
            page.get_by_role('button', name='Abort upload', exact=True).click()
            expect(page.get_by_text('Upload aborted and parts removed.', exact=True)).to_be_visible()
            expect_aws_error('NoSuchUpload', client.list_parts, **target)
            require(not client.list_multipart_uploads(Bucket=BUCKET).get('Uploads'), 'Native abort left active uploads')
            client.delete_bucket(Bucket=BUCKET)
            require(not client.list_buckets().get('Buckets'), 'Local bucket still exists after cleanup')
            require(not foreign and not errors and not redirected, 'Unexpected browser errors, redirects, or off-origin traffic')
        finally:
            browser.close()
    return [first, refreshed, on_retry, after_save]


if __name__ == '__main__':
    run()
