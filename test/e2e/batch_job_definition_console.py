"""Opt-in Batch metadata receipt: exact served Cloudscape DOM, SDK and API reference.

No browser layout, mocked HTTP, job execution, cloud profiles or real AWS.
Pass the pinned, already installed Node/jsdom/SDK and a locally built exact CLI.
"""
import argparse
from contextlib import ExitStack, closing
import hashlib
import json
import os
from pathlib import Path
import subprocess
from tempfile import TemporaryDirectory

from simpledb_crosssurface import digest, session_cookie
from simpledb_select_sessions import ROOT, fetch, json_control, login, official_sdk, require_cookie_will_send

FAMILY = 'console-verification'
FAMILY_ARN = 'arn:aws:batch:us-east-1:000000000000:job-definition/' + FAMILY
ASSETS = {
    '/_simulator/console/': 'internal/server/web/index.html',
    '/_simulator/console/app.js': 'internal/server/web/app.js',
    '/_simulator/docs/catalog/batch.json': 'internal/server/apidocs/catalog/batch.json',
}
DESCRIPTORS = '/_simulator/v1/catalog/aws'
REFERENCE = '/_simulator/docs/catalog/batch.json'
NATIVE = {'X-Simulator-Service': 'batch', 'X-Simulator-Region': 'us-east-1', 'Content-Type': 'application/json'}
OPS = {'create': 'RegisterJobDefinition', 'describe': 'DescribeJobDefinitions', 'update': 'DeregisterJobDefinition'}


def arn(rev):
    return f'{FAMILY_ARN}:{rev}'


def image(label):
    return 'example.invalid/metadata:' + label


def definition(label, cpu='1', memory='512'):
    return {'jobDefinitionName': FAMILY, 'type': 'container', 'platformCapabilities': ['EC2'],
            'containerProperties': {'image': image(label), 'resourceRequirements': [
                {'type': 'VCPU', 'value': cpu}, {'type': 'MEMORY', 'value': memory}]}}


def served(origin, session, client, hashes):
    status, _, body = fetch(session, origin, DESCRIPTORS)
    assert status == 200
    batch, = [s for s in json.loads(body)['services'] if s['model'] == 'batch']
    resource, = [r for r in batch['resources'] if r['kind'] == 'jobDefinition']
    assert (batch['protocol'], batch['signingName'], resource['arnWire']) == ('rest-json', 'batch', 'jobDefinitionArn')
    assert set(resource['operations']) == {'create', 'list', 'describe', 'update'}
    status, _, blob = fetch(session, origin, REFERENCE)
    assert status == 200 and hashlib.sha256(blob).hexdigest() == hashes[REFERENCE]
    document = json.loads(blob)
    operations = {}
    for field, name in OPS.items():
        operation = resource['operations'][field]
        official = client.meta.service_model.operation_model(name)
        assert (operation['name'], operation['method'], operation['uri']) == (name, 'POST', official.http['requestUri'])
        reference = document['paths'][operation['uri']]['post']
        assert reference['operationId'] == 'batch' + name
        assert set(reference['requestBody']['content']['application/json']['schema'].get('required', ())) == set(official.input_shape.required_members)
        operations[field] = (operation['method'], operation['uri'])
    for name in ('SubmitJob', 'SubmitServiceJob', 'ListJobs', 'CancelJob', 'TerminateJob'):
        assert name not in batch['operations']
        assert client.meta.service_model.operation_model(name).http['requestUri'] not in document['paths']
    return operations


def native(team, session, operations, key, body, expected=200):
    method, path = operations[key]
    status, _, result = fetch(session, team.endpoint, path, method=method, headers=NATIVE, data=json.dumps(body).encode())
    assert status == expected, (key, status, expected)
    return json.loads(result) if result else None


def same(team, session, client, operations, expected):
    by_name = {'jobDefinitionName': FAMILY}
    rows = client.describe_job_definitions(**by_name)['jobDefinitions']
    assert rows == native(team, session, operations, 'describe', by_name)['jobDefinitions']
    assert [(r['revision'], r['status'], r['containerProperties']['image']) for r in rows] == [
        (rev, status, image(label)) for rev, status, label in expected]
    for row in rows:
        by_id = {'jobDefinitions': [arn(row['revision'])]}
        assert row['jobDefinitionArn'] == by_id['jobDefinitions'][0]
        assert client.describe_job_definitions(**by_id)['jobDefinitions'] == [row]
        assert native(team, session, operations, 'describe', by_id)['jobDefinitions'] == [row]
    return rows


def dom(args, directory, hashes, team, session, mode, rows):
    payload = {'port':team.port, 'cookie':session_cookie(session, team.endpoint), 'hashes':hashes, 'family':FAMILY,
               'mode':mode, 'seedRows':[(rev,status) for rev,status,_ in rows]}
    runner = Path(__file__).with_name('batch_job_definition_console.cjs')
    result = subprocess.run([str(args.node), '--no-addons', str(runner)], input=json.dumps(payload), text=True,
                            env={'JSDOM_MODULE':str(args.jsdom), 'HOME':directory, 'LANG':'C.UTF-8'},
                            cwd=directory, stdout=subprocess.PIPE, check=True, timeout=35)
    observed = json.loads(result.stdout)
    assert observed['mode'] == mode and not (observed['violations'] or observed['errors'])
    return observed


def exercise(args, hashes):
    official_sdk(ROOT).preflight()
    os.environ['SIMULATOR_BINARY'] = str(args.binary)
    from aws_catalog import Team, refused

    a_seed = [(1, 'ACTIVE', 'sdk-a1'), (2, 'ACTIVE', 'reference-a2')]
    a_after = [a_seed[0], (2, 'INACTIVE', 'reference-a2'), (3, 'ACTIVE', 'console')]
    b_seed = [(1, 'ACTIVE', 'sdk-b1'), (2, 'ACTIVE', 'sdk-b2')]
    stages = []
    with TemporaryDirectory(prefix='batch-job-definition-console-') as directory, ExitStack() as stack:
        a_path, b_path = Path(directory) / 'a', Path(directory) / 'b'
        a_path.mkdir()
        b_path.mkdir()
        a, b = Team(a_path), Team(b_path)
        assert a.endpoint != b.endpoint and a.database != b.database
        for team in (a, b):
            stack.callback(lambda team=team: team.process is not None and team.stop())
            team.start()
        assert a.token != b.token
        sdk_a = stack.enter_context(closing(a.client('batch')))
        sdk_b = stack.enter_context(closing(b.client('batch')))
        ref_a, ref_b = login(a.endpoint, a.token), login(b.endpoint, b.token)
        route_a, route_b = served(a.endpoint, ref_a, sdk_a, hashes), served(b.endpoint, ref_b, sdk_b, hashes)
        assert route_a == route_b
        same(a, ref_a, sdk_a, route_a, [])
        same(b, ref_b, sdk_b, route_b, [])
        assert sdk_a.register_job_definition(**definition('sdk-a1'))['jobDefinitionArn'] == arn(1)
        assert native(a, ref_a, route_a, 'create', definition('reference-a2'))['jobDefinitionArn'] == arn(2)
        for rev, _, label in b_seed:
            assert sdk_b.register_job_definition(**definition(label))['jobDefinitionArn'] == arn(rev)
        same(a, ref_a, sdk_a, route_a, a_seed)
        before_b = same(b, ref_b, sdk_b, route_b, b_seed)

        stages.append(dom(args,directory,hashes,a,ref_a,'write',a_seed))
        after_a = same(a, ref_a, sdk_a, route_a, a_after)
        assert after_a[2]['containerProperties'] == definition('console', '2', '1024')['containerProperties']
        assert same(b, ref_b, sdk_b, route_b, b_seed) == before_b
        refused('UnknownOperationException', sdk_a.list_jobs, 501)

        principal = 'batch-console-describe-only'
        json_control(b.endpoint, '/_simulator/v1/iam/principals/' + principal + '/policy', b.token, 'PUT', {
            'Version':'2012-10-17',
            'Statement':[
                {'Effect':'Allow', 'Action':['batch:DescribeJobDefinitions','batch:ListSchedulingPolicies'], 'Resource':'*'},
                {'Effect':'Allow', 'Action':'batch:ListTagsForResource', 'Resource':arn(2)},
            ],
        })
        limited = json_control(b.endpoint, '/_simulator/v1/access', b.token, 'POST', {'Principal':principal})['AccessToken']
        stages.append(dom(args,directory,hashes,b,login(b.endpoint,limited),'deny',b_seed))
        assert same(b, ref_b, sdk_b, route_b, b_seed) == before_b
        require_cookie_will_send(ref_a, b.endpoint)
        native(b, ref_a, route_b, 'describe', {'jobDefinitions':[arn(2)]}, expected=401)

        a.stop()
        a.start()
        require_cookie_will_send(ref_a,a.endpoint)
        native(a, ref_a, route_a, 'describe', {'jobDefinitions':[arn(2)]}, expected=401)
        new_ref = login(a.endpoint,a.token)
        with closing(a.client('batch')) as restarted:
            assert same(a,new_ref,restarted,route_a,a_after) == after_a
            stages.append(dom(args,directory,hashes,a,new_ref,'reopen',a_after))
        assert same(b, ref_b, sdk_b, route_b, b_seed) == before_b

        b_after_leave = [(1, 'ACTIVE', 'sdk-b1'), (2, 'INACTIVE', 'sdk-b2'), (3, 'ACTIVE', 'console')]
        stages.append(dom(args,directory,hashes,b,ref_b,'leave',b_seed))
        assert same(b,ref_b,sdk_b,route_b,b_after_leave)[2]['containerProperties'] == definition('console', '2', '1024')['containerProperties']
        assert same(a,new_ref,sdk_a,route_a,a_after) == after_a

        # INACTIVE metadata is retained by design. Clean any active fixture
        # registrations through native SDK, then drop both disposable stores.
        for team, session, client, operations, expected in (
                (a,new_ref,sdk_a,route_a,a_after), (b,ref_b,sdk_b,route_b,b_after_leave)):
            for rev,status,_ in expected:
                if status == 'ACTIVE':
                    client.deregister_job_definition(jobDefinition=arn(rev))
            same(team,session,client,operations,[(rev,'INACTIVE',label) for rev,_,label in expected])
    assert not Path(directory).exists()
    return stages


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--binary-sha256', required=True)
    parser.add_argument('--node', type=Path, required=True)
    parser.add_argument('--jsdom', type=Path, required=True)
    args = parser.parse_args()
    if not __debug__:
        raise RuntimeError('Run without Python optimization')

    def git(*args):
        return subprocess.check_output(['git', '-C', str(ROOT), *args])

    assert git('rev-parse','HEAD').decode().strip() == args.revision
    assert not git('status','--porcelain','--untracked-files=no'), 'tracked checkout is modified'
    args.binary, args.node, args.jsdom = (p.resolve(strict=True) for p in (args.binary,args.node,args.jsdom))
    assert digest(args.binary) == args.binary_sha256
    hashes = {route: hashlib.sha256(git('show',args.revision+':'+path)).hexdigest() for route,path in ASSETS.items()}
    assert all(digest(ROOT / path) == hashes[route] for route,path in ASSETS.items())
    stages = exercise(args,hashes)
    print(json.dumps({
        'revision':args.revision, 'tree':git('rev-parse','HEAD^{tree}').decode().strip(), 'binary_sha256':args.binary_sha256,
        'node_sha256':digest(args.node), 'jsdom_manifest_sha256':digest(args.jsdom / 'package.json'),
        'sdk_pins':official_sdk(ROOT).PINS, 'assets':hashes, 'dom_stages':stages, 'same_persistent_state':True,
        'two_team_scopes_independent':True, 'peer_and_stale_session_rejected':True, 'retained_after_restart':True, 'pending_navigation_restored':True, 'cleaned':True,
        'limits':'DOM via jsdom; served reference contract invoked over HTTP. No real browser, visual layout, Scalar Send UI, jobs or live AWS.',
    },indent=2))


if __name__ == '__main__':
    main()
