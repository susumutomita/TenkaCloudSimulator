"""Detector controls: Auto Scaling Plans additions are exact and inherited hashes stay intact."""
import copy
import json
from pathlib import Path
import unittest

from autoscaling_plans_openapi import (EXAMPLES, JSON11, SCHEMAS, TAG,
    project_before_autoscaling_plans, validate_autoscaling_plans)
from route53_openapi import project_before_route53
from test_sakura_openapi import preserved


class AutoScalingPlansReference(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.spec = json.loads((Path(__file__).resolve().parents[2] / 'internal/server/apidocs/openapi.json').read_text())

    def test_exact_extension_and_inherited_contract(self):
        validate_autoscaling_plans(self.spec)
        preserved(self.spec)
        projected = project_before_autoscaling_plans(self.spec)
        root = projected['paths']['/']['post']
        self.assertFalse(set(EXAMPLES) & set(root['requestBody']['content'][JSON11]['examples']))
        self.assertFalse(SCHEMAS & set(projected['components']['schemas']))
        self.assertNotIn(TAG, [tag['name'] for tag in projected['tags']])
        self.assertEqual(projected['paths'].keys(), self.spec['paths'].keys())
        # The Route53 base projection also removes these later additions, and a
        # document that already lacks them is rejected instead of passing silently.
        base = project_before_route53(self.spec)
        self.assertFalse(SCHEMAS & set(base['components']['schemas']))
        self.assertFalse(set(EXAMPLES) & set(base['paths']['/']['post']['requestBody']['content'][JSON11]['examples']))
        with self.assertRaises(AssertionError):
            project_before_autoscaling_plans(projected)

    def test_detector_rejects_missing_changed_relaxed_and_extra_contracts(self):
        def root(spec):
            return spec['paths']['/']['post']
        def request(spec):
            return root(spec)['requestBody']['content'][JSON11]
        def response(spec):
            return root(spec)['responses']['200']['content'][JSON11]
        schemas = lambda spec: spec['components']['schemas']
        mapping = lambda spec: root(spec)['x-simulator-autoscaling-plans-operation-schemas']
        mutations = [
            ('missing request template', lambda s: request(s)['examples'].pop('AutoScalingCreateScalingPlan')),
            ('changed request template', lambda s: request(s)['examples']['AutoScalingUpdateScalingPlan']['value'].update(ScalingPlanName='other')),
            ('wrong target summary', lambda s: request(s)['examples']['AutoScalingDeleteScalingPlan'].update(summary='X-Amz-Target: other')),
            ('missing response template', lambda s: response(s)['examples'].pop('AutoScalingDescribeScalingPlans')),
            ('invented response field', lambda s: response(s)['examples']['AutoScalingCreateScalingPlan']['value'].update(ScalingPlanArn='fake')),
            ('missing schema', lambda s: schemas(s).pop('AutoScalingPlansScalingInstruction')),
            ('relaxed schema', lambda s: schemas(s)['AutoScalingPlansScalingInstruction'].update(additionalProperties=True)),
            ('widened namespace', lambda s: schemas(s)['AutoScalingPlansScalingInstruction']['properties']['ServiceNamespace'].update(const='ec2')),
            ('unreviewed schema', lambda s: schemas(s).update(AutoScalingPlansForecastInput={'type': 'object'})),
            ('unreviewed template', lambda s: request(s)['examples'].update(AutoScalingGetForecast={'value': {}})),
            ('missing mapping', lambda s: mapping(s).pop('AnyScaleScalingPlannerFrontendService.CreateScalingPlan')),
            ('weakened permission', lambda s: mapping(s)['AnyScaleScalingPlannerFrontendService.CreateScalingPlan'].update(permission='autoscaling-plans:CreateScalingPlan on *')),
            ('forecast claimed', lambda s: mapping(s)['AnyScaleScalingPlannerFrontendService.GetScalingPlanResourceForecastData'].update(unsupported='supported')),
            ('missing request union', lambda s: request(s)['schema']['anyOf'].pop()),
            ('duplicate request union', lambda s: request(s)['schema']['anyOf'].append({'$ref': '#/components/schemas/AutoScalingPlansDeleteScalingPlanInput'})),
            ('missing response union', lambda s: response(s)['schema']['anyOf'].pop()),
            ('changed tag', lambda s: next(t for t in s['tags'] if t['name'] == TAG).update(description='Forecasts supported')),
            ('missing tag group', lambda s: next(g for g in s['x-tagGroups'] if g['name'] == 'AWS')['tags'].remove(TAG)),
            ('invented path', lambda s: s['paths'].update({'/autoscaling-plans': {}})),
            ('changed inherited secret', lambda s: request(s)['examples']['DescribeSecret']['value'].update(SecretId='changed')),
        ]
        for label, mutate in mutations:
            broken = copy.deepcopy(self.spec)
            mutate(broken)
            with self.subTest(label=label), self.assertRaises(AssertionError):
                preserved(broken)


if __name__ == '__main__':
    unittest.main()
