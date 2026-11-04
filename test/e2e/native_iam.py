"""Actual native IAM Query SDK/state/Lambda checks; no real AWS endpoints."""
import io,json,sys,zipfile,urllib.parse
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,boto3,Config,ClientError

trust={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'sts:AssumeRole','Principal':{'Service':'lambda.amazonaws.com'}}]}
with running(authenticated=True) as (endpoint,token):
 def client(service,principal=None):
  c=boto3.client(service,endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(s3={'addressing_style':'path'},retries={'max_attempts':0},request_checksum_calculation='when_required',response_checksum_validation='when_required'))
  def headers(request,**kwargs):
   request.headers.add_header('X-Simulator-Token',token)
   if principal:request.headers.add_header('X-Simulator-Principal',principal)
  c.meta.events.register('before-sign.'+service,headers);return c
 def error(code,action):
  try:action()
  except ClientError as e:assert e.response['Error']['Code']==code,(code,e.response['Error'])
  else:raise AssertionError('Expected '+code)
 iam=client('iam');functions=client('lambda');s3=client('s3')
 role=iam.create_role(RoleName='NativeExecutor',AssumeRolePolicyDocument=json.dumps(trust))['Role']
 assert role['AssumeRolePolicyDocument']==trust
 assert role['MaxSessionDuration']==3600 and role['Path']=='/'
 assert iam.get_role(RoleName='nativeexecutor')['Role']['RoleId']==role['RoleId']
 error('EntityAlreadyExists',lambda:iam.create_role(RoleName='nativeexecutor',AssumeRolePolicyDocument=json.dumps(trust)))
 assert iam.list_role_policies(RoleName=role['RoleName'])['PolicyNames']==[]
 assert [r['RoleName'] for r in iam.list_roles()['Roles']]==['NativeExecutor']
 code=io.BytesIO()
 with zipfile.ZipFile(code,'w') as z:z.writestr('app.py','def handler(event,context):\n    if event.get("read"):\n        import boto3\n        return {"stored": boto3.client("s3").get_object(Bucket="native-role-bucket",Key="value")["Body"].read().decode("utf-8")}\n    return {"answer": event["value"] * 6}')
 functions.create_function(FunctionName='native-role-function',Runtime='python3.12',Role=role['Arn'],Handler='app.handler',Timeout=10,Code={'ZipFile':code.getvalue()})
 result=functions.invoke(FunctionName='native-role-function',Payload=b'{"value":7}');assert 'FunctionError' not in result;assert json.loads(result['Payload'].read())=={'answer':42}
 s3.create_bucket(Bucket='native-role-bucket');s3.put_object(Bucket='native-role-bucket',Key='value',Body=b'dummy role state')
 restricted=client('s3','NativeExecutor');error('AccessDenied',lambda:restricted.get_object(Bucket='native-role-bucket',Key='value'))
 denied=functions.invoke(FunctionName='native-role-function',Payload=b'{"read":true}');payload=denied['Payload'].read();assert denied.get('FunctionError')=='Unhandled' and b'AccessDenied' in payload and b'dummy role state' not in payload
 policy={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'s3:GetObject','Resource':'arn:aws:s3:::native-role-bucket/*'}]}
 iam.put_role_policy(RoleName='NativeExecutor',PolicyName='ReadObjects',PolicyDocument=json.dumps(policy))
 assert restricted.get_object(Bucket='native-role-bucket',Key='value')['Body'].read()==b'dummy role state'
 granted=functions.invoke(FunctionName='native-role-function',Payload=b'{"read":true}');assert 'FunctionError' not in granted;assert json.loads(granted['Payload'].read())=={'stored':'dummy role state'}
 assert iam.get_role_policy(RoleName='NativeExecutor',PolicyName='ReadObjects')['PolicyDocument']==policy
 error('DeleteConflict',lambda:iam.delete_role(RoleName='NativeExecutor'))
 wrong=json.loads(json.dumps(trust));wrong['Statement'][0]['Principal']['Service']='ec2.amazonaws.com'
 iam.update_assume_role_policy(RoleName='NativeExecutor',PolicyDocument=json.dumps(wrong))
 error('InvalidParameterValueException',lambda:functions.invoke(FunctionName='native-role-function',Payload=b'{}'))
 iam.update_assume_role_policy(RoleName='NativeExecutor',PolicyDocument=json.dumps(trust))
 iam.delete_role_policy(RoleName='NativeExecutor',PolicyName='ReadObjects')
 revoked=functions.invoke(FunctionName='native-role-function',Payload=b'{"read":true}');payload=revoked['Payload'].read();assert revoked.get('FunctionError')=='Unhandled' and b'AccessDenied' in payload and b'dummy role state' not in payload
 iam.delete_role(RoleName='NativeExecutor')
 error('NoSuchEntity',lambda:iam.get_role(RoleName='NativeExecutor'))
 error('InvalidParameterValueException',lambda:functions.invoke(FunctionName='native-role-function',Payload=b'{}'))
 functions.delete_function(FunctionName='native-role-function');s3.delete_object(Bucket='native-role-bucket',Key='value');s3.delete_bucket(Bucket='native-role-bucket')
 assert iam.list_roles()['Roles']==[]
 print(json.dumps({'nativeIAMQuerySDK':True,'realLambdaAnswer':42,'emptyRoleNoServiceGrant':True,'inlinePolicySharesNativeState':True,'realGuestSDKUsesNativeRolePolicies':True,'wrongTrustRejected':True,'retiredRoleNoFallback':True,'teardown':'DELETED','realAWS':False}))

# The same role name has independent state in separate actual processes and DBs.
with running(authenticated=True) as (first,first_token),running(authenticated=True) as (second,second_token):
 import urllib.request,urllib.error
 def control(url,token,path,method='GET',body=None,status=200):
  request=urllib.request.Request(url+path,data=json.dumps(body).encode() if body is not None else None,method=method,headers={'X-Simulator-Token':token,'Content-Type':'application/json'})
  try:
   with urllib.request.urlopen(request) as response:
    assert response.status==status
    data=response.read();return json.loads(data) if data else None
  except urllib.error.HTTPError as error:
   assert error.code==status,(error.code,error.read())
 def iam_at(url,token):
  c=boto3.client('iam',endpoint_url=url,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
  c.meta.events.register('before-sign.iam',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return c
 one,two=iam_at(first,first_token),iam_at(second,second_token)
 a=one.create_role(RoleName='shared-name',AssumeRolePolicyDocument=json.dumps(trust),Description='team one')['Role']
 b=two.create_role(RoleName='shared-name',AssumeRolePolicyDocument=json.dumps(trust),Description='team two')['Role']
 assert a['RoleId']!=b['RoleId'] and one.get_role(RoleName='shared-name')['Role']['Description']=='team one'
 assert two.get_role(RoleName='shared-name')['Role']['Description']=='team two'
 control(second,first_token,'/_simulator/v1/iam/principals',status=401)
 for method in ('PUT','DELETE'):
  control(first,first_token,'/_simulator/v1/iam/principals/SHARED-NAME/policy',method,policy if method=='PUT' else None,status=409)
 local={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'iam:GetRole','Resource':a['Arn']}]}
 control(first,first_token,'/_simulator/v1/iam/principals/role-reader/policy','PUT',local)
 participant=control(first,first_token,'/_simulator/v1/access','POST',{'Principal':'role-reader'})['AccessToken']
 limited=iam_at(first,participant)
 assert limited.get_role(RoleName='shared-name')['Role']['RoleId']==a['RoleId']
 error('AccessDenied',lambda:limited.list_roles())
 error('AccessDenied',lambda:limited.delete_role(RoleName='shared-name'))
 one.delete_role(RoleName='shared-name')
 assert two.get_role(RoleName='shared-name')['Role']['RoleId']==b['RoleId']
 control(first,first_token,'/_simulator/v1/iam/principals/shared-name/policy','PUT',policy,status=409)
 two.delete_role(RoleName='shared-name')
 print(json.dumps({'twoTeamIAMIsolation':True,'tokenAuthoritiesIndependent':True,'participantIAMActionEnforced':True,'legacyCannotAdoptActiveOrRetiredNativeRole':True,'teardown':'DELETED'}))

# The official Cloudscape wizard and permissions/trust tabs call native IAM Query.
from check import sync_playwright,expect,REPORTS,assert_reflow,os,shutil
with running(authenticated=True) as (endpoint,token),sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
 page=browser.new_page(viewport={'width':1440,'height':1000});errors=[];outside=[]
 page.on('pageerror',lambda e:errors.append(str(e)))
 def route(r):
  if r.request.url.startswith(endpoint+'/'):r.continue_()
  else:outside.append(r.request.url);r.abort()
 page.route('**/*',route)
 page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click()
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='IAM',exact=True).click()
 page.get_by_role('button',name='Create role',exact=True).first.click();expect(page.get_by_role('radio',name='AWS service',exact=True)).to_be_checked()
 page.screenshot(path=str(REPORTS/'native-iam-trusted-entity-desktop.png'),full_page=True)
 page.set_viewport_size({'width':390,'height':844});assert_reflow(page);page.screenshot(path=str(REPORTS/'native-iam-trusted-entity-mobile.png'),full_page=True);page.set_viewport_size({'width':1440,'height':1000})
 page.get_by_role('button',name='Next',exact=True).click();expect(page.get_by_text('Choose a service or use case.',exact=True)).to_be_visible();page.get_by_label('Service or use case',exact=True).click();page.get_by_role('option',name='Lambda',exact=True).click();page.get_by_role('button',name='Next',exact=True).click();expect(page.get_by_text('No permissions added',exact=True)).to_be_visible()
 page.get_by_role('button',name='Next',exact=True).click();page.get_by_label('Role name',exact=True).fill('native-console-executor');page.get_by_label('Description',exact=True).fill('Dummy local Lambda role')
 page.get_by_role('button',name='Create role',exact=True).click();expect(page.get_by_role('heading',name='native-console-executor',exact=True)).to_be_visible()
 expect(page.get_by_text('No permissions policies. Service access is implicitly denied.',exact=True)).to_be_visible()
 page.get_by_role('tab',name='Trust relationships',exact=True).click();assert json.loads(page.get_by_label('Trust policy JSON',exact=True).input_value())==trust
 page.screenshot(path=str(REPORTS/'native-iam-role-trust-desktop.png'),full_page=True)
 page.get_by_role('tab',name='Permissions',exact=True).click();page.get_by_role('button',name='Create inline policy',exact=True).click();page.get_by_label('Policy name',exact=True).fill('ReadOwnBucket');page.get_by_label('Permissions policy JSON',exact=True).fill(json.dumps(policy));page.get_by_role('button',name='Save changes',exact=True).click();expect(page.get_by_role('link',name='ReadOwnBucket',exact=True)).to_be_visible()
 page.get_by_role('link',name='ReadOwnBucket',exact=True).click();assert json.loads(page.get_by_label('Permissions policy JSON',exact=True).input_value())==policy;page.get_by_role('button',name='Cancel',exact=True).click()
 # A delayed GetRolePolicy must not overwrite a newly opened inline-policy draft.
 pending=[]
 def hold_policy(route):
  if urllib.parse.parse_qs(route.request.post_data or '').get('Action')==['GetRolePolicy']:pending.append(route)
  else:route.fallback()
 page.route(endpoint+'/',hold_policy);page.get_by_role('link',name='ReadOwnBucket',exact=True).click();page.get_by_role('button',name='Create inline policy',exact=True).click();page.get_by_label('Policy name',exact=True).fill('DraftMustRemain');draft=json.loads(json.dumps(policy));draft['Statement'][0]['Sid']='DraftMustRemain';page.get_by_label('Permissions policy JSON',exact=True).fill(json.dumps(draft))
 with page.expect_response(lambda response:response.request.method=='POST' and urllib.parse.parse_qs(response.request.post_data or '').get('Action')==['GetRolePolicy']):pending[0].continue_()
 page.evaluate('()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');assert page.get_by_label('Policy name',exact=True).input_value()=='DraftMustRemain';assert json.loads(page.get_by_label('Permissions policy JSON',exact=True).input_value())==draft;page.unroute(endpoint+'/',hold_policy);page.get_by_role('button',name='Cancel',exact=True).click()

 with page.expect_response(lambda response:response.url.endswith('/_simulator/v1/iam/principals') and response.request.method=='GET') as principals:
  page.get_by_role('button',name='Local identities',exact=True).click()
 assert 'native-console-executor' in principals.value.json()['principals']
 expect(page.get_by_role('heading',name='No roles yet',exact=True)).to_be_visible();expect(page.get_by_role('link',name='native-console-executor',exact=True)).to_have_count(0);page.get_by_role('button',name='Native IAM roles',exact=True).click();page.get_by_role('link',name='native-console-executor',exact=True).click()
 page.get_by_role('button',name='Create a Lambda function',exact=True).click();page.get_by_role('button',name='Create function',exact=True).first.click();page.get_by_label('Function name',exact=True).fill('native-console-function');page.get_by_label('Existing role',exact=True).click();page.get_by_role('option',name='native-console-executor',exact=True).click();page.get_by_role('button',name='Create function',exact=True).last.click();expect(page.get_by_role('heading',name='native-console-function',exact=True)).to_be_visible()
 page.get_by_role('tab',name='Code',exact=True).click();data=io.BytesIO()
 with zipfile.ZipFile(data,'w') as z:z.writestr('lambda_function.py','def lambda_handler(event,context): return {"native_role_answer": event["value"] * 6}')
 page.get_by_label('Function ZIP',exact=True).set_input_files({'name':'function.zip','mimeType':'application/zip','buffer':data.getvalue()});page.get_by_role('button',name='Deploy',exact=True).click();expect(page.get_by_text('Function code deployed.',exact=True)).to_be_visible();page.get_by_role('tab',name='Test',exact=True).click();page.get_by_role('button',name='Test',exact=True).click();expect(page.get_by_text('Execution succeeded',exact=True)).to_be_visible(timeout=20000);assert json.loads(page.get_by_label('Response',exact=True).input_value())=={'native_role_answer':42}
 page.screenshot(path=str(REPORTS/'native-iam-lambda-executed.png'),full_page=True)
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='IAM',exact=True).click();page.get_by_role('link',name='native-console-executor',exact=True).click();page.get_by_role('button',name='Delete policy ReadOwnBucket',exact=True).click();expect(page.get_by_text('No permissions policies. Service access is implicitly denied.',exact=True)).to_be_visible()
 page.get_by_role('button',name='Delete role',exact=True).click();dialog=page.get_by_role('dialog');dialog.get_by_label('Confirm role name',exact=True).fill('native-console-executor');dialog.get_by_role('button',name='Delete role',exact=True).click();expect(page.get_by_role('heading',name='No roles yet',exact=True)).to_be_visible()
 native=iam_at(endpoint,token)
 for n in range(21):native.create_role(RoleName=f'page-role-{n:02d}',AssumeRolePolicyDocument=json.dumps(trust))
 page.get_by_role('button',name='Refresh roles',exact=True).click();expect(page.get_by_role('link',name='page-role-00',exact=True)).to_be_visible();expect(page.get_by_role('link',name='page-role-20',exact=True)).to_have_count(0);page.get_by_role('button',name='Next page',exact=True).click();expect(page.get_by_role('link',name='page-role-20',exact=True)).to_be_visible();expect(page.get_by_role('link',name='page-role-00',exact=True)).to_have_count(0)
 page.get_by_placeholder('Find roles',exact=True).fill('page-role-00');expect(page.get_by_role('link',name='page-role-00',exact=True)).to_be_visible()
 for n in range(21):native.delete_role(RoleName=f'page-role-{n:02d}')
 response=page.request.delete(endpoint+'/2015-03-31/functions/native-console-function',headers={'X-Simulator-Service':'lambda','X-Simulator-Region':'us-east-1'});assert response.status==204
 from native_iam_docs import verify_native_iam_docs
 verify_native_iam_docs(page,endpoint,native)
 assert errors==[],errors;assert outside==[],outside
 browser.close()
 print(json.dumps({'nativeIAMCloudscapeWizard':True,'inlinePolicyReadback':True,'delayedReadCannotReplaceDraft':True,'nativeRoleSelectedInLambda':True,'legacyAggregatesHidden':True,'rolePaginationAndFilter':True,'realDeployedPythonAnswer':42,'scalarCreatesAndListsNativeRoles':True,'desktopAndMobileReflow':True,'externalRequests':0,'browserErrors':0,'teardown':'DELETED'}))
