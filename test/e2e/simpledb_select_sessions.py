#!/usr/bin/env python3
"""Opt-in SimpleDB Select acceptance: SDK, served preset, IAM, isolation and restart.

Checks the exact served assets and submits the preset with a real local cookie.
This is HTTP integration, not browser rendering or a Scalar Send interaction.
No live cloud, external profiles, mocked responses, or persistent test data.
"""
import argparse
from contextlib import closing
import hashlib
from http.cookiejar import CookieJar
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
from urllib.error import HTTPError
from urllib.parse import urlencode, urlsplit
from urllib.request import (
    build_opener, HTTPCookieProcessor, HTTPRedirectHandler, ProxyHandler, Request,
)
import xml.etree.ElementTree as ET

from simpledb_scalar_reference_live import FORM, NS, VERSION, query_result

ROOT = Path(__file__).resolve().parents[2]
DOMAIN = 'example-domain'
EXPR = 'select itemName() from `example-domain` limit 25'
FIELDS = {'Action': 'Select', 'Version': VERSION, 'SelectExpression': EXPR, 'ConsistentRead': 'true'}
SERVICE = {'X-Simulator-Service': 'sdb', 'X-Simulator-Region': 'us-east-1'}
ASSETS = {
    '/_simulator/console/app.js': 'internal/server/web/app.js',
    '/_simulator/docs/openapi.json': 'internal/server/apidocs/openapi.json',
}


class LocalRedirectRefusal(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def connect(cookies=False):
    handlers = [ProxyHandler({}), LocalRedirectRefusal()]
    if cookies:
        handlers.append(HTTPCookieProcessor(CookieJar()))
    return build_opener(*handlers)


def fetch(opener, origin, path, *, method='GET', data=None, headers=None):
    assert urlsplit(origin).scheme == 'http' and urlsplit(origin).hostname == '127.0.0.1'
    assert path.startswith('/') and not path.startswith('//')
    request = Request(origin + path, method=method, data=data, headers=headers or {})
    try:
        result = opener.open(request, timeout=12)
    except HTTPError as error:
        result = error
    with result:
        return result.status, result.headers, result.read()


def form(opener, origin, fields, *, token=None, path='/', query=True):
    headers = {'Content-Type': FORM}
    if query:
        headers.update(SERVICE)
    if token is not None:
        headers['X-Simulator-Token'] = token
    return fetch(opener, origin, path, method='POST', data=urlencode(fields).encode('ascii'), headers=headers)


def require_cookie_will_send(session, origin):
    # A 401 only proves rejection of an old cookie if one would be sent.
    request = Request(origin + '/')
    [processor] = [h for h in session.handlers if isinstance(h, HTTPCookieProcessor)]
    processor.cookiejar.add_cookie_header(request)
    assert request.get_header('Cookie')


def json_control(origin, path, token, method, data):
    status, _, body = fetch(connect(), origin, path, method=method, data=json.dumps(data).encode(),
                            headers={'X-Simulator-Token':token, 'Content-Type':'application/json'})
    assert status in (200,201,204), (status,path)
    return json.loads(body) if body else None


def login(origin, token):
    session = connect(cookies=True)
    status, headers, _ = form(session, origin, {'token': token}, path='/_simulator/login', query=False)
    assert status == 303 and headers.get('Set-Cookie'), status
    assert headers['Location'] in ('/_simulator/console/', '/_simulator/console'), headers['Location']
    return session


def names(result):
    status, headers, body = result
    assert status == 200 and headers['Content-Type'].split(';')[0] == 'text/xml', status
    page = query_result(body, 'Select')
    items = page.findall(NS + 'Item')
    assert all(not item.findall(NS + 'Attribute') for item in items)
    return [item.findtext(NS + 'Name') for item in items], page.findtext(NS + 'NextToken')


def refusal(result, code):
    status, _, body = result
    assert status == (403 if code == 'AccessDenied' else 400), status
    codes = [elem.text for elem in ET.fromstring(body).iter() if elem.tag.rsplit('}', 1)[-1] == 'Code']
    assert codes == [code], codes


def official_sdk(root):
    helper = root / 'test/factory/testdata/simpledb/sdk_wire.py'
    spec = importlib.util.spec_from_file_location('simpledb_wire', helper)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def present(client):
    return client.list_domains().get('DomainNames', [])


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--binary-sha256', required=True)
    parser.add_argument('--report', type=Path)
    args=parser.parse_args()
    assert __debug__, 'run Python without optimization'
    binary = args.binary.resolve(strict=True)
    def git(*argv):
        return subprocess.check_output(['git', '-C', str(ROOT), *argv])
    assert git('rev-parse','HEAD').decode().strip()==args.revision
    assert not git('status','--porcelain','--untracked-files=no'), 'tracked checkout is modified'
    with binary.open('rb') as data:
        assert hashlib.file_digest(data,'sha256').hexdigest()==args.binary_sha256
    helper=official_sdk(ROOT)
    boto, config, _, _, _ = helper.preflight()
    report={}
    asset_hashes={}
    with tempfile.TemporaryDirectory(prefix='simpledb-select-native-cross-entry-') as state:
        state=Path(state)
        db_a, db_b = state/'team-a.db', state/'team-b.db'
        with helper.running(binary,db_b,report) as (b_endpoint,b_token), closing(
                helper.client_for(boto,config,b_endpoint,b_token)) as team_b:
            assert present(team_b)==[]
            team_b.create_domain(DomainName=DOMAIN)
            team_b.put_attributes(DomainName=DOMAIN,ItemName='team-b-only',
                                  Attributes=[{'Name':'shade','Value':'separate'}])
            with helper.running(binary,db_a,report) as (a_endpoint,a_token), closing(
                    helper.client_for(boto,config,a_endpoint,a_token)) as team_a:
                assert a_endpoint!=b_endpoint and a_token!=b_token and present(team_a)==[]
                assert fetch(connect(),a_endpoint,'/_simulator/docs/openapi.json')[0] in (303,401)
                assert fetch(connect(),a_endpoint,'/_simulator/console/')[0] in (303,401)
                assert form(connect(),b_endpoint,FIELDS,token=a_token)[0]==401
                team_a.create_domain(DomainName=DOMAIN)
                for i in range(131):
                    item=f'case-{i:03}'
                    value="o'hai & +" if i in (129,130) else 'ordinary'
                    team_a.put_attributes(DomainName=DOMAIN,ItemName=item,Attributes=[{'Name':'shade','Value':value}])
                sdk_first=team_a.select(SelectExpression=EXPR,ConsistentRead=True)
                sdk_names=[i['Name'] for i in sdk_first['Items']]
                assert sdk_names==[f'case-{i:03}' for i in range(25)]
                assert all(not row.get('Attributes') for row in sdk_first['Items'])
                session=login(a_endpoint,a_token)
                require_cookie_will_send(session,b_endpoint)
                assert form(session,b_endpoint,FIELDS)[0] == 401
                for route in ('/_simulator/docs/','/_simulator/console/'):
                    status,headers,body=fetch(session,a_endpoint,route)
                    assert status==200 and headers['Content-Type'].startswith('text/html') and body
                for route,tracked in ASSETS.items():
                    status,_,body=fetch(session,a_endpoint,route)
                    assert status==200 and body==git('show',args.revision+':'+tracked)
                    asset_hashes[route]=hashlib.sha256(body).hexdigest()
                    if route.endswith('openapi.json'):
                        openapi=json.loads(body)
                assert openapi['servers']==[{'url':'/','description':'This simulator only'}]
                preset=openapi['paths']['/']['post']['requestBody']['content'][FORM]['examples']['Select']['value']
                assert preset==FIELDS
                served_names,cursor=names(form(session,a_endpoint,preset))
                assert served_names==sdk_names and cursor and cursor==sdk_first['NextToken']
                second,_=names(form(session,a_endpoint,{**preset,'NextToken':cursor}))
                assert second==[f'case-{i:03}' for i in range(25,50)]
                spec_filter={**preset,'SelectExpression':"select itemName() from `example-domain` where `shade` = 'o''hai & +' limit 25"}
                zero,gap_cursor=names(form(session,a_endpoint,spec_filter))
                assert zero==[] and gap_cursor
                matches,end=names(form(session,a_endpoint,{**spec_filter,'NextToken':gap_cursor}))
                assert matches==['case-129','case-130'] and end is None
                refusal(form(connect(),b_endpoint,{**preset,'NextToken':cursor},token=b_token),'InvalidNextToken')
                assert names(form(connect(),b_endpoint,preset,token=b_token))==(['team-b-only'],None)

                role='simpledb-select-only'
                policy_path=f'/_simulator/v1/iam/principals/{role}/policy'
                def write_policy(effect):
                    json_control(a_endpoint,policy_path,a_token,'PUT',{
                        'Version':'2012-10-17','Statement':[{'Effect':effect,'Action':'sdb:Select',
                            'Resource':'arn:aws:sdb:us-east-1:000000000000:domain/'+DOMAIN}]})
                write_policy('Allow')
                reader=json_control(a_endpoint,'/_simulator/v1/access',a_token,'POST',{'Principal':role})['AccessToken']
                participant=login(a_endpoint,reader)
                reader_names,reader_page=names(form(participant,a_endpoint,preset))
                assert reader_names==sdk_names and reader_page
                for fields in (
                    {'Action':'GetAttributes','Version':VERSION,'DomainName':DOMAIN,'ItemName':'case-000'},
                    {'Action':'PutAttributes','Version':VERSION,'DomainName':DOMAIN,'ItemName':'case-000',
                     'Attribute.1.Name':'shade','Attribute.1.Value':'must-not-write'},
                ):
                    refusal(form(participant,a_endpoint,fields),'AccessDenied')
                write_policy('Deny')
                refusal(form(participant,a_endpoint,{**preset,'NextToken':reader_page}),'AccessDenied')
                current=team_a.get_attributes(DomainName=DOMAIN,ItemName='case-000',ConsistentRead=True)
                assert current['Attributes']==[{'Name':'shade','Value':'ordinary'}]

            with helper.running(binary,db_a,report) as (a_restarted,new_token), closing(
                    helper.client_for(boto,config,a_restarted,new_token)) as after:
                assert new_token!=a_token and present(after)==[DOMAIN]
                assert form(connect(),a_restarted,FIELDS,token=a_token)[0]==401
                require_cookie_will_send(session,a_restarted)
                assert form(session,a_restarted,FIELDS)[0] == 401
                resumed=login(a_restarted,new_token)
                refusal(form(resumed,a_restarted,{**FIELDS,'NextToken':cursor}),'InvalidNextToken')
                assert names(form(resumed,a_restarted,FIELDS))[0]==sdk_names
                assert names(form(connect(),b_endpoint,FIELDS,token=b_token))==(['team-b-only'],None)
                after.delete_domain(DomainName=DOMAIN)
                assert present(after)==[]
            assert present(team_b)==[DOMAIN]
            team_b.delete_domain(DomainName=DOMAIN)
            assert present(team_b)==[]

    assert not report.get('cleanup_errors'), report.get('cleanup_errors')
    assert not state.exists()
    assert not git('status','--porcelain','--untracked-files=no')
    result={'exact_revision':args.revision,'exact_tree':git('rev-parse','HEAD^{tree}').decode().strip(),
            'binary_sha256':args.binary_sha256,'sdk_pins':helper.PINS,'tested':[
             'SDK writes 131 items; SDK and exact served preset agree on first 25 names and cursor in the same persisted store',
             'signed local cookie serves exact Cloudscape app.js and Scalar OpenAPI; same-origin raw preset POST',
             '25-item page, matching second page, empty filter page with continuation and terminal two matches',
             'Select-only identity denied Get and Put; subsequent paging rechecks newly denied Select',
             'two concurrent process/SQLite/token and cookie boundaries; peer cursor rejected',
             'restart retains data and rejects the old operator token, signed cookie and cursor',
             'explicit domain cleanup, expected clean server shutdowns and temporary-directory removal'],
            'asset_sha256':asset_hashes,'not_claimed':['Chromium rendering or actual Scalar Send',
             'timeout/cancellation, whole E2E, real AWS or user credentials']}
    if args.report:
        args.report.write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2))


if __name__=='__main__':
    main()
