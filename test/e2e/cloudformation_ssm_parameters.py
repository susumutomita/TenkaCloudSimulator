"""Actual two-process CFN local SSM snapshot, replay, restart and cleanup."""
import json
from pathlib import Path
import tempfile
import urllib.request
from botocore.exceptions import ClientError
from check import running, boto3, Config, REPORTS

TEMPLATE = '''Parameters:
  Source:
    Type: AWS::SSM::Parameter::Value<String>
    Default: /practice/source
    AllowedValues: [/practice/source]
  Image:
    Type: AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>
    Default: /practice/image
Resources:
  Copy:
    Type: AWS::SSM::Parameter
    Properties: {Name: /practice/copy, Type: String, Value: !Ref Source}
Outputs:
  Source: {Value: !Ref Source}
  Image: {Value: !Ref Image}
'''


def client(service, endpoint, token, region='ap-northeast-1', principal=None):
    native = boto3.client(service, endpoint_url=endpoint, region_name=region,
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
        config=Config(retries={'max_attempts': 0}))
    def headers(request, **kwargs):
        request.headers.add_header('X-Simulator-Token', token)
        if principal:
            request.headers.add_header('X-Simulator-Principal', principal)
    native.meta.events.register('before-sign.' + service, headers)
    return native


def rejected(call, code):
    try:
        call()
    except ClientError as error:
        assert error.response['Error']['Code'] == code, error.response
    else:
        raise AssertionError('unsupported or unauthorized request succeeded')


def control(endpoint, token, path, data):
    request = urllib.request.Request(endpoint+path, data=json.dumps(data).encode(),
        headers={'Content-Type': 'application/json', 'X-Simulator-Token': token},
        method='POST' if path.endswith('/access') else 'PUT')
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)


def run_lifecycle():
    with tempfile.TemporaryDirectory(prefix='cfn-ssm-values-') as directory:
        state = Path(directory) / 'one.db'
        ids = []
        with running(state, authenticated=True) as (one, token_one), running(authenticated=True) as (two, token_two):
            for endpoint, token, value in ((one, token_one, 'one <&> 雪'), (two, token_two, 'two')):
                ssm, cf = [client(service, endpoint, token) for service in ('ssm', 'cloudformation')]
                ssm.put_parameter(Name='/practice/source', Type='String', Value=value)
                ssm.put_parameter(Name='/practice/image', Type='String', Value='ami-0123456789abcdef0')
                wrong = client('cloudformation', endpoint, token, 'eu-west-1')
                rejected(lambda: wrong.create_stack(StackName='snapshot', TemplateBody=TEMPLATE), 'ValidationError')
                assert wrong.describe_stacks()['Stacks'] == []
                policy = {'Version': '2012-10-17', 'Statement': [
                    {'Effect': 'Allow', 'Action': '*', 'Resource': '*'},
                    {'Effect': 'Deny', 'Action': 'ssm:GetParameters',
                     'Resource': 'arn:aws:ssm:ap-northeast-1:000000000000:parameter/practice/source'}]}
                control(endpoint, token, '/_simulator/v1/iam/principals/limited/policy', policy)
                participant = control(endpoint, token, '/_simulator/v1/access', {'Principal': 'limited'})['AccessToken']
                limited = client('cloudformation', endpoint, participant)
                rejected(lambda: limited.create_stack(StackName='snapshot', TemplateBody=TEMPLATE), 'AccessDenied')
                assert cf.describe_stacks()['Stacks'] == []
                created = cf.create_stack(StackName='snapshot', TemplateBody=TEMPLATE, ClientRequestToken='snapshot-token')
                ids.append(created['StackId'])
                stack = cf.describe_stacks(StackName='snapshot')['Stacks'][0]
                params = {p['ParameterKey']: p for p in stack['Parameters']}
                assert params['Source']['ParameterValue'] == '/practice/source'
                assert params['Source']['ResolvedValue'] == value
                assert ssm.get_parameter(Name='/practice/copy')['Parameter']['Value'] == value
                ssm.put_parameter(Name='/practice/source', Type='String', Value='new', Overwrite=True)
                ssm.delete_parameter(Name='/practice/image')
                retried = cf.create_stack(StackName='snapshot', TemplateBody=TEMPLATE, ClientRequestToken='snapshot-token')
                assert retried['StackId'] == created['StackId']
                assert limited.create_stack(StackName='snapshot', TemplateBody=TEMPLATE, ClientRequestToken='snapshot-token')['StackId'] == created['StackId']
                assert cf.describe_stacks(StackName='snapshot')['Stacks'][0]['Parameters'] == stack['Parameters']
                rejected(lambda: cf.create_stack(StackName='snapshot', TemplateBody=TEMPLATE+'\n# changed', ClientRequestToken='snapshot-token'), 'ValidationError')
            assert ids[0] != ids[1]
            peer = client('cloudformation', two, token_two)
            peer.delete_stack(StackName='snapshot')
            assert peer.describe_stacks()['Stacks'] == []
            assert client('ssm', two, token_two).get_parameter(Name='/practice/source')['Parameter']['Value'] == 'new'
            assert client('cloudformation', one, token_one).describe_stacks(StackName='snapshot')['Stacks'][0]['StackId'] == ids[0]
        with running(state, authenticated=True) as (endpoint, token):
            cf, ssm = [client(service, endpoint, token) for service in ('cloudformation', 'ssm')]
            stack = cf.describe_stacks(StackName='snapshot')['Stacks'][0]
            assert stack['StackId'] == ids[0]
            params = {p['ParameterKey']: p for p in stack['Parameters']}
            assert params['Source']['ResolvedValue'] == 'one <&> 雪'
            assert params['Image']['ResolvedValue'] == 'ami-0123456789abcdef0'
            assert cf.create_stack(StackName='snapshot', TemplateBody=TEMPLATE, ClientRequestToken='snapshot-token')['StackId'] == ids[0]
            cf.delete_stack(StackName='snapshot')
            assert cf.describe_stacks()['Stacks'] == []
            assert ssm.get_parameter(Name='/practice/source')['Parameter']['Value'] == 'new'
            rejected(lambda: ssm.get_parameter(Name='/practice/copy'), 'ParameterNotFound')
            # Source deletion is an error for a fresh creation, not for replay.
            rejected(lambda: cf.create_stack(StackName='snapshot', TemplateBody=TEMPLATE), 'ValidationError')
            assert cf.describe_stacks()['Stacks'] == []
    result = {'twoStandaloneProcesses': True, 'sourceKeyAndResolvedValueDistinct': True,
        'regionIsolation': True, 'participantSourceDenial': True, 'snapshotPersistsRestart': True,
        'tokenReplayAfterSourceDeletion': True, 'nativeCleanupPreservesSources': True,
        'realAWSAMIOrVMExecution': False, 'fullOriginalBattle': False}
    (REPORTS / 'cloudformation-ssm-parameters.json').write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(result))


if __name__ == '__main__':
    run_lifecycle()
