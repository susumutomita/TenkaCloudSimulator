"""Opt-in real browser acceptance for Azure Queue Storage, never a mock.

From the repository root, use an approved Linux runner with a working Chromium
sandbox, only loopback in its network namespace, no host network/socket mounts,
and enough temporary space for one copy of the built binary. Provision Python
3.11+ and the pinned requirements beforehand. Do not use an unsandboxed browser fallback.

    SIMULATOR_BINARY=/absolute/path/to/binary \
    SIMULATOR_BINARY_SHA256="$VERIFIED_BUILD_SHA256" \
    CHROMIUM_EXECUTABLE=/absolute/path/to/chromium \
    python3 test/e2e/azure_queue_console_gui.py

A successful receipt applies only to the supplied binary. Neither the interface
preflight nor browser request observation replaces external network isolation.
This test is not official SDK, Azure Portal parity, or Scalar Send evidence.
"""
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
from secrets import token_hex
import socket
import sys
import tempfile
from urllib.parse import parse_qs, urlsplit
import xml.etree.ElementTree as ET

BASE = '/_simulator/providers/azure-queue/tenkacloudlocal'
CAPS = '/_simulator/v1/capabilities'
LOGIN = '/_simulator/login'
CONSOLE = '/_simulator/console/'
ROUTE = CONSOLE + '#/azure-queue'
REFERENCE = '/_simulator/docs/azure-queue.html'
VERSION = '2023-11-03'
OPS = {'CreateQueue', 'DeleteQueue', 'PutMessage', 'GetMessages',
       'PeekMessages', 'UpdateMessage', 'DeleteMessage', 'ClearMessages'}


class AcceptanceFailure(RuntimeError):
    pass


def need(condition, message):
    if not condition:
        raise AcceptanceFailure(message)


def origin_port(endpoint):
    need(isinstance(endpoint, str) and re.fullmatch(r'http://127\.0\.0\.1:[1-9][0-9]{0,4}', endpoint),
         'expected the fixture numeric-loopback endpoint')
    port = int(endpoint.rsplit(':', 1)[1])
    need(port <= 65535, 'invalid fixture port')
    return port


def owns_url(endpoint, url):
    try:
        parsed = urlsplit(url)
        return parsed.scheme == 'http' and parsed.netloc == endpoint[len('http://'):]
    except ValueError:
        return False


def browser_url_allowed(endpoint, url):
    # data/about and our own blob URLs are not network destinations.
    return owns_url(endpoint, url) or url.startswith(('data:', 'blob:' + endpoint + '/')) or url == 'about:blank'


def preflight():
    need(sys.platform == 'linux' and os.geteuid() != 0, 'a non-root isolated Linux runner is required')
    need({name for _, name in socket.if_nameindex()} == {'lo'}, 'runner must have only loopback')
    need(not any(value and key.lower().endswith('_proxy') for key, value in os.environ.items()),
         'a clean environment without proxy settings is required')
    expected = os.environ.get('SIMULATOR_BINARY_SHA256', '')
    need(re.fullmatch(r'[a-f0-9]{64}', expected), 'trusted simulator SHA-256 is required')
    paths = [Path(os.environ.get(key, '')) for key in ('SIMULATOR_BINARY', 'CHROMIUM_EXECUTABLE')]
    need(all(p.is_absolute() and p.is_file() and os.access(p, os.X_OK) for p in paths),
         'explicit simulator and Chromium executables are required')
    return paths[0], paths[1], expected


def wire(port, token, method, path, expect=(200,), body=None):
    need(path.startswith('/_simulator/'), 'invalid fixture path')
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=8)
    headers = {'X-Simulator-Token': token, 'x-ms-version': VERSION}
    if body is not None:
        headers['Content-Type'] = 'application/json'
    try:
        connection.request(method, path, None if body is None else json.dumps(body).encode(), headers)
        response = connection.getresponse()  # Never follows redirects or uses a proxy.
        data = response.read(131073)
        need(response.status in expect and len(data) <= 131072, 'unexpected Queue fixture HTTP status/body')
        return response.status, data
    finally:
        connection.close()


def rows(raw):
    root = ET.fromstring(raw)
    need(root.tag == 'QueueMessagesList' and all(item.tag == 'QueueMessage' for item in root),
         'unexpected native XML message list')
    return [{field.tag: field.text or '' for field in item} for item in root]


def check_message(messages, text, count, message_id=None):
    need(len(messages) == 1, 'expected exactly one synthetic message')
    item = messages[0]
    ident = item.get('MessageId', '')
    need(re.fullmatch(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}', ident) and (message_id is None or ident == message_id)
         and item.get('MessageText') == text and item.get('DequeueCount') == str(count),
         'native message identity/text/dequeue count did not match')
    return item


def match_response(endpoint, response, method, path, query):
    if response.request.method != method or not owns_url(endpoint, response.url):
        return False
    parsed = urlsplit(response.url)
    try:
        observed = parse_qs(parsed.query, keep_blank_values=True, strict_parsing=True)
    except ValueError:
        return False
    return parsed.path == path and observed == {key: [value] for key, value in query.items()}


def console_fixture(browser, expect, endpoint, port, token, target, peer_token):
    misses, page_errors = [], []

    def guarded_page(context):
        def route_guard(route):
            if browser_url_allowed(endpoint, route.request.url):
                route.continue_()
            else:
                misses.append(True)
                route.abort()

        # Observation includes the redirected requests Playwright routing alone
        # cannot block. The externally isolated runner is still mandatory.
        context.route('**/*', route_guard)
        context.route_web_socket('**/*', lambda ws: (misses.append(True), ws.close()))
        context.on('request', lambda r: misses.append(True) if not browser_url_allowed(endpoint, r.url) else None)
        page = context.new_page()
        page.set_default_timeout(12000)
        page.on('pageerror', lambda _: page_errors.append(True))
        return page

    def sign_in(page, value):
        need(page.goto(endpoint + LOGIN).status == 200, 'sign-in page did not load')
        page.get_by_label('Access token', exact=True).fill(value)
        with page.expect_response(lambda r: match_response(endpoint, r, 'POST', LOGIN, {})) as login:
            page.get_by_role('button', name='Open sandbox', exact=True).click()
        need(login.value.status == 303, 'ordinary local form sign-in failed')
        page.wait_for_url(endpoint + CONSOLE)
        expect(page.get_by_role('navigation', name='Cloud provider')).to_be_visible()

    def open_azure_catalog(page):
        nav = page.get_by_role('navigation', name='Cloud provider')
        nav.get_by_role('button', name='Azure', exact=True).click()
        page.get_by_role('button', name='Services', exact=True).click()
        expect(page.get_by_role('menuitem', name='Blob Storage', exact=True)).to_be_visible()

    def peer_denied():
        # Real separately authenticated context, no forged cookie, route
        # fulfillment, credential replacement, or spoofed principal.
        with browser.new_context(service_workers='block', locale='en-US') as peer_context:
            page = guarded_page(peer_context)
            api_calls = []
            page.on('request', lambda r: api_calls.append(True) if urlsplit(r.url).path.startswith(BASE) else None)
            sign_in(page, peer_token)
            open_azure_catalog(page)
            expect(page.get_by_role('menuitem', name='Queue Storage', exact=True)).to_have_count(0)
            page.goto(endpoint + ROUTE)
            expect(page.get_by_role('heading', name='Console unavailable', exact=True)).to_be_visible()
            expect(page.locator('.azure-queue-view')).to_have_count(0)
            need(not api_calls, 'peer view attempted native Queue operations')
            wire(port, peer_token, 'GET', target + '/messages?peekonly=true', (403,))

    queue = target.rsplit('/', 1)[1]
    target_messages = target + '/messages'
    peek_query = {'peekonly': 'true', 'numofmessages': '8'}
    body = 'Console fixture: 学習 & <queue>'
    changed = 'Console fixture revised: 更新 & <queue>'
    completed = set()

    def readback():
        return rows(wire(port, token, 'GET', target_messages + '?peekonly=true')[1])

    with browser.new_context(service_workers='block', locale='en-US', viewport={'width': 1280, 'height': 960}) as context:
        page = guarded_page(context)
        unexpected_queue_paths = []
        page.on('request', lambda r: unexpected_queue_paths.append(True)
                if urlsplit(r.url).path.startswith(BASE) and not (
                    urlsplit(r.url).path == target or urlsplit(r.url).path.startswith(target + '/')) else None)
        sign_in(page, token)
        open_azure_catalog(page)
        page.get_by_role('menuitem', name='Queue Storage', exact=True).click()
        page.wait_for_url(endpoint + ROUTE)
        view = page.locator('.azure-queue-view')
        expect(view.get_by_role('heading', name='Azure Queue Storage', exact=True)).to_be_visible()
        expect(view.get_by_text('Named queues only.', exact=False)).to_be_visible()
        expect(view.get_by_role('link', name='Local API reference', exact=True)).to_have_attribute('href', REFERENCE)

        def button(name):
            return view.get_by_role('button', name=name, exact=True)

        def click(name, method, path, status, query=None, operation=None):
            with page.expect_response(lambda r: match_response(endpoint, r, method, path, query or {})) as receipt:
                button(name).click()
            response = receipt.value
            need(response.status == status and response.request.redirected_from is None
                 and response.request.header_value('x-ms-version') == VERSION
                 and not response.request.header_value('x-simulator-token'),
                 'unexpected real browser Queue wire response')
            expect(view).to_have_attribute('aria-busy', 'false')
            if operation:
                completed.add(operation)
            return response

        def click_messages(name, query, operation):
            observer = Path(__file__).with_name('azure_queue_browser_capture.js').read_text()
            capture = page.evaluate_handle(observer, {'path': target_messages, 'query': query})
            try:
                click(name, 'GET', target_messages, 200, query, operation)
                page.wait_for_function('capture => capture.done', arg=capture, timeout=12000)
                text = capture.evaluate('capture => capture.text')
                need(isinstance(text, str), 'missing or invalid real browser Queue response body')
                return rows(text)
            finally:
                capture.evaluate('capture => capture.stop()')
                capture.dispose()

        wire(port, token, 'GET', target_messages, (404,))
        view.get_by_label('Queue name', exact=True).fill(queue)
        click('Create queue', 'PUT', target, 201, operation='CreateQueue')
        expect(view.get_by_role('heading', name=queue, exact=True)).to_be_visible()
        need(readback() == [], 'new queue was not empty')

        button('Switch queue').click()
        view.get_by_label('Queue name', exact=True).fill(queue)
        click('Open queue', 'GET', target_messages, 200, peek_query)
        expect(view.get_by_role('heading', name=queue, exact=True)).to_be_visible()

        def add(text):
            view.get_by_label('Message text (up to 64 KiB)', exact=True).fill(text)
            view.get_by_label('Lifetime in seconds (-1 to keep indefinitely)', exact=True).fill('600')
            click('Add message', 'POST', target_messages, 201,
                  {'messagettl': '600', 'visibilitytimeout': '0'}, 'PutMessage')

        add(body)
        initial = check_message(readback(), body, 0)
        ident = initial['MessageId']
        need('PopReceipt' not in initial, 'initial Peek exposed a receipt')
        peer_denied()
        check_message(readback(), body, 0, ident)

        peek = click_messages('Peek visible messages', peek_query, 'PeekMessages')
        check_message(peek, body, 0, ident)
        peek_results = view.get_by_role('region', name='Peek results', exact=True)
        peek_results.get_by_text(ident, exact=True).click()
        expect(peek_results.locator('pre')).to_have_text(body)

        view.get_by_label('Receive visibility timeout (seconds)', exact=True).fill('300')
        got = click_messages('Receive and reserve', {'numofmessages': '8', 'visibilitytimeout': '300'}, 'GetMessages')
        old = check_message(got, body, 1, ident).get('PopReceipt')
        need(old and readback() == [], 'receive did not reserve and issue a receipt')
        manage = view.get_by_role('group', name='Manage a received message', exact=True)
        expect(manage.get_by_role('combobox')).to_have_value(ident)

        view.get_by_label('Also replace the message text', exact=True).check()
        manage.get_by_role('textbox').fill(changed)
        update = click('Update message', 'PUT', target_messages + '/' + ident, 204,
                       {'popreceipt': old, 'visibilitytimeout': '0'}, 'UpdateMessage')
        new = update.header_value('x-ms-popreceipt')
        need(new and new != old and update.header_value('x-ms-time-next-visible'), 'update did not return a fresh receipt')
        check_message(readback(), changed, 1, ident)

        expect(button('Delete message')).to_be_disabled()
        view.get_by_label('Permanently delete this message', exact=True).check()
        click('Delete message', 'DELETE', target_messages + '/' + ident, 204, {'popreceipt': new}, 'DeleteMessage')
        need(readback() == [], 'message still present before ClearMessages')

        add('Separate clear fixture')
        check_message(readback(), 'Separate clear fixture', 0)
        confirm = view.get_by_label('Type the exact queue name to confirm', exact=True)
        expect(button('Clear messages')).to_be_disabled()
        confirm.fill(queue)
        click('Clear messages', 'DELETE', target_messages, 204, operation='ClearMessages')
        need(readback() == [], 'clear did not leave an existing empty queue')

        expect(button('Delete queue')).to_be_disabled()
        confirm.fill(queue)
        click('Delete queue', 'DELETE', target, 204, operation='DeleteQueue')
        wire(port, token, 'GET', target_messages, (404,))
        expect(view.get_by_role('heading', name='Azure Queue Storage', exact=True)).to_be_visible()
        need(completed == OPS and not unexpected_queue_paths, 'Queue coverage/path scope incomplete')

    need(not misses and not page_errors, 'unexpected browser request or JavaScript error')


def run():
    binary, chromium, expected = preflight()
    with binary.open('rb') as build:
        sha = hashlib.file_digest(build, 'sha256').hexdigest()
        need(sha == expected, 'simulator binary SHA-256 mismatch')
        with tempfile.TemporaryDirectory(prefix='azure-queue-browser-') as directory:
            storage = os.statvfs(directory)
            need(storage.f_bavail * storage.f_frsize >= os.fstat(build.fileno()).st_size + 16 * 1024**2,
                 'insufficient scratch for the verified binary copy and browser profile')
            os.environ['SIMULATOR_REPORTS'] = str(Path(directory) / 'reports')
            from playwright.sync_api import sync_playwright, expect
            import check
            # Bind the helper's one temporary copy to the verified open inode,
            # not to a path that could be replaced in between check and launch.
            check.BINARY = Path(f'/proc/self/fd/{build.fileno()}')
            with check.running(db=Path(directory) / 'state.db', authenticated=True) as (endpoint, token):
                port = origin_port(endpoint)
                need(isinstance(token, str) and re.fullmatch('[a-f0-9]{64}', token), 'invalid local operator fixture token')
                capdoc = json.loads(wire(port, token, 'GET', CAPS)[1])
                cap = capdoc.get('azureQueue', {})
                # An operator with no selected exercise identity sees the default owner's queue.
                need(capdoc.get('principal') == '' and capdoc.get('identitySelection') is True
                     and cap.get('console') is True
                     and cap.get('endpoint') == BASE and cap.get('account') == 'tenkacloudlocal'
                     and cap.get('apiVersion') == VERSION and cap.get('reference') == REFERENCE
                     and len(cap.get('operations', [])) == len(OPS) and set(cap['operations']) == OPS,
                     'expected integrated owner-only Queue console capability')
                peer = 'azure-queue-browser-peer'
                issued = json.loads(wire(port, token, 'POST', '/_simulator/v1/access', body={'Principal': peer})[1])
                peer_token = issued.get('AccessToken', '')
                need(issued.get('Principal') == peer and isinstance(peer_token, str)
                     and re.fullmatch('[a-f0-9]{64}', peer_token) and peer_token != token,
                     'independent peer session was not issued')
                peer_caps = json.loads(wire(port, peer_token, 'GET', CAPS)[1])
                need(peer_caps.get('principal') == peer and peer_caps.get('identitySelection') is False
                     and 'azureQueue' not in peer_caps,
                     'Queue capability exposed to peer')
                target = BASE + '/gui-queue-' + token_hex(6)
                try:
                    with sync_playwright() as p:
                        with p.chromium.launch(executable_path=str(chromium), chromium_sandbox=True,
                                               args=['--no-proxy-server']) as browser:
                            console_fixture(browser, expect, endpoint, port, token, target, peer_token)
                finally:
                    status, _ = wire(port, token, 'GET', target + '/messages?peekonly=true', (200, 404))
                    if status == 200:
                        wire(port, token, 'DELETE', target, (204,))
                        wire(port, token, 'GET', target + '/messages?peekonly=true', (404,))
                        raise AcceptanceFailure('browser did not complete cleanup; fixture removed through native HTTP')
        build.seek(0)
        need(hashlib.file_digest(build, 'sha256').hexdigest() == sha, 'binary changed during acceptance')
    return {'status': 'passed', 'kind': 'real-authenticated-azure-queue-browser',
            'binary_sha256': sha, 'operations': sorted(OPS), 'peer_denied': True, 'cleanup': 'passed'}


if __name__ == '__main__':
    try:
        print(json.dumps(run(), sort_keys=True))
    except Exception as exc:
        # Playwright exception details may include URLs, receipts or form data.
        # Do not print them or enable trace/HAR/screenshot collection.
        print(json.dumps({'status': 'not-passed', 'error': str(exc) if isinstance(exc, AcceptanceFailure)
                          else type(exc).__name__}, sort_keys=True))
        sys.exit(1)
