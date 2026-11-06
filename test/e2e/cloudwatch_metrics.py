"""Actual two Simulator CLIs, local Access/native IAM and official Metrics SDK.

No development fixture authentication/clock, decoder replacement, external AWS
endpoint or direct state-writing success surrogate. SQLite writes below create
one explicitly labelled host-storage fault, then repair it; all success uses SDK.
"""
import argparse
from contextlib import ExitStack
from datetime import datetime, timedelta, timezone
import gzip
import hashlib
import importlib.metadata
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit

os.environ['AWS_CONFIG_FILE'] = os.devnull
os.environ['AWS_SHARED_CREDENTIALS_FILE'] = os.devnull
os.environ['AWS_EC2_METADATA_DISABLED'] = 'true'
os.environ.pop('AWS_PROFILE', None)
os.environ.pop('AWS_DEFAULT_PROFILE', None)
import boto3
import botocore
from botocore.config import Config
from botocore.exceptions import ClientError
from botocore.parsers import ResponseParserError

MODEL_SHA = '855d223991b77b224ac0a4079a75a17bd51fa7df57a89d9ab2979fbf6738539a'
STATS = ['SampleCount', 'Sum', 'Minimum', 'Maximum', 'Average']
COUNTERS, CONTROLS = [], []


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None


def sha(data):
    return hashlib.sha256(data).hexdigest()


def guard(url, endpoint, path=None):
    expected, actual = urlsplit(endpoint), urlsplit(url)
    assert expected.scheme == actual.scheme == 'http' and expected.port
    assert ipaddress.ip_address(expected.hostname).is_loopback
    assert actual.netloc == expected.netloc and not actual.username and not actual.password
    assert not actual.query and not actual.fragment
    assert ipaddress.ip_address(actual.hostname).is_loopback
    if path is not None:
        assert actual.path == path


def frame(path, metric_only=False):
    with sqlite3.connect('file:' + str(path) + '?mode=ro', uri=True) as db:
        names = sorted(x[0] for x in db.execute("SELECT name FROM sqlite_master WHERE type='table'"))
        result = []
        for name in names:
            if metric_only and not name.startswith('aws_metrics_'):
                continue
            quoted = '"' + name.replace('"', '""') + '"'
            rows = db.execute('SELECT * FROM ' + quoted).fetchall()
            safe = [[{'blobSHA256':sha(v)} if isinstance(v, bytes) else v for v in row] for row in rows]
            result.append([name, sorted(safe, key=lambda x:json.dumps(x,sort_keys=True))])
        if not metric_only:
            result.append(['schema', db.execute('SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name').fetchall()])
        return sha(json.dumps(result,sort_keys=True,separators=(',',':')).encode())


class Team:
    def __init__(self, stack, binary, folder):
        self.binary, self.folder, self.proc, self.log = binary, folder, None, None
        folder.mkdir(parents=True, exist_ok=True)
        self.db, self.tokenfile = folder/'state.sqlite', folder/'local-access-token'
        with socket.socket() as free:
            free.bind(('127.0.0.1',0)); self.port = free.getsockname()[1]
        self.endpoint = 'http://127.0.0.1:' + str(self.port)
        stack.callback(self.stop)  # Before launch/readiness may fail.

    def start(self):
        assert self.proc is None
        self.log = (self.folder/'cli.log').open('a')
        self.proc = subprocess.Popen([str(self.binary),'--listen','127.0.0.1:'+str(self.port),'--state',str(self.db),'--token-file',str(self.tokenfile)],stdout=self.log,stderr=subprocess.STDOUT)
        deadline = time.monotonic()+20
        while time.monotonic()<deadline:
            assert self.proc.poll() is None, 'actual CLI exited before authenticated readiness'
            if self.tokenfile.exists():
                # Only this freshly allocated CLI's own temporary authority file.
                self.token = self.tokenfile.read_text().strip()
                assert re.fullmatch(r'[A-Za-z0-9_-]{32,256}',self.token)
                request = urllib.request.Request(self.endpoint+'/_simulator/health',headers={'X-Simulator-Token':self.token})
                guard(request.full_url,self.endpoint)
                try:
                    with urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect()).open(request,timeout=1) as response:
                        if response.status == 200:
                            assert json.load(response)['simulator'] is True
                            return self
                except (urllib.error.URLError, TimeoutError):
                    pass
            time.sleep(0.05)
        raise AssertionError('actual CLI authenticated readiness deadline')

    def stop(self):
        if self.proc is not None:
            proc,self.proc = self.proc,None
            try:
                if proc.poll() is None: proc.terminate()
                try: code=proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill();proc.wait(timeout=10)
                    raise AssertionError('actual CLI required forced kill')
                assert code == 0, 'actual CLI shutdown failed'
            finally:
                self.log.close();self.log=None


def control(team,path,data=None,method='GET'):
    body=None if data is None else json.dumps(data).encode()
    request=urllib.request.Request(team.endpoint+path,data=body,method=method,headers={'X-Simulator-Token':team.token,'Content-Type':'application/json'})
    guard(request.full_url,team.endpoint)
    with urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect()).open(request,timeout=10) as response:
        assert response.status == 200
        CONTROLS.append({'path':path,'method':method,'status':response.status})
        return json.load(response)


def client(stack,team,service='cloudwatch',region='us-east-1',token=None,selected=None,route='/'):
    endpoint=team.endpoint+route
    guard(endpoint,team.endpoint,route)
    count=dict(service=service,region=region,route=route,calls=0,sends=0,responses=0,operations=[],statuses=[],encoding=[])
    COUNTERS.append(count)
    c=boto3.client(service,endpoint_url=endpoint,region_name=region,aws_access_key_id='TCSIMMETRICS',aws_secret_access_key='dummy',config=Config(retries={'max_attempts':0},proxies={},connect_timeout=5,read_timeout=10))
    stack.callback(c.close)
    credential=token if token is not None else team.token
    def before_sign(request,**kw):
        request.headers.add_header('X-Simulator-Token',credential)
        if selected:request.headers.add_header('X-Simulator-Principal',selected)
    def before_call(model,**kw):count['calls']+=1;count['operations'].append(model.name)
    def before_send(request,**kw):
        guard(request.url,team.endpoint,route)
        assert request.method=='POST'
        count['sends']+=1
        encoding=request.headers.get('Content-Encoding')
        if isinstance(encoding,bytes):encoding=encoding.decode('ascii')
        count['encoding'].append(encoding)
        # No X-Simulator-Service is injected: actual official signing metadata
        # must select monitoring through the production Gateway.
        assert request.headers.get('X-Simulator-Service') is None
    def before_parse(response_dict,**kw):
        # Standard read-only transport observation. Access401 is JSON before the
        # native Query handler; only its exact intentional test may tolerate
        # the SDK's parser exception. Normal native errors MUST parse normally.
        count['responses']+=1;count['statuses'].append(response_dict['status_code'])
    for event,fn in [('before-sign.'+service,before_sign),('before-call.'+service,before_call),('before-send.'+service,before_send),('before-parse.'+service,before_parse)]:c.meta.events.register(event,fn)
    return c,count


def native_error(call,code,status,decision=None):
    try:call()
    except ClientError as error:
        assert error.response['Error']['Code']==code and error.response['ResponseMetadata']['HTTPStatusCode']==status
        text=error.response['Error']['Message']
        assert 'sqlite'not in text.lower()and'SELECT 'not in text and'IgnoreMetricAck'not in text
        if decision:assert error.response['ResponseMetadata']['HTTPHeaders']['x-simulator-iam-decision']==decision
        return
    raise AssertionError('expected parsed native SDK error')


def access401(call,count):
    before=(count['calls'],count['sends'],count['responses'])
    caught=False
    try:call()
    except (ClientError,ResponseParserError) as error:
        caught=True
        if isinstance(error,ClientError):assert error.response['ResponseMetadata']['HTTPStatusCode']==401
    assert caught and (count['calls'],count['sends'],count['responses'])==tuple(n+1 for n in before)
    assert count['statuses'][-1]==401


def statistics(c,namespace,name,dims,start,end):
    return c.get_metric_statistics(Namespace=namespace,MetricName=name,Dimensions=dims,StartTime=start,EndTime=end,Period=60,Statistics=STATS)['Datapoints']


def run(binary,reports):
    assert importlib.metadata.version('boto3')=='1.42.0'and importlib.metadata.version('botocore')=='1.41.6'
    model=Path(botocore.__file__).parent/'data/cloudwatch/2010-08-01/service-2.json.gz'
    assert sha(model.read_bytes())==MODEL_SHA
    assert json.loads(gzip.decompress(model.read_bytes()))['metadata']['protocol']=='query'
    reports.mkdir(parents=True,exist_ok=True)
    result=dict(status='RUNNING',binarySHA256=sha(binary.read_bytes()),helperSHA256=sha(Path(__file__).read_bytes()),SDKModelSHA256=MODEL_SHA,scope='Actual two ordinary Simulator CLIs/NewAccess/nativeIAM/default SDK gzip; no fixture clock/auth',checks=[],SDKCounters=COUNTERS,controlCalls=CONTROLS)
    try:
        with tempfile.TemporaryDirectory(prefix='metrics-root-',dir='/tmp') as temporary,ExitStack() as stack:
            folder=Path(temporary)
            a,b=Team(stack,binary,folder/'alpha').start(),Team(stack,binary,folder/'beta').start()
            east,ec=client(stack,a);west,wc=client(stack,a,region='us-west-2');peer,pc=client(stack,b)
            now=datetime.now(timezone.utc);start=now.replace(second=0,microsecond=0)-timedelta(minutes=1);end=start+timedelta(minutes=3)
            namespace,name='Practice/Production','Latency'
            dims=[{'Name':'Service','Value':'dummy'},{'Name':'Shard','Value':'one'}]
            base=dict(MetricName=name,Dimensions=dims,Timestamp=now)
            for c,values in [(east,[2,0,-1,2]),(west,[42]),(peer,[99])]:c.put_metric_data(Namespace=namespace,MetricData=[dict(base,Value=v)for v in values])
            east.put_metric_data(Namespace=namespace,MetricData=[dict(base,Value=7,Unit='Count')])
            points=statistics(east,namespace,name,list(reversed(dims)),start,end);units={p['Unit']:p for p in points}
            assert len(points)==2 and set(units)=={'None','Count'}
            for key,expected in dict(SampleCount=4,Sum=3,Minimum=-1,Maximum=2,Average=.75).items():assert units['None'][key]==expected
            for key,expected in dict(SampleCount=1,Sum=7,Minimum=7,Maximum=7,Average=7).items():assert units['Count'][key]==expected
            assert statistics(east,namespace,name,dims[:1],start,end)==[]
            assert statistics(west,namespace,name,dims,start,end)[0]['Sum']==42
            assert statistics(peer,namespace,name,dims,start,end)[0]['Sum']==99
            prefixed,pfc=client(stack,a,route='/_simulator/providers/aws/')
            assert prefixed.list_metrics(Namespace=namespace)['Metrics']==east.list_metrics(Namespace=namespace)['Metrics']
            result['checks'].append('actual ordinary CLI constructors and root/provider-alias SDK routes; literalfive stats/unit/dimension/Region/twoDB data')

            iam,ic=client(stack,a,service='iam');peer_iam,pic=client(stack,b,service='iam')
            role='metrics-reader'
            trust={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Principal':{'Service':'lambda.amazonaws.com'},'Action':'sts:AssumeRole'}]}
            readonly={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':['cloudwatch:ListMetrics','cloudwatch:GetMetricStatistics'],'Resource':'*'}]}
            for c,policy in [(iam,readonly),(peer_iam,{'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'cloudwatch:*','Resource':'*'}]})]:
                created=c.create_role(RoleName=role,AssumeRolePolicyDocument=json.dumps(trust))['Role']
                assert created['Arn']=='arn:aws:iam::000000000000:role/'+role
                c.put_role_policy(RoleName=role,PolicyName='metrics',PolicyDocument=json.dumps(policy))
            reader_token=control(a,'/_simulator/v1/access',{'Principal':role},'POST')['AccessToken']
            peer_token=control(b,'/_simulator/v1/access',{'Principal':role},'POST')['AccessToken']
            reader,rc=client(stack,a,token=reader_token);selected,sc=client(stack,a,selected=role);peer_reader,prc=client(stack,b,token=peer_token)
            assert statistics(reader,namespace,name,dims,start,end)==points
            before,peer_before=frame(a.db),frame(b.db)
            native_error(lambda:reader.put_metric_data(Namespace=namespace,MetricData=[dict(base,Value=100)]),'AccessDenied',403,'implicitDeny')
            native_error(lambda:selected.put_metric_data(Namespace=namespace,MetricData=[dict(base,Value=100)]),'AccessDenied',403,'implicitDeny')
            cold,cc=client(stack,a,region='ap-northeast-1',token=reader_token)
            native_error(lambda:cold.put_metric_data(Namespace=namespace,MetricData=[dict(base,Value=100)]),'AccessDenied',403)
            cross,xc=client(stack,b,token=reader_token)
            access401(lambda:cross.list_metrics(),xc)
            assert frame(a.db)==before and frame(b.db)==peer_before
            peer_reader.put_metric_data(Namespace='Peer/Only',MetricData=[dict(MetricName='SeparateRole',Value=1)])
            peer_before=frame(b.db)
            deny={'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':'cloudwatch:*','Resource':'*'},{'Effect':'Deny','Action':'cloudwatch:PutMetricData','Resource':'*'}]}
            iam.put_role_policy(RoleName=role,PolicyName='metrics',PolicyDocument=json.dumps(deny))
            before=frame(a.db)
            native_error(lambda:reader.put_metric_data(Namespace=namespace,MetricData=[dict(base,Value=100)]),'AccessDenied',403,'explicitDeny')
            assert frame(a.db)==before and frame(b.db)==peer_before
            result['checks'].append('actualnativeIAM role/projection+issuedparticipant+operator-selectedreader; currentexplicit/implicitDeny; knownpolicy coldRegion no bootstrap; cross-token transport401; wholeDB/peerframes')

            # A real host-storage fault; NOT an ordinary API attack or AWS comparison.
            with sqlite3.connect(a.db) as db:
                db.execute("CREATE TRIGGER IgnoreMetricAck BEFORE INSERT ON aws_metrics_points WHEN EXISTS(SELECT 1 FROM aws_metrics_series s WHERE s.region=NEW.region AND s.identity=NEW.series_id AND s.name='Ignored') BEGIN SELECT RAISE(IGNORE); END")
            before=frame(a.db)
            batch=[dict(base,Value=4),dict(MetricName='Ignored',Value=5)]
            native_error(lambda:east.put_metric_data(Namespace=namespace,MetricData=batch),'InternalServiceError',500)
            assert frame(a.db)==before and frame(b.db)==peer_before
            with sqlite3.connect(a.db) as db:db.execute('DROP TRIGGER IgnoreMetricAck')
            east.put_metric_data(Namespace=namespace,MetricData=batch)
            after=statistics(east,namespace,name,dims,start,end)
            assert {p['Unit']:p['Sum']for p in after}=={'None':7,'Count':7}
            result['checks'].append('realSQLite pureIGNORE secondpoint causesSDK500/fullTxrollback/sanitizederror, actualrepair+successfulretry')

            east.put_metric_data(Namespace='Pages/Production',MetricData=[dict(MetricName='m%03d'%i,Value=i)for i in range(501)])
            first=east.list_metrics(Namespace='Pages/Production')
            assert len(first['Metrics'])==500 and first['NextToken']
            assert any(status==200 and encoding=='gzip'for status,encoding in zip(ec['statuses'],ec['encoding']))
            token=first['NextToken'];snapshot=frame(a.db,True)
            for c,options in [(east,{'Namespace':'Pages/Production','NextToken':('A'if token[0]!='A'else'B')+token[1:]}),(west,{'Namespace':'Pages/Production','NextToken':token}),(peer,{'Namespace':'Pages/Production','NextToken':token})]:native_error(lambda c=c,options=options:c.list_metrics(**options),'InvalidParameterValue',400)
            assert frame(a.db,True)==snapshot
            for c in [east,west,reader,selected,cold,prefixed]:c.close()
            original_token=a.token;a.stop();a.start();assert a.token==original_token
            east,ec2=client(stack,a)
            last=east.list_metrics(Namespace='Pages/Production',NextToken=token)
            names=[x['MetricName']for x in first['Metrics']+last['Metrics']]
            assert len(last['Metrics'])==1 and not last.get('NextToken') and len(names)==len(set(names))==501 and set(names)=={'m%03d'%i for i in range(501)}
            assert frame(a.db,True)==snapshot
            native_error(lambda:east.list_metrics(Namespace='Changed',NextToken=token),'InvalidParameterValue',400)
            first_again=east.list_metrics(Namespace='Pages/Production');live=first_again['NextToken']
            east.put_metric_data(Namespace='Pages/Production',MetricData=[dict(MetricName='m000',Value=0)])
            before=frame(a.db)
            native_error(lambda:east.list_metrics(Namespace='Pages/Production',NextToken=live),'InvalidParameterValue',400)
            assert frame(a.db)==before and frame(b.db)==peer_before
            stale_actor,sac=client(stack,a,token=reader_token);access401(lambda:stale_actor.list_metrics(),sac)
            new_token=control(a,'/_simulator/v1/access',{'Principal':role},'POST')['AccessToken']
            new_reader,nrc=client(stack,a,token=new_token)
            assert new_reader.list_metrics(Namespace=namespace)['Metrics']
            result['checks'].append('defaultSDKgzip501Put/cursorexactset/tamper/crossDB/Region/persistedHMACrestart/livewriteinvalidation; operatorauthoritypersists, participantsreissued')

            # No invented DeleteMetric: owned temporary DBs disappear after CLI
            # shutdown. IAM resources are actually deleted through their APIs.
            iam.close();iam,ic2=client(stack,a,service='iam')
            iam.delete_role_policy(RoleName=role,PolicyName='metrics');iam.delete_role(RoleName=role)
            peer_iam.delete_role_policy(RoleName=role,PolicyName='metrics');peer_iam.delete_role(RoleName=role)
            for counter in COUNTERS:assert counter['calls']==counter['sends']==counter['responses']>0
            result['checks'].append('allofficialSDKcalls=sends=actualtransportresponses; loopbackonly; nativeIAMcleanup, no fake metricdelete')
            result['status']='ASSERTIONS_PASS_CLEANUP_PENDING'
        result['status']='PASS';result['cleanup']='allclientsclosed/CLIsexited0reaped/ownedDB+tokenfilesremoved'
    except BaseException as error:
        result.update(status='FAIL',failureType=type(error).__name__)
        raise
    finally:
        primary_failed = result['status'] == 'FAIL'
        postguard_failures = []
        for label, path, expected in (
            ('binary', binary, result['binarySHA256']),
            ('helper', Path(__file__), result['helperSHA256']),
        ):
            try:
                exact = sha(path.read_bytes()) == expected
            except Exception as error:
                exact = False
                postguard_failures.append({'input':label,'type':type(error).__name__})
            else:
                if not exact:
                    postguard_failures.append({'input':label,'type':'SHA256Mismatch'})
            result[label+'PostExact'] = exact
        result['caseCounters'] = {'groups':len(result['checks']),
                                  'calls':sum(x['calls']for x in COUNTERS),
                                  'sends':sum(x['sends']for x in COUNTERS),
                                  'responses':sum(x['responses']for x in COUNTERS),
                                  'controls':len(CONTROLS)}
        if postguard_failures:
            result['status'] = 'FAIL'
            result['postGuardFailures'] = postguard_failures
            if not primary_failed:
                result['failureType'] = 'InputPostGuardFailure'
        (reports/'result.json').write_text(json.dumps(result,indent=2)+'\n')
        if postguard_failures and not primary_failed:
            raise AssertionError('Owned input postguard failed; receipt preserved')
    print(json.dumps({'status':result['status'],'checks':len(result['checks']),'calls':sum(x['calls']for x in COUNTERS),'sends':sum(x['sends']for x in COUNTERS),'responses':sum(x['responses']for x in COUNTERS),'scope':result['scope']}))


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--binary',type=Path,required=True);p.add_argument('--binary-sha256',required=True);p.add_argument('--reports',type=Path,required=True);a=p.parse_args()
    assert a.binary.is_file()and sha(a.binary.read_bytes())==a.binary_sha256
    run(a.binary.resolve(),a.reports.resolve())
