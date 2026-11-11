"""Execute ten SERVED static SQS templates with explicit live substitutions.

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

from sqs_batch_delete import Team, NoRedirect
from openapi_json10 import validate_json10_examples, validate_json10_response
from test_sakura_openapi import NEW_IDS, COLLECTION, ITEM
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

OPERATIONS=('CreateQueue','ListQueues','GetQueueUrl','GetQueueAttributes',
            'SendMessage','ReceiveMessage','ChangeMessageVisibility',
            'DeleteMessage','DeleteMessageBatch','DeleteQueue')
MAIN_OPERATION_IDS=('AddPermission', 'AzureCreateContainer', 'AzureDeleteBlob', 'AzureDeleteContainer', 'AzureGetBlob', 'AzureGetBlobProperties', 'AzureGetContainerProperties', 'AzureListBlobs', 'AzureListContainers', 'AzurePutBlob', 'Capabilities', 'CreateBucket', 'CreateDeployment', 'CreateFunction', 'CreateResource', 'CreateRestApi', 'CreateStage', 'DeleteBucket', 'DeleteFunction', 'DeleteIdentityPolicy', 'DeleteObject', 'DeleteResource', 'DeleteRestApi', 'DeleteStage', 'GCPCreateBucket', 'GCPDeleteBucket', 'GCPDeleteObject', 'GCPDownloadObject', 'GCPGetBucket', 'GCPGetObject', 'GCPListBuckets', 'GCPListObjects', 'GCPUploadObject', 'GetDeployment', 'GetFunctionConfiguration', 'GetIdentityPolicy', 'GetMethod', 'GetObject', 'GetPolicy', 'GetResource', 'GetResources', 'GetRestApi', 'GetRestApis', 'GetStage', 'HeadBucket', 'HeadObject', 'IAMRoleQueryOperation', 'Invoke', 'InvokeLocalRESTProxyGet', 'InvokeLocalRESTProxyPost', 'ListBuckets', 'ListFunctions', 'ListObjectsV2OrGetBucketLocation', 'ListPrincipals', 'PutIdentityPolicy', 'PutIntegration', 'PutMethod', 'PutObject', 'RemovePermission', 'UpdateFunctionCode', 'awsRegionalOperation')
MAIN_PATHS=('/', '/2015-03-31/functions', '/2015-03-31/functions/{FunctionName}', '/2015-03-31/functions/{FunctionName}/code', '/2015-03-31/functions/{FunctionName}/configuration', '/2015-03-31/functions/{FunctionName}/invocations', '/2015-03-31/functions/{FunctionName}/policy', '/2015-03-31/functions/{FunctionName}/policy/{StatementId}', '/_simulator/providers/aws/', '/_simulator/providers/aws/_execute-api/{Region}/{RestApiId}/{StageName}/{ProxyPath}', '/_simulator/providers/azure/{StorageAccount}', '/_simulator/providers/azure/{StorageAccount}/{ContainerName}', '/_simulator/providers/azure/{StorageAccount}/{ContainerName}/{BlobName}', '/_simulator/providers/gcp/download/storage/v1/b/{BucketName}/o/{ObjectName}', '/_simulator/providers/gcp/storage/v1/b', '/_simulator/providers/gcp/storage/v1/b/{BucketName}', '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o', '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o/{ObjectName}', '/_simulator/providers/gcp/upload/storage/v1/b/{BucketName}/o', '/_simulator/v1/capabilities', '/_simulator/v1/iam/principals', '/_simulator/v1/iam/principals/{principal}/policy', '/restapis', '/restapis/{RestApiId}', '/restapis/{RestApiId}/deployments', '/restapis/{RestApiId}/deployments/{DeploymentId}', '/restapis/{RestApiId}/resources', '/restapis/{RestApiId}/resources/{ResourceId}', '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}', '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}/integration', '/restapis/{RestApiId}/stages', '/restapis/{RestApiId}/stages/{StageName}', '/{bucket}', '/{bucket}/{key}')
REPORTS=Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve()


def run():
 REPORTS.mkdir(parents=True,exist_ok=True)
 http_counts={'calls':0,'responses':0};sdk_counts={'calls':0,'sends':0,'responses':0}
 executed=[];live={};sdk=None
 with tempfile.TemporaryDirectory(prefix='tcsim-sqs-reference-') as directory:
  team=Team(Path(directory)/'team')
  try:
   team.start();origin=urlsplit(team.endpoint);opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
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
     result=json.load(response)
     if operation:validate_json10_response(spec,response.status,result)
     return result
   spec=wire('/_simulator/docs/openapi.json');caps=wire('/_simulator/v1/capabilities')
   source=json.loads(Path('internal/server/apidocs/openapi.json').read_text());assert spec==source,'Embedded served spec differs from source'
   validate_json10_examples(spec)
   ids=[value['operationId'] for path in spec['paths'].values() for method,value in path.items() if method in ('get','post','put','head','delete')]
   assert len(ids)==67 and len(set(ids))==67 and set(ids)==set(MAIN_OPERATION_IDS)|NEW_IDS|{'S3MultipartUpload'}
   assert spec['paths']['/{bucket}/{key}']['post']['operationId']=='S3MultipartUpload'
   assert set(spec['paths'])==set(MAIN_PATHS)|{COLLECTION,ITEM} and len(spec['paths'])==36
   root=spec['paths']['/']['post'];assert root['operationId']=='awsRegionalOperation'
   assert {'application/x-amz-json-1.0','application/x-amz-json-1.1','application/x-www-form-urlencoded'}<=set(root['requestBody']['content'])
   # JSON 1.0 is shared with DynamoDB. Keep the complete SQS contract while
   # requiring every additional service template to carry its own native target.
   all_examples=root['requestBody']['content']['application/x-amz-json-1.0']['examples']
   assert set(OPERATIONS)<=set(all_examples)
   examples={operation:all_examples[operation] for operation in OPERATIONS}
   for name,example in all_examples.items():
    if name not in OPERATIONS:
     assert name.startswith('DynamoDB')
     assert example['x-simulator-target']=='DynamoDB_20120810.'+name.removeprefix('DynamoDB')
   assert set(caps['services']['sqs']['operations'])==set(OPERATIONS)
   sdk=boto3.client('sqs',endpoint_url=team.endpoint,region_name='us-east-1',aws_access_key_id='TCSIMDOCS',aws_secret_access_key='dummy-local-only',config=Config(proxies={},retries={'max_attempts':0},connect_timeout=2,read_timeout=5));team.clients.append(sdk)
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
    if value=='__SECOND_RECEIPT_HANDLE__':return live['secondReceipt']
    if isinstance(value,str) and value.startswith('__') and value.endswith('__'):raise AssertionError('Unknown unresolved static placeholder')
    return value
   for operation in OPERATIONS:
    example=examples[operation];assert example['x-simulator-static-template'] is True and example['x-simulator-operation']==operation
    assert 'STATIC TEMPLATE' in example['description'] and 'AmazonSQS.'+operation in example['description']
    if operation=='DeleteMessageBatch':
     receipts=[]
     for body in ('batch template first','batch template second'):
      sent=sdk.send_message(QueueUrl=live['queue'],MessageBody=body)
      messages=sdk.receive_message(QueueUrl=live['queue'],VisibilityTimeout=30)['Messages']
      assert len(messages)==1 and messages[0]['MessageId']==sent['MessageId'] and messages[0]['Body']==body
      receipts.append(messages[0]['ReceiptHandle'])
     live['receipt'],live['secondReceipt']=receipts
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
    elif operation=='DeleteMessageBatch':
     assert response=={'Successful':[{'Id':entry['Id']} for entry in value['Entries']],'Failed':[]}
     validate_json10_response(spec,200,response,sqs_operation='DeleteMessageBatch')
     assert sdk.receive_message(QueueUrl=live['queue']).get('Messages',[])==[]
    elif operation=='DeleteQueue':assert response=={} and sdk.list_queues().get('QueueUrls',[])==[]
   assert tuple(executed)==OPERATIONS and http_counts['calls']==14==http_counts['responses']
   assert sdk_counts['calls']>0 and sdk_counts['calls']==sdk_counts['sends']==sdk_counts['responses']
   result={'servedSpecEqualsSource':True,'operationIDsPreserved':66,'totalOperationIDs':67,'pathsPreserved':36,'actualServedTemplatesExecuted':executed,'explicitLiveSubstitution':['QueueUrl','ReceiptHandle'],'MessageIdFromActualResponse':True,'SDKSameSQLiteBytes':True,'nativeReceiptRotationAndStaleRefusal':True,'unsubstitutedFixturePlaceholdersRefused':True,'staticIsNotLiveState':True,'HTTPCounts':http_counts,'SDKCounts':sdk_counts,'receiptAndCredentialValuesOmitted':True,'cleanup':'DELETED','realAWS':False,'uiTested':False}
   (REPORTS/'sqs-reference.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result))
  finally:team.stop()


if __name__=='__main__':run()
