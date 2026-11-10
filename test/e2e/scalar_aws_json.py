"""Actual embedded Scalar decoder + live cookie-authenticated native responses.

A small test-only OpenAPI document selects fixed local requests without coupling
this renderer regression to the shared endpoint's many unrelated request forms.
It uses the served, unmodified init.js configuration and bundled Scalar. Neither
HTTP responses nor response headers are mocked or rewritten. All values are
synthetic; no real cloud service, browser credential or user data is used.
"""
import json
import os
import re
import shutil
import urllib.parse
import urllib.request
from pathlib import Path

from playwright.sync_api import sync_playwright, expect
from check import running, REPORTS


def run():
    region = 'ap-northeast-1'
    name = '/docs-json/value'
    value = 'practice-value-日本語'
    hostile = '<img src=x onerror="globalThis.docsInjected=true"><svg/onload=alert(1)>'
    cases = [
        ('json10', 'application/x-amz-json-1.0', b'{"Tables":["docs-only"]}', 'docs-only'),
        ('json11', 'application/x-amz-json-1.1', json.dumps({'Value': hostile}).encode(), json.dumps({'Value': hostile})),
        ('parameters', 'Application/X-Amz-Json-1.1; charset=UTF-8',
         b' { "large":900719925474099312345, "precise":1.0000000000000001 }\n', '900719925474099312345'),
        ('standard', 'application/json', b'{"unchanged":"ordinary-json"}', 'ordinary-json'),
        ('malformed', 'application/x-amz-json-1.0', b'{not valid JSON', '{not valid JSON'),
        ('binary', 'application/octet-stream', bytes([0, 1, 127, 128, 255]), None),
    ]
    with running(authenticated=True) as (endpoint, token), sync_playwright() as p:
        def native(method, path, body=None, headers=None):
            request = urllib.request.Request(endpoint + path, method=method, data=body,
                headers={'X-Simulator-Token': token, **(headers or {})})
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, response.read()
        ssm_headers = {'Content-Type': 'application/x-amz-json-1.1',
                       'X-Amz-Target': 'AmazonSSM.PutParameter', 'X-Simulator-Region': region}
        assert native('POST', '/', json.dumps({'Name': name, 'Type': 'String', 'Value': value}).encode(), ssm_headers)[0] == 200
        assert native('PUT', '/docs-json-bucket')[0] == 200
        for key, content_type, body, _ in cases:
            assert native('PUT', '/docs-json-bucket/' + key, body, {'Content-Type': content_type})[0] == 200
        browser = p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'), args=['--no-sandbox'])
        try:
            context = browser.new_context(viewport={'width': 1440, 'height': 1000})
            assert context.request.post(endpoint + '/_simulator/login', form={'token': token}, max_redirects=0).status == 303
            # Observe the real served configuration before mounting our focused
            # document. Keep the production plugin, origin filter and S3 guide.
            context.add_init_script("""(() => {
              let scalar;
              Object.defineProperty(window, 'Scalar', {configurable:true,
                get:()=>scalar, set(value) {
                  scalar=value; const create=value.createApiReference;
                  value.createApiReference=(...args)=>{
                    window.docsTestConfig=args[1];
                    window.docsTestReference=create.apply(value,args);
                    return window.docsTestReference;
                  };
                }});
            })();""")
            page = context.new_page()
            errors, outside = [], []
            page.on('pageerror', lambda error: errors.append(str(error)))
            def guard(route):
                if route.request.url.startswith(endpoint + '/'):
                    route.continue_()
                else:
                    outside.append(route.request.url)
                    route.abort()
            context.route('**/*', guard)
            page.goto(endpoint + '/_simulator/docs/')
            page.wait_for_function('window.docsTestReference && window.docsTestConfig')
            original_init = context.request.get(endpoint + '/_simulator/docs/init.js').body()
            assert original_init == Path('internal/server/apidocs/init.js').read_bytes()
            paths = {'/': {'post': {
                'operationId': 'DocsGetParameter', 'summary': 'Read the local parameter',
                'parameters': [{'name': key, 'in': 'header', 'required': True,
                    'schema': {'type': 'string', 'default': default}, 'example': default}
                    for key, default in [('X-Amz-Target', 'AmazonSSM.GetParameter'), ('X-Simulator-Region', region)]],
                'requestBody': {'required': True, 'content': {'application/x-amz-json-1.1': {
                    'schema': {'type': 'object'}, 'example': {'Name': name}}}},
                'responses': {'200': {'description': 'Native parameter response'}}}}}
            for key, content_type, _, _ in cases:
                paths['/docs-json-bucket/' + key] = {'get': {
                    'operationId': 'Docs' + key, 'summary': 'Read ' + key,
                    'responses': {'200': {'description': 'Native stored bytes', 'content': {content_type: {}}}}}}
            document = {'openapi': '3.1.0', 'info': {'title': 'Local response display regression', 'version': '1'},
                        'servers': [{'url': '/'}], 'paths': paths}
            page.evaluate("""document => {
              const config=window.docsTestConfig;
              window.docsTestReference.destroy();
              Scalar.createApiReference('#reference', {...config, url:undefined, content:document});
            }""", document)
            def send(method, path):
                page.get_by_role('button', name=f'Test Request ({method.lower()} {path})', exact=True).click()
                with page.expect_response(lambda response: response.request.method == method
                        and urllib.parse.urlsplit(response.url).path == path) as received:
                    # The initial empty-response CTA disappears after a request.
                    # The address-bar action keeps this accessible name on repeats.
                    page.get_by_role('button', name=re.compile(
                        rf'^Send {method.lower()} request to ')).click()
                return received.value
            def response_body():
                # Scalar inherits this class onto the nested disclosure panel.
                # Select its outer response block, which owns the download action,
                # rather than accepting whichever duplicate happens to come first.
                body = page.locator('.response-section-content-body').filter(
                    has=page.get_by_role('link', name='Download Response Body', exact=True))
                expect(body).to_have_count(1)
                return body
            def check_text(text):
                body = response_body()
                expect(body).to_be_visible()
                expect(body).to_contain_text(text)
                expect(body.get_by_text('Binary file', exact=True)).to_have_count(0)
                assert body.locator('img, svg[onload], script').count() == 0
                assert page.evaluate('globalThis.docsInjected === undefined')
                return body
            # Regression for the reported operation/region with only a browser
            # session cookie. Repeat after a native write to reject static output.
            for read_number, current in enumerate([value, 'second-live-value'], start=1):
                if current != value:
                    native('POST', '/', json.dumps({'Name': name, 'Type': 'String', 'Value': current, 'Overwrite': True}).encode(), ssm_headers)
                response = send('POST', '/')
                assert response.status == 200
                assert response.headers['content-type'] == 'application/x-amz-json-1.1'
                assert response.json()['Parameter']['Value'] == current
                assert not response.request.headers.get('x-simulator-token')
                check_text(current)
                print(f'Scalar native GetParameter JSON display passed: read {read_number}', flush=True)
                page.get_by_role('button', name='Close Client', exact=True).click()
            for key, content_type, raw, text in cases:
                response = send('GET', '/docs-json-bucket/' + key)
                assert response.status == 200 and response.body() == raw
                assert response.headers['content-type'] == content_type
                body = response_body()
                if text is None:
                    expect(body.get_by_text('Binary file', exact=True)).to_be_visible()
                else:
                    check_text(text)
                with page.expect_download() as downloaded:
                    body.get_by_role('link', name='Download Response Body', exact=True).click()
                assert Path(downloaded.value.path()).read_bytes() == raw
                print(f'Scalar native response display and exact download passed: {key}', flush=True)
                page.get_by_role('button', name='Close Client', exact=True).click()
            assert not errors and not outside, (errors, outside)
            context.close()
        finally:
            browser.close()
    (REPORTS / 'scalar-aws-json.json').write_text(json.dumps({
        'nativeSSMGetParameterCookieOnly': True, 'region': region, 'repeatedLiveValues': 2,
        'responseCases': [case[0] for case in cases], 'exactDownloads': True,
        'untrustedMarkupIsText': True, 'nativeHeadersUnchanged': True,
        'configuration': 'unmodified served init.js, focused test OpenAPI document',
        'externalRequests': 0, 'pageErrors': 0,
    }, indent=2) + '\n')


if __name__ == '__main__':
    run()
