"""Real Scalar Send + native S3 metadata, with isolated renderer safety checks.

No guest VM or AWS access. Parser fixtures are explicitly synthetic; the first
and last flows send real HTTP through Scalar against the embedded binary.
"""
import base64
import hashlib
import json
import os
import re
from pathlib import Path
import urllib.request
import xml.etree.ElementTree as ET
from playwright.sync_api import sync_playwright, expect
from check import running, REPORTS
from reflow import assert_reflow
from openapi_parameters import validate_parameter_lists
from test_sakura_openapi import OLD_IDS, NEW_IDS, COLLECTION, ITEM


from metrics_openapi import preserved_metrics_reference

def run_scalar_s3_docs():
    validate_parameter_lists(json.loads(Path('internal/server/apidocs/openapi.json').read_text()))
    with running(authenticated=True) as (endpoint, token), sync_playwright() as p:
        def native(method, path, body=b'', headers=None):
            request = urllib.request.Request(endpoint + path, data=body if method != 'GET' else None,
                method=method, headers={'X-Simulator-Token': token, **(headers or {})})
            with urllib.request.urlopen(request) as response:
                return response.status, response.read()
        native('PUT', '/practice-bucket')
        native('PUT', '/practice-backups', b'<CreateBucketConfiguration><LocationConstraint>us-west-2</LocationConstraint></CreateBucketConfiguration>')
        native('PUT', '/practice-bucket?versioning', b'<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>')
        native('PUT', '/practice-bucket/hello.txt', b'Hello from the local simulator.')
        native('PUT', '/practice-bucket/gone.txt', b'old body')
        native('DELETE', '/practice-bucket/gone.txt')
        native('PUT', '/practice-bucket?publicAccessBlock', b'<PublicAccessBlockConfiguration><BlockPublicAcls>true</BlockPublicAcls><IgnorePublicAcls>true</IgnorePublicAcls><BlockPublicPolicy>true</BlockPublicPolicy><RestrictPublicBuckets>true</RestrictPublicBuckets></PublicAccessBlockConfiguration>')
        tags = b'<Tagging><TagSet><Tag><Key>Environment</Key><Value>practice</Value></Tag></TagSet></Tagging>'
        native('PUT', '/practice-bucket?tagging', tags, {'Content-MD5': base64.b64encode(hashlib.md5(tags).digest()).decode()})
        _, raw_spec = native('GET', '/_simulator/docs/openapi.json')
        spec = json.loads(raw_spec)
        preserved_metrics_reference(spec)
        validate_parameter_lists(spec)
        assert spec == json.loads(Path('internal/server/apidocs/openapi.json').read_text()), 'Must exercise the actual embedded candidate spec'
        operations = [op['operationId'] for item in spec['paths'].values() for op in item.values() if isinstance(op, dict) and 'operationId' in op]
        assert len(operations) == 66 and len(set(operations)) == 66 and set(operations) == OLD_IDS | NEW_IDS
        assert len(spec['paths']) == 36 and COLLECTION in spec['paths'] and ITEM in spec['paths']
        assert {'GCPListBuckets', 'GCPUploadObject', 'AzureListContainers', 'AzureGetBlob'} <= set(operations)
        assert spec['paths']['/']['get']['responses']['200']['content']['application/xml']['schema']['$ref'].endswith('/S3BucketList')
        examples = spec['paths']['/{bucket}']['get']['responses']['200']['content']['application/xml']['examples']
        mode_paths = {'currentObjects': '/practice-bucket?list-type=2', 'versions': '/practice-bucket?versions',
            'location': '/practice-backups?location', 'versioning': '/practice-bucket?versioning',
            'publicAccessBlock': '/practice-bucket?publicAccessBlock', 'tags': '/practice-bucket?tagging'}
        for name, path in mode_paths.items():
            status, body = native('GET', path)
            assert status == 200
            assert ET.fromstring(body).tag.split('}')[-1] == ET.fromstring(examples[name]['value']).tag.split('}')[-1], (name, body)
        executable = os.environ.get('CHROMIUM_EXECUTABLE', '/usr/bin/chromium')
        browser = p.chromium.launch(executable_path=executable, args=['--no-sandbox'])
        context = browser.new_context(viewport={'width': 1440, 'height': 1000})
        assert context.request.post(endpoint + '/_simulator/login', form={'token': token}, max_redirects=0).status == 303
        page = context.new_page(); errors = []; outside = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.on('request', lambda request: outside.append(request.url) if not request.url.startswith(endpoint) and not request.url.startswith('data:') else None)
        page.goto(endpoint + '/_simulator/docs/#tag/s3-basic-object-storage/GET/')
        expect(page.get_by_text('List all buckets', exact=True).first).to_be_visible(timeout=30000)
        trigger = page.locator('#s3-result-trigger')
        expect(trigger).to_be_hidden()  # Static examples must never look executed.
        expect(page.get_by_text('Static examples below are sample documentation, not your current state.', exact=True).first).to_be_visible()
        page.screenshot(path=str(REPORTS / 'scalar-static-example-desktop.png'), animations='disabled')
        # Latest DOM observation selects the actual Scalar Send control.
        assert 'Test Request' in page.locator('body').aria_snapshot()
        page.get_by_role('button', name='Test Request (get /)', exact=True).click()
        with page.expect_response(lambda r: r.request.method == 'GET' and r.url.rstrip('/') == endpoint) as sent:
            page.get_by_role('button').filter(has_text='Send Request').last.click()
        assert sent.value.status == 200
        wire = sent.value.text()
        assert 'practice-backups' in wire and 'practice-bucket' in wire
        expect(trigger).to_contain_text('2 buckets in this response')
        # Restore the exact actual opener at both responsive layouts.
        dialog = page.get_by_role('dialog', name='Understand the live S3 response')
        send_button = page.get_by_role('button', name=re.compile(r'^Send get request'))
        for width, height in [(1440, 1000), (390, 844)]:
            page.set_viewport_size({'width': width, 'height': height})
            send_button.focus(); expect(send_button).to_be_focused()
            page.keyboard.press('Alt+Shift+R')
            expect(dialog).to_be_visible()
            expect(dialog).to_contain_text('practice-backups')
            expect(dialog).to_contain_text('us-west-2')
            expect(page.locator('#s3-result-close')).to_be_focused()
            page.keyboard.press('Tab'); expect(page.locator('#s3-result-close')).to_be_focused()
            assert_reflow(page)
            page.screenshot(path=str(REPORTS / f'scalar-live-fields-{width}.png'), animations='disabled')
            axe = os.environ.get('AXE_SCRIPT')
            if axe:
                page.evaluate(Path(axe).read_text())
                result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
                assert not result['violations'], [(v['id'], len(v['nodes'])) for v in result['violations']]
            page.keyboard.press('Escape'); expect(dialog).to_be_hidden(); expect(send_button).to_be_focused()
        # A layout change can hide the original desktop opener. Use the stable
        # first-party trigger as fallback, rather than querying Scalar internals.
        page.set_viewport_size({'width': 1440, 'height': 1000})
        send_button.focus(); page.keyboard.press('Alt+Shift+R'); expect(dialog).to_be_visible()
        page.set_viewport_size({'width': 390, 'height': 844})
        page.keyboard.press('Escape'); expect(dialog).to_be_hidden(); expect(trigger).to_be_focused()
        page.keyboard.press('Enter'); expect(dialog).to_be_visible()
        page.keyboard.press('Escape'); expect(trigger).to_be_focused()
        page.get_by_role('button', name='Close Client', exact=True).click()
        # Actual native modes through the same documented wrapper; no mock API data.
        for path in mode_paths.values():
            result = page.evaluate("""async path=>{const response=await SimulatorS3ResponseGuide.wrapFetch(fetch)(location.origin+path);return {status:response.status,body:await response.text()}}""", path)
            assert result['status'] == 200
            expect(trigger).to_be_visible()
            trigger.click(); expect(dialog).to_be_visible(); expect(dialog).to_contain_text('Live executed response')
            page.keyboard.press('Escape')
        # Synthetic parser fixtures: prove untrusted markup cannot execute and
        # reader-only work never changes Response identity, bytes or headers.
        def fixture(body, path='/', expected='explanation unavailable'):
            result = page.evaluate("""async({body,path})=>{const response=new Response(body,{status:200,headers:{'content-type':'application/xml','x-test-native':'preserved'}});const wrapped=SimulatorS3ResponseGuide.wrapFetch(async()=>response);const actual=await wrapped(location.origin+path);return {same:actual===response,body:await actual.text(),header:actual.headers.get('x-test-native')}}""", {'body': body, 'path': path})
            assert result == {'same': True, 'body': body, 'header': 'preserved'}
            expect(trigger).to_contain_text(expected)
        fixture('<!DOCTYPE x [<!ENTITY ext SYSTEM "https://example.invalid/no-network">]><ListAllMyBucketsResult><Buckets>&ext;</Buckets></ListAllMyBucketsResult>')
        fixture('<broken')
        fixture('<OtherRoot/>')
        fixture('x' * 65537)
        fixture('<ListAllMyBucketsResult>' + '<nested>' * 18 + '</nested>' * 18 + '</ListAllMyBucketsResult>')
        fixture('<ListAllMyBucketsResult><Buckets>' + '<Bucket><Name>x</Name></Bucket>' * 1000 + '</Buckets></ListAllMyBucketsResult>')
        fixture('<ListAllMyBucketsResult><Buckets><Bucket><Name><![CDATA[<img src=x onerror="globalThis.readerExecuted=true">]]></Name><CreationDate>dummy</CreationDate><BucketRegion>us-east-1</BucketRegion></Bucket></Buckets></ListAllMyBucketsResult>', expected='1 bucket in this response')
        trigger.click(); assert page.locator('#s3-result-content img').count() == 0
        assert page.evaluate('globalThis.readerExecuted === undefined')
        page.keyboard.press('Escape')
        mimic = '<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>'
        result = page.evaluate("""async body=>{let clones=0;const response=new Response(body);const original=response.clone.bind(response);response.clone=()=>{clones++;return original()};const actual=await SimulatorS3ResponseGuide.wrapFetch(async()=>response)(location.origin+'/practice-bucket/object.xml');return {same:actual===response,body:await actual.text(),clones}}""", mimic)
        assert result == {'same': True, 'body': mimic, 'clones': 0}; expect(trigger).to_be_hidden()
        # Newer request wins, including when an older response completes later.
        page.evaluate("""async()=>{let first;const slow=new Promise(resolve=>first=resolve);const wrapped=SimulatorS3ResponseGuide.wrapFetch(input=>String(input).endsWith('/')?slow:Promise.resolve(new Response('<LocationConstraint>us-west-2</LocationConstraint>')));const old=wrapped(location.origin+'/');await wrapped(location.origin+'/practice-backups?location');first(new Response('<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>'));await old} """)
        expect(trigger).to_contain_text('Bucket region: us-west-2')
        page.evaluate("async()=>await SimulatorS3ResponseGuide.wrapFetch(async()=>new Response('{}'))(location.origin+'/_simulator/docs/openapi.json')")
        expect(trigger).to_contain_text('Bucket region: us-west-2')
        # Routing inspection must never copy/access token, Cookie or Authorization values.
        result = page.evaluate("""async()=>{let reads=0;const headers={'X-Simulator-Service':'s3'};for(const name of ['Authorization','Cookie','X-Simulator-Token'])Object.defineProperty(headers,name,{enumerable:true,get(){reads++;throw Error('secret read')}});const response=new Response('<ListAllMyBucketsResult><Buckets/></ListAllMyBucketsResult>');const actual=await SimulatorS3ResponseGuide.wrapFetch(async()=>response)(location.origin+'/',{headers});return {same:actual===response,reads}}""")
        assert result == {'same': True, 'reads': 0}
        result = page.evaluate("""async()=>{let calls=0;const wrapped=SimulatorS3ResponseGuide.wrapFetch(async()=>{calls++;return new Response('unexpected')});const blocked=[];for(const url of ['https://example.invalid/','http://user:secret@'+location.host+'/']){try{await wrapped(url);blocked.push(false)}catch{blocked.push(true)}}return {calls,blocked}}""")
        assert result == {'calls': 0, 'blocked': [True, True]}
        expect(trigger).to_be_hidden()
        assert not errors and not outside, (errors, outside)
        browser.close()
    result = {'staticVsLive': True, 'actualScalarSend': True, 'sixNativeMetadataModes': True,
        'responseIdentityAndBytesUnchanged': True, 'untrustedXMLSafe': True, 'boundedFallback': True,
        'objectBodiesNotInspected': True, 'outOfOrderCorrelated': True, 'specFetchDoesNotClearLive': True,
        'keyboardAnd390Reflow': True, 'axe': bool(os.environ.get('AXE_SCRIPT')), 'externalRequests': 0,
        'embeddedSpecVerified': True, 'fullAccessibilityCertification': False}
    (REPORTS / 'scalar-s3-docs.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result))

if __name__ == '__main__':
    run_scalar_s3_docs()
