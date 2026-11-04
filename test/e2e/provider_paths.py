"""Authenticated real-binary S3 path regression; dummy SDK credentials, loopback only.
No browser/guest/cloud dependency. Run with scripts/requirements-test.txt.
"""
import os, tempfile, pathlib, socket, subprocess, time, json, urllib.request
os.environ.update(AWS_CONFIG_FILE='/dev/null',AWS_SHARED_CREDENTIALS_FILE='/dev/null',AWS_EC2_METADATA_DISABLED='true')
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
binary=str(pathlib.Path(os.environ.get('SIMULATOR_BINARY','dist/tenkacloud-simulator')).resolve())
reports=pathlib.Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve(); reports.mkdir(parents=True,exist_ok=True)
keys=['a/../b','a/./b','a//b','a/%2F/b','a/%25/b','a/雪/é','./b','a///b']
results=[];requests=[];transport_statuses=[]
with tempfile.TemporaryDirectory(prefix='tcsim-provider-path-') as td:
 with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
 tokenfile=pathlib.Path(td)/'token'; endpoint=f'http://127.0.0.1:{port}'
 proc=subprocess.Popen([binary,'--listen',f'127.0.0.1:{port}','--token-file',str(tokenfile)],cwd=td,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
 try:
  for _ in range(250):
   if proc.poll() is not None:raise RuntimeError('server exited')
   if tokenfile.exists():
    token=tokenfile.read_text().strip()
    try:
     urllib.request.urlopen(urllib.request.Request(endpoint+'/_simulator/health',headers={'X-Simulator-Token':token}),timeout=1).close();break
    except OSError:pass
   time.sleep(.02)
  else:raise TimeoutError('startup')
  def client(prefix):
   c=boto3.client('s3',endpoint_url=endpoint+prefix,region_name='us-east-1',aws_access_key_id='TCSIMEXAMPLE',aws_secret_access_key='dummy-local-only',config=Config(s3={'addressing_style':'path'},retries={'max_attempts':0},request_checksum_calculation='when_required',response_checksum_validation='when_required'))
   c.meta.events.register('before-sign.s3',lambda request,**kw:request.headers.add_header('X-Simulator-Token',token))
   c.meta.events.register('needs-retry.s3',lambda response=None,**kw:transport_statuses.append(response[0].status_code) if response is not None else None)
   c.meta.events.register('after-call.s3',lambda http_response,**kw:requests.append({'status':http_response.status_code,'location':http_response.headers.get('location')}))
   return c
  root=client('');pref=client('/_simulator/providers/aws');bucket='path-probe-bucket'
  root.create_bucket(Bucket=bucket);root.put_object(Bucket=bucket,Key='b',Body=b'sentinel')
  for key in keys:
   body=('original:'+key).encode();root.put_object(Bucket=bucket,Key=key,Body=body)
   assert pref.get_object(Bucket=bucket,Key=key)['Body'].read()==body
   assert pref.head_object(Bucket=bucket,Key=key)['ContentLength']==len(body)
   pref.put_object(Bucket=bucket,Key=key,Body=b'replaced')
   assert root.get_object(Bucket=bucket,Key=key)['Body'].read()==b'replaced'
   pref.delete_object(Bucket=bucket,Key=key)
   try:root.get_object(Bucket=bucket,Key=key)
   except ClientError as e:assert e.response['ResponseMetadata']['HTTPStatusCode']==404
   else:raise AssertionError('deleted key remains')
   assert root.get_object(Bucket=bucket,Key='b')['Body'].read()==b'sentinel'
   results.append({'key':key,'putGetHeadDelete':True,'neighborUnchanged':True})
  root.delete_object(Bucket=bucket,Key='b');root.delete_bucket(Bucket=bucket)
  assert all(x['status'] < 300 or x['status'] == 404 for x in requests), requests
  assert all(not 300 <= code < 400 for code in transport_statuses), transport_statuses
  report={'scope':'native root and provider-prefixed S3 literal keys','authenticated':True,'actualNativeSDK':'boto3 '+boto3.__version__,'keys':results,'requestCount':len(requests),'transportResponseCount':len(transport_statuses),'redirects':sum(300<=code<400 for code in transport_statuses),'cleanup':True}
  (reports/'provider-path-sdk.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');print(json.dumps(report,ensure_ascii=False))
 finally:
  proc.terminate();proc.wait(timeout=10)
  if proc.returncode:raise RuntimeError('server shutdown error')
