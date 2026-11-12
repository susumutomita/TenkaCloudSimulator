"""Auto Scaling Plans native SDK acceptance against authenticated local CLI processes.

Uses the official boto3 autoscaling-plans and dynamodb clients with dummy TCSIM
credentials and loopback endpoints only. Covers DynamoDB capacity effects,
served reference templates, participant IAM, two-team isolation, actual CLI
reopen and teardown. No AWS account or network is contacted.
"""
import json
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

from autoscaling_plans_openapi import JSON11, TARGET, validate_autoscaling_plans
from check import running, boto3, Config, ClientError
from openapi_json10 import validator

TABLE = 'autoscaling-practice'
READ, WRITE = 'dynamodb:table:ReadCapacityUnits', 'dynamodb:table:WriteCapacityUnits'


def client(service, endpoint, token):
    c = boto3.client(service, endpoint_url=endpoint, region_name='us-east-1',
        aws_access_key_id='TCSIMLOCAL', aws_secret_access_key='dummy',
        config=Config(retries={'max_attempts': 0}))
    c.meta.events.register('before-sign.' + c.meta.service_model.service_name,
        lambda request, **kwargs: request.headers.add_header('X-Simulator-Token', token))
    def local_only(request, **kwargs):
        assert request.url.startswith(endpoint + '/'), 'SDK escaped the owned local endpoint'
    c.meta.events.register('before-send.' + c.meta.service_model.service_name, local_only)
    return c


def error(code, call):
    try:
        call()
    except ClientError as exc:
        assert exc.response['Error']['Code'] == code, exc.response
        return exc.response
    raise AssertionError('Expected ' + code)


def control(endpoint, token, path, body):
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(),
        headers={'Content-Type': 'application/json', 'X-Simulator-Token': token},
        method='POST' if path.endswith('/access') else 'PUT')
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def capacity(dynamodb, name=TABLE):
    throughput = dynamodb.describe_table(TableName=name)['Table']['ProvisionedThroughput']
    return throughput['ReadCapacityUnits'], throughput['WriteCapacityUnits']


def provisioned(dynamodb, name, read, write):
    dynamodb.create_table(TableName=name, AttributeDefinitions=[{'AttributeName': 'id', 'AttributeType': 'S'}],
        KeySchema=[{'AttributeName': 'id', 'KeyType': 'HASH'}], BillingMode='PROVISIONED',
        ProvisionedThroughput={'ReadCapacityUnits': read, 'WriteCapacityUnits': write})


def instruction(table, dimension, low, high, **extra):
    metric = 'DynamoDBReadCapacityUtilization' if dimension == READ else 'DynamoDBWriteCapacityUtilization'
    return {'ServiceNamespace': 'dynamodb', 'ResourceId': 'table/' + table, 'ScalableDimension': dimension,
            'MinCapacity': low, 'MaxCapacity': high,
            'TargetTrackingConfigurations': [{'PredefinedScalingMetricSpecification': {'PredefinedScalingMetricType': metric}, 'TargetValue': 70.0}], **extra}


def served_reference(endpoint, token):
    """Send every served AutoScaling template through the real shared root."""
    with urllib.request.urlopen(urllib.request.Request(endpoint + '/_simulator/docs/openapi.json',
            headers={'X-Simulator-Token': token}), timeout=5) as response:
        spec = json.load(response)
    source = json.loads((Path(__file__).resolve().parents[2] / 'internal/server/apidocs/openapi.json').read_text())
    assert spec == source, 'Served reference differs from the embedded source'
    validate_autoscaling_plans(spec)
    root = spec['paths']['/']['post']
    examples = root['requestBody']['content'][JSON11]['examples']
    mapping = root['x-simulator-autoscaling-plans-operation-schemas']
    executed = []
    for operation in ('CreateScalingPlan', 'DescribeScalingPlans', 'DescribeScalingPlanResources', 'UpdateScalingPlan', 'DeleteScalingPlan'):
        body = examples['AutoScaling' + operation]['value']
        request = urllib.request.Request(endpoint + '/', data=json.dumps(body).encode(), method='POST', headers={
            'Content-Type': 'application/x-amz-json-1.1', 'X-Amz-Target': TARGET + operation, 'X-Simulator-Token': token})
        with urllib.request.urlopen(request, timeout=5) as response:
            assert response.status == 200
            value = json.load(response)
        validator(spec, {'$ref': mapping[TARGET + operation]['response']}).validate(value)
        executed.append((operation, value))
    return executed


with tempfile.TemporaryDirectory(prefix='autoscaling-plans-sdk-') as directory:
    first_db = Path(directory) / 'first.db'
    with running(db=first_db, authenticated=True) as (first, token), \
         running(db=Path(directory) / 'second.db', authenticated=True) as (second, token2):
        plans, dynamodb = client('autoscaling-plans', first, token), client('dynamodb', first, token)
        other_plans, other_dynamodb = client('autoscaling-plans', second, token2), client('dynamodb', second, token2)
        caps = json.load(urllib.request.urlopen(urllib.request.Request(first + '/_simulator/v1/capabilities', headers={'X-Simulator-Token': token}), timeout=5))
        assert caps['services']['autoscaling-plans']['operations'] == ['CreateScalingPlan', 'DescribeScalingPlans', 'DescribeScalingPlanResources', 'UpdateScalingPlan', 'DeleteScalingPlan']

        # Served templates run in documented order against a PROVISIONED table.
        provisioned(dynamodb, TABLE, 1, 3)
        executed = served_reference(first, token)
        assert executed[0][1] == {'ScalingPlanVersion': 1}
        assert executed[1][1]['ScalingPlans'][0]['ScalingPlanName'] == 'practice-plan'
        assert executed[2][1]['ScalingPlanResources'][0]['ScalingStatusCode'] == 'PartiallyActive'
        assert capacity(dynamodb) == (8, 3), 'Served update/delete templates did not leave the documented capacity'
        assert plans.describe_scaling_plans()['ScalingPlans'] == []

        # SDK create bounds both dimensions inside one transaction.
        source = {'TagFilters': [{'Key': 'app', 'Values': ['shop']}]}
        created = plans.create_scaling_plan(ScalingPlanName='shop', ApplicationSource=source, ScalingInstructions=[
            instruction(TABLE, READ, 10, 20), instruction(TABLE, WRITE, 1, 2, ScalingPolicyUpdateBehavior='KeepExternalPolicies')])
        assert created['ScalingPlanVersion'] == 1
        assert capacity(dynamodb) == (10, 2)
        plan = plans.describe_scaling_plans(ScalingPlanNames=['shop'], ScalingPlanVersion=1)['ScalingPlans'][0]
        assert plan['StatusCode'] == 'ActiveWithProblems' and plan['CreationTime'].year >= 2026
        assert [i['ScalableDimension'] for i in plan['ScalingInstructions']] == [READ, WRITE]
        resources = plans.describe_scaling_plan_resources(ScalingPlanName='shop', ScalingPlanVersion=1)['ScalingPlanResources']
        assert [r['ScalingStatusCode'] for r in resources] == ['PartiallyActive', 'PartiallyActive']
        assert resources[0]['ScalingPolicies'][0]['PolicyType'] == 'TargetTrackingScaling'
        pages = list(plans.get_paginator('describe_scaling_plan_resources').paginate(ScalingPlanName='shop', ScalingPlanVersion=1, PaginationConfig={'PageSize': 1}))
        assert [len(page['ScalingPlanResources']) for page in pages] == [1, 1]

        # Failures roll back every write and name the stated rule.
        dynamodb.create_table(TableName='ondemand-orders', AttributeDefinitions=[{'AttributeName': 'id', 'AttributeType': 'S'}],
            KeySchema=[{'AttributeName': 'id', 'KeyType': 'HASH'}], BillingMode='PAY_PER_REQUEST')
        failure = error('ValidationException', lambda: plans.update_scaling_plan(ScalingPlanName='shop', ScalingPlanVersion=1,
            ScalingInstructions=[instruction(TABLE, READ, 15, 15), instruction('ondemand-orders', READ, 1, 2)]))
        assert 'on-demand' in failure['Error']['Message']
        assert capacity(dynamodb) == (10, 2)
        error('ValidationException', lambda: plans.create_scaling_plan(ScalingPlanName='second', ApplicationSource={'TagFilters': [{'Key': 'app'}]},
            ScalingInstructions=[instruction(TABLE, READ, 1, 2)]))
        error('ValidationException', lambda: plans.create_scaling_plan(ScalingPlanName='second', ApplicationSource=source,
            ScalingInstructions=[instruction('missing-table', READ, 1, 2)]))
        error('ValidationException', lambda: plans.create_scaling_plan(ScalingPlanName='asg', ApplicationSource={'TagFilters': [{'Key': 'asg'}]},
            ScalingInstructions=[{**instruction(TABLE, READ, 1, 2), 'ServiceNamespace': 'autoscaling', 'ResourceId': 'autoScalingGroup/web', 'ScalableDimension': 'autoscaling:autoScalingGroup:DesiredCapacity'}]))
        error('UnknownOperationException', lambda: plans.get_scaling_plan_resource_forecast_data(ScalingPlanName='shop', ScalingPlanVersion=1,
            ServiceNamespace='autoscaling', ResourceId='autoScalingGroup/web', ScalableDimension='autoscaling:autoScalingGroup:DesiredCapacity',
            ForecastDataType='CapacityForecast', StartTime='2026-11-10T00:00:00Z', EndTime='2026-11-11T00:00:00Z'))
        error('ObjectNotFoundException', lambda: plans.delete_scaling_plan(ScalingPlanName='missing', ScalingPlanVersion=1))
        assert [p['ScalingPlanName'] for p in plans.describe_scaling_plans()['ScalingPlans']] == ['shop']

        # A participant needs table authority, not only scaling-plan authority.
        policy = {'Version': '2012-10-17', 'Statement': [{'Effect': 'Allow', 'Action': 'autoscaling-plans:*', 'Resource': '*'}]}
        control(first, token, '/_simulator/v1/iam/principals/planner/policy', policy)
        participant = control(first, token, '/_simulator/v1/access', {'Principal': 'planner'})['AccessToken']
        limited = client('autoscaling-plans', first, participant)
        denied = error('AccessDeniedException', lambda: limited.update_scaling_plan(ScalingPlanName='shop', ScalingPlanVersion=1,
            ScalingInstructions=[instruction(TABLE, READ, 12, 12)]))
        assert denied['ResponseMetadata']['HTTPStatusCode'] == 403 and capacity(dynamodb) == (10, 2)
        policy['Statement'].append({'Effect': 'Allow', 'Action': ['dynamodb:DescribeTable', 'dynamodb:UpdateTable'],
                                    'Resource': 'arn:aws:dynamodb:us-east-1:000000000000:table/' + TABLE})
        control(first, token, '/_simulator/v1/iam/principals/planner/policy', policy)
        limited.update_scaling_plan(ScalingPlanName='shop', ScalingPlanVersion=1, ScalingInstructions=[instruction(TABLE, READ, 12, 12), instruction(TABLE, WRITE, 1, 2)])
        assert capacity(dynamodb) == (12, 2)
        policy['Statement'].append({'Effect': 'Deny', 'Action': 'autoscaling-plans:DeleteScalingPlan', 'Resource': '*'})
        control(first, token, '/_simulator/v1/iam/principals/planner/policy', policy)
        error('AccessDeniedException', lambda: limited.delete_scaling_plan(ScalingPlanName='shop', ScalingPlanVersion=1))

        # The second team has separate state, database and token authority.
        assert other_plans.describe_scaling_plans()['ScalingPlans'] == []
        error('ResourceNotFoundException', lambda: other_dynamodb.describe_table(TableName=TABLE))
        try:
            urllib.request.urlopen(urllib.request.Request(second + '/_simulator/health', headers={'X-Simulator-Token': token}), timeout=5)
        except urllib.error.HTTPError as exc:
            assert exc.code == 401
        else:
            raise AssertionError('Token crossed team authorities')
        for c in (plans, dynamodb, other_plans, other_dynamodb, limited):
            c.close()

    # Actual CLI close/reopen keeps the plan and the bounded capacity.
    with running(db=first_db, authenticated=True) as (restarted, new_token):
        plans, dynamodb = client('autoscaling-plans', restarted, new_token), client('dynamodb', restarted, new_token)
        assert capacity(dynamodb) == (12, 2)
        assert [r['ScalingStatusCode'] for r in plans.describe_scaling_plan_resources(ScalingPlanName='shop', ScalingPlanVersion=1)['ScalingPlanResources']] == ['PartiallyActive', 'PartiallyActive']
        plans.delete_scaling_plan(ScalingPlanName='shop', ScalingPlanVersion=1)
        assert capacity(dynamodb) == (12, 2), 'Deleting a plan must not revert table capacity'
        assert plans.describe_scaling_plans()['ScalingPlans'] == []
        for name in (TABLE, 'ondemand-orders'):
            dynamodb.delete_table(TableName=name)
        assert dynamodb.list_tables()['TableNames'] == []
        plans.close()
        dynamodb.close()

print(json.dumps({'autoscalingPlansNativeSDK': True, 'servedTemplates': len(executed), 'dynamodbCapacityBounded': True,
    'participantTableAuthority': True, 'twoAuthenticatedProcesses': True, 'actualCLIReopen': True,
    'teardown': 'DELETED', 'realAWS': False}))
