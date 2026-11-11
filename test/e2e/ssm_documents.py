"""Native authenticated SSM definition SDK acceptance, without document execution."""
import json
import subprocess
import sys
import tempfile
import urllib.request
from test_ssm_document_reference import validate
from pathlib import Path
from check import running, boto3, Config, ClientError

ROOT = Path(__file__).resolve().parents[2]
CONTENT = json.dumps({'schemaVersion':'2.2','mainSteps':[{'action':'aws:runShellScript','name':'literal','inputs':{'runCommand':['echo retained-not-executed']}}]})

def client(url, token, region='us-east-1'):
    c = boto3.client('ssm', endpoint_url=url, region_name=region, aws_access_key_id='TCSIMDOCUMENT', aws_secret_access_key='dummy', config=Config(retries={'max_attempts':0}))
    c.meta.events.register('before-send.ssm', lambda request, **kwargs: request.headers.__setitem__('X-Simulator-Token', token))
    return c

def absent(c):
    try:
        c.get_document(Name='local-persisted')
    except ClientError as e:
        assert e.response['Error']['Code'] == 'InvalidDocument'
    else:
        raise AssertionError('Foreign definition unexpectedly visible')

def main():
    with tempfile.TemporaryDirectory(prefix='ssm-document-sdk-') as directory:
        db = Path(directory)/'state.db'
        with running(db, authenticated=True) as (url, token), running(authenticated=True) as (peer, peer_token):
            subprocess.run([sys.executable, str(ROOT/'test/e2e/ssm_documents_sdk.py'), '--endpoint',url,'--token',token],check=True,timeout=90)
            with urllib.request.urlopen(urllib.request.Request(url+'/_simulator/docs/openapi.json',headers={'X-Simulator-Token':token})) as response:
                validate(json.load(response))
            c=client(url,token);c.create_document(Name='local-persisted',Content=CONTENT)
            absent(client(peer,peer_token));absent(client(url,token,'us-west-2'))
            assert client(peer,peer_token).list_documents()['DocumentIdentifiers']==[]
        with running(db, authenticated=True) as (url, token):
            c=client(url,token)
            assert c.get_document(Name='local-persisted')['Content']==CONTENT
            c.delete_document(Name='local-persisted');absent(c)
    print('SSM documents: six SDK operations, authenticated process/region isolation and restart passed; no document execution')

if __name__=='__main__':main()
