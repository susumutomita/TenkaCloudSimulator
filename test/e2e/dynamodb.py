"""Mandatory real two-CLI DynamoDB SDK/state/IAM/restart acceptance; no AWS."""
import contextlib
import json
import os
from pathlib import Path
import tempfile
import urllib.error
import urllib.parse
import urllib.request

from sqs import Team, NoRedirect  # Existing real process/DB/private-token lifecycle.
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

os.environ.update(NO_PROXY='127.0.0.1,localhost',no_proxy='127.0.0.1,localhost')

OPERATIONS={'CreateTable','ListTables','DescribeTable','DeleteTable','PutItem','GetItem','DeleteItem'}
COUNTS={'calls':0,'sends':0,'responses':0}
SEEN=set()
REPORTS=Path(os.environ.get('SIMULATOR_REPORTS','reports')).resolve()


def client(team,region='us-east-1',token=None,prefixed=False):
    c=boto3.client('dynamodb',endpoint_url=team.endpoint+('/_simulator/providers/aws/' if prefixed else ''),region_name=region,
                   aws_access_key_id='TCSIMDDBLOCAL',aws_secret_access_key='dummy-local-only',
                   config=Config(retries={'max_attempts':0},connect_timeout=2,read_timeout=5,proxies={}))
    c.meta.events.register('before-sign.dynamodb',lambda request,**_:request.headers.add_header('X-Simulator-Token',team.token if token is None else token))
    def before_call(model,**_):COUNTS['calls']+=1;SEEN.add(model.name)
    def before_send(request,**_):
        url=urllib.parse.urlsplit(request.url)
        assert url.scheme=='http' and url.netloc==urllib.parse.urlsplit(team.endpoint).netloc,'SDK left the owned loopback origin'
        assert url.path in ('/','/_simulator/providers/aws/'),'Unexpected DynamoDB native route'
        COUNTS['sends']+=1
    def after_call(http_response,**_):
        COUNTS['responses']+=1
        assert not 300<=http_response.status_code<400,'Unexpected SDK redirect'
    c.meta.events.register('before-call.dynamodb',before_call)
    c.meta.events.register('before-send.dynamodb',before_send)
    c.meta.events.register('after-call.dynamodb',after_call)
    team.clients.append(c)
    return c


def reject(operation,code=None,http=None):
    try:operation()
    except ClientError as failure:
        if code:assert failure.response['Error']['Code']==code,'Unexpected native error code'
        if http:assert failure.response['ResponseMetadata']['HTTPStatusCode']==http,'Unexpected native HTTP refusal'
    else:raise AssertionError('Expected native refusal')


def control(team,path,body,method='POST'):
    assert path.startswith('/_simulator/') and not path.startswith('//')
    request=urllib.request.Request(team.endpoint+path,data=json.dumps(body).encode(),
        headers={'Content-Type':'application/json','X-Simulator-Token':team.token},method=method)
    with urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect()).open(request,timeout=5) as response:
        assert response.status==200
        return json.load(response)


def table(name,kind='N'):
    return {'TableName':name,'KeySchema':[{'AttributeName':'id','KeyType':'HASH'}],
            'AttributeDefinitions':[{'AttributeName':'id','AttributeType':kind}],'BillingMode':'PAY_PER_REQUEST'}


def run():
    REPORTS.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='ddb-two-owned-clis-') as directory:
        root=Path(directory);(root/'a').mkdir();(root/'b').mkdir();a,b=Team(root/'a'),Team(root/'b')
        try:
            a.start();b.start();assert not os.path.samefile(a.database,b.database)
            sdk,peer=client(a),client(b);name='ddb-acceptance';key={'id':{'N':'1'}}
            first=sdk.create_table(**table(name))['TableDescription']
            peer.create_table(**table(name))
            for c,value in ((sdk,'team-a'),(peer,'team-b')):
                c.put_item(TableName=name,Item={**key,'payload':{'S':value}},ConditionExpression='attribute_not_exists(id)')
            assert sdk.get_item(TableName=name,Key={'id':{'N':'1e0'}})['Item']['payload']=={'S':'team-a'}
            reject(lambda:sdk.put_item(TableName=name,Item={'id':{'N':'1.0'},'payload':{'S':'blocked'}},ConditionExpression='attribute_not_exists(id)'),code='ConditionalCheckFailedException')
            prefix=client(a,prefixed=True);assert prefix.get_item(TableName=name,Key=key)['Item']['id']=={'N':'1'}
            wrong=client(b,token=a.token);reject(lambda:wrong.list_tables(),http=401)
            reject(lambda:client(a,token=b.token).list_tables(),http=401)
            sdk.put_item(TableName=name,Item={'id':{'N':'2'},'binary':{'B':b'x'*350000}})
            sdk.put_item(TableName=name,Item={'id':{'N':'3'},'escaped':{'S':'<'*70000}})
            assert sdk.get_item(TableName=name,Key={'id':{'N':'2'}})['Item']['binary']['B']==b'x'*350000
            assert sdk.get_item(TableName=name,Key={'id':{'N':'3'}})['Item']['escaped']['S']=='<'*70000
            def snapshot(c):
                d=c.describe_table(TableName=name)['Table']
                return (d['TableId'],d['ItemCount'],d['TableSizeBytes'],c.get_item(TableName=name,Key=key)['Item'])
            before=snapshot(sdk)
            reject(lambda:sdk.put_item(TableName=name,Item={**key,'payload':{'S':'x'*409600}}),code='ValidationException')
            assert snapshot(sdk)==before,'Oversized replacement mutated native state'
            arn=first['TableArn']
            control(a,'/_simulator/v1/iam/principals/ddb-reader/policy',{'Version':'2012-10-17','Statement':[
                {'Effect':'Allow','Action':['dynamodb:GetItem','dynamodb:DescribeTable'],'Resource':arn},
                {'Effect':'Deny','Action':['dynamodb:PutItem','dynamodb:DeleteItem','dynamodb:DeleteTable'],'Resource':arn}]},'PUT')
            participant=control(a,'/_simulator/v1/access',{'principal':'ddb-reader'})['token']
            reader=client(a,token=participant)
            assert reader.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'team-a'}
            spoof=client(a,token=participant)
            spoof.meta.events.register('before-sign.dynamodb',lambda request,**_:request.headers.add_header('X-Simulator-Principal','simulator-admin'))
            reject(lambda:spoof.get_item(TableName=name,Key=key),http=403)
            reject(lambda:reader.put_item(TableName=name,Item={**key,'payload':{'S':'not-authorized'}}),code='AccessDeniedException',http=403)
            reject(lambda:reader.delete_item(TableName=name,Key=key),code='AccessDeniedException',http=403)
            reject(lambda:reader.delete_table(TableName=name),code='AccessDeniedException',http=403)
            assert snapshot(sdk)==before,'IAM refusal mutated item/statistics'
            tokyo=client(a,'ap-northeast-1');tokyo.create_table(**table(name));tokyo.put_item(TableName=name,Item={**key,'payload':{'S':'regional'}})
            assert sdk.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'team-a'}
            assert peer.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'team-b'}
            old_token=a.token;a.stop();a.start();assert a.token==old_token,'Operator token unexpectedly rotated'
            sdk,tokyo=client(a),client(a,'ap-northeast-1')
            assert snapshot(sdk)==before,'Restart changed saved table generation/data/stats'
            assert tokyo.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'regional'}
            assert sdk.get_item(TableName=name,Key={'id':{'N':'2'}})['Item']['binary']['B']==b'x'*350000
            reject(lambda:client(a,token=participant).get_item(TableName=name,Key=key),http=401)
            sdk.delete_item(TableName=name,Key={'id':{'N':'999'}})  # Genuine missing-row delete is idempotent.
            for c,keys in ((sdk,('1','2','3')),(tokyo,('1',)),(peer,('1',))):
                for k in keys:c.delete_item(TableName=name,Key={'id':{'N':k}})
                assert c.describe_table(TableName=name)['Table']['ItemCount']==0
                c.delete_table(TableName=name);assert c.list_tables()['TableNames']==[]
                if c is sdk:
                    assert peer.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'team-b'}
                    assert tokyo.get_item(TableName=name,Key=key)['Item']['payload']=={'S':'regional'}
            assert SEEN==OPERATIONS and COUNTS['calls']==COUNTS['sends']==COUNTS['responses']>0
            report={'nativeSevenOperations':sorted(SEEN),'twoAuthenticatedProcesses':True,'twoSQLiteDatabases':True,
                'sameTableNameDifferentBytes':True,'numericAliasAndConditionalAtomicity':True,'providerPrefix':True,
                'decodedBinary350000AndEscapedString70000':True,'oversizeRefusalFrame':True,'IAMReadAllowedWrites403Frame':True,
                'crossToken401':True,'regionSeparation':True,'restartGenerationDataStats':True,'oldParticipant401':True,
                'SDKCounts':COUNTS,'cleanup':'DELETED','realAWS':False,'DynamoDBConsole':'unsupported'}
            (REPORTS/'dynamodb-sdk.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
        finally:
            with contextlib.ExitStack() as cleanup:
                cleanup.callback(a.stop);cleanup.callback(b.stop)


if __name__=='__main__':run()
