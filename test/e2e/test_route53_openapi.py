"""Detector controls preserve every inherited hash and validate new details."""
import copy
import json
import os
import runpy
from unittest.mock import patch
from pathlib import Path
import unittest
from route53_openapi import validate_route53, project_before_route53, ROUTE53_PATHS, ROUTE53_IDS
from test_sakura_openapi import preserved

class Route53Reference(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.spec=json.loads((Path(__file__).resolve().parents[2]/'internal/server/apidocs/openapi.json').read_text())

    def test_exact_extension_and_inherited_contract(self):
        validate_route53(self.spec)
        preserved(self.spec)
        projected=project_before_route53(self.spec)
        self.assertEqual(set(self.spec['paths'])-set(projected['paths']),ROUTE53_PATHS)

    def test_console_tag_correction_is_exact_and_preserves_path_hashes(self):
        import route53_openapi
        validate_route53(self.spec)
        self.assertEqual(route53_openapi.CONTRACT_SHA256,
                         '1210ce75c2af0b81df6acb18207f1a027627d48c0c75932b34a9792d154e7388')
        current = next(tag for tag in self.spec['tags'] if tag['name'] == route53_openapi.TAG)
        self.assertIn('A dedicated Route53 Console is available.', current['description'])
        stale = 'Bounded public zones and saved-state answers only. No external DNS, private VPC zones, aliases, routing policies, console or CloudFormation.'
        for description in (stale, current['description'] + ' External DNS is available.'):
            broken = copy.deepcopy(self.spec)
            next(tag for tag in broken['tags'] if tag['name'] == route53_openapi.TAG)['description'] = description
            with self.subTest(description=description), self.assertRaises(AssertionError):
                validate_route53(broken)

    def test_historical_console_tag_projection_checks_current_contract(self):
        from route53_openapi import TAG, PRE_CONSOLE_DESCRIPTION, project_before_route53_console
        from test_sakura_openapi import project_server_list

        document = copy.deepcopy(self.spec)
        live = project_server_list(document)
        self.assertEqual(document, self.spec)
        self.assertEqual(live['tags'], [tag for tag in document['tags'] if tag['name'] != 'Sakura Server metadata'])
        expected = copy.deepcopy(document)
        tag = next(tag for tag in expected['tags'] if tag['name'] == TAG)
        tag['description'] = PRE_CONSOLE_DESCRIPTION
        project_before_route53_console(document)
        self.assertEqual(document, expected)

        for mutation in ('missing', 'duplicate', 'stale', 'extra field'):
            candidate = copy.deepcopy(self.spec)
            tag = next(tag for tag in candidate['tags'] if tag['name'] == TAG)
            if mutation == 'missing':
                candidate['tags'].remove(tag)
            elif mutation == 'duplicate':
                candidate['tags'].append(copy.deepcopy(tag))
            elif mutation == 'stale':
                tag['description'] = PRE_CONSOLE_DESCRIPTION
            else:
                tag['unexpected'] = True
            with self.subTest(mutation=mutation), self.assertRaises(AssertionError):
                project_before_route53_console(candidate)

    @staticmethod
    def remove_ptr_descriptor(spec):
        changes = spec['paths']['/2013-04-01/hostedzone/{Id}/rrset']['post']
        changes['description'] = changes['description'].replace('/PTR only', ' only')

    def test_contract_detector_rejects_missing_and_changed_details(self):
        collection='/2013-04-01/hostedzone'
        mutations = [
            ('missing path',lambda s:s['paths'].pop(collection)),
            ('missing PTR descriptor', lambda s: self.remove_ptr_descriptor(s)),
            ('missing operation',lambda s:s['paths'][collection].pop('post')),
            ('changed operation',lambda s:s['paths'][collection]['post'].update(operationId='InventedCreate')),
            ('missing schema',lambda s:s['paths'][collection]['post']['requestBody']['content']['application/xml'].pop('schema')),
            ('changed schema',lambda s:s['paths'][collection]['post']['requestBody']['content']['application/xml']['schema'].update(type='object')),
            ('missing request',lambda s:s['paths'][collection]['post'].pop('requestBody')),
            ('changed request example',lambda s:s['paths'][collection]['post']['requestBody']['content']['application/xml'].update(example='<Wrong/>')),
            ('missing response',lambda s:s['paths'][collection]['post']['responses'].pop('201')),
            ('changed response schema',lambda s:s['paths'][collection]['get']['responses']['200']['content']['text/xml']['schema'].update(type='object')),
            ('changed query',lambda s:s['paths']['/2013-04-01/testdnsanswer']['get']['parameters'][0].update(required=False)),
            ('extra route',lambda s:s['paths'].update({'/2013-04-01/fiction': {'get':{'operationId':'Fiction'}}})),
        ]
        for label,mutate in mutations:
            broken=copy.deepcopy(self.spec);mutate(broken)
            with self.subTest(label=label),self.assertRaises(AssertionError):
                preserved(broken)

    def test_assets_path_rejects_missing_and_changed_route53_contract(self):
        root=Path(__file__).resolve().parents[2]
        original_read_text=Path.read_text
        original_cwd=Path.cwd()
        collection='/2013-04-01/hostedzone'
        mutations=[
            ('missing path',lambda s:s['paths'].pop(collection)),
            ('changed response',lambda s:s['paths'][collection]['post']['responses']['201'].update(description='Unreviewed response')),
        ]
        try:
            os.chdir(root)
            for label,mutate in mutations:
                broken=copy.deepcopy(self.spec);mutate(broken)
                def read_text(path,*args,**kwargs):
                    if path.resolve()==root/'internal/server/apidocs/openapi.json':
                        return json.dumps(broken)
                    return original_read_text(path,*args,**kwargs)
                with self.subTest(label=label),patch.object(Path,'read_text',read_text):
                    with self.assertRaisesRegex(AssertionError,'Missing Route53 native path|Route53 exact path/request/response/schema contract changed'):
                        runpy.run_path(str(root/'scripts/check-assets.py'),run_name='__main__')
        finally:
            os.chdir(original_cwd)

if __name__=='__main__':
    unittest.main()
