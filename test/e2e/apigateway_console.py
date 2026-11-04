"""Official Cloudscape REST create/integration/deploy/invoke flow, actual HTTP state."""
import io,json,os,re,shutil,sys,zipfile
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parent))
from check import running,boto3,Config,sync_playwright,expect,REPORTS,assert_reflow

def sdk(service,endpoint,token):
 c=boto3.client(service,endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
 c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return c
with running(authenticated=True) as (endpoint,token),sync_playwright() as p:
 iam=sdk('iam',endpoint,token);functions=sdk('lambda',endpoint,token);gateway=sdk('apigateway',endpoint,token)
 role=iam.create_role(RoleName='console-api-executor',AssumeRolePolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'sts:AssumeRole','Principal':{'Service':'lambda.amazonaws.com'}}]}))['Role']
 code=io.BytesIO()
 with zipfile.ZipFile(code,'w') as z:z.writestr('app.py','''import json
def handler(event,context):
 if 'cookie' in (event.get('headers') or {}): return {'statusCode':500,'body':'unexpected automatic Cookie in guest'}
 if (event.get('queryStringParameters') or {}).get('status') in ('400','401','500'):
  status=int(event['queryStringParameters']['status']);return {'statusCode':status,'body':'application status '+str(status)}
 if (event.get('queryStringParameters') or {}).get('html')=='1':
  return {'statusCode':200,'headers':{'Content-Type':'text/html','Set-Cookie':'tcsim_fake=stolen; Path=/','Location':'/_simulator/v1/access'},'body':'<script>window.unsafe=true;fetch("/_simulator/v1/access",{method:"POST"})</script>'}
 return {'statusCode':200,'body':json.dumps({'path':event.get('path','/warm'),'answer':42})}
''')
 functions.create_function(FunctionName='console-api-handler',Runtime='python3.12',Role=role['Arn'],Handler='app.handler',Timeout=10,Code={'ZipFile':code.getvalue()});warm=functions.invoke(FunctionName='console-api-handler',Payload=b'{}');assert 'FunctionError' not in warm;warm['Payload'].read()
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox']);context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page();errors=[];outside=[]
 page.on('pageerror',lambda e:errors.append(str(e)))
 def route(r):
  if r.request.url.startswith(endpoint+'/'):r.continue_()
  else:outside.append(r.request.url);r.abort()
 page.route('**/*',route);page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click()
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='API Gateway',exact=True).click();expect(page.get_by_role('heading',name=re.compile(r'^APIs \(\d+\)$'))).to_be_visible()
 # Invalidating a delayed list read through form navigation must not leave
 # the resource table permanently loading when its stale response completes.
 pending=[]
 def hold_first_list(route):
  if route.request.method=='GET' and not pending:pending.append(route)
  else:route.fallback()
 page.route(endpoint+'/restapis',hold_first_list);page.get_by_role('button',name='Refresh APIs',exact=True).click();expect(page.get_by_text('Loading APIs',exact=True)).to_be_visible();page.get_by_role('button',name='Create API',exact=True).click();page.get_by_role('button',name='Cancel',exact=True).click()
 assert pending
 with page.expect_response(lambda response:response.request.method=='GET' and response.url==endpoint+'/restapis'):pending[0].continue_()
 expect(page.get_by_text('Loading APIs',exact=True)).to_have_count(0);page.unroute(endpoint+'/restapis',hold_first_list)
 page.get_by_role('button',name='Create API',exact=True).click();page.get_by_label('API name',exact=True).fill('dummy-console-rest');page.get_by_label('Description',exact=True).fill('Local dummy API');assert_reflow(page)
 page.screenshot(path=str(REPORTS/'apigateway-create-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});assert_reflow(page);page.screenshot(path=str(REPORTS/'apigateway-create-mobile.png'),full_page=True);page.set_viewport_size({'width':1440,'height':1000})
 page.get_by_role('button',name='Create API',exact=True).click();expect(page.get_by_role('heading',name='dummy-console-rest',exact=True)).to_be_visible()
 expect(page.get_by_role('button',name='Dismiss API Gateway notification',exact=True)).to_be_visible()
 apis=gateway.get_rest_apis()['items'];assert len(apis)==1 and apis[0]['name']=='dummy-console-rest';identifier=apis[0]['id']
 page.get_by_role('button',name='Create resource',exact=True).click();page.get_by_label('Resource name',exact=True).fill('{proxy+}');page.get_by_role('button',name='Create resource',exact=True).click()
 page.get_by_role('button',name='Create method',exact=True).click();page.get_by_label('Lambda function',exact=True).click();page.get_by_role('option',name='console-api-handler',exact=True).click();page.get_by_role('button',name='Create method',exact=True).click();expect(page.get_by_text('Method and Lambda proxy integration created. An API-scoped Lambda invocation permission was added.',exact=True)).to_be_visible()
 policy=json.loads(functions.get_policy(FunctionName='console-api-handler')['Policy']);assert len(policy['Statement'])==1 and policy['Statement'][0]['Condition']['ArnLike']['AWS:SourceArn']=='arn:aws:execute-api:us-east-1:000000000000:'+identifier+'/*/*/*'
 page.get_by_role('button',name='Deploy API',exact=True).click();page.get_by_label('Stage name',exact=True).fill('prod');page.get_by_role('button',name='Deploy',exact=True).click();expect(page.get_by_text('API deployed to a new stage.',exact=True)).to_be_visible()
 assert gateway.get_stage(restApiId=identifier,stageName='prod')['deploymentId']
 page.get_by_label('Request path',exact=True).fill('/wp-json/batch/v1')
 page.context.add_cookies([{'name':'other_local_app_session','value':'private-dummy-other-app','url':endpoint+'/'}])
 page.get_by_role('button',name='Invoke stage',exact=True).click();expect(page.get_by_label('Invocation response',exact=True)).to_be_visible(timeout=20000);response=page.get_by_label('Invocation response',exact=True).input_value();assert response.startswith('200\n') and json.loads(response.split('\n',1)[1])=={'path':'/wp-json/batch/v1','answer':42}
 page.screenshot(path=str(REPORTS/'apigateway-invoke-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});assert_reflow(page);page.screenshot(path=str(REPORTS/'apigateway-invoke-mobile.png'),full_page=True);page.set_viewport_size({'width':1440,'height':1000})
 # Non-2xx guest responses are application data, including 401 rather than a host login redirect.
 for status in (400,401,500):
  page.get_by_label('Request path',exact=True).fill('/wp-json/batch/v1?status='+str(status));page.get_by_role('button',name='Invoke stage',exact=True).click();expect(page.get_by_label('Invocation response',exact=True)).to_have_value(str(status)+'\napplication status '+str(status),timeout=20000);assert '/_simulator/console/' in page.url
 page.get_by_label('Request path',exact=True).fill('/wp-json/batch/v1')
 # Actual navigation to guest HTML downloads without executing on console origin.
 cookies=page.context.cookies();popup=page.context.new_page();unwanted=[]
 page.context.on('request',lambda request:unwanted.append(request.url) if request.url.endswith('/_simulator/v1/access') else None)
 from playwright.sync_api import Error as BrowserError
 with popup.expect_download() as downloaded:
  try:popup.goto(endpoint+'/_simulator/providers/aws/_execute-api/us-east-1/'+identifier+'/prod/wp-json/batch/v1?html=1')
  except BrowserError as error:assert 'ERR_ABORTED' in str(error) or 'Download is starting' in str(error),str(error)
 assert downloaded.value.suggested_filename=='response.bin'
 assert popup.evaluate('typeof window.unsafe')=='undefined' and page.context.cookies()==cookies and unwanted==[]
 popup.close()
 # Host authority expiration still redirects to sign-in on the invocation path.
 saved_cookies=page.context.cookies();page.context.clear_cookies();page.get_by_role('button',name='Invoke stage',exact=True).click();expect(page.get_by_label('Access token',exact=True)).to_be_visible(timeout=20000);assert '/_simulator/login' in page.url
 page.context.add_cookies(saved_cookies);page.goto(endpoint+'/_simulator/console/');page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='API Gateway',exact=True).click();page.get_by_role('link',name='dummy-console-rest',exact=True).click()
 page.get_by_role('button',name='Delete API',exact=True).click();page.get_by_label('Enter confirm to delete',exact=True).fill('confirm');page.get_by_role('button',name='Delete API',exact=True).click();expect(page.get_by_role('heading',name=re.compile(r'^APIs \(\d+\)$'))).to_be_visible();assert gateway.get_rest_apis()['items']==[]
 from apigateway_docs import verify_rest_docs
 verify_rest_docs(page,endpoint,gateway)
 functions.delete_function(FunctionName='console-api-handler');iam.delete_role(RoleName=role['RoleName']);assert errors==[],errors;assert outside==[],outside;browser.close()
 print(json.dumps({'cloudscapeRESTCreate':True,'realLambdaProxyAndScopedPermission':True,'deployAndActualInvoke':42,'greedyConsoleGrantAndRoute':True,'staleListCannotFreezeLoading':True,'guest400401500Preserved':True,'hostExpiredAuthorityRedirects':True,'actualScalarCreateAndList':True,'guestHTMLDownloadsWithoutExecuting':True,'sameSDKState':True,'desktopAndMobileReflow':True,'externalRequests':0,'browserErrors':0,'teardown':'DELETED','authenticatedAWSScreenObserved':False}))
