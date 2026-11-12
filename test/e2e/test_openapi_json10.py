"""Schema union regression and negative controls; no server or browser."""
import copy
import json
from pathlib import Path
import unittest

from jsonschema import ValidationError
from route53_openapi import validate_route53
from autoscaling_plans_openapi import validate_autoscaling_plans
from openapi_json10 import (DYNAMODB_OPERATIONS, MEDIA, request_schema,
                            validate_json10_examples, validate_json10_response, validator)


class SharedJSON10Schemas(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        root = Path(__file__).resolve().parents[2]
        cls.spec = json.loads((root / 'internal/server/apidocs/openapi.json').read_text())

    def test_route53_extension_is_exact_and_does_not_replace_shared_root(self):
        validate_route53(self.spec)

    def examples(self):
        return self.spec['paths']['/']['post']['requestBody']['content'][MEDIA]['examples']

    def test_all_seventeen_templates_match_shared_and_operation_schemas(self):
        self.assertEqual(validate_json10_examples(self.spec), 17)

    def test_omitting_dynamodb_union_branches_reproduces_composition_failure(self):
        broken = copy.deepcopy(self.spec)
        schema = broken['paths']['/']['post']['requestBody']['content'][MEDIA]['schema']
        schema['anyOf'] = [branch for branch in schema['anyOf']
                           if '/DynamoDB' not in branch.get('$ref', '')]
        self.assertEqual(len(schema['anyOf']), 10)
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

    def test_batch_delete_template_and_strict_entry_bounds(self):
        example = self.examples()['DeleteMessageBatch']
        self.assertEqual(example['x-simulator-target'], 'AmazonSQS.DeleteMessageBatch')
        schema = validator(self.spec, request_schema(self.spec, 'DeleteMessageBatch'))
        original = example['value']
        schema.validate(original)
        entry = original['Entries'][0]
        for entries in ([], [entry] * 11, None, {}, [None],
                        [{**entry, 'Id': value} for value in ('valid', '')],
                        [{**entry, 'ReceiptHandle': ''}],
                        [{**entry, 'Unknown': True}],
                        [{'Id': 'missing-receipt'}], [{'ReceiptHandle': 'missing-id'}]):
            with self.subTest(entries=entries), self.assertRaises(ValidationError):
                schema.validate({**original, 'Entries': entries})
        for field, invalids in (('Id', ('', 'x' * 81, 'bad.id', '日本語', 'valid\n', 'valid\r\n', 1, None, True)),
                               ('ReceiptHandle', ('', None, 1, True))):
            for value in invalids:
                with self.subTest(field=field, value=value), self.assertRaises(ValidationError):
                    schema.validate({**original, 'Entries': [{**entry, field: value}]})
        for missing in ('QueueUrl', 'Entries'):
            invalid = copy.deepcopy(original)
            del invalid[missing]
            with self.assertRaises(ValidationError):
                schema.validate(invalid)
        # Uniqueness of only the Id field is a native API obligation, not
        # something JSON Schema's whole-object uniqueItems can express.
        self.assertNotIn('uniqueItems', self.spec['components']['schemas'][
            'SQSDeleteMessageBatchRequest']['properties']['Entries'])

    def test_batch_response_requires_both_strict_partitions(self):
        success = {'Id': 'deleted'}
        failure = {'Id': 'invalid', 'Code': 'ReceiptHandleIsInvalid',
                   'Message': 'The input receipt handle is invalid.', 'SenderFault': True}
        for result in ({'Successful': [success], 'Failed': []},
                       {'Successful': [], 'Failed': [failure]},
                       {'Successful': [success], 'Failed': [failure]}):
            validate_json10_response(self.spec, 200, result, sqs_operation='DeleteMessageBatch')
        for invalid in ({'Successful': []}, {'Failed': []},
                        {'Successful': None, 'Failed': []},
                        {'Successful': [], 'Failed': None},
                        {'Successful': [{'Id': 'ok', 'ReceiptHandle': 'leak'}], 'Failed': []},
                        {'Successful': [], 'Failed': [{**failure, 'SenderFault': False}]},
                        {'Successful': [], 'Failed': [{**failure, 'Code': 'InternalError'}]}):
            with self.subTest(invalid=invalid), self.assertRaises(ValidationError):
                validate_json10_response(self.spec, 200, invalid, sqs_operation='DeleteMessageBatch')
        # ECMAScript/Python '$' may match before a final newline. The ID
        # contract forbids every non-ASCII-ID character anywhere in the value.
        for part, item in (('Successful', success), ('Failed', failure)):
            for invalid_id in ('valid\n', 'valid\r\n'):
                invalid = {'Successful': [], 'Failed': []}
                invalid[part] = [{**item, 'Id': invalid_id}]
                with self.subTest(part=part, invalid_id=invalid_id), self.assertRaises(ValidationError):
                    validate_json10_response(self.spec, 200, invalid, sqs_operation='DeleteMessageBatch')
        broken = copy.deepcopy(self.spec)
        media = broken['paths']['/']['post']['responses']['200']['content'][MEDIA]
        media['schema']['anyOf'] = [branch for branch in media['schema']['anyOf']
                                   if not branch.get('$ref', '').endswith('/SQSDeleteMessageBatchResult')]
        with self.assertRaises(ValidationError):
            validate_json10_response(broken, 200, {'Successful': [success], 'Failed': []})

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
        self.assertEqual(set(media['examples']), {
            'SSMGetParameter', 'SSMDeleteParameters', 'KMSCreateKey', 'KMSDescribeKey', 'KMSListKeys',
            'KMSEnableKey', 'KMSDisableKey', 'KMSGetKeyPolicy', 'KMSEncrypt', 'KMSDecrypt',
            'LogsCreateLogGroup', 'LogsDescribeLogGroups', 'LogsDeleteLogGroup',
            'LogsCreateLogStream', 'LogsDeleteLogStream', 'LogsDescribeLogStreams', 'LogsPutLogEvents', 'LogsGetLogEvents',
            'CreateSecret', 'DescribeSecret', 'ListSecrets', 'PutSecretValue',
            'GetSecretValue', 'UpdateSecretVersionStage', 'DeleteSecret',
            'KMSCreateAlias', 'KMSListAliases', 'KMSUpdateAlias', 'KMSDeleteAlias',
            'ListSecretVersionIds', 'UpdateSecret',
            'AutoScalingCreateScalingPlan', 'AutoScalingDescribeScalingPlans',
            'AutoScalingDescribeScalingPlanResources', 'AutoScalingUpdateScalingPlan',
            'AutoScalingDeleteScalingPlan',
        })
        self.assertEqual(len(media['examples']), 36)
        for example in media['examples'].values():
            shared.validate(example['value'])

    def test_autoscaling_plan_templates_match_strict_target_schemas(self):
        validate_autoscaling_plans(self.spec)
        operation = self.spec['paths']['/']['post']
        requests = operation['requestBody']['content']['application/x-amz-json-1.1']['examples']
        responses = operation['responses']['200']['content']['application/x-amz-json-1.1']['examples']
        mapping = operation['x-simulator-autoscaling-plans-operation-schemas']
        self.assertEqual(set(mapping), {'AnyScaleScalingPlannerFrontendService.' + name for name in (
            'CreateScalingPlan', 'DescribeScalingPlans', 'DescribeScalingPlanResources',
            'UpdateScalingPlan', 'DeleteScalingPlan', 'GetScalingPlanResourceForecastData')})
        self.assertIn('501', mapping['AnyScaleScalingPlannerFrontendService.GetScalingPlanResourceForecastData']['unsupported'])
        for target, contract in mapping.items():
            if 'request' not in contract:
                continue
            name = 'AutoScaling' + target.rsplit('.', 1)[1]
            self.assertEqual(requests[name]['summary'], 'X-Amz-Target: ' + target)
            validator(self.spec, {'$ref': contract['request']}).validate(requests[name]['value'])
            validator(self.spec, {'$ref': contract['response']}).validate(responses[name]['value'])
        create = requests['AutoScalingCreateScalingPlan']['value']
        strict = validator(self.spec, {'$ref': mapping['AnyScaleScalingPlannerFrontendService.CreateScalingPlan']['request']})
        instruction = create['ScalingInstructions'][0]
        invalid = [{**create, 'Extra': 1}, {**create, 'ScalingPlanName': 'a/b'}, {**create, 'ScalingPlanName': 'a:b'},
                   {**create, 'ScalingPlanName': 'a|b'}, {**create, 'ScalingInstructions': []},
                   {**create, 'ApplicationSource': {}},
                   {**create, 'ApplicationSource': {'TagFilters': [{'Key': 'a'}], 'CloudFormationStackARN': 'arn:aws:cloudformation:us-east-1:000000000000:stack/s/1'}}]
        for field, value in (('ServiceNamespace', 'autoscaling'), ('ResourceId', 'table/t/index/i'),
                             ('ScalableDimension', 'dynamodb:index:ReadCapacityUnits'), ('MinCapacity', 0),
                             ('MaxCapacity', 40001), ('PredictiveScalingMode', 'ForecastOnly'),
                             ('TargetTrackingConfigurations', [instruction['TargetTrackingConfigurations'][0]] * 2)):
            invalid.append({**create, 'ScalingInstructions': [{**instruction, field: value}]})
        tracking = instruction['TargetTrackingConfigurations'][0]
        for field, value in (('EstimatedInstanceWarmup', 60), ('CustomizedScalingMetricSpecification', {}), ('ScaleInCooldown', -1)):
            invalid.append({**create, 'ScalingInstructions': [{**instruction, 'TargetTrackingConfigurations': [{**tracking, field: value}]}]})
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(ValidationError):
                strict.validate(value)
        describe = validator(self.spec, {'$ref': mapping['AnyScaleScalingPlannerFrontendService.DescribeScalingPlans']['request']})
        for value in ({'ScalingPlanNames': ['a'], 'ApplicationSources': [create['ApplicationSource']]},
                      {'ScalingPlanVersion': 1}, {'MaxResults': 51}, {'ScalingPlanNames': ['a'], 'ScalingPlanVersion': 2}):
            with self.subTest(value=value), self.assertRaises(ValidationError):
                describe.validate(value)
        empty = validator(self.spec, {'$ref': mapping['AnyScaleScalingPlannerFrontendService.DeleteScalingPlan']['response']})
        with self.assertRaises(ValidationError):
            empty.validate({'ScalingPlanVersion': 1})

    def test_update_secret_template_uses_strict_metadata_only_contract(self):
        operation = self.spec['paths']['/']['post']
        media = operation['requestBody']['content']['application/x-amz-json-1.1']
        example = media['examples']['UpdateSecret']
        mapping = operation['x-simulator-secrets-operation-schemas']['secretsmanager.UpdateSecret']
        self.assertEqual(example['summary'], 'X-Amz-Target: secretsmanager.UpdateSecret (description only)')
        strict = validator(self.spec, {'$ref': mapping['request']})
        strict.validate(example['value'])
        base = example['value']
        for description in ('', 'x' * 2048):
            strict.validate({**base, 'Description': description})
        for token in ('a' * 32, 'a' * 64):
            strict.validate({**base, 'ClientRequestToken': token})
        invalid = [None, [], {}, {'SecretId': base['SecretId']}, {'Description': ''}]
        invalid += [{**base, 'Description': value} for value in (None, True, 1, 'x' * 2049)]
        invalid += [{**base, 'SecretId': value} for value in (None, '', True, 'x' * 2049)]
        invalid += [{**base, 'ClientRequestToken': value} for value in (None, '', 'a' * 31, 'a' * 65)]
        invalid += [{**base, field: 'unsupported'} for field in
                    ('SecretString', 'SecretBinary', 'KmsKeyId', 'Type', 'VersionStages', 'UnknownSetting')]
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(ValidationError):
                strict.validate(value)
        response = operation['responses']['200']['content']['application/x-amz-json-1.1']['examples']['UpdateSecret']['value']
        output = validator(self.spec, {'$ref': mapping['response']})
        output.validate(response)
        for field in ('VersionId', 'VersionStages', 'SecretString', 'SecretBinary', 'KmsKeyId'):
            with self.subTest(field=field), self.assertRaises(ValidationError):
                output.validate({**response, field: 'unsupported'})

    def test_delete_log_stream_template_has_exact_target_and_strict_request(self):
        media = self.spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']
        example = media['examples']['LogsDeleteLogStream']
        ref = {'$ref': '#/components/schemas/LogsDeleteLogStreamRequest'}
        self.assertEqual(example['x-request-schema'], ref)
        self.assertIn('X-Amz-Target: Logs_20140328.DeleteLogStream', example['summary'])
        self.assertEqual(media['schema']['anyOf'].count(ref), 1)
        strict = validator(self.spec, ref)
        strict.validate(example['value'])
        for required in ('logGroupName', 'logStreamName'):
            missing = copy.deepcopy(example['value'])
            del missing[required]
            for invalid in (missing, {**example['value'], required: None},
                            {**example['value'], required: ''},
                            {**example['value'], required: True},
                            {**example['value'], required: 'x' * 513}):
                with self.subTest(required=required, invalid=invalid), self.assertRaises(ValidationError):
                    strict.validate(invalid)
        with self.assertRaises(ValidationError):
            strict.validate({**example['value'], 'UnsupportedSetting': True})


    def test_alias_templates_match_each_literal_target_schema(self):
        media = self.spec['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']
        for name, schema_name, target in (
                ('KMSCreateAlias', 'KMSCreateAliasInput', 'TrentService.CreateAlias'),
                ('KMSListAliases', 'KMSListAliasesInput', 'TrentService.ListAliases'),
                ('KMSUpdateAlias', 'KMSUpdateAliasInput', 'TrentService.UpdateAlias'),
                ('KMSDeleteAlias', 'KMSDeleteAliasInput', 'TrentService.DeleteAlias')):
            with self.subTest(name=name):
                example = media['examples'][name]
                self.assertEqual(example['summary'], 'X-Amz-Target: ' + target)
                schema = validator(self.spec, {'$ref': '#/components/schemas/' + schema_name})
                schema.validate(example['value'])
                for invalid in (None, [], {**example['value'], 'UnknownSetting': True}):
                    with self.assertRaises(ValidationError):
                        schema.validate(invalid)
                for required in self.spec['components']['schemas'][schema_name].get('required', []):
                    missing = copy.deepcopy(example['value'])
                    del missing[required]
                    with self.assertRaises(ValidationError):
                        schema.validate(missing)

    def test_alias_target_name_and_paging_boundaries_are_not_weakened(self):
        key = '11111111-1111-4111-8111-111111111111'
        for name in ('KMSCreateAliasInput', 'KMSUpdateAliasInput'):
            schema = validator(self.spec, {'$ref': '#/components/schemas/' + name})
            schema.validate({'AliasName': 'alias/Practice_1', 'TargetKeyId': key})
            schema.validate({'AliasName': 'alias/practice',
                             'TargetKeyId': 'arn:aws:kms:us-east-1:000000000000:key/' + key})
            for alias in (None, '', 'practice', 'alias/', 'alias/aws/reserved', 'alias/' + 'x' * 251):
                with self.subTest(schema=name, alias=alias), self.assertRaises(ValidationError):
                    schema.validate({'AliasName': alias, 'TargetKeyId': key})
            for target in (None, 1, '', 'alias/other', 'not-a-key'):
                with self.subTest(schema=name, target=target), self.assertRaises(ValidationError):
                    schema.validate({'AliasName': 'alias/practice', 'TargetKeyId': target})
        schema = validator(self.spec, {'$ref': '#/components/schemas/KMSDeleteAliasInput'})
        schema.validate({'AliasName': 'alias/practice'})
        for alias in (None, '', 'alias/aws/reserved', 'practice'):
            with self.subTest(alias=alias), self.assertRaises(ValidationError):
                schema.validate({'AliasName': alias})
        schema = validator(self.spec, {'$ref': '#/components/schemas/KMSListAliasesInput'})
        for valid in ({}, {'Limit': 1}, {'Limit': 100}, {'KeyId': key}, {'Marker': 'opaque'}):
            schema.validate(valid)
        for invalid in ({'Limit': 0}, {'Limit': 101}, {'Limit': True}, {'Limit': '1'},
                        {'Marker': ''}, {'Marker': None}, {'Marker': 'x' * 1025},
                        {'KeyId': 'alias/other'}):
            with self.subTest(invalid=invalid), self.assertRaises(ValidationError):
                schema.validate(invalid)

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
