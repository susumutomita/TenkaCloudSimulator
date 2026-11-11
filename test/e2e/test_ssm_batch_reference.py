"""Independent additive SSM reference checks; no server or browser is launched."""
import copy
import hashlib
import json
from pathlib import Path
import unittest

from batch_reference_projection import project_batches
from route53_openapi import project_before_route53
from test_sakura_openapi import preserved

ROOT = Path(__file__).resolve().parents[2]
BASE_SHA256 = '02bdc1440b20fe8bf74d2cef12613b9a48dfc4bf1a0f0a4956ebd7e8773ccba2'


def validate(spec):
    base = project_before_route53(spec)
    project_batches(base)
    assert hashlib.sha256(json.dumps(base, sort_keys=True, separators=(',', ':')).encode()).hexdigest() == BASE_SHA256


class SSMReference(unittest.TestCase):
    def setUp(self):
        self.spec = json.loads((ROOT / 'internal/server/apidocs/openapi.json').read_text())

    def test_additive_contract_and_existing_reference(self):
        validate(self.spec)

    def test_negative_controls(self):
        changes = (
            lambda s: s['components']['schemas']['SSMDeleteParametersInput']['properties']['Names'].update(maxItems=11),
            lambda s: s['components']['schemas']['SSMDeleteParametersInput'].update(additionalProperties=True),
            lambda s: s['components']['schemas']['SSMDeleteParametersOutput']['properties'].pop('InvalidParameters'),
            lambda s: s['paths']['/']['post']['x-simulator-ssm-operation-schemas']['DeleteParameters'].update(iamAction='ssm:DeleteParameter'),
            lambda s: s['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']['examples']['SSMDeleteParameters'].update(description='harmless read'),
            lambda s: s['paths']['/']['get'].update(summary='unrelated change'),
            lambda s: s['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']['schema']['anyOf'].pop(),
        )
        for change in changes:
            with self.subTest(change=change):
                bad = copy.deepcopy(self.spec)
                change(bad)
                with self.assertRaises((AssertionError, KeyError)):
                    validate(bad)

    def test_both_guards_reject_either_delta_and_unrelated_drift(self):
        def post(spec):
            return spec['paths']['/']['post']
        def media(spec, version, outgoing=False):
            owner = post(spec)['responses']['200'] if outgoing else post(spec)['requestBody']
            return owner['content']['application/x-amz-json-' + version]
        def schema(spec, name):
            return spec['components']['schemas'][name]
        changes = (
            ('SSM missing input', lambda s: s['components']['schemas'].pop('SSMDeleteParametersInput')),
            ('SSM extra schema field', lambda s: schema(s, 'SSMDeleteParametersInput').update(unreviewed=True)),
            ('SSM altered example summary', lambda s: media(s, '1.1')['examples']['SSMDeleteParameters'].update(summary='Changed')),
            ('SSM altered response example', lambda s: media(s, '1.1', True)['examples']['SSMDeleteParameters']['value'].update(InvalidParameters=[])),
            ('SSM missing response example', lambda s: media(s, '1.1', True)['examples'].pop('SSMDeleteParameters')),
            ('SSM weakened item pattern', lambda s: schema(s, 'SSMDeleteParametersInput')['properties']['Names']['items'].update(pattern='.*')),
            ('SSM unknown operation field', lambda s: post(s)['x-simulator-ssm-operation-schemas']['DeleteParameters'].update(unreviewed=True)),
            ('SSM duplicate request binding', lambda s: media(s, '1.1')['schema']['anyOf'].append({'$ref': '#/components/schemas/SSMDeleteParametersInput'})),
            ('SSM missing response binding', lambda s: media(s, '1.1', True)['schema']['anyOf'].pop()),
            ('SSM duplicate response binding', lambda s: media(s, '1.1', True)['schema']['anyOf'].append({'$ref': '#/components/schemas/SSMDeleteParametersOutput'})),
            ('SSM description changed', lambda s: post(s).update(description=post(s)['description'].replace('ssm:DeleteParameters', 'ssm:DeleteParameter'))),
            ('SQS missing input', lambda s: s['components']['schemas'].pop('SQSDeleteMessageBatchRequest')),
            ('SQS extra schema field', lambda s: schema(s, 'SQSDeleteMessageBatchRequest').update(unreviewed=True)),
            ('SQS missing example', lambda s: media(s, '1.0')['examples'].pop('DeleteMessageBatch')),
            ('SQS altered example', lambda s: media(s, '1.0')['examples']['DeleteMessageBatch'].update(summary='Changed')),
            ('SQS unknown operation field', lambda s: post(s)['x-simulator-sqs-operation-schemas']['DeleteMessageBatch'].update(unreviewed=True)),
            ('SQS wrong IAM action', lambda s: post(s)['x-simulator-sqs-operation-schemas']['DeleteMessageBatch'].update(permission='sqs:DeleteMessageBatch')),
            ('SQS missing result partition', lambda s: schema(s, 'SQSDeleteMessageBatchResult')['required'].remove('Failed')),
            ('SQS missing request binding', lambda s: media(s, '1.0')['schema']['anyOf'].pop()),
            ('SQS duplicate response binding', lambda s: media(s, '1.0', True)['schema']['anyOf'].append({'$ref': '#/components/schemas/SQSDeleteMessageBatchResult'})),
            ('SQS error description changed', lambda s: schema(s, 'SQSAPIError')['properties']['__type'].update(description='Changed')),
            ('SQS description changed', lambda s: post(s).update(description=post(s)['description'].replace('IAM sqs:DeleteMessage on', 'IAM sqs:DeleteMessageBatch on'))),
            ('inherited SQS template', lambda s: media(s, '1.0')['examples']['SendMessage']['value'].update(MessageBody='Changed')),
            ('inherited DynamoDB template', lambda s: media(s, '1.0')['examples']['DynamoDBGetItem']['value'].update(TableName='Changed')),
            ('inherited JSON 1.1 template', lambda s: media(s, '1.1')['examples']['ListSecretVersionIds']['value'].update(UnexpectedField=True)),
            ('inherited Logs delta', lambda s: schema(s, 'LogsDeleteLogStreamRequest').update(additionalProperties=True)),
            ('unrelated path', lambda s: s['paths']['/{bucket}']['put'].update(summary='Changed')),
            ('unreviewed component', lambda s: s['components']['schemas'].update(UnreviewedBatchExtension={'type': 'string'})),
            ('unreviewed shared root field', lambda s: post(s).update(unreviewed=True)),
        )
        for label, change in changes:
            for guard in (validate, preserved):
                broken = copy.deepcopy(self.spec)
                change(broken)
                with self.subTest(change=label, guard=guard.__name__), self.assertRaises((AssertionError, KeyError)):
                    guard(broken)


if __name__ == '__main__':
    unittest.main()
