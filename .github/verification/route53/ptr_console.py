"""Check the hosted simulator's Route 53 PTR console and native readback on loopback."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
from urllib.parse import parse_qs, urlsplit
from urllib.request import ProxyHandler, Request, build_opener
from xml.etree import ElementTree as ET

import boto3
import botocore
from contextlib import closing
from datetime import datetime
from botocore.config import Config
from playwright.sync_api import expect, sync_playwright

ROOT = Path(os.environ['PRODUCT_ROOT'])
BINARY = ROOT / 'dist/tenkacloud-simulator'
NS = '{https://route53.amazonaws.com/doc/2013-04-01/}'
NAMES = ('2.0.192.in-addr.arpa.', '100.51.198.in-addr.arpa.')
PARTICIPANT = 'ptr-console-review'


def record(name, ttl, value):
    return {'Name': name, 'Type': 'PTR', 'TTL': ttl, 'ResourceRecords': [{'Value': value}]}


def control(origin, token, method, path, data):
    req = Request(origin + path, data=json.dumps(data).encode(), method=method,
                  headers={'X-Simulator-Token': token, 'Content-Type': 'application/json'})
    with build_opener(ProxyHandler({})).open(req, timeout=5) as res:
        return json.load(res)


def native_client(origin, token):
    client = boto3.client('route53', endpoint_url=origin, region_name='us-east-1',
                          aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
                          config=Config(proxies={}, retries={'max_attempts': 0}, connect_timeout=3, read_timeout=5))
    client.meta.events.register('before-sign.route53', lambda request, **_: request.headers.add_header('X-Simulator-Token', token))

    def local(request, **_):
        assert request.url.startswith(origin + '/2013-04-01/'), 'SDK destination changed'

    client.meta.events.register('before-send.route53', local)
    return client


def change(client, zone, action, rr):
    return client.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={'Changes': [{'Action': action, 'ResourceRecordSet': rr}]})


def answer(client, zone, rr):
    response = client.test_dns_answer(HostedZoneId=zone, RecordName=rr['Name'], RecordType='PTR')
    return response['ResponseCode'], response.get('RecordData', [])


def allowed_session(origin, token, zones):
    arns = ['arn:aws:route53:::' + zone.lstrip('/') for zone in zones]
    policy = {'Version': '2012-10-17', 'Statement': [
        {'Effect': 'Allow', 'Action': 'route53:ListHostedZones', 'Resource': '*'},
        {'Effect': 'Allow', 'Action': ['route53:GetHostedZone', 'route53:ListResourceRecordSets'], 'Resource': arns},
        {'Effect': 'Allow', 'Action': 'route53:ChangeResourceRecordSets', 'Resource': arns[0]}]}
    control(origin, token, 'PUT', '/_simulator/v1/iam/principals/' + PARTICIPANT + '/policy', policy)
    return control(origin, token, 'POST', '/_simulator/v1/access', {'Principal': PARTICIPANT})['AccessToken']


def verify_reference(context, origin):
    response = context.request.get(origin + '/_simulator/docs/openapi.json', max_redirects=0)
    assert response.status == 200
    paths = response.json()['paths']
    records = paths['/2013-04-01/hostedzone/{Id}/rrset']
    assert records['get']['operationId'] == 'ListResourceRecordSets'
    assert records['post']['operationId'] == 'ChangeResourceRecordSets' and 'PTR' in records['post']['description']
    assert paths['/2013-04-01/testdnsanswer']['get']['operationId'] == 'TestDNSAnswer'


def exercise(browser, origin, participant, client, zones, baseline, siblings, wanted):
    context = browser.new_context(service_workers='block', locale='en-US', viewport={'width': 1365, 'height': 900})
    writes, native_requests, outside, page_errors = [], [], [], []

    def guard(route):
        if route.request.url.startswith(origin + '/'):
            route.continue_()
        else:
            outside.append(urlsplit(route.request.url).scheme)
            route.abort()

    try:
        context.route('**/*', guard)
        assert context.request.post(origin + '/_simulator/login', form={'token': participant}, max_redirects=0).status == 303
        verify_reference(context, origin)
        page = context.new_page()
        page.on('pageerror', lambda error: page_errors.append(type(error).__name__))
        def observe(request):
            if '/2013-04-01/' in request.url:
                native_requests.append(request)
                if request.method not in ('GET', 'HEAD', 'OPTIONS'):
                    writes.append(request)
        page.on('request', observe)
        page.goto(origin + '/_simulator/console/')
        page.get_by_role('button', name='Services', exact=True).click()
        page.get_by_role('menuitem', name='Route 53', exact=True).click()
        expect(page.get_by_role('heading', level=1, name='Route 53', exact=True)).to_be_visible()
        for i, (zone, rr) in enumerate(zip(zones, wanted)):
            page.get_by_role('button', name=NAMES[i], exact=True).click()
            rows = page.get_by_role('table', name='Route 53 record sets', exact=True)
            expect(rows.get_by_role('cell', name=siblings[i]['Name'], exact=True)).to_be_visible()
            page.get_by_label('Full record name', exact=True).fill(rr['Name'])
            page.get_by_label('TTL (seconds)', exact=True).fill(str(rr['TTL']))
            page.get_by_label('PTR target', exact=True).fill(rr['ResourceRecords'][0]['Value'])
            endpoint = origin + '/2013-04-01' + zone + '/rrset'
            assert len(writes) == i
            with page.expect_response(lambda r: r.request.method == 'POST' and r.url == endpoint) as sent:
                page.get_by_role('button', name='Create PTR record', exact=True).click()
            response = sent.value
            assert len(writes) == i+1 and response.request is writes[-1]
            headers = response.request.all_headers()
            assert headers.get('cookie') and headers['x-simulator-service'] == 'route53'
            assert headers.get('x-simulator-principal') == PARTICIPANT
            assert not {'authorization', 'x-simulator-token', 'x-simulator-access-role'}.intersection(headers)
            body = ET.fromstring(response.request.post_data)
            changes = body.findall('./'+NS+'ChangeBatch/'+NS+'Changes/'+NS+'Change')
            assert len(changes) == 1 and changes[0].findtext(NS+'Action') == 'CREATE'
            entry = changes[0].find(NS+'ResourceRecordSet')
            assert entry is not None and entry.findtext(NS+'Name') == rr['Name'] and entry.findtext(NS+'Type') == 'PTR'
            assert entry.findtext(NS+'TTL') == str(rr['TTL']) and [e.text for e in entry.iter(NS+'Value')] == [rr['ResourceRecords'][0]['Value']]
            xml = ET.fromstring(response.body())
            if i == 0:
                assert response.status == 200 and xml.findtext('.//'+NS+'Status') == 'INSYNC'
                receipt_id = xml.findtext('.//'+NS+'Id')
                assert receipt_id and re.fullmatch('/change/C[A-Z0-9]+', receipt_id)
                submitted = datetime.fromisoformat(xml.findtext('.//'+NS+'SubmittedAt').replace('Z', '+00:00'))
                assert submitted.utcoffset() is not None
                expect(page.get_by_text(receipt_id, exact=True)).to_be_visible()
                row = rows.get_by_role('row').filter(has=page.get_by_role('cell', name=rr['Name'], exact=True))
                for value in ['PTR', str(rr['TTL']), rr['ResourceRecords'][0]['Value']]:
                    expect(row.get_by_role('cell', name=value, exact=True)).to_be_visible()
                reads = [req for req in native_requests[native_requests.index(response.request)+1:]
                         if req.method == 'GET' and req.url.startswith(endpoint+'?')]
                assert len(reads) == 1
                params = parse_qs(urlsplit(reads[0].url).query)
                assert params['name'] == [rr['Name']] and params['type'] == ['PTR']
                readback = reads[0].response()
                assert readback and readback.status == 200
                listed = ET.fromstring(readback.body()).find(NS+'ResourceRecordSets/'+NS+'ResourceRecordSet')
                assert listed is not None and listed.findtext(NS+'Name') == rr['Name']
                assert listed.findtext(NS+'Type') == 'PTR' and listed.findtext(NS+'TTL') == str(rr['TTL'])
                assert [e.text for e in listed.iter(NS+'Value')] == [rr['ResourceRecords'][0]['Value']]
                assert client.get_change(Id=receipt_id)['ChangeInfo']['Status'] == 'INSYNC'
                assert answer(client, zone, rr) == ('NOERROR', [rr['ResourceRecords'][0]['Value']])
                assert rr in client.list_resource_record_sets(HostedZoneId=zone)['ResourceRecordSets']
                page.get_by_role('button', name='Back to hosted zones', exact=True).click()
            else:
                assert response.status == 403 and xml.tag == NS+'ErrorResponse'
                code = xml.findtext(NS+'Error/'+NS+'Code')
                message = xml.findtext(NS+'Error/'+NS+'Message')
                assert (code, message) == ('AccessDenied', 'Route53 action is not permitted.')
                expect(page.get_by_text(f'{code}: {message}', exact=True)).to_be_visible()
                expect(page.get_by_text('No save was confirmed. Refresh the records before submitting again.', exact=True)).to_be_visible()
                expect(page.get_by_role('button', name='Create PTR record', exact=True)).to_be_disabled()
                assert page.get_by_text('Verified local PTR record:', exact=False).count() == 0
                assert client.list_resource_record_sets(HostedZoneId=zone)['ResourceRecordSets'] == baseline[i]
                assert answer(client, zone, rr) == ('NXDOMAIN', [])
            assert answer(client, zone, siblings[i]) == ('NOERROR', [siblings[i]['ResourceRecords'][0]['Value']])
        assert [req.method for req in writes] == ['POST', 'POST'] and not outside and not page_errors
        for i, zone in enumerate(zones):
            current = client.list_resource_record_sets(HostedZoneId=zone)['ResourceRecordSets']
            expected = baseline[i] + ([wanted[0]] if i == 0 else [])
            assert sorted(current, key=lambda r: (r['Name'], r['Type'])) == sorted(expected, key=lambda r: (r['Name'], r['Type']))
    finally:
        context.close()


def run():
    assert {name for _, name in socket.if_nameindex()} == {'lo'} and os.getuid() != 0
    assert boto3.__version__ == '1.42.0' and botocore.__version__ == '1.41.6'
    revision = subprocess.check_output(['git', '-C', str(ROOT), 'rev-parse', 'HEAD'], text=True).strip()
    assert revision == os.environ['PRODUCT_REVISION']
    assert not subprocess.check_output(['git','-C',str(ROOT),'status','--porcelain'])
    digest = hashlib.sha256(BINARY.read_bytes()).hexdigest()
    assert digest == os.environ['PRODUCT_DIGEST']
    spec = importlib.util.spec_from_file_location('local_cli', ROOT/'test/factory/testdata/simpledb/sdk_wire.py')
    harness = importlib.util.module_from_spec(spec); spec.loader.exec_module(harness)
    cleanup = {}
    with tempfile.TemporaryDirectory(prefix='route53-ptr-console-') as tmp, harness.running(BINARY, Path(tmp)/'state.db', cleanup) as (origin, token):
        assert urlsplit(origin).hostname == '127.0.0.1'
        with closing(native_client(origin, token)) as client:
            zones, siblings, wanted, baseline = [], [], [], []
            try:
                for i, name in enumerate(NAMES):
                    zone = client.create_hosted_zone(Name=name, CallerReference=f'local-ptr-{i}')['HostedZone']['Id']
                    zones.append(zone)
                    sibling = record('3.'+name, 30, 'existing.example.test.'); siblings.append(sibling)
                    change(client, zone, 'CREATE', sibling)
                    wanted.append(record('7.'+name, 60, 'console.example.test.'))
                    baseline.append(client.list_resource_record_sets(HostedZoneId=zone)['ResourceRecordSets'])
                token_for_browser = allowed_session(origin, token, zones)
                with sync_playwright() as playwright, playwright.chromium.launch(chromium_sandbox=True) as browser:
                    exercise(browser, origin, token_for_browser, client, zones, baseline, siblings, wanted)
            finally:
                for i, zone in reversed(list(enumerate(zones))):
                    for rr in client.list_resource_record_sets(HostedZoneId=zone)['ResourceRecordSets']:
                        if rr in siblings+wanted:
                            change(client, zone, 'DELETE', rr)
                    client.delete_hosted_zone(Id=zone)
            assert client.list_hosted_zones()['HostedZones'] == []
    assert not Path(tmp).exists() and not cleanup.get('cleanup_errors')
    print(json.dumps({'revision':revision, 'binarySha256':digest, 'browser':'Chromium', 'sdk':boto3.__version__, 'allowed':200, 'denied':403, 'browserMutationRequests':2, 'freshNativeReadback':True, 'referenceContract':True, 'boundedDNSAnswer':True, 'siblingsPreserved':True, 'cleaned':True}))


if __name__ == '__main__':
    run()
