"""Actual Scalar Send for three existing SimpleDB form examples.

Companion to simpledb_scalar_reference_live.py; its original three cases stay
unchanged. Supply the independently verified SIMULATOR_BINARY and
SIMULATOR_BINARY_SHA256 and the pinned Chromium through CHROMIUM_EXECUTABLE.
Only the shipped examples, ordinary browser controls and local cookies are used.
No substituted specification, replayed request, external cloud or Terraform.
"""
import hashlib
import json
import os
import re
from pathlib import Path
from xml.etree import ElementTree

from simpledb_scalar_reference_live import (
    FORM, NS, VERSION, checked_send, item_values, listed_domains, query_result,
)

CREATE = {'Action': 'CreateDomain', 'Version': VERSION, 'DomainName': 'scalar-practice-domain'}
SELECT = {'Action': 'Select', 'Version': VERSION,
          'SelectExpression': 'select itemName() from `example-domain` limit 25', 'ConsistentRead': 'true'}
DELETE = {'Action': 'DeleteAttributes', 'Version': VERSION, 'DomainName': 'example-domain',
          'ItemName': 'example-item', 'Attribute.1.Name': 'example-attribute',
          'Attribute.1.Value': 'replace-with-current-value', 'Expected.Name': 'example-attribute',
          'Expected.Value': 'replace-with-current-value'}
TARGET_VALUES = [('example-attribute', DELETE['Attribute.1.Value']), ('retained', 'committed + & 値')]
PEER_VALUES = [('retained', 'independent peer item')]


def mutation_xml(raw, action):
    root = ElementTree.fromstring(raw)
    assert root.tag == NS + action + 'Response'
    assert [row.tag for row in root] == [NS + 'ResponseMetadata']
    assert re.fullmatch('[a-f0-9]{32}', root.findtext(NS + 'ResponseMetadata/' + NS + 'RequestId') or '')


def run():
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    expected = os.environ['SIMULATOR_BINARY_SHA256']
    assert re.fullmatch('[a-f0-9]{64}', expected)
    with binary.open('rb') as data:
        assert hashlib.file_digest(data, 'sha256').hexdigest() == expected
    from check import BINARY, running, REPORTS
    from playwright.sync_api import sync_playwright, expect

    assert BINARY == binary
    with running(authenticated=True) as (endpoint, operator), sync_playwright() as p:
        native = p.request.new_context(extra_http_headers={
            'X-Simulator-Token': operator, 'X-Simulator-Service': 'sdb',
        })

        def query(action, **fields):
            response = native.post(endpoint + '/', form={'Action': action, 'Version': VERSION, **fields},
                                   max_redirects=0, timeout=10000)
            assert response.status == 200
            return response.body()

        def values(item):
            return item_values(query('GetAttributes', DomainName=DELETE['DomainName'],
                                     ItemName=item, ConsistentRead='true'))

        def seed(item, saved):
            fields = {f'Attribute.{i}.{key}': value for i, (name, text) in enumerate(saved, 1)
                      for key, value in [('Name', name), ('Value', text)]}
            query('PutAttributes', DomainName=DELETE['DomainName'], ItemName=item, **fields)

        try:
            assert listed_domains(query('ListDomains')) == []
            try:
                with p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or None,
                                       chromium_sandbox=True) as browser:
                    with browser.new_context(viewport={'width': 1440, 'height': 1000},
                                             service_workers='block') as context:
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
                        with page.expect_response(lambda r: r.url == endpoint + '/_simulator/docs/openapi.json') as served:
                            page.goto(endpoint + '/_simulator/docs/')
                        spec = served.value.json()
                        root = spec['paths']['/']['post']
                        presets = root['requestBody']['content'][FORM]['examples']
                        for fields in (CREATE, SELECT, DELETE):
                            assert presets[fields['Action']]['value'] == fields
                        page.get_by_role('link', name='Shared AWS JSON/Query endpoint', exact=True).click()
                        page.get_by_role('button', name='Selected Content Type: application/x-amz-json-1.1', exact=True).click()
                        page.get_by_role('option', name=FORM, exact=True).click()
                        picker = page.get_by_test_id('example-picker')
                        client = page.get_by_role('dialog').filter(
                            has=page.get_by_role('button', name='Close Client', exact=True))
                        request_panel = client.get_by_role('region', name='Request: ' + root['summary'], exact=True)

                        def send(fields):
                            count = len(sends)
                            picker.click()
                            page.get_by_role('option', name=presets[fields['Action']]['summary'], exact=True).click()
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
                            for field in fields:
                                request_panel.get_by_role('checkbox', name=f'Include {field} in request', exact=True).check()
                            request_panel.get_by_role('tab', name='Headers', exact=True).press('Enter')
                            service = request_panel.get_by_role('row', name=re.compile(r'^Include X-Simulator-Service in request '))
                            service.get_by_role('button', name=re.compile(r'^(ssm|sdb)$')).click()
                            page.get_by_role('menuitem', name='sdb', exact=True).click()
                            client.get_by_role('checkbox', name='Include X-Simulator-Service in request', exact=True).check()
                            for name in ('X-Amz-Target', 'X-Simulator-Region'):
                                checkbox = client.get_by_role('checkbox', name=f'Include {name} in request', exact=True)
                                if checkbox.is_checked():
                                    checkbox.uncheck()
                            assert len(sends) == count, 'example editing emitted a request before Send'
                            with page.expect_response(lambda r: r.request.method == 'POST'
                                                      and r.url == endpoint + '/') as captured:
                                client.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
                            response = captured.value
                            checked_send(endpoint, sends[count:], response, fields)
                            assert response.headers['content-type'].split(';', 1)[0] == 'text/xml'
                            return response

                        def panel_text(text):
                            panel = client.locator('.response-section-content-body').filter(
                                has=page.get_by_role('link', name='Download Response Body', exact=True))
                            expect(panel).to_have_count(1)
                            expect(panel).to_contain_text(text)

                        # Actual browser CreateDomain, followed by independent native readback.
                        created = send(CREATE)
                        mutation_xml(created.body(), 'CreateDomain')
                        panel_text('CreateDomainResponse')
                        assert listed_domains(query('ListDomains')) == [CREATE['DomainName']]
                        checked_send(endpoint, sends, created, CREATE)
                        client.get_by_role('button', name='Close Client', exact=True).click()

                        # Seed the exact domain named by the already shipped Select/Delete examples.
                        query('CreateDomain', DomainName=DELETE['DomainName'])
                        seed(DELETE['ItemName'], TARGET_VALUES)
                        seed('peer-item', PEER_VALUES)
                        assert values(DELETE['ItemName']) == TARGET_VALUES
                        assert values('peer-item') == PEER_VALUES
                        selected = send(SELECT)
                        result = query_result(selected.body(), 'Select')
                        items = result.findall(NS + 'Item')
                        assert sorted(row.findtext(NS + 'Name') for row in items) == ['example-item', 'peer-item']
                        assert all([child.tag for child in row] == [NS + 'Name'] for row in items)
                        assert result.find(NS + 'NextToken') is None
                        panel_text('SelectResult')
                        panel_text('example-item')
                        panel_text('peer-item')
                        assert values(DELETE['ItemName']) == TARGET_VALUES
                        assert values('peer-item') == PEER_VALUES
                        checked_send(endpoint, sends[1:], selected, SELECT)
                        client.get_by_role('button', name='Close Client', exact=True).click()

                        deleted = send(DELETE)
                        mutation_xml(deleted.body(), 'DeleteAttributes')
                        panel_text('DeleteAttributesResponse')
                        assert values(DELETE['ItemName']) == [TARGET_VALUES[1]]
                        assert values('peer-item') == PEER_VALUES
                        assert listed_domains(query('ListDomains')) == sorted([CREATE['DomainName'], DELETE['DomainName']])
                        checked_send(endpoint, sends[2:], deleted, DELETE)
                        assert len(sends) == 3 and not outside and not errors
            finally:
                # The subprocess is private: only these two shipped fixture domains can exist.
                # Run even after an uncertain browser mutation or assertion failure.
                cleanup_errors = []
                for domain in (CREATE['DomainName'], DELETE['DomainName']):
                    try:
                        query('DeleteDomain', DomainName=domain)
                    except Exception:
                        cleanup_errors.append(domain)
                assert listed_domains(query('ListDomains')) == []
                assert not cleanup_errors, 'native domain cleanup failed'
        finally:
            native.dispose()
    (REPORTS / 'simpledb-shipped-examples-live.json').write_text(json.dumps({
        'binary_sha256': expected, 'servedPresets': ['CreateDomain', 'Select', 'DeleteAttributes'],
        'nativeStatuses': {'CreateDomain': 200, 'Select': 200, 'DeleteAttributes': 200},
        'oneCookieSendPerPreset': True, 'exactForm': True, 'liveXmlAndRenderedResult': True,
        'nativeReadback': True, 'targetedDeletePreservesPeerValue': True, 'freshDomainsDeleted': True,
    }, indent=2) + '\n')


if __name__ == '__main__':
    run()
