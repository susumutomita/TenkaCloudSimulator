"""Real CFN VPC -> EC2 Query and console, plus console CreateVpc/DeleteVpc; no guest or cloud launch."""
import json
import os
import shutil
from check import running, boto3, Config, sync_playwright, expect
from reflow import assert_reflow

with running(authenticated=True) as (endpoint, operator), sync_playwright() as p:
    def client(service, region='us-east-1'):
        sdk=boto3.client(service, endpoint_url=endpoint, region_name=region,
            aws_access_key_id='TCSIMREADONLY', aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts':0}))
        sdk.meta.events.register('before-sign',lambda request,**_:request.headers.add_header('X-Simulator-Token',operator))
        return sdk
    cfn=client('cloudformation')
    cfn.create_stack(StackName='readonly-console-vpc',TemplateBody=json.dumps({'Resources':{
        'Vpc':{'Type':'AWS::EC2::VPC','Properties':{'CidrBlock':'10.23.0.0/16','Tags':[{'Key':'Name','Value':'Read-only VPC 日本語'}]}},
        'Subnet':{'Type':'AWS::EC2::Subnet','Properties':{'VpcId':{'Ref':'Vpc'},'CidrBlock':'10.23.1.0/24'}},
        'Table':{'Type':'AWS::EC2::RouteTable','Properties':{'VpcId':{'Ref':'Vpc'},'Tags':[{'Key':'Purpose','Value':'Browser readback'}]}},
        'Assoc':{'Type':'AWS::EC2::SubnetRouteTableAssociation','Properties':{'RouteTableId':{'Ref':'Table'},'SubnetId':{'Ref':'Subnet'}}},
        'Gateway':{'Type':'AWS::EC2::InternetGateway','Properties':{}},
        'Attach':{'Type':'AWS::EC2::VPCGatewayAttachment','Properties':{'VpcId':{'Ref':'Vpc'},'InternetGatewayId':{'Ref':'Gateway'}}},
        'Default':{'Type':'AWS::EC2::Route','DependsOn':'Attach','Properties':{'RouteTableId':{'Ref':'Table'},'GatewayId':{'Ref':'Gateway'},'DestinationCidrBlock':'0.0.0.0/0'}},
    }}))
    vpcs=client('ec2').describe_vpcs()['Vpcs']
    assert len(vpcs)==1
    vpc_id=vpcs[0]['VpcId']
    assert client('ec2','ap-northeast-1').describe_vpcs()['Vpcs']==[]
    browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
    page=browser.new_page(viewport={'width':1100,'height':800})
    page.goto(endpoint+'/_simulator/console/#/vpc')
    page.get_by_label('Access token',exact=True).fill(operator)
    page.get_by_role('button',name='Open sandbox',exact=True).click()
    # Login may return home; use the real catalog rather than injecting state.
    page.get_by_role('button',name='Services',exact=True).click()
    page.get_by_role('menuitem',name='VPC',exact=True).click()
    # Both the service navigation and page title say VPC; assert the main h1.
    expect(page.get_by_role('heading',name='VPC',level=1,exact=True)).to_be_visible()
    expect(page.get_by_text(vpc_id,exact=True)).to_be_visible()
    page.get_by_role('radio',name='Select '+vpc_id,exact=True).check()
    expect(page.get_by_role('heading',name='Resource details',exact=True)).to_be_visible()
    # Read the same real CFN-owned route table through the Console and native EC2 API.
    table, = client('ec2').describe_route_tables()['RouteTables']
    assert table['VpcId'] == vpc_id
    association, = table['Associations']
    route, = table['Routes']
    assert association['Main'] is False and route['DestinationCidrBlock'] == '0.0.0.0/0'
    for text in (table['RouteTableId'], association['RouteTableAssociationId'], association['SubnetId'],
                 route['DestinationCidrBlock'], route['GatewayId'], 'Purpose', 'Browser readback'):
        expect(page.get_by_text(text,exact=True).first).to_be_visible()
    page.get_by_role('button',name='Refresh route tables',exact=True).click()
    expect(page.get_by_text(association['SubnetId'],exact=True)).to_be_visible()
    expect(page.get_by_role('button',name='Start instance',exact=True)).to_have_count(0)
    # A CloudFormation-owned VPC is deleted with its stack, never from this view.
    expect(page.get_by_text('CloudFormation',exact=True).first).to_be_visible()
    expect(page.get_by_role('button',name='Delete VPC',exact=True)).to_be_disabled()
    page.get_by_role('button',name='Close details',exact=True).click()
    ec2=client('ec2')
    page.get_by_role('button',name='Create VPC',exact=True).click()
    expect(page.get_by_role('heading',name='Create VPC',level=1,exact=True)).to_be_visible()
    page.get_by_label('Name tag - optional',exact=True).fill('Console VPC 日本語')
    page.get_by_label('IPv4 CIDR',exact=True).fill('10.0.0.0/8')
    page.get_by_role('button',name='Create VPC',exact=True).click()
    # The server's refusal is shown as an error; nothing is created.
    expect(page.get_by_text('InvalidVpc.Range',exact=False).first).to_be_visible()
    assert [v['VpcId'] for v in ec2.describe_vpcs()['Vpcs']]==[vpc_id]
    page.get_by_label('IPv4 CIDR',exact=True).fill('10.24.0.0/16')
    page.get_by_role('button',name='Create VPC',exact=True).click()
    expect(page.get_by_role('heading',name='VPC',level=1,exact=True)).to_be_visible()
    native=[v for v in ec2.describe_vpcs()['Vpcs'] if v['VpcId']!=vpc_id]
    assert len(native)==1 and native[0]['CidrBlock']=='10.24.0.0/16' and native[0]['Tags']==[{'Key':'Name','Value':'Console VPC 日本語'}],native
    native_id=native[0]['VpcId']
    expect(page.get_by_text('You successfully created '+native_id,exact=True)).to_be_visible()
    expect(page.get_by_text('Console VPC 日本語',exact=True).first).to_be_visible()
    # The created VPC is selected; its delete requires typing delete.
    expect(page.get_by_role('radio',name='Select '+native_id,exact=True)).to_be_checked()
    expect(page.get_by_text('No detached route tables are saved for this VPC.',exact=True)).to_be_visible()
    expect(page.get_by_text(association['SubnetId'],exact=True)).to_have_count(0)
    expect(page.get_by_role('button',name='Refresh route tables',exact=True)).to_be_enabled()
    expect(page.get_by_role('button',name='Create route table',exact=True)).to_be_enabled()
    page.get_by_role('button',name='Delete VPC',exact=True).click()
    dialog=page.get_by_role('dialog')
    expect(dialog.get_by_role('button',name='Delete',exact=True)).to_be_disabled()
    dialog.get_by_label('To confirm deletion, type delete in the field.',exact=True).fill('delete')
    dialog.get_by_role('button',name='Delete',exact=True).click()
    expect(page.get_by_text('Deleted '+native_id,exact=True)).to_be_visible()
    expect(page.get_by_text(native_id,exact=True)).to_have_count(0)
    assert [v['VpcId'] for v in ec2.describe_vpcs()['Vpcs']]==[vpc_id]
    page.get_by_role('button',name='Refresh',exact=True).click()
    expect(page.get_by_text(vpc_id,exact=True)).to_be_visible()
    page.get_by_label('Filter resources',exact=True).fill('does-not-match-any-resource')
    expect(page.get_by_text('No resources match your filter.',exact=True)).to_be_visible()
    expect(page.get_by_text('No resources found in this region.',exact=True)).to_have_count(0)
    page.get_by_role('button',name='Clear filter',exact=True).click()
    expect(page.get_by_text(vpc_id,exact=True)).to_be_visible()
    def malformed(route):
        if 'Action=DescribeVpcs' in (route.request.post_data or ''):
            route.fulfill(status=200,content_type='text/xml',body='<DescribeVpcsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"/>')
        else:
            route.continue_()
    page.route('**/*',malformed)
    page.get_by_role('button',name='Refresh',exact=True).click()
    # Cloudscape Alert renders its header as text, not a heading element.
    expect(page.get_by_text('Could not load resources',exact=True)).to_be_visible()
    expect(page.get_by_text(vpc_id,exact=True)).to_have_count(0)
    expect(page.get_by_text('No resources found in this region.',exact=True)).to_have_count(0)
    page.unroute('**/*',malformed)
    page.get_by_role('button',name='Refresh',exact=True).click()
    expect(page.get_by_text(vpc_id,exact=True)).to_be_visible()
    page.get_by_label('Language',exact=True).select_option('ja')
    expect(page.get_by_role('heading',name='VPC',level=1,exact=True)).to_be_visible()
    expect(page.get_by_role('columnheader',name='名前',exact=True)).to_be_visible()
    page.set_viewport_size({'width':390,'height':844});assert_reflow(page)
    page.get_by_role('button',name='VPC を作成',exact=True).click()
    expect(page.get_by_role('heading',name='VPC を作成',level=1,exact=True)).to_be_visible()
    assert_reflow(page)
    page.get_by_role('button',name='キャンセル',exact=True).click()
    expect(page.get_by_role('heading',name='VPC',level=1,exact=True)).to_be_visible()
    page.get_by_role('button',name='サービス',exact=True).click()
    page.get_by_role('menuitem',name='EC2',exact=True).click()
    expect(page.get_by_role('heading',name='EC2 インスタンス',level=1,exact=True)).to_be_visible()
    expect(page.get_by_text('このリージョンには該当するリソースがありません。',exact=True)).to_be_visible()
    browser.close()
    cfn.delete_stack(StackName='readonly-console-vpc')
print('EC2/VPC console reads and creates/deletes VPCs through real local API state')
