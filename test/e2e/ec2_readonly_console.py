"""Real CFN VPC -> readonly EC2 Query and console, no guest or cloud launch."""
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
    cfn.create_stack(StackName='readonly-console-vpc',TemplateBody=json.dumps({'Resources':{'Vpc':{'Type':'AWS::EC2::VPC','Properties':{'CidrBlock':'10.23.0.0/16','Tags':[{'Key':'Name','Value':'Read-only VPC 日本語'}]}}}}))
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
    expect(page.get_by_role('heading',name='VPC',exact=True)).to_be_visible()
    expect(page.get_by_text(vpc_id,exact=True)).to_be_visible()
    page.get_by_role('radio',name='Select '+vpc_id,exact=True).check()
    expect(page.get_by_role('heading',name='Resource details',exact=True)).to_be_visible()
    expect(page.get_by_role('button',name='Start instance',exact=True)).to_have_count(0)
    expect(page.get_by_role('button',name='Create VPC',exact=True)).to_have_count(0)
    page.get_by_role('button',name='Close details',exact=True).click()
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
    expect(page.get_by_role('heading',name='Could not load resources',exact=True)).to_be_visible()
    expect(page.get_by_text(vpc_id,exact=True)).to_have_count(0)
    expect(page.get_by_text('No resources found in this region.',exact=True)).to_have_count(0)
    page.unroute('**/*',malformed)
    page.get_by_role('button',name='Refresh',exact=True).click()
    expect(page.get_by_text(vpc_id,exact=True)).to_be_visible()
    page.get_by_label('Language',exact=True).select_option('ja')
    expect(page.get_by_role('heading',name='VPC 一覧',exact=True)).to_be_visible()
    page.set_viewport_size({'width':390,'height':844});assert_reflow(page)
    page.get_by_role('button',name='サービス',exact=True).click()
    page.get_by_role('menuitem',name='EC2',exact=True).click()
    expect(page.get_by_role('heading',name='EC2 インスタンス',exact=True)).to_be_visible()
    expect(page.get_by_text('このリージョンには該当するリソースがありません。',exact=True)).to_be_visible()
    browser.close()
    cfn.delete_stack(StackName='readonly-console-vpc')
print('Read-only EC2/VPC console uses real local API state')
