"""Opt-in served Scalar ListDomains, DomainMetadata, and GetAttributes Send.

Run from exact checkout with Playwright's pinned Chromium:
SIMULATOR_BINARY=/path/to/binary SIMULATOR_BINARY_SHA256=<verified> \
  python test/e2e/simpledb_scalar_reference_live.py
No mocked HTTP response, substituted spec, custom fetch or live cloud. The
expected digest must come from independently approved exact-source binary provenance.
"""
import hashlib
import json
import os
import re
from pathlib import Path
from urllib.parse import parse_qsl
from xml.etree import ElementTree

FORM = 'application/x-www-form-urlencoded'
VERSION = '2009-04-15'
EXAMPLE_DOMAIN = 'scalar-practice-domain'
LIST_FIELDS = {'Action': 'ListDomains', 'Version': VERSION}
METADATA_FIELDS = {'Action': 'DomainMetadata', 'Version': VERSION, 'DomainName': EXAMPLE_DOMAIN}
EXAMPLE_ITEM = 'scalar-practice-item'
GET_FIELDS = {'Action': 'GetAttributes', 'Version': VERSION, 'DomainName': EXAMPLE_DOMAIN,
              'ItemName': EXAMPLE_ITEM, 'ConsistentRead': 'true'}
SAVED_VALUES = [('display-state', 'saved + & 値'), ('empty', '')]
STATS = ('ItemCount', 'ItemNamesSizeBytes', 'AttributeNameCount',
         'AttributeNamesSizeBytes', 'AttributeValueCount', 'AttributeValuesSizeBytes')
NS = '{http://sdb.amazonaws.com/doc/2009-04-15/}'
FORBIDDEN_SEND_HEADERS = (
    'authorization', 'x-simulator-principal', 'x-simulator-token', 'x-amz-target',
)


def checked_wire(headers, payload, fields):
    assert headers['content-type'].split(';', 1)[0].strip().lower() == FORM
    assert headers.get('x-simulator-service') == 'sdb'
    assert headers.get('x-simulator-region', 'us-east-1') == 'us-east-1'
    assert set(headers).isdisjoint(FORBIDDEN_SEND_HEADERS), 'unexpected Send header'
    assert headers.get('cookie'), 'expected browser cookie'
    pairs = parse_qsl(payload, strict_parsing=True, keep_blank_values=True)
    assert len(pairs) == len(fields) and dict(pairs) == fields


def checked_send(endpoint, requests, response, fields):
    assert len(requests) == 1, 'expected exactly one native POST'
    request = requests[0]
    assert response.request is request, 'response did not match captured Send request'
    assert request.method == 'POST' and request.url == response.url == endpoint + '/'
    assert request.redirected_from is None and request.redirected_to is None, 'unexpected redirect'
    assert response.status == 200
    checked_wire(request.all_headers(), request.post_data, fields)


def query_result(body, action):
    root = ElementTree.fromstring(body)
    assert root.tag == NS + action + 'Response'
    results = root.findall(NS + action + 'Result')
    assert len(results) == 1
    request_id = root.findtext(NS + 'ResponseMetadata/' + NS + 'RequestId')
    assert request_id and re.fullmatch('[a-f0-9]{32}', request_id)
    return results[0]


def listed_domains(body):
    return [row.text for row in query_result(body, 'ListDomains').findall(NS + 'DomainName')]


def empty_metadata_timestamp(body):
    result = query_result(body, 'DomainMetadata')
    assert sorted(child.tag for child in result) == sorted(NS + name for name in (*STATS, 'Timestamp'))
    assert all(result.findtext(NS + name) == '0' for name in STATS)
    timestamp = result.findtext(NS + 'Timestamp')
    assert timestamp and timestamp.isascii() and timestamp.isdecimal() and int(timestamp) > 0
    return timestamp


def item_values(body):
    result = query_result(body, 'GetAttributes')
    return sorted((row.findtext(NS + 'Name'), row.findtext(NS + 'Value'))
                  for row in result.findall(NS + 'Attribute'))


def run():
    # Require a separately verified executable, not whatever dist happens to hold.
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    expected = os.environ['SIMULATOR_BINARY_SHA256']
    assert re.fullmatch('[a-f0-9]{64}', expected)
    with binary.open('rb') as data:
        assert hashlib.file_digest(data, 'sha256').hexdigest() == expected
    from check import BINARY, running, REPORTS
    from playwright.sync_api import sync_playwright, expect

    assert BINARY == binary
    with running(authenticated=True) as (endpoint, operator), sync_playwright() as p:
        # Native seed/cleanup calls are separate from the browser request capture.
        native = p.request.new_context(extra_http_headers={
            'X-Simulator-Token': operator, 'X-Simulator-Service': 'sdb',
        })
        domain = EXAMPLE_DOMAIN  # Private new process and empty ephemeral store below.
        def query(action, **fields):
            return native.post(endpoint + '/', form={'Action': action, 'Version': VERSION, **fields},
                               max_redirects=0, timeout=10000)
        try:
            before = query('ListDomains')
            assert before.status == 200 and listed_domains(before.body()) == []
            create = query('CreateDomain', DomainName=domain)
            assert create.status == 200
            try:
                assert listed_domains(query('ListDomains').body()) == [domain]
                browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or None)
                try:
                    context = browser.new_context(viewport={'width': 1440, 'height': 1000},
                                                  service_workers='block')
                    try:
                        assert context.request.post(endpoint + '/_simulator/login', form={'token': operator},
                                                    max_redirects=0).status == 303
                        errors, outside, sends = [], [], []
                        def scope(route):
                            if route.request.url.startswith(endpoint + '/'):
                                route.continue_()
                            else:
                                outside.append(True)
                                route.abort()
                        context.route('**/*', scope)
                        page = context.new_page()
                        page.on('pageerror', lambda _: errors.append(True))
                        page.on('request', lambda q: sends.append(q) if q.method == 'POST'
                                and q.url.split('?', 1)[0] == endpoint + '/' else None)
                        with page.expect_response(lambda response: response.url == endpoint + '/_simulator/docs/openapi.json') as served:
                            page.goto(endpoint + '/_simulator/docs/')
                        spec = served.value.json()
                        root = spec['paths']['/']['post']
                        presets = root['requestBody']['content'][FORM]['examples']
                        assert presets['ListDomains']['value'] == LIST_FIELDS
                        assert presets['DomainMetadata']['value'] == METADATA_FIELDS
                        assert presets['GetAttributes']['value'] == GET_FIELDS
                        page.get_by_role('link', name='Shared AWS JSON/Query endpoint', exact=True).click()
                        page.get_by_role('button', name='Selected Content Type: application/x-amz-json-1.1', exact=True).click()
                        page.get_by_role('option', name=FORM, exact=True).click()
                        picker = page.get_by_test_id('example-picker')
                        client = page.get_by_role('dialog').filter(
                            has=page.get_by_role('button', name='Close Client', exact=True))
                        request_panel = client.get_by_role('region', name='Request: ' + root['summary'], exact=True)

                        def open_example(action):
                            # Choose the named example on the reference card before opening the client.
                            picker.click()
                            page.get_by_role('option', name=presets[action]['summary'], exact=True).click()
                            page.get_by_role('button', name='Test Request (post /)', exact=True).click()
                            expect(client).to_have_count(1)
                            request_panel.get_by_role('tab', name='Body', exact=True).press('Enter')
                            form = request_panel.get_by_role('button', name='Form URL Encoded', exact=True)
                            json_body = request_panel.get_by_role('button', name='application/x-amz-json-1.1', exact=True)
                            expect(form.or_(json_body)).to_be_visible()
                            if not form.is_visible():
                                json_body.click()
                                page.get_by_role('option', name='Form URL Encoded', exact=True).click()
                            expect(form).to_be_visible()
                            # Scalar renders the example values with optional fields unchecked.
                            for field in presets[action]['value']:
                                request_panel.get_by_role('checkbox', name=f'Include {field} in request', exact=True).check()
                            request_panel.get_by_role('tab', name='Headers', exact=True).press('Enter')
                            service_row = request_panel.get_by_role('row', name=re.compile(r'^Include X-Simulator-Service in request '))
                            service_row.get_by_role('button', name=re.compile(r'^(ssm|sdb)$')).click()
                            page.get_by_role('menuitem', name='sdb', exact=True).click()
                            include = client.get_by_role('checkbox', name='Include X-Simulator-Service in request', exact=True)
                            expect(include).to_have_count(1)
                            if not include.is_checked():
                                include.check()
                            target = client.get_by_role('checkbox', name='Include X-Amz-Target in request', exact=True)
                            expect(target).to_have_count(1)
                            if target.is_checked():
                                target.uncheck()
                            region = client.get_by_role('checkbox', name='Include X-Simulator-Region in request', exact=True)
                            expect(region).to_have_count(1)
                            if region.is_checked():
                                region.uncheck()

                        open_example('ListDomains')
                        assert not sends, 'editing the preset sent a request before Send'
                        with page.expect_response(lambda response: response.request.method == 'POST'
                                                  and response.url == endpoint + '/') as capture:
                            client.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
                        response = capture.value
                        checked_send(endpoint, sends, response, LIST_FIELDS)
                        assert response.headers['content-type'].split(';', 1)[0] == 'text/xml'
                        assert listed_domains(response.body()) == [domain]
                        panel = client.locator('.response-section-content-body').filter(
                            has=page.get_by_role('link', name='Download Response Body', exact=True))
                        expect(panel).to_have_count(1)
                        expect(panel).to_contain_text(domain)
                        # Recheck after render to catch any additional root POST or redirect.
                        checked_send(endpoint, sends, response, LIST_FIELDS)
                        # Select the second served example for the same test domain.
                        client.get_by_role('button', name='Close Client', exact=True).click()
                        open_example('DomainMetadata')
                        assert len(sends) == 1, 'the metadata preset sent a request before Send'
                        with page.expect_response(lambda response: response.request.method == 'POST'
                                                  and response.url == endpoint + '/') as capture:
                            client.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
                        metadata = capture.value
                        assert len(sends) == 2
                        checked_send(endpoint, sends[1:], metadata, METADATA_FIELDS)
                        assert metadata.headers['content-type'].split(';', 1)[0] == 'text/xml'
                        timestamp = empty_metadata_timestamp(metadata.body())
                        expect(panel).to_have_count(1)
                        expect(panel).to_contain_text('DomainMetadataResult')
                        expect(panel).to_contain_text('ItemCount')
                        expect(panel).to_contain_text(timestamp)
                        assert len(sends) == 2
                        checked_send(endpoint, sends[:1], response, LIST_FIELDS)
                        checked_send(endpoint, sends[1:], metadata, METADATA_FIELDS)

                        # Seed through the native Query API, then let Scalar Send
                        # read exactly that item without any browser header bypass.
                        fixture = {f'Attribute.{i}.{k}': v for i, (name, value) in enumerate(SAVED_VALUES, 1)
                                   for k, v in [('Name', name), ('Value', value)]}
                        created = query('PutAttributes', DomainName=domain, ItemName=EXAMPLE_ITEM, **fixture)
                        assert created.status == 200
                        assert ElementTree.fromstring(created.body()).tag == NS + 'PutAttributesResponse'
                        client.get_by_role('button', name='Close Client', exact=True).click()
                        open_example('GetAttributes')
                        assert len(sends) == 2, 'the GetAttributes preset sent a request before Send'
                        with page.expect_response(lambda response: response.request.method == 'POST'
                                                  and response.url == endpoint + '/') as capture:
                            client.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
                        values = capture.value
                        checked_send(endpoint, sends[2:], values, GET_FIELDS)
                        assert values.headers['content-type'].split(';', 1)[0] == 'text/xml'
                        assert item_values(values.body()) == SAVED_VALUES
                        expect(panel).to_contain_text('GetAttributesResult')
                        expect(panel).to_contain_text('display-state')
                        checked_send(endpoint, sends[2:], values, GET_FIELDS)
                        assert not outside and not errors
                    finally:
                        context.close()
                finally:
                    browser.close()
            finally:
                deleted = query('DeleteDomain', DomainName=domain)
                assert deleted.status == 200
                after = query('ListDomains')
                assert after.status == 200 and listed_domains(after.body()) == []
        finally:
            native.dispose()
    # Emit success only after real response, domain removal and standalone shutdown.
    (REPORTS / 'simpledb-scalar-reference-live.json').write_text(json.dumps({
        'binary_sha256': expected, 'servedPresets': ['ListDomains', 'DomainMetadata', 'GetAttributes'],
        'nativeStatuses': {'ListDomains': 200, 'DomainMetadata': 200, 'GetAttributes': 200},
        'getAttributesMatchesNativeWriteIncludingEmptyValue': True,
        'exactForm': True, 'canonicalService': 'sdb', 'oneMatchedSendPerPresetNoRedirect': True,
        'requestCookiePresent': True, 'forbiddenSendHeadersAbsent': list(FORBIDDEN_SEND_HEADERS),
        'liveXml': True, 'freshDomainDeleted': True,
    }, indent=2) + '\n')


if __name__ == '__main__':
    run()
