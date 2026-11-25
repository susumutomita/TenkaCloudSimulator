"""Console Create subnet, Launch instance and Terminate against real local API state.

A real browser drives VPC -> Subnets -> Create subnet and EC2 -> Launch
instances; the official SDK reads the same state back. A launch record has no
guest, so no runtime state is expected anywhere. No VM or cloud launch.
"""
import json
import os
import re
import shutil
from check import running, boto3, Config, sync_playwright, expect
from reflow import assert_reflow

with running(authenticated=True) as (endpoint, operator), sync_playwright() as p:
    def client(service, region='us-east-1'):
        sdk=boto3.client(service, endpoint_url=endpoint, region_name=region,
            aws_access_key_id='TCSIMCONSOLEEC2', aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts':0}))
        sdk.meta.events.register('before-sign',lambda request,**_:request.headers.add_header('X-Simulator-Token',operator))
        return sdk
    ec2=client('ec2')
    def instances():
        return [i for r in ec2.describe_instances()['Reservations'] for i in r['Instances']]
    # One CloudFormation subnet and one EC2-owned VPC exist before the browser starts.
    cfn=client('cloudformation')
    cfn.create_stack(StackName='console-stack-network',TemplateBody=json.dumps({'Resources':{
        'Vpc':{'Type':'AWS::EC2::VPC','Properties':{'CidrBlock':'10.70.0.0/16'}},
        'Subnet':{'Type':'AWS::EC2::Subnet','Properties':{'VpcId':{'Ref':'Vpc'},'CidrBlock':'10.70.1.0/24'}}}}))
    stack_subnet=ec2.describe_subnets()['Subnets'][0]['SubnetId']
    vpc_id=ec2.create_vpc(CidrBlock='10.71.0.0/16',TagSpecifications=[{'ResourceType':'vpc','Tags':[{'Key':'Name','Value':'練習用 VPC'}]}])['Vpc']['VpcId']
    browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
    page=browser.new_page(viewport={'width':1100,'height':800})
    failures=[]
    page.on('pageerror',lambda error:failures.append(str(error)))
    page.goto(endpoint+'/_simulator/console/#/ec2')
    page.get_by_label('Access token',exact=True).fill(operator)
    page.get_by_role('button',name='Open sandbox',exact=True).click()
    page.get_by_role('button',name='Services',exact=True).click()
    page.get_by_role('menuitem',name='EC2',exact=True).click()
    expect(page.get_by_role('heading',name='EC2 instances',level=1,exact=True)).to_be_visible()
    expect(page.get_by_text('No resources found in this region.',exact=True)).to_be_visible()
    expect(page.get_by_role('button',name='Start instance',exact=True)).to_have_count(0)

    # Without an EC2-owned subnet the launch form says where to create one.
    page.get_by_role('button',name='Launch instances',exact=True).click()
    expect(page.get_by_role('heading',name='Launch an instance',level=1,exact=True)).to_be_visible()
    expect(page.get_by_text('A launch record, not a running server',exact=True)).to_be_visible()
    expect(page.get_by_text('Create a subnet first',exact=True)).to_be_visible()
    page.get_by_label('AMI ID',exact=True).fill('ami-0123456789abcdef0')
    page.get_by_role('button',name='Launch instance',exact=True).click()
    expect(page.get_by_text('Choose a subnet.',exact=True)).to_be_visible()
    assert instances()==[]
    page.get_by_role('link',name='Open Subnets',exact=True).click()

    # VPC -> Subnets lists the stack's subnet read-only and creates an EC2-owned one.
    expect(page.get_by_role('heading',name='Subnets',level=1,exact=True)).to_be_visible()
    expect(page.get_by_text(stack_subnet,exact=True)).to_be_visible()
    page.get_by_role('radio',name='Select '+stack_subnet,exact=True).check()
    expect(page.get_by_role('button',name='Delete subnet',exact=True)).to_be_disabled()
    page.get_by_role('button',name='Create subnet',exact=True).click()
    expect(page.get_by_role('heading',name='Create subnet',level=1,exact=True)).to_be_visible()
    page.get_by_label('VPC ID',exact=True).click()
    # Only the EC2-owned VPC is offered; the stack's VPC is not.
    expect(page.get_by_role('option',name=re.compile('^vpc-'))).to_have_count(1)
    page.get_by_role('option',name=re.compile(vpc_id)).click()
    expect(page.get_by_text('10.71.0.0/16',exact=True)).to_be_visible()
    page.get_by_label('Subnet name - optional',exact=True).fill('公開サブネット')
    page.get_by_label('IPv4 subnet CIDR block',exact=True).fill('10.99.0.0/24')
    page.get_by_role('button',name='Create subnet',exact=True).click()
    # The server's refusal stays on the form; nothing is created.
    expect(page.get_by_text('InvalidSubnet.Range',exact=False).first).to_be_visible()
    assert [s['SubnetId'] for s in ec2.describe_subnets()['Subnets']]==[stack_subnet]
    page.get_by_label('IPv4 subnet CIDR block',exact=True).fill('10.71.1.0/24')
    page.get_by_role('button',name='Create subnet',exact=True).click()
    expect(page.get_by_role('heading',name='Subnets',level=1,exact=True)).to_be_visible()
    native=[s for s in ec2.describe_subnets()['Subnets'] if s['SubnetId']!=stack_subnet]
    assert len(native)==1 and (native[0]['VpcId'],native[0]['CidrBlock'],native[0]['AvailabilityZone'])==(vpc_id,'10.71.1.0/24','us-east-1a'),native
    assert native[0]['Tags']==[{'Key':'Name','Value':'公開サブネット'}],native
    subnet_id=native[0]['SubnetId']
    expect(page.get_by_text('You successfully created '+subnet_id,exact=True)).to_be_visible()
    expect(page.get_by_role('radio',name='Select '+subnet_id,exact=True)).to_be_checked()

    # EC2 -> Launch instances saves one launch record in that subnet.
    page.get_by_role('button',name='Services',exact=True).click()
    page.get_by_role('menuitem',name='EC2',exact=True).click()
    page.get_by_role('button',name='Launch instances',exact=True).click()
    expect(page.get_by_text('Create a subnet first',exact=True)).to_have_count(0)
    page.get_by_label('Name - optional',exact=True).fill('練習用サーバー')
    page.get_by_label('AMI ID',exact=True).fill('not-an-image')
    page.get_by_label('VPC ID',exact=True).click()
    page.get_by_role('option',name=re.compile(vpc_id)).click()
    page.get_by_label('Subnet',exact=True).click()
    # The stack's subnet is never offered for a launch record.
    expect(page.get_by_role('option',name=re.compile('^subnet-'))).to_have_count(1)
    page.get_by_role('option',name=re.compile(subnet_id)).click()
    page.get_by_role('button',name='Launch instance',exact=True).click()
    expect(page.get_by_text('InvalidAMIID.Malformed',exact=False).first).to_be_visible()
    assert instances()==[]
    page.get_by_label('AMI ID',exact=True).fill('ami-0123456789abcdef0')
    # The first response is lost after the server saved the record. Resubmitting
    # the unchanged form sends the same client token and returns that record
    # instead of saving a second one.
    lost=[]
    def lose_first_launch(route):
        if 'Action=RunInstances' in (route.request.post_data or '') and not lost:
            lost.append(route.fetch().status)
            route.abort()
        else:
            route.continue_()
    page.route('**/*',lose_first_launch)
    page.get_by_role('button',name='Launch instance',exact=True).click()
    expect(page.get_by_role('heading',name='Launch an instance',level=1,exact=True)).to_be_visible()
    expect(page.get_by_text('No response from the server.',exact=False).first).to_be_visible()
    assert lost==[200] and len(instances())==1,(lost,instances())
    page.get_by_role('button',name='Launch instance',exact=True).click()
    expect(page.get_by_role('heading',name='EC2 instances',level=1,exact=True)).to_be_visible()
    page.unroute('**/*',lose_first_launch)
    launched=instances()
    assert len(launched)==1,launched
    record=launched[0]
    instance_id=record['InstanceId']
    assert (record['ImageId'],record['InstanceType'],record['SubnetId'],record['VpcId'])==('ami-0123456789abcdef0','t3.micro',subnet_id,vpc_id),record
    assert record['Tags']==[{'Key':'Name','Value':'練習用サーバー'}] and 'State' not in record,record
    expect(page.get_by_text('Saved the launch record '+instance_id,exact=True)).to_be_visible()
    expect(page.get_by_role('radio',name='Select '+instance_id,exact=True)).to_be_checked()
    # The list shows the record without claiming that a guest runs.
    expect(page.get_by_text('No guest (launch record only)',exact=True).first).to_be_visible()
    for claimed in ('running','Running','pending','Pending'):
        expect(page.get_by_text(claimed,exact=True)).to_have_count(0)
    expect(page.get_by_role('heading',name='Resource details',exact=True)).to_be_visible()
    expect(page.get_by_text(subnet_id,exact=True)).to_be_visible()

    # A subnet that holds a launch record cannot be deleted from the console.
    page.goto(endpoint+'/_simulator/console/#/subnets')
    page.get_by_role('radio',name='Select '+subnet_id,exact=True).check()
    page.get_by_role('button',name='Delete subnet',exact=True).click()
    dialog=page.get_by_role('dialog')
    dialog.get_by_label('To confirm deletion, type delete in the field.',exact=True).fill('delete')
    dialog.get_by_role('button',name='Delete',exact=True).click()
    expect(dialog.get_by_text('DependencyViolation',exact=False).first).to_be_visible()
    dialog.get_by_role('button',name='Cancel',exact=True).click()
    assert subnet_id in [s['SubnetId'] for s in ec2.describe_subnets()['Subnets']]

    # Terminate removes the record; then the subnet can be deleted.
    page.goto(endpoint+'/_simulator/console/#/ec2')
    page.get_by_role('radio',name='Select '+instance_id,exact=True).check()
    page.get_by_role('button',name='Terminate (delete) instance',exact=True).click()
    dialog=page.get_by_role('dialog')
    expect(dialog.get_by_text(instance_id,exact=False)).to_be_visible()
    dialog.get_by_role('button',name='Terminate (delete)',exact=True).click()
    expect(page.get_by_text('Terminated '+instance_id,exact=True)).to_be_visible()
    expect(page.get_by_role('radio',name='Select '+instance_id,exact=True)).to_have_count(0)
    assert instances()==[]
    page.goto(endpoint+'/_simulator/console/#/subnets')
    page.get_by_role('radio',name='Select '+subnet_id,exact=True).check()
    page.get_by_role('button',name='Delete subnet',exact=True).click()
    dialog=page.get_by_role('dialog')
    expect(dialog.get_by_role('button',name='Delete',exact=True)).to_be_disabled()
    dialog.get_by_label('To confirm deletion, type delete in the field.',exact=True).fill('delete')
    dialog.get_by_role('button',name='Delete',exact=True).click()
    expect(page.get_by_text('Deleted '+subnet_id,exact=True)).to_be_visible()
    assert [s['SubnetId'] for s in ec2.describe_subnets()['Subnets']]==[stack_subnet]

    # A malformed list response is an error, never an empty list.
    def malformed(route):
        if 'Action=DescribeSubnets' in (route.request.post_data or ''):
            route.fulfill(status=200,content_type='text/xml',body='<DescribeSubnetsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"/>')
        else:
            route.continue_()
    page.route('**/*',malformed)
    page.get_by_role('button',name='Refresh',exact=True).click()
    expect(page.get_by_text('Could not load resources',exact=True)).to_be_visible()
    expect(page.get_by_text('No resources found in this region.',exact=True)).to_have_count(0)
    page.unroute('**/*',malformed)
    page.get_by_role('button',name='Refresh',exact=True).click()
    expect(page.get_by_text(stack_subnet,exact=True)).to_be_visible()

    # Japanese labels and 390 px reflow of both new forms.
    page.get_by_label('Language',exact=True).select_option('ja')
    expect(page.get_by_role('heading',name='サブネット',level=1,exact=True)).to_be_visible()
    page.set_viewport_size({'width':390,'height':844});assert_reflow(page)
    page.get_by_role('button',name='サブネットを作成',exact=True).click()
    expect(page.get_by_role('heading',name='サブネットを作成',level=1,exact=True)).to_be_visible()
    assert_reflow(page)
    page.get_by_role('button',name='キャンセル',exact=True).click()
    page.goto(endpoint+'/_simulator/console/#/ec2')
    page.get_by_role('button',name='インスタンスを起動',exact=True).click()
    expect(page.get_by_role('heading',name='インスタンスを起動',level=1,exact=True)).to_be_visible()
    expect(page.get_by_text('稼働するサーバーではなく起動記録です',exact=True)).to_be_visible()
    assert_reflow(page)
    page.get_by_role('button',name='キャンセル',exact=True).click()
    expect(page.get_by_role('heading',name='EC2 インスタンス',level=1,exact=True)).to_be_visible()
    assert failures==[],failures
    browser.close()
print('EC2 console creates subnets and launch records and terminates them through real local API state')
