"""Opt-in exact-binary SimpleDB CreateDomain reference/SDK acceptance.

Uses the served Query example, a real local cookie, and the pinned official
SDK over numeric loopback. No browser rendering, live cloud, or external state.
"""
import argparse
from contextlib import closing
import hashlib
import json
from pathlib import Path
import re
import subprocess
from tempfile import TemporaryDirectory
from xml.etree import ElementTree as ET

from simpledb_scalar_reference_live import FORM, NS, VERSION, query_result
from simpledb_select_sessions import ROOT, connect, fetch, form, json_control, login, official_sdk, refusal

SPEC = 'internal/server/apidocs/openapi.json'
PRESET = {'Action': 'CreateDomain', 'Version': VERSION, 'DomainName': 'scalar-practice-domain'}
ITEM = {'DomainName': PRESET['DomainName'], 'ItemName': 'create-domain-replay'}
VALUE = {'Name': 'status', 'Value': 'retained + & 値'}


def served_presets(origin, session):
    status, _, raw = fetch(session, origin, '/_simulator/docs/openapi.json')
    assert status == 200, status
    assert raw == (ROOT / SPEC).read_bytes(), 'served reference differs from exact checkout'
    return json.loads(raw)['paths']['/']['post']['requestBody']['content'][FORM]['examples']


def submit(posted):
    status, headers, wire = posted
    assert status == 200 and headers.get_content_type() == 'text/xml', status
    response = ET.fromstring(wire)
    assert response.tag == NS + 'CreateDomainResponse'
    assert [row.tag for row in response] == [NS + 'ResponseMetadata']
    assert re.fullmatch('[a-f0-9]{32}', response.findtext(NS + 'ResponseMetadata/' + NS + 'RequestId') or '')


def see_value(sdk):
    assert sdk.get_attributes(**ITEM, ConsistentRead=True).get('Attributes') == [VALUE]


def exercise(binary, sdk, report):
    boto3, config, ClientError, _, _ = sdk.preflight()
    with TemporaryDirectory(prefix='simpledb-create-domain-reference-') as path:
        db = Path(path) / 'state.db'
        with sdk.running(binary, db, report) as (origin, operator), closing(
                sdk.client_for(boto3, config, origin, operator)) as client:
            read_session = login(origin, operator)
            preset = served_presets(origin, read_session)['CreateDomain']['value']
            assert preset == PRESET
            assert client.list_domains().get('DomainNames', []) == []

            # A request with no authority, and one with the right action but
            # a different domain, must both leave the private DB unchanged.
            assert form(connect(), origin, preset)[0] == 401
            principal = 'simpledb-reference-create-only'
            domain = preset['DomainName']
            json_control(origin, '/_simulator/v1/iam/principals/' + principal + '/policy', operator, 'PUT', {
                'Version': '2012-10-17', 'Statement': [{
                    'Effect': 'Allow', 'Action': 'sdb:CreateDomain',
                    'Resource': 'arn:aws:sdb:us-east-1:000000000000:domain/' + domain,
                }],
            })
            token = json_control(origin, '/_simulator/v1/access', operator, 'POST',
                                 {'Principal': principal})['AccessToken']
            writer = login(origin, token)
            refusal(form(writer, origin, {**preset, 'DomainName': 'different-practice-domain'}), 'AccessDenied')
            assert client.list_domains().get('DomainNames', []) == []

            submit(form(writer, origin, preset))
            assert client.list_domains().get('DomainNames', []) == [domain]
            assert client.domain_metadata(DomainName=domain)['ItemCount'] == 0
            client.put_attributes(**ITEM, Attributes=[VALUE])
            submit(form(writer, origin, preset))
            see_value(client)
            print('Served form: 200 create/repeat with one saved domain and retained SDK item; 401 unauthenticated; 403 wrong domain')

        with sdk.running(binary, db, report) as (origin, operator), closing(
                sdk.client_for(boto3, config, origin, operator)) as client:
            assert client.list_domains().get('DomainNames', []) == [domain]
            assert client.domain_metadata(DomainName=domain)['ItemCount'] == 1
            see_value(client)
            client.delete_domain(DomainName=domain)
            try:
                client.domain_metadata(DomainName=domain)
            except ClientError as error:
                assert (error.response['ResponseMetadata']['HTTPStatusCode'],
                        error.response['Error']['Code']) == (400, 'NoSuchDomain')
            else:
                raise AssertionError('SDK still sees the deleted practice domain')
            session = login(origin, operator)
            example = served_presets(origin, session)['ListDomains']['value']
            status, _, body = form(session, origin, example)
            assert status == 200
            assert query_result(body, 'ListDomains').findall(NS + 'DomainName') == []
    assert not Path(path).exists(), 'private SQLite state must be removed'
    assert not report.get('cleanup_errors'), report.get('cleanup_errors')
    print('Pinned SDK: saved state survived restart; DeleteDomain observed by SDK and served ListDomains; temporary state removed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--binary-sha256', required=True)
    args = parser.parse_args()
    if not __debug__:
        raise RuntimeError('Run without Python optimization')

    def git(*argv):
        return subprocess.check_output(['git', '-C', str(ROOT), *argv])

    assert git('rev-parse', 'HEAD').decode().strip() == args.revision
    assert not git('status', '--porcelain', '--untracked-files=no'), 'tracked checkout is modified'
    binary = args.binary.resolve(strict=True)
    with binary.open('rb') as data:
        assert hashlib.file_digest(data, 'sha256').hexdigest() == args.binary_sha256
    exercise(binary, official_sdk(ROOT), {})
    print(f'PASS: source {args.revision}, binary SHA-256 {args.binary_sha256}')


if __name__ == '__main__':
    main()
