"""Read-only oracle for the dedicated quota view inside the full catalog sweep."""
import re
import sqlite3
from playwright.sync_api import expect


def database_frame(database):
    with sqlite3.connect(database) as connection:
        return tuple(connection.iterdump())


def check_service_quotas_catalog_view(page, team, descriptor, database):
    assert descriptor['model'] == 'service-quotas'
    assert [resource['kind'] for resource in descriptor['resources']] == ['quota']
    expected = {'ListServices', 'ListAWSDefaultServiceQuotas', 'ListServiceQuotas',
                'GetAWSDefaultServiceQuota', 'GetServiceQuota'}
    before = database_frame(database)
    requests, responses, native_reads = [], [], {}

    def observe_request(request):
        target = request.headers.get('x-amz-target', '')
        if target.startswith('ServiceQuotasV20190624.'):
            assert request.method == 'POST' and request.url == team.endpoint + '/'
            assert request.headers['content-type'] == 'application/x-amz-json-1.1'
            requests.append((target.split('.')[-1], request.post_data_json,
                             request.headers['x-simulator-region']))

    def observe_response(response):
        target = response.request.headers.get('x-amz-target', '')
        if target.startswith('ServiceQuotasV20190624.'):
            operation = target.split('.')[-1]
            responses.append((operation, response.status))
            if operation in {'GetAWSDefaultServiceQuota', 'GetServiceQuota'}:
                native_reads[operation] = response

    page.on('request', observe_request)
    page.on('response', observe_response)
    try:
        page.get_by_role('button', name=re.compile('^Catalog service')).click()
        label = descriptor['title'] + ' ' + str(len(descriptor['operations'])) + ' / ' + str(descriptor['officialOperations']) + ' official operations'
        page.get_by_role('option', name=label, exact=True).click()
        expect(page.get_by_role('heading', level=1, name='Service Quotas', exact=True)).to_be_visible()
        expect(page.get_by_role('tab')).to_have_count(0)
        expect(page.get_by_role('button', name=re.compile('^Create '))).to_have_count(0)
        expect(page.get_by_role('button', name='Submit increase request', exact=True)).to_be_enabled()
        expect(page.get_by_text('Published AWS default', exact=True)).to_be_visible()
        expect(page.get_by_text('Applied account capacity', exact=True)).to_be_visible()
        expect(page.get_by_text('5', exact=True)).to_have_count(2)
        expect(page.get_by_role('spinbutton', name='Desired value', exact=True)).to_have_value('8')
        expect(page.get_by_role('checkbox', name='Allow support case (stored PENDING locally only)', exact=True)).not_to_be_checked()
        expect(page.get_by_role('textbox', name='Request ID', exact=True)).to_have_value('')
        expect(page.get_by_role('button', name='Get request', exact=True)).to_be_disabled()
        assert {operation for operation, _, _ in requests} == expected
        assert len(requests) == 5 and sorted(responses) == sorted((operation, 200) for operation in expected)
        regions = {region for _, _, region in requests}
        assert len(regions) == 1
        region = regions.pop()
        key = {'ServiceCode': 'b2bi', 'QuotaCode': 'L-05F6A9EF'}
        default = native_reads['GetAWSDefaultServiceQuota'].json()['Quota']
        applied = native_reads['GetServiceQuota'].json()['Quota']
        assert default['Value'] == applied['Value'] == 5
        assert all(value[name] == expected for value in (default, applied) for name, expected in key.items())
        assert applied['QuotaAppliedAtLevel'] == 'ACCOUNT'
        assert applied['QuotaArn'] == f'arn:aws:servicequotas:{region}:000000000000:b2bi/L-05F6A9EF'
        with page.expect_response(lambda response: response.request.headers.get('x-amz-target') == 'ServiceQuotasV20190624.ListRequestedServiceQuotaChangeHistoryByQuota') as read:
            page.get_by_role('button', name='Refresh history', exact=True).click()
        assert read.value.status == 200
        expect(page.get_by_role('button', name='Refresh history', exact=True)).to_be_enabled()
        assert requests[-1][1] == {**key, 'MaxResults': 1, 'QuotaRequestedAtLevel': 'ACCOUNT'}
        assert [operation for operation, _, _ in requests] == [operation for operation, _, _ in requests if operation in expected | {'ListRequestedServiceQuotaChangeHistoryByQuota'}]
        assert len(requests) == 6 and database_frame(database) == before
        # The dedicated view must preserve the same complete, descriptive picker.
        page.get_by_role('button', name=re.compile('^Catalog service')).click()
        expect(page.get_by_role('option', name=label, exact=True)).to_have_count(1)
        page.keyboard.press('Escape')
    finally:
        page.remove_listener('request', observe_request)
        page.remove_listener('response', observe_response)
