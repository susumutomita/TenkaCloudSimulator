"""Exercise one synthetic IAM role from the Console, served GetRole form and STS SDK."""
import argparse
from collections import Counter
from contextlib import ExitStack
from datetime import datetime, timezone
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import parse_qs, unquote
from xml.etree import ElementTree

from botocore.exceptions import ClientError
from playwright.sync_api import expect, sync_playwright

from sts_session_lifecycle import LocalClients, LocalSimulator, TRUST, need, successful

ROLE = 'practice-executor'  # The existing GetRole example's name, in a fresh private DB.
ARN = 'arn:aws:iam::000000000000:role/' + ROLE
SESSION = 'console-role-check'
DENIED = {'Version': TRUST['Version'], 'Statement': [{**TRUST['Statement'][0], 'Effect': 'Deny'}]}
FORM = 'application/x-www-form-urlencoded'


def form_action(request):
    return parse_qs(request.post_data or '').get('Action', [''])[0]


def console_context(browser, team, token, outside):
    context = browser.new_context(service_workers='block', locale='en-US')

    def restrict(route):
        if route.request.url.startswith(team.endpoint + '/'):
            route.continue_()
        else:
            outside.append(True)
            route.abort()

    context.route('**/*', restrict)
    page = context.new_page()
    page.goto(team.endpoint + '/_simulator/console/')
    page.get_by_label('Access token', exact=True).fill(token)
    page.get_by_role('button', name='Open sandbox', exact=True).click()
    page.get_by_role('button', name='Services', exact=True).click()
    page.get_by_role('menuitem', name='IAM', exact=True).click()
    expect(page.get_by_role('heading', name='Roles', exact=True)).to_be_visible()
    return context, page


def create_role(page):
    page.get_by_role('button', name='Create role', exact=True).first.click()
    page.get_by_role('radio', name='Custom trust policy', exact=True).check()
    page.get_by_label('Trust policy JSON', exact=True).fill(json.dumps(TRUST))
    for _ in range(2):
        page.get_by_role('button', name='Next', exact=True).click()
    page.get_by_label('Role name', exact=True).fill(ROLE)
    page.get_by_role('button', name='Create role', exact=True).click()
    expect(page.get_by_role('heading', name=ROLE, exact=True)).to_be_visible()
    page.get_by_role('button', name='Roles', exact=True).click()
    row = page.get_by_role('row').filter(has=page.get_by_role('link', name=ROLE, exact=True))
    expect(row).to_contain_text('Allow · ' + TRUST['Statement'][0]['Principal']['AWS'])
    row.get_by_role('link', name=ROLE, exact=True).click()


def get_role_send(context, endpoint):
    page = context.new_page()
    sends = []
    page.on('request', lambda request: sends.append(request)
            if request.url == endpoint + '/' and request.method == 'POST' else None)
    with page.expect_response(lambda response: response.url == endpoint + '/_simulator/docs/openapi.json') as served:
        page.goto(endpoint + '/_simulator/docs/')
    example = served.value.json()['paths']['/']['post']['requestBody']['content'][FORM]['examples']['GetRole']
    fields = {'Action': 'GetRole', 'Version': '2010-05-08', 'RoleName': ROLE}
    need(example['value'] == fields, 'served GetRole example changed')
    page.get_by_role('button', name='Test Request (post /)', exact=True).click()
    dialog = page.get_by_role('dialog').filter(has=page.get_by_role('button', name='Close Client', exact=True))
    dialog.get_by_role('button', name='Body', exact=True).click()
    media = dialog.get_by_role('button', name=re.compile(
        r'^(application/x-amz-json-1\.[01]|application/x-www-form-urlencoded|Form URL Encoded)$'))
    if media.inner_text().strip() not in (FORM, 'Form URL Encoded'):
        media.click()
        page.get_by_role('option', name='Form URL Encoded', exact=True).click()
    dialog.get_by_test_id('example-picker').click()
    page.get_by_role('option', name=example['summary'], exact=True).click()
    for key in fields:
        box = dialog.get_by_role('checkbox', name='Include ' + key + ' in request', exact=True)
        if box.count() and not box.is_checked():
            box.check()
    dialog.get_by_role('button', name='Headers', exact=True).click()
    service = dialog.get_by_label('X-Simulator-Service Value', exact=True)
    service.fill('iam')
    service.press('Tab')
    dialog.get_by_role('checkbox', name='Include X-Simulator-Service in request', exact=True).check()
    for header in ('X-Amz-Target', 'X-Simulator-Region'):
        dialog.get_by_role('checkbox', name='Include ' + header + ' in request', exact=True).uncheck()
    need(not sends, 'the reference sent the form before Send')
    with page.expect_response(lambda response: response.url == endpoint + '/' and response.request.method == 'POST') as sent:
        dialog.get_by_role('button', name=re.compile('^Send post request to ')).click()
    response = sent.value
    need(response.status == 200 and sends == [response.request], 'expected one successful GetRole Send')
    headers = response.request.all_headers()
    need(headers.get('cookie') and headers.get('x-simulator-service') == 'iam'
         and not {'x-simulator-principal', 'x-simulator-token', 'authorization', 'x-amz-target'}.intersection(headers)
         and headers['content-type'].split(';')[0] == FORM, 'unexpected browser authentication or routing')
    need(parse_qs(response.request.post_data, keep_blank_values=True) == {k: [v] for k, v in fields.items()},
         'GetRole Send did not use the served example')
    role = ElementTree.fromstring(response.body()).find('{*}GetRoleResult/{*}Role')
    need(role is not None and role.findtext('{*}Arn') == ARN
         and json.loads(unquote(role.findtext('{*}AssumeRolePolicyDocument'))) == TRUST,
         'the reference did not return the role created by the Console')
    role_id = role.findtext('{*}RoleId')
    need(role_id, 'the saved IAM role has no ID')
    panel = dialog.locator('.response-section-content-body').filter(has_text='GetRoleResponse')
    expect(panel).to_contain_text(role_id)
    need(len(sends) == 1, 'the reference repeated GetRole')
    page.close()
    return role_id


def edit_trust(page, save):
    page.get_by_role('tab', name='Trust relationships', exact=True).click()
    page.get_by_role('button', name='Edit trust policy', exact=True).click()
    page.get_by_label('Trust policy JSON', exact=True).fill(json.dumps(DENIED))
    page.get_by_role('button', name='Save changes' if save else 'Cancel', exact=True).click()


def delete_role(page):
    page.get_by_role('button', name='Delete role', exact=True).click()
    dialog = page.get_by_role('dialog')
    dialog.get_by_label('Confirm role name', exact=True).fill(ROLE)
    dialog.get_by_role('button', name='Delete role', exact=True).click()
    expect(page.get_by_role('heading', name='No roles yet', exact=True)).to_be_visible()


def refused(call, code):
    try:
        call()
    except ClientError as error:
        need(error.response['Error']['Code'] == code and not error.response['ResponseMetadata']['RetryAttempts'],
             'unexpected local SDK refusal')
    else:
        raise AssertionError('expected a native SDK refusal')


def exercise(team, context, page, clients):
    iam, sts = clients.client('iam'), clients.client('sts')
    need(successful(iam.list_roles)['Roles'] == [], 'expected a fresh IAM store')
    mutations = Counter()
    def observe(request):
        if request.method == 'POST' and request.url == team.endpoint + '/':
            action = form_action(request)
            if action in {'CreateRole', 'UpdateAssumeRolePolicy', 'DeleteRole'}:
                mutations[action] += 1
    page.on('request', observe)
    try:
        create_role(page)
        role_id = get_role_send(context, team.endpoint)
        role = successful(iam.get_role, RoleName=ROLE)['Role']
        need(role['Arn'] == ARN and role['RoleId'] == role_id and role['AssumeRolePolicyDocument'] == TRUST,
             'SDK and reference read different saved role state')
        def fresh_assumption():
            return successful(sts.assume_role, RoleArn=ARN, RoleSessionName=SESSION, DurationSeconds=900)
        issued = fresh_assumption()
        credentials = issued['Credentials']
        need(credentials['Expiration'] > datetime.now(timezone.utc), 'assumed credentials already expired')
        assumed = clients.client('sts', {'aws_access_key_id': credentials['AccessKeyId'],
                                         'aws_secret_access_key': credentials['SecretAccessKey'],
                                         'aws_session_token': credentials['SessionToken']})
        identity = successful(assumed.get_caller_identity)
        need(identity['Arn'] == 'arn:aws:sts::000000000000:assumed-role/' + ROLE + '/' + SESSION
             and identity['UserId'] == role_id + ':' + SESSION, 'session identity did not match the role')
        edit_trust(page, False)
        expect(page.get_by_role('button', name='Edit trust policy', exact=True)).to_be_visible()
        need(successful(iam.get_role, RoleName=ROLE)['Role'] == role and mutations == {'CreateRole': 1},
             'cancelled editor changed the role')
        edit_trust(page, True)
        expect(page.get_by_text('Policy saved.', exact=True)).to_be_visible()
        saved = successful(iam.get_role, RoleName=ROLE)['Role']
        need(saved['AssumeRolePolicyDocument'] == DENIED and saved['RoleId'] == role_id, 'trust edit was not saved')
        refused(fresh_assumption, 'AccessDenied')
        need(successful(iam.get_role, RoleName=ROLE)['Role'] == saved, 'refusal changed the saved role')
        delete_role(page)
        refused(lambda: successful(iam.get_role, RoleName=ROLE), 'NoSuchEntity')
        refused(fresh_assumption, 'AccessDenied')
        need(mutations == {'CreateRole': 1, 'UpdateAssumeRolePolicy': 1, 'DeleteRole': 1},
             'unexpected Console mutation count')
    finally:
        if any(role['RoleName'] == ROLE for role in successful(iam.list_roles)['Roles']):
            successful(iam.delete_role, RoleName=ROLE)
        need(successful(iam.list_roles)['Roles'] == [], 'fixture role cleanup failed')


def main():
    import boto3
    from botocore.config import Config
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    need(importlib.metadata.version('boto3') == '1.42.0' and importlib.metadata.version('botocore') == '1.41.6',
         'use the pinned SDK')
    session = boto3.Session()
    binary = args.binary.resolve(strict=True)
    with binary.open('rb') as data:
        need(hashlib.file_digest(data, 'sha256').hexdigest() == args.binary_sha256, 'binary digest mismatch')
    config = Config(connect_timeout=3, read_timeout=5, retries={'max_attempts': 0}, proxies={})
    with tempfile.TemporaryDirectory(prefix='sts-iam-console-') as directory, ExitStack() as stack:
        team = LocalSimulator(Path(directory) / 'team', binary)
        stack.callback(team.close)
        team.start()
        playwright = stack.enter_context(sync_playwright())
        browser = playwright.chromium.launch(chromium_sandbox=True,
                                             executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or None)
        stack.callback(browser.close)
        outside = []
        context, page = console_context(browser, team, team.token, outside)
        stack.callback(context.close)
        exercise(team, context, page, LocalClients(session, team, stack, config))
        need(not outside and team.sends == team.responses, 'a browser or SDK request left its expected scope')
    print(json.dumps({'binary_sha256': args.binary_sha256, 'console_create_edit_delete': True,
                      'scalar_cookie_get_role': True, 'signed_sdk_role_session': True,
                      'cancel_preserved_trust': True, 'changed_trust_refused_new_session': True,
                      'owned_role_cleanup': True}))


if __name__ == '__main__':
    main()
