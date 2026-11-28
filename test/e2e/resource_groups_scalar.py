"""Genuine Resource Groups Scalar reads over owned native ECR tag state.

This is a browser/native HTTP witness, not official SDK compatibility evidence.
The ordinary SDK fixture is resource_group_ecr_members.py.
"""
from contextlib import ExitStack
import hashlib
from http.client import HTTPConnection
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright
from sakura_cli import frame
from sts_session_lifecycle import LocalSimulator

TYPE = 'AWS::ECR::Repository'
NAME = 'scalar-owned-group'
ACTIONS = ('resource-groups:ListGroupResources', 'cloudformation:DescribeStacks',
           'cloudformation:ListStackResources', 'tag:GetResources')
QUERY = {'Type': 'TAG_FILTERS_1_0', 'Query': json.dumps({
    'ResourceTypeFilters': [TYPE], 'TagFilters': [{'Key': 'Team', 'Values': ['blue']}]})}


def native(team, method, path, fields=None, *, service=None, operation=None, status=200):
    assert path.startswith('/') and not path.startswith('//')
    headers = {'X-Simulator-Token': team.token, 'Content-Type': 'application/json'}
    if service:
        headers['X-Simulator-Service'] = service
    if operation:
        assert service == 'ecr' and path == '/' and method == 'POST'
        headers['X-Amz-Target'] = 'AmazonEC2ContainerRegistry_V20150921.' + operation
    connection = HTTPConnection('127.0.0.1', team.port, timeout=5)
    try:
        connection.request(method, path, None if fields is None else json.dumps(fields), headers)
        response = connection.getresponse()
        assert response.status == status, 'unexpected local status; response withheld'
        if status == 400:
            assert response.getheader('X-Amzn-ErrorType') == 'BadRequestException'
        body = response.read((1 << 20) + 1)
        assert len(body) <= 1 << 20
        return json.loads(body) if body else {}
    finally:
        connection.close()


def members(team, name=NAME):
    return native(team, 'POST', '/list-group-resources', {'Group': name}, service='resource-groups')


def identifiers(arn):
    value = {'ResourceType': TYPE, 'ResourceArn': arn}
    return {'ResourceIdentifiers': [value], 'Resources': [{'Identifier': value}]}


def send(context, team, path, fields, expected, status=200):
    page = context.new_page()
    requests, errors = [], []
    page.on('pageerror', lambda error: errors.append(type(error).__name__))
    page.on('request', lambda request: requests.append(request)
            if urlsplit(request.url).path in ('/list-group-resources', '/get-group-query') else None)
    try:
        assert page.goto(team.endpoint + '/_simulator/docs/catalog.html?api=resource-groups').status == 200
        sidebar = page.get_by_role('complementary', name='Sidebar for AWS Resource Groups (simulator catalog)', exact=True)
        sidebar.locator(f'a[href="#resource-groups/tag/aws-resource-groups/POST{path}"]').click()
        page.get_by_role('button', name=f'Test Request (post {path})', exact=True).click()
        dialog = page.get_by_role('dialog').filter(has=page.get_by_role('button', name='Close Client', exact=True))
        header = dialog.get_by_role('row').filter(has=page.get_by_role(
            'checkbox', name='Include X-Simulator-Service in request', exact=True))
        expect(header.get_by_role('button', name='resource-groups', exact=True)).to_have_count(1)
        header.get_by_role('checkbox', name='Include X-Simulator-Service in request', exact=True).check()
        editor = dialog.get_by_role('textbox')
        editor.fill(json.dumps(fields))
        assert json.loads(editor.inner_text()) == fields and not requests
        # The shipped relative server is displayed with a slash. Assert the
        # actual original request separately; never rewrite its URL or body.
        action = dialog.get_by_role('button', name=f'Send post request to {team.endpoint}/{path}', exact=True)
        with page.expect_response(lambda response: response.url == team.endpoint + path
                                  and response.request.method == 'POST') as observed:
            action.click()
        response = observed.value
        request = response.request
        assert requests == [request] and request.url == team.endpoint + path
        assert request.redirected_from is None and request.redirected_to is None
        assert request.post_data_json == fields and response.status == status
        headers = request.all_headers()
        assert headers['x-simulator-service'] == 'resource-groups'
        assert any(part.strip().startswith('tcsim_') for part in headers['cookie'].split(';'))
        assert not {'authorization', 'x-simulator-token', 'x-simulator-access-role',
                    'x-simulator-principal', 'x-amz-security-token', 'x-amz-target'} & headers.keys()
        actual = response.json()
        assert actual == expected
        rendered = dialog.locator('.response-section-content-body').filter(
            has=page.get_by_role('link', name='Download Response Body', exact=True))
        expect(rendered).to_have_count(1)
        if status != 200:
            assert response.headers['x-amzn-errortype'] == 'ForbiddenException'
        for text in ([expected['Message']] if status != 200 else
                     [fields['Group'], 'ResourceQuery', 'TAG_FILTERS_1_0', TYPE, 'Team', 'blue'] if path == '/get-group-query' else
                     [entry['ResourceArn'] for entry in expected['ResourceIdentifiers']]):
            expect(rendered).to_contain_text(text)
        if status == 200 and path == '/list-group-resources' and not expected['ResourceIdentifiers']:
            expect(rendered).to_contain_text(re.compile(r'"ResourceIdentifiers":\s*\[\s*\]'))
            expect(rendered).to_contain_text(re.compile(r'"Resources":\s*\[\s*\]'))
        if status != 200:
            assert 'ResourceIdentifiers' not in actual and 'Resources' not in actual
        assert not errors
    finally:
        page.close()


def run():
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    digest = os.environ['SIMULATOR_BINARY_SHA256']
    assert re.fullmatch('[a-f0-9]{64}', digest)
    with binary.open('rb') as source:
        assert hashlib.file_digest(source, 'sha256').hexdigest() == digest
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix='resource-groups-scalar-') as directory, ExitStack() as stack:
        teams = []
        for label in ('owner', 'peer'):
            team = LocalSimulator(Path(directory) / label, binary)
            stack.callback(team.close)
            team.start()
            group = native(team, 'POST', '/groups', {'Name': NAME, 'ResourceQuery': QUERY}, service='resource-groups')['Group']
            stack.callback(native, team, 'POST', '/delete-group', {'Group': NAME}, service='resource-groups')
            repository = native(team, 'POST', '/', {'repositoryName': 'scalar-' + label,
                                'tags': [{'Key': 'Team', 'Value': 'blue'}]}, service='ecr', operation='CreateRepository')['repository']
            stack.callback(native, team, 'POST', '/', {'repositoryName': repository['repositoryName']},
                           service='ecr', operation='DeleteRepository')
            assert members(team) == identifiers(repository['repositoryArn'])
            teams.append((team, group, repository))
        (owner, group, repository), (peer, _, peer_repository) = teams
        assert owner.token != peer.token and owner.port != peer.port
        assert repository['repositoryArn'] != peer_repository['repositoryArn']
        native(owner, 'POST', '/groups', {'Name': 'scalar-plain'}, service='resource-groups')
        stack.callback(native, owner, 'POST', '/delete-group', {'Group': 'scalar-plain'}, service='resource-groups')
        assert native(owner, 'POST', '/get-group', {'Group': 'scalar-plain'}, service='resource-groups')['Group']['Name'] == 'scalar-plain'
        assert native(owner, 'POST', '/list-group-resources', {'Group': 'scalar-plain'},
                      service='resource-groups', status=400)['Message'] == 'The group has no supported resource query.'
        statements = [{'Effect': 'Allow', 'Action': action,
                       'Resource': group['GroupArn'] if action == ACTIONS[0] else '*'} for action in ACTIONS[:-1]]
        policy_path = '/_simulator/v1/iam/principals/scalar-partial-reader/policy'
        native(owner, 'PUT', policy_path, {'Version': '2012-10-17', 'Statement': statements})
        stack.callback(native, owner, 'DELETE', policy_path, status=204)
        participant = native(owner, 'POST', '/_simulator/v1/access', {'Principal': 'scalar-partial-reader'})['AccessToken']
        peer_before = frame(peer.database)
        with sync_playwright() as playwright:
            with playwright.chromium.launch(executable_path=str(executable), chromium_sandbox=True) as browser:
                outside = []
                def context_for(token):
                    context = browser.new_context(service_workers='block')
                    context.set_default_timeout(20_000)
                    def guard(route):
                        target = urlsplit(route.request.url)
                        if target.scheme == 'http' and target.netloc == f'127.0.0.1:{owner.port}':
                            route.continue_()
                        else:
                            outside.append(True)
                            route.abort()
                    context.route('**/*', guard)
                    assert context.request.post(owner.endpoint + '/_simulator/login',
                                                form={'token': token}, max_redirects=0).status == 303
                    return context
                with context_for(owner.token) as context:
                    spec = context.request.get(owner.endpoint + '/_simulator/docs/catalog/resource-groups.json').json()
                    for path, operation in [('/get-group-query', 'GetGroupQuery'), ('/list-group-resources', 'ListGroupResources')]:
                        assert spec['paths'][path]['post']['summary'] == operation
                    before = frame(owner.database)
                    query = native(owner, 'POST', '/get-group-query', {'Group': NAME}, service='resource-groups')
                    assert query == {'GroupQuery': {'GroupName': NAME, 'ResourceQuery': QUERY}}
                    send(context, owner, '/get-group-query', {'Group': NAME}, query)
                    send(context, owner, '/list-group-resources', {'Group': NAME}, identifiers(repository['repositoryArn']))
                    assert frame(owner.database) == before
                    native(owner, 'POST', '/', {'resourceArn': repository['repositoryArn'], 'tagKeys': ['Team']}, service='ecr', operation='UntagResource')
                    before = frame(owner.database)
                    send(context, owner, '/list-group-resources', {'Group': NAME}, {'ResourceIdentifiers': [], 'Resources': []})
                    assert frame(owner.database) == before
                    native(owner, 'POST', '/', {'resourceArn': repository['repositoryArn'], 'tags': [{'Key': 'Team', 'Value': 'blue'}]}, service='ecr', operation='TagResource')
                    before = frame(owner.database)
                    send(context, owner, '/list-group-resources', {'Group': NAME}, identifiers(repository['repositoryArn']))
                    assert frame(owner.database) == before
                with context_for(participant) as context:
                    before = frame(owner.database)
                    # Obtain the modeled error through the same native read,
                    # without borrowing any participant authority for the browser.
                    connection = HTTPConnection('127.0.0.1', owner.port, timeout=5)
                    try:
                        connection.request('POST', '/list-group-resources', json.dumps({'Group': NAME}),
                            {'X-Simulator-Token': participant, 'Content-Type': 'application/json', 'X-Simulator-Service': 'resource-groups'})
                        response = connection.getresponse()
                        assert response.status == 403
                        refusal = json.loads(response.read())
                        assert response.getheader('X-Amzn-ErrorType') == 'ForbiddenException'
                        assert set(refusal) == {'Message'} and refusal['Message']
                    finally:
                        connection.close()
                    send(context, owner, '/list-group-resources', {'Group': NAME}, refusal, 403)
                    assert frame(owner.database) == before
                owner.close()
                owner.start()
                with context_for(owner.token) as context:
                    before = frame(owner.database)
                    send(context, owner, '/list-group-resources', {'Group': NAME}, identifiers(repository['repositoryArn']))
                    assert frame(owner.database) == before
                assert not outside
        assert members(peer) == identifiers(peer_repository['repositoryArn']) and frame(peer.database) == peer_before
        assert members(owner) == identifiers(repository['repositoryArn'])
    assert not Path(directory).exists()
    print(json.dumps({'ok': True, 'binarySha256': digest, 'scalarSends': 6,
                      'tagChangesVisible': True, 'partialPermissionDenied': True,
                      'peerUnchangedAcrossReopen': True, 'cleanup': True, 'outsideRequests': 0}))


if __name__ == '__main__':
    run()
