"""Offline repair receipt: signed pre-#599 seed, CLI on a private copy, pinned SDK and served jsdom."""
from contextlib import ExitStack, closing
from hashlib import sha256
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile

from ec2_launch_preflight_native import Local, run_default
from ec2_native_network_sdk import REGION, boto3, botocore, native_local_route, reject, sdk
from ec2_native_route_main_console import view_token
from ec2_route_table_pages_sdk import create_stack

CIDR = '10.190.0.0/16'
LOCAL = native_local_route(CIDR)
FORMAT = 'ec2-native-main-route/v1'
COMMAND = 'repair-ec2-native-main-route'
CLIENT_TOKEN = 'legacy-route-table-explicit-replay'
TAGS = [{'ResourceType': 'route-table', 'Tags': [{'Key': 'Name', 'Value': 'Preserved <設定>'}]}]
ROOT = Path(__file__).resolve().parents[2]


def digest(path):
    return sha256(path.read_bytes()).hexdigest()


def snapshot(path):
    with closing(sqlite3.connect(path.as_uri()+'?mode=ro', uri=True)) as db:
        db.execute('PRAGMA query_only=ON')
        assert db.execute('PRAGMA user_version').fetchone() == (1,)
        return [db.execute(statement).fetchall() for statement in (
            'SELECT region,id,kind,stack_id,revision,document FROM aws_ec2_network_resources ORDER BY region,id',
            'SELECT * FROM aws_ec2_native_route_table_replay ORDER BY region,token_hash',
            'SELECT * FROM aws_cfn_stacks ORDER BY region,id',
            'SELECT * FROM aws_cfn_ownership ORDER BY namespace,kind,physical_id',
            'SELECT * FROM identity_policies ORDER BY name',
        )]


def offline(binary, mode, path, vpc, *arguments, refusal=None):
    run = subprocess.run([binary, COMMAND, mode, '--state', path, '--region', REGION, '--vpc-id', vpc, *arguments],
                         cwd=path.parent, env={'HOME': str(path.parent), 'PATH': '/nonexistent'},
                         stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=35)
    if refusal is not None:
        assert run.returncode != 0 and refusal in run.stderr and not run.stdout, (refusal, run.returncode, run.stderr)
        return
    assert run.returncode == 0 and not run.stderr, (run.returncode, run.stderr)
    return json.loads(run.stdout)


def copy(original, target):
    shutil.copyfile(original, target)
    target.chmod(0o600)
    assert not os.path.samefile(original, target) and digest(target) == digest(original)


def inventory(client):
    return {r['RouteTableId']: r for r in client.describe_route_tables()['RouteTables']}


def explicit_tables(client, subnet):
    return client.describe_route_tables(Filters=[{'Name': 'association.subnet-id', 'Values': [subnet]}])['RouteTables']


def verify_rows(before, after, receipt):
    assert before[1:] == after[1:], 'replay history, CloudFormation, or IAM changed'
    index = lambda rows: {(row[0], row[1]): row for row in rows}
    saved, result = index(before[0]), index(after[0])
    primary = (REGION, receipt['mainRouteTableId'])
    assert result.keys() == saved.keys() | {primary}
    edited = {(REGION, receipt['vpcId']), *((REGION, t) for t in receipt['customRouteTableIds'])}
    assert {k: r for k, r in result.items() if k not in edited | {primary}} == {k: r for k, r in saved.items() if k not in edited}
    for k in edited:
        old, new = saved[k], result[k]
        assert old[:4] == new[:4] and (old[4], new[4]) == (1, 2)
        fields = json.loads(old[5])
        fields['Revision'] = 2
        if k[1] == receipt['vpcId']:
            fields.update(MainRouteTableID=primary[1], MainRouteRepair={'Version': 1, 'PlanDigest': receipt['planDigest']})
        else:
            fields['CIDR'] = CIDR
        assert json.loads(new[5]) == fields
    main = result[primary]
    assert main[2:5] == ('AWS::EC2::RouteTable', saved[(REGION, receipt['vpcId'])][3], 1)
    document = json.loads(main[5])
    assert document['CIDR'] == CIDR and document['VPCID'] == receipt['vpcId'] and not document['Tags']
    assert all(r[1] != receipt['mainAssociationId'] for r in after[0])


def check(old, current):
    assert __debug__ and (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    with tempfile.TemporaryDirectory(prefix='legacy-native-main-repair-') as td:
        folder = Path(td)
        original, working = folder/'signed-original.sqlite', folder/'working.sqlite'
        with run_default(old, state=original) as (port, token), ExitStack() as life:
            a, far = sdk(life, port, token), sdk(life, port, token, 'ap-northeast-1')
            vpc = a.create_vpc(CidrBlock=CIDR)['Vpc']['VpcId']
            explicit, implicit = [a.create_subnet(VpcId=vpc, CidrBlock=f'10.190.{i}.0/24')['Subnet']['SubnetId'] for i in (1, 2)]
            custom = a.create_route_table(VpcId=vpc)['RouteTable']['RouteTableId']
            with_static = a.create_route_table(VpcId=vpc, ClientToken=CLIENT_TOKEN, TagSpecifications=TAGS)['RouteTable']['RouteTableId']
            association = a.associate_route_table(SubnetId=explicit, RouteTableId=custom)['AssociationId']
            gateway = a.create_internet_gateway()['InternetGateway']['InternetGatewayId']
            a.attach_internet_gateway(InternetGatewayId=gateway, VpcId=vpc)
            for prefix in ('0.0.0.0/0', '198.51.100.0/24'):
                assert a.create_route(RouteTableId=with_static, DestinationCidrBlock=prefix, GatewayId=gateway)['Return']
            peer = a.create_vpc(CidrBlock='10.191.0.0/16')['Vpc']['VpcId']
            far_vpc = far.create_vpc(CidrBlock='10.192.0.0/16')['Vpc']['VpcId']
            far_table = far.create_route_table(VpcId=far_vpc)['RouteTable']['RouteTableId']
            local = Local(port)
            stack, other = create_stack(local, local.session(token))
            view_token(local, token)  # Durable fixture policy, not a reusable token.
            east_before, west_before = inventory(a), inventory(far)
            assert set(east_before) == {custom, with_static, other['Table']} and set(west_before) == {far_table}
            assert east_before[custom]['Routes'] == [] and len(east_before[with_static]['Routes']) == 2
            assert explicit_tables(a, explicit) == [east_before[custom]] and not explicit_tables(a, implicit)
        before, original_hash = snapshot(original), digest(original)
        original.chmod(0o400)
        copy(original, working)
        ready = offline(current, 'plan', original, vpc)
        assert ready == offline(current, 'plan', working, vpc)
        key = lambda item: item.encode() + b'\0'
        expected = sha256(key(FORMAT)+key(REGION)+key(vpc)+bytes.fromhex(original_hash)).hexdigest()
        assert ready == {'format': FORMAT, 'status': 'ready', 'region': REGION, 'vpcId': vpc, 'vpcRevision': 1,
                         'mainRouteTableId': ready['mainRouteTableId'], 'mainAssociationId': ready['mainAssociationId'],
                         'customRouteTableIds': sorted([custom, with_static]), 'extraPersistedRows': 1, 'planDigest': expected}
        assert digest(working) == digest(original) == original_hash
        consent = ('--original', original, '--expect-plan', expected)
        offline(current, 'apply', original, vpc, *consent, refusal='distinct working copy')
        wrong = ('f' if expected[0] != 'f' else 'e') + expected[1:]
        offline(current, 'apply', working, vpc, '--original', original, '--expect-plan', wrong, refusal='stale repair plan')
        sidecar = Path(str(working)+'-wal')
        sidecar.touch()
        offline(current, 'plan', working, vpc, refusal='SQLite sidecars')
        sidecar.unlink()
        alias = folder/'link-to-working.sqlite'
        alias.hardlink_to(working)
        offline(current, 'plan', alias, vpc, refusal='no symlink or hard link')
        alias.unlink()
        assert digest(working) == original_hash

        stale = folder/'stale.sqlite'
        copy(original, stale)
        with run_default(old, state=stale) as (port, token), ExitStack() as life:
            sdk(life, port, token).create_route_table(VpcId=peer)
        stale_hash = digest(stale)
        offline(current, 'apply', stale, vpc, *consent, refusal='stale repair plan')
        assert digest(stale) == stale_hash

        removed = folder/'removed.sqlite'
        removed_plan = offline(current, 'plan', original, peer)
        copy(original, removed)
        with run_default(old, state=removed) as (port, token), ExitStack() as life:
            sdk(life, port, token).delete_vpc(VpcId=peer)
        removed_hash = digest(removed)
        offline(current, 'apply', removed, peer, '--original', original, '--expect-plan', removed_plan['planDigest'],
                refusal='InvalidVpcID.NotFound')
        assert digest(removed) == removed_hash

        # Simulate a separate process retaining a writer reservation without
        # modifying state. The real exclusive apply must refuse, not wait forever.
        with closing(sqlite3.connect(working, timeout=0)) as held:
            held.execute('BEGIN IMMEDIATE')
            offline(current, 'apply', working, vpc, *consent, refusal='database is locked')
            held.rollback()
        assert digest(working) == digest(original) == original_hash

        applied = offline(current, 'apply', working, vpc, *consent)
        assert applied == {**ready, 'status': 'applied', 'vpcRevision': 2}
        unchanged = {**applied, 'status': 'already-repaired', 'extraPersistedRows': 0}
        exactly_applied_hash = digest(working)
        assert exactly_applied_hash != original_hash and digest(original) == original_hash
        verify_rows(before, snapshot(working), applied)
        assert offline(current, 'apply', working, vpc, *consent) == offline(current, 'plan', working, vpc) == unchanged
        assert digest(working) == exactly_applied_hash
        main, system = applied['mainRouteTableId'], applied['mainAssociationId']
        for restart in range(2):
            with run_default(current, state=working) as (port, token), ExitStack() as life:
                a, far = sdk(life, port, token), sdk(life, port, token, 'ap-northeast-1')
                projected = inventory(a)
                assert set(projected) == {main, *east_before} and inventory(far) == west_before
                assert projected[other['Table']] == east_before[other['Table']]
                assert projected[custom] == {**east_before[custom], 'Routes': [LOCAL]}
                first, last = east_before[with_static]['Routes']
                assert projected[with_static] == {**east_before[with_static], 'Routes': [first, LOCAL, last]}
                own = projected[main]
                assert own['VpcId'] == vpc and own['Routes'] == [LOCAL] and own['Associations'] == [
                    {'Main': True, 'RouteTableAssociationId': system, 'RouteTableId': main, 'AssociationState': {'State': 'associated'}}]
                assert a.describe_route_tables(RouteTableIds=[main])['RouteTables'] == [own]
                assert explicit_tables(a, explicit) == [projected[custom]] and not explicit_tables(a, implicit)
                if restart == 0:
                    local = Local(port)
                    _, raw = local.call('POST', '/_simulator/v1/access', json.dumps({'Principal': 'route-main-read-except-subnets'}),
                                             **{'X-Simulator-Token': token, 'Content-Type': 'application/json'})
                    viewer = json.loads(raw)['AccessToken']
                    least = sdk(life, port, viewer)
                    assert inventory(least) == projected
                    reject(least.describe_subnets, 'UnauthorizedOperation')
                    subprocess.run(['node', str(Path(__file__).with_name('ec2_legacy_main_repair_dom.cjs'))],
                                   input=json.dumps({'port': port, 'cookie': local.session(token), 'viewer': local.session(viewer), 'vpc': vpc,
                                       'main': main, 'custom': custom, 'withStatic': with_static, 'implicit': implicit, 'explicit': explicit,
                                       'asset': digest(ROOT/'internal/server/web/app.js')}), text=True, check=True, timeout=40)
                else:
                    assert a.create_route_table(VpcId=vpc, ClientToken=CLIENT_TOKEN, TagSpecifications=TAGS)['RouteTable']['RouteTableId'] == with_static
                    reject(a.delete_route_table, 'DependencyViolation', RouteTableId=main)
                    reject(a.delete_route, 'InvalidParameterValue', RouteTableId=main, DestinationCidrBlock=CIDR)
                    reject(a.delete_vpc, 'DependencyViolation', VpcId=vpc)
                    a.disassociate_route_table(AssociationId=association)
                    for prefix in ('0.0.0.0/0', '198.51.100.0/24'):
                        a.delete_route(RouteTableId=with_static, DestinationCidrBlock=prefix)
                    a.delete_route_table(RouteTableId=with_static)
                    a.delete_route_table(RouteTableId=custom)
                    a.detach_internet_gateway(InternetGatewayId=gateway, VpcId=vpc)
                    a.delete_internet_gateway(InternetGatewayId=gateway)
                    a.delete_subnet(SubnetId=explicit)
                    a.delete_subnet(SubnetId=implicit)
                    a.delete_vpc(VpcId=vpc)
                    assert inventory(a) == {other['Table']: east_before[other['Table']]} and inventory(far) == west_before
                    a.delete_vpc(VpcId=peer)
                    far.delete_route_table(RouteTableId=far_table)
                    far.delete_vpc(VpcId=far_vpc)
                    local = Local(port)
                    local.query('cloudformation', 'DeleteStack', local.session(token), StackName=stack)
                    assert not inventory(a) and not inventory(far) and not a.describe_vpcs()['Vpcs']
            assert digest(original) == original_hash and snapshot(original) == before
            if restart == 0:
                assert digest(working) == exactly_applied_hash
        # Reopening a fresh copy of the preserved backup remains the rollback:
        # no down migration, no reopening the repaired state with the old binary.
        restored = folder/'restored.sqlite'
        copy(original, restored)
        with run_default(current, state=restored) as (port, token), ExitStack() as life:
            assert inventory(sdk(life, port, token)) == east_before
        assert digest(restored) == original_hash
    print('PASS signed old fixture → read-only plan → explicit working-copy apply → pinned SDK/restart; byte-exact original, rollback copy, stable relations and IAM/CFN, stale and busy refusals. No real AWS or packet claims.')


if __name__ == '__main__':
    check(Path(os.environ['BASELINE_SIMULATOR']).resolve(strict=True), Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
