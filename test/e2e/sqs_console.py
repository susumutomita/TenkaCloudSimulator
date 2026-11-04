"""Prepared actual SQS UI/native SDK contract; SOURCE ONLY until run by root.

Never emit credential/receipt values. The browser uses the embedded binary and
native SQS APIs; SDK verifies mutations, separate-team state and cleanup.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile
import time
import urllib.request
from urllib.parse import urlsplit

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import sync_playwright, expect, Error as PlaywrightError
from sqs import Team, NoRedirect
from reflow import assert_reflow

REPORTS=Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve()


def run():
 REPORTS.mkdir(parents=True,exist_ok=True)
 axe_path=Path(os.environ['AXE_SCRIPT']);provenance=json.loads((axe_path.parent/'provenance.json').read_text())
 assert hashlib.sha256(axe_path.read_bytes()).hexdigest()==provenance['bundleSha256']
 counters={'calls':0,'sends':0,'responses':0};native_operations=set();browser_operations=set();errors=[];outside=[];console_output=[];receipts=[];audits=[]
 with tempfile.TemporaryDirectory(prefix='tcsim-sqs-console-') as directory:
  a=Team(Path(directory)/'a');b=Team(Path(directory)/'b');a.directory.mkdir();b.directory.mkdir()
  def client(team,region='us-east-1',token=None):
   result=boto3.client('sqs',endpoint_url=team.endpoint,region_name=region,aws_access_key_id='TCSIMSQSCONSOLE',aws_secret_access_key='dummy-local-only',config=Config(retries={'max_attempts':0},connect_timeout=2,read_timeout=5))
   result.meta.events.register('before-sign.sqs',lambda request,**_:request.headers.add_header('X-Simulator-Token',team.token if token is None else token))
   def before_call(model,**_):counters['calls']+=1;native_operations.add(model.name)
   def before_send(request,**_):
    assert urlsplit(request.url).scheme=='http' and urlsplit(request.url).netloc==urlsplit(team.endpoint).netloc,'SDK left owned loopback'
    counters['sends']+=1
   def after_call(http_response,**_):
    counters['responses']+=1;assert not 300<=http_response.status_code<400,'Unexpected SDK redirect'
   result.meta.events.register('before-call.sqs',before_call);result.meta.events.register('before-send.sqs',before_send);result.meta.events.register('after-call.sqs',after_call)
   team.clients.append(result);return result
  def control(path,body,method='POST'):
   request=urllib.request.Request(a.endpoint+path,data=json.dumps(body).encode(),headers={'Content-Type':'application/json','X-Simulator-Token':a.token},method=method)
   with urllib.request.build_opener(NoRedirect()).open(request,timeout=5) as response:
    assert response.status==200;return json.load(response)
  try:
   a.start();b.start();sdk=client(a);peer=client(b)
   wrong=client(b,token=a.token)
   try:wrong.list_queues()
   except ClientError as failure:assert failure.response['ResponseMetadata']['HTTPStatusCode']==401
   else:raise AssertionError('Peer accepted foreign team token')
   peer_url=peer.create_queue(QueueName='console-standard')['QueueUrl'];peer.send_message(QueueUrl=peer_url,MessageBody='peer-must-survive')
   with sync_playwright() as p:
    browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
    context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page()
    page.on('pageerror',lambda error:errors.append(str(error)));page.on('console',lambda message:console_output.append(message.text))
    def local_only(route):
     if urlsplit(route.request.url).scheme=='http' and urlsplit(route.request.url).netloc==urlsplit(a.endpoint).netloc:route.continue_()
     else:outside.append(urlsplit(route.request.url).netloc);route.abort()
    context.route('**/*',local_only)
    def observe_request(request):
     target=request.headers.get('x-amz-target','')
     if target.startswith('AmazonSQS.'):browser_operations.add(target.split('.',1)[1])
    def observe_response(response):
     if response.request.headers.get('x-amz-target')=='AmazonSQS.ReceiveMessage' and response.status==200:
      for message in response.json().get('Messages',[]):receipts.append(message['ReceiptHandle'])
    page.on('request',observe_request);page.on('response',observe_response)
    page.goto(a.endpoint+'/_simulator/console/#/sqs');page.get_by_label('Access token',exact=True).fill(a.token);page.get_by_role('button',name='Open sandbox',exact=True).click()
    # Login intentionally navigates to the console root. Use the actual service menu.
    page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='SQS',exact=True).click()
    expect(page.get_by_role('heading',level=1,name='Queues',exact=False)).to_be_visible()
    page.get_by_role('button',name='Create queue',exact=True).click();page.get_by_label('Name',exact=True).fill('cancelled-queue')
    page.get_by_role('button',name='Cancel',exact=True).click();assert sdk.list_queues().get('QueueUrls',[])==[],'Cancel submitted queue'
    page.get_by_role('button',name='Create queue',exact=True).click();page.get_by_label('Name',exact=True).fill('console-standard')
    page.get_by_label('Visibility timeout (seconds)',exact=True).fill('30');page.get_by_label('Message retention period (seconds)',exact=True).fill('60');page.get_by_label('Delivery delay (seconds)',exact=True).fill('0')
    page.get_by_label('Name',exact=True).focus();page.keyboard.press('Enter')
    expect(page.get_by_role('heading',level=1,name='console-standard',exact=True)).to_be_visible()
    url=sdk.get_queue_url(QueueName='console-standard')['QueueUrl'];attrs=sdk.get_queue_attributes(QueueUrl=url,AttributeNames=['All'])['Attributes']
    assert [attrs[key] for key in ('VisibilityTimeout','MessageRetentionPeriod','DelaySeconds')]==['30','60','0'],'UI configuration did not persist'
    def audit(label):
     for width in (1440,390):
      page.set_viewport_size({'width':width,'height':1000});assert_reflow(page);page.evaluate(axe_path.read_text())
      result=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
      assert not result['violations'],[item['id'] for item in result['violations']]
      page.screenshot(path=str(REPORTS/(label+'-'+str(width)+'.png')),full_page=False,animations='disabled');audits.append([label,width])
     page.set_viewport_size({'width':1440,'height':1000});assert_reflow(page)
    audit('sqs-details');page.get_by_role('button',name='Send and receive messages',exact=True).click()
    payload='<img src=x onerror="window.untrustedExecuted=true"> actual queue body'
    page.get_by_label('Message body',exact=True).fill(payload);page.get_by_role('button',name='Send message',exact=True).click();expect(page.get_by_text('Message sent.',exact=True)).to_be_visible()
    assert sdk.get_queue_attributes(QueueUrl=url,AttributeNames=['All'])['Attributes']['ApproximateNumberOfMessages']=='1'
    with page.expect_response(lambda response:response.request.headers.get('x-amz-target')=='AmazonSQS.ReceiveMessage' and response.status==200):
     page.get_by_role('button',name='Poll for messages',exact=True).click()
    expect(page.get_by_text('1 message(s) received.',exact=True)).to_be_visible()
    received=page.get_by_role('radio',name='Select message ',exact=False);expect(received).to_have_count(1);received.check()
    expect(page.get_by_label('Received message body',exact=True)).to_have_value(payload);assert page.evaluate('window.untrustedExecuted===undefined')
    assert receipts,'No actual native receipt observed';old_receipt=receipts[-1]
    for receipt in receipts:assert receipt not in page.content(),'Receipt leaked into rendered DOM'
    audit('sqs-messages')
    page.get_by_role('button',name='Change visibility',exact=True).click();dialog=page.get_by_role('dialog');dialog.get_by_label('New visibility timeout (seconds)',exact=True).fill('0');dialog.get_by_role('button',name='Change visibility',exact=True).click()
    expect(page.get_by_text('Message released. Poll again for a new receipt.',exact=True)).to_be_visible()
    with page.expect_response(lambda response:response.request.headers.get('x-amz-target')=='AmazonSQS.ReceiveMessage' and response.status==200):
     page.get_by_role('button',name='Poll for messages',exact=True).click()
    expect(page.get_by_text('1 message(s) received.',exact=True)).to_be_visible();received.check()
    assert receipts[-1]!=old_receipt,'Redelivery did not rotate native receipt'
    try:sdk.delete_message(QueueUrl=url,ReceiptHandle=old_receipt)
    except ClientError as failure:assert failure.response['Error']['Code']=='ReceiptHandleIsInvalid'
    else:raise AssertionError('Native stale receipt unexpectedly acknowledged')
    page.get_by_role('button',name='Delete message',exact=True).click();page.get_by_role('dialog').get_by_role('button',name='Delete',exact=True).click();expect(page.get_by_text('Message deleted.',exact=True)).to_be_visible()
    attrs=sdk.get_queue_attributes(QueueUrl=url,AttributeNames=['All'])['Attributes'];assert attrs['ApproximateNumberOfMessages']=='0' and attrs['ApproximateNumberOfMessagesNotVisible']=='0'
    # Participant cannot turn exercise selection into operator authority.
    policy={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['sqs:ListQueues','sqs:GetQueueUrl','sqs:GetQueueAttributes'],'Resource':'*'},{'Effect':'Deny','Action':'sqs:SendMessage','Resource':'*'}]}
    control('/_simulator/v1/iam/principals/queue-reader/policy',policy,method='PUT')
    participant=control('/_simulator/v1/access',{'Principal':'queue-reader'})['AccessToken']
    limited_context=browser.new_context(viewport={'width':1440,'height':1000});limited_context.route('**/*',local_only);limited=limited_context.new_page();limited.on('pageerror',lambda error:errors.append(str(error)));limited.on('console',lambda message:console_output.append(message.text))
    limited.goto(a.endpoint+'/_simulator/console/');limited.get_by_label('Access token',exact=True).fill(participant);limited.get_by_role('button',name='Open sandbox',exact=True).click()
    limited.get_by_role('button',name='Services',exact=True).click();limited.get_by_role('menuitem',name='SQS',exact=True).click();limited.get_by_role('link',name='console-standard',exact=True).click()
    limited.get_by_role('button',name='Send and receive messages',exact=True).click();limited.get_by_label('Message body',exact=True).fill('must-not-be-stored')
    before=sdk.get_queue_attributes(QueueUrl=url,AttributeNames=['All'])['Attributes']
    with limited.expect_response(lambda response:response.request.headers.get('x-amz-target')=='AmazonSQS.SendMessage' and response.status==403):limited.get_by_role('button',name='Send message',exact=True).click()
    expect(limited.get_by_text('Request could not be completed',exact=True)).to_be_visible();expect(limited.get_by_text('Message sent.',exact=True)).to_have_count(0)
    assert sdk.get_queue_attributes(QueueUrl=url,AttributeNames=['All'])['Attributes']==before,'Denied UI send changed native queue'
    limited_context.close()
    # Same-name west state must be the selected native region, not a local cache.
    west=client(a,'us-west-2');west_url=west.create_queue(QueueName='console-standard')['QueueUrl'];west.send_message(QueueUrl=west_url,MessageBody='west-only')
    page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='us-west-2',exact=True).click()
    page.get_by_role('link',name='console-standard',exact=True).click();page.get_by_role('button',name='Send and receive messages',exact=True).click();page.get_by_label('Visibility timeout (seconds)',exact=True).fill('0')
    with page.expect_response(lambda response:response.request.headers.get('x-amz-target')=='AmazonSQS.ReceiveMessage' and response.status==200):page.get_by_role('button',name='Poll for messages',exact=True).click()
    page.get_by_role('radio',name='Select message ',exact=False).check();expect(page.get_by_label('Received message body',exact=True)).to_have_value('west-only')
    assert sdk.get_queue_attributes(QueueUrl=url,AttributeNames=['All'])['Attributes']['ApproximateNumberOfMessages']=='0'
    # Hold an ACTUAL native poll response while changing region. Cancellation
    # may abort delivery; either way no old body/receipt may enter the new view.
    held=[]
    def delay_poll(route):
     assert urlsplit(route.request.url).netloc==urlsplit(a.endpoint).netloc
     if route.request.headers.get('x-amz-target')=='AmazonSQS.ReceiveMessage':held.append((route,route.fetch()))
     else:route.continue_()
    page.route(a.endpoint+'/',delay_poll);page.get_by_role('button',name='Poll for messages',exact=True).click()
    deadline=time.monotonic()+10
    while not held and time.monotonic()<deadline:page.wait_for_timeout(10)
    assert len(held)==1,'No actual delayed native response obtained'
    page.get_by_role('button',name='us-west-2',exact=True).click();page.get_by_role('menuitem',name='us-east-1',exact=True).click()
    route,response=held.pop()
    held_messages=response.json().get('Messages',[])
    assert len(held_messages)==1 and held_messages[0].get('Body')=='west-only','Held response was not actual west queue data'
    held_receipt=held_messages[0].get('ReceiptHandle');assert isinstance(held_receipt,str) and held_receipt,'Held response lacked native receipt'
    receipts.append(held_receipt)
    try:route.fulfill(response=response)
    except PlaywrightError as failure:
     assert any(part in str(failure).lower() for part in ('already handled','invalid interception','invalidinterception','target closed','request was cancelled','request was canceled')),'Unexpected delayed route delivery failure'
    page.unroute(a.endpoint+'/',delay_poll);expect(page.get_by_role('heading',level=1,name='Queues',exact=False)).to_be_visible()
    assert page.get_by_label('Received message body',exact=True).count()==0 and 'west-only' not in page.locator('body').inner_text(),'Late west response painted east view'
    west.delete_queue(QueueUrl=west_url)
    page.get_by_role('link',name='console-standard',exact=True).click()
    page.get_by_role('button',name='Delete',exact=True).click();page.get_by_label('Type delete to confirm',exact=True).fill('delete');page.get_by_role('dialog').get_by_role('button',name='Delete',exact=True).click();expect(page.get_by_text('Queue deleted.',exact=True)).to_be_visible()
    assert sdk.list_queues().get('QueueUrls',[])==[]
    persist=sdk.create_queue(QueueName='restart-check')['QueueUrl'];sdk.send_message(QueueUrl=persist,MessageBody='persisted-after-ui')
    saved_token=a.token;a.stop();a.start();assert a.token==saved_token,'Restart rotated SDK authority';sdk=client(a)
    page.goto(a.endpoint+'/_simulator/console/');expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible()
    page.get_by_label('Access token',exact=True).fill(a.token);page.get_by_role('button',name='Open sandbox',exact=True).click();page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='SQS',exact=True).click();expect(page.get_by_role('link',name='restart-check',exact=True)).to_be_visible()
    persisted=sdk.receive_message(QueueUrl=persist,VisibilityTimeout=0)['Messages'][0];assert persisted['Body']=='persisted-after-ui';sdk.delete_message(QueueUrl=persist,ReceiptHandle=persisted['ReceiptHandle']);sdk.delete_queue(QueueUrl=persist);assert sdk.list_queues().get('QueueUrls',[])==[]
    message=peer.receive_message(QueueUrl=peer_url,VisibilityTimeout=0)['Messages'][0];assert message['Body']=='peer-must-survive';peer.delete_message(QueueUrl=peer_url,ReceiptHandle=message['ReceiptHandle']);peer.delete_queue(QueueUrl=peer_url)
    for receipt in receipts:
     assert receipt not in page.content() and receipt not in page.url
     assert all(receipt not in line for line in console_output),'Receipt leaked to browser log'
     assert not page.evaluate('(value)=>Object.values(localStorage).some(x=>x.includes(value))||Object.values(sessionStorage).some(x=>x.includes(value))',receipt)
    assert browser_operations=={'CreateQueue','ListQueues','GetQueueUrl','DeleteQueue','GetQueueAttributes','SendMessage','ReceiveMessage','DeleteMessage','ChangeMessageVisibility'}
    assert not outside and not errors,'Unexpected external request or browser error'
    assert counters['calls']>0 and counters['calls']==counters['sends']==counters['responses'],'Vacuous or incomplete SDK transport accounting'
    browser.close()
   (REPORTS/'sqs-console-a11y.json').write_text(json.dumps({'states':audits,'violations':0},indent=2)+'\n')
   print(json.dumps({'actualEmbeddedUI':True,'nativeSDKSameState':True,'twoOwnedProcesses':True,'peerPreserved':True,'crossToken401':True,'participantDeniedSendNoMutation':True,'regionAndDelayedReplyIsolation':True,'restartNativeStateAndBrowserRelogin':True,'receiptValuesOmitted':True,'visibilityReleaseAndRotation':True,'nativeStaleReceiptRefused':True,'cancelAndKeyboard':True,'UIOperations':sorted(browser_operations),'SDKCounts':counters,'PC390Axe':True,'externalRequests':0,'cleanup':'DELETED','authenticatedAWSScreenObserved':False}))
  finally:
   try:a.stop()
   finally:b.stop()


if __name__=='__main__':run()
