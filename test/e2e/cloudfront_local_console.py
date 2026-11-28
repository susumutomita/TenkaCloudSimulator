"""Real Cloudscape/Scalar reads and invalidation over official SDK-created local S3 state."""
from contextlib import ExitStack, contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import tempfile
from urllib.parse import parse_qs, urlsplit
from xml.etree import ElementTree as ET

from playwright.sync_api import expect, sync_playwright

from cloudfront_local_content import (BUCKET, PREFIX, client, config, local,
                                      ordinary_session, successful)
from cloudfront_local_scalar import LOCAL_TEMPLATE, send
from sakura_cli import frame
from sts_session_lifecycle import LocalSimulator

NS = '{http://cloudfront.amazonaws.com/doc/2020-05-31/}'
KEY = 'asset.txt'
OLD = b'<em>old local bytes</em> <img src="https://outside.invalid/never">'
FRESH = b'<em>fresh local bytes</em> <img src="https://outside.invalid/never">'
READER = 'cloudfront-content-reader'


@contextmanager
def owned_resources():
    stack = ExitStack()
    try:
        yield stack
    except BaseException as primary:
        try:
            stack.close()
        except BaseException as cleanup_error:
            primary.add_note('Owned fixture cleanup also failed: ' + type(cleanup_error).__name__)
        raise
    else:
        stack.close()


@contextmanager
def storage_fault(team, kind):
    assert kind in ('write', 'read') and team.database.parent == team.directory
    with sqlite3.connect(team.database, timeout=5) as database:
        if kind == 'write':
            database.execute("CREATE TRIGGER fixture_cf_receipt AFTER INSERT ON aws_cloudfront_invalidations BEGIN SELECT RAISE(ABORT,'local fixture interruption'); END")
        else:
            database.execute('ALTER TABLE aws_cloudfront_invalidations RENAME TO fixture_cf_receipts_offline')
    try:
        yield
    finally:
        with sqlite3.connect(team.database, timeout=5) as database:
            if kind == 'write':
                database.execute('DROP TRIGGER fixture_cf_receipt')
            else:
                database.execute('ALTER TABLE fixture_cf_receipts_offline RENAME TO aws_cloudfront_invalidations')


def receipt_count(team, identifier):
    with sqlite3.connect(team.database.as_uri() + '?mode=ro', uri=True) as database:
        return database.execute('SELECT count(*) FROM aws_cloudfront_invalidations WHERE distribution=?', (identifier,)).fetchone()[0]


def reader_policy(owner, arn, deny=False):
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': 'cloudfront:ListDistributions', 'Resource': '*'},
        {'Effect': 'Allow', 'Action': 'cloudfront:GetDistribution', 'Resource': arn},
        {'Effect': 'Deny' if deny else 'Allow', 'Action': 's3:GetObject', 'Resource': 'arn:aws:s3:::' + BUCKET + '/' + KEY},
    ]}
    local(owner, 'PUT', '/_simulator/v1/iam/principals/' + READER + '/policy', 200, policy)


class Console:
    def __init__(self, browser, team, token=None):
        self.team, self.principal = team, 'simulator-admin'
        self.context = browser.new_context(service_workers='block', viewport={'width': 1440, 'height': 1000})
        self.context.set_default_timeout(20_000)
        self.outside, self.errors, self.requests = [], [], []
        self.context.route('**/*', self.guard)
        self.context.on('request', self.observe)
        self.page = self.context.new_page()
        self.page.on('pageerror', lambda error: self.errors.append(type(error).__name__))
        self.page.goto(team.endpoint + '/_simulator/console/')
        self.page.get_by_label('Access token', exact=True).fill(token or team.token)
        with self.page.expect_response(lambda response: urlsplit(response.url).path == '/_simulator/v1/capabilities') as discovered:
            self.page.get_by_role('button', name='Open sandbox', exact=True).click()
        capabilities = discovered.value.json()
        self.principal = capabilities['principal'] or 'simulator-admin'
        self.identity_selection = capabilities['identitySelection']
        assert capabilities['services']['cloudfront']['distributionConsole'] is True
        self.page.get_by_role('button', name='Services', exact=True).click()
        self.page.get_by_role('menuitem', name='CloudFront', exact=True).click()
        expect(self.page.get_by_role('table', name='Distributions', exact=True)).to_be_visible()

    def guard(self, route):
        target = urlsplit(route.request.url)
        if target.scheme == 'http' and target.netloc == f'127.0.0.1:{self.team.port}':
            route.continue_()
        else:
            self.outside.append(True)
            route.abort()

    def observe(self, request):
        target = urlsplit(request.url)
        if not target.path.startswith((PREFIX, '/_simulator/cloudfront/distributions/')):
            return
        headers = request.all_headers()
        assert target.netloc == f'127.0.0.1:{self.team.port}' and not target.fragment
        assert request.redirected_from is None and headers.get('cookie')
        assert not {'authorization', 'x-simulator-token', 'x-simulator-access-role', 'x-amz-security-token'} & headers.keys()
        console = '/_simulator/console/' in request.frame.page.url
        assert headers.get('x-simulator-principal') == (self.principal if console else None)
        if target.path.startswith(PREFIX):
            assert headers['x-simulator-service'] == 'cloudfront' and headers['x-simulator-region'] == 'us-east-1'
            assert request.method in ('GET', 'POST')
        else:
            assert request.method == 'GET' and not target.query
            assert not {'x-simulator-service', 'x-simulator-region'} & headers.keys()
        self.requests.append(request)

    def response(self, path, method='GET'):
        return self.page.expect_response(lambda response: urlsplit(response.url).path == path and response.request.method == method)

    def select(self, identifier):
        capture = self.page.evaluate_handle(Path(__file__).with_name('azure_queue_browser_capture.js').read_text(),
            {'path': PREFIX + '/' + identifier, 'query': {}})
        try:
            with self.response(PREFIX + '/' + identifier) as fetched:
                self.page.get_by_role('table', name='Distributions', exact=True).get_by_role('button', name=identifier, exact=True).click()
            response = fetched.value
            assert response.status == 200
            self.page.wait_for_function('capture => capture.done', arg=capture, timeout=12000)
            captured = capture.evaluate('capture => capture.text')
            assert captured is not None
            root = ET.fromstring(captured)
        finally:
            capture.evaluate('capture => capture.stop()')
            capture.dispose()
        assert root.findtext(NS + 'Id') == identifier
        expect(self.page.get_by_role('heading', name=identifier, exact=True)).to_be_visible()
        expect(self.page.get_by_text(response.headers['etag'], exact=True)).to_be_visible()
        return response.headers['etag']

    def read(self, identifier, body, status=200, japanese=False):
        key_label = 'オブジェクトキー' if japanese else 'Object key'
        action = 'ローカルコンテンツを読み取る' if japanese else 'Read local content'
        self.page.get_by_label(key_label, exact=True).fill(KEY)
        path = f'/_simulator/cloudfront/distributions/{identifier}/objects/{KEY}'
        capture = self.page.evaluate_handle(Path(__file__).with_name('azure_queue_browser_capture.js').read_text(),
            {'path': path, 'query': {}}) if status == 200 else None
        if status != 200:
            install_observer(self, path, 'GET', 'observe')
        try:
            with self.response(path) as fetched:
                self.page.get_by_role('button', name=action, exact=True).click()
            response = fetched.value
            assert response.status == status
            if capture is not None:
                self.page.wait_for_function('capture => capture.done', arg=capture, timeout=12000)
                observed = capture.evaluate('capture => capture.text')
                assert observed is not None and observed.encode('utf-8') == body
            else:
                self.page.wait_for_function('window.__cfObservation.ready')
                error_body = self.page.evaluate('window.__cfObservation.body')
        finally:
            if capture is not None:
                capture.evaluate('capture => capture.stop()')
                capture.dispose()
            else:
                remove_observer(self)
        if status == 200:
            assert response.headers['content-disposition'] == 'attachment'
            assert response.headers['x-content-type-options'] == 'nosniff'
            assert response.headers['content-security-policy'] == "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
            expect(self.page.get_by_text(body.decode(), exact=True)).to_be_visible()
        else:
            assert ET.fromstring(error_body).findtext(NS + 'Error/' + NS + 'Code') == 'AccessDenied'
            expect(self.page.get_by_text(re.compile('AccessDenied:'))).to_be_visible()
            expect(self.page.get_by_text(FRESH.decode(), exact=True)).to_have_count(0)
            expect(self.page.get_by_role('button', name='Download saved bytes', exact=True)).to_be_disabled()
        return response

    def open_invalidation(self, replay=False):
        self.page.get_by_role('button', name='Replay same invalidation' if replay else 'Create invalidation', exact=True).click()
        dialog = self.page.get_by_role('dialog', name='Confirm invalidation', exact=True)
        expect(dialog).to_be_visible()
        expect(dialog.get_by_role('button', name='Invalidate all objects', exact=True)).to_be_disabled()
        return dialog, dialog.get_by_label('Caller reference', exact=True).input_value()

    def submit(self, identifier, dialog, status, *, lost_ack=False):
        if not lost_ack:
            install_observer(self, PREFIX + '/' + identifier + '/invalidation', 'POST', 'observe')
        try:
            dialog.get_by_role('checkbox').check()
            with self.response(PREFIX + '/' + identifier + '/invalidation', 'POST') as fetched:
                dialog.get_by_role('button', name='Invalidate all objects', exact=True).click()
            response = fetched.value
            assert response.status == status
            self.page.wait_for_function('window.__cfObservation.ready')
            observed = self.page.evaluate('window.__cfObservation.body')
            assert response.request.post_data and response.request.method == 'POST'
            root = ET.fromstring(response.request.post_data)
            assert root.findtext(NS + 'Paths/' + NS + 'Quantity') == '1'
            assert [n.text for n in root.findall(NS + 'Paths/' + NS + 'Items/' + NS + 'Path')] == ['/*']
            if status == 201:
                result = ET.fromstring(observed)
                assert result.findtext(NS + 'Status') == 'Completed'
                return result.findtext(NS + 'Id'), root.findtext(NS + 'CallerReference'), response.request.post_data
            assert ET.fromstring(observed).findtext(NS + 'Error/' + NS + 'Code') == 'InternalError'
            return None, root.findtext(NS + 'CallerReference'), response.request.post_data
        finally:
            if not lost_ack:
                remove_observer(self)

    def close(self):
        self.context.close()
        assert not self.outside and not self.errors


# A timing/failure observer, never an HTTP responder: the original browser
# fetch sends unchanged arguments, and its real Response is retained untouched.
FETCH_OBSERVER = r"""({url, method, mode}) => {
  if (window.__cfObservation) throw Error('observer already installed');
  const original = window.fetch;
  const state = {ready:false, intercepted:false, returned:false};
  window.__cfObservation = state;
  state.restore = () => {window.fetch = original; state.release?.();};
  window.fetch = async function(input, init) {
    const response = await original.call(this, input, init);
    const target = new URL(typeof input === 'string' ? input : input.url, location.href);
    const verb = init?.method || (typeof input === 'string' ? 'GET' : input.method);
    if (state.intercepted || target.href !== url || verb !== method) return response;
    state.intercepted = true;
    state.response = response;
    const reader = response.clone().body.getReader();
    const decoder = new TextDecoder('utf-8', {fatal:true});
    let size = 0, body = '';
    for (;;) {
      let timer;
      const chunk = await Promise.race([reader.read(), new Promise((_, reject) => {
        timer = setTimeout(() => reject(Error('observer read timed out')), 15000);
      })]).finally(() => clearTimeout(timer));
      if (chunk.done) break;
      size += chunk.value.byteLength;
      if (size > 262144) {reader.cancel().catch(() => {}); throw Error('observer size limit');}
      body += decoder.decode(chunk.value, {stream:true});
    }
    state.body = body + decoder.decode(); state.status = response.status; state.ready = true;
    if (mode === 'lose') {
      window.fetch = original;
      throw TypeError('local fixture acknowledgement interrupted');
    }
    if (mode === 'observe') {window.fetch = original; return response;}
    await new Promise(resolve => {
      const timer = setTimeout(resolve, 15000);
      state.release = () => {clearTimeout(timer); resolve();};
    });
    state.returned = true;
    return response;
  };
}"""


def install_observer(surface, path, method, mode):
    surface.page.evaluate(FETCH_OBSERVER, {'url': surface.team.endpoint + path, 'method': method, 'mode': mode})


def remove_observer(surface):
    surface.page.evaluate("() => {window.__cfObservation?.restore(); delete window.__cfObservation;}")


def browser_checks(browser, owner, saved, all_ids, replace_object, get_receipt, directory):
    identifier = saved['Id']
    surface = Console(browser, owner)
    try:
        document = surface.context.request.get(owner.endpoint + '/_simulator/docs/cloudfront-local.json')
        assert document.status == 200
        spec = document.json()
        table = surface.page.get_by_role('table', name='Distributions', exact=True)
        expect(table.get_by_role('button', name=re.compile('^D[a-f0-9]{24}$'))).to_have_count(25)
        with surface.response(PREFIX) as next_page:
            surface.page.get_by_role('button', name='Next page', exact=True).click()
        assert parse_qs(urlsplit(next_page.value.url).query) == {'MaxItems': ['25'], 'Marker': [all_ids[24]]}
        expect(table.get_by_role('button', name=re.compile('^D[a-f0-9]{24}$'))).to_have_count(1)
        expect(table.get_by_role('button', name=all_ids[25], exact=True)).to_be_visible()
        with surface.response(PREFIX) as previous:
            surface.page.get_by_role('button', name='Previous page', exact=True).click()
        assert parse_qs(urlsplit(previous.value.url).query) == {'MaxItems': ['25']}
        etag = surface.select(identifier)
        path = f'/_simulator/cloudfront/distributions/{identifier}/objects/{KEY}'
        surface.read(identifier, OLD)
        send(surface.context, owner, 'GET', LOCAL_TEMPLATE, identifier, spec, path, 200, OLD.decode(), directory,
             object_key=KEY, expected_body=OLD, show_raw_html=True)
        replace_object()
        assert surface.read(identifier, OLD).headers['x-cache'].startswith('Hit')
        dialog, _ = surface.open_invalidation()
        count = len([request for request in surface.requests if request.method == 'POST'])
        dialog.get_by_role('button', name='Cancel', exact=True).click()
        expect(surface.page.get_by_role('button', name='Create invalidation', exact=True)).to_be_focused()
        assert len([request for request in surface.requests if request.method == 'POST']) == count
        dialog, caller = surface.open_invalidation()
        first, actual_caller, _ = surface.submit(identifier, dialog, 201)
        assert actual_caller == caller, 'visible caller reference differs from original request'
        assert get_receipt(first)['InvalidationBatch']['CallerReference'] == caller, 'native readback caller reference differs'
        expect(surface.page.get_by_text(first, exact=True)).to_be_visible()
        expect(surface.page.get_by_text(OLD.decode(), exact=True)).to_have_count(0)
        surface.read(identifier, FRESH)
        with surface.page.expect_download() as download:
            surface.page.get_by_role('button', name='Download saved bytes', exact=True).click()
        destination = Path(directory) / 'console-object.bin'
        download.value.save_as(destination)
        assert destination.read_bytes() == FRESH
        send(surface.context, owner, 'GET', LOCAL_TEMPLATE, identifier, spec, path, 200, FRESH.decode(), directory,
             object_key=KEY, expected_body=FRESH, show_raw_html=True)
        # A read outage may clear presentation, but must preserve the exact known lookup ID for retry.
        with storage_fault(owner, 'read'):
            with surface.response(PREFIX + '/' + identifier + '/invalidation/' + first) as failed:
                surface.page.get_by_role('button', name='Refresh invalidation', exact=True).click()
            assert failed.value.status == 500
            expect(surface.page.get_by_text(first, exact=True)).to_have_count(0)
        with surface.response(PREFIX + '/' + identifier + '/invalidation/' + first) as recovered:
            surface.page.get_by_role('button', name='Refresh invalidation', exact=True).click()
        assert recovered.value.status == 200
        expect(surface.page.get_by_text(first, exact=True)).to_be_visible()
        # The real storage failure is a rolled-back request, not a committed lost acknowledgement.
        before = frame(owner.database)
        with storage_fault(owner, 'write'):
            dialog, caller = surface.open_invalidation()
            _, original, failed_body = surface.submit(identifier, dialog, 500)
            assert original == caller
            expect(surface.page.get_by_role('button', name='Replay same invalidation', exact=True)).to_be_visible()
        assert frame(owner.database) == before
        dialog, repeated = surface.open_invalidation(True)
        assert repeated == caller
        second, _, replay_body = surface.submit(identifier, dialog, 201)
        assert replay_body == failed_body and get_receipt(second)['InvalidationBatch']['CallerReference'] == caller
        assert receipt_count(owner, identifier) == 2
        # A second, distinct case really commits before the application loses its ACK.
        install_observer(surface, PREFIX + '/' + identifier + '/invalidation', 'POST', 'lose')
        try:
            dialog, caller = surface.open_invalidation()
            before_count = receipt_count(owner, identifier)
            third, original, committed_body = surface.submit(identifier, dialog, 201, lost_ack=True)
            assert original == caller
            surface.page.wait_for_function('window.__cfObservation.ready')
            observation = surface.page.evaluate('({status:window.__cfObservation.status,body:window.__cfObservation.body,unused:!window.__cfObservation.response.bodyUsed})')
            assert observation['status'] == 201 and observation['unused']
            observed = ET.fromstring(observation['body'])
            assert observed.findtext(NS + 'Id') == third
            expect(surface.page.get_by_role('button', name='Replay same invalidation', exact=True)).to_be_visible()
            expect(surface.page.get_by_text(third, exact=True)).to_have_count(0)
            assert receipt_count(owner, identifier) == before_count + 1
            assert get_receipt(third)['InvalidationBatch']['CallerReference'] == caller
        finally:
            remove_observer(surface)
        dialog, repeated = surface.open_invalidation(True)
        assert repeated == caller
        recovered_id, _, replay_body = surface.submit(identifier, dialog, 201)
        assert recovered_id == third and replay_body == committed_body
        assert receipt_count(owner, identifier) == before_count + 1
        surface.read(identifier, FRESH)
        # The old owner's genuine 200 arrives after an actual selected-identity change.
        install_observer(surface, path, 'GET', 'hold')
        try:
            surface.page.get_by_role('button', name='Read local content', exact=True).click()
            surface.page.wait_for_function('window.__cfObservation.ready')
            assert surface.page.evaluate('window.__cfObservation.status') == 200
            assert surface.page.evaluate('window.__cfObservation.body') == FRESH.decode()
            surface.page.get_by_role('button', name='Account menu', exact=True).click()
            surface.page.get_by_role('menuitem', name='Choose identity', exact=True).click()
            identity = surface.page.get_by_role('dialog', name='Choose identity', exact=True)
            identity.get_by_label('Principal name', exact=True).fill(READER)
            surface.principal = READER
            identity.get_by_role('button', name='Choose identity', exact=True).click()
            expect(surface.page.get_by_role('table', name='Distributions', exact=True)).to_be_visible()
            surface.page.evaluate('window.__cfObservation.release()')
            surface.page.wait_for_function('window.__cfObservation.returned')
            surface.page.evaluate('() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))')
            expect(surface.page.get_by_text(FRESH.decode(), exact=True)).to_have_count(0)
            expect(surface.page.get_by_text(third, exact=True)).to_have_count(0)
        finally:
            remove_observer(surface)
        surface.select(identifier)
        assert surface.read(identifier, FRESH).headers['x-cache'].startswith('Miss')
        reader_policy(owner, saved['ARN'], deny=True)
        before = frame(owner.database)
        surface.read(identifier, FRESH, status=403)
        assert frame(owner.database) == before
        assert len([request for request in surface.requests if request.method == 'POST']) == 5
        return {'surface': surface, 'etag': etag, 'lastReceipt': third, 'path': path}
    except BaseException as primary:
        try:
            surface.close()
        except BaseException as cleanup_error:
            primary.add_note('Browser cleanup also failed: ' + type(cleanup_error).__name__)
        raise


def remove_distribution(front, identifier):
    current = successful(front.get_distribution_config, Id=identifier)
    value = current['DistributionConfig']
    value['Enabled'] = False
    disabled = successful(front.update_distribution, Id=identifier, IfMatch=current['ETag'], DistributionConfig=value)
    successful(front.delete_distribution, status=204, Id=identifier, IfMatch=disabled['ETag'])


def remove_origin(front, s3):
    assert not successful(front.list_distributions)['DistributionList'].get('Items', [])
    successful(s3.delete_object, status=204, Bucket=BUCKET, Key=KEY)
    successful(s3.delete_bucket, status=204, Bucket=BUCKET)


def run():
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    digest = os.environ['SIMULATOR_BINARY_SHA256']
    assert re.fullmatch('[a-f0-9]{64}', digest)
    with binary.open('rb') as source:
        assert hashlib.file_digest(source, 'sha256').hexdigest() == digest
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    session, settings = ordinary_session()
    with tempfile.TemporaryDirectory(prefix='cloudfront-console-') as directory, owned_resources() as stack:
        stats = {'sends': 0, 'responses': 0}
        teams = []
        for name, body in [('owner', OLD), ('peer', b'peer content')]:
            team = LocalSimulator(Path(directory) / name, binary)
            stack.callback(team.close)
            team.start()
            s3 = client(session, 's3', team, stack, settings, stats, key=KEY)
            front = client(session, 'cloudfront', team, stack, settings, stats, key=KEY)
            successful(s3.create_bucket, Bucket=BUCKET)
            successful(s3.put_object, Bucket=BUCKET, Key=KEY, Body=body, ContentType='text/html')
            stack.callback(remove_origin, front, s3)
            teams.append((team, s3, front))
        (owner, s3, front), (peer, peer_s3, peer_front) = teams
        assert owner.token != peer.token and owner.port != peer.port
        with sync_playwright() as playwright:
            with playwright.chromium.launch(executable_path=str(executable), chromium_sandbox=True) as browser:
                empty = Console(browser, owner)
                try:
                    expect(empty.page.get_by_text('No distributions on this page.', exact=True)).to_be_visible()
                finally:
                    empty.close()
                saved = []
                for number in range(26):
                    value = successful(front.create_distribution, status=201, DistributionConfig=config('console-' + str(number), ttl=3600))['Distribution']
                    stack.callback(remove_distribution, front, value['Id'])
                    saved.append(value)
                other = successful(peer_front.create_distribution, status=201, DistributionConfig=config('console-peer', ttl=3600))['Distribution']
                stack.callback(remove_distribution, peer_front, other['Id'])
                saved.sort(key=lambda value: value['Id'])
                target, sibling = saved[:2]
                for item in (target, sibling):
                    assert local(owner, 'GET', f'/_simulator/cloudfront/distributions/{item["Id"]}/objects/{KEY}', 200)[0] == OLD
                peer_path = f'/_simulator/cloudfront/distributions/{other["Id"]}/objects/{KEY}'
                assert local(peer, 'GET', peer_path, 200)[0] == b'peer content'
                peer_before = frame(peer.database)
                reader_policy(owner, target['ARN'])
                stack.callback(local, owner, 'DELETE', '/_simulator/v1/iam/principals/' + READER + '/policy', 204)
                result = browser_checks(browser, owner, target, [value['Id'] for value in saved],
                    lambda: successful(s3.put_object, Bucket=BUCKET, Key=KEY, Body=FRESH, ContentType='text/html'),
                    lambda identifier: successful(front.get_invalidation, DistributionId=target['Id'], Id=identifier)['Invalidation'], directory)
                result['surface'].close()
                participant = json.loads(local(owner, 'POST', '/_simulator/v1/access', 200, {'Principal': READER})[0])['AccessToken']
                bound = Console(browser, owner, participant)
                try:
                    assert bound.principal == READER and not bound.identity_selection
                    bound.select(target['Id'])
                    bound.read(target['Id'], FRESH, status=403)
                    bound.page.get_by_role('button', name='Account menu', exact=True).click()
                    expect(bound.page.get_by_role('menuitem', name='Choose identity', exact=True)).to_have_count(0)
                finally:
                    bound.close()
                assert successful(front.get_distribution, Id=sibling['Id'])['Distribution'] == sibling
                assert local(owner, 'GET', f'/_simulator/cloudfront/distributions/{sibling["Id"]}/objects/{KEY}', 200)[0] == OLD
                assert frame(peer.database) == peer_before
                last = successful(front.get_invalidation, DistributionId=target['Id'], Id=result['lastReceipt'])['Invalidation']
                owner.close()
                owner.start()
                assert successful(front.get_invalidation, DistributionId=target['Id'], Id=result['lastReceipt'])['Invalidation'] == last
                assert successful(front.get_distribution, Id=target['Id'])['ETag'] == result['etag']
                stored = successful(s3.get_object, Bucket=BUCKET, Key=KEY)['Body']
                try:
                    assert stored.read() == FRESH
                finally:
                    stored.close()
                reopened = Console(browser, owner)
                try:
                    reopened.select(target['Id'])
                    reopened.page.get_by_label('Language', exact=True).select_option('ja')
                    reopened.page.set_viewport_size({'width':390,'height':844})
                    expect(reopened.page.get_by_role('heading', name='ディストリビューションの詳細', exact=True)).to_be_visible()
                    assert reopened.read(target['Id'], FRESH, japanese=True).headers['x-cache'].startswith('Hit')
                    assert reopened.page.evaluate('document.documentElement.scrollWidth <= innerWidth + 1')
                finally:
                    reopened.close()
                assert frame(peer.database) == peer_before
                assert successful(front.get_distribution, Id=sibling['Id'])['Distribution'] == sibling
                assert local(peer, 'GET', peer_path, 200)[0] == b'peer content'
                assert frame(peer.database) == peer_before
        assert stats['sends'] == stats['responses']
    assert not Path(directory).exists()
    print(json.dumps({'ok': True, 'binarySha256': digest, 'nativePaging25Plus1': True,
                      'sameSDKScalarConsoleObject': True, 'safeDownload': True,
                      'knownReceiptReadRecovery': True, 'rollbackReplay': True, 'committedLostAckReplay': True,
                      'lateOwnerReadIgnored': True, 'hotCachePolicyDenial': True, 'participantBound': True,
                      'peerRestartPreserved': True, 'japanese390': True, 'cleanup': True}))


if __name__ == '__main__':
    run()
