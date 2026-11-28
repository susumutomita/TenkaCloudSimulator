"""Cloudscape query editing and paged ECR membership against two real local teams."""
import argparse
from contextlib import ExitStack
import hashlib
from http.client import HTTPConnection
import json
import os
from pathlib import Path
import re
import tempfile
import time
from urllib.parse import urlsplit

from playwright.sync_api import Error as PlaywrightError, expect, sync_playwright
from reflow import assert_reflow
from resource_groups_scalar import identifiers, send as scalar_send
from sts_session_lifecycle import LocalSimulator

REGION = 'us-east-1'
RESOURCE = 'AWS::ECR::Repository'
NAME = 'query-console'


def native(team, path, body, *, target=None, method='POST', region=REGION, status=200):
    assert path.startswith('/') and not path.startswith('//')
    headers = {'Content-Type': 'application/json', 'X-Simulator-Token': team.token,
               'X-Simulator-Service': 'resource-groups', 'X-Simulator-Region': region}
    if target:
        headers.update({'Content-Type': 'application/x-amz-json-1.1', 'X-Simulator-Service': 'ecr',
                        'X-Amz-Target': 'AmazonEC2ContainerRegistry_V20150921.' + target})
    connection = HTTPConnection('127.0.0.1', team.port, timeout=5)
    try:
        connection.request(method, path, json.dumps(body), headers)
        response = connection.getresponse()
        assert response.status == status, (path, target, response.status)
        data = response.read(131073)
        assert len(data) <= 131072
        return json.loads(data) if data else {}
    finally:
        connection.close()


def query(value):
    return {'Type': 'TAG_FILTERS_1_0', 'Query': json.dumps(
        {'ResourceTypeFilters': [RESOURCE], 'TagFilters': [{'Key': 'Team', 'Values': [value]}]})}


def saved_members(team, group):
    response = native(team, '/list-group-resources', {'Group': group})
    assert response['Resources'] == [{'Identifier': item} for item in response['ResourceIdentifiers']]
    return [item['ResourceArn'] for item in response['ResourceIdentifiers']]


def inspect_group(page, name):
    page.get_by_role('button', name='Services', exact=True).click()
    page.get_by_role('menuitem', name='Service catalog', exact=True).click()
    select_group(page, name)


def select_group(page, name):
    page.get_by_role('button', name=re.compile('^Catalog service')).click()
    page.get_by_role('option', name=re.compile('^AWS Resource Groups')).click()
    page.get_by_role('link', name=name, exact=True).click()


def login(context, team, token):
    origin = urlsplit(team.endpoint)
    outside = []
    errors = []
    def local(route):
        url = urlsplit(route.request.url)
        if (url.scheme, url.netloc) != (origin.scheme, origin.netloc):
            outside.append(True)
            route.abort()
        else:
            route.continue_()
    context.route('**/*', local)
    page = context.new_page()
    page.on('pageerror', lambda error: errors.append(str(error)))
    page.goto(team.endpoint + '/_simulator/console/')
    page.get_by_label('Access token', exact=True).fill(token)
    page.get_by_role('button', name='Open sandbox', exact=True).click()
    return page, outside, errors


def run(with_sdk):
    if with_sdk:
        import boto3
        from botocore.config import Config
        session = boto3.Session()
        config = Config(retries={'total_max_attempts': 1}, connect_timeout=3, read_timeout=5, proxies={})
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    expected = os.environ['SIMULATOR_BINARY_SHA256']
    with binary.open('rb') as source:
        assert hashlib.file_digest(source, 'sha256').hexdigest() == expected
    with tempfile.TemporaryDirectory(prefix='resource-group-browser-') as temp, ExitStack() as processes:
        owner, peer = [LocalSimulator(Path(temp) / name, binary) for name in ('owner', 'peer')]
        for team in (owner, peer):
            processes.callback(team.close)
            team.start()
        clients = {}
        if with_sdk:
            for team in (owner, peer):
                client = session.client('resource-groups', region_name=REGION, endpoint_url=team.endpoint,
                                        aws_access_key_id='TCSIMEXAMPLE', aws_secret_access_key='fixture-only', config=config)
                def local_token(request, team=team, **kwargs):
                    assert urlsplit(request.url).netloc == urlsplit(team.endpoint).netloc
                    request.headers['X-Simulator-Token'] = team.token
                client.meta.events.register('before-sign.resource-groups', local_token)
                clients[team] = client
                processes.callback(client.close)

        def members(team, group):
            result = saved_members(team, group)
            if with_sdk:
                response = clients[team].list_group_resources(Group=group)
                assert response['ResponseMetadata']['HTTPStatusCode'] == 200
                assert [item['ResourceArn'] for item in response['ResourceIdentifiers']] == result
            return result

        with ExitStack() as cleanup:
            def create_group(team, name, value=None, region=REGION):
                result = native(team, '/groups', {'Name': name, **({'ResourceQuery': query(value)} if value else {})}, region=region)
                arn = result['Group']['GroupArn']
                cleanup.callback(native, team, '/delete-group', {'Group': arn}, region=region)
                return arn
            def repository(team, name, value, region=REGION):
                result = native(team, '/', {'repositoryName': name, 'tags': [{'Key': 'Team', 'Value': value}]}, target='CreateRepository', region=region)
                cleanup.callback(native, team, '/', {'repositoryName': name}, target='DeleteRepository', region=region)
                return result['repository']['repositoryArn']
            group = create_group(owner, NAME, 'blue')
            create_group(owner, 'queryless')
            blue = [repository(owner, f'query-blue-{i:02}', 'blue') for i in range(11)]
            green = repository(owner, 'query-green', 'green')
            west = 'us-west-2'
            create_group(owner, NAME, 'green', region=west)
            west_repo = repository(owner, 'west-only', 'green', region=west)
            peer_group = create_group(peer, NAME, 'blue')
            peer_repo = repository(peer, 'peer-only', 'blue')

            policy_path = '/_simulator/v1/iam/principals/group-console/policy'
            allow = {'Effect': 'Allow', 'Action': '*', 'Resource': '*'}
            def policy(*extra):
                native(owner, policy_path, {'Version': '2012-10-17', 'Statement': [allow, *extra]}, method='PUT')
            policy()
            cleanup.callback(native, owner, policy_path, None, method='DELETE', status=204)
            token = native(owner, '/_simulator/v1/access', {'Principal': 'group-console'})['AccessToken']
            with sync_playwright() as playwright, playwright.chromium.launch(
                    executable_path=os.environ.get('CHROMIUM_EXECUTABLE'), chromium_sandbox=True) as browser:
                with browser.new_context(service_workers='block', viewport={'width': 1440, 'height': 1000}) as context:
                    page, outside, errors = login(context, owner, token)
                    inspect_group(page, NAME)
                    expect(page.get_by_role('heading', name='Matching repositories')).to_be_visible()
                    for arn in blue[:10]:
                        expect(page.get_by_text(arn, exact=True)).to_be_visible()
                    expect(page.get_by_text(blue[-1], exact=True)).to_have_count(0)
                    assert members(owner, group) == blue

                    def click(action, path, status=200):
                        with page.expect_response(lambda r: r.request.method == 'POST' and r.url == owner.endpoint + path) as captured:
                            page.get_by_role('button', name=action, exact=True).click()
                        response = captured.value
                        assert response.status == status
                        assert response.request.post_data_json['Group'] == group
                        assert response.request.headers['x-simulator-region'] == REGION
                        return response
                    page_two = click('Next membership page', '/list-group-resources')
                    assert page_two.request.post_data_json['NextToken']
                    expect(page.get_by_text(blue[-1], exact=True)).to_be_visible()
                    expect(page.get_by_text(blue[0], exact=True)).to_have_count(0)
                    click('First membership page', '/list-group-resources')
                    expect(page.get_by_text(blue[0], exact=True)).to_be_visible()
                    policy({'Effect': 'Deny', 'Action': 'tag:GetResources', 'Resource': '*'})
                    denied = click('Next membership page', '/list-group-resources', 403)
                    assert denied.headers.get('x-amzn-errortype') == 'ForbiddenException'
                    expect(page.get_by_text(re.compile('403:.*explicitDeny'))).to_be_visible()
                    expect(page.get_by_role('heading', name='Matching repositories')).to_have_count(0)
                    for arn in blue:
                        expect(page.get_by_text(arn, exact=True)).to_have_count(0)
                    policy()
                    click('Refresh query and members', '/list-group-resources')
                    expect(page.get_by_text(blue[0], exact=True)).to_be_visible()
                    page.get_by_role('button', name='Edit query', exact=True).click()
                    field = page.get_by_label('Query tag value', exact=True)
                    expect(field).to_have_value('blue')
                    field.fill('green')
                    written = click('Save query', '/update-group-query')
                    assert json.loads(written.request.post_data_json['ResourceQuery']['Query']) == json.loads(query('green')['Query'])
                    expect(page.get_by_text(green, exact=True)).to_be_visible()
                    assert json.loads(native(owner, '/get-group-query', {'Group': group})['GroupQuery']['ResourceQuery']['Query']) == json.loads(query('green')['Query'])
                    assert members(owner, group) == [green] and members(peer, peer_group) == [peer_repo]
                    scalar_send(context, owner, '/list-group-resources', {'Group': group}, identifiers(green))
                    # Hold one genuine east response after the local application has
                    # answered. Change region while it is pending, then release it.
                    held = []
                    def hold(route):
                        assert route.request.post_data_json['Group'] == group
                        response = route.fetch(max_redirects=0)
                        assert response.ok and response.json()['Resources'][0]['Identifier']['ResourceArn'] == green
                        held.append((route, response))
                    page.route('**/list-group-resources', hold, times=1)
                    page.get_by_role('button', name='Refresh query and members', exact=True).click()
                    deadline = time.monotonic() + 5
                    while not held and time.monotonic() < deadline:
                        page.wait_for_timeout(20)
                    assert len(held) == 1
                    page.get_by_role('button', name=REGION, exact=True).click()
                    page.get_by_role('menuitem', name=west, exact=True).click()
                    select_group(page, NAME)
                    expect(page.get_by_text(west_repo, exact=True)).to_be_visible()
                    route, response = held[0]
                    try:
                        route.fulfill(response=response)
                    except PlaywrightError:
                        assert route.request.failure is not None, 'response release failed for a live request'
                    expect(page.get_by_text(green, exact=True)).to_have_count(0)
                    expect(page.get_by_text(west_repo, exact=True)).to_be_visible()
                    page.get_by_role('button', name=west, exact=True).click()
                    page.get_by_role('menuitem', name=REGION, exact=True).click()
                    select_group(page, NAME)
                    expect(page.get_by_text(green, exact=True)).to_be_visible()

                    # Drop an actual mutation acknowledgment after the server commits.
                    # The editor must read back current state before another write.
                    acknowledgments = []
                    def lose_ack(route):
                        result = route.fetch(max_redirects=0)
                        assert result.status == 200
                        acknowledgments.append(result.json()['GroupQuery'])
                        route.abort()
                    page.route('**/update-group-query', lose_ack, times=1)
                    page.get_by_role('button', name='Edit query', exact=True).click()
                    page.get_by_label('Query tag value', exact=True).fill('blue')
                    page.get_by_role('button', name='Save query', exact=True).click()
                    expect(page.get_by_text(re.compile('Refresh before editing again'))).to_be_visible()
                    expect(page.get_by_role('button', name='Edit query', exact=True)).to_have_count(0)
                    assert len(acknowledgments) == 1 and members(owner, group) == blue
                    click('Refresh query and members', '/list-group-resources')
                    expect(page.get_by_text(blue[0], exact=True)).to_be_visible()
                    page.get_by_role('button', name='Edit query', exact=True).click()
                    page.get_by_label('Query tag value', exact=True).fill('green')
                    click('Save query', '/update-group-query')
                    expect(page.get_by_text(green, exact=True)).to_be_visible()
                    for width in (1440, 390):
                        page.set_viewport_size({'width': width, 'height': 950})
                        expect(page.get_by_text(green, exact=True)).to_be_visible()
                        assert_reflow(page)
                    native(owner, '/', {'resourceArn': green, 'tagKeys': ['Team']}, target='UntagResource')
                    click('Refresh query and members', '/list-group-resources')
                    expect(page.get_by_text('No matching repositories', exact=True)).to_be_visible()
                    expect(page.get_by_text(green, exact=True)).to_have_count(0)
                    assert members(owner, group) == []
                    scalar_send(context, owner, '/list-group-resources', {'Group': group}, {'Resources': [], 'ResourceIdentifiers': []})
                    page.set_viewport_size({'width': 1440, 'height': 950})
                    page.get_by_role('navigation', name='Catalog location').get_by_role('link').nth(1).click()
                    page.get_by_role('link', name='queryless', exact=True).click()
                    expect(page.get_by_text('This group has no resource query.', exact=True)).to_be_visible()
                    expect(page.get_by_role('button', name='Edit query', exact=True)).to_have_count(0)
                    expect(page.get_by_role('heading', name=re.compile(r'^Tags\s*\(0\)$'))).to_be_visible()
                    assert not outside and not errors, (len(outside), len(errors))
                with browser.new_context(service_workers='block') as context:
                    page, outside, errors = login(context, peer, peer.token)
                    inspect_group(page, NAME)
                    expect(page.get_by_text(peer_repo, exact=True)).to_be_visible()
                    expect(page.get_by_text(green, exact=True)).to_have_count(0)
                    assert not outside and not errors, (len(outside), len(errors))
            owner.close()
            owner.start()
            assert json.loads(native(owner, '/get-group-query', {'Group': group})['GroupQuery']['ResourceQuery']['Query']) == json.loads(query('green')['Query'])
            assert members(owner, group) == [] and members(peer, peer_group) == [peer_repo]
        for team, region in [(owner, REGION), (owner, west), (peer, REGION)]:
            assert native(team, '/groups-list', {}, region=region)['GroupIdentifiers'] == []
            assert native(team, '/', {}, target='DescribeRepositories', region=region)['repositories'] == []
    print(json.dumps({'resourceGroupConsole': True, 'binarySha256': expected, 'widths': [1440, 390],
                      'nativePages': 2, 'sameGroupScalarSends': 2, 'deniedContinuation': 403, 'updatedThenEmpty': True,
                      'regionChangeDuringRead': True, 'lostUpdateReconciled': True, 'reopened': True, 'teamsClean': 2, 'outsideRequests': 0, 'sdkSharedState': with_sdk}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--with-sdk', action='store_true', help='Also read each verified membership set with the official boto3 client.')
    run(parser.parse_args().with_sdk)
