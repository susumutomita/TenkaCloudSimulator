"""Acceptance for detached native SG rules through pinned SDK and optional served JSDOM."""
from contextlib import ExitStack
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess
import tempfile

from ec2_internet_gateway_sdk import allow, cfn_client, grant, outputs
from ec2_launch_preflight_native import Local, run_default
from ec2_native_network_sdk import REGION, boto3, botocore, sdk
from botocore.exceptions import ClientError

ROOT = Path(__file__).resolve().parents[2]
OPS = {
    'authorize_security_group_ingress': 'AuthorizeSecurityGroupIngress',
    'authorize_security_group_egress': 'AuthorizeSecurityGroupEgress',
    'revoke_security_group_ingress': 'RevokeSecurityGroupIngress',
    'revoke_security_group_egress': 'RevokeSecurityGroupEgress',
}
INGRESS = {'from': 443, 'to': 444, 'cidr': '10.138.1.4/32'}
EGRESS = {'from': 8443, 'to': 8443, 'cidr': '10.138.2.0/24'}
BROWSER_IN = {'from': 5353, 'to': 5353, 'cidr': '10.138.5.0/24'}
BROWSER_OUT = {'from': 8080, 'to': 8081, 'cidr': '10.138.6.4/32'}


def refused(fn, code, status=400, **params):
    try:
        fn(**params)
    except ClientError as exc:
        assert (exc.response['ResponseMetadata']['HTTPStatusCode'], exc.response['Error']['Code']) == (status, code)
    else:
        raise AssertionError(f'Simulator did not refuse {fn.__name__}')


def exact(c, group):
    saved, = c.describe_security_groups(GroupIds=[group])['SecurityGroups']
    assert saved['GroupId'] == group
    return saved


def expected(rule):
    return {'IpProtocol': 'tcp', 'FromPort': rule['from'], 'ToPort': rule['to'], 'IpRanges': [{'CidrIp': rule['cidr']}]}


def rules(c, group, inbound=(), outbound=()):
    saved = exact(c, group)
    assert saved['IpPermissions'] == [expected(r) for r in inbound]
    if not outbound:
        assert 'IpPermissionsEgress' not in saved, 'unsupported implicit egress'
    else:
        assert saved['IpPermissionsEgress'] == [expected(r) for r in outbound]
    return saved


def fields(group, permission):
    return {'GroupId': group, 'IpPermissions': [expected(permission)]}


def receipt(c, fn, group, r):
    out = getattr(c, fn)(**fields(group, r))
    meta = out['ResponseMetadata']
    assert meta['RequestId'] and meta['HTTPStatusCode'] == 200 and out['Return'] is True
    assert 'SecurityGroupRules' not in out and 'RevokedSecurityGroupRules' not in out


def native(c, name, cidr='10.138.0.0/16'):
    vpc = c.create_vpc(CidrBlock=cidr)['Vpc']['VpcId']
    group = c.create_security_group(VpcId=vpc, GroupName=name, Description='Detached rule fixture')['GroupId']
    rules(c, group)
    return vpc, group


def stack(cfn):
    legacy_rule = {'IpProtocol': 'tcp', 'FromPort': 443, 'ToPort': 443, 'CidrIp': '198.51.100.0/24'}
    created = cfn.create_stack(StackName='read-only-native-rules-fixture', TemplateBody=json.dumps({
        'Resources': {
            'Vpc': {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.137.0.0/16'}},
            'Group': {'Type': 'AWS::EC2::SecurityGroup', 'Properties': {
                'VpcId': {'Ref': 'Vpc'}, 'GroupDescription': 'Legacy rule readback', 'SecurityGroupIngress': [legacy_rule]}},
        },
        'Outputs': {'Vpc': {'Value': {'Ref': 'Vpc'}}, 'Group': {'Value': {'Ref': 'Group'}}},
    }))['StackId']
    return created, outputs(cfn, created)


def served(port, token, vpc, group, legacy):
    cookie = Local(port).session(token)
    script = Path(__file__).with_name('ec2_native_sg_rules_dom.cjs')
    digest = sha256((ROOT/'internal/server/web/app.js').read_bytes()).hexdigest()
    subprocess.run(['node', str(script)], text=True, check=True, timeout=75, input=json.dumps({
        'port': port, 'cookie': cookie, 'vpc': vpc, 'group': group, 'legacy': legacy,
        'asset': digest, 'tuples': [INGRESS, EGRESS, BROWSER_IN, BROWSER_OUT],
    }))


def check(binary):
    assert (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    assert __debug__, 'Run without Python optimization'
    with tempfile.TemporaryDirectory(prefix='detached-sg-rules-') as directory:
        alpha_db, beta_db = Path(directory)/'alpha.sqlite', Path(directory)/'beta.sqlite'
        with run_default(binary, state=beta_db) as (bp, bt), ExitStack() as bx:
            b = sdk(bx, bp, bt)
            bv, bg = native(b, 'Other team')
            receipt(b, 'authorize_security_group_ingress', bg, INGRESS)
            before_b = rules(b, bg, (INGRESS,))
            with run_default(binary, state=alpha_db) as (ap, at), ExitStack() as ax:
                assert ap != bp and at != bt
                Local(ap).call('GET', '/_simulator/health', status=401, **{'X-Simulator-Token': bt})
                Local(bp).call('GET', '/_simulator/health', status=401, **{'X-Simulator-Token': at})
                c = sdk(ax, ap, at)
                cfn = cfn_client(ax, ap, at)
                caps = json.loads(Local(ap).call('GET', '/_simulator/v1/capabilities', **{'X-Simulator-Token': at})[1])['services']['ec2']
                assert caps['detachedSecurityGroupRuleProfile'] == 'tcp-ipv4-same-vpc'
                assert caps['detachedSecurityGroupRulesPerDirection'] == 60 and set(OPS.values()) <= set(caps['operations'])
                assert not {'DescribeSecurityGroupRules', 'ModifySecurityGroupRules'} & set(caps['operations'])
                sid, legacy = stack(cfn)
                original_legacy = rules(c, legacy['Group'], ({'from': 443, 'to': 443, 'cidr': '198.51.100.0/24'},))
                av, ag = native(c, 'Native rule configuration')
                pv, pg = native(c, 'Peer VPC', '10.139.0.0/16')
                arn = f'arn:aws:ec2:{REGION}:000000000000:security-group/'
                grants = [allow('DescribeSecurityGroups', '*')]
                editor = sdk(ax, ap, grant(ap, at, 'sg-rule-editor', grants + [allow(a, arn+ag) for a in OPS.values()]))
                wrong = sdk(ax, ap, grant(ap, at, 'wrong-sg-rule-editor', grants + [allow(a, arn+pg) for a in OPS.values()]))
                for m in OPS:
                    refused(getattr(wrong, m), 'UnauthorizedOperation', 403, **fields(ag, INGRESS))
                    refused(getattr(c, m), 'UnsupportedOperation', **fields(legacy['Group'], INGRESS))
                    refused(getattr(b, m), 'InvalidGroup.NotFound', **fields(ag, INGRESS))
                refused(c.authorize_security_group_ingress, 'InvalidGroup.NotFound', **fields(bg, INGRESS))
                refused(c.authorize_security_group_ingress, 'InvalidParameterValue', **fields(ag, {**INGRESS, 'cidr': '10.139.0.0/16'}))
                refused(c.describe_security_group_rules, 'UnsupportedOperation', Filters=[{'Name': 'group-id', 'Values': [ag]}])
                refused(c.authorize_security_group_egress, 'UnsupportedOperation', DryRun=True, **fields(ag, EGRESS))
                receipt(editor, 'authorize_security_group_ingress', ag, INGRESS)
                rules(editor, ag, (INGRESS,))
                receipt(editor, 'authorize_security_group_egress', ag, EGRESS)
                rules(c, ag, (INGRESS,), (EGRESS,))
                refused(editor.authorize_security_group_egress, 'InvalidPermission.Duplicate', **fields(ag, EGRESS))
                receipt(editor, 'revoke_security_group_ingress', ag, INGRESS)
                rules(editor, ag, (), (EGRESS,))
                refused(editor.revoke_security_group_ingress, 'InvalidPermission.NotFound', **fields(ag, INGRESS))
                receipt(editor, 'revoke_security_group_egress', ag, EGRESS)
                rules(editor, ag)
                receipt(editor, 'authorize_security_group_ingress', ag, INGRESS)
                receipt(editor, 'authorize_security_group_egress', ag, EGRESS)
                rules(c, pg)
                assert exact(c, legacy['Group']) == original_legacy and exact(b, bg) == before_b
            with run_default(binary, state=alpha_db) as (ap, at), ExitStack() as ax:
                c, cfn = sdk(ax, ap, at), cfn_client(ax, ap, at)
                rules(c, ag, (INGRESS,), (EGRESS,))
                rules(c, pg)
                assert outputs(cfn, sid) == legacy and exact(c, legacy['Group']) == original_legacy
                if os.environ.get('JSDOM_MODULE'):
                    served(ap, at, av, ag, legacy)
                    rules(c, ag, (BROWSER_IN,), (BROWSER_OUT,))
                    receipt(c, 'revoke_security_group_ingress', ag, BROWSER_IN)
                    receipt(c, 'revoke_security_group_egress', ag, BROWSER_OUT)
                else:
                    receipt(c, 'revoke_security_group_ingress', ag, INGRESS)
                    receipt(c, 'revoke_security_group_egress', ag, EGRESS)
                rules(c, ag)
                receipt(c, 'authorize_security_group_ingress', ag, INGRESS)
                c.delete_security_group(GroupId=ag)  # Persistent rule removed with its group.
                c.delete_security_group(GroupId=pg)
                c.delete_vpc(VpcId=av)
                c.delete_vpc(VpcId=pv)
                cfn.delete_stack(StackName=sid)
            with run_default(binary, state=alpha_db) as (ap, at), ExitStack() as ax:
                c = sdk(ax, ap, at)
                assert c.describe_security_groups()['SecurityGroups'] == c.describe_vpcs()['Vpcs'] == []
            assert exact(b, bg) == before_b
            b.delete_security_group(GroupId=bg)
            b.delete_vpc(VpcId=bv)
            assert b.describe_security_groups()['SecurityGroups'] == b.describe_vpcs()['Vpcs'] == []
    print('PASS actual localhost boto3 1.42.0/botocore 1.41.6: both directional read/add/revoke, exact IAM, other team, ownership, restart, cleanup, stable no implicit egress.'
          + (' Served exact Cloudscape JS exercised via JSDOM and shared SDK readback.' if os.environ.get('JSDOM_MODULE') else ' UI not run.'))


if __name__ == '__main__':
    check(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
