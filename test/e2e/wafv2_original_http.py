"""Real Cloudscape WAF + native API stage + pinned original WP Lambda.

This is an API/WAF vertical slice, not full original problem provisioning/scoring.
The original app source is unmodified; the existing test-only CFN projection
omits unsupported original resources and keeps the production problem disabled.
"""
import json,os,shutil,subprocess,sys,urllib.request,urllib.error
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'test/e2e'))
from check import running,boto3,Config,sync_playwright,expect,REPORTS,assert_reflow

challenge=Path(os.environ['CHALLENGE_ROOT'])/'challenges/wp2shell-friday-night-patch/template.yaml'
projection=json.loads(subprocess.check_output(['node','test/platform/wp-sdk-subset.mjs',str(challenge)],cwd=ROOT))
assert projection['handlerSha256']=='8d559b2b8ad72f7a281d3ef5cd3ba804d76cb01d6f2d45049baf56ee4c8a4f03','Pinned original handler changed'
with running(authenticated=True) as (endpoint,token),sync_playwright() as p:
 def sdk(service):
  client=boto3.client(service,endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
  client.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return client
 cfn=sdk('cloudformation');functions=sdk('lambda');api=sdk('apigateway');waf=sdk('wafv2')
 parameters={'NamePrefix':'tc-wp-team','TenkaCloudAccountId':'000000000000','ExternalId':'local-external-id-only','FlagSeed':'LocalDummySeed123'}
 cfn.create_stack(StackName='wp-waf-console-subset',TemplateBody=projection['template'],Parameters=[{'ParameterKey':k,'ParameterValue':v} for k,v in parameters.items()],Capabilities=['CAPABILITY_IAM'])
 # Warm the actual fixed interpreter before the outer HTTP invocation deadline.
 warm=functions.invoke(FunctionName='tc-wp-team-app',Payload=b'{}');assert 'FunctionError' not in warm;warm['Payload'].read()
 rest=api.create_rest_api(name='dummy-original-wp-waf',endpointConfiguration={'types':['REGIONAL']});identifier=rest['id']
 proxy=api.create_resource(restApiId=identifier,parentId=rest['rootResourceId'],pathPart='{proxy+}')['id']
 api.put_method(restApiId=identifier,resourceId=proxy,httpMethod='ANY',authorizationType='NONE',requestParameters={'method.request.path.proxy':True})
 api.put_integration(restApiId=identifier,resourceId=proxy,httpMethod='ANY',type='AWS_PROXY',integrationHttpMethod='POST',uri='arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:tc-wp-team-app/invocations')
 grant={'FunctionName':'tc-wp-team-app','StatementId':'original-wp-waf-api','Principal':'apigateway.amazonaws.com','Action':'lambda:InvokeFunction','SourceArn':'arn:aws:execute-api:us-east-1:000000000000:'+identifier+'/prod/POST/*'}
 functions.add_permission(**grant);deployment=api.create_deployment(restApiId=identifier)['id'];api.create_stage(restApiId=identifier,stageName='prod',deploymentId=deployment)
 resource_arn='arn:aws:apigateway:us-east-1::/restapis/'+identifier+'/stages/prod'
 def invoke(category):
  body=json.dumps({'requests':[{'method':'GET','path':'/wp2shell-lab/v1/reports/','body':{'category':category}}]}).encode()
  request=urllib.request.Request(endpoint+'/_simulator/providers/aws/_execute-api/us-east-1/'+identifier+'/prod/wp-json/batch/v1',data=body,headers={'X-Simulator-Token':token,'Content-Type':'application/json'})
  try:
   with urllib.request.urlopen(request,timeout=25) as response:return response.status,json.loads(response.read())
  except urllib.error.HTTPError as error:return error.code,json.loads(error.read())
 malicious="x' UNION SELECT id, marker, note FROM vault -- "
 status,payload=invoke(malicious);assert status==200 and any(row['title']=='deep-vault-row' for row in payload['responses'][0]['body']['reports'])
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox']);context=browser.new_context(viewport={'width':1440,'height':1000},bypass_csp=bool(os.environ.get('AXE_SCRIPT_PATH')));page=context.new_page();errors=[];outside=[]
 page.on('pageerror',lambda error:errors.append(str(error)))
 def route(r):
  if r.request.url.startswith(endpoint+'/'):r.continue_()
  else:outside.append(r.request.url);r.abort()
 page.route('**/*',route);page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click();page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='AWS WAF',exact=True).click()
 page.get_by_role('button',name='Create web ACL',exact=True).click();page.get_by_label('Name',exact=True).fill('dummy-original-wp-waf');page.get_by_role('button',name='Create web ACL',exact=True).click();expect(page.get_by_role('heading',name='dummy-original-wp-waf',exact=True)).to_be_visible()
 statement={'AndStatement':{'Statements':[{'ByteMatchStatement':{'SearchString':'L3dwLWpzb24vYmF0Y2gvdjE=','FieldToMatch':{'UriPath':{}},'PositionalConstraint':'ENDS_WITH','TextTransformations':[{'Priority':0,'Type':'NONE'}]}},{'SqliMatchStatement':{'FieldToMatch':{'Body':{'OversizeHandling':'CONTINUE'}},'TextTransformations':[{'Priority':0,'Type':'URL_DECODE'}]}}]}}
 page.get_by_role('button',name='Add rule',exact=True).click();page.get_by_label('Rule name',exact=True).fill('batch-sqli');page.get_by_label('Statement JSON',exact=True).fill(json.dumps(statement));page.get_by_role('button',name='Save',exact=True).click();expect(page.get_by_text('Rule saved.',exact=True)).to_be_visible()
 acl=waf.list_web_acls(Scope='REGIONAL')['WebACLs'][0];saved=waf.get_web_acl(Name=acl['Name'],Id=acl['Id'],Scope='REGIONAL')['WebACL'];assert saved['Rules'][0]['Action']=={'Count':{}}
 page.get_by_role('tab',name='Associated AWS resources',exact=True).click();page.get_by_role('button',name='Add AWS resources',exact=True).click();page.get_by_label('Resource ARN',exact=True).fill(resource_arn);page.get_by_role('button',name='Add AWS resources',exact=True).click();expect(page.get_by_text('API Gateway stage associated.',exact=True)).to_be_visible();assert waf.get_web_acl_for_resource(ResourceArn=resource_arn)['WebACL']['Id']==acl['Id']
 status,payload=invoke(malicious);assert status==200 and any(row['title']=='deep-vault-row' for row in payload['responses'][0]['body']['reports']),'Count must reach the original app'
 page.get_by_role('tab',name='Rules',exact=True).click();page.get_by_role('radio',name='Select batch-sqli',exact=True).check();page.get_by_role('button',name='Edit rule',exact=True).click();page.get_by_label('Action',exact=True).click();page.get_by_role('option',name='Block',exact=True).click();page.get_by_role('button',name='Save',exact=True).focus();page.keyboard.press('Enter');expect(page.get_by_text('Rule saved.',exact=True)).to_be_visible()
 blocked=invoke(malicious);assert blocked[0]==403,blocked;safe=invoke('general');assert safe[0]==200 and safe[1]['responses'][0]['status']==200,safe
 # No Lambda invocation grant: attack must still terminate in WAF while the safe
 # request reaches the Lambda authorization step and fails. Restore the grant.
 functions.remove_permission(FunctionName='tc-wp-team-app',StatementId=grant['StatementId']);assert invoke(malicious)[0]==403;assert invoke('general')[0]==502;functions.add_permission(**grant);assert invoke('general')[0]==200
 axe=os.environ.get('AXE_SCRIPT_PATH')
 if axe:page.add_script_tag(path=axe)
 for width in (1440,390):
  page.set_viewport_size({'width':width,'height':1000});assert_reflow(page)
  page.screenshot(path=str(REPORTS/('wafv2-original-http-'+str(width)+'.png')),full_page=True,animations='disabled')
  if axe:
   audit=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})");assert not audit['violations'],audit['violations']
 page.set_viewport_size({'width':1440,'height':1000});page.get_by_role('tab',name='Associated AWS resources',exact=True).click();page.get_by_role('button',name='Remove association',exact=True).click();page.get_by_role('button',name='Remove association',exact=True).click();expect(page.get_by_text('API Gateway stage disassociated.',exact=True)).to_be_visible();assert waf.list_resources_for_web_acl(WebACLArn=acl['ARN'])['ResourceArns']==[]
 page.get_by_role('button',name='Delete web ACL',exact=True).click();page.get_by_label('Enter web ACL name to confirm',exact=True).fill(acl['Name']);page.get_by_role('button',name='Delete web ACL',exact=True).click();expect(page.get_by_text('Web ACL deleted.',exact=True)).to_be_visible();assert waf.list_web_acls(Scope='REGIONAL')['WebACLs']==[]
 api.delete_rest_api(restApiId=identifier);cfn.delete_stack(StackName='wp-waf-console-subset');assert functions.list_functions()['Functions']==[];assert errors==[],errors;assert outside==[],outside;browser.close()
 print(json.dumps({'originalHandlerSha256':projection['handlerSha256'],'unmodifiedOriginalApp':True,'actualNativeUIAssociation':True,'countAllowsOriginalSQLLeak':True,'blockHTTPStatus':403,'safeHTTPStatus':200,'blockBeforeLambdaAuthorization':True,'keyboardSave':True,'desktop390Reflow':True,'axe':bool(axe),'browserErrors':0,'outsideRequests':0,'teardown':'DELETED','fullProblemSupported':False,'authenticatedAWSScreenObserved':False}))
