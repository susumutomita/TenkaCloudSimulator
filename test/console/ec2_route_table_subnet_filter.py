"""Opt-in JSDOM acceptance of the served CFN route table filter, loopback only."""
import json
import os
import subprocess
from hashlib import sha256
from pathlib import Path

from ec2_launch_preflight_native import CFN, EC2, Local, run_default

ROOT = Path(__file__).resolve().parents[2]


def template_body():
    result = json.loads((ROOT / 'test/e2e/fixtures/ec2-network-contract.json').read_text())
    result['Resources']['Unassociated'] = {
        'Type': 'AWS::EC2::Subnet',
        'Properties': {'VpcId': {'Ref': 'Vpc'}, 'CidrBlock': '10.67.2.0/24'},
    }
    result['Resources']['SpareTable'] = {
        'Type': 'AWS::EC2::RouteTable',
        'Properties': {'VpcId': {'Ref': 'Vpc'}},
    }
    result['Outputs']['Unassociated'] = {'Value': {'Ref': 'Unassociated'}}
    return json.dumps(result)


def check_console(port, cookie, selected):
    asset = sha256((ROOT / 'internal/server/web/app.js').read_bytes()).hexdigest()
    script = Path(__file__).with_name('ec2_route_table_subnet_filter_dom.cjs')
    subprocess.run(['node', str(script)], check=True, text=True, timeout=50,
                   input=json.dumps({'port': port, 'cookie': cookie, 'selected': selected, 'asset': asset}))


def run(binary):
    with run_default(binary) as (port, token):
        local = Local(port)
        cookie = local.session(token)
        cloud = 'cloudformation'
        created = local.query(cloud, 'CreateStack', cookie, StackName='console-route-filter',
                              TemplateBody=template_body())
        stack_id = created.findtext(f'{CFN}CreateStackResult/{CFN}StackId')
        assert stack_id
        try:
            found = local.query(cloud, 'DescribeStacks', cookie, StackName=stack_id)
            node = found.find(f'{CFN}DescribeStacksResult/{CFN}Stacks/{CFN}member')
            assert node is not None and node.findtext(CFN + 'StackStatus') == 'CREATE_COMPLETE'
            out = {item.findtext(CFN + 'OutputKey'): item.findtext(CFN + 'OutputValue')
                   for item in node.findall(f'{CFN}Outputs/{CFN}member')}
            check_console(port, cookie, out)
        finally:
            local.query(cloud, 'DeleteStack', cookie, StackName=stack_id)
        routes = local.query('ec2', 'DescribeRouteTables', cookie)
        assert not routes.findall(f'{EC2}routeTableSet/{EC2}item')
    print('PASS served Cloudscape in JSDOM: exact subnet filter, no match, late-response fencing, invalid input, clear, same local API and cleanup. Not Chromium layout.')


if __name__ == '__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization.')
    run(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
