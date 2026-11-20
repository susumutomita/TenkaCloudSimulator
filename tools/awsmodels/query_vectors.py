"""Generate Query protocol request vectors with botocore's own serializer.

Development-only. Writes internal/aws/catalog/testdata/query_serializer.json:
each case holds the parameters given to botocore, the exact form body that
botocore's QuerySerializer and AWSRequest preparation produce, and the
parameters normalized to JSON (blobs as base64, timestamps as RFC 3339). The
Go decoder test parses each body and must recover the normalized parameters.

Usage: python tools/awsmodels/query_vectors.py  (requires the pinned botocore
from scripts/requirements-test.txt).
"""
import base64
import datetime
import json
import sys
from pathlib import Path

import botocore
import botocore.session
from botocore.awsrequest import create_request_object, prepare_request_dict
from botocore.model import ServiceModel
from botocore.serialize import create_serializer

ROOT = Path(__file__).resolve().parents[2]
OUTPUT = ROOT / 'internal/aws/catalog/testdata/query_serializer.json'
PINNED = '1.41.6'

# A synthetic Query model covering every serialization trait: renamed and
# flattened lists, lists of lists, both map forms, renamed map keys and
# values, shape-level and member-level locationName, recursion and every
# scalar type.
SYNTHETIC = {
    'metadata': {'apiVersion': '2026-01-01', 'protocol': 'query', 'endpointPrefix': 'querysynthetic',
                 'serviceId': 'QuerySynthetic', 'signatureVersion': 'v4', 'xmlNamespace': 'urn:query'},
    'operations': {'Echo': {'name': 'Echo', 'http': {'method': 'POST', 'requestUri': '/'}, 'input': {'shape': 'EchoInput'}}},
    'shapes': {
        'Text': {'type': 'string'}, 'Int': {'type': 'integer'}, 'Dbl': {'type': 'double'}, 'Flag': {'type': 'boolean'},
        'Time': {'type': 'timestamp'}, 'Data': {'type': 'blob'},
        'Pair': {'type': 'structure', 'members': {'Key': {'shape': 'Text'}, 'Value': {'shape': 'Text'}}},
        'Pairs': {'type': 'list', 'member': {'shape': 'Pair', 'locationName': 'Pair'}},
        'Plain': {'type': 'list', 'member': {'shape': 'Text'}},
        'Flat': {'type': 'list', 'member': {'shape': 'Text', 'locationName': 'Item'}},
        'FlatBare': {'type': 'list', 'flattened': True, 'member': {'shape': 'Text'}},
        'Grid': {'type': 'list', 'member': {'shape': 'Plain'}},
        'Dict': {'type': 'map', 'key': {'shape': 'Text', 'locationName': 'Name'}, 'value': {'shape': 'Text', 'locationName': 'Val'}},
        'FlatDict': {'type': 'map', 'flattened': True, 'key': {'shape': 'Text'}, 'value': {'shape': 'Pair'}},
        'Inner': {'type': 'structure', 'locationName': 'In', 'members': {'X': {'shape': 'Text'}}},
        'Node': {'type': 'structure', 'members': {'Child': {'shape': 'Node'}, 'Name': {'shape': 'Text'}}},
        'EchoInput': {'type': 'structure', 'members': {
            'Name': {'shape': 'Text'}, 'Count': {'shape': 'Int'}, 'Ratio': {'shape': 'Dbl'}, 'On': {'shape': 'Flag'},
            'At': {'shape': 'Time'}, 'Data': {'shape': 'Data'}, 'Pairs': {'shape': 'Pairs'}, 'Plain': {'shape': 'Plain'},
            'Flat': {'shape': 'Flat', 'flattened': True}, 'FlatBare': {'shape': 'FlatBare'}, 'Grid': {'shape': 'Grid'},
            'Dict': {'shape': 'Dict'}, 'FlatDict': {'shape': 'FlatDict'}, 'Inner': {'shape': 'Inner'},
            'Renamed': {'shape': 'Text', 'locationName': 'Alias'}, 'Node': {'shape': 'Node'}}},
    },
}

AT = datetime.datetime(2026, 11, 20, 1, 2, 3, tzinfo=datetime.timezone.utc)
SYNTHETIC_CASES = [
    ('every trait', {
        'Name': 'n & m', 'Count': 7, 'Ratio': 1.5, 'On': True, 'At': AT, 'Data': b'hi',
        'Pairs': [{'Key': 'k1', 'Value': 'v1'}, {'Key': 'k2'}], 'Plain': ['p'], 'Flat': ['a', 'b'], 'FlatBare': ['c'],
        'Grid': [['g'], []], 'Dict': {'dk': 'dv'}, 'FlatDict': {'fk': {'Key': 'pk'}}, 'Inner': {'X': 'x'},
        'Renamed': 'r', 'Node': {'Child': {'Name': 'leaf'}}}),
    ('empty lists', {'Pairs': [], 'Plain': [], 'Flat': [], 'FlatBare': [], 'Grid': []}),
    ('false and zero', {'On': False, 'Count': 0, 'Ratio': -0.25}),
    ('map with empty structure value', {'FlatDict': {'a': {}, 'b': {'Value': 'v'}}}),
    ('unicode text', {'Name': 'テナ ☁ +%&='}),
]

RDS_CASES = [
    ('CreateDBSubnetGroup', {'DBSubnetGroupName': 'app', 'DBSubnetGroupDescription': 'app subnets',
                             'SubnetIds': ['subnet-0a1b2c3d', 'subnet-4e5f6a7b'], 'Tags': [{'Key': 'team', 'Value': 'a'}]}),
    ('ModifyDBSubnetGroup', {'DBSubnetGroupName': 'app', 'SubnetIds': ['subnet-0a1b2c3d', 'subnet-4e5f6a7b']}),
    ('DescribeDBSubnetGroups', {'DBSubnetGroupName': 'app', 'Filters': [], 'MaxRecords': 20, 'Marker': 'm'}),
    ('DescribeOptionGroups', {'EngineName': 'mysql', 'MajorEngineVersion': '8.0'}),
    ('AddTagsToResource', {'ResourceName': 'arn:aws:rds:us-east-1:000000000000:subgrp:app', 'Tags': [{'Key': 'a', 'Value': 'b'}, {'Key': 'c', 'Value': ''}]}),
    ('RemoveTagsFromResource', {'ResourceName': 'arn:aws:rds:us-east-1:000000000000:subgrp:app', 'TagKeys': ['a', 'c']}),
    ('ListTagsForResource', {'ResourceName': 'arn:aws:rds:us-east-1:000000000000:subgrp:app', 'Filters': []}),
]


def normalize(value):
    if isinstance(value, bytes):
        return base64.b64encode(value).decode()
    if isinstance(value, datetime.datetime):
        return value.strftime('%Y-%m-%dT%H:%M:%SZ')
    if isinstance(value, dict):
        return {k: normalize(v) for k, v in value.items()}
    if isinstance(value, list):
        return [normalize(v) for v in value]
    return value


def wire(model, operation, params):
    op = model.operation_model(operation)
    request = create_serializer('query', include_validation=True).serialize_to_request(params, op)
    prepare_request_dict(request, endpoint_url='https://example.invalid')
    body = create_request_object(request).prepare().body
    return body.decode() if isinstance(body, bytes) else body


def main():
    if botocore.__version__ != PINNED:
        raise SystemExit(f'botocore {PINNED} is required; found {botocore.__version__}')
    synthetic = ServiceModel(SYNTHETIC, 'querysynthetic')
    rds = botocore.session.get_session().get_service_model('rds')
    cases = [{'model': 'synthetic', 'name': name, 'operation': 'Echo', 'body': wire(synthetic, 'Echo', params), 'params': normalize(params)}
             for name, params in SYNTHETIC_CASES]
    cases += [{'model': 'rds', 'name': operation, 'operation': operation, 'body': wire(rds, operation, params), 'params': normalize(params)}
              for operation, params in RDS_CASES]
    data = {'generator': 'tools/awsmodels/query_vectors.py', 'botocore': botocore.__version__, 'synthetic': SYNTHETIC, 'cases': cases}
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    OUTPUT.write_text(json.dumps(data, indent=1, sort_keys=True, ensure_ascii=False) + '\n')
    print(f'Wrote {len(cases)} botocore Query vectors to {OUTPUT.relative_to(ROOT)}')


if __name__ == '__main__':
    sys.exit(main())
