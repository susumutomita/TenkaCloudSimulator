"""Real participant STS -> one-use handoff -> bounded Cloudscape SSM GUI.

Only dummy local values are shown. Credentials and handoff codes are never
printed, included in screenshots, or persisted in reports.
"""
import json, os, shutil, urllib.request
from pathlib import Path
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials
from check import running, boto3, Config, sync_playwright, expect, REPORTS, assert_reflow

axe=Path(os.environ['AXE_SCRIPT']).read_text()
with running(authenticated=True) as (endpoint,operator), sync_playwright() as p:
 def client(service):
  return boto3.client(service,endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMOPERATOR',aws_secret_access_key=operator,config=Config(retries={'max_attempts':0}))
 iam=client('iam');ssm=client('ssm');sts=client('sts')
 role='console-handoff-viewer';name='/console-handoff/own';other='/console-handoff/other'
 arn='arn:aws:iam::000000000000:role/'+role
 iam.create_role(RoleName=role,AssumeRolePolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Principal':{'AWS':'arn:aws:iam::999999999999:root'},'Action':'sts:AssumeRole'}]}))
 def policy(actions):
  iam.put_role_policy(RoleName=role,PolicyName='Viewer',PolicyDocument=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':actions,'Resource':'*'}]}))
 policy(['ssm:GetParameter','ssm:DescribeParameters'])
 ssm.put_parameter(Name=name,Value='local-practice-value',Type='String');ssm.put_parameter(Name=other,Value='must-not-appear',Type='String')
 c=sts.assume_role(RoleArn=arn,RoleSessionName='console-browser')['Credentials']
 def handoff():
  data=json.dumps({'ParameterName':name,'Region':'us-east-1'}).encode()
  req=AWSRequest(method='POST',url=endpoint+'/_simulator/v1/console-handoffs',data=data,headers={'Content-Type':'application/json'})
  SigV4Auth(Credentials(c['AccessKeyId'],c['SecretAccessKey'],c['SessionToken']),'sts','us-east-1').add_auth(req)
  with urllib.request.urlopen(urllib.request.Request(req.url,data=data,headers=dict(req.headers),method='POST')) as response:return json.load(response)['Code']
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox'])
 context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page();errors=[];requests=[]
 page.on('pageerror',lambda error:errors.append(str(error)))
 page.on('request',lambda request:requests.append((request.method,request.url,request.post_data)))
 code=handoff()
 # Different local site, genuine document navigation, no credential in referrer.
 page.goto(endpoint.replace('127.0.0.1','localhost')+'/_simulator/login')
 page.evaluate('(url)=>location.assign(url)',endpoint+'/_simulator/login#handoff='+code)
 expect(page.get_by_label('Value',exact=True)).to_have_value('local-practice-value')
 assert 'handoff=' not in page.url
 assert not any('handoff=' in request[1] for request in requests),'fragment leaked into HTTP'
 assert not any('DescribeParameters' in (request[2] or '') for request in requests)
 for width in (1440,390):
  page.set_viewport_size({'width':width,'height':1000});assert_reflow(page)
  # Use the same audit instrumentation as console_logout; keep the site CSP intact.
  page.evaluate(axe);audit=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21a','wcag21aa']}})")
  assert not audit['violations'],[v['id'] for v in audit['violations']]
  page.screenshot(path=str(REPORTS/('console-handoff-'+str(width)+'.png')),full_page=True)
 page.set_viewport_size({'width':1440,'height':1000})
 # Reload and list/detail use only the bound parameter, despite broad role grants.
 page.reload();expect(page.get_by_label('Value',exact=True)).to_have_value('local-practice-value')
 page.goto(endpoint+'/_simulator/console/#/ssm')
 page.get_by_role('link',name=name,exact=True).click();expect(page.get_by_label('Value',exact=True)).to_have_value('local-practice-value')
 assert 'must-not-appear' not in page.content()
 def native(op,parameter):
  return page.evaluate("async({op,parameter})=>{const r=await fetch('/',{method:'POST',headers:{'Content-Type':'application/x-amz-json-1.1','X-Amz-Target':'AmazonSSM.'+op},body:JSON.stringify(op==='PutParameter'?{Name:parameter,Value:'changed',Type:'String',Overwrite:true}:{Name:parameter})});return r.status}",{'op':op,'parameter':parameter})
 assert native('GetParameter',other)==403
 assert native('DescribeParameters',name)==403
 assert native('PutParameter',name)==403
 # Denied request must not disturb the current page's valid browser session.
 page.get_by_role('button',name='Edit parameter',exact=True).click();page.get_by_label('Value',exact=True).fill('new-local-practice')
 # Cloudscape repeats form errors in a hidden live region; assert the visible error alert.
 page.get_by_role('button',name='Save changes',exact=True).click();expect(page.locator('form [data-analytics-alert="error"]').get_by_text('403:',exact=False)).to_be_visible();page.get_by_role('button',name='Cancel',exact=True).click()
 policy(['ssm:GetParameter','ssm:PutParameter'])
 page.get_by_role('button',name='Edit parameter',exact=True).click();page.get_by_label('Value',exact=True).fill('new-local-practice');page.get_by_role('button',name='Save changes',exact=True).click()
 expect(page.locator('[data-analytics-alert="success"]').get_by_text('Parameter saved.',exact=True)).to_be_visible()
 assert ssm.get_parameter(Name=name)['Parameter']['Value']=='new-local-practice'
 assert native('PutParameter',other)==403
 # One-use replay and role-generation revocation are exercised through HTTP.
 replay=context.request.post(endpoint+'/_simulator/login',form={'handoff':code});assert replay.status==401
 iam.delete_role_policy(RoleName=role,PolicyName='Viewer');iam.delete_role(RoleName=role)
 assert context.request.get(endpoint+'/_simulator/v1/capabilities').status==401
 page.reload();expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible()
 ssm.delete_parameter(Name=name);ssm.delete_parameter(Name=other)
 assert not errors,errors
 browser.close()
 print(json.dumps({'participantSTSHandoffGUI':True,'fragmentErased':True,'ownParameterListDetailRefresh':True,'crossResourceDenied':True,'enumerationDenied':True,'readonlyDenied':True,'allowedWrite':True,'replayDenied':True,'roleDeletionRevokesBrowser':True,'axeWidths':[1440,390],'browserErrors':0,'scope':'Simulator GUI; host answer submission tested separately'}))
