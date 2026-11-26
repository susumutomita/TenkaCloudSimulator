"""Opt-in SimpleDB acceptance: served Cloudscape DOM, pinned SDK, served Query presets.

Runs the unmodified embedded HTML/JS in pinned jsdom (not Chromium) and checks
actual authenticated localhost requests. No mocked HTTP, external installs,
live cloud, existing credentials, or input endpoints.
"""
import argparse
from contextlib import closing, contextmanager
import hashlib
from http.cookiejar import CookieJar
import json
from pathlib import Path
import subprocess
from tempfile import TemporaryDirectory
from urllib.parse import urlsplit
from urllib.request import HTTPCookieProcessor, Request

from simpledb_create_domain_reference_live import PRESET, served_presets, submit
from simpledb_scalar_reference_live import GET_FIELDS, LIST_FIELDS, item_values, listed_domains
from simpledb_select_sessions import ROOT, form, json_control, login, official_sdk, require_cookie_will_send

DOMAIN = PRESET['DomainName']
ITEM = GET_FIELDS['ItemName']
TEAM_A = [('source', 'SDK team A + & 値')]
TEAM_B = [('source', 'SDK team B + & 旗')]
CHANGED = ('console-result', 'Cloudscape & + 保存')
ASSETS = {
    '/_simulator/console/': 'internal/server/web/index.html',
    '/_simulator/console/app.js': 'internal/server/web/app.js',
}
DOM_RUNNER = Path(__file__).with_name('simpledb_crosssurface_dom.cjs')


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


@contextmanager
def team(sdk, binary, data, report, boto3, config):
    with sdk.running(binary, data, report) as (origin, credential), closing(
            sdk.client_for(boto3, config, origin, credential)) as client:
        yield origin, credential, client, login(origin, credential)


def session_cookie(session, origin):
    [jar] = [h.cookiejar for h in session.handlers if isinstance(h, HTTPCookieProcessor)]
    assert isinstance(jar, CookieJar)
    request = Request(origin + '/')
    jar.add_cookie_header(request)
    cookie = request.get_header('Cookie')
    assert cookie
    return cookie


def dom(args, hashes, events, origin, session, mode, before=(), change=CHANGED):
    host = urlsplit(origin)
    assert host.scheme == 'http' and host.hostname == '127.0.0.1' and host.port
    field, value = change
    payload = {'port':host.port,'cookie':session_cookie(session,origin),'hashes':hashes,
               'item':ITEM,'domain':DOMAIN,'before':before,'mode':mode,'attribute':field,'value':value}
    result = subprocess.run([str(args.node), '--no-addons', str(DOM_RUNNER)], input=json.dumps(payload),
                            env={'JSDOM_MODULE':str(args.jsdom),'HOME':str(args.private_home),'LANG':'C.UTF-8'},
                            cwd=args.private_home, text=True, stdout=subprocess.PIPE, check=True, timeout=26)
    observed = json.loads(result.stdout)
    assert observed['mode'] == mode
    events.append(observed)
    return observed


def ensure_domains(client, session, origin, expected):
    assert client.list_domains().get('DomainNames', []) == expected
    preset = served_presets(origin, session)['ListDomains']['value']
    assert preset == LIST_FIELDS
    status, _, wire = form(session, origin, preset)
    assert status == 200 and listed_domains(wire) == expected


def consistent_state(client, session, origin, expected):
    out = client.get_attributes(DomainName=DOMAIN, ItemName=ITEM, ConsistentRead=True)
    assert sorted((a['Name'],a['Value']) for a in out.get('Attributes', [])) == sorted(expected)
    preset = served_presets(origin, session)['GetAttributes']['value']
    assert preset == GET_FIELDS
    status, _, wire = form(session, origin, preset)
    assert status == 200 and item_values(wire) == sorted(expected)


def exercise(args, sdk, hashes):
    boto3, config, _, _, _ = sdk.preflight()
    cleanup, events = {}, []
    with TemporaryDirectory(prefix='simpledb-crosssurface-') as directory:
        args.private_home = Path(directory)
        a_db, b_db = args.private_home / 'team-a.sqlite', args.private_home / 'team-b.sqlite'
        with team(sdk,args.binary,b_db,cleanup,boto3,config) as (b_origin,b_token,b,b_ref):
            with team(sdk,args.binary,a_db,cleanup,boto3,config) as (a_origin,a_token,a,a_ref):
                assert a_origin != b_origin and a_token != b_token
                ensure_domains(a,a_ref,a_origin,[])
                ensure_domains(b,b_ref,b_origin,[])
                # Prepare the same domain and item name independently; never share
                # a cookie, process, SQLite file, or SDK client between teams.
                create = served_presets(a_origin,a_ref)['CreateDomain']['value']
                assert create == PRESET
                submit(form(a_ref,a_origin,create))
                b.create_domain(DomainName=DOMAIN)
                for client, own in ((a,TEAM_A),(b,TEAM_B)):
                    client.put_attributes(DomainName=DOMAIN, ItemName=ITEM,
                                          Attributes=[{'Name':name,'Value':value} for name,value in own])
                for client, ref, origin, own in ((a,a_ref,a_origin,TEAM_A),(b,b_ref,b_origin,TEAM_B)):
                    ensure_domains(client,ref,origin,[DOMAIN])
                    consistent_state(client,ref,origin,own)

                dom(args,hashes,events,a_origin,a_ref,'write',TEAM_A)
                consistent_state(a,a_ref,a_origin,[*TEAM_A,CHANGED])
                consistent_state(b,b_ref,b_origin,TEAM_B)

                principal = 'simpledb-crosssurface-read-only'
                json_control(b_origin,'/_simulator/v1/iam/principals/'+principal+'/policy',b_token,'PUT',{
                    'Version':'2012-10-17','Statement':[
                        {'Effect':'Allow','Action':'sdb:ListDomains','Resource':'*'},
                        {'Effect':'Allow','Action':'sdb:GetAttributes',
                         'Resource':'arn:aws:sdb:us-east-1:000000000000:domain/'+DOMAIN},
                    ],
                })
                credential = json_control(b_origin,'/_simulator/v1/access',b_token,'POST',
                                          {'Principal':principal})['AccessToken']
                dom(args,hashes,events,b_origin,login(b_origin,credential),'deny',TEAM_B,
                    change=('console-result','must not persist'))
                consistent_state(b,b_ref,b_origin,TEAM_B)
                consistent_state(a,a_ref,a_origin,[*TEAM_A,CHANGED])

                # On loopback the cookie path would otherwise include both
                # ports. This ordinary denied request is to our synthetic peer.
                require_cookie_will_send(a_ref,b_origin)
                assert form(a_ref,b_origin,GET_FIELDS)[0] == 401

            with team(sdk,args.binary,a_db,cleanup,boto3,config) as (again,new_token,reopened,ref):
                assert new_token not in (a_token,b_token)
                require_cookie_will_send(a_ref,again)
                assert form(a_ref,again,GET_FIELDS)[0] == 401
                consistent_state(reopened,ref,again,[*TEAM_A,CHANGED])
                consistent_state(b,b_ref,b_origin,TEAM_B)
                dom(args,hashes,events,again,ref,'delete',[*TEAM_A,CHANGED])
                consistent_state(reopened,ref,again,TEAM_A)
                consistent_state(b,b_ref,b_origin,TEAM_B)
                reopened.delete_domain(DomainName=DOMAIN)
                ensure_domains(reopened,ref,again,[])
                dom(args,hashes,events,again,ref,'empty')
            dom(args,hashes,events,b_origin,b_ref,'read',TEAM_B)
            b.delete_domain(DomainName=DOMAIN)
            ensure_domains(b,b_ref,b_origin,[])
            dom(args,hashes,events,b_origin,b_ref,'empty')
    assert not Path(directory).exists() and not cleanup.get('cleanup_errors'), cleanup
    return events


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--binary', required=True,type=Path)
    parser.add_argument('--binary-sha256', required=True)
    parser.add_argument('--node', required=True,type=Path,help='approved Node v24.22.3 executable')
    parser.add_argument('--jsdom', required=True,type=Path,help='approved installed jsdom 30.0.1 package directory')
    args = parser.parse_args()
    if not __debug__:
        raise RuntimeError('Run without Python optimization')

    def git(*argv):
        return subprocess.check_output(['git', '-C', str(ROOT), *argv])

    assert git('rev-parse', 'HEAD').decode().strip() == args.revision
    assert not git('status', '--porcelain', '--untracked-files=no'), 'tracked checkout is modified'
    args.binary, args.node, args.jsdom = (p.resolve(strict=True) for p in (args.binary,args.node,args.jsdom))
    assert digest(args.binary) == args.binary_sha256
    sdk = official_sdk(ROOT)
    paths = {**ASSETS, '/_simulator/docs/openapi.json': 'internal/server/apidocs/openapi.json'}
    hashes = {}
    for route, path in paths.items():
        expected = hashlib.sha256(git('show', args.revision + ':' + path)).hexdigest()
        assert digest(ROOT / path) == expected, 'worktree asset differs from exact Git blob'
        hashes[route] = expected
    events = exercise(args, sdk, hashes)
    print(json.dumps({
        'revision':args.revision,'tree':git('rev-parse', 'HEAD^{tree}').decode().strip(),'binary_sha256':args.binary_sha256,
        'sdk_pins':sdk.PINS,'node_sha256':digest(args.node),
        'jsdom_manifest_sha256':digest(args.jsdom / 'package.json'),'verified_assets':hashes,'dom_stages':events,
        'retained_after_restart':True,'independent_two_team_scopes':True,
        'peer_and_stale_session_rejected':True,'cleaned':True,
        'limits':'DOM via jsdom; API reference templates replayed over HTTP. No Chromium, visual layout, Scalar Send UI or live AWS.',
    },indent=2))


if __name__ == '__main__':
    main()
