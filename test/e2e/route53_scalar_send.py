"""Opt-in real Scalar TestDNSAnswer against saved local records; no SDK or DNS traffic."""
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile
from urllib.error import HTTPError
from urllib.parse import parse_qsl, urlsplit
from urllib.request import Request
from xml.etree import ElementTree as ET

from playwright.sync_api import expect, sync_playwright
from route53_openapi import validate_route53
from sakura_cli import CLI, frame

NS = 'https://route53.amazonaws.com/doc/2013-04-01/'
PREFIX = '/2013-04-01/'
ZONE = 'scalar.answer.example.test.'
NAME = 'saved.' + ZONE
ADDRESS = '192.0.2.53'


def native(cli, method, path, root=None, status=200):
    assert path.startswith(PREFIX) and not path.startswith('//')
    target = cli.origin + path
    assert urlsplit(target).netloc == urlsplit(cli.origin).netloc
    headers = {'X-Simulator-Token': cli.token}
    body = None
    if root is not None:
        headers['Content-Type'] = 'application/xml'
        body = ET.tostring(root, encoding='utf-8')
    try:
        response = cli.opener.open(Request(target, body, headers, method=method), timeout=5)
    except HTTPError as error:
        response = error
    with response:
        assert response.status == status, 'Native Route53 status differed; body withheld'
        data = response.read(131073)
        assert len(data) <= 131072
        return ET.fromstring(data)


def changes(action, address):
    root = ET.Element('ChangeResourceRecordSetsRequest', xmlns=NS)
    change = ET.SubElement(ET.SubElement(ET.SubElement(root, 'ChangeBatch'), 'Changes'), 'Change')
    ET.SubElement(change, 'Action').text = action
    record = ET.SubElement(change, 'ResourceRecordSet')
    for key, value in [('Name', NAME), ('Type', 'A'), ('TTL', '60')]:
        ET.SubElement(record, key).text = value
    ET.SubElement(ET.SubElement(ET.SubElement(record, 'ResourceRecords'), 'ResourceRecord'), 'Value').text = address
    return root


def seed(cli, address, owned):
    root = ET.Element('CreateHostedZoneRequest', xmlns=NS)
    ET.SubElement(root, 'Name').text = ZONE
    ET.SubElement(root, 'CallerReference').text = 'scalar-saved-answer'
    created = native(cli, 'POST', PREFIX + 'hostedzone', root, 201)
    identifier = created.findtext('.//{' + NS + '}HostedZone/{' + NS + '}Id').rsplit('/', 1)[-1]
    assert re.fullmatch('[A-Z0-9]+', identifier)
    record = {'cli': cli, 'id': identifier, 'address': address, 'saved': False}
    owned.append(record)
    native(cli, 'POST', PREFIX + 'hostedzone/' + identifier + '/rrset', changes('CREATE', address))
    record['saved'] = True
    records = native(cli, 'GET', PREFIX + 'hostedzone/' + identifier + '/rrset')
    saved = [row for row in records.findall('.//{' + NS + '}ResourceRecordSet')
             if row.findtext('{' + NS + '}Name') == NAME]
    assert len(saved) == 1 and saved[0].findtext('{' + NS + '}Type') == 'A'
    assert saved[0].findtext('.//{' + NS + '}Value') == address
    return identifier


def cleanup(owned):
    primary, failures = sys.exception(), []
    for record in reversed(owned):
        cli, identifier = record['cli'], record['id']
        try:
            path = PREFIX + 'hostedzone/' + identifier
            if record['saved']:
                native(cli, 'POST', path + '/rrset', changes('DELETE', record['address']))
            native(cli, 'DELETE', path)
            missing = native(cli, 'GET', path, status=404)
            assert missing.findtext('.//{' + NS + '}Code') == 'NoSuchHostedZone'
        except Exception as error:
            failures.append(type(error).__name__)
    if failures:
        if primary is not None:
            primary.add_note('Owned Route53 cleanup also failed: ' + ', '.join(failures))
        else:
            raise AssertionError('Owned Route53 cleanup failed: ' + ', '.join(failures))


def run():
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    expected = os.environ['SIMULATOR_BINARY_SHA256']
    assert re.fullmatch('[a-f0-9]{64}', expected)
    with binary.open('rb') as stream:
        assert hashlib.file_digest(stream, 'sha256').hexdigest() == expected
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    owned = []
    with tempfile.TemporaryDirectory(prefix='route53-scalar-') as directory, ExitStack() as processes:
        # Reuse only the existing SDK-free CLI lifecycle and read-only DB fingerprint.
        owner = CLI(str(binary), Path(directory) / 'owner', 'route53-owner', enabled=False)
        processes.callback(owner.close)
        peer = CLI(str(binary), Path(directory) / 'peer', 'route53-peer', enabled=False)
        processes.callback(peer.close)
        try:
            identifier = seed(owner, ADDRESS, owned)
            seed(peer, '192.0.2.54', owned)
            with sync_playwright() as playwright:
                with playwright.chromium.launch(executable_path=str(executable), chromium_sandbox=True) as browser:
                    with browser.new_context(service_workers='block') as context:
                        own = urlsplit(owner.origin)
                        outside, errors, requests = [], [], []

                        def guard(route):
                            url = urlsplit(route.request.url)
                            if (url.scheme, url.netloc) == (own.scheme, own.netloc):
                                route.continue_()
                            else:
                                outside.append(True)
                                route.abort()

                        context.route('**/*', guard)
                        assert context.request.post(owner.origin + '/_simulator/login',
                            form={'token': owner.token}, max_redirects=0).status == 303
                        served = context.request.get(owner.origin + '/_simulator/docs/openapi.json', max_redirects=0)
                        assert served.status == 200
                        spec = served.json()
                        validate_route53(spec)
                        matches = [(method.upper(), path, operation) for path, item in spec['paths'].items()
                                   for method, operation in item.items() if isinstance(operation, dict)
                                   and operation.get('operationId') == 'TestDNSAnswer']
                        assert len(matches) == 1
                        method, path, operation = matches[0]
                        assert method == 'GET' and path == PREFIX + 'testdnsanswer'
                        parameters = operation['parameters']
                        assert {(p['name'], p['in'], p['required']) for p in parameters} == {
                            (name, 'query', True) for name in ('hostedzoneid', 'recordname', 'recordtype')}
                        values = {'hostedzoneid': identifier, 'recordname': NAME, 'recordtype': 'A'}
                        page = context.new_page()
                        page.on('pageerror', lambda _: errors.append(True))
                        page.on('request', lambda request: requests.append(request)
                                if urlsplit(request.url).path.startswith(PREFIX) else None)
                        assert page.goto(owner.origin + '/_simulator/docs/').status == 200
                        page.get_by_role('link', name='Route53 public hosted zones', exact=True).click()
                        page.get_by_role('link').filter(has_text=re.compile('^' + re.escape(operation['summary'])
                            + r'\s*HTTP Method:\s*' + method + '$')).click()
                        page.get_by_role('button', name='Test Request (get ' + path + ')', exact=True).click()
                        dialog = page.get_by_role('dialog').filter(
                            has=page.get_by_role('button', name='Close Client', exact=True))
                        expect(dialog).to_have_count(1)
                        for parameter in parameters:
                            name = parameter['name']
                            include = dialog.get_by_role('checkbox', name='Include ' + name + ' in request', exact=True)
                            row = include.locator('xpath=ancestor::*[.//*[@role="combobox" and normalize-space(@aria-label)="Value"]][1]')
                            expect(row.get_by_role('checkbox')).to_have_count(1)
                            control = row.get_by_role('combobox', name='Value', exact=True)
                            expect(control).to_have_count(1)
                            include.check()
                            control.fill(values[name])
                            expect(control).to_have_text(values[name])
                        assert not requests, 'Native read occurred before Send'
                        snapshots = frame(owner.db), frame(peer.db)
                        with page.expect_response(lambda response: response.request.method == method
                                and urlsplit(response.url).path == path) as pending:
                            dialog.get_by_role('button', name=re.compile('^Send get request to ')).click()
                        response = pending.value
                        request = response.request
                        assert response.status == 200 and request.redirected_from is None
                        assert request.method == method and request.post_data is None
                        assert (urlsplit(request.url).scheme, urlsplit(request.url).netloc) == (own.scheme, own.netloc)
                        assert sorted(parse_qsl(urlsplit(request.url).query, keep_blank_values=True)) == sorted(values.items())
                        assert request.header_value('cookie')
                        for name in ('authorization', 'x-simulator-token', 'x-simulator-principal',
                                     'x-simulator-service', 'x-simulator-region', 'x-amz-target'):
                            assert not request.header_value(name), 'Unexpected Send authority or routing override'
                        xml = ET.fromstring(response.body())
                        assert xml.tag == '{' + NS + '}TestDNSAnswerResponse'
                        assert xml.findtext('{' + NS + '}ResponseCode') == 'NOERROR'
                        assert xml.findtext('{' + NS + '}RecordName') == NAME
                        assert xml.findtext('{' + NS + '}RecordType') == 'A'
                        assert [entry.text for entry in xml.findall('.//{' + NS + '}RecordDataEntry')] == [ADDRESS]
                        body = dialog.locator('.response-section-content-body').filter(
                            has=dialog.get_by_role('link', name='Download Response Body', exact=True))
                        expect(body).to_have_count(1)
                        expect(body).to_contain_text(ADDRESS)
                        assert len(requests) == 1 and not outside and not errors
                        assert (frame(owner.db), frame(peer.db)) == snapshots
        finally:
            cleanup(owned)
    print(json.dumps({'route53ScalarSend': True, 'binarySha256': expected, 'operation': 'TestDNSAnswer',
                      'method': 'GET', 'queryParameters': 3, 'sends': 1, 'nativeSnapshotsUnchanged': True,
                      'ownedCleanup': True, 'externalDNS': False}))


if __name__ == '__main__':
    run()
