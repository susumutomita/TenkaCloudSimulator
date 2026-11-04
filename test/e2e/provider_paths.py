"""Authenticated real-binary S3 path regression; dummy SDK credentials, loopback only.
No browser/guest/cloud dependency. Run with scripts/requirements-test.txt.
"""
import os, tempfile, pathlib, socket, subprocess, time, json, urllib.request, urllib.parse
os.environ.update(AWS_CONFIG_FILE='/dev/null',AWS_SHARED_CREDENTIALS_FILE='/dev/null',AWS_EC2_METADATA_DISABLED='true')
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
binary=str(pathlib.Path(os.environ.get('SIMULATOR_BINARY','dist/tenkacloud-simulator')).resolve())
reports=pathlib.Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve(); reports.mkdir(parents=True,exist_ok=True)
keys=['b','a/b','a/../b','a/./b','a//b','a///b','a/%2F/b','a/%2f/b','a/%25/b','a/%/b','a/%252F/b','a/雪/é','./b']
results=[];requests=[];transport_statuses=[];counts={'calls':0,'snapshots':0,'sends':0}
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
   def local_target_only(request,**kwargs):
    target=urllib.parse.urlsplit(request.url)
    assert target.scheme=='http' and target.netloc==f'127.0.0.1:{port}', 'SDK request left the owned loopback origin'
    counts['sends']+=1
   c.meta.events.register('before-send.s3',local_target_only)
   c.meta.events.register('needs-retry.s3',lambda response=None,**kw:transport_statuses.append(response[0].status_code) if response is not None else None)
   c.meta.events.register('after-call.s3',lambda http_response,**kw:requests.append({'status':http_response.status_code,'location':http_response.headers.get('location')}))
   return c
  root=client('');pref=client('/_simulator/providers/aws');bucket='path-probe-bucket'
  clients=[root,pref];expected={}
  def invoke(c,operation,**kwargs):
   counts['calls']+=1
   return getattr(c,operation)(**kwargs)
  def snapshot():
   counts['snapshots']+=1
   for c in clients:
    listing=invoke(c,'list_objects_v2',Bucket=bucket)
    listed=[item['Key'] for item in listing.get('Contents',[])]
    assert not listing.get('IsTruncated') and sorted(listed)==sorted(expected), (listed,sorted(expected))
    for key,body in expected.items():
     assert invoke(c,'get_object',Bucket=bucket,Key=key)['Body'].read()==body, key
  invoke(root,'create_bucket',Bucket=bucket)
  # Keep collision neighbors live together: a shared SDK/server normalizer must
  # not pass merely because root and prefixed clients agree on a wrong key.
  for index,key in enumerate(keys):
   expected[key]=(f'seed-{index}:'+key).encode()
   invoke(clients[index%2],'put_object',Bucket=bucket,Key=key,Body=expected[key]);snapshot()
  for index,key in enumerate(keys):
   for writer,c in enumerate(clients):
    assert invoke(c,'get_object',Bucket=bucket,Key=key)['Body'].read()==expected[key];snapshot()
    assert invoke(c,'head_object',Bucket=bucket,Key=key)['ContentLength']==len(expected[key]);snapshot()
    expected[key]=(f'updated-{writer}-{index}:'+key).encode()
    invoke(c,'put_object',Bucket=bucket,Key=key,Body=expected[key]);snapshot()
    invoke(c,'delete_object',Bucket=bucket,Key=key);del expected[key];snapshot()
    for reader in clients:
     try:invoke(reader,'get_object',Bucket=bucket,Key=key)
     except ClientError as e:assert e.response['ResponseMetadata']['HTTPStatusCode']==404
     else:raise AssertionError('deleted key remains')
     snapshot()
    expected[key]=(f'restored-{writer}-{index}:'+key).encode()
    invoke(c,'put_object',Bucket=bucket,Key=key,Body=expected[key]);snapshot()
   results.append({'key':key,'bothEndpointsPutGetHeadDelete':True,'allNeighborsUnchanged':True})
  for index,key in enumerate(keys):
   invoke(clients[index%2],'delete_object',Bucket=bucket,Key=key);del expected[key];snapshot()
  invoke(root,'delete_bucket',Bucket=bucket)
  assert bucket not in [item['Name'] for item in invoke(root,'list_buckets')['Buckets']]
  assert counts['calls']>0 and counts['sends']==counts['calls'] and len(requests)==counts['calls'] and len(transport_statuses)==counts['calls']
  assert all(x['status'] < 300 or x['status'] == 404 for x in requests), requests
  assert all(not 300 <= code < 400 for code in transport_statuses), transport_statuses
  report={'scope':'simultaneous exact-key set and all-neighbor snapshots after root/prefixed S3 operations','authenticated':True,'actualNativeSDK':'boto3 '+boto3.__version__,'keys':results,'sdkCallCount':counts['calls'],'snapshotCount':counts['snapshots'],'guardedSendCount':counts['sends'],'requestCount':len(requests),'transportResponseCount':len(transport_statuses),'redirects':sum(300<=code<400 for code in transport_statuses),'cleanup':True}
  (reports/'provider-path-sdk.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');print(json.dumps(report,ensure_ascii=False))
 finally:
  if proc.poll() is None:proc.terminate()
  try:proc.wait(timeout=10)
  except subprocess.TimeoutExpired:
   proc.kill();proc.wait(timeout=10)
   raise AssertionError('Graceful shutdown timed out; killed and reaped owned process')
  if proc.returncode:raise RuntimeError('server shutdown error')
