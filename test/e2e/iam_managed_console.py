"""Real embedded IAM customer-policy console/SDK contract (source until run).

Two independent local CLIs/DB/token authorities, no real AWS or fabricated ACK.
Only one explicitly aborted post-ACK metadata read is an injected failure.
"""
from console_locale import select_console_locale
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import time
import urllib.error
import urllib.request
from urllib.parse import parse_qs, urlencode, urlsplit

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import expect, sync_playwright
from reflow import assert_reflow
from sqs import Team, NoRedirect

REPORTS = Path(os.environ.get('SIMULATOR_REPORTS', 'reports')).resolve()
OPERATIONS = {'CreatePolicy', 'GetPolicy', 'ListPolicies', 'DeletePolicy',
              'CreatePolicyVersion', 'GetPolicyVersion', 'ListPolicyVersions',
              'SetDefaultPolicyVersion', 'DeletePolicyVersion', 'AttachRolePolicy',
              'DetachRolePolicy', 'ListAttachedRolePolicies'}
_PRIVATE = []
TRUST = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow',
         'Action': 'sts:AssumeRole', 'Principal': {'Service': 'lambda.amazonaws.com'}}]}


def action(request):
    return parse_qs(request.post_data or '').get('Action', [''])[0]


def run():
    try:
        _run()
    except Exception as failure:
        message = str(failure)
        for secret in _PRIVATE:
            if secret:
                message = message.replace(secret, '[redacted]')
        raise RuntimeError(type(failure).__name__ + ': ' + message) from None
    finally:
        _PRIVATE.clear()


def _run():
    REPORTS.mkdir(parents=True, exist_ok=True)
    axe_path = Path(os.environ['AXE_SCRIPT'])
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == json.loads(
        (axe_path.parent / 'provenance.json').read_text())['bundleSha256']
    axe = axe_path.read_text()
    sdk_counts, screens, completed = [], [], []
    errors, outside = [], []
    with tempfile.TemporaryDirectory(prefix='iam-managed-console-') as directory:
        a, b = Team(Path(directory) / 'a'), Team(Path(directory) / 'b')
        a.directory.mkdir(); b.directory.mkdir()

        def sdk(team, service, principal=None, token=None):
            counts = {'calls': 0, 'sends': 0, 'responses': 0}
            client = boto3.client(service, endpoint_url=team.endpoint,
                region_name='us-east-1', aws_access_key_id='TCSIMIAMMANAGED',
                aws_secret_access_key='dummy-local-only',
                config=Config(retries={'max_attempts': 0}, connect_timeout=2,
                              read_timeout=5, s3={'addressing_style': 'path'},
                              request_checksum_calculation='when_required',
                              response_checksum_validation='when_required'))
            def signed(request, **_):
                request.headers.add_header('X-Simulator-Token', token or team.token)
                if principal:
                    request.headers.add_header('X-Simulator-Principal', principal)
            def called(**_):
                counts['calls'] += 1
            def sent(request, **_):
                target = urlsplit(request.url)
                assert target.scheme == 'http' and target.netloc == urlsplit(team.endpoint).netloc
                counts['sends'] += 1
            def received(http_response, **_):
                counts['responses'] += 1
                assert not 300 <= http_response.status_code < 400
            client.meta.events.register('before-sign.' + service, signed)
            client.meta.events.register('before-call.' + service, called)
            client.meta.events.register('before-send.' + service, sent)
            client.meta.events.register('after-call.' + service, received)
            team.clients.append(client); sdk_counts.append(counts)
            return client

        def refused(call, status=403):
            try:
                call()
            except ClientError as failure:
                assert failure.response['ResponseMetadata']['HTTPStatusCode'] == status
            else:
                raise AssertionError('Native operation unexpectedly succeeded')

        try:
            a.start(); b.start(); _PRIVATE.extend([a.token, b.token])
            iam, peer = sdk(a, 'iam'), sdk(b, 'iam')
            objects, peer_objects = sdk(a, 's3'), sdk(b, 's3')
            # Local access failures are JSON401 before IAM XML dispatch. Check
            # actual HTTP status directly rather than claim botocore can parse
            # this independent authentication envelope as an IAM service error.
            foreign = urllib.request.Request(b.endpoint + '/', method='POST',
                data=urlencode({'Action': 'ListPolicies', 'Version': '2010-05-08',
                                'Scope': 'Local'}).encode(),
                headers={'Content-Type': 'application/x-www-form-urlencoded',
                         'X-Simulator-Service': 'iam', 'X-Simulator-Token': a.token})
            try:
                urllib.request.build_opener(NoRedirect()).open(foreign, timeout=5)
            except urllib.error.HTTPError as failure:
                assert failure.code == 401
                assert json.load(failure) == {'error': 'A valid simulator access token is required'}
            else:
                raise AssertionError('Foreign token unexpectedly crossed CLI authority')
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(
                    executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),
                    args=['--no-sandbox'])
                context = None
                try:
                    for width in (1440, 390):
                        name, role_name, bucket = ('managed-console-' + str(width),
                            'managed-role-' + str(width), 'managed-bucket-' + str(width))
                        payload, sentinel = b'dummy-own-byte-body', b'dummy-peer-byte-body'
                        arn = 'arn:aws:iam::000000000000:policy/' + name
                        allow = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow',
                                 'Action': 's3:GetObject', 'Resource': 'arn:aws:s3:::' + bucket + '/object'}]}
                        deny = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Deny',
                                'Action': 's3:GetObject', 'Resource': 'arn:aws:s3:::' + bucket + '/object'}]}
                        role = iam.create_role(RoleName=role_name, AssumeRolePolicyDocument=json.dumps(TRUST))['Role']
                        peer_role = peer.create_role(RoleName=role_name, AssumeRolePolicyDocument=json.dumps(TRUST))['Role']
                        peer_policy = peer.create_policy(PolicyName=name, PolicyDocument=json.dumps(deny))['Policy']
                        assert role['RoleId'] and peer_role['RoleId']
                        objects.create_bucket(Bucket=bucket); peer_objects.create_bucket(Bucket=bucket)
                        objects.put_object(Bucket=bucket, Key='object', Body=payload)
                        peer_objects.put_object(Bucket=bucket, Key='object', Body=sentinel)
                        as_role = sdk(a, 's3', principal=role_name)
                        refused(lambda: as_role.get_object(Bucket=bucket, Key='object'))
                        context = browser.new_context(viewport={'width': width, 'height': 1000})
                        page = context.new_page(); page.set_default_timeout(10000)
                        page.on('pageerror', lambda _: errors.append(True))
                        operations, responses, failed_reads = [], [], []
                        intercept = [None]
                        def guard(route):
                            target = urlsplit(route.request.url)
                            if target.scheme != 'http' or target.netloc != urlsplit(a.endpoint).netloc:
                                outside.append(True); route.abort(); return
                            # One owner handles every route, including the explicit
                            # hold/abort fixtures. Mixing page/context route stacks
                            # can hand an already-handled request to another owner.
                            if intercept[0] is not None and intercept[0](route):
                                return
                            route.continue_()
                        context.route('**/*', guard)
                        page.on('request', lambda request: operations.append(action(request))
                            if action(request) in OPERATIONS else None)
                        page.on('response', lambda response: responses.append(
                            (action(response.request), response.status))
                            if action(response.request) in OPERATIONS else None)
                        page.on('requestfailed', lambda request: failed_reads.append(action(request))
                            if action(request) in OPERATIONS else None)

                        def audit(label):
                            assert_reflow(page)
                            for secret in _PRIVATE:
                                assert secret not in page.content() and secret not in page.url
                                assert not page.evaluate('(v)=>Object.values(localStorage).some(x=>x.includes(v))||Object.values(sessionStorage).some(x=>x.includes(v))', secret)
                            page.evaluate(axe)
                            result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
                            assert not result['violations'], 'IAM managed policy accessibility violation'
                            page.screenshot(path=str(REPORTS / (label + '-' + str(width) + '.png')),
                                            full_page=False, animations='disabled')
                            screens.append({'name': label, 'width': width, 'violations': 0})

                        def native(button, operation, scope=None, status=200):
                            with page.expect_response(lambda response: action(response.request) == operation) as result:
                                (scope or page).get_by_role('button', name=button, exact=True).click()
                            assert result.value.status == status

                        def language(value):
                            select_console_locale(page, value)
                            expect(page.locator('html')).to_have_attribute('lang', value)

                        page.goto(a.endpoint + '/_simulator/console/')
                        page.get_by_label('Access token', exact=True).fill(a.token)
                        page.get_by_role('button', name='Open sandbox', exact=True).click()
                        page.wait_for_url(re.compile(r'/_simulator/console/(?:#.*)?$'))
                        expect(page.get_by_role('button', name='Services', exact=True)).to_be_enabled()
                        page.get_by_role('button', name='Services', exact=True).click()
                        page.get_by_role('menuitem', name='IAM', exact=True).click()
                        # Preserve a real pending initial list across Create/Cancel.
                        # Cancel must clear loading while rejecting its old response.
                        sentinel_name = 'cancelled-read-sentinel-' + str(width)
                        sentinel_policy = iam.create_policy(PolicyName=sentinel_name,
                            PolicyDocument=json.dumps(deny))['Policy']
                        held_initial = []
                        def initial_list(route):
                            if action(route.request) == 'ListPolicies':
                                held_initial.append(route); return True
                            return False
                        intercept[0] = initial_list
                        page.get_by_role('button', name='Policies', exact=True).click()
                        deadline = time.monotonic() + 5
                        while not held_initial:
                            assert time.monotonic() < deadline
                            page.wait_for_timeout(10)
                        assert len(held_initial) == 1
                        page.get_by_role('button', name='Create policy', exact=True).click()
                        page.get_by_label('Permissions policy JSON', exact=True).fill('DUMMY-cancelled-draft')
                        page.get_by_role('button', name='Cancel', exact=True).click()
                        with page.expect_response(lambda response: action(response.request) == 'ListPolicies') as cancelled_list:
                            held_initial.pop().continue_()
                        assert cancelled_list.value.status == 200
                        intercept[0] = None
                        expect(page.get_by_role('button', name='Refresh', exact=True)).to_be_enabled()
                        assert operations.count('CreatePolicy') == 0
                        # The held native result contains a real row, so rejecting
                        # stale publication is observable rather than an empty fixture.
                        expect(page.get_by_role('link', name=sentinel_name, exact=True)).to_have_count(0)
                        assert [p['PolicyName'] for p in iam.list_policies(Scope='Local')['Policies']] == [sentinel_name]
                        native('Refresh', 'ListPolicies')
                        expect(page.get_by_role('link', name=sentinel_name, exact=True)).to_be_visible()
                        iam.delete_policy(PolicyArn=sentinel_policy['Arn'])
                        native('Refresh', 'ListPolicies')
                        expect(page.get_by_role('link', name=sentinel_name, exact=True)).to_have_count(0)
                        assert iam.list_policies(Scope='Local')['Policies'] == []
                        expect(page.get_by_role('heading', name='Customer managed policies', exact=True)).to_be_visible()
                        audit('policy-list-cancelled-read')
                        page.get_by_role('button', name='Create policy', exact=True).click()
                        page.get_by_label('Permissions policy JSON', exact=True).fill(json.dumps(allow, ensure_ascii=False))
                        before = list(operations); language('ja')
                        expect(page.get_by_label('許可ポリシー JSON', exact=True)).to_have_value(json.dumps(allow, ensure_ascii=False))
                        assert operations == before, 'Locale switch changed native IAM operations'
                        audit('policy-json-ja')
                        page.get_by_role('button', name='次へ', exact=True).click()
                        page.get_by_label('ポリシー名', exact=True).fill(name)
                        language('en'); expect(page.get_by_label('Policy name', exact=True)).to_have_value(name)
                        audit('policy-review-en')
                        native('Create policy', 'CreatePolicy')
                        assert iam.get_policy(PolicyArn=arn)['Policy']['DefaultVersionId'] == 'v1'
                        assert iam.get_policy_version(PolicyArn=arn, VersionId='v1')['PolicyVersion']['Document'] == allow
                        assert peer.get_policy_version(PolicyArn=peer_policy['Arn'], VersionId='v1')['PolicyVersion']['Document'] == deny
                        refused(lambda: as_role.get_object(Bucket=bucket, Key='object'))
                        page.get_by_role('button', name='Roles', exact=True).click()
                        page.get_by_role('link', name=role_name, exact=True).click()
                        expect(page.get_by_role('button', name='Create inline policy', exact=True)).to_be_visible()
                        # The attachment catalog is a separate real read/epoch.
                        # Its late response must not leave role metadata loading.
                        held_catalog = []
                        def attachment_catalog(route):
                            if action(route.request) == 'ListPolicies':
                                held_catalog.append(route); return True
                            return False
                        intercept[0] = attachment_catalog
                        page.get_by_role('button', name='Add permissions', exact=True).click()
                        page.get_by_role('menuitem', name='Attach policies', exact=True).click()
                        deadline = time.monotonic() + 5
                        while not held_catalog:
                            assert time.monotonic() < deadline
                            page.wait_for_timeout(10)
                        assert len(held_catalog) == 1
                        page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
                        with page.expect_response(lambda response: action(response.request) == 'ListPolicies') as cancelled_catalog:
                            held_catalog.pop().continue_()
                        assert cancelled_catalog.value.status == 200
                        intercept[0] = None
                        expect(page.get_by_role('dialog')).to_have_count(0)
                        expect(page.get_by_role('button', name='Refresh', exact=True)).to_be_enabled()
                        assert operations.count('AttachRolePolicy') == 0
                        assert iam.list_attached_role_policies(RoleName=role_name)['AttachedPolicies'] == []
                        native('Refresh', 'ListAttachedRolePolicies')
                        audit('role-cancelled-catalog')
                        page.get_by_role('button', name='Add permissions', exact=True).click()
                        page.get_by_role('menuitem', name='Attach policies', exact=True).click()
                        dialog = page.get_by_role('dialog')
                        expect(dialog.get_by_role('heading', name='Attach policies', exact=True)).to_be_visible()
                        dialog.get_by_role('radio', name='Attach ' + name, exact=True).check()
                        audit('role-attach')
                        native('Attach policies', 'AttachRolePolicy', dialog)
                        assert iam.list_attached_role_policies(RoleName=role_name)['AttachedPolicies'] == [{'PolicyName': name, 'PolicyArn': arn}]
                        assert as_role.get_object(Bucket=bucket, Key='object')['Body'].read() == payload
                        # Separate inline permission must survive managed detachment; v2 Deny overrides it.
                        iam.put_role_policy(RoleName=role_name, PolicyName='independent-inline', PolicyDocument=json.dumps(allow))
                        page.get_by_role('button', name='Policies', exact=True).click()
                        page.get_by_role('link', name=name, exact=True).click()
                        page.get_by_role('link', name='v1', exact=True).click()
                        expect(page.get_by_label('Version JSON', exact=True)).to_have_value(json.dumps(allow, indent=2))
                        page.get_by_role('button', name='Create version', exact=True).click()
                        page.get_by_label('Permissions policy JSON', exact=True).fill(json.dumps(deny))
                        language('ja'); expect(page.get_by_label('許可ポリシー JSON', exact=True)).to_have_value(json.dumps(deny))
                        language('en'); audit('version-draft')
                        fixture = {'armed': False, 'aborts': 0}
                        def ack_seen(response):
                            if action(response.request) == 'CreatePolicyVersion' and response.status == 200:
                                fixture['armed'] = True
                        def metadata_failure(route):
                            if action(route.request) == 'GetPolicy' and fixture['armed']:
                                fixture['armed'] = False; fixture['aborts'] += 1; route.abort('failed'); return True
                            return False
                        page.on('response', ack_seen); intercept[0] = metadata_failure
                        count = operations.count('CreatePolicyVersion')
                        native('Create version', 'CreatePolicyVersion')
                        expect(page.get_by_role('button', name='Retry read', exact=True)).to_be_visible()
                        expect(page.get_by_label('Permissions policy JSON', exact=True)).to_have_count(0)
                        assert fixture['aborts'] == 1
                        assert len(iam.list_policy_versions(PolicyArn=arn)['Versions']) == 2
                        assert iam.get_policy(PolicyArn=arn)['Policy']['DefaultVersionId'] == 'v1'
                        native('Retry read', 'GetPolicy')
                        expect(page.get_by_role('radio', name='Select version v2', exact=True)).to_be_visible()
                        assert operations.count('CreatePolicyVersion') == count + 1, 'Read retry replayed non-idempotent version create'
                        intercept[0] = None; page.remove_listener('response', ack_seen)
                        assert as_role.get_object(Bucket=bucket, Key='object')['Body'].read() == payload
                        page.get_by_role('radio', name='Select version v2', exact=True).check()
                        page.get_by_role('button', name='Set as default', exact=True).click()
                        native('Confirm', 'SetDefaultPolicyVersion', page.get_by_role('dialog'))
                        assert iam.get_policy(PolicyArn=arn)['Policy']['DefaultVersionId'] == 'v2'
                        refused(lambda: as_role.get_object(Bucket=bucket, Key='object'))
                        page.get_by_role('radio', name='Select version v2', exact=True).check()
                        expect(page.get_by_role('button', name='Delete version', exact=True)).to_be_disabled()
                        page.get_by_role('radio', name='Select version v1', exact=True).check()
                        page.get_by_role('button', name='Set as default', exact=True).click()
                        native('Confirm', 'SetDefaultPolicyVersion', page.get_by_role('dialog'))
                        assert as_role.get_object(Bucket=bucket, Key='object')['Body'].read() == payload
                        page.get_by_role('radio', name='Select version v2', exact=True).check()
                        page.get_by_role('button', name='Delete version', exact=True).click()
                        native('Confirm', 'DeletePolicyVersion', page.get_by_role('dialog'))
                        assert [v['VersionId'] for v in iam.list_policy_versions(PolicyArn=arn)['Versions']] == ['v1']
                        page.get_by_role('button', name='Delete policy', exact=True).click()
                        page.get_by_label('Confirm policy name', exact=True).fill(name)
                        native('Confirm', 'DeletePolicy', page.get_by_role('dialog'), status=409)
                        assert iam.get_policy(PolicyArn=arn)['Policy']['AttachmentCount'] == 1
                        assert iam.list_attached_role_policies(RoleName=role_name)['AttachedPolicies'][0]['PolicyArn'] == arn
                        page.get_by_role('dialog').get_by_role('button', name='Cancel', exact=True).click()
                        # Release a real late version read after leaving its policy; it must not repopulate.
                        held = []
                        def hold_version(route):
                            if action(route.request) == 'GetPolicyVersion':
                                held.append(route); return True
                            return False
                        intercept[0] = hold_version
                        page.get_by_role('link', name='v1', exact=True).click()
                        deadline = time.monotonic() + 5
                        while not held:
                            assert time.monotonic() < deadline
                            page.wait_for_timeout(10)
                        page.get_by_role('button', name='Policies', exact=True).click()
                        with page.expect_response(lambda response: action(response.request) == 'GetPolicyVersion') as late:
                            held.pop().continue_()
                        assert late.value.status == 200
                        intercept[0] = None
                        expect(page.get_by_role('heading', name='Customer managed policies', exact=True)).to_be_visible()
                        expect(page.get_by_label('Version JSON', exact=True)).to_have_count(0)
                        audit('policy-list-after-stale-read')
                        page.get_by_role('button', name='Roles', exact=True).click()
                        page.get_by_role('link', name=role_name, exact=True).click()
                        page.get_by_role('radio', name='Select policy ' + name, exact=True).check()
                        page.get_by_role('button', name='Detach', exact=True).click()
                        native('Detach', 'DetachRolePolicy', page.get_by_role('dialog'))
                        assert iam.list_attached_role_policies(RoleName=role_name)['AttachedPolicies'] == []
                        assert as_role.get_object(Bucket=bucket, Key='object')['Body'].read() == payload
                        assert iam.get_role_policy(RoleName=role_name, PolicyName='independent-inline')['PolicyDocument'] == allow
                        iam.delete_role_policy(RoleName=role_name, PolicyName='independent-inline')
                        refused(lambda: as_role.get_object(Bucket=bucket, Key='object'))
                        page.get_by_role('button', name='Policies', exact=True).click()
                        page.get_by_role('link', name=name, exact=True).click()
                        page.get_by_role('button', name='Delete policy', exact=True).click()
                        page.get_by_label('Confirm policy name', exact=True).fill(name)
                        native('Confirm', 'DeletePolicy', page.get_by_role('dialog'))
                        assert iam.list_policies(Scope='Local')['Policies'] == []
                        assert set(operations) == OPERATIONS
                        assert operations.count('CreatePolicy') == 1 and operations.count('CreatePolicyVersion') == 1
                        assert all(status == 200 or (op == 'DeletePolicy' and status == 409)
                                   for op, status in responses)
                        assert failed_reads == ['GetPolicy'], 'Unexpected failed native request beyond the explicit read-abort fixture'
                        assert len(responses) + len(failed_reads) == len(operations), 'Browser IAM transport was not balanced after accounting for the single explicit aborted read'
                        assert peer.get_policy_version(PolicyArn=peer_policy['Arn'], VersionId='v1')['PolicyVersion']['Document'] == deny
                        assert peer_objects.get_object(Bucket=bucket, Key='object')['Body'].read() == sentinel
                        assert peer.list_attached_role_policies(RoleName=role_name)['AttachedPolicies'] == []
                        assert not errors and not outside
                        completed.append({'width': width, 'operations': sorted(set(operations)),
                                          'postACKReadAbort': fixture['aborts'], 'versionCreateCalls': 1,
                                          'initialListCancelAndAttachCatalogCancel': True,
                                          'inlineAndManagedUnionDeny': True, 'staleReadDidNotPublish': True})
                        context.close(); context = None
                        iam.delete_role(RoleName=role_name); peer.delete_policy(PolicyArn=peer_policy['Arn']); peer.delete_role(RoleName=role_name)
                        objects.delete_object(Bucket=bucket, Key='object'); objects.delete_bucket(Bucket=bucket)
                        peer_objects.delete_object(Bucket=bucket, Key='object'); peer_objects.delete_bucket(Bucket=bucket)
                    assert all(c['calls'] > 0 and c['calls'] == c['sends'] == c['responses'] for c in sdk_counts)
                    assert iam.list_policies(Scope='Local')['Policies'] == []
                    assert peer.list_policies(Scope='Local')['Policies'] == []
                    report = {'actualEmbeddedUI': True, 'actualTwoCLIAndDBs': True,
                              'viewports': [1440, 390], 'flows': completed,
                              'axeStates': screens, 'SDKCounts': sdk_counts,
                              'crossToken401': True, 'peerPreserved': True,
                              'outsideRequests': 0, 'pageErrors': 0, 'cleanup': 'DELETED',
                              'actualAWS': False, 'authenticatedAWSScreenObserved': False}
                    (REPORTS / 'iam-managed-console.json').write_text(json.dumps(report, indent=2) + '\n')
                    print(json.dumps(report))
                finally:
                    if context is not None: context.close()
                    browser.close()
        finally:
            try: a.stop()
            finally: b.stop()


if __name__ == '__main__':
    run()
