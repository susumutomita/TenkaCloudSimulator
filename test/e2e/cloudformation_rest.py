"""Actual binary/SDK CFN REST→saved-role Lambda→SSM lifecycle in two teams."""
import json
from pathlib import Path
import tempfile
import urllib.request
import urllib.error
from check import running, boto3, Config, REPORTS


def run_lifecycle():
    template = Path('examples/cloudformation-rest-lambda.json').read_text()
    def client(service, endpoint, token):
        native = boto3.client(service, endpoint_url=endpoint, region_name='us-east-1',
            aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
            config=Config(retries={'max_attempts': 0}))
        native.meta.events.register('before-sign.' + service,
            lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', token))
        return native
    def http(endpoint, token, path):
        request = urllib.request.Request(endpoint + path, headers={'X-Simulator-Token': token})
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                assert response.headers['Content-Type'] == 'application/octet-stream'
                return response.status, json.load(response)
        except urllib.error.HTTPError as error:
            return error.code, json.loads(error.read())
    with tempfile.TemporaryDirectory(prefix='cfn-rest-teams-') as directory:
        state = Path(directory) / 'team-one.db'
        with running(state, authenticated=True) as (one, token_one), running(authenticated=True) as (two, token_two):
            teams = []
            for endpoint, token, value in [(one, token_one, 'team-one'), (two, token_two, 'team-two')]:
                cf, api, functions, ssm = [client(service, endpoint, token) for service in ['cloudformation', 'apigateway', 'lambda', 'ssm']]
                ssm.put_parameter(Name='/private/dummy', Type='String', Value='private-local-dummy')
                cf.create_stack(StackName='rest-practice', TemplateBody=template,
                    Parameters=[{'ParameterKey': 'Value', 'ParameterValue': value}], Capabilities=['CAPABILITY_IAM'])
                record = cf.describe_stacks(StackName='rest-practice')['Stacks'][0]
                outputs = {item['OutputKey']: item['OutputValue'] for item in record['Outputs']}
                assert record['StackStatus'] == 'CREATE_COMPLETE'
                assert api.get_stage(restApiId=outputs['ApiId'], stageName='prod')['deploymentId']
                policy = json.loads(functions.get_policy(FunctionName='cfn-rest-app')['Policy'])
                assert policy['Statement'][0]['Principal']['Service'] == 'apigateway.amazonaws.com'
                status, body = http(endpoint, token, outputs['LocalPath'])
                assert status == 200 and body == {'value': value, 'path': '/config'}, (status, body)
                status, body = http(endpoint, token, outputs['LocalPath'] + '?name=/private/dummy')
                assert status == 403 and body['code'] == 'AccessDeniedException', (status, body)
                assert 'private-local-dummy' not in json.dumps(body)
                teams.append((cf, api, functions, ssm, outputs))
            assert teams[0][-1]['ApiId'] != teams[1][-1]['ApiId']
            assert http(two, token_one, teams[1][-1]['LocalPath'])[0] == 401
            cf, api, functions, ssm, outputs = teams[1]
            cf.delete_stack(StackName='rest-practice')
            assert api.get_rest_apis()['items'] == [] and functions.list_functions()['Functions'] == []
            assert cf.describe_stacks()['Stacks'] == []
            status, body = http(one, token_one, teams[0][-1]['LocalPath'])
            assert status == 200 and body['value'] == 'team-one'
            saved_path = teams[0][-1]['LocalPath']
        # A new token authority and process read the persisted deployment and grant.
        with running(state, authenticated=True) as (endpoint, token):
            assert http(endpoint, token, saved_path)[1]['value'] == 'team-one'
            cf, api, functions, ssm = [client(service, endpoint, token) for service in ['cloudformation', 'apigateway', 'lambda', 'ssm']]
            cf.delete_stack(StackName='rest-practice')
            assert api.get_rest_apis()['items'] == [] and functions.list_functions()['Functions'] == []
            assert cf.describe_stacks()['Stacks'] == []
            assert ssm.get_parameter(Name='/private/dummy')['Parameter']['Value'] == 'private-local-dummy'
            assert not any(item['Name'] == '/cfn-rest/public' for item in ssm.describe_parameters()['Parameters'])
    result = {'twoTeamCFNRESTLifecycle': True, 'actualHTTPSavedRoleSSM': True,
        'privateSSMDenied': True, 'crossTeamTokenDenied': True, 'restartPersistsSnapshotAndGrant': True,
        'nativeCleanup': 'DELETED', 'unrelatedSSMPreserved': True, 'fullProblemGrading': False}
    (REPORTS / 'cloudformation-rest-sdk.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result))

if __name__ == '__main__':
    run_lifecycle()
