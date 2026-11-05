"""Validate the shared served JSON 1.0 contract with the standard validator.

This is development-only JSON Schema validation, not a replacement for native
API admission, table-state constraints, IAM or the existing SDK assertions.
"""
from jsonschema import Draft202012Validator

MEDIA = 'application/x-amz-json-1.0'
SQS_OPERATIONS = ('CreateQueue', 'ListQueues', 'GetQueueUrl', 'GetQueueAttributes',
                  'SendMessage', 'ReceiveMessage', 'ChangeMessageVisibility',
                  'DeleteMessage', 'DeleteQueue')
DYNAMODB_OPERATIONS = ('CreateTable', 'ListTables', 'DescribeTable', 'PutItem',
                       'GetItem', 'DeleteItem', 'DeleteTable')


def validator(spec, schema):
    # Local component references remain rooted in the document containing the
    # selected schema. No registry lookup or external schema fetch is needed.
    document = {'$schema': 'https://json-schema.org/draft/2020-12/schema',
                **schema, 'components': spec['components']}
    Draft202012Validator.check_schema(document)
    return Draft202012Validator(document)


def request_schema(spec, example_name):
    prefix = '' if example_name.startswith('DynamoDB') else 'SQS'
    return {'$ref': '#/components/schemas/' + prefix + example_name + 'Request'}


def validate_json10_examples(spec):
    media = spec['paths']['/']['post']['requestBody']['content'][MEDIA]
    expected = {*SQS_OPERATIONS, *('DynamoDB' + operation for operation in DYNAMODB_OPERATIONS)}
    if set(media['examples']) != expected:
        raise AssertionError('Shared JSON 1.0 examples do not contain the nine SQS and seven DynamoDB operations')
    union = validator(spec, media['schema'])
    for name, example in media['examples'].items():
        union.validate(example['value'])
        validator(spec, request_schema(spec, name)).validate(example['value'])
    return len(expected)


def validate_json10_response(spec, status, value, dynamodb_operation=None):
    post = spec['paths']['/']['post']
    schema = post['responses'][str(status)]['content'][MEDIA]['schema']
    validator(spec, schema).validate(value)
    if status == 200 and dynamodb_operation:
        exact = post['x-simulator-dynamodb-operation-schemas'][dynamodb_operation]['response']
        validator(spec, exact).validate(value)
