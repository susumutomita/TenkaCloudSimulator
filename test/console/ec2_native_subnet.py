"""Opt-in: real served Cloudscape VPC panes, native SDK readback and loopback cleanup."""
from contextlib import ExitStack
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess

from ec2_launch_preflight_native import CFN, EC2, Local, run_default
from ec2_native_network_sdk import boto3, botocore, fresh_main_route_table, native_local_route, sdk

REGION = 'us-east-1'


def run(binary):
    assert (boto3.__version__, botocore.__version__) == ('1.42.0', '1.41.6')
    root = Path(__file__).resolve().parents[2]
    asset = sha256((root/'internal/server/web/app.js').read_bytes()).hexdigest()
    probe = Path(__file__).with_name('ec2_native_subnet_dom.cjs')
    with run_default(binary) as (port, token), ExitStack() as clients:
        local = Local(port)
        admin = local.session(token)
        ec2 = sdk(clients, port, token)
        parent = ec2.create_vpc(CidrBlock='10.74.0.0/16')['Vpc']['VpcId']
        main = fresh_main_route_table(ec2, parent, '10.74.0.0/16')
        peer = ec2.create_vpc(CidrBlock='10.77.0.0/16')['Vpc']['VpcId']
        peer_main = fresh_main_route_table(ec2, peer, '10.77.0.0/16')
        peer_table = ec2.create_route_table(VpcId=peer)['RouteTable']['RouteTableId']
        peer_details, = ec2.describe_route_tables(RouteTableIds=[peer_table])['RouteTables']
        assert peer_details['VpcId'] == peer and peer_details['Routes'] == [native_local_route('10.77.0.0/16')]
        sibling = ec2.create_security_group(VpcId=parent, GroupName='Saved sibling',
                                            Description='Independent native group')['GroupId']
        template = {'Resources': {
            'Vpc': {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.76.0.0/16'}},
            'Subnet': {'Type': 'AWS::EC2::Subnet', 'Properties': {'VpcId': {'Ref': 'Vpc'}, 'CidrBlock': '10.76.1.0/24'}},
            'Group': {'Type': 'AWS::EC2::SecurityGroup', 'Properties': {'VpcId': {'Ref': 'Vpc'}, 'GroupDescription': 'CFN readback is still here'}},
            'Table': {'Type': 'AWS::EC2::RouteTable', 'Properties': {'VpcId': {'Ref': 'Vpc'}}},
            'Association': {'Type': 'AWS::EC2::SubnetRouteTableAssociation', 'Properties': {
                'SubnetId': {'Ref': 'Subnet'}, 'RouteTableId': {'Ref': 'Table'}}},
        }}
        stack_name = 'cfn-native-console-compare'
        assert local.query('cloudformation', 'CreateStack', admin, REGION, StackName=stack_name,
                           TemplateBody=json.dumps(template)).findtext('.//'+CFN+'StackId')
        child, = ec2.describe_subnets()['Subnets']
        stack, legacy_subnet = child['VpcId'], child['SubnetId']
        assert stack != parent
        legacy_table, = [row for row in ec2.describe_route_tables()['RouteTables'] if row['VpcId'] == stack]
        legacy_table_id = legacy_table['RouteTableId']
        assert [(row['Main'], row['SubnetId']) for row in legacy_table['Associations']] == [(False, legacy_subnet)]
        original_routes = {r['RouteTableId']: r for r in (main, peer_main, peer_details, legacy_table)}

        principal = 'vpc-builder'
        prefix = f'arn:aws:ec2:{REGION}:000000000000:'
        parent_arn = prefix+'vpc/'+parent
        children = {'Subnet':'subnet', 'SecurityGroup':'security-group', 'NetworkAcl':'network-acl', 'RouteTable':'route-table'}
        policy = {'Version':'2012-10-17', 'Statement':[
            {'Effect':'Allow','Action':['ec2:DescribeVpcs','ec2:DescribeSubnets','ec2:DescribeSecurityGroups',
                                        'ec2:DescribeRouteTables','ec2:DescribeNetworkAcls'],'Resource':'*'},
            *({'Effect':'Allow','Action':'ec2:Create'+key,'Resource':[parent_arn,prefix+kind+'/*']}
              for key,kind in children.items()),
            *({'Effect':'Allow','Action':'ec2:Delete'+key,'Resource':prefix+kind+'/*'}
              for key,kind in children.items()),
            {'Effect':'Allow','Action':'ec2:CreateTags','Resource':[prefix+'network-acl/*',prefix+'route-table/*']},
        ]}
        content = {'Content-Type':'application/json'}
        local.call('PUT','/_simulator/v1/iam/principals/'+principal+'/policy',json.dumps(policy),Cookie=admin,**content)
        _, issued = local.call('POST','/_simulator/v1/access',json.dumps({'Principal':principal}),Cookie=admin,**content)
        cookie=local.session(json.loads(issued)['AccessToken'])

        def drive(phase, **extra):
            data=dict(phase=phase, port=port, cookie=cookie, vpc=parent, stackVpc=stack, stackSubnet=legacy_subnet,
                      asset=asset, principal=principal, sibling=sibling,
                      legacyRoute=legacy_table_id, peerRoute=peer_table, nativeMain=main['RouteTableId'],
                      peerMain=peer_main['RouteTableId'], **extra)
            result=subprocess.run(['node',str(probe)],check=True,timeout=90,text=True,
                                  input=json.dumps(data),stdout=subprocess.PIPE)
            return json.loads(result.stdout)

        observed=drive('create')
        saved=observed['saved']
        group=ec2.describe_security_groups(GroupIds=[saved['group']])['SecurityGroups'][0]
        assert (group['VpcId'],group['GroupName'],group['Description'],group['IpPermissions'])==(
            parent,'GUI group','Detached GUI group',[])
        acl,=ec2.describe_network_acls(NetworkAclIds=[saved['acl']])['NetworkAcls']
        assert (acl['VpcId'],acl['IsDefault'],acl['Associations'],acl['Tags'])==(
            parent,False,[],[{'Key':'Name','Value':'GUI ACL <local>'}])
        subnets={s['SubnetId']:s for s in ec2.describe_subnets()['Subnets']}
        assert set(subnets)=={legacy_subnet,saved['subnet']}
        assert (subnets[saved['subnet']]['VpcId'],subnets[saved['subnet']]['CidrBlock'])==(parent,'10.74.1.0/24')
        routes={t['RouteTableId']:t for t in ec2.describe_route_tables()['RouteTables']}
        assert set(routes)=={*original_routes, saved['table']}
        assert {id: routes[id] for id in original_routes} == original_routes
        actual=routes[saved['table']]
        assert (actual['VpcId'],actual['Routes'],actual['Associations'],actual['Tags']) == (
            parent,[native_local_route('10.74.0.0/16')],[],[{'Key':'Name','Value':'GUI route <設定>'}])
        assert observed['holdProof'] and observed['lateReadAborted'] and observed['heldRouteReadback']

        drive('delete', saved=saved)
        assert {s['SubnetId'] for s in ec2.describe_subnets()['Subnets']}=={legacy_subnet}
        assert {g['GroupId'] for g in ec2.describe_security_groups(Filters=[{'Name':'vpc-id','Values':[parent]}])['SecurityGroups']}=={sibling}
        assert ec2.describe_network_acls()['NetworkAcls']==[]
        survivors={t['RouteTableId']:t for t in ec2.describe_route_tables()['RouteTables']}
        assert survivors==original_routes
        ec2.delete_route_table(RouteTableId=peer_table)
        ec2.delete_vpc(VpcId=peer)
        ec2.delete_security_group(GroupId=sibling)
        ec2.delete_vpc(VpcId=parent)
        local.query('cloudformation','DeleteStack',admin,REGION,StackName=stack_name)
        assert ec2.describe_vpcs()['Vpcs']==[] and ec2.describe_subnets()['Subnets']==[]
        assert ec2.describe_security_groups()['SecurityGroups']==[] and ec2.describe_network_acls()['NetworkAcls']==[]
        assert ec2.describe_route_tables()['RouteTables']==[]
    print('PASS exact served bundle (JSDOM): participant creates and removes subnet, detached SG, ACL and route table; held readbacks fence controls, stale refresh aborts, CFN and peer route rows stay intact. Independent pinned SDK confirmed four actual rows and clean teardown. No browser layout, packet, guest or cloud.')


if __name__=='__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization.')
    run(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
