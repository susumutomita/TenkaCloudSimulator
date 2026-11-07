import contextlib,hashlib,json,os,pathlib,socket,subprocess,tempfile,time,urllib.request
os.environ.update(AWS_CONFIG_FILE='/dev/null',AWS_SHARED_CREDENTIALS_FILE='/dev/null',AWS_EC2_METADATA_DISABLED='true')
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError
ROOT=pathlib.Path(__file__).resolve().parents[2]
TEMPLATE=ROOT/'internal/aws/testdata/sts-original/hello-world.yaml'
(ROOT/'reports').mkdir(exist_ok=True)
report={'boundary':'Actual native SDK + unchanged original problem template; not Portal/GUI grading acceptance','templateSha256':hashlib.sha256(TEMPLATE.read_bytes()).hexdigest(),'checks':[]}
with tempfile.TemporaryDirectory(prefix='native-hello-') as directory:
 with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
 d=pathlib.Path(directory);tokenpath=d/'token';endpoint=f'http://127.0.0.1:{port}'
 token=''
 process=subprocess.Popen([str(pathlib.Path(os.environ.get('SIMULATOR_BINARY', str(ROOT/'dist/tenkacloud-simulator'))).resolve()),'--listen',f'127.0.0.1:{port}','--state',str(d/'state.db'),'--token-file',str(tokenpath)],env={**os.environ,'CI':'1'},stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
 try:
  for _ in range(200):
   if process.poll() is not None:raise RuntimeError('simulator startup failed')
   if tokenpath.exists():
    token=tokenpath.read_text().strip()
    try:
     with urllib.request.urlopen(urllib.request.Request(endpoint+'/_simulator/health',headers={'X-Simulator-Token':token}),timeout=1):break
    except OSError:pass
   time.sleep(.05)
  else:raise RuntimeError('simulator readiness timed out')
  creds={'aws_access_key_id':'TCSIMOPERATOR','aws_secret_access_key':token}
  def client(service,credentials=None):
   return boto3.client(service,endpoint_url=endpoint,region_name='ap-northeast-1',config=Config(retries={'max_attempts':0}),**(credentials or creds))
  def check(name,fn,expected=200):
   try:
    result=fn();status=result.get('ResponseMetadata',{}).get('HTTPStatusCode');item={'name':name,'httpStatus':status,'passed':status==expected}
   except ClientError as e:
    result=None;status=e.response['ResponseMetadata']['HTTPStatusCode'];item={'name':name,'httpStatus':status,'code':e.response['Error']['Code'],'message':e.response['Error']['Message'],'passed':status==expected}
   except Exception as e:
    result=None;item={'name':name,'exception':type(e).__name__,'message':str(e),'passed':False}
   report['checks'].append(item)
   if not item['passed']:raise RuntimeError(name+' failed: '+json.dumps(item))
   return result
  caller=check('native-operator-get-caller-identity',lambda:client('sts').get_caller_identity())
  assert caller['Account']=='999999999999',caller
  iam=client('iam');role='HelloDeployRole';arn='arn:aws:iam::000000000000:role/'+role
  trust={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Principal':{'AWS':'arn:aws:iam::999999999999:root'},'Action':'sts:AssumeRole','Condition':{'StringEquals':{'sts:ExternalId':'local-host-external-123'}}}]}
  check('native-bootstrap-deploy-role',lambda:iam.create_role(RoleName=role,AssumeRolePolicyDocument=json.dumps(trust)))
  policy={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['cloudformation:CreateStack','cloudformation:DescribeStacks','cloudformation:DescribeStackEvents','cloudformation:DeleteStack'],'Resource':'arn:aws:cloudformation:ap-northeast-1:000000000000:stack/tc-hello-*/*'},{'Effect':'Allow','Action':['ssm:PutParameter','ssm:GetParameter','ssm:DeleteParameter'],'Resource':'arn:aws:ssm:ap-northeast-1:000000000000:parameter/tc-hello-*/*'},{'Effect':'Allow','Action':['iam:CreateRole','iam:DeleteRole','iam:PutRolePolicy','iam:DeleteRolePolicy','iam:AttachRolePolicy','iam:DetachRolePolicy','iam:TagRole','sts:AssumeRole'],'Resource':'arn:aws:iam::000000000000:role/tc-hello-*'}]}
  check('native-bootstrap-least-privilege-policy',lambda:iam.put_role_policy(RoleName=role,PolicyName='HelloDeployment',PolicyDocument=json.dumps(policy)))
  check('wrong-external-id-denied',lambda:client('sts').assume_role(RoleArn=arn,RoleSessionName='local-host',ExternalId='wrong',DurationSeconds=900),403)
  assumed=check('native-assume-deploy-role',lambda:client('sts').assume_role(RoleArn=arn,RoleSessionName='local-host',ExternalId='local-host-external-123',DurationSeconds=900))['Credentials']
  def sdk_credentials(c):return {'aws_access_key_id':c['AccessKeyId'],'aws_secret_access_key':c['SecretAccessKey'],'aws_session_token':c['SessionToken']}
  deploy=sdk_credentials(assumed)
  identity=check('native-deploy-session-get-caller-identity',lambda:client('sts',deploy).get_caller_identity());assert identity['Account']=='000000000000'
  cfn=client('cloudformation',deploy)
  prefix='tc-hello-world-native'
  created=check('original-template-native-create-stack',lambda:cfn.create_stack(StackName=prefix,TemplateBody=TEMPLATE.read_text(),Capabilities=['CAPABILITY_NAMED_IAM'],Parameters=[{'ParameterKey':k,'ParameterValue':v} for k,v in {'NamePrefix':prefix,'TenkaCloudAccountId':caller['Account'],'ExternalId':'native-job-external-123','FlagSeed':'RandomLocalFixture0123456789'}.items()]))
  stack=check('native-deploy-describe-stack',lambda:cfn.describe_stacks(StackName=created['StackId']))['Stacks'][0]
  outputs={v['OutputKey']:v['OutputValue'] for v in stack['Outputs']}
  events=check('native-original-deployment-events',lambda:cfn.describe_stack_events(StackName=created['StackId']))['StackEvents']
  assert events[0]['ResourceStatus']=='CREATE_COMPLETE' and len(events)>=4
  assert all(e['StackId']==created['StackId'] and 'ResourceProperties' not in e and 'ClientRequestToken' not in e for e in events)
  assert outputs['ParameterValue'] not in json.dumps(events,default=str)
  report['createEventCount']=len(events)
  viewer=check('native-assume-original-participant-role',lambda:client('sts').assume_role(RoleArn=outputs['ParticipantViewerRoleArn'],RoleSessionName='participant',ExternalId='native-job-external-123',DurationSeconds=900))['Credentials']
  participant=sdk_credentials(viewer)
  value=check('native-participant-read-original-flag',lambda:client('ssm',participant).get_parameter(Name=outputs['ParameterName']))
  assert value['Parameter']['Value']==outputs['ParameterValue']
  check('native-participant-grading-output-denied',lambda:client('cloudformation',participant).describe_stacks(StackName=created['StackId']),403)
  check('native-participant-event-history-denied',lambda:client('cloudformation',participant).describe_stack_events(StackName=created['StackId']),403)
  check('native-participant-write-denied',lambda:client('ssm',participant).put_parameter(Name=outputs['ParameterName'],Type='String',Value='wrong',Overwrite=True),403)
  check('native-original-stack-delete',lambda:cfn.delete_stack(StackName=created['StackId']))
  deleted_events=check('native-original-deletion-events',lambda:cfn.describe_stack_events(StackName=created['StackId']))['StackEvents']
  assert deleted_events[0]['ResourceStatus']=='DELETE_COMPLETE' and len(deleted_events)>len(events)
  report['retainedEventCountAfterDelete']=len(deleted_events)
  check('native-participant-revoked-after-stack-deletion',lambda:client('ssm',participant).get_parameter(Name=outputs['ParameterName']),403)
  check('native-original-parameter-cleanup',lambda:client('ssm').get_parameter(Name=outputs['ParameterName']),400)
  check('native-bootstrap-policy-cleanup',lambda:iam.delete_role_policy(RoleName=role,PolicyName='HelloDeployment'))
  check('native-bootstrap-role-cleanup',lambda:iam.delete_role(RoleName=role))
 except Exception as e:
  report['error']=str(e)
 finally:
  process.terminate();out,err=process.communicate(timeout=8)
  report['processExitCode']=process.returncode
  report['secretInLogs']=bool(token) and (token in out or token in err)
report['passed']=len(report['checks'])==20 and all(x['passed'] for x in report['checks']) and not report.get('error') and report['processExitCode']==0 and not report['secretInLogs']
(ROOT/'reports/sts-native-sdk.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report,indent=2))
raise SystemExit(0 if report['passed'] else 1)
