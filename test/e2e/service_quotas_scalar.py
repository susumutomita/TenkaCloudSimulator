"""Nine genuine Scalar operations on two disposable local quota authorities.

Run with the pinned stock SDK, sandboxed Chromium and an exact local binary.
Inherited SDK bindings are retained. No request body selects a native operation.
"""
from contextlib import ExitStack
from datetime import datetime
from importlib.metadata import version
import json
import os
import re
import tempfile
from pathlib import Path
from urllib.parse import urlsplit

import boto3
from botocore.config import Config
from playwright.sync_api import expect, sync_playwright

from service_quotas_discovery import Team, KEY, DEFAULT, participant, refused


MEDIA = 'application/x-amz-json-1.1'
PREFIX = 'ServiceQuotasV20190624.'
OPERATIONS = {
    'GetAWSDefaultServiceQuota', 'GetRequestedServiceQuotaChange', 'GetServiceQuota',
    'ListAWSDefaultServiceQuotas', 'ListRequestedServiceQuotaChangeHistory',
    'ListRequestedServiceQuotaChangeHistoryByQuota', 'ListServiceQuotas', 'ListServices',
    'RequestServiceQuotaIncrease',
}


class FixtureStack(ExitStack):
    def __exit__(self, exception_type, exception, traceback):
        self.primary = exception
        return super().__exit__(exception_type, exception, traceback)

    def callback(self, callback, /, *args, **kwargs):
        def protected():
            try:
                callback(*args, **kwargs)
            except BaseException as error:
                if self.primary is None:
                    self.primary = error
                    raise
                self.primary.add_note('Owned cleanup failed: ' + type(error).__name__)
        return super().callback(protected)


def quota_sdk(team, stack):
    client = boto3.client('service-quotas', endpoint_url=team.endpoint, region_name='us-east-1',
                          aws_access_key_id='TCSIMQUOTASCALAR', aws_secret_access_key='fixture-local-only',
                          config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2, read_timeout=10))
    stack.callback(client.close)
    client.meta.events.register('before-sign.service-quotas',
                                lambda request, **_: request.headers.add_header('X-Simulator-Token', team.token))

    def signed_local(request, **_):
        assert request.method == 'POST' and request.url == team.endpoint + '/'
        target = request.headers['X-Amz-Target']
        if isinstance(target, bytes):
            target = target.decode('ascii')
        assert target in {PREFIX + operation for operation in OPERATIONS}
        authorization = request.headers['Authorization']
        if isinstance(authorization, bytes):
            authorization = authorization.decode('ascii')
        assert authorization.startswith('AWS4-HMAC-SHA256 ')
        assert '/us-east-1/servicequotas/aws4_request' in authorization
    client.meta.events.register('before-send.service-quotas', signed_local)
    return client


class ScalarReference:
    """Reusable real client for a UI fixture's actual returned request ID."""
    def __init__(self, browser, team, token, stack, viewport=None, locale='en-US'):
        self.team = team
        self.context = browser.new_context(locale=locale, viewport=viewport or {'width': 1440, 'height': 1000},
                                           service_workers='block')
        stack.callback(self.context.close)
        self.outside, self.errors, self.sends = [], [], []

        def local_only(route):
            url = urlsplit(route.request.url)
            if (url.scheme, url.netloc) == ('http', f'127.0.0.1:{team.port}'):
                route.continue_()
            else:
                self.outside.append(url.hostname)
                route.abort()
        self.context.route('**/*', local_only)
        response = self.context.request.post(team.endpoint + '/_simulator/login', form={'token': token}, max_redirects=0)
        assert response.status == 303
        response.dispose()

    def send(self, operation, payload, status=200):
        assert operation in OPERATIONS
        page = self.context.new_page()
        observed = []
        try:
            page.on('pageerror', lambda error: self.errors.append(type(error).__name__))
            page.on('request', lambda request: observed.append(request) if request.method == 'POST' else None)
            url = self.team.endpoint + '/_simulator/docs/catalog/service-quotas.json'
            with page.expect_response(lambda response: response.url == url) as loaded:
                page.goto(self.team.endpoint + '/_simulator/docs/service-quotas.html')
            spec = loaded.value.json()['paths']['/']['post']
            examples = spec['requestBody']['content'][MEDIA]['examples']
            assert set(examples) == OPERATIONS
            target = PREFIX + operation
            assert target in next(p for p in spec['parameters'] if p['name'] == 'X-Amz-Target')['schema']['enum']
            # Scalar decorates the operation's accessible name with its HTTP
            # method. Bind both its exact native route and declared summary.
            operation_link = page.locator('a[href="#tag/service-quotas/POST/"]').filter(has_text=spec['summary'])
            expect(operation_link).to_have_count(1)
            operation_link.click()
            page.get_by_test_id('example-picker').click()
            page.get_by_role('option', name=examples[operation]['summary'], exact=True).click()
            page.get_by_role('button', name='Test Request (post /)', exact=True).click()
            dialog = page.get_by_role('dialog').filter(has=page.get_by_role('button', name='Close Client', exact=True))
            request = dialog.get_by_role('region', name='Request: ' + spec['summary'], exact=True)
            request.get_by_role('tab', name='Headers', exact=True).press('Enter')
            row = request.get_by_role('row', name=re.compile(r'^Include X-Amz-Target in request'))
            row.get_by_role('button', name=re.compile(r'^ServiceQuotasV20190624\.')).click()
            page.get_by_role('menuitem', name=target, exact=True).click()
            expect(row.get_by_role('checkbox')).to_be_checked()
            request.get_by_role('tab', name='Body', exact=True).press('Enter')
            editor = request.locator('.cm-content[contenteditable=true]:visible')
            assert json.loads(editor.inner_text()) == examples[operation]['value']
            editor.click()
            editor.press('ControlOrMeta+a')
            page.keyboard.insert_text(json.dumps(payload))
            assert json.loads(editor.inner_text()) == payload and not observed
            with page.expect_response(lambda response: response.request.method == 'POST') as sent:
                dialog.get_by_role('button', name=re.compile(r'^Send post request to ')).click()
            response = sent.value
            assert observed == [response.request]
            assert response.url == self.team.endpoint + '/' and response.status == status, (operation, response.status)
            assert response.request.post_data_json == payload
            assert response.request.redirected_from is None and response.request.redirected_to is None
            headers = response.request.all_headers()
            assert headers['x-amz-target'] == target and headers['content-type'].split(';', 1)[0] == MEDIA
            assert headers.get('cookie') and not {
                'authorization', 'x-simulator-token', 'x-simulator-principal', 'x-simulator-access-role',
                'x-simulator-region', 'x-simulator-service',
            } & headers.keys()
            result = response.json()
            panel = dialog.locator('.response-section-content-body').filter(
                has=page.get_by_role('link', name='Download Response Body', exact=True))
            expect(panel).to_have_count(1)
            expect(panel).to_contain_text(next(iter(result)))
            if 'RequestedQuota' in result:
                expect(panel).to_contain_text(result['RequestedQuota']['Id'])
            if '__type' in result:
                expect(panel).to_contain_text(result['__type'])
            assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'reference overflowed its viewport'
            self.sends.append((operation, status))
            assert not self.outside and not self.errors
            return result
        finally:
            page.close()


def sdk_record_equals(scalar, sdk):
    assert set(scalar) == set(sdk) and 'CaseId' not in scalar
    for key, value in scalar.items():
        if isinstance(sdk[key], datetime):
            assert type(value) in (int, float) and value > 0
            assert abs(value - sdk[key].timestamp()) < 0.000001
        else:
            assert value == sdk[key], key


def exercise(browser, owner, peer, stack):
    sdk, peer_sdk = quota_sdk(owner, stack), quota_sdk(peer, stack)
    actor = ScalarReference(browser, owner, owner.token, stack)
    # A real peer record makes isolation stronger than an empty second ledger.
    peer_record = peer_sdk.request_service_quota_increase(**KEY, DesiredValue=7, SupportCaseAllowed=False)['RequestedQuota']
    original = sdk.get_service_quota(**KEY)['Quota']
    assert original['Value'] == 5
    profiles = owner.client('b2bi')
    stack.callback(profiles.close)
    profile_ids = {profiles.create_profile(name=f'scalar-profile-{n}', businessName='Quota fixture',
                                           phone='+1 555 0100', logging='DISABLED')['profileId'] for n in range(5)}
    assert len(profile_ids) == 5
    assert actor.send('ListServices', {})['Services'] == [{'ServiceCode': 'b2bi', 'ServiceName': 'AWS B2B Data Interchange'}]
    assert actor.send('GetAWSDefaultServiceQuota', KEY)['Quota'] == DEFAULT
    assert actor.send('ListAWSDefaultServiceQuotas', {'ServiceCode': 'b2bi'})['Quotas'] == [DEFAULT]
    assert actor.send('ListServiceQuotas', {'ServiceCode': 'b2bi'})['Quotas'] == [original]
    assert actor.send('GetServiceQuota', KEY)['Quota'] == original
    false_request = {**KEY, 'DesiredValue': 8, 'SupportCaseAllowed': False}
    first = actor.send('RequestServiceQuotaIncrease', false_request)['RequestedQuota']
    second = actor.send('RequestServiceQuotaIncrease', false_request)['RequestedQuota']
    assert first['Id'] != second['Id'] and first['Status'] == second['Status'] == 'NOT_APPROVED'
    assert first['DesiredValue'] == second['DesiredValue'] == 8
    refused(profiles.create_profile, 'ServiceQuotaExceededException', name='scalar-overflow',
            businessName='Quota fixture', phone='+1 555 0100', logging='DISABLED')
    assert sdk.get_service_quota(**KEY)['Quota'] == original
    got = actor.send('GetRequestedServiceQuotaChange', {'RequestId': first['Id']})['RequestedQuota']
    assert got == first
    sdk_record_equals(first, sdk.get_requested_service_quota_change(RequestId=first['Id'])['RequestedQuota'])
    for operation, extra in [('ListRequestedServiceQuotaChangeHistory', {}),
                             ('ListRequestedServiceQuotaChangeHistoryByQuota', KEY)]:
        args = {**extra, 'Status': 'NOT_APPROVED', 'MaxResults': 1}
        page = actor.send(operation, args)
        assert len(page['RequestedQuotas']) == 1 and page['NextToken']
        next_page = actor.send(operation, {**args, 'NextToken': page['NextToken']})
        assert len(next_page['RequestedQuotas']) == 1 and 'NextToken' not in next_page
        assert {page['RequestedQuotas'][0]['Id'], next_page['RequestedQuotas'][0]['Id']} == {first['Id'], second['Id']}
    empty = actor.send('ListRequestedServiceQuotaChangeHistoryByQuota', {**KEY, 'QuotaRequestedAtLevel': 'RESOURCE'})
    assert empty['RequestedQuotas'] == [] and 'NextToken' not in empty
    # Deliberately mismatched header/body must reach native validation, never a
    # body-derived target rewrite that would turn this into a successful Get.
    mismatch = actor.send('ListServices', KEY, 400)
    assert mismatch['__type'] == 'IllegalArgumentException'
    arn = original['QuotaArn']
    limited_token = participant(owner, 'quota-scalar-named', [{
        'Effect': 'Allow', 'Action': ['servicequotas:GetServiceQuota', 'servicequotas:ListRequestedServiceQuotaChangeHistoryByQuota'], 'Resource': arn,
    }])
    limited = ScalarReference(browser, owner, limited_token, stack)
    assert limited.send('GetServiceQuota', KEY)['Quota'] == original
    assert limited.send('GetAWSDefaultServiceQuota', KEY, 400)['__type'] == 'AccessDeniedException'
    before = sdk.list_requested_service_quota_change_history()['RequestedQuotas']
    assert limited.send('RequestServiceQuotaIncrease', false_request, 400)['__type'] == 'AccessDeniedException'
    assert sdk.list_requested_service_quota_change_history()['RequestedQuotas'] == before
    other = ScalarReference(browser, peer, peer.token, stack)
    assert other.send('GetRequestedServiceQuotaChange', {'RequestId': first['Id']}, 400)['__type'] == 'NoSuchResourceException'
    assert peer_sdk.get_requested_service_quota_change(RequestId=peer_record['Id'])['RequestedQuota'] == peer_record
    owner.stop()
    owner.start()
    reopened_sdk = quota_sdk(owner, stack)
    fresh = ScalarReference(browser, owner, owner.token, stack, viewport={'width': 390, 'height': 844}, locale='ja-JP')
    assert fresh.send('GetRequestedServiceQuotaChange', {'RequestId': first['Id']})['RequestedQuota'] == first
    sdk_record_equals(first, reopened_sdk.get_requested_service_quota_change(RequestId=first['Id'])['RequestedQuota'])
    assert reopened_sdk.get_service_quota(**KEY)['Quota'] == original
    reopened_profiles = owner.client('b2bi')
    stack.callback(reopened_profiles.close)
    assert {p['profileId'] for p in reopened_profiles.list_profiles()['profiles']} == profile_ids
    assert peer_sdk.get_requested_service_quota_change(RequestId=peer_record['Id'])['RequestedQuota'] == peer_record
    assert {operation for operation, status in actor.sends if status == 200} == OPERATIONS
    return {'operations': sorted(OPERATIONS), 'ownerSends': len(actor.sends), 'distinctFalseRequests': 2,
            'stockSDKReadback': True, 'peerPreserved': True, 'reopen': True}


def main():
    assert version('boto3') == '1.42.0' and version('botocore') == '1.41.6'
    with tempfile.TemporaryDirectory(prefix='quota-scalar-') as temporary, FixtureStack() as stack:
        teams = []
        for name in ('owner', 'peer'):
            directory = Path(temporary) / name
            directory.mkdir()
            team = Team(directory)
            teams.append(team)
            stack.callback(team.stop)
            team.start()
        playwright = sync_playwright().start()
        stack.callback(playwright.stop)
        browser = playwright.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE'), chromium_sandbox=True)
        stack.callback(browser.close)
        receipt = exercise(browser, *teams, stack)
    assert not Path(temporary).exists()
    print(json.dumps({**receipt, 'cleanup': True}))


if __name__ == '__main__':
    main()
