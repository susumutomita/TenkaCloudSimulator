"""Optional isolated test: served Cloudscape VPC Console + exact real native local API."""
import json
import os
from pathlib import Path
import subprocess
from ec2_launch_preflight_native import CFN, EC2, Local, run_default


def run(binary):
    with run_default(binary) as (port, token):
        local=Local(port)
        admin=local.session(token)
        region='us-east-1'
        parent=local.query('ec2','CreateVpc',admin,region,CidrBlock='10.74.0.0/16').findtext('.//'+EC2+'vpcId')
        native_group=local.query('ec2','CreateSecurityGroup',admin,region,VpcId=parent,GroupName='native-no-ui',GroupDescription='Detached group').findtext('.//'+EC2+'groupId')
        assert native_group
        template={'Resources':{
            'Vpc':{'Type':'AWS::EC2::VPC','Properties':{'CidrBlock':'10.76.0.0/16'}},
            'Subnet':{'Type':'AWS::EC2::Subnet','Properties':{'VpcId':{'Ref':'Vpc'},'CidrBlock':'10.76.1.0/24'}},
            'Group':{'Type':'AWS::EC2::SecurityGroup','Properties':{'VpcId':{'Ref':'Vpc'},'GroupDescription':'CFN readback is still here'}},
        }}
        assert local.query('cloudformation','CreateStack',admin,region,StackName='cfn-native-console-compare',TemplateBody=json.dumps(template)).findtext('.//'+CFN+'StackId')
        listed=local.query('ec2','DescribeSubnets',admin,region)
        stack=listed.findtext('.//'+EC2+'vpcId')
        child=listed.findtext('.//'+EC2+'subnetId')
        assert parent and stack and child and parent!=stack
        principal='subnet-builder'
        policy={'Version':'2012-10-17','Statement':[
            {'Effect':'Allow','Action':['ec2:DescribeVpcs','ec2:DescribeSubnets','ec2:DescribeSecurityGroups','ec2:DescribeRouteTables'],'Resource':'*'},
            {'Effect':'Allow','Action':'ec2:CreateSubnet','Resource':[f'arn:aws:ec2:{region}:000000000000:vpc/{parent}',f'arn:aws:ec2:{region}:000000000000:subnet/*']},
            {'Effect':'Allow','Action':'ec2:DeleteSubnet','Resource':f'arn:aws:ec2:{region}:000000000000:subnet/*'},
        ]}
        local.call('PUT','/_simulator/v1/iam/principals/'+principal+'/policy',json.dumps(policy),Cookie=admin,**{'Content-Type':'application/json'})
        _,issued=local.call('POST','/_simulator/v1/access',json.dumps({'Principal':principal}),Cookie=admin,**{'Content-Type':'application/json'})
        cookie=local.session(json.loads(issued)['AccessToken'])
        subprocess.run(['node',str(Path(__file__).with_name('ec2_native_subnet_dom.cjs'))],check=True,timeout=150,text=True,
            input=json.dumps(dict(port=port,cookie=cookie,vpc=parent,stackVpc=stack,stackSubnet=child)))
        rows=local.query('ec2','DescribeSubnets',admin,region).findall('.//'+EC2+'subnetId')
        assert [row.text for row in rows]==[child]
        local.query('ec2','DeleteSecurityGroup',admin,region,GroupId=native_group)
        local.query('ec2','DeleteVpc',admin,region,VpcId=parent)
        local.query('cloudformation','DeleteStack',admin,region,StackName='cfn-native-console-compare')
        assert not local.query('ec2','DescribeSubnets',admin,region).findall('.//'+EC2+'subnetId')
        print('PASS native/CFN cleanup and simulator process teardown; no guest or external AWS.')

if __name__=='__main__':
    if not __debug__: raise RuntimeError('Run without Python optimization')
    run(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True))
