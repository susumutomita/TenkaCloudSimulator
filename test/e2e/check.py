"""Real binary + boto3 + Chromium conformance. All requests target loopback.
Dev dependencies: scripts/requirements-test.txt. No AWS account or credentials.
"""
import contextlib
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

os.environ['AWS_CONFIG_FILE']='/dev/null'
os.environ['AWS_SHARED_CREDENTIALS_FILE']='/dev/null'
os.environ['AWS_EC2_METADATA_DISABLED']='true'
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
from playwright.sync_api import sync_playwright, expect

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
def submit(page):
 dialog=page.get_by_role('dialog')
 if dialog.is_visible():dialog.get_by_role('button').last.click()
 else:page.get_by_role('button',name=re.compile('^(Create bucket|Upload|Create parameter|Save changes)$')).last.click()
class Conformance(unittest.TestCase):
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
   page.goto(endpoint+'/_simulator/console/');expect(page.get_by_role('heading',name='No buckets yet',exact=True)).to_be_visible()
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
   page.get_by_role('link',name='Identity policies',exact=True).click();page.get_by_label('Principal name',exact=True).fill('reader');submit(page);page.get_by_role('button',name='Choose identity',exact=True).click();page.get_by_label('Principal name',exact=True).fill('reader');submit(page);expect(page.get_by_role('button',name='Choose identity',exact=True)).to_have_text('reader')
   page.get_by_role('button',name='Create bucket',exact=True).first.click();page.get_by_label('Bucket name',exact=True).fill('denied-bucket');submit(page);expect(page.locator('[id^=form-error-]')).to_contain_text('implicitDeny');page.get_by_role('button',name='Cancel',exact=True).click();self.assertNotIn('denied-bucket',[b['Name'] for b in c.list_buckets()['Buckets']])
   page.get_by_role('searchbox',name='Find resources',exact=True).fill('no-matches');expect(page.get_by_role('heading',name='No matching resources')).to_be_visible();page.get_by_role('searchbox',name='Find resources',exact=True).fill('')
   page.set_viewport_size({'width':390,'height':844});expect(page.get_by_role('button',name='Create bucket',exact=True).first).to_be_visible();page.screenshot(path=str(REPORTS/'console-mobile.png'),full_page=True);self.assertEqual(page.evaluate('document.documentElement.scrollWidth <= innerWidth'),True)
   self.assertEqual(page.evaluate('window.__policyViolations'),[]);page.set_viewport_size({'width':1440,'height':1000});page.goto(endpoint+'/_simulator/docs/');expect(page.get_by_role('heading',name='TenkaCloud Simulator API',exact=True)).to_be_visible(timeout=15000)
   expect(page.get_by_role('button',name='Ask AI',exact=True)).to_have_count(0)
   # Scalar executes the real S3 ListBuckets operation. The response contains console-created state.
   page.get_by_role('button',name='Test Request',exact=False).first.click();page.wait_for_timeout(400)
   # Use the request client Send button, not a parallel fetch injected by the test.
   send=page.get_by_role('button',name=re.compile('^Send.*get request'))
   expect(send).to_be_visible();send.click();expect(page.get_by_text('ui-practice',exact=False).last).to_be_visible(timeout=10000)
   page.get_by_role('button',name='Close Client',exact=True).click()
   c.create_bucket(Bucket='practice-bucket')
   page.get_by_role('link',name=re.compile('^Upload or replace an object')).click()
   page.get_by_role('button',name='Test Request (put /{bucket}/{key})',exact=True).click()
   put_send=page.get_by_role('button',name=re.compile('^Send.*put request'))
   expect(put_send).to_be_visible();put_send.click()
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
   page.get_by_role('link',name=re.compile('^List all buckets')).click()
   page.get_by_role('button',name='Test Request (get /)',exact=True).click()
   send=page.get_by_role('button',name=re.compile('^Send.*get request'))
   send.click();expect(page.get_by_text('ui-practice',exact=False).last).to_be_visible(timeout=10000)
   page.screenshot(path=str(REPORTS/'api-reference.png'),full_page=False,animations='disabled')
   self.assertEqual(page.evaluate('window.__policyViolations'),[]);self.assertEqual(errors,[]);self.assertEqual(outside,[],'Runtime attempted external network requests');browser.close()
 def test_console_literal_dot_keys_do_not_mutate_neighbors(self):
  with running() as endpoint,sync_playwright() as p:
   c=sdk(endpoint);c.create_bucket(Bucket='dot-practice');c.create_bucket(Bucket='other-bucket')
   c.put_object(Bucket='dot-practice',Key='victim',Body=b'local sentinel')
   c.put_object(Bucket='other-bucket',Key='victim',Body=b'other sentinel')
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
   browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox'])
   page=browser.new_page();page.goto(endpoint+'/_simulator/console/')
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
   expect(page.get_by_role('button',name='Choose identity',exact=True)).to_have_count(0)
   page.get_by_role('button',name='Services',exact=True).click();page.get_by_role('menuitem',name='Systems Manager',exact=True).click();page.get_by_role('link',name=outputs['ParameterName'],exact=True).click();expect(page.get_by_label('Value',exact=True)).to_have_value('TC{'+seed+'}');expect(page.get_by_role('tab',name='Overview',exact=True)).to_be_visible()
   page.get_by_role('button',name='Edit parameter',exact=True).click();expect(page.get_by_role('button',name='Type String',exact=True)).to_be_disabled();page.get_by_label('Value',exact=True).fill('unauthorized change');page.get_by_role('button',name='Save changes',exact=True).click();expect(page.locator('[id^=form-error-]')).to_contain_text('403');self.assertEqual(client('ssm',token).get_parameter(Name=outputs['ParameterName'])['Parameter']['Value'],'TC{'+seed+'}')
   page.get_by_role('button',name='Cancel',exact=True).click();page.set_viewport_size({'width':390,'height':844});self.assertTrue(page.evaluate('document.documentElement.scrollWidth <= innerWidth'));self.assertEqual(errors,[]);self.assertEqual(outside,[])
   browser.close();cfn.delete_stack(StackName='tc-hello-world-browser')
 def test_region_change_clears_bucket_and_writes_only_new_region(self):
  with running() as endpoint,sync_playwright() as p:
   c=sdk(endpoint);c.create_bucket(Bucket='east-region-bucket');c.create_bucket(Bucket='tokyo-region-bucket',CreateBucketConfiguration={'LocationConstraint':'ap-northeast-1'});c.put_object(Bucket='east-region-bucket',Key='sentinel',Body=b'east unchanged')
   executable=os.environ.get('CHROMIUM_EXECUTABLE') or shutil.which('chromium')
   browser=p.chromium.launch(**({'executable_path':executable} if executable else {}),args=['--no-sandbox']);page=browser.new_page(viewport={'width':1440,'height':1000});page.goto(endpoint+'/_simulator/console/');page.get_by_role('link',name='east-region-bucket',exact=True).click();expect(page.get_by_role('link',name='sentinel',exact=True)).to_be_visible()
   page.get_by_role('button',name='us-east-1',exact=True).click();page.get_by_role('menuitem',name='ap-northeast-1',exact=True).click();expect(page.get_by_role('heading',name='General purpose buckets',exact=False)).to_be_visible();expect(page.get_by_role('link',name='east-region-bucket',exact=True)).to_have_count(0);page.get_by_role('link',name='tokyo-region-bucket',exact=True).click()
   page.get_by_role('button',name='Upload',exact=True).first.click();page.get_by_label('Choose a file').set_input_files({'name':'new-region.txt','mimeType':'text/plain','buffer':b'tokyo only'});submit(page);expect(page.get_by_role('link',name='new-region.txt',exact=True)).to_be_visible()
   self.assertEqual(c.get_object(Bucket='tokyo-region-bucket',Key='new-region.txt')['Body'].read(),b'tokyo only');self.assertEqual(c.get_object(Bucket='east-region-bucket',Key='sentinel')['Body'].read(),b'east unchanged')
   with self.assertRaises(ClientError):c.get_object(Bucket='east-region-bucket',Key='new-region.txt')
   browser.close()
 def test_refuses_non_loopback_by_default(self):
  result=subprocess.run([str(BINARY),'--listen','0.0.0.0:0'],capture_output=True,text=True);self.assertNotEqual(result.returncode,0);self.assertIn('--allow-remote',result.stderr)
if __name__=='__main__':unittest.main(verbosity=2)
