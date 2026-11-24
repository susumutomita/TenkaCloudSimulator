"""Opt-in real browser acceptance of the local Sakura Server read-only view.

SIMULATOR_BINARY=/absolute/build/path SIMULATOR_BINARY_SHA256=<trusted digest>
CHROMIUM_EXECUTABLE=/absolute/chromium python test/e2e/sakura_server_console_gui.py

Requires a digest-bound build containing --enable-sakura-server-console.
Use an externally isolated, approved Linux loopback-only runner with working
Chromium sandboxing and no host networking sockets. Provision dependencies
first. /proc and request events are diagnostics, not egress enforcement.
No VM, real cloud, mock responses, or direct storage seeding. Not Scalar Send.
"""
import hashlib
import json
import os
from pathlib import Path
import queue
import re
from secrets import token_hex
import subprocess
import tempfile
import threading
import time
import urllib.request
from urllib.parse import urlsplit

from playwright.sync_api import sync_playwright, expect
from sakura_cli import BASIC, BASE as DISKS, CLI, NoRedirect

BINARY = Path(os.environ.get('SIMULATOR_BINARY', 'dist/tenkacloud-simulator')).resolve()
CAPS = '/_simulator/v1/capabilities'
SERVERS = '/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/server'
OWNER = 'learner'
DESCRIPTION = 'Saved local metadata only'
ENABLE = '--enable-sakura-server-console'


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


def cli_arguments(binary, state, tokenfile, console):
    args = [str(binary), '--listen', '127.0.0.1:0', '--state', str(state), '--token-file', str(tokenfile),
            '--enable-sakura-disks', '--sakura-environment', 'sakura-gui',
            '--sakura-project', 'practice', '--sakura-zone', 'is1a', '--sakura-owner', OWNER]
    return args + ([ENABLE] if console else [])


class ServerConsoleCLI(CLI):
    """Retain the exact checked native HTTP client and extend startup arguments only."""
    def __init__(self, binary, directory, console):
        self.directory = Path(directory)
        self.directory.mkdir()
        self.db = self.directory / 'state.db'
        self.tokenfile = self.directory / 'token'
        args = cli_arguments(binary, self.db, self.tokenfile, console)
        self.process = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                        text=True, cwd=self.directory,
                                        env={'PATH': os.defpath, 'CI': '1', 'GOMAXPROCS': '1'})
        self.lines, self.token_leak, self.token = queue.Queue(), False, ''
        self.thread = threading.Thread(target=self._drain, daemon=True)
        self.thread.start()
        self.calls = self.responses = 0
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        try:
            posted = self.lines.get(timeout=20)
            require(isinstance(posted, str) and posted.startswith('Console http://'),
                    'Owned simulator exit or bad readiness')
            try:
                parsed = urlsplit(posted[len('Console '):])
                require(parsed.scheme == 'http' and parsed.hostname == '127.0.0.1'
                        and parsed.port and not parsed.username and not parsed.password,
                        'Readiness must reference owned numeric loopback')
                self.origin = 'http://127.0.0.1:' + str(parsed.port)
            except ValueError as error:
                raise AssertionError('Bad simulator readiness') from error
            self.token = self.tokenfile.read_text().strip()
            require(len(self.token) >= 32, 'Expected local operator token')
        except BaseException:
            self.close()
            raise


def server_body(name):
    return {'Server': {'Name': name, 'Description': DESCRIPTION, 'Tags': [],
                       'Icon': {'ID': 0, 'Tags': []}, 'InterfaceDriver': 'virtio',
                       'ServerPlan': {'CPU': 2, 'MemoryMB': 4096, 'Commitment': 'standard',
                                      'ConfidentialVM': False},
                       'PrivateHost': {'ID': 0, 'Description': '', 'Tags': []},
                       'WaitDiskMigration': False}}


def exact_row(row, name, expected_description=None):
    if not isinstance(row, dict):
        return False
    identifier = row.get('ID')
    return (isinstance(identifier, str) and re.fullmatch('[1-9][0-9]{0,18}', identifier)
            and row.get('Name') == name and 'Availability' not in row
            and (expected_description is None or row.get('Description') == expected_description))


def verify_caps(document, enabled):
    disk, server = document.get('sakura'), document.get('sakuraServer')
    require(document.get('principal') == OWNER and document.get('identitySelection') is False,
            'Browser did not use the configured participant owner')
    require(isinstance(disk, dict) and disk.get('console') is True and disk.get('endpoint') == DISKS,
            'Existing Disk capability was not available')
    require(isinstance(server, dict) and server.get('console') is enabled
            and server.get('apiVersion') == '1.1' and server.get('zone') == 'is1a'
            and server.get('endpoint') == SERVERS and server.get('availabilityReported') is False
            and server.get('operations') == ['GET /server', 'GET /server/{id}'],
            'Unexpected scope, console opt-in or operation list')


def run_browser(browser, cli, token, enabled, server=None, disk=None):
    origin = cli.origin
    expected_path = SERVERS + '/' + server['ID'] if server else None
    context = browser.new_context(service_workers='block', locale='en-US', viewport={'width': 1440, 'height': 1000})
    try:
        context.set_default_timeout(30000)
        expect.set_options(timeout=30000)
        page = context.new_page()
        failures, reads = [], []
        page.on('pageerror', lambda _: failures.append('page error'))

        def path(request):
            return urlsplit(request.url).path if exact_origin(request.url, origin) else ''

        def api(p):
            return (p in (DISKS, SERVERS) or p.startswith(DISKS + '/') or p.startswith(SERVERS + '/'))

        def audit(req):
            p = path(req)
            if not redirect_chain_owned(req, origin):
                failures.append('cross-origin request or redirect')
            previous = req.redirected_from
            if previous is not None and (api(p) or api(path(previous))):
                failures.append('native API redirect')
            if api(p) and req.method != 'GET':
                failures.append('unexpected console mutation')
            if not enabled and (p == SERVERS or p.startswith(SERVERS + '/')):
                failures.append('unexpected native Server request while console disabled')

        def capture(response):
            p = path(response.request)
            if api(p):
                reads.append((p, response.status))
                if 300 <= response.status < 400:
                    failures.append('native API redirect')

        context.on('request', audit)
        page.on('response', capture)
        page.goto(origin + '/_simulator/console/')
        page.get_by_label('Access token', exact=True).fill(token)
        with page.expect_response(lambda r: path(r.request) == CAPS and r.request.method == 'GET') as bootstrap:
            page.get_by_role('button', name='Open sandbox', exact=True).click()
        require(bootstrap.value.status == 200, 'Local owner bootstrap failed')
        verify_caps(bootstrap.value.json(), enabled)
        with page.expect_response(lambda r: path(r.request) == DISKS and r.request.method == 'GET') as disk_list:
            page.goto(origin + '/_simulator/console/#/sakura-disks')
        require(disk_list.value.status == 200 and disk_list.value.request.header_value('authorization') == BASIC,
                'Legacy Disk native read failed')
        expect(page.get_by_role('heading', name='Disks', exact=True)).to_be_visible()
        if disk is None:
            expect(page.get_by_text('No Disk configurations', exact=True)).to_be_visible()
        else:
            expect(page.get_by_text(disk['Name'], exact=True)).to_be_visible()
        if not enabled:
            expect(page.locator('nav.sakura-resource-nav').get_by_role('button', name='Servers', exact=True)).to_have_count(0)
            page.goto(origin + '/_simulator/console/#/sakura-servers')
            expect(page.get_by_role('heading', name='Console unavailable', exact=True)).to_be_visible()
            expect(page.get_by_role('heading', name='Servers', exact=True)).to_have_count(0)
            # Materialize a positive ready-state condition rather than only taking an early negative snapshot.
            expect(page.locator('nav.sakura-resource-nav').get_by_role('button', name='Disks', exact=True)).to_be_visible()
            require(not failures, 'Default-disabled route did not stay inert')
            return

        rail = page.locator('nav.sakura-resource-nav').get_by_role('button', name='Servers', exact=True)
        with page.expect_response(lambda r: path(r.request) == SERVERS and r.request.method == 'GET') as listing:
            rail.click()
        response = listing.value
        require(response.status == 200 and response.request.header_value('authorization') == BASIC
                and response.json().get('Servers') == [server], 'GUI Server list did not see the real fixture')
        expect(page.get_by_role('heading', name='Servers', exact=True)).to_be_visible()
        expect(page.get_by_role('button', name=server['Name'], exact=True)).to_be_visible()
        page.get_by_role('checkbox', name='Select Server ' + server['ID'], exact=True).check()
        with page.expect_response(lambda r: path(r.request) == expected_path and r.request.method == 'GET') as item:
            page.get_by_role('button', name='Details', exact=True).click()
        response = item.value
        require(response.status == 200 and response.request.header_value('authorization') == BASIC
                and response.json().get('Server') == server, 'GUI detail was not the selected native Server')
        expect(page.get_by_role('heading', name='Server information', exact=True)).to_be_visible()
        expect(page.get_by_text(DESCRIPTION, exact=True)).to_be_visible()
        expect(page.get_by_text('2 vCPU / 4096 MB', exact=True)).to_be_visible()
        expect(page.get_by_text('Metadata only', exact=True)).to_be_visible()
        with page.expect_response(lambda r: path(r.request) == SERVERS and r.request.method == 'GET') as again:
            page.get_by_role('button', name='Back to Servers', exact=True).click()
        require(again.value.status == 200, 'GUI back navigation did not refresh the native Server list')
        expect(page.get_by_role('button', name=server['Name'], exact=True)).to_be_visible()
        require(not failures and reads.count((SERVERS, 200)) >= 2 and (expected_path, 200) in reads,
                'Browser request observation failed or native endpoint was not exercised')
    finally:
        context.close()


def run():
    run_preflight()
    digest = binary_checksum()
    chromium = os.environ.get('CHROMIUM_EXECUTABLE', '')
    require(Path(chromium).is_absolute() and Path(chromium).is_file(), 'Explicit Chromium binary is required')
    os.environ['NO_PROXY'] = os.environ['no_proxy'] = '127.0.0.1,localhost'
    with tempfile.TemporaryDirectory(prefix='sakura-server-console-gui-') as tmp, sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=chromium, chromium_sandbox=True, args=['--no-proxy-server'])
        try:
            for enabled in (False, True):
                cli = ServerConsoleCLI(BINARY, Path(tmp) / str(enabled), enabled)
                try:
                    participant = cli.call('POST', '/_simulator/v1/access', {'Principal': OWNER})['AccessToken']
                    actor = {'token': participant, 'principal': ''}
                    cli.call('GET', SERVERS, **actor)
                    if not enabled:
                        run_browser(browser, cli, participant, enabled)
                        continue
                    alternate = cli.call('POST', '/_simulator/v1/access', {'Principal': 'alternate'})['AccessToken']
                    cli.call('GET', SERVERS, status=403, token=alternate, principal='')
                    require(cli.call('GET', SERVERS, **actor)['Servers'] == []
                            and cli.call('GET', DISKS, **actor)['Disks'] == [],
                            'Fixture requires a fresh owned Sakura environment')
                    suffix = token_hex(6)
                    server_name, disk_name = 'gui-server-' + suffix, 'gui-disk-' + suffix
                    try:
                        server = cli.call('POST', SERVERS, server_body(server_name), 201, **actor)['Server']
                        require(exact_row(server, server_name, DESCRIPTION), 'Native Server create mismatch')
                        disk = cli.call('POST', DISKS, {'Disk': {'Name': disk_name, 'Plan': {'ID': 4},
                                                                'SizeMB': 20480}}, 201, **actor)['Disk']
                        require(exact_row(disk, disk_name), 'Native Disk create mismatch')
                        run_browser(browser, cli, participant, enabled, server=server, disk=disk)
                        require(cli.call('GET', SERVERS, **actor)['Servers'] == [server]
                                and cli.call('GET', DISKS, **actor)['Disks'] == [disk],
                                'Read-only browser interactions changed owned native metadata')
                    finally:
                        for collection, rows_key, name, label in ((DISKS, 'Disks', disk_name, None),
                                                                  (SERVERS, 'Servers', server_name, DESCRIPTION)):
                            rows = cli.call('GET', collection, **actor)[rows_key]
                            require(len(rows) <= 1 and all(exact_row(row, name, label) for row in rows),
                                    'Will not clean metadata outside the owned fixture')
                            if rows:
                                cli.call('DELETE', collection + '/' + rows[0]['ID'], **actor)
                            require(cli.call('GET', collection, **actor)[rows_key] == [], 'Metadata cleanup incomplete')
                finally:
                    cli.close()
        finally:
            browser.close()
    print(json.dumps({'binarySha256': digest, 'serverDefaultOff': True, 'explicitOptIn': True,
                      'scopedOwnerGUIListAndDetails': True, 'legacyDiskNativeRead': True,
                      'nativeCleanup': True, 'VMOrRealCloud': False}))


if __name__ == '__main__':
    run()
