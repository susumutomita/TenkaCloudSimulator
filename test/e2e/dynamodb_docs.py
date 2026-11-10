"""Mandatory seven SERVED DynamoDB templates against the real binary and SDK."""
import json
from pathlib import Path
from route53_openapi import project_before_route53
import tempfile
import urllib.error
import urllib.parse
import urllib.request

from dynamodb import client,COUNTS,REPORTS
from sqs import Team,NoRedirect
from openapi_json10 import validate_json10_examples,validate_json10_response
from test_sakura_openapi import OLD_IDS,NEW_IDS,COLLECTION,ITEM

OPERATIONS=('CreateTable','ListTables','DescribeTable','PutItem','GetItem','DeleteItem','DeleteTable')


def validate_reference_contract(spec):
    inherited = project_before_route53(spec)
    ids=[v['operationId'] for p in inherited['paths'].values() for m,v in p.items() if m in ('get','post','put','head','delete')]
    assert len(ids)==len(set(ids))==67 and set(ids)==OLD_IDS|NEW_IDS|{'S3MultipartUpload'} and len(inherited['paths'])==36 and {COLLECTION,ITEM} <= set(inherited['paths']),'Main API paths/IDs changed'
    assert spec['paths']['/{bucket}/{key}']['post']['operationId']=='S3MultipartUpload','Multipart addition moved from the native S3 object route'


def run():
    REPORTS.mkdir(parents=True,exist_ok=True)
    counts={'calls':0,'responses':0};executed=[]
    with tempfile.TemporaryDirectory(prefix='ddb-served-reference-') as directory:
        team=Team(directory)
        try:
            team.start();opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect());origin=urllib.parse.urlsplit(team.endpoint)
            def request(path,body=None,target=None):
                assert path.startswith('/') and not path.startswith('//') and '://' not in path
                destination=team.endpoint+path
                assert urllib.parse.urlsplit(destination).netloc==origin.netloc
                headers={'X-Simulator-Token':team.token}
                if target:headers.update({'Content-Type':'application/x-amz-json-1.0','X-Amz-Target':target,'X-Simulator-Service':'dynamodb','X-Simulator-Region':'us-east-1'})
                raw=urllib.request.Request(destination,data=json.dumps(body).encode() if body is not None else None,headers=headers,method='POST' if target else 'GET')
                if target:counts['calls']+=1
                with opener.open(raw,timeout=5) as response:
                    assert response.status==200,'Served native template did not succeed'
                    if target:
                        counts['responses']+=1
                        assert response.headers['Content-Type'].startswith('application/x-amz-json-1.0')
                    result=json.load(response)
                    if target:validate_json10_response(spec,response.status,result,target.removeprefix('DynamoDB_20120810.'))
                    return result
            spec=request('/_simulator/docs/openapi.json')
            assert spec==json.loads(Path('internal/server/apidocs/openapi.json').read_text()),'Embedded/source reference mismatch'
            schema_examples=validate_json10_examples(spec)
            validate_reference_contract(spec)
            caps=request('/_simulator/v1/capabilities');assert set(caps['services']['dynamodb']['operations'])==set(OPERATIONS)
            examples=spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.0']['examples']
            sdk=client(team);put=examples['DynamoDBPutItem']['value'];name=put['TableName'];key=examples['DynamoDBGetItem']['value']['Key']
            for op in OPERATIONS:
                example=examples['DynamoDB'+op];assert example['x-simulator-target']=='DynamoDB_20120810.'+op
                result=request('/',example['value'],example['x-simulator-target']);executed.append(op)
                if op=='CreateTable':assert sdk.describe_table(TableName=name)['Table']['TableId']==result['TableDescription']['TableId']
                if op=='ListTables':assert name in result['TableNames']
                if op=='PutItem':assert sdk.get_item(TableName=name,Key=key)['Item']==put['Item']
                if op=='GetItem':assert result['Item']==put['Item']
                if op=='DeleteItem':assert 'Item' not in sdk.get_item(TableName=name,Key=key)
                if op=='DeleteTable':assert sdk.list_tables()['TableNames']==[]
            assert tuple(executed)==OPERATIONS and counts=={'calls':7,'responses':7}
            assert COUNTS['calls']==COUNTS['sends']==COUNTS['responses']>0
            report={'servedSevenTemplates':executed,'nativeHTTPCounts':counts,'SDKCounts':COUNTS,
                'sharedSQLiteConfirmed':True,'sourceEqualsServed':True,'mainOperationIDs':67,'mainPaths':36,
                'schemaValidatedSharedTemplates':schema_examples,'schemaValidatedNativeResponses':counts['responses'],
                'staticExamplesNotLiveState':True,'cleanup':'DELETED','realAWS':False,'DynamoDBConsole':'unsupported'}
            (REPORTS/'dynamodb-reference.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
        finally:team.stop()


if __name__=='__main__':run()
