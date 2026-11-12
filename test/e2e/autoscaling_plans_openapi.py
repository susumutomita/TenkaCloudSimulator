"""Exact additive Auto Scaling Plans reference contract, removed before inherited checks.

The five templates, strict schemas, union bindings, operation mapping and guide
tag are pinned here. Projection removes only these additions so every earlier
inherited digest keeps detecting drift in the shared AWS root.
"""
import copy
import hashlib
import json

JSON11 = 'application/x-amz-json-1.1'
TARGET = 'AnyScaleScalingPlannerFrontendService.'
OPERATIONS = ('CreateScalingPlan', 'DescribeScalingPlans', 'DescribeScalingPlanResources',
              'UpdateScalingPlan', 'DeleteScalingPlan')
EXAMPLES = tuple('AutoScaling' + operation for operation in OPERATIONS)
INPUTS = tuple('AutoScalingPlans' + operation + 'Input' for operation in OPERATIONS)
OUTPUTS = ('AutoScalingPlansCreateScalingPlanOutput', 'AutoScalingPlansDescribeScalingPlansOutput',
           'AutoScalingPlansDescribeScalingPlanResourcesOutput', 'AutoScalingPlansEmptyOutput')
SCHEMAS = frozenset(INPUTS + OUTPUTS + (
    'AutoScalingPlansName', 'AutoScalingPlansVersion', 'AutoScalingPlansTagFilter',
    'AutoScalingPlansApplicationSource', 'AutoScalingPlansTargetTracking',
    'AutoScalingPlansScalingInstruction', 'AutoScalingPlansScalingPlan',
    'AutoScalingPlansScalingPolicy', 'AutoScalingPlansScalingPlanResource'))
EXTENSION = 'x-simulator-autoscaling-plans-operation-schemas'
TAG = 'AWS Auto Scaling plans'
CONTRACT_SHA256 = '6579a2ae57c999e0a3827140074b02c4b5288d210a2cf15bf460d4dbf980f528'


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'),
        ensure_ascii=False).encode()).hexdigest()


def ref(name):
    return {'$ref': '#/components/schemas/' + name}


def contract(spec):
    root = spec['paths']['/']['post']
    request = root['requestBody']['content'][JSON11]
    response = root['responses']['200']['content'][JSON11]
    return {
        'requests': {name: request['examples'].get(name) for name in EXAMPLES},
        'responses': {name: response['examples'].get(name) for name in EXAMPLES},
        'schemas': {name: spec['components']['schemas'].get(name) for name in sorted(SCHEMAS)},
        'mapping': root.get(EXTENSION),
        'tags': [tag for tag in spec['tags'] if tag.get('name') == TAG],
    }


def validate_autoscaling_plans(spec):
    root = spec['paths']['/']['post']
    request = root['requestBody']['content'][JSON11]
    response = root['responses']['200']['content'][JSON11]
    assert all(name in request['examples'] and name in response['examples'] for name in EXAMPLES), 'Missing Auto Scaling Plans template'
    assert digest(contract(spec)) == CONTRACT_SHA256, 'Auto Scaling Plans exact template/schema/mapping contract changed'
    extra = [name for name in request['examples'] if name.startswith('AutoScaling') and name not in EXAMPLES]
    extra += [name for name in spec['components']['schemas'] if name.startswith('AutoScalingPlans') and name not in SCHEMAS]
    assert not extra, 'Unreviewed Auto Scaling Plans addition: ' + ', '.join(extra)
    union = request['schema']['anyOf']
    assert union[-len(INPUTS):] == [ref(name) for name in INPUTS], 'Auto Scaling Plans request union binding changed'
    assert all(union.count(ref(name)) == 1 for name in INPUTS), 'Auto Scaling Plans request union duplicated'
    union = response['schema']['anyOf']
    assert union[-len(OUTPUTS):] == [ref(name) for name in OUTPUTS], 'Auto Scaling Plans response union binding changed'
    assert all(union.count(ref(name)) == 1 for name in OUTPUTS), 'Auto Scaling Plans response union duplicated'
    assert sum(group.get('tags', []).count(TAG) for group in spec['x-tagGroups']) == 1
    assert TAG in next(group['tags'] for group in spec['x-tagGroups'] if group['name'] == 'AWS')
    assert not any('autoscaling' in path.lower() for path in spec['paths']), 'Auto Scaling Plans uses the actual shared root'


def project_before_autoscaling_plans(spec):
    validate_autoscaling_plans(spec)
    projected = copy.deepcopy(spec)
    root = projected['paths']['/']['post']
    request = root['requestBody']['content'][JSON11]
    response = root['responses']['200']['content'][JSON11]
    for name in EXAMPLES:
        del request['examples'][name]
        del response['examples'][name]
    del request['schema']['anyOf'][-len(INPUTS):]
    del response['schema']['anyOf'][-len(OUTPUTS):]
    del root[EXTENSION]
    for name in SCHEMAS:
        del projected['components']['schemas'][name]
    projected['tags'] = [tag for tag in projected['tags'] if tag.get('name') != TAG]
    for group in projected['x-tagGroups']:
        if TAG in group.get('tags', []):
            group['tags'].remove(TAG)
    return projected
