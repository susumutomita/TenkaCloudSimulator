"""Schema union regression and negative controls; no server or browser."""
import copy
import json
from pathlib import Path
import unittest

from jsonschema import ValidationError
from openapi_json10 import (DYNAMODB_OPERATIONS, MEDIA, request_schema,
                            validate_json10_examples, validate_json10_response, validator)


class SharedJSON10Schemas(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        root = Path(__file__).resolve().parents[2]
        cls.spec = json.loads((root / 'internal/server/apidocs/openapi.json').read_text())

    def examples(self):
        return self.spec['paths']['/']['post']['requestBody']['content'][MEDIA]['examples']

    def test_all_sixteen_templates_match_shared_and_operation_schemas(self):
        self.assertEqual(validate_json10_examples(self.spec), 16)

    def test_omitting_dynamodb_union_branches_reproduces_composition_failure(self):
        broken = copy.deepcopy(self.spec)
        schema = broken['paths']['/']['post']['requestBody']['content'][MEDIA]['schema']
        schema['anyOf'] = [branch for branch in schema['anyOf']
                           if '/DynamoDB' not in branch.get('$ref', '')]
        self.assertEqual(len(schema['anyOf']), 9)
        shared = validator(broken, schema)
        for operation in DYNAMODB_OPERATIONS:
            with self.subTest(operation=operation), self.assertRaises(ValidationError):
                shared.validate(self.examples()['DynamoDB' + operation]['value'])

    def test_unknown_top_level_fields_are_rejected_for_every_template(self):
        media = self.spec['paths']['/']['post']['requestBody']['content'][MEDIA]
        shared = validator(self.spec, media['schema'])
        for name, example in self.examples().items():
            invalid = {**copy.deepcopy(example['value']), 'UnsupportedSetting': True}
            with self.subTest(name=name):
                with self.assertRaises(ValidationError):
                    shared.validate(invalid)
                with self.assertRaises(ValidationError):
                    validator(self.spec, request_schema(self.spec, name)).validate(invalid)

    def test_nested_options_and_case_changes_are_rejected(self):
        original = self.examples()['DynamoDBCreateTable']['value']
        for field in ('AttributeDefinitions', 'KeySchema'):
            invalid = copy.deepcopy(original)
            invalid[field][0]['UnexpectedSetting'] = True
            with self.subTest(field=field), self.assertRaises(ValidationError):
                validator(self.spec, request_schema(self.spec, 'DynamoDBCreateTable')).validate(invalid)
        invalid = copy.deepcopy(original)
        invalid['KeySchema'][0]['keytype'] = invalid['KeySchema'][0].pop('KeyType')
        with self.assertRaises(ValidationError):
            validator(self.spec, request_schema(self.spec, 'DynamoDBCreateTable')).validate(invalid)
        invalid = copy.deepcopy(self.examples()['CreateQueue']['value'])
        invalid['Attributes']['RedrivePolicy'] = '{}'
        with self.assertRaises(ValidationError):
            validator(self.spec, request_schema(self.spec, 'CreateQueue')).validate(invalid)

    def test_invalid_dynamodb_key_shapes_are_rejected(self):
        schema = validator(self.spec, request_schema(self.spec, 'DynamoDBGetItem'))
        original = self.examples()['DynamoDBGetItem']['value']
        for key in ({}, {'id': {'S': ''}}, {'id': {'BOOL': True}},
                    {'id': {'S': 'dummy', 'N': '1'}}, {'id': {'N': 1}},
                    {'id': {'S': 'dummy'}, 'sort': {'S': 'a'}, 'extra': {'S': 'b'}}):
            with self.subTest(key=key), self.assertRaises(ValidationError):
                schema.validate({**original, 'Key': key})
        for key in ({'id': {'S': 'dummy'}}, {'id': {'N': '1.0'}}, {'id': {'B': 'ZHVtbXk='}}):
            schema.validate({**original, 'Key': key})

    def test_billing_and_recursive_value_subset(self):
        create = copy.deepcopy(self.examples()['DynamoDBCreateTable']['value'])
        schema = validator(self.spec, request_schema(self.spec, 'DynamoDBCreateTable'))
        provisioned = {**create, 'BillingMode': 'PROVISIONED',
                       'ProvisionedThroughput': {'ReadCapacityUnits': 1, 'WriteCapacityUnits': 2}}
        schema.validate(provisioned)
        del provisioned['BillingMode']
        schema.validate(provisioned)
        with self.assertRaises(ValidationError):
            schema.validate({**provisioned, 'BillingMode': 'PAY_PER_REQUEST'})
        with self.assertRaises(ValidationError):
            schema.validate({**create, 'BillingMode': 'PROVISIONED'})
        item = copy.deepcopy(self.examples()['DynamoDBPutItem']['value'])
        item['Item']['nested'] = {'M': {'values': {'L': [{'BOOL': False}, {'NULL': True}, {'NS': ['1', '2']}]}}}
        schema = validator(self.spec, request_schema(self.spec, 'DynamoDBPutItem'))
        schema.validate(item)
        item['Item']['nested']['M']['values']['L'][1] = {'NULL': False}
        with self.assertRaises(ValidationError):
            schema.validate(item)

    def test_response_union_retains_dynamodb_and_native_error_member_names(self):
        validate_json10_response(self.spec, 200, {'TableNames': ['dummy-table']}, 'ListTables')
        validate_json10_response(self.spec, 200, {'Item': {'id': {'S': 'dummy'}}}, 'GetItem')
        for status in (400, 403, 500, 501):
            validate_json10_response(self.spec, status, {'__type': 'InternalError', 'Message': 'dummy'})
            validate_json10_response(self.spec, status,
                                     {'__type': 'com.amazonaws.dynamodb.v20120810#ValidationException', 'message': 'dummy'})
        broken = copy.deepcopy(self.spec)
        schema = broken['paths']['/']['post']['responses']['200']['content'][MEDIA]['schema']
        schema['anyOf'] = [branch for branch in schema['anyOf']
                           if '/DynamoDB' not in branch.get('$ref', '')]
        with self.assertRaises(ValidationError):
            validate_json10_response(broken, 200, {'TableNames': ['dummy-table']})

    def test_json11_templates_remain_admitted_by_shared_schema(self):
        media = self.spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']
        shared = validator(self.spec, media['schema'])
        expected = {
            'SSMGetParameter', 'KMSCreateKey', 'KMSDescribeKey', 'KMSListKeys',
            'KMSEnableKey', 'KMSDisableKey', 'KMSGetKeyPolicy', 'KMSEncrypt',
            'KMSDecrypt', 'LogsCreateLogGroup', 'LogsDescribeLogGroups',
            'LogsDeleteLogGroup', 'LogsCreateLogStream', 'LogsDescribeLogStreams',
            'LogsPutLogEvents', 'LogsGetLogEvents', 'CreateSecret', 'DescribeSecret',
            'ListSecrets', 'PutSecretValue', 'GetSecretValue',
            'UpdateSecretVersionStage', 'DeleteSecret', 'ListSecretVersionIds',
        }
        self.assertEqual(set(media['examples']), expected)
        for example in media['examples'].values():
            shared.validate(example['value'])


    def test_version_list_template_has_its_strict_native_target_schema(self):
        regional = self.spec['paths']['/']['post']
        template = regional['requestBody']['content']['application/x-amz-json-1.1']['examples']['ListSecretVersionIds']
        self.assertEqual(template['summary'], 'X-Amz-Target: secretsmanager.ListSecretVersionIds')
        operation = regional['x-simulator-secrets-operation-schemas']['secretsmanager.ListSecretVersionIds']
        self.assertEqual(operation['request'], '#/components/schemas/SecretsListSecretVersionIdsInput')
        strict = validator(self.spec, {'$ref': operation['request']})
        strict.validate(template['value'])
        original = template['value']
        missing = copy.deepcopy(original)
        del missing['SecretId']
        invalid_inputs = [missing, {**original, 'SecretId': None},
                          {**original, 'MaxResults': 0}, {**original, 'MaxResults': 101},
                          {**original, 'MaxResults': True},
                          {**original, 'IncludeDeprecated': None},
                          {**original, 'IncludeDeprecated': 'true'},
                          {**original, 'NextToken': ''},
                          {**original, 'UnsupportedSetting': True}]
        for index, invalid in enumerate(invalid_inputs):
            with self.subTest(index=index), self.assertRaises(ValidationError):
                strict.validate(invalid)


if __name__ == '__main__':
    unittest.main()
