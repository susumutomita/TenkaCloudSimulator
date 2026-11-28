"""Opt-in real-browser Cloud DNS List acceptance; no mock, SDK, or external cloud."""
import os
import re
from pathlib import Path
from urllib.parse import quote, urlsplit

from check import running
from playwright.sync_api import expect, sync_playwright

PROVIDER = '/_simulator/providers/gcp'
REFERENCE = '/_simulator/docs/gcp-dns.html'
ZONE = 'scalar-list-receipt'
DNS_NAME = 'scalar-list-receipt.invalid.'


def run():
    # No default executable lookup, installation or browser download in this test.
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    assert executable.is_file()
    with running(authenticated=True) as (endpoint, token), sync_playwright() as playwright:
        own = urlsplit(endpoint)
        assert own.scheme == 'http' and own.hostname == '127.0.0.1' and own.port
        browser = playwright.chromium.launch(executable_path=str(executable), chromium_sandbox=True)
        try:
            context = browser.new_context(service_workers='block')
            context.set_default_timeout(15_000)
            outside, errors = [], []

            def guard(route):
                u = urlsplit(route.request.url)
                if (u.scheme, u.netloc) != (own.scheme, own.netloc):
                    outside.append(True)
                    route.abort()
                else:
                    route.continue_()
            context.route('**/*', guard)
            assert context.request.post(endpoint+'/_simulator/login', form={'token': token},
                                        max_redirects=0).status == 303
            caps = context.request.get(endpoint+'/_simulator/v1/capabilities', max_redirects=0)
            assert caps.status == 200
            gcp = caps.json()['cloudStorage']['gcp']
            project = gcp['project']
            assert gcp['endpoint'] == PROVIDER and re.fullmatch(r'[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?', project)
            collection = f'{PROVIDER}/dns/v1/projects/{project}/managedZones'
            seed = context.request.post(endpoint+collection, data={
                'name': ZONE, 'dnsName': DNS_NAME, 'description': 'synthetic Scalar fixture'},
                max_redirects=0)
            assert seed.status in (200, 201), 'native fixture create failed'
            try:
                assert seed.json()['name'] == ZONE
                page = context.new_page()
                page.on('pageerror', lambda _: errors.append(True))
                list_requests = []
                page.on('request', lambda req: list_requests.append(req)
                        if req.method == 'GET' and urlsplit(req.url).path == collection else None)
                assert page.goto(endpoint+REFERENCE).status == 200
                expect(page.get_by_text('List managed zones', exact=True).first).to_be_visible()
                page.get_by_role('button', name='Test Request (get /projects/{project}/managedZones)',
                                 exact=True).click()

                def expected(response):
                    u = urlsplit(response.url)
                    return (u.scheme, u.netloc, u.path, response.request.method) == (
                        own.scheme, own.netloc, collection, 'GET')

                assert not list_requests, 'opening the reference sent a list request'
                with page.expect_response(expected) as receipt:
                    page.get_by_role('button', name=re.compile(r'^Send get request to ')).click()
                received = receipt.value
                assert received.status == 200
                assert received.request.redirected_from is None
                assert received.request.header_value('cookie'), 'the browser session cookie was missing'
                assert not received.request.header_value('authorization')
                assert not received.request.header_value('x-simulator-token')
                zones = received.json()['managedZones']
                assert len(zones) == 1 and (zones[0]['name'], zones[0]['dnsName']) == (ZONE, DNS_NAME)
                body = page.locator('.response-section-content-body').filter(
                    has=page.get_by_role('link', name='Download Response Body', exact=True))
                expect(body).to_have_count(1)
                expect(body).to_contain_text(ZONE)
                expect(body).to_contain_text(DNS_NAME)
                assert len(list_requests) == 1, 'a single Send must issue one zone list request'
                assert not outside and not errors, 'unexpected browser navigation or page error'
            finally:
                item = endpoint+collection+'/'+quote(ZONE, safe='')
                assert context.request.delete(item, max_redirects=0).status in (200, 204)
                clean = context.request.get(endpoint+collection, max_redirects=0)
                assert clean.status == 200 and clean.json()['managedZones'] == []
        finally:
            browser.close()
    print('Cloud DNS Scalar Send: real same-origin List response and cleanup passed')


if __name__ == '__main__':
    run()
