"""Strict definition reference and whole inherited API contract preservation."""
import copy
import hashlib
import json
from pathlib import Path
import unittest
from jsonschema import Draft202012Validator, ValidationError
from openapi_json10 import validator as reference_validator
from ssm_document_reference import project_ssm_documents, OPS
ROOT=Path(__file__).resolve().parents[2]
BASE_HASH='67d23ddc7a0325b066185d51c076d588a756c05ac6c6794ebe17f3def4ed0ce8'

def validate(spec):
    projected=copy.deepcopy(spec)
    project_ssm_documents(projected)
    assert hashlib.sha256(json.dumps(projected,sort_keys=True,separators=(',', ':'),ensure_ascii=False).encode()).hexdigest()==BASE_HASH

class DocumentReference(unittest.TestCase):
    def setUp(self):self.spec=json.loads((ROOT/'internal/server/apidocs/openapi.json').read_text())
    def test_preserved_and_strict_inputs(self):
        validate(self.spec)
        post=self.spec['paths']['/']['post']
        for op in OPS:
            schema=self.spec['components']['schemas']['SSM'+op+'Input']
            validator=Draft202012Validator(schema)
            example=post['requestBody']['content']['application/x-amz-json-1.1']['examples']['SSM'+op]['value']
            validator.validate(example)
            for bad in [dict(example,UnknownField=True),None,[],dict(example,DocumentFormat='YAML')]:
                with self.subTest(op=op,bad=bad), self.assertRaises(ValidationError):validator.validate(bad)
    def test_response_examples_are_typed_and_no_execution_result(self):
        post=self.spec['paths']['/']['post']
        for op in OPS:
            mapping=post['x-simulator-ssm-document-operation-schemas']['AmazonSSM.'+op]
            if op=='DeleteDocument':
                self.assertEqual(mapping['responseBody'],'empty HTTP 200')
                self.assertNotIn('response',mapping)
                continue
            check=reference_validator(self.spec,{'$ref':mapping['response']})
            sample=post['responses']['200']['content']['application/x-amz-json-1.1']['examples']['SSM'+op]['value']
            check.validate(sample)
            with self.assertRaises(ValidationError):check.validate(dict(sample,CommandId='not-supported'))

    def test_projection_cannot_hide_contract_or_inherited_changes(self):
        for mutate in [
            lambda s:s['components']['schemas']['SSMGetDocumentOutput'].update(additionalProperties=True),
            lambda s:s['components']['schemas']['SSMCreateDocumentInput'].update(additionalProperties=True),
            lambda s:s['paths']['/']['post'].pop('x-simulator-ssm-document-operation-schemas'),
            lambda s:s['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']['schema']['anyOf'].pop(),
            lambda s:s['paths']['/{bucket}']['put'].update(summary='drift'),
            lambda s:s['components']['schemas'].update(Unreviewed={'type':'string'}),
        ]:
            broken=copy.deepcopy(self.spec);mutate(broken)
            with self.assertRaises(AssertionError):validate(broken)

if __name__=='__main__':unittest.main()
