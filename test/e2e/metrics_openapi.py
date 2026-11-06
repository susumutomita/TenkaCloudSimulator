# SPDX-License-Identifier: MIT
"""Closed Metrics-only reference extension over immutable public66/36.

Static source guard only. Does not execute examples, verify SDK/Scalar or enable
capability flags. Every old field survives the inverse; no prefix exclusions.
"""
import copy
import hashlib
import json
from pathlib import Path

CONTRACT_SHA256 = '46a55b0d7a0c03405621d71e21d6cbd64e44f95b8f6e182bf861ec21a7603ea5'
TAG = 'CloudWatch Metrics'
MEDIA = 'application/x-www-form-urlencoded'
ACTIONS = {'PutMetricData', 'ListMetrics', 'GetMetricStatistics'}

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)


def contract():
    raw = Path(__file__).with_name('cloudwatch_metrics_openapi_contract.json').read_bytes()
    assert hashlib.sha256(raw).hexdigest() == CONTRACT_SHA256, 'Reviewed Metrics contract changed'
    return json.loads(raw)


def preserved_metrics_reference(spec):
    """Admit ONLY the exact Metrics additions, then return the untouched66/36.

    Require the complete immutable public baseline after this exact inverse.
    Unknown changes are retained and rejected by canonical equality, never hidden.
    """
    c = contract()
    # These shared entries can be removed by a negative document fixture.
    # Refuse them through the validator contract before projecting additions;
    # an incidental KeyError/StopIteration is not a validated refusal.
    assert isinstance(spec, dict), 'Reference document must be an object'
    paths = spec.get('paths')
    assert isinstance(paths, dict), 'Reference paths must be an object'
    shared = paths.get('/')
    assert isinstance(shared, dict), 'Shared AWS root path is required'
    root = shared.get('post')
    assert isinstance(root, dict), 'Shared AWS root POST operation is required'
    assert root['operationId'] == 'awsRegionalOperation'
    assert root['tags'].count(TAG) == 1 and root['tags'][-1] == TAG
    assert root['description'].endswith(c['descriptionAppend'])
    service = [p for p in root['parameters'] if p.get('name') == 'X-Simulator-Service' and p.get('in') == 'header']
    assert len(service) == 1
    hint = service[0]
    assert hint['schema']['type'] == 'string'
    assert hint['schema']['enum'].count('monitoring') == 1 and hint['schema']['enum'][-1] == 'monitoring'
    assert hint['description'].endswith(c['serviceDescriptionAppend'])
    content = root['requestBody']['content'][MEDIA]
    assert set(content['schema']) == {'oneOf'} and len(content['schema']['oneOf']) == 4
    branches = content['schema']['oneOf']
    assert canonical(branches[1:]) == canonical(c['oneOfAppend'])
    old_action = branches[0].get('properties', {}).get('Action', {})
    assert old_action.get('type') == 'string' and type(old_action.get('enum')) is list
    old_actions = old_action['enum']
    assert canonical(old_actions) == canonical(c['oldGenericActionEnum'])
    assert set(old_actions).isdisjoint(ACTIONS), 'Old generic form overlaps Metrics Action values'
    assert isinstance(content.get('examples'), dict) and set(c['examples']) <= set(content['examples']), 'Required Metrics examples are missing'
    assert isinstance(spec.get('components'), dict) and isinstance(spec['components'].get('schemas'), dict), 'Reference schemas must be an object'
    assert set(c['schemaNames']) <= set(spec['components']['schemas']), 'Required Metrics schemas are missing'
    for name, value in c['examples'].items():
        assert canonical(content['examples'][name]) == canonical(value), 'Static Metrics example changed'
    for name in c['schemaNames']:
        assert canonical(spec['components']['schemas'][name]) == canonical(c['schemas'][name]), 'Metrics schema changed'
    for status, value in c['responseMedia'].items():
        assert canonical(root['responses'][status]['content']['text/xml']) == canonical(value), 'Metrics XML/error example changed'
    assert sum(t['name'] == TAG for t in spec['tags']) == 1
    assert canonical(next(t for t in spec['tags'] if t['name'] == TAG)) == canonical(c['tag'])
    groups = spec.get('x-tagGroups')
    assert isinstance(groups, list), 'Cloud groups must be a list'
    assert all(isinstance(g, dict) and isinstance(g.get('name'), str)
               and isinstance(g.get('tags'), list)
               and all(isinstance(tag, str) for tag in g['tags'])
               for g in groups), 'Cloud groups require names and tag lists'
    aws_indices = [i for i, group in enumerate(groups) if group['name'] == 'AWS']
    assert len(aws_indices) == 1, 'Exactly one AWS cloud group is required'
    aws_index = aws_indices[0]
    aws = groups[aws_index]
    assert aws['tags'].count(TAG) == 1 and aws['tags'][-1] == TAG
    assert sum(TAG in g['tags'] for g in spec['x-tagGroups']) == 1

    restored = copy.deepcopy(spec)
    original = restored['paths']['/']['post']
    original['tags'].pop()
    original['description'] = original['description'][:-len(c['descriptionAppend'])]
    hint = next(p for p in original['parameters'] if p.get('name') == 'X-Simulator-Service' and p.get('in') == 'header')
    hint['schema']['enum'].pop()
    hint['description'] = hint['description'][:-len(c['serviceDescriptionAppend'])]
    form = original['requestBody']['content'][MEDIA]
    form['schema'] = form['schema']['oneOf'][0]
    for name in c['examples']:
        del form['examples'][name]
    for name in c['schemaNames']:
        del restored['components']['schemas'][name]
    for status in c['responseMedia']:
        del original['responses'][status]['content']['text/xml']
        if not c['originalResponseContentPresent'][status]:
            assert original['responses'][status]['content'] == {}
            del original['responses'][status]['content']
    restored['tags'].remove(next(t for t in restored['tags'] if t['name'] == TAG))
    restored['x-tagGroups'][aws_index]['tags'].pop()
    assert hashlib.sha256(canonical(restored).encode()).hexdigest() == c['base66CanonicalSHA256'], 'Original public66/36 reference changed'
    return restored
