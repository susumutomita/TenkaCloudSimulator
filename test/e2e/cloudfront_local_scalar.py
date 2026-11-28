"""Three genuine Scalar Sends over SDK-created local CloudFront/S3 state."""
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit
from xml.etree import ElementTree as ET

from playwright.sync_api import expect, sync_playwright

from cloudfront_local_content import (BUCKET, PREFIX, cleanup, client, config,
                                      local, ordinary_session, successful)
from sakura_cli import frame
from sts_session_lifecycle import LocalSimulator

NS = '{http://cloudfront.amazonaws.com/doc/2020-05-31/}'
KEY = 'asset.txt'
OLD = b'old local scalar text'
FRESH = b'fresh local scalar text'
LOCAL_TEMPLATE = '/_simulator/cloudfront/distributions/{Id}/objects/{ObjectKey}'


def send(context, team, method, template, identifier, spec, expected_path, expected_status,
         expected_text, directory=None):
    operations = spec['paths'][template]
    operation = operations[method.lower()]
    tag = operation['tags'][0].lower().replace(' ', '-')
    requests, errors = [], []
    page = context.new_page()
    page.on('pageerror', lambda error: errors.append(type(error).__name__))
    page.on('request', lambda request: requests.append(request)
            if urlsplit(request.url).path.startswith((PREFIX, '/_simulator/cloudfront/distributions/')) else None)
    try:
        assert page.goto(team.endpoint + '/_simulator/docs/cloudfront-local.html').status == 200
        sidebar = page.get_by_role('complementary', name='Sidebar for Local CloudFront distributions', exact=True)
        sidebar.locator(f'a[href="#tag/{tag}/{method}{template}"]').click()
        page.get_by_role('button', name=f'Test Request ({method.lower()} {template})', exact=True).click()
        dialog = page.get_by_role('dialog').filter(has=page.get_by_role('button', name='Close Client', exact=True))
        for name, value in [('Id', identifier)] + ([('ObjectKey', KEY)] if template == LOCAL_TEMPLATE else []):
            include = dialog.get_by_role('checkbox', name=f'Include {name} in request', exact=True)
            row = dialog.get_by_role('row').filter(has=include)
            field = row.get_by_role('combobox', name=' Value', exact=True)
            expect(include).to_have_count(1)
            expect(field).to_have_count(1)
            include.check()
            field.fill(value)
            expect(field).to_have_text(value)
        assert not requests
        target = team.endpoint + expected_path
        action = dialog.get_by_role('button', name=f'Send {method.lower()} request to {target}', exact=True)
        expect(action).to_have_count(1)
        with page.expect_response(lambda response: response.url == target and response.request.method == method) as observed:
            action.click()
        response = observed.value
        assert response.status == expected_status
        assert response.request.redirected_from is None and response.request.header_value('cookie')
        assert response.request.url == target
        for header in ('authorization', 'x-simulator-token', 'x-simulator-principal', 'x-simulator-access-role'):
            assert not response.request.header_value(header)
        if template == LOCAL_TEMPLATE:
            assert not response.request.header_value('x-simulator-service')
            assert not response.request.header_value('x-simulator-region')
        else:
            assert response.request.header_value('x-simulator-service') == 'cloudfront'
            assert response.request.header_value('x-simulator-region') == 'us-east-1'
        if method == 'POST':
            actual = ET.fromstring(response.request.post_data)
            example = ET.fromstring(operation['requestBody']['content']['application/xml']['example'])
            assert actual.tag == example.tag == NS + 'InvalidationBatch'
            assert actual.findtext(NS + 'CallerReference') == example.findtext(NS + 'CallerReference')
            assert actual.findtext(NS + 'Paths/' + NS + 'Quantity') == '1'
            assert [path.text for path in actual.findall(NS + 'Paths/' + NS + 'Items/' + NS + 'Path')] == ['/*']
        else:
            assert response.request.post_data is None
        body = response.body()
        rendered = dialog.locator('.response-section-content-body').filter(
            has=page.get_by_role('link', name='Download Response Body', exact=True))
        expect(rendered).to_have_count(1)
        expect(rendered).to_contain_text(expected_text)
        if template == LOCAL_TEMPLATE:
            assert body == FRESH
            headers = response.headers
            assert headers['content-disposition'] == 'attachment'
            assert headers['x-content-type-options'] == 'nosniff'
            assert headers['content-security-policy'] == "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
            assert headers['cache-control'] == 'no-store'
            with page.expect_download() as pending:
                rendered.get_by_role('link', name='Download Response Body', exact=True).click()
            saved = Path(directory) / 'scalar-response.txt'
            pending.value.save_as(saved)
            assert saved.read_bytes() == FRESH
        else:
            parsed = ET.fromstring(body)
            if method == 'GET':
                assert parsed.tag == NS + 'Distribution' and parsed.findtext(NS + 'Id') == identifier
                assert parsed.findtext(NS + 'DistributionConfig/' + NS + 'Origins/' + NS + 'Items/' + NS + 'Origin/' + NS + 'DomainName') == BUCKET + '.s3.amazonaws.com'
            else:
                assert parsed.tag == NS + 'Invalidation' and parsed.findtext(NS + 'Status') == 'Completed'
        assert len(requests) == 1 and requests[0].url == target and requests[0].method == method
        assert not errors
        return body
    finally:
        page.close()


def run():
    binary = Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True)
    digest = os.environ['SIMULATOR_BINARY_SHA256']
    assert re.fullmatch('[a-f0-9]{64}', digest)
    with binary.open('rb') as source:
        assert hashlib.file_digest(source, 'sha256').hexdigest() == digest
    executable = Path(os.environ['CHROMIUM_EXECUTABLE']).resolve(strict=True)
    session, settings = ordinary_session()
    with tempfile.TemporaryDirectory(prefix='cloudfront-scalar-') as directory, ExitStack() as stack:
        teams = []
        stats = {'sends': 0, 'responses': 0}
        for name, body in [('owner', OLD), ('peer', b'peer local scalar text')]:
            team = LocalSimulator(Path(directory) / name, binary)
            stack.callback(team.close)
            team.start()
            s3 = client(session, 's3', team, stack, settings, stats, key=KEY)
            front = client(session, 'cloudfront', team, stack, settings, stats, key=KEY)
            successful(s3.create_bucket, Bucket=BUCKET)
            successful(s3.put_object, Bucket=BUCKET, Key=KEY, Body=body, ContentType='text/plain')
            saved = successful(front.create_distribution, status=201, DistributionConfig=config('scalar-content', ttl=3600))['Distribution']
            identifier = saved['Id']
            stack.callback(cleanup, front, s3, identifier, KEY)
            path = f'/_simulator/cloudfront/distributions/{identifier}/objects/{KEY}'
            assert local(team, 'GET', path, 200)[0] == body
            teams.append((team, s3, front, saved, path))
        owner, s3, front, saved, path = teams[0]
        peer, _, other, peer_saved, peer_path = teams[1]
        assert owner.token != peer.token and owner.port != peer.port
        peer_before = frame(peer.database)
        successful(s3.put_object, Bucket=BUCKET, Key=KEY, Body=FRESH, ContentType='text/plain')
        assert local(owner, 'GET', path, 200)[0] == OLD
        policy_path = '/_simulator/v1/iam/principals/scalar-content-reader/policy'
        policy = {'Version': '2012-10-17', 'Statement': [
            {'Effect': 'Allow', 'Action': 'cloudfront:GetDistribution', 'Resource': saved['ARN']},
            {'Effect': 'Allow', 'Action': 's3:GetObject', 'Resource': 'arn:aws:s3:::' + BUCKET + '/' + KEY}]}
        local(owner, 'PUT', policy_path, 200, policy)
        stack.callback(local, owner, 'DELETE', policy_path, 204)
        participant = json.loads(local(owner, 'POST', '/_simulator/v1/access', 200, {'Principal': 'scalar-content-reader'})[0])['AccessToken']
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
                    assert context.request.post(owner.endpoint + '/_simulator/login', form={'token': token}, max_redirects=0).status == 303
                    return context

                with context_for(owner.token) as context:
                    document = context.request.get(owner.endpoint + '/_simulator/docs/cloudfront-local.json')
                    assert document.status == 200
                    spec = document.json()
                    native = [operation for methods in spec['paths'].values() for operation in methods.values()
                              if not operation.get('x-simulator-only')]
                    assert len(native) == 8
                    send(context, owner, 'GET', PREFIX + '/{Id}', saved['Id'], spec, PREFIX + '/' + saved['Id'], 200, saved['Id'])
                    assert local(owner, 'GET', path, 200)[0] == OLD
                    send(context, owner, 'POST', PREFIX + '/{Id}/invalidation', saved['Id'], spec,
                         PREFIX + '/' + saved['Id'] + '/invalidation', 201, 'Completed')
                    assert local(owner, 'GET', path, 200)[0] == FRESH
                    send(context, owner, 'GET', LOCAL_TEMPLATE, saved['Id'], spec, path, 200, FRESH.decode(), directory)
                with context_for(participant) as restricted:
                    positive = restricted.request.get(owner.endpoint + path)
                    assert positive.status == 200 and positive.body() == FRESH
                    assert positive.headers['content-disposition'] == 'attachment'
                    policy['Statement'].append({'Effect': 'Deny', 'Action': 's3:GetObject', 'Resource': '*'})
                    local(owner, 'PUT', policy_path, 200, policy)
                    assert restricted.request.get(owner.endpoint + path).status == 403
                assert successful(front.get_distribution, Id=saved['Id'])['Distribution'] == saved
                assert successful(other.get_distribution, Id=peer_saved['Id'])['Distribution'] == peer_saved
                assert frame(peer.database) == peer_before
                owner.close()
                owner.start()
                assert successful(front.get_distribution, Id=saved['Id'])['Distribution'] == saved
                assert local(owner, 'GET', path, 200)[0] == FRESH
                stored = successful(s3.get_object, Bucket=BUCKET, Key=KEY)
                with stored['Body'] as data:
                    assert data.read() == FRESH
                assert local(peer, 'GET', peer_path, 200)[0] == b'peer local scalar text'
                assert successful(other.get_distribution, Id=peer_saved['Id'])['Distribution'] == peer_saved
                assert frame(peer.database) == peer_before and not outside
        assert stats['sends'] == stats['responses'] and stats['sends'] > 0
    assert not Path(directory).exists()
    print(json.dumps({'ok': True, 'binarySha256': digest, 'nativeCloudFrontSends': 2,
                      'simulatorOnlyContentSends': 1, 'freshVisibleDownloadedBytes': True,
                      'freshHitDenial': True, 'peerUnchangedAcrossReopen': True,
                      'cleanup': True, 'outsideRequests': 0, 'externalOriginFetch': False}))


if __name__ == '__main__':
    run()
