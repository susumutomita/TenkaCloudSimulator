"""Real local SDK/API Gateway/Lambda vertical slice; no external AWS endpoints."""
import io,json,sys,zipfile,urllib.request,urllib.error
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,boto3,Config,ClientError

def client(service,endpoint,token,region='us-east-1'):
 c=boto3.client(service,endpoint_url=endpoint,region_name=region,aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
 c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return c

def expect_error(code,action):
 try:action()
 except ClientError as e:assert e.response['Error']['Code']==code,(code,e.response['Error'])
 else:raise AssertionError('Expected '+code)

with running(authenticated=True) as (endpoint,token):
 iam=client('iam',endpoint,token);functions=client('lambda',endpoint,token);api=client('apigateway',endpoint,token);ssm=client('ssm',endpoint,token)
 role=iam.create_role(RoleName='rest-proxy-role',AssumeRolePolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'sts:AssumeRole','Principal':{'Service':'lambda.amazonaws.com'}}]}))['Role']
 iam.put_role_policy(RoleName=role['RoleName'],PolicyName='ReadOwnParameter',PolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'ssm:GetParameter','Resource':'arn:aws:ssm:us-east-1:000000000000:parameter/api-gateway/team'}]}))
 ssm.put_parameter(Name='/api-gateway/team',Value='dummy first team',Type='String')
 code=io.BytesIO()
 with zipfile.ZipFile(code,'w') as z:z.writestr('app.py','''import json,boto3
def handler(event,context):
 if event.get('body')=='html':
  return {'statusCode':302,'headers':{'Content-Type':'text/html','Location':'/_simulator/console/','Set-Cookie':'tcsim_fake=stolen; Path=/','Access-Control-Allow-Origin':'*'},'body':'<script>window.unsafe=true</script>'}
 value=boto3.client('ssm').get_parameter(Name='/api-gateway/team')['Parameter']['Value']
 return {'statusCode':201,'headers':{'X-App-Result':'executed'},'body':json.dumps({'path':event['path'],'resource':event['resource'],'query':event['multiValueQueryStringParameters'],'headers':event['headers'],'proxy':event['pathParameters']['proxy'],'value':value})}
''')
 functions.create_function(FunctionName='rest-proxy-function',Runtime='python3.12',Role=role['Arn'],Handler='app.handler',Timeout=10,Code={'ZipFile':code.getvalue()})
 # Compile the fixed embedded interpreter before measuring integration execution.
 functions.invoke(FunctionName='rest-proxy-function',Payload=b'{}')['Payload'].read()
 rest=api.create_rest_api(name='local-proxy',endpointConfiguration={'types':['REGIONAL']},tags={'Team':'dummy'})
 api_id=rest['id'];root=rest['rootResourceId'];assert [item['id'] for item in api.get_rest_apis()['items']]==[api_id];assert api.get_resources(restApiId=api_id)['items'][0]['path']=='/';proxy=api.create_resource(restApiId=api_id,parentId=root,pathPart='{proxy+}')['id']
 api.put_method(restApiId=api_id,resourceId=proxy,httpMethod='ANY',authorizationType='NONE',requestParameters={'method.request.path.proxy':True})
 uri='arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:rest-proxy-function/invocations'
 api.put_integration(restApiId=api_id,resourceId=proxy,httpMethod='ANY',type='AWS_PROXY',integrationHttpMethod='POST',uri=uri)
 deployment=api.create_deployment(restApiId=api_id,description='frozen SDK snapshot')['id']
 api.create_stage(restApiId=api_id,stageName='prod',deploymentId=deployment,variables={'mode':'dummy'})
 path='/_simulator/providers/aws/_execute-api/us-east-1/'+api_id+'/prod/wp-json/batch/v1?query=one&query=two'
 def invoke(body=b'{}',method='POST',local_token=token,status=201):
  request=urllib.request.Request(endpoint+path,data=body,method=method,headers={'X-Simulator-Token':local_token,'X-Simulator-Principal':'simulator-admin','Authorization':'Bearer application-only-dummy','Cookie':'tcsim_fake=private-local-value; other_local_app=private-host-session; app_session=dummy','Content-Type':'application/json'})
  try:response=urllib.request.urlopen(request)
  except urllib.error.HTTPError as e:response=e
  with response:
   data=response.read();assert response.status==status,(response.status,data);return response.headers,data
 invoke(status=502)
 source='arn:aws:execute-api:us-east-1:000000000000:'+api_id+'/prod/POST/*'
 grant=functions.add_permission(FunctionName='rest-proxy-function',StatementId='allow-rest',Action='lambda:InvokeFunction',Principal='apigateway.amazonaws.com',SourceArn=source)
 assert json.loads(grant['Statement'])['Condition']['ArnLike']['AWS:SourceArn']==source
 policy=functions.get_policy(FunctionName='rest-proxy-function');old_revision=policy['RevisionId']
 headers,data=invoke();result=json.loads(data)
 assert result['value']=='dummy first team' and result['path']=='/wp-json/batch/v1' and result['resource']=='/{proxy+}' and result['proxy']=='wp-json/batch/v1' and result['query']['query']==['one','two']
 assert result['headers']['authorization']=='Bearer application-only-dummy' and 'cookie' not in result['headers']
 assert b'private-local' not in data and b'private-host-session' not in data and token.encode() not in data and 'x-simulator-token' not in result['headers']
 assert headers['Content-Type']=='application/octet-stream' and headers['Content-Disposition'].startswith('attachment;')
 invoke(method='GET',status=502);invoke(local_token='invalid-other-team-token',status=401)
 headers,data=invoke(b'html',status=302);assert b'<script>' in data and headers.get('Location') is None and headers.get('Set-Cookie') is None and headers.get('Access-Control-Allow-Origin') is None and headers['Content-Type']=='application/octet-stream' and 'sandbox;' in headers['Content-Security-Policy']
 api.put_integration(restApiId=api_id,resourceId=proxy,httpMethod='ANY',type='AWS_PROXY',integrationHttpMethod='POST',uri=uri.replace('rest-proxy-function','undeployed-target'))
 invoke() # Existing deployment still selects the prior function.
 api.delete_resource(restApiId=api_id,resourceId=proxy);invoke() # Frozen resource persists until the API is removed.
 functions.remove_permission(FunctionName='rest-proxy-function',StatementId='allow-rest',RevisionId=old_revision);invoke(status=502)
 expect_error('PreconditionFailedException',lambda:functions.add_permission(FunctionName='rest-proxy-function',StatementId='stale-grant',Action='lambda:InvokeFunction',Principal='apigateway.amazonaws.com',SourceArn=source,RevisionId=old_revision))
 api.delete_rest_api(restApiId=api_id);expect_error('NotFoundException',lambda:api.get_rest_api(restApiId=api_id))
 functions.delete_function(FunctionName='rest-proxy-function');iam.delete_role_policy(RoleName=role['RoleName'],PolicyName='ReadOwnParameter');iam.delete_role(RoleName=role['RoleName']);ssm.delete_parameter(Name='/api-gateway/team')
 print(json.dumps({'realAPIGatewaySDK':True,'nativeSourceARNPermission':True,'actualSavedRolePythonAndSSM':True,'frozenDeploymentAfterEditAndDelete':True,'duplicateQueryPreserved':True,'transportCredentialsRedacted':True,'untrustedHTMLDownloaded':True,'permissionRevocation':True,'teardown':'DELETED','realAWS':False}))

# Separate actual processes/DBs/token authorities: same names and paths must
# resolve to each team's own saved-role state, never a shared global registry.
with running(authenticated=True) as (first,first_token),running(authenticated=True) as (second,second_token):
 teams=[]
 for address,local_token,value in ((first,first_token,'team one dummy'),(second,second_token,'team two dummy')):
  roles=client('iam',address,local_token);lambdas=client('lambda',address,local_token);gateway=client('apigateway',address,local_token);parameters=client('ssm',address,local_token)
  role=roles.create_role(RoleName='same-proxy-role',AssumeRolePolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'sts:AssumeRole','Principal':{'Service':'lambda.amazonaws.com'}}]}))['Role']
  roles.put_role_policy(RoleName=role['RoleName'],PolicyName='ReadSameName',PolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'ssm:GetParameter','Resource':'arn:aws:ssm:us-east-1:000000000000:parameter/same-name'}]}))
  parameters.put_parameter(Name='/same-name',Type='String',Value=value)
  code=io.BytesIO()
  with zipfile.ZipFile(code,'w') as z:z.writestr('app.py','import boto3\ndef handler(event,context):\n return {"statusCode":200,"body":boto3.client("ssm").get_parameter(Name="/same-name")["Parameter"]["Value"]}')
  lambdas.create_function(FunctionName='same-proxy-function',Runtime='python3.12',Role=role['Arn'],Handler='app.handler',Timeout=10,Code={'ZipFile':code.getvalue()});warm=lambdas.invoke(FunctionName='same-proxy-function',Payload=b'{}');assert 'FunctionError' not in warm;warm['Payload'].read()
  rest=gateway.create_rest_api(name='same-api-name',endpointConfiguration={'types':['REGIONAL']});identifier=rest['id']
  gateway.put_method(restApiId=identifier,resourceId=rest['rootResourceId'],httpMethod='ANY',authorizationType='NONE')
  gateway.put_integration(restApiId=identifier,resourceId=rest['rootResourceId'],httpMethod='ANY',type='AWS_PROXY',integrationHttpMethod='POST',uri='arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:same-proxy-function/invocations')
  deployment=gateway.create_deployment(restApiId=identifier)['id'];gateway.create_stage(restApiId=identifier,stageName='prod',deploymentId=deployment)
  lambdas.add_permission(FunctionName='same-proxy-function',StatementId='same-permission',Action='lambda:InvokeFunction',Principal='apigateway.amazonaws.com',SourceArn='arn:aws:execute-api:us-east-1:000000000000:'+identifier+'/prod/*/*')
  teams.append((address,local_token,value,identifier,roles,lambdas,gateway,parameters))
 for address,local_token,value,identifier,roles,lambdas,gateway,parameters in teams:
  request=urllib.request.Request(address+'/_simulator/providers/aws/_execute-api/us-east-1/'+identifier+'/prod/',headers={'X-Simulator-Token':local_token})
  with urllib.request.urlopen(request) as response:assert response.status==200 and response.read().decode()==value
 request=urllib.request.Request(second+'/_simulator/providers/aws/_execute-api/us-east-1/'+teams[0][3]+'/prod/',headers={'X-Simulator-Token':second_token})
 try:urllib.request.urlopen(request)
 except urllib.error.HTTPError as e:assert e.code==404
 else:raise AssertionError('First team API crossed into second database')
 request=urllib.request.Request(second+'/_simulator/providers/aws/_execute-api/us-east-1/'+teams[1][3]+'/prod/',headers={'X-Simulator-Token':first_token})
 try:urllib.request.urlopen(request)
 except urllib.error.HTTPError as e:assert e.code==401
 else:raise AssertionError('First team authority crossed into second server')
 for address,local_token,value,identifier,roles,lambdas,gateway,parameters in teams:
  gateway.delete_rest_api(restApiId=identifier);lambdas.delete_function(FunctionName='same-proxy-function');roles.delete_role_policy(RoleName='same-proxy-role',PolicyName='ReadSameName');roles.delete_role(RoleName='same-proxy-role');parameters.delete_parameter(Name='/same-name')
 print(json.dumps({'twoActualTeamProcesses':True,'sameNamesIndependentSDKState':True,'savedRoleSSMPerTeam':True,'crossToken':401,'crossAPI':404,'teardown':'DELETED'}))

# Restart the actual binary with the same SQLite file: routes, stage snapshot,
# native function code/role and SourceArn policy must survive without old tokens.
import tempfile
with tempfile.TemporaryDirectory(prefix='tcsim-rest-restart-') as directory:
 state=Path(directory)/'state.sqlite'
 with running(state,authenticated=True) as (address,local_token):
  roles=client('iam',address,local_token);lambdas=client('lambda',address,local_token);gateway=client('apigateway',address,local_token)
  role=roles.create_role(RoleName='restart-role',AssumeRolePolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'sts:AssumeRole','Principal':{'Service':'lambda.amazonaws.com'}}]}))['Role']
  code=io.BytesIO()
  with zipfile.ZipFile(code,'w') as z:z.writestr('app.py','def handler(event,context): return {"statusCode":200,"body":"persisted dummy response"}')
  lambdas.create_function(FunctionName='restart-handler',Runtime='python3.12',Role=role['Arn'],Handler='app.handler',Timeout=10,Code={'ZipFile':code.getvalue()})
  rest=gateway.create_rest_api(name='restart-api',endpointConfiguration={'types':['REGIONAL']});identifier=rest['id']
  gateway.put_method(restApiId=identifier,resourceId=rest['rootResourceId'],httpMethod='ANY',authorizationType='NONE')
  gateway.put_integration(restApiId=identifier,resourceId=rest['rootResourceId'],httpMethod='ANY',type='AWS_PROXY',integrationHttpMethod='POST',uri='arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:restart-handler/invocations')
  deployment=gateway.create_deployment(restApiId=identifier)['id'];gateway.create_stage(restApiId=identifier,stageName='prod',deploymentId=deployment)
  lambdas.add_permission(FunctionName='restart-handler',StatementId='persistent-permission',Action='lambda:InvokeFunction',Principal='apigateway.amazonaws.com',SourceArn='arn:aws:execute-api:us-east-1:000000000000:'+identifier+'/prod/*/*')
  previous=local_token
 with running(state,authenticated=True) as (address,local_token):
  gateway=client('apigateway',address,local_token);lambdas=client('lambda',address,local_token);roles=client('iam',address,local_token)
  assert gateway.get_rest_api(restApiId=identifier)['name']=='restart-api' and gateway.get_stage(restApiId=identifier,stageName='prod')['deploymentId']==deployment
  assert json.loads(lambdas.get_policy(FunctionName='restart-handler')['Policy'])['Statement'][0]['Sid']=='persistent-permission'
  warm=lambdas.invoke(FunctionName='restart-handler',Payload=b'{}');assert 'FunctionError' not in warm;warm['Payload'].read()
  request=urllib.request.Request(address+'/_simulator/providers/aws/_execute-api/us-east-1/'+identifier+'/prod/',headers={'X-Simulator-Token':local_token})
  with urllib.request.urlopen(request) as response:assert response.status==200 and response.read()==b'persisted dummy response'
  request=urllib.request.Request(request.full_url,headers={'X-Simulator-Token':previous})
  try:urllib.request.urlopen(request)
  except urllib.error.HTTPError as e:assert e.code==401
  else:raise AssertionError('A token from the prior process remained valid')
  gateway.delete_rest_api(restApiId=identifier);lambdas.delete_function(FunctionName='restart-handler');roles.delete_role(RoleName='restart-role')
 print(json.dumps({'actualBinaryRestartPreservesRouteAndCodeAndPolicy':True,'oldAuthorityExpires':401,'teardown':'DELETED'}))
