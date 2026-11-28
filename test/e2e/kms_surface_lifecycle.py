"""Use the shipped KMS Console and Scalar with a scoped browser session and real SDK clients."""
import argparse
from contextlib import ExitStack
from datetime import datetime, timedelta, timezone
import hashlib
from http.client import HTTPConnection
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from kms_grants import clean_key, denied, kms_client, role_client
from sts_session_lifecycle import (LocalClients, LocalSimulator, ROLE, ROLE_ARN, TRUST,
                                   cleanup_role, successful)

PRINCIPAL = 'kms-surface-review'
JSON11 = 'application/x-amz-json-1.1'
ALIAS = 'alias/shared-surface'
EXAMPLE_KEY = '11111111-1111-4111-8111-111111111111'
PRESETS = {
    'CreateGrant': {'KeyId': EXAMPLE_KEY, 'GranteePrincipal': 'arn:aws:iam::000000000000:role/practice',
                    'Operations': ['DescribeKey']},
    'ListGrants': {'KeyId': EXAMPLE_KEY},
    'ScheduleKeyDeletion': {'KeyId': EXAMPLE_KEY, 'PendingWindowInDays': 7},
    'CancelKeyDeletion': {'KeyId': EXAMPLE_KEY},
    'RevokeGrant': {'KeyId': EXAMPLE_KEY, 'GrantId': '0' * 64},
}


def control(team, method, path, fields):
    connection = HTTPConnection('127.0.0.1', team.port, timeout=5)
    try:
        connection.request(method, path, json.dumps(fields),
                           {'Content-Type': 'application/json', 'X-Simulator-Token': team.token})
        response = connection.getresponse()
        assert response.status in (200, 201, 204)
        data = response.read()
        return json.loads(data) if data else {}
    finally:
        connection.close()


def issue_browser_access(team, arn):
    control(team, 'PUT', f'/_simulator/v1/iam/principals/{PRINCIPAL}/policy', {
        'Version': '2012-10-17', 'Statement': [
            {'Effect': 'Allow', 'Action': 'kms:ListKeys', 'Resource': '*'},
            {'Effect': 'Allow', 'Action': ['kms:' + op for op in [*PRESETS, 'DescribeKey', 'EnableKey']],
             'Resource': arn},
        ],
    })
    return control(team, 'POST', '/_simulator/v1/access', {'Principal': PRINCIPAL})['AccessToken']


class Surfaces:
    def __init__(self, browser, team, arn):
        self.team = team
        self.context = browser.new_context(locale='en-US', viewport={'width': 1440, 'height': 1000},
                                           service_workers='block')
        self.outside, self.errors, self.actions = [], [], []
        self.context.route('**/*', self.guard)
        self.console = self.page()
        self.docs = self.page()
        try:
            self.console.goto(team.endpoint + '/_simulator/console/')
            self.console.get_by_label('Access token', exact=True).fill(issue_browser_access(team, arn))
            self.console.get_by_role('button', name='Open sandbox', exact=True).click()
            self.console.get_by_role('button', name='Services', exact=True).click()
            self.console.get_by_role('menuitem', name='Key Management Service', exact=True).click()
            expect(self.console.get_by_role('table', name='Customer managed keys', exact=True)).to_be_visible()
        except BaseException:
            self.context.close()
            raise

    def page(self):
        page = self.context.new_page()
        page.on('pageerror', lambda e: self.errors.append(type(e).__name__))
        page.on('request', self.observe)
        return page

    def guard(self, route):
        requested, owner = urlsplit(route.request.url), urlsplit(self.team.endpoint)
        if (requested.scheme, requested.netloc) == (owner.scheme, owner.netloc):
            route.continue_()
        else:
            self.outside.append((requested.scheme, requested.hostname))
            route.abort()

    def observe(self, request):
        if request.url == self.team.endpoint + '/' and request.method == 'POST':
            target = request.headers.get('x-amz-target', '')
            if target not in ('TrentService.ListKeys', 'TrentService.DescribeKey'):
                self.actions.append(request)

    def close(self):
        self.context.close()
        assert not self.outside and not self.errors

    def check_request(self, response, op, fields, *, console=False):
        request = response.request
        headers = request.all_headers()
        assert request.url == self.team.endpoint + '/' and request.method == 'POST'
        assert request.redirected_from is None and request.redirected_to is None
        assert headers['x-amz-target'] == 'TrentService.' + op and request.post_data_json == fields
        assert headers['content-type'].split(';')[0] == JSON11
        assert any(part.strip().startswith('tcsim_') for part in headers['cookie'].split(';'))
        assert not {'authorization', 'x-amz-security-token', 'x-simulator-token',
                    'x-simulator-access-role'}.intersection(headers)
        if console:
            assert headers['x-simulator-principal'] == PRINCIPAL
        else:
            assert 'x-simulator-principal' not in headers
        assert response.headers['content-type'].split(';')[0] == JSON11

    def scalar(self, op, fields, status, shown):
        page = self.docs
        previous = len(self.actions)
        with page.expect_response(lambda r: r.url == self.team.endpoint + '/_simulator/docs/openapi.json') as loaded:
            page.goto(self.team.endpoint + '/_simulator/docs/')
        root = loaded.value.json()['paths']['/']['post']
        assert root['operationId'] == 'awsRegionalOperation'
        preset = root['requestBody']['content'][JSON11]['examples']['KMS' + op]
        assert preset['value'] == PRESETS[op] and preset['summary'] == 'X-Amz-Target: TrentService.' + op
        page.get_by_role('link', name='Shared AWS JSON/Query endpoint', exact=True).click()
        expect(page.get_by_role('button', name='Selected Content Type: ' + JSON11, exact=True)).to_be_visible()
        page.get_by_test_id('example-picker').click()
        page.get_by_role('option', name=preset['summary'], exact=True).click()
        page.get_by_role('button', name='Test Request (post /)', exact=True).click()
        dialog = page.get_by_role('dialog').filter(has=page.get_by_role('button', name='Close Client', exact=True))
        panel = dialog.get_by_role('region', name='Request: ' + root['summary'], exact=True)
        panel.get_by_role('tab', name='Body', exact=True).press('Enter')
        editor = panel.get_by_role('textbox')
        expect(editor).to_contain_text(EXAMPLE_KEY)
        assert json.loads(editor.inner_text()) == PRESETS[op]
        editor.click()
        editor.press('ControlOrMeta+a')
        page.keyboard.insert_text(json.dumps(fields))
        assert json.loads(editor.inner_text()) == fields
        panel.get_by_role('tab', name='Headers', exact=True).press('Enter')
        row = panel.get_by_role('row', name=re.compile(r'^Include X-Amz-Target in request '))
        field = row.get_by_role('combobox', name='Value', exact=True)
        field.fill('TrentService.' + op)
        field.press('Tab')
        row.get_by_role('checkbox', name='Include X-Amz-Target in request', exact=True).check()
        for name in ('X-Simulator-Service', 'X-Simulator-Region'):
            panel.get_by_role('checkbox', name=f'Include {name} in request', exact=True).uncheck()
        assert len(self.actions) == previous
        with page.expect_response(lambda r: r.url == self.team.endpoint + '/' and r.request.method == 'POST') as sent:
            dialog.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
        response = sent.value
        assert self.actions[previous:] == [response.request]
        self.check_request(response, op, fields)
        assert response.status == status
        body = dialog.locator('.response-section-content-body').filter(
            has=page.get_by_role('link', name='Download Response Body', exact=True))
        if shown is None:
            assert op == 'RevokeGrant' and response.body() == b''
            expect(dialog.get_by_role('link', name='200 OK', exact=True)).to_be_visible()
            expect(body).to_have_count(0)
            data = {}
        else:
            expect(body).to_be_visible()
            expect(body).to_contain_text(shown)
            data = response.json()
        dialog.get_by_role('button', name='Close Client', exact=True).click()
        return data

    def inspect(self, key, state):
        page = self.console
        back = page.get_by_role('button', name='Customer managed keys', exact=True)
        if back.is_visible():
            back.click()
        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'TrentService.DescribeKey') as sent:
            page.get_by_role('link', name=key['KeyId'], exact=True).click()
        response = sent.value
        self.check_request(response, 'DescribeKey', {'KeyId': key['KeyId']}, console=True)
        metadata = response.json()['KeyMetadata']
        assert response.status == 200 and metadata['Arn'] == key['Arn'] and metadata['KeyState'] == state
        expect(page.get_by_role('heading', name=key['KeyId'], exact=True)).to_be_visible()
        expect(page.get_by_text(state, exact=True)).to_be_visible()
        return metadata

    def enable(self, key):
        page, previous = self.console, len(self.actions)
        with page.expect_response(lambda r: r.request.headers.get('x-amz-target') == 'TrentService.EnableKey') as sent:
            page.get_by_role('button', name='Enable', exact=True).click()
        assert self.actions[previous:] == [sent.value.request] and sent.value.status == 200
        self.check_request(sent.value, 'EnableKey', {'KeyId': key['KeyId']}, console=True)
        expect(page.get_by_text('Enabled', exact=True)).to_be_visible()


def snapshot(client, arn):
    return (successful(client.describe_key, KeyId=arn)['KeyMetadata'],
            successful(client.list_grants, KeyId=arn)['Grants'],
            successful(client.list_aliases, KeyId=arn)['Aliases'])


def cleanup_key(client, arn):
    if successful(client.describe_key, KeyId=arn)['KeyMetadata']['KeyState'] == 'PendingDeletion':
        successful(client.cancel_key_deletion, KeyId=arn)
    for alias in successful(client.list_aliases, KeyId=arn)['Aliases']:
        assert alias['AliasName'] == ALIAS
        successful(client.delete_alias, AliasName=ALIAS)
    clean_key(client, arn)


def exercise(session, config, binary, root, stack, browser):
    from botocore.exceptions import ClientError
    teams = []
    for name in ('owner', 'peer'):
        team = LocalSimulator(root / name, binary)
        stack.callback(team.close)
        team.start()
        clients = LocalClients(session, team, stack, config)
        iam, sts = clients.client('iam'), clients.client('sts')
        role = successful(iam.create_role, RoleName=ROLE, AssumeRolePolicyDocument=json.dumps(TRUST))['Role']
        stack.callback(cleanup_role, iam, ClientError)
        kms = kms_client(session, team, stack, config)
        key = successful(kms.create_key, Description='shared KMS browser practice')['KeyMetadata']
        stack.callback(cleanup_key, kms, key['Arn'])
        successful(kms.create_alias, AliasName=ALIAS, TargetKeyId=key['KeyId'])
        teams.append((team, kms, key, role, sts))
    (owner, kms, key, role, sts), (peer, peer_kms, peer_key, peer_role, _) = teams
    arn = key['Arn']
    assert owner.token != peer.token and owner.port != peer.port and arn != peer_key['Arn']
    assert role['Arn'] == peer_role['Arn'] == ROLE_ARN and role['RoleId'] != peer_role['RoleId']
    successful(peer_kms.create_grant, KeyId=peer_key['Arn'], GranteePrincipal=ROLE_ARN, Operations=['DescribeKey'])
    other = successful(kms.create_key, Description='out-of-scope sibling')['KeyMetadata']['Arn']
    stack.callback(cleanup_key, kms, other)
    sibling, peer_state = snapshot(kms, other), snapshot(peer_kms, peer_key['Arn'])
    assert len(peer_state[1]) == 1 and len(peer_state[2]) == 1
    sample = b'local KMS surface round trip'
    ciphertext = successful(kms.encrypt, KeyId=ALIAS, Plaintext=sample)['CiphertextBlob']
    reader = role_client(session, owner, stack, config, sts, role['RoleId'])
    denied(lambda: reader.describe_key(KeyId=arn), 'AccessDeniedException', 403)

    def end_access():
        if owner.process is None:
            owner.start()
        control(owner, 'PUT', f'/_simulator/v1/iam/principals/{PRINCIPAL}/policy',
                {'Version': '2012-10-17', 'Statement': [{'Effect': 'Deny', 'Action': '*', 'Resource': '*'}]})
    stack.callback(end_access)

    with ExitStack() as first:
        surfaces = Surfaces(browser, owner, arn)
        first.callback(surfaces.close)
        surfaces.inspect(key, 'Enabled')
        grant = surfaces.scalar('CreateGrant', {'KeyId': arn, 'GranteePrincipal': ROLE_ARN,
                                                'Operations': ['DescribeKey']}, 200, 'GrantId')
        assert set(grant) == {'GrantId', 'GrantToken'} and grant['GrantToken']
        assert successful(reader.describe_key, KeyId=arn, GrantTokens=[grant['GrantToken']])['KeyMetadata'] == key
        listed = surfaces.scalar('ListGrants', {'KeyId': arn}, 200, grant['GrantId'])
        (entry,) = listed['Grants']
        (sdk_entry,) = successful(kms.list_grants, KeyId=arn)['Grants']
        assert not listed['Truncated'] and entry['GrantId'] == sdk_entry['GrantId'] == grant['GrantId']
        assert entry['GranteePrincipal'] == sdk_entry['GranteePrincipal'] == ROLE_ARN
        assert entry['Operations'] == sdk_entry['Operations'] == ['DescribeKey']
        assert entry['KeyId'] == sdk_entry['KeyId'] == arn and 'GrantToken' not in entry
        owner_before = snapshot(kms, arn)
        refused = surfaces.scalar('ScheduleKeyDeletion', {'KeyId': other, 'PendingWindowInDays': 7},
                                  403, 'AccessDeniedException')
        assert refused['__type'] == 'AccessDeniedException'
        assert snapshot(kms, other) == sibling and snapshot(kms, arn) == owner_before
        earliest = datetime.now(timezone.utc) + timedelta(days=7) - timedelta(milliseconds=1)
        scheduled = surfaces.scalar('ScheduleKeyDeletion', {'KeyId': arn, 'PendingWindowInDays': 7},
                                    200, 'PendingDeletion')
        due = datetime.fromtimestamp(scheduled['DeletionDate'], timezone.utc)
        assert earliest <= due <= datetime.now(timezone.utc) + timedelta(days=7)
        assert scheduled['KeyId'] == arn and scheduled['PendingWindowInDays'] == 7
        assert scheduled['KeyState'] == 'PendingDeletion'
        pending = successful(reader.describe_key, KeyId=arn)['KeyMetadata']
        assert pending['KeyState'] == 'PendingDeletion' and not pending['Enabled'] and pending['DeletionDate'] == due
        assert surfaces.inspect(key, 'PendingDeletion')['DeletionDate'] == scheduled['DeletionDate']
        assert snapshot(peer_kms, peer_key['Arn']) == peer_state
        assert len(surfaces.actions) == 4

    owner.close()
    owner.start()
    denied(lambda: reader.describe_key(KeyId=arn), 'UnrecognizedClientException', 403)
    reader = role_client(session, owner, stack, config, sts, role['RoleId'])
    assert successful(reader.describe_key, KeyId=arn, GrantTokens=[grant['GrantToken']])['KeyMetadata'] == pending
    assert snapshot(kms, other) == sibling and snapshot(peer_kms, peer_key['Arn']) == peer_state
    with ExitStack() as after_restart:
        surfaces = Surfaces(browser, owner, arn)
        after_restart.callback(surfaces.close)
        assert surfaces.inspect(key, 'PendingDeletion')['DeletionDate'] == scheduled['DeletionDate']
        assert surfaces.scalar('CancelKeyDeletion', {'KeyId': arn}, 200, arn) == {'KeyId': arn}
        disabled = successful(reader.describe_key, KeyId=arn)['KeyMetadata']
        assert disabled['KeyState'] == 'Disabled' and not disabled['Enabled'] and 'DeletionDate' not in disabled
        assert 'DeletionDate' not in surfaces.inspect(key, 'Disabled')
        denied(lambda: kms.decrypt(CiphertextBlob=ciphertext), 'DisabledException', 400)
        surfaces.enable(key)
        assert successful(kms.describe_key, KeyId=arn)['KeyMetadata'] == key
        assert successful(kms.decrypt, CiphertextBlob=ciphertext)['Plaintext'] == sample
        assert surfaces.scalar('RevokeGrant', {'KeyId': arn, 'GrantId': grant['GrantId']}, 200, None) == {}
        denied(lambda: reader.describe_key(KeyId=arn), 'AccessDeniedException', 403)
        assert successful(kms.list_grants, KeyId=arn)['Grants'] == []
        assert snapshot(kms, other) == sibling and snapshot(peer_kms, peer_key['Arn']) == peer_state
        assert len(surfaces.actions) == 3


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    assert not any(k.startswith(('AWS_', 'BOTO_')) for k in os.environ)
    assert all(not p.exists() and not p.is_symlink() for p in
               (Path.home() / '.aws/config', Path.home() / '.aws/credentials', Path.home() / '.boto', Path('/etc/boto.cfg')))
    assert (version('boto3'), version('botocore'), version('playwright')) == ('1.42.0', '1.41.6', '1.62.0')
    import boto3
    from botocore.config import Config
    binary = args.binary.resolve(strict=True)
    with binary.open('rb') as source:
        assert hashlib.file_digest(source, 'sha256').hexdigest() == args.binary_sha256
    session = boto3.Session()
    config = Config(connect_timeout=3, read_timeout=5, retries={'max_attempts': 0}, proxies={})
    with tempfile.TemporaryDirectory(prefix='kms-surface-') as directory, sync_playwright() as p:
        with p.chromium.launch(executable_path=p.chromium.executable_path, chromium_sandbox=True) as browser, ExitStack() as stack:
            exercise(session, config, binary, Path(directory), stack, browser)
    assert not Path(directory).exists()
    print('KMS restricted Scalar 6 sends, Console enable, signed SDK readback, restart, peer and cleanup passed')


if __name__ == '__main__':
    main()
