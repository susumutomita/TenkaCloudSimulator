import copy
import hashlib
import json
from pathlib import Path
import unittest

from cloudwatch_reference_projection import ACTIONS, project_monitoring
from openapi_json10 import validator


class MonitoringQueryReference(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = Path(__file__).resolve().parents[2] / 'internal/server/apidocs/openapi.json'
        cls.spec = json.loads(path.read_bytes())

    def test_native_presets_and_prior_contract(self):
        post = self.spec['paths']['/']['post']
        media = post['requestBody']['content']['application/x-www-form-urlencoded']
        for action in ACTIONS:
            example = media['examples'][action]
            validator(self.spec, media['schema']).validate(example['value'])
            self.assertEqual((example['value']['Action'], example['value']['Version']), (action, '2010-08-01'))
        restored = copy.deepcopy(self.spec)
        project_monitoring(restored)
        original = restored['paths']['/']['post']['requestBody']['content']['application/x-www-form-urlencoded']['examples']
        # The complete published baseline presets remain available even when other services add new ones.
        original_names = ('DescribeStacks CreateRole GetRole ListRoles DeleteRole PutRolePolicy GetRolePolicy '
                          'DeleteRolePolicy ListRolePolicies UpdateAssumeRolePolicy CreateRESTStack CreatePolicy '
                          'GetPolicy ListPolicies DeletePolicy CreatePolicyVersion GetPolicyVersion ListPolicyVersions '
                          'SetDefaultPolicyVersion DeletePolicyVersion AttachRolePolicy DetachRolePolicy ListAttachedRolePolicies '
                          'CreateInstanceProfile GetInstanceProfile ListInstanceProfiles DeleteInstanceProfile AddRoleToInstanceProfile '
                          'RemoveRoleFromInstanceProfile ListInstanceProfilesForRole ListDomains Select DeleteAttributes DomainMetadata '
                          'GetAttributes DescribeInstances DescribeInternetGateways DescribeRouteTables CreateDomain').split()
        self.assertEqual([name for name in original if name in original_names], original_names)
        ordered = [(name, original[name]) for name in original_names]
        digest = hashlib.sha256(json.dumps(ordered, sort_keys=True, ensure_ascii=False, separators=(',', ':')).encode()).hexdigest()
        self.assertEqual(digest, '2e494cacf978f0a0d981c137172ae9a6fb230867b1d4c813bf75cce53fee9d9c')

    def test_projection_rejects_changed_contract(self):
        for control in ('default-service', 'scope', 'missing-field', 'metric-value', 'reordered', 'extra-header-option'):
            with self.subTest(control=control):
                changed = copy.deepcopy(self.spec)
                post = changed['paths']['/']['post']
                media = post['requestBody']['content']['application/x-www-form-urlencoded']
                if control == 'default-service':
                    next(p for p in post['parameters'] if p.get('name') == 'X-Simulator-Service')['schema']['enum'].remove('monitoring')
                elif control == 'scope':
                    media['examples']['DescribeAlarms']['value']['AlarmNames.member.1'] = 'other'
                elif control == 'missing-field':
                    media['schema']['properties']['MetricData.member.1.Unit'] = {'type':'integer'}
                elif control == 'metric-value':
                    media['examples']['PutMetricData']['value']['MetricData.member.1.Value'] = '22'
                elif control == 'reordered':
                    media['examples']['PutMetricData'] = media['examples'].pop('PutMetricData')
                else:
                    next(p for p in post['parameters'] if p.get('name') == 'X-Simulator-Service')['schema']['enum'].append('monitoring')
                with self.assertRaises(AssertionError):
                    project_monitoring(changed)
