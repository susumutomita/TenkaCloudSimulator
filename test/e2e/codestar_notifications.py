"""Check CodeCommit branch changes through the native notifications, SNS and SQS services.

The default uses pinned boto3 on a neutral host. --native sends the documented
protocols directly for local development. All requests stay on numeric loopback.
"""
import argparse
import base64
from contextlib import ExitStack, closing
from http.client import HTTPConnection
from importlib.metadata import version
import json
import os
from pathlib import Path
import tempfile
import time
from urllib.parse import urlencode, urlsplit
from xml.etree import ElementTree

from sts_session_lifecycle import LocalSimulator

CREATED = 'codecommit-repository-branches-and-tags-created'
UPDATED = 'codecommit-repository-branches-and-tags-updated'
REGION = 'us-east-1'
ACCOUNT = '000000000000'


def control(team, method, path, data):
    with closing(HTTPConnection('127.0.0.1', team.port, timeout=8)) as conn:
        conn.request(method, path, json.dumps(data), {'X-Simulator-Token':team.token, 'Content-Type':'application/json'})
        res = conn.getresponse()
        assert 200 <= res.status < 300, (method, path, res.status)
        return json.loads(res.read())


class Refusal(Exception):
    def __init__(self, code, status):
        super().__init__(code)
        self.code, self.status = code, status


def clients(team, token, stack, native, receipt, region=REGION):
    if not native:
        import boto3
        from botocore.config import Config
        from botocore.exceptions import ClientError
        services = {}
        def request(service, operation, **params):
            if service not in services:
                c = boto3.client(service, region_name=region, endpoint_url=team.endpoint,
                    aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='local-fixture-only',
                    config=Config(proxies={},retries={'max_attempts':0},connect_timeout=3,read_timeout=6))
                stack.callback(c.close)
                c.meta.events.register('before-sign', lambda request, **_:request.headers.add_header('X-Simulator-Token', token))
                def bound(request, **_):
                    endpoint = urlsplit(request.url)
                    assert (endpoint.scheme,endpoint.hostname,endpoint.port)==('http','127.0.0.1',team.port)
                    assert ('/'+region+'/'+service+'/aws4_request').encode() in request.headers['Authorization']
                    receipt['sends'] += 1
                c.meta.events.register('before-send',bound)
                c.meta.events.register('after-call',lambda **_:receipt.update(replies=receipt['replies']+1))
                services[service]=c
            c=services[service]
            name=next(k for k,v in c.meta.method_to_api_mapping.items() if v==operation)
            try:
                return getattr(c,name)(**params)
            except ClientError as err:
                raise Refusal(err.response['Error']['Code'],err.response['ResponseMetadata']['HTTPStatusCode']) from err
        return request
    def direct(service,operation,**params):
        headers={'X-Simulator-Token':token,'X-Simulator-Service':service,'X-Simulator-Region':region}
        path='/'
        if service=='sns':
            headers['Content-Type']='application/x-www-form-urlencoded'
            data=urlencode({'Action':operation,'Version':'2010-03-31',**params})
        else:
            headers['Content-Type']='application/json'
            if service=='codestar-notifications':
                path='/'+operation[0].lower()+operation[1:]
            else:
                is_git=service=='codecommit'
                headers['Content-Type']='application/x-amz-json-1.'+('1' if is_git else '0')
                headers['X-Amz-Target']=('CodeCommit_20150413.' if is_git else 'AmazonSQS.')+operation
            data=json.dumps(params,default=lambda b:base64.b64encode(b).decode())
        with closing(HTTPConnection('127.0.0.1',team.port,timeout=8)) as conn:
            conn.request('POST',path,data,headers)
            res=conn.getresponse();body=res.read();receipt['sends']+=1;receipt['replies']+=1
            if service=='sns':
                doc=ElementTree.fromstring(body)
                if not 200<=res.status<300:raise Refusal(doc.findtext('.//{*}Code'),res.status)
                return {x.tag.split('}')[-1]:x.text for x in doc.iter() if not len(x)}
            result=json.loads(body) if body else {}
            if not 200<=res.status<300:raise Refusal(result.get('__type',result.get('code')),res.status)
            return result
    return direct


def expect(code, fn, *args, **kwargs):
    try: fn(*args, **kwargs)
    except Refusal as err: assert err.code==code,(code,err.code)
    else: raise AssertionError('Expected '+code)


def exercise(binary,native):
    if not native:
        assert not any(k.startswith(('AWS_', 'BOTO_')) for k in os.environ)
        assert version('botocore')=='1.41.6' and version('boto3')=='1.42.0'
    receipt={'stockSDK':not native,'sends':0,'replies':0,'delivered':[]}
    cs='codestar-notifications'
    with tempfile.TemporaryDirectory(prefix='codecommit-notifications-') as tmp,ExitStack() as stack:
        team, peer = [LocalSimulator(Path(tmp)/name,binary) for name in ('alpha','peer')]
        for t in (team,peer):
            stack.callback(t.close);t.start()
        owner=clients(team,team.token,stack,native,receipt);other=clients(peer,peer.token,stack,native,receipt)
        repo='practice'
        metadata=owner('codecommit','CreateRepository',repositoryName=repo)['repositoryMetadata']
        peer_meta=other('codecommit','CreateRepository',repositoryName=repo)['repositoryMetadata']
        assert peer_meta['Arn']==metadata['Arn'] and peer_meta['repositoryId']!=metadata['repositoryId']
        source=metadata['Arn']
        topic=owner('sns','CreateTopic',Name='branch-events')['TopicArn']
        url=owner('sqs','CreateQueue',QueueName='branch-events')['QueueUrl']
        queue=owner('sqs','GetQueueAttributes',QueueUrl=url,AttributeNames=['QueueArn'])['Attributes']['QueueArn']
        sub=owner('sns','Subscribe',TopicArn=topic,Protocol='sqs',Endpoint=queue)['SubscriptionArn']
        common=dict(Name='Branches',EventTypeIds=[CREATED,UPDATED],Resource=source,Targets=[{'TargetType':'SNS','TargetAddress':topic}],DetailType='BASIC',ClientRequestToken='local-repeat')
        expect('ConfigurationException',owner,cs,'CreateNotificationRule',**common)
        def policy(effect):
            owner('sns','SetTopicAttributes',TopicArn=topic,AttributeName='Policy',AttributeValue=json.dumps({'Version':'2012-10-17','Statement':[{'Effect':effect,'Principal':{'Service':'codestar-notifications.amazonaws.com'},'Action':'sns:Publish','Resource':topic}]}))
        policy('Allow')
        def participant(name,actions,resource):
            control(team,'PUT','/_simulator/v1/iam/principals/'+name+'/policy',{'Version':'2012-10-17','Statement':[{'Effect':'Allow','Action':actions,'Resource':resource}]})
            issued=control(team,'POST','/_simulator/v1/access',{'Principal':name})['AccessToken']
            return clients(team,issued,stack,native,receipt)
        writer=participant('content-writer',['codecommit:PutFile','codecommit:CreateBranch','codecommit:GetBranch'],source)
        expect('AccessDeniedException',writer,cs,'CreateNotificationRule',**common)
        rule=owner(cs,'CreateNotificationRule',**common)['Arn']
        assert owner(cs,'CreateNotificationRule',**common)['Arn']==rule
        expect('ValidationException',owner,cs,'CreateNotificationRule',**{**common,'Name':'Different'})
        assert [r['Arn'] for r in owner(cs,'ListNotificationRules',Filters=[{'Name':'RESOURCE','Value':source}])['NotificationRules']]==[rule]
        record=owner(cs,'DescribeNotificationRule',Arn=rule)
        assert record['Resource']==source and record['Targets']==[{'TargetType':'SNS','TargetAddress':topic,'TargetStatus':'ACTIVE'}]
        assert {e['EventTypeId'] for e in record['EventTypes']}=={CREATED,UPDATED}
        assert other(cs,'ListNotificationRules')['NotificationRules']==[]
        expect('ResourceNotFoundException',other,cs,'DescribeNotificationRule',Arn=rule)

        def put(data,parent=None):
            inp=dict(repositoryName=repo,branchName='main',filePath='note.txt',fileContent=data)
            if parent:inp['parentCommitId']=parent
            return writer('codecommit','PutFile',**inp)['commitId']
        def receive(name,commit,old=None):
            until=time.monotonic()+18
            messages=[]
            while not messages and time.monotonic()<until:
                messages=owner('sqs','ReceiveMessage',QueueUrl=url,MaxNumberOfMessages=10).get('Messages',[])
                if not messages:time.sleep(0.2)
            assert len(messages)==1,(name,len(messages))
            item=messages[0];outer=json.loads(item['Body']);assert outer['TopicArn']==topic
            event=json.loads(outer['Message'])
            assert event['notificationRuleArn']==rule and event['source']=='aws.codecommit' and event['resources']==[source]
            assert event['region']==REGION and event['account']==ACCOUNT
            detail=event['detail'];assert detail['repositoryId']==metadata['repositoryId'] and detail['referenceName']==name
            assert detail['referenceFullName']=='refs/heads/'+name and detail['commitId']==commit and detail.get('oldCommitId')==old
            assert detail['event']==('referenceUpdated' if old else 'referenceCreated')
            owner('sqs','DeleteMessage',QueueUrl=url,ReceiptHandle=item['ReceiptHandle'])
            receipt['delivered'].append(detail['event'])
        first=put(b'one')
        receive('main',first)
        policy('Deny')
        second=put(b'two',first)
        assert writer('codecommit','GetBranch',repositoryName=repo,branchName='main')['branch']['commitId']==second
        time.sleep(1.4)
        assert not owner('sqs','ReceiveMessage',QueueUrl=url).get('Messages')
        team.close();team.start()
        policy('Allow');receive('main',second,first)
        writer('codecommit','CreateBranch',repositoryName=repo,branchName='review',commitId=first)
        receive('review',first)
        owner(cs,'UpdateNotificationRule',Arn=rule,Status='DISABLED')
        assert owner(cs,'DescribeNotificationRule',Arn=rule)['Status']=='DISABLED'
        put(b'three',second)
        time.sleep(1.4)
        assert not owner('sqs','ReceiveMessage',QueueUrl=url).get('Messages')
        assert owner(cs,'DeleteNotificationRule',Arn=rule)['Arn']==rule
        assert not owner(cs,'ListNotificationRules')['NotificationRules']
        owner('sns','Unsubscribe',SubscriptionArn=sub)
        owner('sqs','DeleteQueue',QueueUrl=url)
        owner('sns','DeleteTopic',TopicArn=topic)
        owner('codecommit','DeleteRepository',repositoryName=repo)
        other('codecommit','DeleteRepository',repositoryName=repo)
    assert not Path(tmp).exists() and receipt['sends']==receipt['replies']
    print(json.dumps({**receipt,'restart':True,'twoTeams':True,'cleanup':True}))


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--native',action='store_true')
    exercise(Path(os.environ['SIMULATOR_BINARY']).resolve(strict=True),parser.parse_args().native)
