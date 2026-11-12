"""Opt-in RunCommand shapes and immutable inherited reference contract."""
import copy,hashlib,json,unittest
from pathlib import Path
from jsonschema import Draft202012Validator,ValidationError
from autoscaling_plans_openapi import project_before_autoscaling_plans
from ssm_command_reference import project_ssm_commands,OPS
from openapi_json10 import validator
ROOT=Path(__file__).resolve().parents[2]
BASE_HASH='a476756f5844114b6f5f45c4652da5c1fd6812b8488af674e19c94b64d2aed53'
class CommandReference(unittest.TestCase):
    def setUp(self): self.spec=json.loads((ROOT/'internal/server/apidocs/openapi.json').read_text())
    def test_inherited_reference_unchanged(self):
        p=project_before_autoscaling_plans(self.spec);project_ssm_commands(p)
        self.assertEqual(hashlib.sha256(json.dumps(p,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest(),BASE_HASH)
    def test_exact_inputs_and_no_prefilled_execution(self):
        examples={'SendCommand':{'DocumentName':'AWS-RunShellScript','InstanceIds':['i-0123456789abcdef0'],'Parameters':{'commands':['fixture-text-only']},'TimeoutSeconds':30},'GetCommandInvocation':{'CommandId':'01234567-89ab-4def-8123-456789abcdef','InstanceId':'i-0123456789abcdef0'},'ListCommands':{'Filters':[{'key':'InvokedAfter','value':'2026-11-11T00:00:00Z'}],'MaxResults':50}}
        for op in OPS:
            v=Draft202012Validator(self.spec['components']['schemas']['SSM'+op+'Input']);v.validate(examples[op])
            for bad in [None,[],dict(examples[op],Unknown=True)]:
                with self.subTest(op=op,bad=bad),self.assertRaises(ValidationError):v.validate(bad)
        v=Draft202012Validator(self.spec['components']['schemas']['SSMSendCommandInput'])
        for changes in [{'DocumentName':'stored-custom'},{'TimeoutSeconds':29},{'InstanceIds':['i-0123456789abcdef0']*2},{'Parameters':{'commands':['a'],'other':['b']}}]:
            with self.assertRaises(ValidationError):v.validate(dict(examples['SendCommand'],**changes))
        v.validate({k:x for k,x in examples['SendCommand'].items() if k!='TimeoutSeconds'})  # AWS default 3600 applies
        v=Draft202012Validator(self.spec['components']['schemas']['SSMListCommandsInput'])
        for bad in [{},{'Filters':None},dict(examples['ListCommands'],MaxResults=None),dict(examples['ListCommands'],MaxResults=51)]:
            with self.assertRaises(ValidationError):v.validate(bad)
        p=project_before_autoscaling_plans(self.spec);project_ssm_commands(p)
    def test_output_is_observation_not_invented_success(self):
        v=validator(self.spec,{'$ref':'#/components/schemas/SSMGetCommandInvocationOutput'})
        sample={'CommandId':'01234567-89ab-4def-8123-456789abcdef','InstanceId':'i-0123456789abcdef0','DocumentName':'AWS-RunShellScript','Comment':'','Status':'InProgress','StatusDetails':'InProgress','ResponseCode':-1,'StandardOutputContent':'','StandardErrorContent':''};v.validate(sample)
        for patch in [{'Status':'Unknown'},{'ResponseCode':None},{'Unreviewed':True}]:
            with self.assertRaises(ValidationError):v.validate(dict(sample,**patch))
    def test_projection_rejects_hidden_changes(self):
        for mutate in [lambda s:s['components']['schemas']['SSMSendCommandInput'].update(additionalProperties=True),lambda s:s['paths']['/']['post'].pop('x-simulator-ssm-command-operation-schemas'),lambda s:s['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']['schema']['anyOf'].pop(),lambda s:s['paths']['/']['post']['requestBody']['content']['application/x-amz-json-1.1']['examples'].update(SSMSendCommand={'value':{}})]:
            p=copy.deepcopy(self.spec);mutate(p)
            with self.assertRaises(AssertionError):project_ssm_commands(project_before_autoscaling_plans(p))
if __name__=='__main__':unittest.main()
