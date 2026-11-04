"""Execute nine SERVED static SQS templates with explicit live substitutions.

Native SDK reads the same SQLite. No browser or actual AWS, and no receipt/token
values are emitted in reports. Run only against the actual owned local binary.
"""
import hashlib
import json
import os
from pathlib import Path
import tempfile
import urllib.request
import urllib.error
from urllib.parse import urlsplit

from sqs import Team, NoRedirect
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

OPERATIONS=('CreateQueue','ListQueues','GetQueueUrl','GetQueueAttributes',
            'SendMessage','ReceiveMessage','ChangeMessageVisibility',
            'DeleteMessage','DeleteQueue')
REPORTS=Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve()


def run():
 REPORTS.mkdir(parents=True,exist_ok=True)
 http_counts={'calls':0,'responses':0};sdk_counts={'calls':0,'sends':0,'responses':0}
 executed=[];live={};sdk=None
 with tempfile.TemporaryDirectory(prefix='tcsim-sqs-reference-') as directory:
  team=Team(directory)
  try:
   team.start();origin=urlsplit(team.endpoint);opener=urllib.request.build_opener(NoRedirect())
   def wire(path,body=None,operation=None,expected_status=200):
    assert path.startswith('/') and not path.startswith('//') and '://' not in path
    target=team.endpoint+path;assert urlsplit(target).netloc==origin.netloc and urlsplit(target).scheme=='http'
    headers={'X-Simulator-Token':team.token}
    if operation:headers.update({'Content-Type':'application/x-amz-json-1.0','X-Amz-Target':'AmazonSQS.'+operation,'X-Simulator-Service':'sqs','X-Simulator-Region':'us-east-1'})
    request=urllib.request.Request(target,data=json.dumps(body).encode() if body is not None else None,headers=headers,method='POST' if operation else 'GET')
    http_counts['calls']+=1
    try:response=opener.open(request,timeout=5)
    except urllib.error.HTTPError as failure:response=failure
    with response:
     http_counts['responses']+=1;assert response.status==expected_status,'Unexpected native HTTP status'
     if operation:assert response.headers['Content-Type'].startswith('application/x-amz-json-1.0')
     return json.load(response)
   spec=wire('/_simulator/docs/openapi.json');caps=wire('/_simulator/v1/capabilities')
   source=json.loads(Path('internal/server/apidocs/openapi.json').read_text());assert spec==source,'Embedded served spec differs from source'
   ids=[value['operationId'] for path in spec['paths'].values() for method,value in path.items() if method in ('get','post','put','head','delete')]
   assert len(ids)==43 and len(set(ids))==43 and len(spec['paths'])==25
   root=spec['paths']['/']['post'];assert root['operationId']=='awsRegionalOperation'
   assert {'application/x-amz-json-1.0','application/x-amz-json-1.1','application/x-www-form-urlencoded'}<=set(root['requestBody']['content'])
   examples=root['requestBody']['content']['application/x-amz-json-1.0']['examples'];assert set(examples)==set(OPERATIONS)
   assert set(caps['services']['sqs']['operations'])==set(OPERATIONS)
   sdk=boto3.client('sqs',endpoint_url=team.endpoint,region_name='us-east-1',aws_access_key_id='TCSIMDOCS',aws_secret_access_key='dummy-local-only',config=Config(retries={'max_attempts':0},connect_timeout=2,read_timeout=5));team.clients.append(sdk)
   sdk.meta.events.register('before-sign.sqs',lambda request,**_:request.headers.add_header('X-Simulator-Token',team.token))
   def before_call(**_):sdk_counts['calls']+=1
   def before_send(request,**_):
    parsed=urlsplit(request.url);assert parsed.scheme=='http' and parsed.netloc==origin.netloc,'SDK left owned loopback'
    sdk_counts['sends']+=1
   def after_call(http_response,**_):
    sdk_counts['responses']+=1;assert not 300<=http_response.status_code<400,'Unexpected SDK redirect'
   sdk.meta.events.register('before-call.sqs',before_call);sdk.meta.events.register('before-send.sqs',before_send);sdk.meta.events.register('after-call.sqs',after_call)
   def substitute(value):
    if isinstance(value,dict):return {key:substitute(item) for key,item in value.items()}
    if isinstance(value,list):return [substitute(item) for item in value]
    if value=='__QUEUE_URL__':return live['queue']
    if value=='__RECEIPT_HANDLE__':return live['receipt']
    if isinstance(value,str) and value.startswith('__') and value.endswith('__'):raise AssertionError('Unknown unresolved static placeholder')
    return value
   for operation in OPERATIONS:
    example=examples[operation];assert example['x-simulator-static-template'] is True and example['x-simulator-operation']==operation
    assert 'STATIC TEMPLATE' in example['description'] and 'AmazonSQS.'+operation in example['description']
    value=substitute(example['value']);response=wire('/',value,operation);executed.append(operation)
    if operation=='CreateQueue':
     live['queue']=response['QueueUrl'];live['name']=value['QueueName']
     assert sdk.get_queue_url(QueueName=live['name'])['QueueUrl']==live['queue']
     bad=wire('/',{'QueueUrl':'__QUEUE_URL__','AttributeNames':['All']},'GetQueueAttributes',400);assert bad['__type']=='AWS.SimpleQueueService.NonExistentQueue'
     attrs=sdk.get_queue_attributes(QueueUrl=live['queue'],AttributeNames=['All'])['Attributes']
     assert all(attrs[key]==item for key,item in value['Attributes'].items()),'Template attributes did not persist'
    elif operation=='ListQueues':assert response['QueueUrls']==[live['queue']]
    elif operation=='GetQueueUrl':assert response['QueueUrl']==live['queue']
    elif operation=='GetQueueAttributes':assert response['Attributes']==sdk.get_queue_attributes(QueueUrl=live['queue'],AttributeNames=['All'])['Attributes']
    elif operation=='SendMessage':
     live['messageId']=response['MessageId'];live['body']=value['MessageBody']
     assert response['MD5OfMessageBody']==hashlib.md5(live['body'].encode()).hexdigest()
     received=sdk.receive_message(QueueUrl=live['queue'],VisibilityTimeout=0)['Messages']
     assert len(received)==1 and received[0]['MessageId']==live['messageId'] and received[0]['Body']==live['body'],'SDK did not read HTTP template bytes from same SQLite'
     live['previousReceipt']=received[0]['ReceiptHandle']
    elif operation=='ReceiveMessage':
     received=response.get('Messages',[]);assert len(received)==1 and received[0]['MessageId']==live['messageId'] and received[0]['Body']==live['body']
     live['receipt']=received[0]['ReceiptHandle'];assert live['receipt'] and live['receipt']!=live['previousReceipt']
     try:sdk.delete_message(QueueUrl=live['queue'],ReceiptHandle=live['previousReceipt'])
     except ClientError as failure:assert failure.response['Error']['Code']=='ReceiptHandleIsInvalid'
     else:raise AssertionError('Stale native receipt unexpectedly acknowledged')
     before=sdk.get_queue_attributes(QueueUrl=live['queue'],AttributeNames=['All'])['Attributes'];assert before['ApproximateNumberOfMessagesNotVisible']=='1'
     bad=wire('/',{'QueueUrl':live['queue'],'ReceiptHandle':'__RECEIPT_HANDLE__','VisibilityTimeout':0},'ChangeMessageVisibility',400);assert bad['__type']=='ReceiptHandleIsInvalid'
     assert sdk.get_queue_attributes(QueueUrl=live['queue'],AttributeNames=['All'])['Attributes']==before,'Unsubstituted receipt mutated current queue'
    elif operation=='ChangeMessageVisibility':
     assert response=={};assert sdk.get_queue_attributes(QueueUrl=live['queue'],AttributeNames=['All'])['Attributes']['ApproximateNumberOfMessagesNotVisible']=='1'
    elif operation=='DeleteMessage':
     assert response=={} and sdk.receive_message(QueueUrl=live['queue']).get('Messages',[])==[]
    elif operation=='DeleteQueue':assert response=={} and sdk.list_queues().get('QueueUrls',[])==[]
   assert tuple(executed)==OPERATIONS and http_counts['calls']==13==http_counts['responses']
   assert sdk_counts['calls']>0 and sdk_counts['calls']==sdk_counts['sends']==sdk_counts['responses']
   result={'servedSpecEqualsSource':True,'operationIDsPreserved':43,'pathsPreserved':25,'actualServedTemplatesExecuted':executed,'explicitLiveSubstitution':['QueueUrl','ReceiptHandle'],'MessageIdFromActualResponse':True,'SDKSameSQLiteBytes':True,'nativeReceiptRotationAndStaleRefusal':True,'unsubstitutedFixturePlaceholdersRefused':True,'staticIsNotLiveState':True,'HTTPCounts':http_counts,'SDKCounts':sdk_counts,'receiptAndCredentialValuesOmitted':True,'cleanup':'DELETED','realAWS':False,'uiTested':False}
   (REPORTS/'sqs-reference.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result))
  finally:team.stop()


if __name__=='__main__':run()
