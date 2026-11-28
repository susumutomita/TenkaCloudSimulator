"""Opt-in genuine Cloudscape account quota/request workflow; no cloud endpoints.

Uses the unchanged ordinary SDK environment and two owned persistent CLIs.
A lost acknowledgement is deliberately injected only after the original native
response arrives. No response is fabricated and no write is replayed.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import sys
import tempfile
from urllib.parse import urlsplit

import boto3
from botocore.config import Config
from playwright.sync_api import sync_playwright, expect
from service_quotas_discovery import Team, KEY, participant


def client(team):
    sdk = boto3.client('service-quotas', endpoint_url=team.endpoint, region_name='us-east-1',
                       aws_access_key_id='TCSIMQUOTACONSOLE', aws_secret_access_key='synthetic-local-only',
                       config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=2, read_timeout=5))
    sdk.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', team.token))
    def local(request, **_):
        url = urlsplit(request.url)
        assert (url.scheme, url.netloc, url.path, url.query) == ('http', urlsplit(team.endpoint).netloc, '/', '')
        assert request.method == 'POST'
    sdk.meta.events.register('before-send', local)
    return sdk


def frame(team):
    with sqlite3.connect(team.directory / 'state.db') as db:
        return tuple(db.execute('SELECT * FROM aws_b2bi_profile_quota_requests ORDER BY seq'))


def full_frame(team):
    with sqlite3.connect(team.directory / 'state.db') as db:
        return tuple(db.iterdump())


def cleanup(call):
    primary = sys.exception()
    try:
        call()
    except BaseException as error:
        if primary is None:
            raise
        primary.add_note('Owned cleanup also failed: ' + type(error).__name__)


class Console:
    def __init__(self, browser, team, token=None, japanese=False):
        self.context = browser.new_context(viewport={'width': 390 if japanese else 1440, 'height': 1000})
        self.page = self.context.new_page()
        self.page.set_default_timeout(15000)
        self.requests, self.errors, self.outside = [], [], []
        selected_principal = [None]
        self.page.on('pageerror', lambda _: self.errors.append(True))
        def route(value):
            url = urlsplit(value.request.url)
            if (url.scheme, url.netloc) == ('http', urlsplit(team.endpoint).netloc):
                value.continue_()
            else:
                self.outside.append(True)
                value.abort()
        self.page.route('**/*', route)
        def wire(request):
            target = request.headers.get('x-amz-target', '')
            if target.startswith('ServiceQuotasV20190624.'):
                assert request.url == team.endpoint + '/' and request.method == 'POST'
                assert request.headers['content-type'] == 'application/x-amz-json-1.1'
                assert request.headers['x-simulator-region'] == 'us-east-1'
                assert request.headers.get('cookie')
                assert selected_principal[0] and request.headers.get('x-simulator-principal') == selected_principal[0]
                assert not {'authorization', 'x-simulator-token', 'x-simulator-access-role'} & request.headers.keys()
                self.requests.append((target.split('.')[-1], request.post_data_json))
        self.page.on('request', wire)
        self.page.goto(team.endpoint + '/_simulator/console/#/catalog')
        self.page.get_by_label('Access token', exact=True).fill(token or team.token)
        self.page.get_by_role('button', name='Open sandbox', exact=True).click()
        self.page.wait_for_url('**/_simulator/console/**')
        capabilities = self.page.evaluate("async () => {const response=await fetch('/_simulator/v1/capabilities');if(!response.ok)throw Error('capabilities failed');return response.json();}")
        selected_principal[0] = capabilities['principal'] or ('simulator-admin' if capabilities['identitySelection'] else None)
        assert selected_principal[0]
        self.page.get_by_role('button', name='Services', exact=True).click()
        self.page.get_by_role('menuitem', name='Service catalog', exact=True).click()
        self.page.get_by_role('button', name=re.compile('^Catalog service')).click()
        self.page.get_by_role('option', name=re.compile('^Service Quotas')).click()
        expect(self.page.get_by_role('heading', name='Service Quotas', exact=True)).to_be_visible()
        if japanese:
            self.page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
            expect(self.page.locator('html')).to_have_attribute('lang', 'ja')

    def click(self, label, operation, status=200):
        with self.page.expect_response(lambda response: response.request.headers.get('x-amz-target') == 'ServiceQuotasV20190624.' + operation) as reply:
            self.page.get_by_role('button', name=label, exact=True).click()
        assert reply.value.status == status
        return reply.value

    def close(self):
        try:
            assert not self.outside and not self.errors
        finally:
            self.context.close()


def audit(page):
    script = Path(os.environ['AXE_SCRIPT'])
    provenance = json.loads((script.parent / 'provenance.json').read_text())
    assert hashlib.sha256(script.read_bytes()).hexdigest() == provenance['bundleSha256']
    page.evaluate(script.read_text())
    result = page.evaluate("async () => await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
    assert not result['violations'], [item['id'] for item in result['violations']]
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth + 1')


def run():
    # boto3.Session deliberately preserves inherited profile/configuration guards.
    boto3.Session()
    directory = tempfile.TemporaryDirectory(prefix='tenka-quota-console-')
    owner, peer = Team(Path(directory.name) / 'owner'), Team(Path(directory.name) / 'peer')
    for team in (owner, peer):
        team.directory.mkdir()
    sdks = []
    try:
        owner.start(); peer.start()
        sdk, other = client(owner), client(peer)
        sdks.extend((sdk, other))
        other.request_service_quota_increase(**KEY, DesiredValue=11, SupportCaseAllowed=False)
        peer_before = full_frame(peer)
        preexisting = sdk.request_service_quota_increase(**KEY, DesiredValue=8, SupportCaseAllowed=False)['RequestedQuota']['Id']
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(executable_path=os.environ['CHROMIUM_EXECUTABLE'], chromium_sandbox=True)
            try:
                view = Console(browser, owner)
                try:
                    page = view.page
                    expect(page.get_by_text('Published AWS default', exact=True)).to_be_visible()
                    expect(page.get_by_text('Applied account capacity', exact=True)).to_be_visible()
                    audit(page)
                    desired_control = page.get_by_role('spinbutton', name='Desired value', exact=True)
                    desired_control.focus()
                    page.keyboard.press('Tab')
                    expect(page.get_by_role('checkbox', name='Allow support case (stored PENDING locally only)', exact=True)).to_be_focused()
                    assert sdk.get_service_quota(**KEY)['Quota']['Value'] == 5
                    ids = {preexisting}
                    for desired in ('8', '9'):
                        page.get_by_role('spinbutton', name='Desired value', exact=True).fill(desired)
                        view.click('Submit increase request', 'RequestServiceQuotaIncrease')
                        current = sdk.list_requested_service_quota_change_history_by_quota(**KEY)['RequestedQuotas']
                        added = {item['Id'] for item in current} - ids
                        assert len(added) == 1
                        identifier = added.pop(); ids.add(identifier)
                        expect(page.get_by_text(identifier, exact=True)).to_be_visible()
                        assert sdk.get_requested_service_quota_change(RequestId=identifier)['RequestedQuota']['Status'] == 'NOT_APPROVED'
                    # Cross the genuine served Scalar reference in both directions.
                    # This helper is owned separately and joins the product at integration.
                    from service_quotas_scalar import FixtureStack, ScalarReference, sdk_record_equals
                    with FixtureStack() as scalar_stack:
                        reference = ScalarReference(browser, owner, owner.token, scalar_stack)
                        via_scalar = reference.send('GetRequestedServiceQuotaChange', {'RequestId': identifier})['RequestedQuota']
                        sdk_record_equals(via_scalar, sdk.get_requested_service_quota_change(RequestId=identifier)['RequestedQuota'])
                        scalar_saved = reference.send('RequestServiceQuotaIncrease', {**KEY, 'DesiredValue': 8, 'SupportCaseAllowed': False})['RequestedQuota']
                        scalar_id = scalar_saved['Id']
                        sdk_record_equals(scalar_saved, sdk.get_requested_service_quota_change(RequestId=scalar_id)['RequestedQuota'])
                    page.get_by_role('textbox', name='Request ID', exact=True).fill(scalar_id)
                    view.click('Get request', 'GetRequestedServiceQuotaChange')
                    expect(page.get_by_text(scalar_id, exact=True)).to_be_visible()
                    page.get_by_role('textbox', name='Request ID', exact=True).fill(identifier)
                    view.click('Get request', 'GetRequestedServiceQuotaChange')
                    expect(page.get_by_text(identifier, exact=True)).to_be_visible()
                    view.click('Refresh history', 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    view.click('Next history page', 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    assert any(body.get('NextToken') for op, body in view.requests if op == 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    view.click('Previous history page', 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    # Original request commits; only its acknowledgement to this view is lost.
                    page.evaluate("""() => {const original=window.fetch; window.__quotaLost={original}; window.fetch=async function(input,options){const response=await original.apply(this,arguments); if(options?.headers?.['X-Amz-Target']==='ServiceQuotasV20190624.RequestServiceQuotaIncrease'){window.fetch=original; window.__quotaLost.status=response.status; throw new TypeError('deliberately lost local acknowledgement');} return response;};} """)
                    # A filtered RESOURCE page must never unlock unknown-write recovery.
                    page.get_by_role('button', name=re.compile('^QuotaRequestedAtLevel')).click()
                    page.get_by_role('option', name='RESOURCE', exact=True).click()
                    before = frame(owner)
                    page.get_by_role('spinbutton', name='Desired value', exact=True).fill('8')
                    view.click('Submit increase request', 'RequestServiceQuotaIncrease')
                    expect(page.get_by_text('Request outcome unknown', exact=True)).to_be_visible()
                    audit(page)
                    page.get_by_role('combobox', name='Language', exact=True).select_option('ja')
                    page.set_viewport_size({'width': 390, 'height': 1000})
                    expect(page.get_by_text('リクエスト結果が不明です', exact=True)).to_be_visible()
                    audit(page)
                    page.get_by_role('combobox', name='言語', exact=True).select_option('en')
                    page.set_viewport_size({'width': 1440, 'height': 1000})
                    expect(page.get_by_role('button', name='Submit increase request', exact=True)).to_be_disabled()
                    after = frame(owner)
                    assert len(after) == len(before) + 1
                    assert page.evaluate('window.__quotaLost.status') == 200
                    view.click('Refresh history', 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    expect(page.get_by_role('button', name='Start a separate new request', exact=True)).to_have_count(0)
                    assert view.requests[-1][1]['QuotaRequestedAtLevel'] == 'RESOURCE'
                    view.click('Refresh same-scope history', 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    assert view.requests[-1][1] == {**KEY, 'MaxResults': 1, 'QuotaRequestedAtLevel': 'ACCOUNT'}
                    assert frame(owner) == after
                    page.get_by_role('button', name='Start a separate new request', exact=True).click()
                    page.get_by_role('checkbox', name='Allow support case (stored PENDING locally only)', exact=True).check()
                    view.click('Submit increase request', 'RequestServiceQuotaIncrease')
                    pending = [item for item in sdk.list_requested_service_quota_change_history_by_quota(**KEY)['RequestedQuotas'] if item['Status'] == 'PENDING']
                    assert len(pending) == 1
                    expect(page.get_by_text(pending[0]['Id'], exact=True)).to_be_visible()
                    before = frame(owner)
                    view.click('Submit increase request', 'RequestServiceQuotaIncrease', 400)
                    assert frame(owner) == before and sdk.get_service_quota(**KEY)['Quota']['Value'] == 5
                    assert len([op for op, _ in view.requests if op == 'RequestServiceQuotaIncrease']) == 5
                    page.get_by_role('button', name=re.compile('^Status filter')).click()
                    page.get_by_role('option', name='PENDING', exact=True).click()
                    view.click('Refresh history', 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    expect(page.get_by_role('link', name=pending[0]['Id'], exact=True)).to_be_visible()
                    assert view.requests[-1][1]['Status'] == 'PENDING'
                    page.get_by_role('button', name=re.compile('^History scope')).click()
                    page.get_by_role('option', name='Service', exact=True).click()
                    view.click('Refresh history', 'ListRequestedServiceQuotaChangeHistory')
                    assert view.requests[-1][1]['Status'] == 'PENDING'
                    # Hold the genuine native read's original Response while leaving
                    # this selected service. Its real AbortSignal must be canceled;
                    # neither late success nor its resulting AbortError may repaint.
                    page.evaluate("""() => {
                      const original=window.fetch;
                      window.__quotaRead={original,ready:false,aborted:false};
                      window.fetch=async function(input,options){
                        const response=await original.apply(this,arguments);
                        if(options?.headers?.['X-Amz-Target']==='ServiceQuotasV20190624.GetServiceQuota'){
                          window.fetch=original;
                          window.__quotaRead.status=response.status;
                          window.__quotaRead.ready=true;
                          options.signal.addEventListener('abort',()=>window.__quotaRead.aborted=true,{once:true});
                          await new Promise(resolve=>window.__quotaRead.release=resolve);
                          return response;
                        }
                        return response;
                      };
                    }""")
                    page.get_by_role('button', name='Refresh quota', exact=True).click()
                    page.wait_for_function('() => window.__quotaRead.ready')
                    assert page.evaluate('window.__quotaRead.status') == 200
                    page.get_by_role('button', name=re.compile('^Catalog service')).click()
                    page.get_by_role('option', name=re.compile('^Amazon Elastic Container Registry')).click()
                    page.wait_for_function('() => window.__quotaRead.aborted')
                    page.evaluate('window.__quotaRead.release()')
                    page.evaluate('() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))')
                    expect(page.get_by_role('heading', name='Service Quotas', exact=True)).to_have_count(0)
                    expect(page.get_by_text(pending[0]['Id'], exact=True)).to_have_count(0)
                    page.get_by_role('button', name=re.compile('^Catalog service')).click()
                    page.get_by_role('option', name=re.compile('^Service Quotas')).click()
                    expect(page.get_by_role('button', name='Submit increase request', exact=True)).to_be_enabled()
                finally:
                    cleanup(view.close)
                owner.stop(); owner.start()
                reopened = Console(browser, owner, japanese=True)
                try:
                    reopened.click('履歴を更新', 'ListRequestedServiceQuotaChangeHistoryByQuota')
                    audit(reopened.page)
                    expect(reopened.page.get_by_text('適用済みアカウント上限', exact=True)).to_be_visible()
                    assert sdk.get_requested_service_quota_change(RequestId=pending[0]['Id'])['RequestedQuota']['Status'] == 'PENDING'
                finally:
                    cleanup(reopened.close)
                arn = 'arn:aws:servicequotas:us-east-1:000000000000:b2bi/L-05F6A9EF'
                scoped = participant(owner, 'quota-named-reader', [{'Effect': 'Allow', 'Action': ['servicequotas:GetServiceQuota'], 'Resource': arn}])
                restricted = Console(browser, owner, token=scoped)
                try:
                    expect(restricted.page.get_by_role('button', name='Submit increase request', exact=True)).to_be_enabled()
                    expect(restricted.page.get_by_text('GetAWSDefaultServiceQuota:', exact=False)).to_be_visible()
                    before_denial = frame(owner)
                    restricted.click('Submit increase request', 'RequestServiceQuotaIncrease', 400)
                    assert frame(owner) == before_denial
                    expect(restricted.page.get_by_text('Request outcome unknown', exact=True)).to_have_count(0)
                    expect(restricted.page.get_by_role('button', name='Submit increase request', exact=True)).to_be_disabled()
                    restricted.click('Refresh quota', 'GetServiceQuota')
                    expect(restricted.page.get_by_role('button', name='Submit increase request', exact=True)).to_be_enabled()
                    owner.control('PUT', '/_simulator/v1/iam/principals/quota-named-reader/policy', {'Version': '2012-10-17', 'Statement': [{'Effect': 'Deny', 'Action': '*', 'Resource': '*'}]})
                    restricted.click('Refresh quota', 'GetServiceQuota', 400)
                    expect(restricted.page.get_by_role('button', name='Submit increase request', exact=True)).to_be_disabled()
                    expect(restricted.page.get_by_text('Not loaded', exact=True)).to_have_count(2)
                    audit(restricted.page)
                    expect(restricted.page.get_by_text(pending[0]['Id'], exact=True)).to_have_count(0)
                finally:
                    cleanup(restricted.close)
                assert full_frame(peer) == peer_before
            finally:
                cleanup(browser.close)
    finally:
        for sdk in sdks:
            cleanup(sdk.close)
        try:
            cleanup(owner.stop)
        finally:
            try:
                cleanup(peer.stop)
            finally:
                cleanup(directory.cleanup)
    assert not Path(directory.name).exists()
    print(json.dumps({'ok': True, 'serviceQuotasConsole': True, 'physicalHistoryAndLostAcknowledgement': True,
                      'peerRestartCleanup': True, 'japaneseWidth': 390,
                      'binarySha256': hashlib.sha256(Path(os.environ['SIMULATOR_BINARY']).read_bytes()).hexdigest()}))


if __name__ == '__main__':
    run()
