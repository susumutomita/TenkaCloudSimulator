"""Opt-in real served bilingual Console over loopback, JSDOM not Chromium or layout."""
from contextlib import ExitStack
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess

from ec2_launch_preflight_native import Local, run_default
from ec2_native_network_sdk import sdk
from ec2_route_table_pages_sdk import create_stack, rows, sdk_pages

ROOT = Path(__file__).resolve().parents[2]


def check(binary):
    with run_default(binary) as (port, token), ExitStack() as stack:
        c = sdk(stack, port, token)
        local = Local(port)
        cookie = local.session(token)
        vpc = c.create_vpc(CidrBlock='10.73.0.0/16')['Vpc']['VpcId']
        cfn, stack_out = create_stack(local, cookie)
        saved = sorted(c.create_route_table(VpcId=vpc)['RouteTable']['RouteTableId'] for _ in range(105))
        assert len(set(saved)) == 105
        asset = sha256((ROOT/'internal/server/web/app.js').read_bytes()).hexdigest()
        script = Path(__file__).with_name('ec2_route_table_pages_dom.cjs')
        run = subprocess.run(['node', str(script)], timeout=70, check=True, text=True, stdout=subprocess.PIPE,
                             input=json.dumps({'port': port, 'cookie': cookie, 'vpc': vpc,
                                               'saved': saved, 'stack': stack_out, 'asset': asset}))
        proof = json.loads(run.stdout)
        assert proof == {'removed': saved[0], 'peer': proof['peer'], 'completeJSDOM': True,
                         'lateScopeRetired': True, 'staleWriteUnconfirmed': True}
        expected = {*saved[1:], proof['peer']}
        assert saved[0] not in expected and len(expected) == 105
        assert {t['RouteTableId'] for t in rows(sdk_pages(c))} == {*expected, stack_out['Table']}
        for table in sorted(expected):
            c.delete_route_table(RouteTableId=table)
        c.delete_vpc(VpcId=vpc)
        local.query('cloudformation', 'DeleteStack', cookie, StackName=cfn)
        assert rows(sdk_pages(c)) == [] and c.describe_vpcs()['Vpcs'] == []
    print('PASS served English/Japanese Cloudscape in JSDOM: complete pages, CFN filter, late scope retirement, '
          'real stale continuation during deletion, no replay, SDK reconciliation and cleanup. Not Chromium/layout.')


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization')
    check(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
