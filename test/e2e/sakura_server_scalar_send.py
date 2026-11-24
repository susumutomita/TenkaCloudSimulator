"""Opt-in browser acceptance of the local Sakura Server List reference, not the console."""
import argparse
from contextlib import ExitStack
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit

from sakura_cli import BASIC, CLI, frame

TEMPLATE = '/_simulator/providers/sakura/cloud/zone/{zone}/api/cloud/1.1/server'
COLLECTION = TEMPLATE.replace('{zone}', 'is1a')
NAME = 'scalar-server-list-receipt'
CREATE = {'Server': {'Name': NAME, 'Description': 'saved local metadata', 'Tags': [],
                     'Icon': {'ID': 0, 'Tags': []}, 'InterfaceDriver': 'virtio',
                     'ServerPlan': {'CPU': 2, 'MemoryMB': 4096, 'Commitment': 'standard',
                                    'ConfidentialVM': False},
                     'PrivateHost': {'ID': 0, 'Description': '', 'Tags': []},
                     'WaitDiskMigration': False}}


def route_kind(origin, target, method, already_sent):
    endpoint = urlsplit(origin)
    url = urlsplit(target)
    if (url.scheme, url.netloc) != (endpoint.scheme, endpoint.netloc):
        return 'reject'
    if url.path == '/_simulator/providers' or url.path.startswith('/_simulator/providers/'):
        if not already_sent and method == 'GET' and target == origin + COLLECTION:
            return 'send'
        return 'reject'
    if method in ('GET', 'HEAD') and url.path.startswith('/_simulator/docs/'):
        return 'pass'
    return 'reject'


def run(binary, chromium):
    from playwright.sync_api import expect, sync_playwright

    with tempfile.TemporaryDirectory(prefix='sakura-server-scalar-') as temporary, ExitStack() as cleanup:
        instance = CLI(str(binary), Path(temporary), 'reference-server-list')
        cleanup.callback(instance.close)
        endpoint = urlsplit(instance.origin)
        assert endpoint.scheme == 'http' and endpoint.hostname == '127.0.0.1' and endpoint.port
        participant = instance.call('POST', '/_simulator/v1/access', {'Principal': 'learner'})['AccessToken']
        actor = {'token': participant, 'principal': ''}
        assert instance.call('GET', COLLECTION, **actor)['Servers'] == []
        row = instance.call('POST', COLLECTION, CREATE, 201, **actor)['Server']
        server_id = row['ID']
        assert isinstance(server_id, str) and server_id.isascii() and server_id.isdecimal()
        item = COLLECTION + '/' + server_id

        def remove():
            instance.call('DELETE', item, **actor)
            assert instance.call('GET', COLLECTION, **actor)['Servers'] == []

        cleanup.callback(remove)
        native = instance.call('GET', COLLECTION, **actor)
        assert native['is_ok'] is True and native['Count'] == native['Total'] == 1
        assert native['Servers'] == [row] and row['Name'] == NAME
        assert 'Availability' not in row

        playwright = cleanup.enter_context(sync_playwright())
        browser = playwright.chromium.launch(executable_path=str(chromium), args=['--no-sandbox'])
        cleanup.callback(browser.close)
        context = browser.new_context(service_workers='block', viewport={'width': 1440, 'height': 1000})
        cleanup.callback(context.close)
        context.set_default_timeout(30_000)
        outside, errors, server_requests = [], [], []

        def guard(route):
            kind = route_kind(instance.origin, route.request.url, route.request.method,
                              bool(server_requests))
            if kind == 'reject':
                outside.append(True)
                route.abort()
                return
            if kind == 'send':
                server_requests.append((route.request.method, route.request.url))
            route.continue_()

        context.route('**/*', guard)
        assert context.request.post(instance.origin+'/_simulator/login',
                                    form={'token': participant}, max_redirects=0).status == 303
        before = frame(instance.db)
        page = context.new_page()
        page.on('pageerror', lambda _: errors.append(True))
        assert page.goto(instance.origin+'/_simulator/docs/').status == 200
        page.get_by_role('button', name='Open Group - Sakura Cloud', exact=True).click()
        page.get_by_role('link', name='Sakura Server metadata', exact=True).click()
        expect(page.get_by_text('List saved Server metadata', exact=True).first).to_be_visible()
        page.get_by_role('button', name='Test Request (get '+TEMPLATE+')', exact=True).click()
        page.get_by_role('button', name=re.compile(r'^Authentication\s*Required')).click()
        username = page.get_by_text('Username:', exact=True).locator('..').locator('[contenteditable="true"]')
        expect(username).to_have_count(1)
        username.fill('TCSIMSAKURA')
        page.get_by_placeholder('********', exact=True).fill('dummy-local-only')
        target = instance.origin + COLLECTION
        assert not server_requests, 'native operation executed before clicking Send'

        with page.expect_response(lambda response: response.request.method == 'GET' and response.url == target) as sent:
            page.get_by_role('button').filter(has_text='Send Request').last.click()
        receipt = sent.value
        assert receipt.status == 200 and receipt.request.redirected_from is None
        assert receipt.request.header_value('authorization') == BASIC
        assert not receipt.request.header_value('x-simulator-token')
        assert not receipt.request.header_value('x-simulator-principal')
        assert receipt.json() == native
        rendered = page.locator('.response-section-content-body').filter(
            has=page.get_by_role('link', name='Download Response Body', exact=True))
        expect(rendered).to_have_count(1)
        expect(rendered).to_contain_text(NAME)
        assert server_requests == [('GET', target)] and not outside and not errors
        assert frame(instance.db) == before
    print('Sakura Scalar Send: authenticated local Server List, rendered saved row, cleanup passed')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    chromium = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    assert binary.is_file() and chromium.is_file()
    run(binary, chromium)
