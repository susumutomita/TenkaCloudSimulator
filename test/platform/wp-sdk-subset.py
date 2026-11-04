"""Original WP handler via real Lambda and REST API; full CFN/WAF/grading absent."""
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.request

ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'test/e2e'))
from check import running, boto3, Config

challenge=Path(os.environ['CHALLENGE_ROOT'])/'challenges/wp2shell-friday-night-patch/template.yaml'
projection=json.loads(subprocess.check_output(['node','test/platform/wp-sdk-subset.mjs',str(challenge)],cwd=ROOT))
assert projection['handlerSha256']=='8d559b2b8ad72f7a281d3ef5cd3ba804d76cb01d6f2d45049baf56ee4c8a4f03', 'Pinned original handler changed'
with running(authenticated=True) as (endpoint,token):
    def client(service):
        c=boto3.client(service,endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
        c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token))
        return c
    cfn=client('cloudformation');functions=client('lambda');ssm=client('ssm');api=client('apigateway')
    parameters={'NamePrefix':'tc-wp-team','TenkaCloudAccountId':'000000000000','ExternalId':'local-external-id-only','FlagSeed':'LocalDummySeed123'}
    cfn.create_stack(StackName='wp-sdk-subset',TemplateBody=projection['template'],Parameters=[{'ParameterKey':k,'ParameterValue':v} for k,v in parameters.items()],Capabilities=['CAPABILITY_IAM'])
    event={'httpMethod':'POST','pathParameters':{'proxy':'wp-json/batch/v1'},'body':json.dumps({'requests':[{'method':'GET','path':'/wp2shell-lab/v1/reports/','body':{'category':"x' UNION SELECT id, marker, note FROM vault -- "}}]})}
    def invoke():
        result=functions.invoke(FunctionName='tc-wp-team-app',Payload=json.dumps(event).encode())
        if 'FunctionError' in result:raise AssertionError('Original handler execution failed: '+result['Payload'].read().decode())
        payload=json.loads(result['Payload'].read())
        assert payload['statusCode']==200
        return json.loads(payload['body'])['responses'][0]
    initial=invoke();assert initial['status']==200 and any(row['title']=='deep-vault-row' for row in initial['body']['reports'])
    rest=api.create_rest_api(name='original-wp-http-subset',endpointConfiguration={'types':['REGIONAL']})
    proxy=api.create_resource(restApiId=rest['id'],parentId=rest['rootResourceId'],pathPart='{proxy+}')['id']
    api.put_method(restApiId=rest['id'],resourceId=proxy,httpMethod='ANY',authorizationType='NONE',requestParameters={'method.request.path.proxy':True})
    api.put_integration(restApiId=rest['id'],resourceId=proxy,httpMethod='ANY',type='AWS_PROXY',integrationHttpMethod='POST',uri='arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:tc-wp-team-app/invocations')
    functions.add_permission(FunctionName='tc-wp-team-app',StatementId='original-wp-api',Principal='apigateway.amazonaws.com',Action='lambda:InvokeFunction',SourceArn='arn:aws:execute-api:us-east-1:000000000000:'+rest['id']+'/prod/POST/*')
    deployment=api.create_deployment(restApiId=rest['id'])['id'];api.create_stage(restApiId=rest['id'],stageName='prod',deploymentId=deployment)
    def invoke_http():
        request=urllib.request.Request(endpoint+'/_simulator/providers/aws/_execute-api/us-east-1/'+rest['id']+'/prod/wp-json/batch/v1',data=event['body'].encode(),headers={'X-Simulator-Token':token,'Content-Type':'application/json'})
        with urllib.request.urlopen(request) as response:
            assert response.status==200 and response.headers['Content-Type']=='application/octet-stream'
            return json.loads(response.read())['responses'][0]
    initial_http=invoke_http();assert initial_http['status']==200 and any(row['title']=='deep-vault-row' for row in initial_http['body']['reports'])
    ssm.put_parameter(Name='/tc-wp-team/config/parameterized-queries',Type='String',Value='true',Overwrite=True)
    parameterized=invoke();assert parameterized['status']==400 and parameterized['body']['error']=='invalid_category'
    assert invoke_http()['status']==400
    ssm.put_parameter(Name='/tc-wp-team/config/parameterized-queries',Type='String',Value='false',Overwrite=True)
    ssm.put_parameter(Name='/tc-wp-team/config/route-isolation',Type='String',Value='true',Overwrite=True)
    isolated=invoke();assert isolated['status']==401 and isolated['body']['error']=='rest_forbidden'
    assert invoke_http()['status']==401
    api.delete_rest_api(restApiId=rest['id'])
    cfn.delete_stack(StackName='wp-sdk-subset');assert functions.list_functions()['Functions']==[]
    print(json.dumps({'originalHandlerSha256':projection['handlerSha256'],'originalHandlerUnmodified':True,'initialSQLLeak':True,'actualRESTHTTPOriginalHandler':True,'parameterizedQueryStatus':400,'routeIsolationStatus':401,'teardown':'DELETED','fullProblemSupported':False,'browserAppHosting':False,'notExercisedByThisProjection':['CloudFormation API Gateway/Lambda::Permission provisioning','WAF','custom resource lifecycle','seven original grading checks']}))
