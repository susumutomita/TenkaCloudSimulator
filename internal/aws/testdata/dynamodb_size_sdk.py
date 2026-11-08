"""Opt-in developer verification using an existing boto3, loopback endpoint only."""
import json
import sys
from urllib.parse import urlparse
import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

endpoint = sys.argv[1]
assert urlparse(endpoint).hostname == '127.0.0.1'
c = boto3.client('dynamodb', endpoint_url=endpoint, region_name='us-east-1',
                 aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='local-dummy',
                 config=Config(retries={'max_attempts': 0}, proxies={}))
c.create_table(TableName='sizes', AttributeDefinitions=[{'AttributeName':'id','AttributeType':'S'}],
               KeySchema=[{'AttributeName':'id','KeyType':'HASH'}], BillingMode='PAY_PER_REQUEST')
for kind, value, expected in [('B',bytes(350000),350010),('S','<'*70000,70010)]:
    item = {'id':{'S':'k'},'payload':{kind:value}}
    c.put_item(TableName='sizes',Item=item)
    assert c.get_item(TableName='sizes',Key={'id':{'S':'k'}})['Item'] == item
    table = c.describe_table(TableName='sizes')['Table']
    assert (table['ItemCount'],table['TableSizeBytes']) == (1,expected)
    too_big = {'id':{'S':'k'},'payload':{kind:bytes(409591) if kind=='B' else 'x'*409591}}
    try:
        c.put_item(TableName='sizes',Item=too_big)
        raise AssertionError('oversize accepted')
    except ClientError as e:
        assert e.response['Error']['Code'] == 'ValidationException'
    assert c.get_item(TableName='sizes',Key={'id':{'S':'k'}})['Item'] == item
    assert c.describe_table(TableName='sizes')['Table']['TableSizeBytes'] == expected
c.delete_table(TableName='sizes')
print(json.dumps({'sdk':'boto3','realHTTP':True,'binaryRawBytes':350000,'escapedStringBytes':70000,
                  'oversizeRejected':True,'failedWriteUnchanged':True,'actualAWS':False}))
