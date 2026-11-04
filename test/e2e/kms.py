"""Actual local KMS SDK/identity/crypto/restart/two-team contract; no AWS calls."""
import base64,ipaddress,json,sys,tempfile,urllib.request
from urllib.parse import urlsplit
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,boto3,Config,ClientError

sdk_stats=[]
sdk_clients=[]
def local_target(url,endpoint):
 origin=urlsplit(endpoint);target=urlsplit(url)
 assert origin.scheme=='http' and origin.port and origin.username is None and origin.password is None
 assert ipaddress.ip_address(origin.hostname).is_loopback,'Only numeric-loopback endpoints are allowed'
 assert target.scheme=='http' and target.netloc==origin.netloc and target.username is None and target.password is None and not target.fragment,'Outbound target refused before send'
 assert ipaddress.ip_address(target.hostname).is_loopback

def client(service,endpoint,token,region='us-east-1'):
 local_target(endpoint,endpoint)
 stats={'service':service,'region':region,'calls':0,'sends':0,'responses':0};sdk_stats.append(stats)
 c=boto3.client(service,endpoint_url=endpoint,region_name=region,aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0},proxies={}))
 c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token))
 def called(**kwargs):stats['calls']+=1
 def sending(request,**kwargs):local_target(request.url,endpoint);stats['sends']+=1
 def responded(**kwargs):stats['responses']+=1
 c.meta.events.register('before-call.'+service,called);c.meta.events.register('before-send.'+service,sending);c.meta.events.register('after-call.'+service,responded)
 sdk_clients.append(c);return c

def denied(code,action):
 try:action()
 except ClientError as e:assert e.response['Error']['Code']==code,(code,e.response['Error'])
 else:raise AssertionError('Expected '+code)

def participant(endpoint,operator,name):
 request=urllib.request.Request(endpoint+'/_simulator/v1/access',data=json.dumps({'Principal':name}).encode(),headers={'Content-Type':'application/json','X-Simulator-Token':operator})
 local_target(request.full_url,endpoint)
 with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request) as response:return json.load(response)['AccessToken']

with tempfile.TemporaryDirectory(prefix='kms-sdk-') as d:
 state=Path(d)/'team-a.db'
 with running(state,authenticated=True) as (endpoint,operator),running(authenticated=True) as (other,other_operator):
  kms=client('kms',endpoint,operator);second=client('kms',other,other_operator)
  key=kms.create_key(Description='Local dummy key')['KeyMetadata'];kid=key['KeyId'];arn=key['Arn']
  assert key['KeyState']=='Enabled' and key['Enabled'] and key['KeySpec']=='SYMMETRIC_DEFAULT' and not key['MultiRegion']
  ctx={'team':'A','purpose':'demo'};plain=b'\x00\xfflocal dummy data';blob=kms.encrypt(KeyId=kid,Plaintext=plain,EncryptionContext=ctx)['CiphertextBlob']
  assert kms.decrypt(CiphertextBlob=blob,EncryptionContext={'purpose':'demo','team':'A'})['Plaintext']==plain
  assert kms.encrypt(KeyId=kid,Plaintext=plain,EncryptionContext=ctx)['CiphertextBlob']!=blob
  denied('InvalidCiphertextException',lambda:kms.decrypt(CiphertextBlob=blob,EncryptionContext={'team':'B','purpose':'demo'}))
  tampered=bytearray(blob);tampered[-1]^=1
  denied('InvalidCiphertextException',lambda:kms.decrypt(CiphertextBlob=bytes(tampered),EncryptionContext=ctx))
  wrong=kms.create_key()['KeyMetadata']['KeyId']
  denied('IncorrectKeyException',lambda:kms.decrypt(KeyId=wrong,CiphertextBlob=blob,EncryptionContext=ctx))
  denied('InvalidCiphertextException',lambda:client('kms',endpoint,operator,'us-west-2').decrypt(CiphertextBlob=blob,EncryptionContext=ctx))
  policy=kms.get_key_policy(KeyId=kid);assert policy['PolicyName']=='default';assert json.loads(policy['Policy'])['Statement'][0]['Principal']['AWS']=='arn:aws:iam::000000000000:root'
  assert len(kms.list_keys(Limit=1)['Keys'])==1 and kms.list_keys(Limit=1)['Truncated']
  assert len(client('kms',endpoint,operator,'us-west-2').list_keys()['Keys'])==0
  kms.disable_key(KeyId=kid);assert kms.describe_key(KeyId=arn)['KeyMetadata']['KeyState']=='Disabled'
  denied('DisabledException',lambda:kms.encrypt(KeyId=kid,Plaintext=plain));denied('DisabledException',lambda:kms.decrypt(CiphertextBlob=blob,EncryptionContext=ctx))
  kms.enable_key(KeyId=kid);assert kms.decrypt(CiphertextBlob=blob,EncryptionContext=ctx)['Plaintext']==plain
  iam=client('iam',endpoint,operator);role=iam.create_role(RoleName='kms-learner',AssumeRolePolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'sts:AssumeRole','Principal':{'Service':'lambda.amazonaws.com'}}]}))['Role']
  iam.put_role_policy(RoleName=role['RoleName'],PolicyName='KeyUse',PolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['kms:DescribeKey','kms:Encrypt','kms:Decrypt','kms:GetKeyPolicy'],'Resource':arn}]}))
  access=participant(endpoint,operator,role['RoleName']);learner=client('kms',endpoint,access)
  assert learner.describe_key(KeyId=kid)['KeyMetadata']['Arn']==arn
  assert learner.decrypt(CiphertextBlob=blob,EncryptionContext=ctx)['Plaintext']==plain
  denied('AccessDeniedException',lambda:learner.disable_key(KeyId=kid));denied('AccessDeniedException',lambda:learner.list_keys());denied('AccessDeniedException',lambda:learner.create_key())
  iam.put_role_policy(RoleName=role['RoleName'],PolicyName='KeyDeny',PolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Deny','Action':'kms:Decrypt','Resource':arn}]}))
  denied('AccessDeniedException',lambda:learner.decrypt(CiphertextBlob=blob,EncryptionContext=ctx))
  assert second.list_keys()['Keys']==[];denied('NotFoundException',lambda:second.decrypt(CiphertextBlob=blob,EncryptionContext=ctx))
  second.create_key(Description='Independent team B');assert len(second.list_keys()['Keys'])==1 and len(kms.list_keys()['Keys'])==2
  # The wrong host token must fail before key authorization or key lookup.
  try:client('kms',other,operator).list_keys()
  except ClientError as e:assert e.response['ResponseMetadata']['HTTPStatusCode']==401
  else:raise AssertionError('Team token crossed process authority')
  # Native option rejection is verified without changing the protocol or granting success.
  denied('ValidationException',lambda:kms.create_key(Tags=[{'TagKey':'x','TagValue':'y'}]))
  denied('ValidationException',lambda:kms.create_key(Policy='{}'))
  denied('UnknownOperationException',lambda:kms.create_alias(AliasName='alias/test',TargetKeyId=kid))
  iam.delete_role_policy(RoleName=role['RoleName'],PolicyName='KeyDeny');iam.delete_role_policy(RoleName=role['RoleName'],PolicyName='KeyUse');iam.delete_role(RoleName=role['RoleName'])
 with running(state,authenticated=True) as (endpoint,new_operator):
  resumed=client('kms',endpoint,new_operator);assert resumed.decrypt(CiphertextBlob=blob,EncryptionContext=ctx)['Plaintext']==plain
  assert resumed.describe_key(KeyId=kid)['KeyMetadata']['Description']=='Local dummy key'
  # Read the actual embedded reference from this exact running CLI, preserving
  # the same SQLite state and real SDK wire. Body examples do not select headers;
  # the official SDK chooses the matching TrentService operation explicitly.
  spec_request=urllib.request.Request(endpoint+'/_simulator/docs/openapi.json',headers={'X-Simulator-Token':new_operator})
  local_target(spec_request.full_url,endpoint)
  with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(spec_request) as response:served_spec=json.load(response)
  root_operation=served_spec['paths']['/']['post'];examples=root_operation['requestBody']['content']['application/x-amz-json-1.1']['examples'];mapping=root_operation['x-simulator-kms-operation-schemas']
  expected_ops={'CreateKey','DescribeKey','ListKeys','EnableKey','DisableKey','GetKeyPolicy','Encrypt','Decrypt'}
  assert set(mapping)==expected_ops and {name[3:] for name in examples if name.startswith('KMS')}==expected_ops
  for operation in expected_ops:assert mapping[operation]['target']=='TrentService.'+operation
  def template(operation):return json.loads(json.dumps(examples['KMS'+operation]['value']))
  docs_key=resumed.create_key(**template('CreateKey'))['KeyMetadata'];docs_id=docs_key['KeyId'];docs_arn=docs_key['Arn']
  assert docs_id not in {kid,wrong} and docs_arn=='arn:aws:kms:us-east-1:000000000000:key/'+docs_id
  describe=template('DescribeKey');describe['KeyId']=docs_id
  assert resumed.describe_key(**describe)['KeyMetadata']['Description']==template('CreateKey')['Description']
  page_input=template('ListKeys');all_docs_keys=[];seen_markers=set()
  while True:
   page=resumed.list_keys(**page_input);assert len(page['Keys'])<=page_input['Limit'];all_docs_keys+=page['Keys'];assert len(all_docs_keys)<=1000
   if not page['Truncated']:break
   assert page['NextMarker'] and page['NextMarker'] not in seen_markers;seen_markers.add(page['NextMarker']);page_input['Marker']=page['NextMarker']
  assert len({entry['KeyId'] for entry in all_docs_keys})==len(all_docs_keys)
  assert [entry['KeyArn'] for entry in all_docs_keys if entry['KeyId']==docs_id]==[docs_arn]
  policy_input=template('GetKeyPolicy');policy_input['KeyId']=docs_id
  assert resumed.get_key_policy(**policy_input)['PolicyName']=='default'
  encrypt_input=template('Encrypt');encrypt_input['KeyId']=docs_id;encrypt_input['Plaintext']=base64.b64decode(encrypt_input['Plaintext'],validate=True)
  docs_ciphertext=resumed.encrypt(**encrypt_input);assert docs_ciphertext['KeyId']==docs_arn
  decrypt_input=template('Decrypt')
  placeholder=base64.b64decode(decrypt_input['CiphertextBlob'],validate=True)
  denied('InvalidCiphertextException',lambda:resumed.decrypt(CiphertextBlob=placeholder,EncryptionContext=decrypt_input['EncryptionContext']))
  decrypt_input['CiphertextBlob']=docs_ciphertext['CiphertextBlob']
  assert resumed.decrypt(**decrypt_input)['Plaintext']==encrypt_input['Plaintext']
  disable_input=template('DisableKey');disable_input['KeyId']=docs_id;resumed.disable_key(**disable_input)
  assert resumed.describe_key(**describe)['KeyMetadata']['KeyState']=='Disabled'
  denied('DisabledException',lambda:resumed.decrypt(**decrypt_input))
  enable_input=template('EnableKey');enable_input['KeyId']=docs_id;resumed.enable_key(**enable_input)
  assert resumed.describe_key(**describe)['KeyMetadata']['KeyState']=='Enabled'
  assert resumed.decrypt(**decrypt_input)['Plaintext']==encrypt_input['Plaintext']
 for stats in sdk_stats:assert stats['calls']>0 and stats['calls']==stats['sends']==stats['responses'],stats
 totals={key:sum(stats[key] for stats in sdk_stats) for key in ('calls','sends','responses')}
 for c in sdk_clients:c.close()
 print(json.dumps({'nativeEightOperations':True,'servedOpenAPIExamples':8,'servedExamplesUseSameSQLite':True,'guardedSDKCounts':totals,'sdkClients':len(sdk_stats),'beforeSendLoopbackGuard':True,'binaryRoundtrip':True,'contextKeyRegionTamperGuards':True,'disabledGuard':True,'IAMExactKeyAndExplicitDeny':True,'twoTeamDBs':True,'crossToken':401,'restart':True,'scheduledDeletion':'unsupported; temporary fixture DBs removed'}))
