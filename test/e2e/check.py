"""Real binary + boto3 + Chromium conformance. All requests target loopback.
Dev dependencies: scripts/requirements-test.txt. No AWS account or credentials.
"""
import contextlib
import base64
import io
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.request
import zipfile

os.environ['AWS_CONFIG_FILE']='/dev/null'
os.environ['AWS_SHARED_CREDENTIALS_FILE']='/dev/null'
os.environ['AWS_EC2_METADATA_DISABLED']='true'
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import sync_playwright, expect
from reflow import assert_reflow

BINARY=Path(os.environ.get('SIMULATOR_BINARY','dist/tenkacloud-simulator')).resolve()
REPORTS=Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve()
REPORTS.mkdir(parents=True,exist_ok=True)
@contextlib.contextmanager
def running(db=None,authenticated=False):
 with tempfile.TemporaryDirectory(prefix='tcsim-runtime-') as directory:
  standalone=Path(directory)/'simulator';shutil.copyfile(BINARY,standalone);standalone.chmod(0o700)
  with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
  endpoint=f'http://127.0.0.1:{port}'
  token_file=Path(directory)/'operator-token'
  args=[str(standalone),'--listen',f'127.0.0.1:{port}','--token-file',str(token_file)]
  if not authenticated:args+=['--unsafe-no-auth']
  if db:args+=['--state',str(db)]
  # No runtime executables on PATH and no repository files in cwd.
  env={**os.environ,'PATH':'/nonexistent'}
  process=subprocess.Popen(args,cwd=directory,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
  try:
   deadline=time.monotonic()+25
   while True:
    if process.poll() is not None:raise RuntimeError(process.communicate())
    try:
     token=token_file.read_text().strip() if authenticated and token_file.exists() else ''
     with urllib.request.urlopen(urllib.request.Request(endpoint+'/_simulator/health',headers={'X-Simulator-Token':token}),timeout=1) as response:
      if response.status==200:break
    except OSError:
     if time.monotonic()>deadline:raise TimeoutError('Binary did not become ready')
     time.sleep(.05)
   yield (endpoint,token) if authenticated else endpoint
  finally:
   if process.poll() is None:process.send_signal(signal.SIGTERM)
   try:stdout,stderr=process.communicate(timeout=8)
   except subprocess.TimeoutExpired:process.kill();stdout,stderr=process.communicate();raise AssertionError('Graceful shutdown timed out')
   if process.returncode!=0:raise AssertionError(f'Process exit {process.returncode}: {stdout} {stderr}')
def sdk(endpoint,principal=None):
 client=boto3.client('s3',endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMEXAMPLE',aws_secret_access_key='simulator-only',config=Config(s3={'addressing_style':'path'},retries={'max_attempts':0},request_checksum_calculation='when_required',response_checksum_validation='when_required'))
 if principal:client.meta.events.register('before-sign.s3',lambda request,**kwargs:request.headers.add_header('x-simulator-principal',principal))
 return client
def open_service(page,name):
 page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name=name,exact=True).click()
def submit(page):
 dialog=page.get_by_role('dialog')
 if dialog.is_visible():dialog.get_by_role('button').last.click()
 else:page.get_by_role('button',name=re.compile('^(Create bucket|Upload|Create parameter|Save changes)$')).last.click()
class Conformance(unittest.TestCase):
 def test_route53_reference_contract_detectors(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name("test_route53_openapi.py"))],check=True,timeout=60)

 def test_route53_native_public_zone_sdk(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name("route53.py"))],check=True,timeout=180)

 def test_sns_sqs_native_fanout_restart_and_isolation(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('sns_sqs_fanout.py'))],check=True,timeout=180)
 def test_scalar_aws_json_live_response_display(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('scalar_aws_json.py'))],check=True,timeout=180)

 def test_iam_ssm_builtin_sdk_pin_authority_and_cleanup(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('iam_ssm_builtin.py'))],check=True,timeout=180)
 def test_cfn_iam_profiles_sdk_composition_restart_and_cleanup(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('cfn_iam_profiles.py'))],check=True,timeout=180)
 def test_lambda_log_tail_explicit_console(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('lambda_log_tail_console.py'))],check=True,timeout=180)
 def test_iam_instance_profiles_sdk_isolation_restart_and_cleanup(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('iam_instance_profiles.py'))],check=True,timeout=180)
 def test_cloudformation_events_real_console_history(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('cloudformation_events_console.py'))],check=True,timeout=180)
 def test_s3_multipart_served_reference_examples(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('s3_multipart_docs.py'))],check=True,timeout=120)
 def test_s3_multipart_native_persistence_and_console(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('s3_multipart.py'))],check=True)
 def test_sts_native_sdk_original_hello_world(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('sts_sdk.py'))],check=True,timeout=180)
 def test_sns_native_admission_persistence_and_isolation(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name("sns_admission.py"))], check=True, timeout=180)

 def test_landing_topology_html_fallback_keyboard_and_public_navigation(self):
  from landing_topology import run_landing_topology
  # The fixture owns fresh dummy credentials for readiness only. The helper
  # receives no token, browser authentication, native API state or user keys.
  with running(authenticated=True) as (endpoint,_):
   result=run_landing_topology(endpoint,REPORTS/'landing-topology')
  self.assertEqual(result['cases'],8)
  self.assertEqual(result['strictAxeStates'],6)
 def test_sakura_reference_real_examples_and_scalar_send(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('sakura_reference_live.py')), '--root', str(Path(__file__).resolve().parents[2]), '--binary', str(BINARY), '--reports', str(REPORTS/'sakura-reference-live'), '--browser'], check=True, timeout=180)
 def test_sakura_disk_console_real_native_state_and_scope(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('sakura_disks_console.py'))], check=True, timeout=180)
 def test_sakura_native_reference_schema_and_preservation_controls(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('test_sakura_openapi.py'))], check=True)
 def test_sakura_opt_in_native_two_cli_authority_and_persistence(self):
  # Native stdlib HTTP, not SDK verification; failure is mandatory and unskipped.
  subprocess.run([sys.executable, str(Path(__file__).with_name('sakura_cli.py')), '--binary', str(BINARY), '--report', str(REPORTS/'sakura-cli.json')], check=True, timeout=180)
 def test_browser_actual_clipboard_login_and_safe_errors(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('login_paste.py'))],check=True)
 def test_browser_explicit_token_file_and_strict_authentication(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('login_token_file.py'))],check=True)
 def test_console_language_and_provider_catalog(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('console_language.py'))],check=True)
 def test_s3_provider_prefix_preserves_literal_keys(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('provider_paths.py'))],check=True)
 def test_azure_container_properties_native_typed_targets(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('azure_container_properties.py'))], check=True, timeout=180)
 def test_azure_live_properties_head_and_scope_lifecycle(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('azure_live_properties.py'))], check=True, timeout=180)
 def test_crossprovider_console_two_cli_state_and_authority(self):
  # Run the actual standalone acceptance as a required case, never a source-only
  # or optional/skip check. It must import the real browser/audit dependencies.
  subprocess.run([sys.executable, str(Path(__file__).with_name('crossprovider_console.py'))], check=True, timeout=300)
 def test_lambda_execution_role_sdk_bridge_two_teams(self):
  with running(authenticated=True) as (endpoint,operator),running(authenticated=True) as (other,other_operator):
   def client(service,url,token):
    c=boto3.client(service,endpoint_url=url,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(s3={'addressing_style':'path'},retries={'max_attempts':0},request_checksum_calculation='when_required',response_checksum_validation='when_required'))
    c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return c
   def control(url,token,path,data):
    request=urllib.request.Request(url+path,data=json.dumps(data).encode(),headers={'Content-Type':'application/json','X-Simulator-Token':token},method='POST' if path.endswith('/access') else 'PUT')
    with urllib.request.urlopen(request) as response:return json.load(response)
   role={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['ssm:GetParameter','ssm:GetParameters'],'Resource':'arn:aws:ssm:us-east-1:000000000000:parameter/sdk-own/*'},{'Effect':'Allow','Action':['s3:GetObject','s3:PutObject','s3:DeleteObject'],'Resource':'arn:aws:s3:::sdk-evidence/*'}]}
   source="""import boto3
from botocore.exceptions import ClientError
ssm=boto3.client('ssm')
s3=boto3.client('s3')
def handler(event,context):
    value=ssm.get_parameter(Name='/sdk-own/value')['Parameter']['Value']
    s3.put_object(Bucket='sdk-evidence',Key='result.txt',Body=value,ContentType='text/plain')
    result=s3.get_object(Bucket='sdk-evidence',Key='result.txt')['Body'].read().decode()
    try:
        ssm.get_parameter(Name='/sdk-private/value')
        denied=False
    except ClientError as error:
        denied=error.response['Error']['Code']=='AccessDeniedException'
    return {'value':result,'privateDenied':denied}
"""
   code=io.BytesIO()
   with zipfile.ZipFile(code,'w') as z:z.writestr('index.py',source)
   clients=[]
   for url,token,value in ((endpoint,operator,'team-one'),(other,other_operator,'team-two')):
    control(url,token,'/_simulator/v1/iam/principals/executor/policy',role);ssm=client('ssm',url,token);ssm.put_parameter(Name='/sdk-own/value',Type='String',Value=value);ssm.put_parameter(Name='/sdk-private/value',Type='String',Value='private-dummy');s3=client('s3',url,token);s3.create_bucket(Bucket='sdk-evidence');lc=client('lambda',url,token)
    created=lc.create_function(FunctionName='guest-sdk',Runtime='python3.12',Role='arn:aws:iam::000000000000:role/executor',Handler='index.handler',Timeout=15,Code={'ZipFile':code.getvalue()})
    control(url,token,'/_simulator/v1/iam/principals/invoker/policy',{'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'lambda:InvokeFunction','Resource':created['FunctionArn']}]});participant=control(url,token,'/_simulator/v1/access',{'Principal':'invoker'})['AccessToken'];limited=client('lambda',url,participant)
    with self.assertRaises(ClientError) as denied:client('ssm',url,participant).get_parameter(Name='/sdk-own/value')
    self.assertEqual(denied.exception.response['ResponseMetadata']['HTTPStatusCode'],403)
    result=limited.invoke(FunctionName='guest-sdk',Payload=b'{}');self.assertNotIn('FunctionError',result);self.assertEqual(json.loads(result['Payload'].read()),{'value':value,'privateDenied':True});self.assertEqual(s3.get_object(Bucket='sdk-evidence',Key='result.txt')['Body'].read().decode(),value);clients.append((limited,s3,participant))
   with self.assertRaises(ClientError) as denied:client('lambda',other,clients[0][2]).invoke(FunctionName='guest-sdk',Payload=b'{}')
   self.assertEqual(denied.exception.response['ResponseMetadata']['HTTPStatusCode'],401)
   role['Statement'].append({'Effect':'Deny','Action':'s3:PutObject','Resource':'arn:aws:s3:::sdk-evidence/result.txt'});control(endpoint,operator,'/_simulator/v1/iam/principals/executor/policy',role)
   result=clients[0][0].invoke(FunctionName='guest-sdk',Payload=b'{}');self.assertEqual(result['FunctionError'],'Unhandled');self.assertEqual(json.loads(result['Payload'].read())['errorType'],'ClientError');self.assertEqual(clients[0][1].get_object(Bucket='sdk-evidence',Key='result.txt')['Body'].read(),b'team-one');self.assertEqual(clients[1][1].get_object(Bucket='sdk-evidence',Key='result.txt')['Body'].read(),b'team-two')

 def test_sdk_persistence_isolation_and_process_lifecycle(self):
  with tempfile.TemporaryDirectory() as d:
   db=Path(d)/'state.db'
   with running(db) as endpoint:
    c=sdk(endpoint);self.assertEqual(sdk(endpoint+'/_simulator/providers/aws').list_buckets()['Buckets'],[]);c.create_bucket(Bucket='sdk-practice');c.put_object(Bucket='sdk-practice',Key='hello.txt',Body=b'from SDK',ContentType='text/plain');c.head_bucket(Bucket='sdk-practice')
    self.assertEqual(c.get_bucket_location(Bucket='sdk-practice')['LocationConstraint'],None)
    self.assertEqual(c.head_object(Bucket='sdk-practice',Key='hello.txt')['ContentLength'],8)
    self.assertEqual(c.get_object(Bucket='sdk-practice',Key='hello.txt',Range='bytes=0-3')['Body'].read(),b'from')
    self.assertEqual(c.list_objects_v2(Bucket='sdk-practice')['Contents'][0]['Key'],'hello.txt')
    with self.assertRaises(ClientError) as error:c.delete_bucket(Bucket='sdk-practice')
    self.assertEqual(error.exception.response['Error']['Code'],'BucketNotEmpty')
    c.put_object(Bucket='sdk-practice',Key='second.txt',Body=b'2')
    pages=list(c.get_paginator('list_objects_v2').paginate(Bucket='sdk-practice',PaginationConfig={'PageSize':1}));self.assertEqual(len(pages),2)
   with running(db) as endpoint:
    c=sdk(endpoint);self.assertEqual(c.get_object(Bucket='sdk-practice',Key='hello.txt')['Body'].read(),b'from SDK')
    c.delete_object(Bucket='sdk-practice',Key='hello.txt');c.delete_object(Bucket='sdk-practice',Key='second.txt');c.delete_bucket(Bucket='sdk-practice');self.assertEqual(c.list_buckets()['Buckets'],[])
   with running() as endpoint:self.assertEqual(sdk(endpoint).list_buckets()['Buckets'],[])
 def test_s3_versions_sdk_console_and_isolation(self):
  with running(authenticated=True) as (endpoint,token),running(authenticated=True) as (other,other_token),sync_playwright() as p:
   c=sdk(endpoint);c.meta.events.register('before-sign.s3',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));c.create_bucket(Bucket='version-evidence')
   other_client=sdk(other);other_client.meta.events.register('before-sign.s3',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',other_token))
   self.assertEqual(c.get_bucket_versioning(Bucket='version-evidence').get('Status'),None)
   self.assertTrue(all(c.get_public_access_block(Bucket='version-evidence')['PublicAccessBlockConfiguration'].values()))
   c.put_object(Bucket='version-evidence',Key='incident-log.json',Body=b'original evidence')
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
   browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox'])
   context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page();errors=[];outside=[]
   page.on('pageerror',lambda e:errors.append(str(e)))
   def route(request_route):
    if request_route.request.url.startswith(endpoint+'/'):request_route.continue_()
    else:outside.append(request_route.request.url);request_route.abort()
   context.route('**/*',route)
   page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click();open_service(page,'S3');page.get_by_role('link',name='version-evidence',exact=True).click()
   page.get_by_role('tab',name='Properties',exact=True).click();page.get_by_role('button',name='Edit',exact=True).click();page.get_by_role('radio',name='Enable',exact=True).check();page.get_by_role('button',name='Save changes',exact=True).click();expect(page.get_by_text('Changes saved.',exact=True)).to_be_visible()
   self.assertEqual(c.get_bucket_versioning(Bucket='version-evidence')['Status'],'Enabled')
   page.screenshot(path=str(REPORTS/'s3-versioning-properties.png'),full_page=True)
   page.get_by_role('tab',name='Permissions',exact=True).click();page.get_by_role('button',name='Edit',exact=True).click();page.get_by_role('checkbox',name='Block all public access',exact=True).uncheck();page.get_by_role('button',name='Save changes',exact=True).click();page.get_by_label('Type confirm to save changes',exact=True).fill('confirm');page.get_by_role('button',name='Confirm',exact=True).click();expect(page.get_by_text('Changes saved.',exact=True)).to_be_visible()
   self.assertFalse(any(c.get_public_access_block(Bucket='version-evidence')['PublicAccessBlockConfiguration'].values()))
   page.screenshot(path=str(REPORTS/'s3-public-access-settings.png'),full_page=True)
   updated=c.put_object(Bucket='version-evidence',Key='incident-log.json',Body=b'updated evidence')['VersionId'];marker=c.delete_object(Bucket='version-evidence',Key='incident-log.json')['VersionId']
   pages=list(c.get_paginator('list_object_versions').paginate(Bucket='version-evidence',PaginationConfig={'PageSize':1}));self.assertEqual(len(pages),3)
   self.assertEqual(pages[0]['DeleteMarkers'][0]['VersionId'],marker)
   self.assertEqual(c.get_object(Bucket='version-evidence',Key='incident-log.json',VersionId='null')['Body'].read(),b'original evidence')
   page.get_by_role('tab',name='Objects',exact=True).click();page.get_by_role('checkbox',name='Show versions',exact=True).check()
   expect(page.get_by_role('cell',name=updated,exact=True)).to_be_visible();expect(page.get_by_role('cell',name='Delete marker',exact=True)).to_be_visible();expect(page.get_by_role('row').nth(1)).to_contain_text('Delete marker')
   page.screenshot(path=str(REPORTS/'s3-versions-desktop.png'),full_page=True)
   assert_reflow(page)
   null_row=page.get_by_role('row').filter(has=page.get_by_role('cell',name='null',exact=True));null_row.get_by_role('link',name='incident-log.json',exact=True).click()
   with page.expect_download() as download:page.get_by_role('button',name='Download object',exact=True).click()
   self.assertEqual(Path(download.value.path()).read_bytes(),b'original evidence')
   delete_row=page.get_by_role('row').filter(has=page.get_by_role('cell',name='Delete marker',exact=True));delete_row.get_by_role('link',name='incident-log.json',exact=True).click();page.get_by_label('Type incident-log.json to confirm',exact=True).fill('incident-log.json');page.get_by_role('button',name='Delete permanently',exact=True).click();expect(page.get_by_role('cell',name='Delete marker',exact=True)).to_have_count(0)
   self.assertEqual(c.get_object(Bucket='version-evidence',Key='incident-log.json')['Body'].read(),b'updated evidence')
   page.set_viewport_size({'width':390,'height':844});page.screenshot(path=str(REPORTS/'s3-versions-mobile.png'),full_page=True);assert_reflow(page)
   self.assertEqual(other_client.list_buckets()['Buckets'],[])
   with self.assertRaises(ClientError) as error:other_client.get_object(Bucket='version-evidence',Key='incident-log.json',VersionId=updated)
   self.assertEqual(error.exception.response['Error']['Code'],'NoSuchBucket')
   def operator_call(path,body):
    return json.load(urllib.request.urlopen(urllib.request.Request(endpoint+path,data=json.dumps(body).encode(),method='POST' if path.endswith('/access') else 'PUT',headers={'X-Simulator-Token':token,'Content-Type':'application/json'})))
   operator_call('/_simulator/v1/iam/principals/version-reader/policy',{'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['s3:GetObjectVersion','s3:ListBucketVersions'],'Resource':['arn:aws:s3:::version-evidence','arn:aws:s3:::version-evidence/incident-log.json']}]})
   participant=operator_call('/_simulator/v1/access',{'Principal':'version-reader'})['AccessToken']
   reader=sdk(endpoint);reader.meta.events.register('before-sign.s3',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',participant))
   self.assertEqual(reader.get_object(Bucket='version-evidence',Key='incident-log.json',VersionId=updated)['Body'].read(),b'updated evidence')
   self.assertEqual(len(reader.list_object_versions(Bucket='version-evidence')['Versions']),2)
   with self.assertRaises(ClientError) as error:reader.get_object(Bucket='version-evidence',Key='incident-log.json')
   self.assertEqual(error.exception.response['ResponseMetadata']['HTTPStatusCode'],403)
   with self.assertRaises(ClientError) as error:reader.delete_object(Bucket='version-evidence',Key='incident-log.json',VersionId=updated)
   self.assertEqual(error.exception.response['ResponseMetadata']['HTTPStatusCode'],403)
   cross_reader=sdk(other);cross_reader.meta.events.register('before-sign.s3',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',participant))
   with self.assertRaises(ClientError) as error:cross_reader.list_object_versions(Bucket='version-evidence')
   self.assertEqual(error.exception.response['ResponseMetadata']['HTTPStatusCode'],401)
   c.put_bucket_versioning(Bucket='version-evidence',VersioningConfiguration={'Status':'Suspended'})
   self.assertEqual(c.put_object(Bucket='version-evidence',Key='incident-log.json',Body=b'null replacement')['VersionId'],'null')
   for v in c.list_object_versions(Bucket='version-evidence').get('Versions',[]):c.delete_object(Bucket='version-evidence',Key=v['Key'],VersionId=v['VersionId'])
   c.delete_bucket(Bucket='version-evidence');self.assertEqual(c.list_buckets()['Buckets'],[])
   self.assertEqual(errors,[]);self.assertEqual(outside,[]);browser.close()
 def test_cloudformation_s3_tags_console_and_atomic_cleanup(self):
  template=Path('internal/aws/testdata/s3-evidence.yaml').read_text()
  with running(authenticated=True) as (endpoint,token),running(authenticated=True) as (other,other_token),sync_playwright() as p:
   def client(service,url,access):
    c=boto3.client(service,endpoint_url=url,region_name='ap-northeast-1',aws_access_key_id='TCSIMEXAMPLE',aws_secret_access_key='simulator-only',config=Config(s3={'addressing_style':'path'},retries={'max_attempts':0},request_checksum_calculation='when_required',response_checksum_validation='when_required'))
    c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',access));return c
   c=client('s3',endpoint,token);f=client('cloudformation',endpoint,token);ssm_client=client('ssm',endpoint,token)
   fields={'StackName':'evidence-stack','TemplateBody':template,'Parameters':[{'ParameterKey':'Prefix','ParameterValue':'browser-evidence'}]}
   f.create_stack(**fields);other_cfn=client('cloudformation',other,other_token);other_cfn.create_stack(**fields)
   other_s3=client('s3',other,other_token)
   self.assertEqual(c.get_bucket_versioning(Bucket='browser-evidence')['Status'],'Enabled')
   self.assertEqual(c.get_bucket_tagging(Bucket='browser-evidence')['TagSet'],[{'Key':'TenkaCloud:NamePrefix','Value':'browser-evidence'}])
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
   browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page();errors=[];outside=[]
   page.on('pageerror',lambda e:errors.append(str(e)))
   def route(r):
    if r.request.url.startswith(endpoint+'/'):r.continue_()
    else:outside.append(r.request.url);r.abort()
   context.route('**/*',route)
   page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click();open_service(page,'S3')
   page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='ap-northeast-1',exact=True).click();page.get_by_role('link',name='browser-evidence',exact=True).click();page.get_by_role('tab',name='Properties',exact=True).click()
   expect(page.get_by_role('table',name='Bucket tags',exact=True)).to_contain_text('TenkaCloud:NamePrefix')
   page.get_by_role('button',name='Edit tags',exact=True).click();page.get_by_role('button',name='Add tag',exact=True).click();page.get_by_placeholder('Enter key',exact=True).last.fill('Environment');page.get_by_placeholder('Enter value',exact=True).last.fill('practice');held=[]
   def hold_tag_save(r):
    if r.request.method=='PUT':held.append(r)
    else:r.continue_()
   page.route(re.compile(re.escape(endpoint)+r'/browser-evidence\?tagging$'),hold_tag_save)
   with page.expect_request(lambda r:r.url==endpoint+'/browser-evidence?tagging' and r.method=='PUT'):page.get_by_role('button',name='Save tags',exact=True).click()
   expect(page.get_by_role('button',name='Edit',exact=True)).to_be_disabled();expect(page.get_by_text('Tags saved.',exact=True)).to_have_count(0)
   self.assertEqual(c.get_bucket_tagging(Bucket='browser-evidence')['TagSet'],[{'Key':'TenkaCloud:NamePrefix','Value':'browser-evidence'}]);self.assertEqual(len(held),1);held[0].continue_();expect(page.get_by_text('Tags saved.',exact=True)).to_be_visible()
   self.assertEqual({t['Key']:t['Value'] for t in c.get_bucket_tagging(Bucket='browser-evidence')['TagSet']},{'TenkaCloud:NamePrefix':'browser-evidence','Environment':'practice'})
   page.screenshot(path=str(REPORTS/'s3-tags-desktop.png'),full_page=True)
   c.put_bucket_tagging(Bucket='browser-evidence',Tagging={'TagSet':[{'Key':'Team','Value':'independent'}]})
   page.get_by_role('tab',name='Objects',exact=True).click();page.get_by_role('tab',name='Properties',exact=True).click();expect(page.get_by_role('table',name='Bucket tags',exact=True)).to_contain_text('independent');expect(page.get_by_role('table',name='Bucket tags',exact=True)).not_to_contain_text('TenkaCloud:NamePrefix')
   page.set_viewport_size({'width':390,'height':844});page.screenshot(path=str(REPORTS/'s3-tags-mobile.png'),full_page=True);assert_reflow(page)
   self.assertEqual(other_s3.get_bucket_tagging(Bucket='browser-evidence')['TagSet'],[{'Key':'TenkaCloud:NamePrefix','Value':'browser-evidence'}])
   c.put_object(Bucket='browser-evidence',Key='incident.json',Body=b'evidence');c.delete_object(Bucket='browser-evidence',Key='incident.json')
   with self.assertRaises(ClientError) as error:f.delete_stack(StackName='evidence-stack')
   self.assertEqual(error.exception.response['Error']['Code'],'ValidationError')
   self.assertEqual(ssm_client.get_parameter(Name='/cfn-evidence/config')['Parameter']['Value'],'preserve-me')
   self.assertEqual(f.describe_stacks(StackName='evidence-stack')['Stacks'][0]['Outputs'][0]['OutputValue'] is not None,True)
   versions=c.list_object_versions(Bucket='browser-evidence')
   for v in versions.get('Versions',[])+versions.get('DeleteMarkers',[]):c.delete_object(Bucket='browser-evidence',Key=v['Key'],VersionId=v['VersionId'])
   f.delete_stack(StackName='evidence-stack');other_cfn.delete_stack(StackName='evidence-stack')
   with self.assertRaises(ClientError) as error:c.get_bucket_tagging(Bucket='browser-evidence')
   self.assertEqual(error.exception.response['Error']['Code'],'NoSuchBucket')
   self.assertEqual(errors,[]);self.assertEqual(outside,[]);browser.close()
 def test_cloudformation_inline_lambda_native_execution_and_isolation(self):
  template=Path('internal/aws/testdata/lambda-inline.yaml').read_text()
  def client(service,url,token):
   c=boto3.client(service,endpoint_url=url,region_name='ap-northeast-1',aws_access_key_id='TCSIMEXAMPLE',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
   c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return c
  with tempfile.TemporaryDirectory() as d:
   state=Path(d)/'team.db'
   with running(state,authenticated=True) as (endpoint,token),running(authenticated=True) as (other,other_token):
    f=client('cloudformation',endpoint,token);c=client('lambda',endpoint,token);of=client('cloudformation',other,other_token);oc=client('lambda',other,other_token)
    for cf,value in ((f,'team-one'),(of,'team-two')):cf.create_stack(StackName='inline-practice',TemplateBody=template,Parameters=[{'ParameterKey':'Value','ParameterValue':value}],Capabilities=['CAPABILITY_IAM'])
    self.assertEqual(c.get_function_configuration(FunctionName='cfn-inline-app')['Runtime'],'python3.12')
    for lc,value in ((c,'team-one'),(oc,'team-two')):
     result=lc.invoke(FunctionName='cfn-inline-app',Payload=b'{"value":7}');self.assertNotIn('FunctionError',result);self.assertEqual(json.loads(result['Payload'].read()),{'answer':42,'value':value,'event':{'value':7}})
    with sync_playwright() as p:
     executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium');browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page();outside=[];errors=[]
     page.on('pageerror',lambda e:errors.append(str(e)))
     def route(r):
      if r.request.url.startswith(endpoint+'/'):r.continue_()
      else:outside.append(r.request.url);r.abort()
     context.route('**/*',route)
     page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click();page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='ap-northeast-1',exact=True).click();page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Lambda',exact=True).click();page.get_by_role('link',name='cfn-inline-app',exact=True).click();page.get_by_label('Event JSON',exact=True).fill('{"value":7}');page.get_by_role('button',name='Test',exact=True).click();expect(page.get_by_text('Execution succeeded',exact=True)).to_be_visible(timeout=20000)
     self.assertEqual(json.loads(page.get_by_label('Response',exact=True).input_value()),{'answer':42,'value':'team-one','event':{'value':7}});page.screenshot(path=str(REPORTS/'lambda-cfn-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});page.screenshot(path=str(REPORTS/'lambda-cfn-mobile.png'),full_page=True);assert_reflow(page);self.assertEqual(errors,[]);self.assertEqual(outside,[]);browser.close()
    with self.assertRaises(ClientError) as error:client('lambda',other,token).get_function_configuration(FunctionName='cfn-inline-app')
    self.assertEqual(error.exception.response['ResponseMetadata']['HTTPStatusCode'],401)
    of.delete_stack(StackName='inline-practice');self.assertEqual(oc.list_functions()['Functions'],[]);self.assertEqual(len(c.list_functions()['Functions']),1)
   with running(state,authenticated=True) as (endpoint,token):
    f=client('cloudformation',endpoint,token);c=client('lambda',endpoint,token)
    result=c.invoke(FunctionName='cfn-inline-app',Payload=b'null');self.assertNotIn('FunctionError',result);self.assertEqual(json.loads(result['Payload'].read()),{'answer':42,'value':'team-one','event':None})
    f.delete_stack(StackName='inline-practice');self.assertEqual(c.list_functions()['Functions'],[])
 def test_console_participant_sts_handoff(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('console_handoff.py'))],check=True,timeout=180)

 def test_console_account_sign_out_revokes_browser_session_only(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('console_logout.py'))],check=True)

 def test_openapi_parameter_detector_controls(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('test_openapi_parameters.py'))],check=True)
 def test_openapi_json10_shared_schema_controls(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('test_openapi_json10.py'))],check=True,timeout=30)
 def test_scalar_s3_docs_live_response_reader(self):
  from scalar_s3_docs import run_scalar_s3_docs
  run_scalar_s3_docs()
 def test_kms_native_sdk_served_examples_two_teams_and_restart(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('kms.py'))],check=True)
 def test_kms_console_actual_native_state_identity_and_logout(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('kms_console.py'))],check=True)
 def test_kms_aliases_native_sdk_scopes_crypto_and_restart(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('kms_aliases.py'))],check=True)
 def test_kms_aliases_console_actual_native_lifecycle(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('kms_aliases_console.py'))],check=True)
 def test_kms_aliases_served_examples_both_native_routes(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('kms_alias_docs.py'))],check=True)
 def test_kms_japanese_uses_canonical_crypto_operations(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('kms_locale.py'))],check=True,timeout=120)
 def test_service_japanese_uses_canonical_native_operations(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('service_locale.py'))],check=True,timeout=180)
 def test_official_storage_sdk_harness_boundaries(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('test_crossprovider_official_storage.py'))],check=True,timeout=30)
 def test_official_storage_sdks_two_teams_restart_and_conditions(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('crossprovider_official_storage.py')),'--binary',str(BINARY),'--provider','both'],check=True,timeout=300)
 def test_cloudformation_local_ssm_parameter_snapshot(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('cloudformation_ssm_parameters.py'))],check=True,timeout=120)
 def test_cloudformation_rest_native_sdk_lifecycle(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('cloudformation_rest.py'))],check=True)
 def test_sqs_native_sdk_two_teams_restart_retention_and_cleanup(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('sqs.py'))],check=True)
 def test_apigateway_rest_sdk_permissions_and_two_teams(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('apigateway.py'))],check=True)
 def test_apigateway_console_and_actual_scalar_requests(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('apigateway_console.py'))],check=True)
 def test_wafv2_native_console_concurrency_and_state(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('wafv2_console.py'))],check=True,timeout=180)
 def test_iam_managed_console_native_policy_versions_and_attachments(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('iam_managed_console.py'))],check=True,timeout=240)
 def test_iam_managed_served_reference_templates_execute(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('iam_managed_reference.py'))],check=True,timeout=180)
 def test_api_reference_cloud_sections_and_live_native_results(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('api_cloud_sections_contract.py')),'--self-test'],check=True)
  subprocess.run([sys.executable,str(Path(__file__).with_name('api_cloud_sections.py'))],check=True,timeout=180)
 def test_iam_managed_policy_reaches_saved_lambda_role_sdk(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('iam_managed_lambda.py'))],check=True,timeout=180)
 def test_iam_managed_policy_sdk_isolation_restart_and_cleanup(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('iam_managed.py'))],check=True)
 def test_native_iam_query_and_console_to_lambda(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('native_iam.py'))],check=True)
 def test_iam_role_console_to_lambda_deploy_without_host_setup(self):
  with running(authenticated=True) as (endpoint,token),running(authenticated=True) as (other,other_token),sync_playwright() as p:
   browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox']);page=browser.new_page(viewport={'width':1440,'height':1000});errors=[];outside=[]
   page.on('pageerror',lambda e:errors.append(str(e)))
   def route(r):
    if r.request.url.startswith(endpoint+'/'):r.continue_()
    else:outside.append(r.request.url);r.abort()
   page.route('**/*',route)
   page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click()
   page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Lambda',exact=True).click();page.get_by_role('button',name='Create function',exact=True).first.click();expect(page.get_by_text('No local execution roles exist.',exact=False)).to_be_visible()
   page.get_by_role('button',name='Create role in IAM',exact=True).click();page.get_by_role('button',name='Local identities',exact=True).click();expect(page.get_by_role('heading',name='No roles yet',exact=True)).to_be_visible();page.get_by_role('button',name='Create role',exact=True).first.click()
   page.get_by_label('Role name',exact=True).fill('console-executor');page.get_by_role('button',name='Create role',exact=True).last.click();expect(page.get_by_role('heading',name='console-executor',exact=True)).to_be_visible();expect(page.get_by_text('arn:aws:iam::000000000000:role/console-executor',exact=True)).to_be_visible()
   expect(page.get_by_role('button',name='Global',exact=True)).to_be_visible();page.screenshot(path=str(REPORTS/'iam-role-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});assert_reflow(page);page.screenshot(path=str(REPORTS/'iam-role-mobile.png'),full_page=True);page.set_viewport_size({'width':1440,'height':1000})
   page.get_by_role('button',name='Roles',exact=True).click();page.get_by_role('link',name='console-executor',exact=True).click();expect(page.get_by_role('heading',name='console-executor',exact=True)).to_be_visible();page.get_by_role('button',name='Edit permissions',exact=True).click()
   document=json.loads(page.get_by_label('Permissions policy JSON',exact=True).input_value());document['Statement'][0]['Sid']='Reviewed';page.get_by_label('Permissions policy JSON',exact=True).fill(json.dumps(document));page.get_by_role('button',name='Save changes',exact=True).click();expect(page.get_by_role('heading',name='console-executor',exact=True)).to_be_visible()
   saved=page.request.get(endpoint+'/_simulator/v1/iam/principals/console-executor/policy',headers={'X-Simulator-Token':token});self.assertEqual(saved.json()['Statement'][0]['Sid'],'Reviewed')
   page.get_by_role('button',name='Roles',exact=True).click();pending=[];pattern='**/_simulator/v1/iam/principals/console-executor/policy';page.route(pattern,lambda r:pending.append(r) if r.request.method=='GET' else r.continue_());page.get_by_role('link',name='console-executor',exact=True).click();page.get_by_role('button',name='Create role',exact=True).first.click()
   with page.expect_response(lambda r:r.url.endswith('/console-executor/policy')) as response:pending[0].continue_()
   self.assertEqual(response.value.json()['Statement'][0]['Sid'],'Reviewed');page.evaluate('()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))');self.assertNotIn('Reviewed',page.get_by_label('Permissions policy JSON',exact=True).input_value());page.unroute(pattern)
   page.get_by_label('Role name',exact=True).fill('console-executor');page.get_by_role('button',name='Create role',exact=True).last.click();expect(page.get_by_text('412: An identity with this name already exists.',exact=True)).to_be_visible();page.get_by_role('button',name='Cancel',exact=True).click();page.get_by_role('link',name='console-executor',exact=True).click();expect(page.get_by_label('Permissions policy JSON',exact=True)).to_have_value(re.compile('Reviewed'))
   page.get_by_role('button',name='Create a Lambda function',exact=True).click();page.get_by_role('button',name='Create function',exact=True).first.click();page.get_by_label('Function name',exact=True).fill('console-iam-function');page.get_by_label('Existing role',exact=True).click();page.get_by_role('option',name='console-executor',exact=True).click();self.assertEqual(page.get_by_label('Execution role ARN',exact=True).input_value(),'arn:aws:iam::000000000000:role/console-executor');page.get_by_role('button',name='Create function',exact=True).last.click();expect(page.get_by_role('heading',name='console-iam-function',exact=True)).to_be_visible();page.get_by_role('button',name='Test',exact=True).click();expect(page.get_by_text('Execution succeeded',exact=True)).to_be_visible(timeout=20000)
   page.get_by_role('tab',name='Code',exact=True).click()
   data=io.BytesIO()
   with zipfile.ZipFile(data,'w') as z:z.writestr('lambda_function.py','def lambda_handler(event, context): return {"deployed": event["value"] * 6}')
   page.get_by_label('Function ZIP',exact=True).set_input_files({'name':'function.zip','mimeType':'application/zip','buffer':data.getvalue()});page.get_by_role('button',name='Deploy',exact=True).click();expect(page.get_by_text('Function code deployed.',exact=True)).to_be_visible();page.get_by_role('tab',name='Test',exact=True).click();page.get_by_role('button',name='Test',exact=True).click();expect(page.get_by_text('Execution succeeded',exact=True)).to_be_visible(timeout=20000);self.assertEqual(json.loads(page.get_by_label('Response',exact=True).input_value()),{'deployed':42});page.screenshot(path=str(REPORTS/'iam-lambda-deployed.png'),full_page=True)
   remote=urllib.request.Request(other+'/_simulator/v1/iam/principals',headers={'X-Simulator-Token':other_token})
   with urllib.request.urlopen(remote) as r:self.assertEqual(json.load(r)['principals'],['simulator-admin'])
   access=page.request.post(endpoint+'/_simulator/v1/access',data={'Principal':'console-executor'},headers={'X-Simulator-Token':token});self.assertEqual(access.status,200);participant=access.json()['AccessToken']
   self.assertEqual(page.request.get(endpoint+'/_simulator/v1/iam/principals',headers={'X-Simulator-Token':participant}).status,403)
   self.assertEqual(page.request.put(endpoint+'/_simulator/v1/iam/principals/unauthorized/policy',data={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'*','Resource':'*'}]},headers={'X-Simulator-Token':participant}).status,403)
   self.assertEqual(errors,[]);self.assertEqual(outside,[]);browser.close()
 def test_console_and_api_share_state_offline(self):
  with running() as endpoint,sync_playwright() as p:
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
   browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox'])
   context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page();page.add_init_script("window.__policyViolations=[];document.addEventListener('securitypolicyviolation',e=>window.__policyViolations.push(e.blockedURI));");errors=[];outside=[]
   page.on('pageerror',lambda e:errors.append(str(e)))
   def route(request_route):
    if request_route.request.url.startswith(endpoint+'/'):request_route.continue_()
    else:outside.append(request_route.request.url);request_route.abort()
   context.route('**/*',route)
   page.goto(endpoint+'/_simulator/console/#/s3');expect(page.get_by_role('heading',name='No buckets yet',exact=True)).to_be_visible()
   page.get_by_role('button',name='Create bucket',exact=True).first.click();page.get_by_label('Bucket name',exact=True).fill('ui-practice');submit(page);expect(page.get_by_role('link',name='ui-practice',exact=True)).to_be_visible()
   c=sdk(endpoint);self.assertEqual(c.list_buckets()['Buckets'][0]['Name'],'ui-practice')
   c.create_bucket(Bucket='sdk-created');page.get_by_role('button',name='Refresh resources').click();expect(page.get_by_role('link',name='sdk-created',exact=True)).to_be_visible()
   page.get_by_role('link',name='ui-practice',exact=True).click();expect(page.get_by_role('heading',name='This bucket is empty')).to_be_visible()
   page.get_by_role('button',name='Upload',exact=True).first.click();page.get_by_label('Choose a file').set_input_files({'name':'hello.txt','mimeType':'text/plain','buffer':b'hello from UI'});page.get_by_label('Object key',exact=True).fill('docs/hello.txt');submit(page);expect(page.get_by_role('link',name='docs/hello.txt',exact=True)).to_be_visible();self.assertEqual(c.get_object(Bucket='ui-practice',Key='docs/hello.txt')['Body'].read(),b'hello from UI')
   c.put_object(Bucket='ui-practice',Key='from-sdk.txt',Body=b'shared');page.get_by_role('button',name='Refresh resources',exact=True).click();expect(page.get_by_role('link',name='from-sdk.txt',exact=True)).to_be_visible()
   page.screenshot(path=str(REPORTS/'console-objects-desktop.png'),full_page=True)
   page.get_by_role('link',name='docs/hello.txt',exact=True).click()
   with page.expect_download() as download:submit(page)
   self.assertEqual(Path(download.value.path()).read_bytes(),b'hello from UI')
   page.get_by_role('link',name='General purpose buckets',exact=True).click();page.get_by_role('radio',name='Select ui-practice',exact=True).check();page.get_by_role('button',name='Delete',exact=True).click();page.get_by_label('Type ui-practice to confirm').fill('ui-practice');submit(page);expect(page.get_by_role('dialog')).to_contain_text('BucketNotEmpty');page.get_by_role('button',name='Cancel',exact=True).click()
   page.get_by_role('link',name='Identity policies',exact=True).click();page.get_by_label('Principal name',exact=True).fill('reader');submit(page)
   page.get_by_role('button',name='Account menu',exact=True).click();page.get_by_role('menuitem',name='Choose identity',exact=True).click();page.get_by_label('Principal name',exact=True).fill('reader');submit(page);expect(page.get_by_role('button',name='Account menu',exact=True)).to_contain_text('reader')
   buckets_before=sorted(b['Name'] for b in c.list_buckets()['Buckets'])
   page.get_by_role('button',name='Create bucket',exact=True).first.click();page.get_by_label('Bucket name',exact=True).fill('denied-bucket')
   with page.expect_response(lambda response:response.request.method=='PUT' and response.url==endpoint+'/denied-bucket') as denied_create:submit(page)
   self.assertEqual(denied_create.value.status,403);self.assertEqual(denied_create.value.request.headers.get('x-simulator-principal'),'reader');expect(page.locator('[id^=form-error-]')).to_contain_text('implicitDeny');page.get_by_role('button',name='Cancel',exact=True).click()
   self.assertNotIn('denied-bucket',[b['Name'] for b in c.list_buckets()['Buckets']]);self.assertEqual(sorted(b['Name'] for b in c.list_buckets()['Buckets']),buckets_before);self.assertEqual(c.get_object(Bucket='ui-practice',Key='docs/hello.txt')['Body'].read(),b'hello from UI');self.assertEqual(c.get_object(Bucket='ui-practice',Key='from-sdk.txt')['Body'].read(),b'shared')
   page.get_by_role('searchbox',name='Find resources',exact=True).fill('no-matches');expect(page.get_by_role('heading',name='No matching resources')).to_be_visible();page.get_by_role('searchbox',name='Find resources',exact=True).fill('')
   page.set_viewport_size({'width':390,'height':844});expect(page.get_by_role('button',name='Create bucket',exact=True).first).to_be_visible();page.screenshot(path=str(REPORTS/'console-mobile.png'),full_page=True);assert_reflow(page)
   self.assertEqual(page.evaluate('window.__policyViolations'),[]);page.set_viewport_size({'width':1440,'height':1000});page.goto(endpoint+'/_simulator/docs/');expect(page.get_by_role('heading',name='TenkaCloud Simulator API',exact=True)).to_be_visible(timeout=15000)
   expect(page.get_by_role('button',name='Ask AI',exact=True)).to_have_count(0)
   # Cloud groups now start at Introduction. Expand the actual AWS/S3 sidebar
   # before selecting the operation; do not replace Scalar Send with a fetch.
   def select_s3_operation(title):
    sidebar=page.get_by_role('complementary',name='Sidebar for TenkaCloud Simulator API',exact=True)
    for group_name in ['AWS','S3 basic object storage']:
     group=sidebar.get_by_role('button',name=re.compile(r'^(Open|Close) Group - '+re.escape(group_name)+r'$'))
     expect(group).to_be_visible()
     if group.get_attribute('aria-expanded')=='false':group.click()
    sidebar.get_by_role('link',name=re.compile('^'+re.escape(title))).click()
   select_s3_operation('List all buckets')
   page.get_by_role('button',name='Test Request (get /)',exact=True).click()
   # Use the request client Send button, not a parallel fetch injected by the test.
   send=page.get_by_role('button',name=re.compile('^Send.*get request'))
   expect(send).to_be_visible()
   with page.expect_response(lambda response:response.request.method=='GET' and response.url.rstrip('/')==endpoint) as listed:
    send.click()
   self.assertEqual(listed.value.status,200);self.assertIn('ListAllMyBucketsResult',listed.value.text());self.assertIn('ui-practice',listed.value.text())
   expect(page.get_by_text('ui-practice',exact=False).last).to_be_visible(timeout=10000)
   page.get_by_role('button',name='Close Client',exact=True).click()
   c.create_bucket(Bucket='practice-bucket')
   select_s3_operation('Upload or replace an object')
   page.get_by_role('button',name='Test Request (put /{bucket}/{key})',exact=True).click()
   put_send=page.get_by_role('button',name=re.compile('^Send.*put request'))
   expect(put_send).to_be_visible()
   with page.expect_response(lambda response:response.request.method=='PUT' and response.url==endpoint+'/practice-bucket/hello.txt') as uploaded:
    put_send.click()
   self.assertEqual(uploaded.value.status,200)
   # The prefilled OpenAPI text example goes through the actual Scalar editor/client.
   deadline=time.monotonic()+8
   while True:
    try:
     body=c.get_object(Bucket='practice-bucket',Key='hello.txt')['Body'].read();break
    except ClientError:
     if time.monotonic()>deadline:raise
     time.sleep(.1)
   self.assertEqual(body,b'Hello from the API reference.')
   page.screenshot(path=str(REPORTS/'api-reference-put.png'),full_page=False,animations='disabled')
   page.get_by_role('button',name='Close Client',exact=True).click()
   select_s3_operation('List all buckets')
   page.get_by_role('button',name='Test Request (get /)',exact=True).click()
   send=page.get_by_role('button',name=re.compile('^Send.*get request'))
   with page.expect_response(lambda response:response.request.method=='GET' and response.url.rstrip('/')==endpoint) as listed:
    send.click()
   self.assertEqual(listed.value.status,200);self.assertIn('ui-practice',listed.value.text());self.assertIn('practice-bucket',listed.value.text())
   expect(page.get_by_text('ui-practice',exact=False).last).to_be_visible(timeout=10000)
   page.screenshot(path=str(REPORTS/'api-reference.png'),full_page=False,animations='disabled')
   self.assertEqual(page.evaluate('window.__policyViolations'),[]);self.assertEqual(errors,[]);self.assertEqual(outside,[],'Runtime attempted external network requests');browser.close()
 def test_console_literal_dot_keys_do_not_mutate_neighbors(self):
  with running() as endpoint,sync_playwright() as p:
   c=sdk(endpoint);c.create_bucket(Bucket='dot-practice');c.create_bucket(Bucket='other-bucket')
   c.put_object(Bucket='dot-practice',Key='victim',Body=b'local sentinel')
   c.put_object(Bucket='other-bucket',Key='victim',Body=b'other sentinel')
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
   browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox'])
   page=browser.new_page();page.goto(endpoint+'/_simulator/console/#/s3')
   page.get_by_role('link',name='dot-practice',exact=True).click()
   for key in ['a/../victim','a/./victim','../other-bucket/victim','a//victim']:
    c.put_object(Bucket='dot-practice',Key=key,Body=b'original literal bytes');page.get_by_role('button',name='Refresh resources',exact=True).click()
    page.get_by_role('link',name=key,exact=True).click()
    with page.expect_download() as download:submit(page)
    self.assertEqual(Path(download.value.path()).read_bytes(),b'original literal bytes')
    page.get_by_role('button',name='Upload',exact=True).first.click();page.get_by_label('Choose a file').set_input_files({'name':'replacement.txt','mimeType':'text/plain','buffer':b'replaced literal bytes'});page.get_by_label('Object key',exact=True).fill(key);submit(page);expect(page.get_by_role('heading',name='Files and folders',exact=True)).not_to_be_visible();expect(page.get_by_role('dialog')).not_to_be_visible()
    self.assertEqual(c.get_object(Bucket='dot-practice',Key=key)['Body'].read(),b'replaced literal bytes')
    page.get_by_role('radio',name='Select '+key,exact=True).check();page.get_by_role('button',name='Delete',exact=True).click();page.get_by_label('Type '+key+' to confirm',exact=True).fill(key);submit(page);expect(page.get_by_role('heading',name='Files and folders',exact=True)).not_to_be_visible();expect(page.get_by_role('dialog')).not_to_be_visible()
    with self.assertRaises(ClientError):c.get_object(Bucket='dot-practice',Key=key)
    self.assertEqual(c.get_object(Bucket='dot-practice',Key='victim')['Body'].read(),b'local sentinel')
    self.assertEqual(c.get_object(Bucket='other-bucket',Key='victim')['Body'].read(),b'other sentinel')
   for key in ['.','..']:
    c.put_object(Bucket='dot-practice',Key=key,Body=b'bare dot sentinel');page.get_by_role('button',name='Refresh resources',exact=True).click()
    page.get_by_role('link',name=key,exact=True).click();submit(page);expect(page.get_by_role('dialog')).to_contain_text('Use an S3 SDK');page.get_by_role('button',name='Cancel',exact=True).click()
    page.get_by_role('radio',name='Select '+key,exact=True).check();page.get_by_role('button',name='Delete',exact=True).click();page.get_by_label('Type '+key+' to confirm',exact=True).fill(key);submit(page);expect(page.locator('[id^=form-error-]')).to_contain_text('Use an S3 SDK');page.get_by_role('button',name='Cancel',exact=True).click()
    page.get_by_role('button',name='Upload',exact=True).first.click();page.get_by_label('Choose a file').set_input_files({'name':'dot.txt','mimeType':'text/plain','buffer':b'bad replacement'});page.get_by_label('Object key',exact=True).fill(key);submit(page);expect(page.locator('[id^=form-error-]')).to_contain_text('Use an S3 SDK');page.get_by_role('button',name='Cancel',exact=True).click()
    self.assertEqual(c.get_object(Bucket='dot-practice',Key=key)['Body'].read(),b'bare dot sentinel')
   browser.close()
 def test_actual_catalog_parameter_console_authenticated(self):
  import secrets
  with running(authenticated=True) as (endpoint,operator),sync_playwright() as p:
   def client(service,token):
    c=boto3.client(service,endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
    c.meta.events.register('before-sign.'+service,lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return c
   cfn=client('cloudformation',operator);seed=secrets.token_hex(16)
   cfn.create_stack(StackName='tc-hello-world-browser',TemplateBody=Path('test/fixtures/hello-world.yaml').read_text(),Parameters=[{'ParameterKey':key,'ParameterValue':value} for key,value in {'NamePrefix':'tc-hello-world-browser','TenkaCloudAccountId':'000000000000','ExternalId':'local-browser-job-2026','FlagSeed':seed}.items()],Capabilities=['CAPABILITY_NAMED_IAM'])
   outputs={item['OutputKey']:item['OutputValue'] for item in cfn.describe_stacks(StackName='tc-hello-world-browser')['Stacks'][0]['Outputs']}
   request=urllib.request.Request(endpoint+'/_simulator/v1/access',data=json.dumps({'Principal':outputs['ParticipantViewerRoleArn'].split('/')[-1]}).encode(),headers={'Content-Type':'application/json','X-Simulator-Token':operator})
   with urllib.request.urlopen(request) as response:token=json.load(response)['AccessToken']
   with self.assertRaises(ClientError) as denied:client('cloudformation',token).describe_stacks()
   self.assertEqual(denied.exception.response['ResponseMetadata']['HTTPStatusCode'],403)
   browser=p.chromium.launch(**({'executable_path':os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')} if os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium') else {}),args=['--no-sandbox'])
   page=browser.new_page(viewport={'width':1440,'height':1000});errors=[];outside=[]
   page.on('pageerror',lambda e:errors.append(str(e)))
   def route(r):
    if r.request.url.startswith(endpoint+'/'):r.continue_()
    else:outside.append(r.request.url);r.abort()
   page.route('**/*',route)
   page.goto(endpoint+'/_simulator/console/');expect(page.get_by_role('heading',name='Open your local sandbox',exact=True)).to_be_visible();page.get_by_label('Access token',exact=True).fill(token);page.get_by_role('button',name='Open sandbox',exact=True).click()
   page.get_by_role('button',name='Account menu',exact=True).click();expect(page.get_by_role('menuitem',name='Sign out',exact=True)).to_be_visible();expect(page.get_by_role('menuitem',name='Choose identity',exact=True)).to_have_count(0);page.keyboard.press('Escape')
   page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Systems Manager',exact=True).click();page.get_by_role('link',name=outputs['ParameterName'],exact=True).click();expect(page.get_by_label('Value',exact=True)).to_have_value('TC{'+seed+'}');expect(page.get_by_role('tab',name='Overview',exact=True)).to_be_visible()
   page.get_by_role('button',name='Edit parameter',exact=True).click();expect(page.get_by_role('button',name='Type String',exact=True)).to_be_disabled();page.get_by_label('Value',exact=True).fill('unauthorized change');page.get_by_role('button',name='Save changes',exact=True).click();expect(page.locator('[id^=form-error-]')).to_contain_text('403');self.assertEqual(client('ssm',token).get_parameter(Name=outputs['ParameterName'])['Parameter']['Value'],'TC{'+seed+'}')
   page.get_by_role('button',name='Cancel',exact=True).click();page.set_viewport_size({'width':390,'height':844});assert_reflow(page);self.assertEqual(errors,[]);self.assertEqual(outside,[])
   browser.close();cfn.delete_stack(StackName='tc-hello-world-browser')
 def test_region_change_clears_bucket_and_writes_only_new_region(self):
  with running() as endpoint,sync_playwright() as p:
   c=sdk(endpoint);c.create_bucket(Bucket='east-region-bucket');c.create_bucket(Bucket='tokyo-region-bucket',CreateBucketConfiguration={'LocationConstraint':'ap-northeast-1'});c.put_object(Bucket='east-region-bucket',Key='sentinel',Body=b'east unchanged')
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
   browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);page=browser.new_page(viewport={'width':1440,'height':1000});page.goto(endpoint+'/_simulator/console/#/s3');page.get_by_role('link',name='east-region-bucket',exact=True).click();expect(page.get_by_role('link',name='sentinel',exact=True)).to_be_visible()
   page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='ap-northeast-1',exact=True).click();expect(page.get_by_role('heading',name='General purpose buckets',exact=False)).to_be_visible();expect(page.get_by_role('link',name='east-region-bucket',exact=True)).to_have_count(0);page.get_by_role('link',name='tokyo-region-bucket',exact=True).click()
   page.get_by_role('button',name='Upload',exact=True).first.click();page.get_by_label('Choose a file').set_input_files({'name':'new-region.txt','mimeType':'text/plain','buffer':b'tokyo only'});submit(page);expect(page.get_by_role('link',name='new-region.txt',exact=True)).to_be_visible()
   self.assertEqual(c.get_object(Bucket='tokyo-region-bucket',Key='new-region.txt')['Body'].read(),b'tokyo only');self.assertEqual(c.get_object(Bucket='east-region-bucket',Key='sentinel')['Body'].read(),b'east unchanged')
   with self.assertRaises(ClientError):c.get_object(Bucket='east-region-bucket',Key='new-region.txt')
   browser.close()
 def test_ssm_detail_history_refresh_save_and_delete(self):
  with running() as endpoint,sync_playwright() as p:
   client=boto3.client('ssm',endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}))
   name='/navigation/lifecycle';client.put_parameter(Name=name,Type='String',Value='initial',Description='preserve existing description')
   browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium'),args=['--no-sandbox']);page=browser.new_page(viewport={'width':1440,'height':1000});errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
   page.goto(endpoint+'/_simulator/console/#/parameters?region=us-east-1');page.get_by_role('link',name=name,exact=True).click();expect(page.get_by_label('Value',exact=True)).to_have_value('initial')
   page.get_by_role('button',name='Edit parameter',exact=True).click();expect(page.get_by_label('Description',exact=True)).to_have_value('preserve existing description');page.get_by_role('button',name='Cancel',exact=True).click()
   client.put_parameter(Name=name,Type='String',Value='sdk-updated',Overwrite=True)
   page.get_by_role('button',name='Refresh parameter',exact=True).click();expect(page.get_by_label('Value',exact=True)).to_have_value('sdk-updated')
   page.get_by_role('button',name='Edit parameter',exact=True).click();page.get_by_label('Value',exact=True).fill('console-updated');page.get_by_role('button',name='Save changes',exact=True).click()
   expect(page.locator('[data-analytics-alert="success"]').get_by_text('Parameter saved.',exact=True)).to_be_visible();expect(page.get_by_label('Value',exact=True)).to_have_value('console-updated');self.assertEqual(client.get_parameter(Name=name)['Parameter']['Value'],'console-updated');self.assertEqual(client.describe_parameters()['Parameters'][0]['Description'],'preserve existing description')
   page.reload();expect(page.get_by_label('Value',exact=True)).to_have_value('console-updated')
   page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='ap-northeast-1',exact=True).click();expect(page.get_by_label('Value',exact=True)).to_have_count(0);expect(page.get_by_role('heading',name='No parameters yet',exact=True)).to_be_visible();self.assertIn('region=ap-northeast-1',page.url);self.assertNotIn('parameter=',page.url)
   page.goto(endpoint+'/_simulator/console/#/parameters?region=us-east-1');page.get_by_role('link',name=name,exact=True).click();expect(page.get_by_label('Value',exact=True)).to_have_value('console-updated')
   page.get_by_role('button',name='Delete parameter',exact=True).click();page.get_by_label('Type '+name+' to confirm',exact=True).fill(name);page.get_by_role('button',name='Delete permanently',exact=True).click()
   expect(page.locator('[data-analytics-alert="success"]').get_by_text('Resource deleted.',exact=True)).to_be_visible();expect(page.get_by_role('heading',name='No parameters yet',exact=True)).to_be_visible();self.assertNotIn('parameter=',page.url)
   with self.assertRaises(ClientError):client.get_parameter(Name=name)
   self.assertEqual(errors,[]);browser.close()
 def test_service_navigation_dismisses_resource_forms(self):
  with running() as endpoint,sync_playwright() as p:
   c=sdk(endpoint);c.create_bucket(Bucket='navigation-bucket');c.put_object(Bucket='navigation-bucket',Key='sentinel',Body=b'unchanged')
   ssm=boto3.client('ssm',endpoint_url=endpoint,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}));ssm.put_parameter(Name='/navigation/existing',Type='String',Value='unchanged')
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium');browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);page=browser.new_page(viewport={'width':1440,'height':1000});errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
   for route in ('menu','hash'):
    for kind in ('create-bucket','upload','create-parameter','edit-parameter'):
     with self.subTest(route=route,form=kind):
      source='s3' if kind in ('create-bucket','upload') else 'ssm';destination='ssm' if source=='s3' else 's3'
      page.goto('about:blank');page.goto(endpoint+'/_simulator/console/#/'+source)
      if kind=='create-bucket':page.get_by_role('button',name='Create bucket',exact=True).first.click();page.get_by_label('Bucket name',exact=True).fill('must-not-be-created')
      elif kind=='upload':
       page.get_by_role('link',name='navigation-bucket',exact=True).click();page.get_by_role('button',name='Upload',exact=True).first.click();page.get_by_label('Choose a file').set_input_files({'name':'must-not-upload.txt','mimeType':'text/plain','buffer':b'wrong destination'})
      else:
       if kind=='edit-parameter':page.get_by_role('link',name='/navigation/existing',exact=True).click();page.get_by_role('button',name='Edit parameter',exact=True).click()
       else:page.get_by_role('button',name='Create parameter',exact=True).first.click();page.get_by_label('Name',exact=True).fill('/navigation/must-not-create')
       page.get_by_label('Value',exact=True).fill('must not save')
      if route=='menu':page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Systems Manager' if destination=='ssm' else 'S3',exact=True).click()
      else:page.evaluate('(hash)=>{location.hash=hash}', '/parameters' if destination=='ssm' else '/s3')
      expect(page.get_by_role('heading',name='My parameters' if destination=='ssm' else 'General purpose buckets',exact=False)).to_be_visible();expect(page.get_by_label('Bucket name',exact=True)).to_have_count(0);expect(page.get_by_label('Object key',exact=True)).to_have_count(0);expect(page.get_by_label('Value',exact=True)).to_have_count(0)
      if destination=='ssm':
       page.get_by_role('button',name='Create parameter',exact=True).first.click();expect(page.get_by_label('Name',exact=True)).to_have_value('');expect(page.get_by_label('Value',exact=True)).to_have_value('');name='/navigation/'+route+'-'+kind;page.get_by_label('Name',exact=True).fill(name);page.get_by_label('Value',exact=True).fill('correct destination');submit(page);expect(page.get_by_role('link',name=name,exact=True)).to_be_visible();self.assertEqual(ssm.get_parameter(Name=name)['Parameter']['Value'],'correct destination')
      self.assertEqual([b['Name'] for b in c.list_buckets()['Buckets']],['navigation-bucket']);self.assertEqual([o['Key'] for o in c.list_objects_v2(Bucket='navigation-bucket')['Contents']],['sentinel']);self.assertEqual(c.get_object(Bucket='navigation-bucket',Key='sentinel')['Body'].read(),b'unchanged');self.assertEqual(ssm.get_parameter(Name='/navigation/existing')['Parameter']['Value'],'unchanged')
      with self.assertRaises(ClientError):ssm.get_parameter(Name='/navigation/must-not-create')
   self.assertEqual(errors,[]);browser.close()
 def test_lambda_sdk_console_and_isolated_python_without_host_runtime(self):
  with running(authenticated=True) as (endpoint,operator),running(authenticated=True) as (other_endpoint,other_operator),sync_playwright() as p:
   def client(url,token):
    c=boto3.client('lambda',endpoint_url=url,region_name='us-east-1',aws_access_key_id='TCSIMLOCAL',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0}));c.meta.events.register('before-sign.lambda',lambda request,**kwargs:request.headers.add_header('X-Simulator-Token',token));return c
   def control(url,token,path,data):
    request=urllib.request.Request(url+path,data=json.dumps(data).encode(),headers={'Content-Type':'application/json','X-Simulator-Token':token},method='POST' if path.endswith('/access') else 'PUT')
    with urllib.request.urlopen(request) as response:return json.load(response)
   policy={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'ssm:GetParameter','Resource':'*'}]}
   for url,token in ((endpoint,operator),(other_endpoint,other_operator)):control(url,token,'/_simulator/v1/iam/principals/executor/policy',policy)
   c=client(endpoint,operator);other=client(other_endpoint,other_operator)
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium');browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);context=browser.new_context(viewport={'width':1440,'height':1000});page=context.new_page();errors=[];outside=[];page.on('pageerror',lambda e:errors.append(str(e)))
   def route(r):
    if r.request.url.startswith(endpoint+'/'):r.continue_()
    else:outside.append(r.request.url);r.abort()
   context.route('**/*',route)
   page.goto(endpoint+'/_simulator/console/');page.get_by_label('Access token',exact=True).fill(operator);page.get_by_role('button',name='Open sandbox',exact=True).click();page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Lambda',exact=True).click();expect(page.get_by_role('heading',name='No functions yet',exact=True)).to_be_visible()
   page.get_by_role('button',name='Create function',exact=True).first.click();page.get_by_label('Function name',exact=True).fill('ui-lambda');page.get_by_label('Execution role ARN',exact=True).fill('arn:aws:iam::000000000000:role/executor');page.get_by_role('button',name='Create function',exact=True).last.click();expect(page.get_by_role('heading',name='ui-lambda',exact=True)).to_be_visible();page.get_by_label('Event JSON',exact=True).fill('{"value":7}');page.get_by_role('button',name='Test',exact=True).click();expect(page.get_by_text('Execution succeeded',exact=True)).to_be_visible(timeout=20000)
   response=json.loads(page.get_by_label('Response',exact=True).input_value());self.assertEqual(json.loads(response['body'])['event'],{'value':7});self.assertEqual(json.loads(c.invoke(FunctionName='ui-lambda',Payload=b'{"value":7}')['Payload'].read()),response)
   code=io.BytesIO()
   with zipfile.ZipFile(code,'w',compression=zipfile.ZIP_DEFLATED) as z:z.writestr('lambda_function.py',"import sqlite3\ndef lambda_handler(event, context):\n    db=sqlite3.connect(':memory:')\n    return {'answer':db.execute('select ? * 6',(event['value'],)).fetchone()[0]}\n")
   previous=c.get_function_configuration(FunctionName='ui-lambda')['RevisionId'];page.get_by_role('tab',name='Code',exact=True).click();page.get_by_label('Function ZIP',exact=True).set_input_files({'name':'function.zip','mimeType':'application/zip','buffer':code.getvalue()});page.get_by_role('button',name='Deploy',exact=True).click();expect(page.get_by_text('Function code deployed.',exact=True)).to_be_visible();configuration=c.get_function_configuration(FunctionName='ui-lambda');self.assertNotEqual(previous,configuration['RevisionId']);self.assertEqual(configuration['CodeSha256'],base64.b64encode(hashlib.sha256(code.getvalue()).digest()).decode())
   page.get_by_role('tab',name='Test',exact=True).click();page.get_by_role('button',name='Test',exact=True).click();expect(page.get_by_label('Response',exact=True)).to_have_value('{\n  "answer": 42\n}');self.assertEqual(json.loads(c.invoke(FunctionName=configuration['FunctionArn'],Payload=b'{"value":7}')['Payload'].read()),{'answer':42});page.screenshot(path=str(REPORTS/'lambda-test-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});assert_reflow(page);page.screenshot(path=str(REPORTS/'lambda-test-mobile.png'),full_page=True);page.set_viewport_size({'width':1440,'height':1000})
   invoke_policy={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'lambda:InvokeFunction','Resource':configuration['FunctionArn']}]};control(endpoint,operator,'/_simulator/v1/iam/principals/invoker/policy',invoke_policy);participant=control(endpoint,operator,'/_simulator/v1/access',{'Principal':'invoker'})['AccessToken'];limited=client(endpoint,participant)
   with self.assertRaises(ClientError):limited.get_function_configuration(FunctionName='ui-lambda')
   participant_context=browser.new_context(viewport={'width':1440,'height':1000});participant_page=participant_context.new_page();participant_page.goto(endpoint+'/_simulator/console/');participant_page.get_by_label('Access token',exact=True).fill(participant);participant_page.get_by_role('button',name='Open sandbox',exact=True).click();participant_page.get_by_role('button',name='Services',exact=True).click();participant_page.get_by_role('menuitem',name='Lambda',exact=True).click();expect(participant_page.get_by_text('Functions unavailable',exact=True)).to_be_visible();participant_page.get_by_label('Function name or ARN',exact=True).fill(configuration['FunctionArn']);participant_page.get_by_role('button',name='Test',exact=True).click();expect(participant_page.get_by_label('Response',exact=True)).to_have_value('{\n  "answer": 42\n}')
   other_code=io.BytesIO()
   with zipfile.ZipFile(other_code,'w') as z:z.writestr('index.py',"def handler(event, context): return {'team':'other'}\n")
   other.create_function(FunctionName='ui-lambda',Runtime='python3.12',Role='arn:aws:iam::000000000000:role/executor',Handler='index.handler',Timeout=5,Code={'ZipFile':other_code.getvalue()});self.assertEqual(json.loads(other.invoke(FunctionName='ui-lambda',Payload=b'{}')['Payload'].read()),{'team':'other'});self.assertEqual(json.loads(c.invoke(FunctionName='ui-lambda',Payload=b'{"value":7}')['Payload'].read()),{'answer':42})
   with self.assertRaises(ClientError) as denied:client(other_endpoint,participant).invoke(FunctionName='ui-lambda',Payload=b'{}')
   self.assertEqual(denied.exception.response['ResponseMetadata']['HTTPStatusCode'],401)
   page.get_by_role('button',name='Functions',exact=True).click();page.get_by_role('radio',name='Select ui-lambda',exact=True).check();page.get_by_role('button',name='Delete',exact=True).click();page.get_by_label('Type ui-lambda to confirm',exact=True).fill('ui-lambda');page.get_by_role('dialog').get_by_role('button',name='Delete',exact=True).click();expect(page.get_by_role('heading',name='No functions yet',exact=True)).to_be_visible();self.assertEqual(c.list_functions()['Functions'],[]);self.assertEqual(len(other.list_functions()['Functions']),1);other.delete_function(FunctionName='ui-lambda');self.assertEqual(errors,[]);self.assertEqual(outside,[]);browser.close()
 def test_refuses_non_loopback_by_default(self):
  result=subprocess.run([str(BINARY),'--listen','0.0.0.0:0'],capture_output=True,text=True);self.assertNotEqual(result.returncode,0);self.assertIn('--allow-remote',result.stderr)

 def test_sqs_served_static_templates_execute_against_native_state(self):
  from sqs_docs import run
  run()

 def test_sqs_console_native_state_scope_and_receipt_custody(self):
  from sqs_console import run
  run()

 def test_cloudwatch_logs_native_sdk_restart_and_two_teams(self):
  from cloudwatch_logs import run
  run()

 def test_cloudwatch_logs_console_and_native_sdk_share_state(self):
  from cloudwatch_logs_console import run
  run()

 def test_secrets_manager_native_two_teams_restart_and_redaction(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('secrets_manager.py'))],check=True)

 def test_secrets_manager_version_metadata_native_sdk_and_cursor_scope(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('secrets_versions.py'))],check=True,timeout=180)

 def test_secrets_manager_version_metadata_console_scope_and_paging(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('secret_versions_console.py'))],check=True,timeout=180)

 def test_secrets_manager_console_reveal_context_and_hide(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('secrets_manager_console.py'))],check=True)

 def test_secrets_manager_served_openapi_examples(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('secrets_manager_docs.py'))],check=True)

 def test_dynamodb_native_sdk_two_clis_identity_restart_and_cleanup(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('dynamodb.py'))],check=True,timeout=120)

 def test_dynamodb_served_seven_templates_native_shared_state(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('dynamodb_docs.py'))],check=True,timeout=120)
 def test_sqs_console_locale_preserves_drafts_and_native_receipts(self):
  from sqs_locale import run_sqs_locale
  run_sqs_locale()

 def test_dynamodb_console_native_state_keys_context_locale_and_cancellation(self):
  subprocess.run([sys.executable,str(Path(__file__).with_name('dynamodb_console.py'))],check=True,timeout=240)

 def test_cloudformation_original_crypto_template_native_sdk_lifecycle(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('cloudformation_crypto.py'))], check=True, timeout=180)

 def test_aws_catalog_native_sdk_two_teams_policy_restart_and_cleanup(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('aws_catalog.py'))], check=True, timeout=240)

 def test_aws_catalog_console_reference_and_shared_native_state(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('aws_catalog_console.py'))], check=True, timeout=300)

 def test_aws_catalog_fixture_lifecycles_cover_every_bound_operation(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('aws_catalog_fixtures.py'))], check=True, timeout=900)

 def test_gcp_catalog_discovery_client_two_teams_owner_restart_and_cleanup(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('gcp_catalog_fixtures.py'))], check=True, timeout=600)

 def test_gcp_catalog_console_reference_and_shared_native_state(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('gcp_catalog_console.py'))], check=True, timeout=300)

 def test_azure_catalog_official_sdks_two_teams_owner_restart_and_cleanup(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('azure_catalog_fixtures.py'))], check=True, timeout=600)

 def test_azure_catalog_console_reference_and_shared_native_state(self):
  subprocess.run([sys.executable, str(Path(__file__).with_name('azure_catalog_console.py'))], check=True, timeout=300)


if __name__=='__main__':unittest.main(verbosity=2)
