"""Opt-in: real served Cloudscape VPC panes, native SDK readback and loopback cleanup."""
from contextlib import ExitStack
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess

from ec2_launch_preflight_native import CFN, EC2, Local, run_default
from ec2_native_network_sdk import boto3, botocore, sdk

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
        sibling = ec2.create_security_group(VpcId=parent, GroupName='Saved sibling',
                                            Description='Independent native group')['GroupId']
        template = {'Resources': {
            'Vpc': {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.76.0.0/16'}},
            'Subnet': {'Type': 'AWS::EC2::Subnet', 'Properties': {'VpcId': {'Ref': 'Vpc'}, 'CidrBlock': '10.76.1.0/24'}},
            'Group': {'Type': 'AWS::EC2::SecurityGroup', 'Properties': {'VpcId': {'Ref': 'Vpc'}, 'GroupDescription': 'CFN readback is still here'}},
        }}
        stack_name = 'cfn-native-console-compare'
        assert local.query('cloudformation', 'CreateStack', admin, REGION, StackName=stack_name,
                           TemplateBody=json.dumps(template)).findtext('.//'+CFN+'StackId')
        child, = ec2.describe_subnets()['Subnets']
        stack, legacy_subnet = child['VpcId'], child['SubnetId']
        assert stack != parent
        principal = 'vpc-builder'
        prefix = f'arn:aws:ec2:{REGION}:000000000000:'
        parent_arn = prefix+'vpc/'+parent
        children = {'Subnet':'subnet', 'SecurityGroup':'security-group', 'NetworkAcl':'network-acl'}
        policy = {'Version':'2012-10-17', 'Statement':[
            {'Effect':'Allow','Action':['ec2:DescribeVpcs','ec2:DescribeSubnets','ec2:DescribeSecurityGroups',
                                        'ec2:DescribeRouteTables','ec2:DescribeNetworkAcls'],'Resource':'*'},
            *({'Effect':'Allow','Action':'ec2:Create'+key,'Resource':[parent_arn,prefix+kind+'/*']}
              for key,kind in children.items()),
            *({'Effect':'Allow','Action':'ec2:Delete'+key,'Resource':prefix+kind+'/*'}
              for key,kind in children.items()),
            {'Effect':'Allow','Action':'ec2:CreateTags','Resource':prefix+'network-acl/*'},
        ]}
        content = {'Content-Type':'application/json'}
        local.call('PUT','/_simulator/v1/iam/principals/'+principal+'/policy',json.dumps(policy),Cookie=admin,**content)
        _, issued = local.call('POST','/_simulator/v1/access',json.dumps({'Principal':principal}),Cookie=admin,**content)
        cookie=local.session(json.loads(issued)['AccessToken'])

        def drive(phase, **extra):
            data=dict(phase=phase, port=port, cookie=cookie, vpc=parent, stackVpc=stack, stackSubnet=legacy_subnet,
                      asset=asset, principal=principal, sibling=sibling, **extra)
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
        assert observed['holdProof'] and observed['lateReadAborted']
        drive('delete', saved=saved)
        assert {s['SubnetId'] for s in ec2.describe_subnets()['Subnets']}=={legacy_subnet}
        assert {g['GroupId'] for g in ec2.describe_security_groups(Filters=[{'Name':'vpc-id','Values':[parent]}])['SecurityGroups']}=={sibling}
        assert ec2.describe_network_acls()['NetworkAcls']==[]
        ec2.delete_security_group(GroupId=sibling)
        ec2.delete_vpc(VpcId=parent)
        local.query('cloudformation','DeleteStack',admin,REGION,StackName=stack_name)
        assert ec2.describe_vpcs()['Vpcs']==[] and ec2.describe_subnets()['Subnets']==[]
        assert ec2.describe_security_groups()['SecurityGroups']==[] and ec2.describe_network_acls()['NetworkAcls']==[]
    print('PASS exact served bundle (JSDOM): participant creates/removes subnet, detached SG and detached ACL; CFN read-only, lease holds through readback, stale disowned read fails. Independent pinned SDK confirmed all three actual records and clean teardown. No browser layout, guest or cloud.')


if __name__=='__main__':
    if not __debug__:
        raise RuntimeError('Run without Python optimization.')
    run(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
