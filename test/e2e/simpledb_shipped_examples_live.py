"""Actual Scalar Send for five shipped SimpleDB form examples.

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
from tempfile import TemporaryDirectory
from xml.etree import ElementTree

from simpledb_scalar_reference_live import (
    NS, VERSION, ScalarQueryReference, checked_send, item_values, listed_domains, query_result,
)

CREATE = {'Action': 'CreateDomain', 'Version': VERSION, 'DomainName': 'scalar-practice-domain'}
SELECT = {'Action': 'Select', 'Version': VERSION,
          'SelectExpression': 'select itemName() from `example-domain` limit 25', 'ConsistentRead': 'true'}
DELETE = {'Action': 'DeleteAttributes', 'Version': VERSION, 'DomainName': 'example-domain',
          'ItemName': 'example-item', 'Attribute.1.Name': 'example-attribute',
          'Attribute.1.Value': 'replace-with-current-value', 'Expected.Name': 'example-attribute',
          'Expected.Value': 'replace-with-current-value'}
PUT = {'Action': 'PutAttributes', 'Version': VERSION, 'DomainName': 'scalar-practice-domain',
       'ItemName': 'scalar-practice-item', 'Attribute.1.Name': 'practice-attribute',
       'Attribute.1.Value': 'sample-blue', 'Attribute.1.Replace': 'true'}
DELETE_DOMAIN = {'Action': 'DeleteDomain', 'Version': VERSION, 'DomainName': 'scalar-practice-domain'}
PRESETS = (CREATE, SELECT, DELETE, PUT, DELETE_DOMAIN)
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
    observed_statuses = {}
    with (TemporaryDirectory() as directory,
          running(db=Path(directory) / 'state.sqlite', authenticated=True) as (endpoint, operator),
          sync_playwright() as p):
        native = p.request.new_context(extra_http_headers={
            'X-Simulator-Token': operator, 'X-Simulator-Service': 'sdb',
        })

        def query(action, expected_status=200, **fields):
            response = native.post(endpoint + '/', form={'Action': action, 'Version': VERSION, **fields},
                                   max_redirects=0, timeout=10000)
            assert response.status == expected_status
            return response.body()

        def values(item, domain=DELETE['DomainName']):
            return item_values(query('GetAttributes', DomainName=domain,
                                     ItemName=item, ConsistentRead='true'))

        def seed(item, saved, domain=DELETE['DomainName']):
            fields = {f'Attribute.{i}.{key}': value for i, (name, text) in enumerate(saved, 1)
                      for key, value in [('Name', name), ('Value', text)]}
            query('PutAttributes', DomainName=domain, ItemName=item, **fields)

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
                        reference = ScalarQueryReference(page, endpoint, {
                            fields['Action']: fields for fields in PRESETS
                        })

                        def send(fields):
                            count = len(sends)
                            reference.open_example(fields['Action'])
                            assert len(sends) == count, 'example editing emitted a request before Send'
                            response = reference.send()
                            checked_send(endpoint, sends[count:], response, fields)
                            assert response.headers['content-type'].split(';', 1)[0] == 'text/xml'
                            observed_statuses[fields['Action']] = response.status
                            return response

                        def panel_text(text):
                            panel = reference.response_panel
                            expect(panel).to_have_count(1)
                            expect(panel).to_contain_text(text)

                        # Actual browser CreateDomain, followed by independent native readback.
                        created = send(CREATE)
                        mutation_xml(created.body(), 'CreateDomain')
                        panel_text('CreateDomainResponse')
                        assert listed_domains(query('ListDomains')) == [CREATE['DomainName']]
                        checked_send(endpoint, sends, created, CREATE)
                        reference.close_client()

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
                        reference.close_client()

                        deleted = send(DELETE)
                        mutation_xml(deleted.body(), 'DeleteAttributes')
                        panel_text('DeleteAttributesResponse')
                        assert values(DELETE['ItemName']) == [TARGET_VALUES[1]]
                        assert values('peer-item') == PEER_VALUES
                        assert listed_domains(query('ListDomains')) == sorted([CREATE['DomainName'], DELETE['DomainName']])
                        checked_send(endpoint, sends[2:], deleted, DELETE)
                        assert len(sends) == 3 and not outside and not errors
                        reference.close_client()

                        practice_domain = PUT['DomainName']
                        practice_item = PUT['ItemName']
                        attribute = PUT['Attribute.1.Name']
                        retained = ('retained', 'keep this practice value')
                        before = [(attribute, 'old A'), (attribute, 'old B'), retained]
                        seed(practice_item, before, practice_domain)
                        seed('peer-item', PEER_VALUES, practice_domain)
                        assert values(practice_item, practice_domain) == before
                        assert values('peer-item', practice_domain) == PEER_VALUES

                        written = send(PUT)
                        mutation_xml(written.body(), 'PutAttributes')
                        panel_text('PutAttributesResponse')
                        expected_values = [(attribute, PUT['Attribute.1.Value']), retained]
                        assert values(practice_item, practice_domain) == expected_values
                        assert values('peer-item', practice_domain) == PEER_VALUES
                        assert values(DELETE['ItemName']) == [TARGET_VALUES[1]]
                        assert values('peer-item') == PEER_VALUES
                        checked_send(endpoint, sends[3:], written, PUT)
                        reference.close_client()

                        removed = send(DELETE_DOMAIN)
                        mutation_xml(removed.body(), 'DeleteDomain')
                        panel_text('DeleteDomainResponse')
                        assert listed_domains(query('ListDomains')) == [DELETE['DomainName']]
                        missing = query('GetAttributes', expected_status=400, DomainName=practice_domain,
                                        ItemName=practice_item, ConsistentRead='true')
                        missing_code = ElementTree.fromstring(missing).findtext(NS + 'Errors/' + NS + 'Error/' + NS + 'Code')
                        assert missing_code == 'NoSuchDomain'
                        assert values(DELETE['ItemName']) == [TARGET_VALUES[1]]
                        assert values('peer-item') == PEER_VALUES
                        checked_send(endpoint, sends[4:], removed, DELETE_DOMAIN)
                        assert len(sends) == len(PRESETS) and not outside and not errors
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
        'binary_sha256': expected, 'servedPresets': list(observed_statuses),
        'nativeStatuses': observed_statuses,
        'oneCookieSendPerPreset': True, 'exactForm': True, 'liveXmlAndRenderedResult': True,
        'nativeReadback': True, 'targetedDeletePreservesPeerValue': True, 'freshDomainsDeleted': True,
        'putReplacesAllOldValuesAndPreservesPeers': True, 'deleteDomainPreservesOtherDomainAndItems': True,
    }, indent=2) + '\n')


if __name__ == '__main__':
    run()
