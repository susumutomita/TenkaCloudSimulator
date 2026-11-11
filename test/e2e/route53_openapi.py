"""Exact additive Route53 contract, checked before inherited-schema projection."""
import copy
import hashlib
import json
import runpy
from pathlib import Path

# This module is loaded both by sibling tests and by the asset gate via runpy.
project_ssm_documents = runpy.run_path(
    str(Path(__file__).with_name("ssm_document_reference.py"))
)["project_ssm_documents"]

ROUTE53_PATHS = {
    '/2013-04-01/hostedzone',
    '/2013-04-01/hostedzone/{Id}',
    '/2013-04-01/hostedzone/{Id}/rrset',
    '/2013-04-01/change/{Id}',
    '/2013-04-01/testdnsanswer',
}
ROUTE53_IDS = {'CreateHostedZone', 'GetHostedZone', 'ListHostedZones', 'DeleteHostedZone',
    'ChangeResourceRecordSets', 'ListResourceRecordSets', 'GetChange', 'TestDNSAnswer'}
TAG = 'Route53 public hosted zones'
CONTRACT_SHA256 = '1210ce75c2af0b81df6acb18207f1a027627d48c0c75932b34a9792d154e7388'


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'),
        ensure_ascii=False).encode()).hexdigest()


def validate_route53(spec):
    assert ROUTE53_PATHS <= set(spec['paths']), 'Missing Route53 native path'
    contract = {path: spec['paths'][path] for path in ROUTE53_PATHS}
    assert digest(contract) == CONTRACT_SHA256, 'Route53 exact path/request/response/schema contract changed'
    ids = [operation['operationId'] for path in spec['paths'].values()
        for operation in path.values() if isinstance(operation, dict) and 'operationId' in operation]
    assert all(ids.count(name) == 1 for name in ROUTE53_IDS), 'Route53 operation missing or duplicated'
    tags = [tag for tag in spec['tags'] if tag.get('name') == TAG]
    assert tags == [{'name': TAG, 'description': 'Bounded public zones and saved-state answers only. No external DNS, private VPC zones, aliases, routing policies, console or CloudFormation.'}]
    assert sum(group.get('tags', []).count(TAG) for group in spec['x-tagGroups']) == 1
    assert TAG in next(group['tags'] for group in spec['x-tagGroups'] if group['name'] == 'AWS')


def project_before_route53(spec):
    validate_route53(spec)
    projected = copy.deepcopy(spec)
    project_ssm_documents(projected)
    for path in ROUTE53_PATHS:
        del projected['paths'][path]
    projected['tags'] = [tag for tag in projected['tags'] if tag.get('name') != TAG]
    for group in projected['x-tagGroups']:
        if TAG in group.get('tags', []):
            group['tags'].remove(TAG)
    return projected
