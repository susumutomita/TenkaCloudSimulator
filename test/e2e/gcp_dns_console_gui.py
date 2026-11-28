"""Opt-in, real-browser Cloud DNS console acceptance, with a verified local binary.

SIMULATOR_BINARY=/absolute/build/path SIMULATOR_BINARY_SHA256=<trusted digest>
CHROMIUM_EXECUTABLE=/absolute/chromium python test/e2e/gcp_dns_console_gui.py

Use an independently isolated Linux runner with no network except loopback
(e.g. an approved network-none container), no host network/socket mounts,
and working Chromium sandbox support. Provision pinned tools beforehand.
Request observation and the /proc diagnostic do not configure a firewall
or prove runner provenance. No cloud, DNS serving, fake responses or fixtures
seeded through direct storage writes. This tests the GUI, not Scalar Send.
"""
import hashlib
import json
import os
from pathlib import Path
import re
from secrets import token_hex
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright
from sqs import BINARY, Team

PROJECT = 'tenkacloud-local'
ALTERNATE = 'dns-gui-nonowner'
CAPS = '/_simulator/v1/capabilities'
COLLECTION = '/_simulator/providers/gcp/dns/v1/projects/' + PROJECT + '/managedZones'
OPS = {'managedZones.list', 'managedZones.create', 'managedZones.get', 'resourceRecordSets.list', 'changes.create'}


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


def verify_capability(document):
    cap = document.get('gcpDns') if isinstance(document, dict) else None
    require(isinstance(cap, dict), 'Invalid Cloud DNS capability shape')
    require(document.get('principal') == '' and document.get('identitySelection') is True
            and cap.get('console') is True
            and cap.get('api') == 'dns.v1' and cap.get('project') == PROJECT
            and cap.get('endpoint') == COLLECTION and len(cap.get('operations', [])) == len(OPS)
            and set(cap.get('operations', [])) == OPS,
            'Expected the exact default-owner local Cloud DNS capability')


def verify_zone(zone, expected):
    if not isinstance(zone, dict):
        return False
    return (all(zone.get(key) == value for key, value in expected.items())
            and isinstance(zone.get('id'), str) and bool(re.fullmatch('[1-9][0-9]{0,18}', zone['id'])))


def run_gui(team, chromium):
    origin = team.endpoint
    expected = {'name': 'gui-owned-' + token_hex(6),
                'description': 'Synthetic local GUI fixture', 'visibility': 'public'}
    expected['dnsName'] = expected['name'] + '.invalid.'
    zone_id = None
    eligible_for_cleanup = False
    browser_errors, malformed_origin, native_redirects = [], [], []
    native_reads = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=chromium, chromium_sandbox=True,
                                     args=['--no-proxy-server'])
        try:
            context = browser.new_context(service_workers='block', locale='en-US')
            context.set_default_timeout(30000)
            expect.set_options(timeout=30000)
            page = context.new_page()
            page.on('pageerror', lambda _: browser_errors.append(True))

            def path(request):
                if not exact_origin(request.url, origin):
                    return ''
                return urlsplit(request.url).path

            def native(request):
                p = path(request)
                return p == COLLECTION or p.startswith(COLLECTION + '/')

            def audit_request(request):
                if not redirect_chain_owned(request, origin):
                    malformed_origin.append(True)
                previous = request.redirected_from
                if previous is not None and (native(request) or native(previous)):
                    native_redirects.append(True)

            def capture(response):
                if native(response.request):
                    if 300 <= response.status < 400:
                        native_redirects.append(True)
                    if response.request.method == 'GET':
                        native_reads.append((path(response.request), response.status))

            context.on('request', audit_request)
            page.on('response', capture)

            def native_api(method, target, **kwargs):
                require(target in (CAPS, COLLECTION)
                        or re.fullmatch(re.escape(COLLECTION) + r'/[1-9][0-9]{0,18}', target),
                        'Unexpected fixture API target')
                result = context.request.fetch(origin + target, method=method, max_redirects=0, **kwargs)
                require(exact_origin(result.url, origin) and not 300 <= result.status < 400,
                        'Unexpected independent read or cleanup redirect')
                return result

            def listed():
                response = native_api('GET', COLLECTION)
                require(response.status == 200, 'Local DNS list request failed')
                document = response.json()
                require(isinstance(document.get('managedZones'), list) and not document.get('nextPageToken'),
                        'Unexpected local DNS list shape')
                return document['managedZones']

            try:
                page.goto(origin + '/_simulator/console/')
                page.get_by_label('Access token', exact=True).fill(team.token)
                with page.expect_response(lambda r: path(r.request) == CAPS and r.request.method == 'GET') as owner:
                    page.get_by_role('button', name='Open sandbox', exact=True).click()
                require(owner.value.status == 200, 'Browser session did not bootstrap')
                verify_capability(owner.value.json())
                other = native_api('GET', CAPS, headers={'X-Simulator-Principal': ALTERNATE})
                require(other.status == 200 and other.json().get('principal') == ALTERNATE
                        and 'gcpDns' not in other.json(), 'Alternate identity saw owner-only console capability')
                with page.expect_response(lambda r: path(r.request) == COLLECTION and r.request.method == 'GET'):
                    page.goto(origin + '/_simulator/console/#/gcp-dns')
                expect(page.get_by_role('heading', name='Cloud DNS', exact=True)).to_be_visible()
                expect(page.get_by_text('No zones', exact=True)).to_be_visible()
                require(listed() == [], 'Owned simulator must start with an empty DNS project')
                eligible_for_cleanup = True
                page.get_by_role('button', name='Create zone', exact=True).click()
                expect(page.get_by_role('heading', name='Create a DNS zone', exact=True)).to_be_visible()
                expect(page.get_by_label('Zone type', exact=True)).to_have_value('public')
                page.get_by_label('Zone name', exact=True).fill(expected['name'])
                page.get_by_label('DNS name', exact=True).fill(expected['dnsName'])
                page.get_by_label('Description', exact=True).fill(expected['description'])
                with page.expect_response(lambda r: path(r.request) == COLLECTION
                                          and r.request.method == 'POST') as creation:
                    page.get_by_role('button', name='Create', exact=True).click()
                made = creation.value
                require(made.status in (200, 201) and made.request.post_data_json == expected
                        and verify_zone(made.json(), expected), 'Native GUI creation did not match fixture')
                zone_id = made.json()['id']
                item = COLLECTION + '/' + zone_id

                def details():
                    expect(page.get_by_role('heading', name='Zone details: ' + expected['name'],
                                            exact=True)).to_be_visible()
                    expect(page.get_by_text(expected['dnsName'], exact=True).first).to_be_visible()
                    expect(page.get_by_text(expected['description'], exact=True)).to_be_visible()
                    expect(page.get_by_role('heading', name='Record sets', exact=True)).to_be_visible()
                    expect(page.get_by_role('cell', name='NS', exact=True)).to_be_visible()
                    expect(page.get_by_role('cell', name='SOA', exact=True)).to_be_visible()

                details()
                require(all((p, 200) in native_reads for p in (COLLECTION, item, item + '/rrsets')),
                        'Missing direct native browser list, zone or record-set response')
                require([z['id'] for z in listed() if verify_zone(z, expected)] == [zone_id],
                        'Independent native state did not list the created zone')
                exact = native_api('GET', item)
                require(exact.status == 200 and verify_zone(exact.json(), expected),
                        'Independent native state did not describe the created zone')
                page.get_by_role('button', name='Back to zones', exact=True).click()
                link = page.get_by_role('button', name=expected['name'], exact=True)
                expect(link).to_be_visible()
                start = len(native_reads)
                link.click()
                details()
                require(all((p, 200) in native_reads[start:] for p in (item, item + '/rrsets')),
                        'Reopening the listed zone did not issue native browser requests')
                require(not browser_errors and not malformed_origin and not native_redirects,
                        'Unexpected browser error, cross-origin hop, or native redirect')
            finally:
                if eligible_for_cleanup:
                    matches = [z for z in listed() if verify_zone(z, expected)]
                    require(len(matches) <= 1, 'Will not clean an ambiguous fixture')
                    if matches:
                        require(zone_id is None or zone_id == matches[0]['id'], 'Will not clean a mismatched zone')
                        deleted = native_api('DELETE', COLLECTION + '/' + matches[0]['id'])
                        require(deleted.status in (200, 204), 'Native zone cleanup failed')
                    elif zone_id is not None:
                        raise AssertionError('Expected owned fixture disappeared before cleanup')
                    require(listed() == [], 'Owned DNS project was not empty after cleanup')
        finally:
            browser.close()


def run():
    run_preflight()
    checksum = binary_checksum()
    chromium = os.environ.get('CHROMIUM_EXECUTABLE', '')
    require(Path(chromium).is_absolute() and Path(chromium).is_file(), 'Explicit Chromium required')
    os.environ['NO_PROXY'] = os.environ['no_proxy'] = '127.0.0.1,localhost'
    with tempfile.TemporaryDirectory(prefix='gcp-dns-gui-') as temp:
        team = Team(Path(temp))
        try:
            team.start()
            run_gui(team, chromium)
        finally:
            team.stop()
    print(json.dumps({'binarySha256': checksum, 'cloudDNSConsole': True, 'ownerScopedCapability': True,
                      'nativeListCreateDetailsAndRecordSets': True, 'nativeCleanup': True, 'realCloud': False}))


if __name__ == '__main__':
    run()
