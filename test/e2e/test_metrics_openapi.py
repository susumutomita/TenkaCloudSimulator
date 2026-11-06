# SPDX-License-Identifier: MIT
"""Pure reference/schema detectors; no clients, network or served PASS claim."""
import copy
import json
from pathlib import Path
import unittest
from xml.etree import ElementTree as ET
from jsonschema import Draft202012Validator
from metrics_openapi import contract, preserved_metrics_reference, MEDIA, TAG, ACTIONS
from metrics_openapi import canonical

def digest(value):
    import hashlib
    return hashlib.sha256(canonical(value).encode()).hexdigest()

preserved_reference = preserved_metrics_reference

SPEC = Path(__file__).resolve().parents[2]/'internal/server/apidocs/openapi.json'
XML = '{http://monitoring.amazonaws.com/doc/2010-08-01/}'

class MetricsReference(unittest.TestCase):
    def setUp(self):
        self.spec = json.loads(SPEC.read_text())
        self.c = contract()
    def guard(self,spec):
        return preserved_reference(spec)
    def reject(self,edit):
        bad=copy.deepcopy(self.spec);edit(bad)
        with self.assertRaises((AssertionError,KeyError)):self.guard(bad)
    def branch_matches(self,spec,value):
        schemas=spec['components']['schemas']
        branches=spec['paths']['/']['post']['requestBody']['content'][MEDIA]['schema']['oneOf']
        resolved=[schemas[x['$ref'].rsplit('/',1)[1]] if '$ref' in x else x for x in branches]
        for schema in resolved:Draft202012Validator.check_schema(schema)
        return [Draft202012Validator(schema).is_valid(value) for schema in resolved]
    def test_closed66_36_and_exact_public_inverse(self):
        unchanged=copy.deepcopy(self.spec)
        prior=preserved_metrics_reference(self.spec)
        self.assertEqual(digest(prior),self.c['base66CanonicalSHA256'])
        main=self.guard(self.spec)
        self.assertEqual((len(self.spec['paths']),len(prior['paths']),len(main['paths'])),(36,36,36))
        self.assertEqual(self.spec,unchanged)
    def test_all_three_examples_match_exactly_one_real_schema_branch(self):
        for example in self.c['examples'].values():
            with self.subTest(action=example['value']['Action']):
                matches=self.branch_matches(self.spec,example['value'])
                self.assertFalse(matches[0]);self.assertEqual(sum(matches),1)
    def test_generic_overlap_negative_is_real_and_rejected(self):
        bad=copy.deepcopy(self.spec)
        old=bad['paths']['/']['post']['requestBody']['content'][MEDIA]['schema']['oneOf'][0]
        old['properties']['Action']['enum'].append('PutMetricData')
        old['properties']['Version']['enum'].append('2010-08-01')
        value=self.c['examples']['CloudWatchPutMetricData']['value']
        self.assertEqual(sum(self.branch_matches(bad,value)),2,'Negative fixture must actually overlap')
        with self.assertRaises(AssertionError):self.guard(bad)
    def test_extra_metric_field_and_boolean_number_refused_by_schema(self):
        for edit in (lambda x:x.update(MaxResults=1),lambda x:x.update({'MetricData.member.1.Value':False})):
            value=copy.deepcopy(self.c['examples']['CloudWatchPutMetricData']['value']);edit(value)
            self.assertEqual(sum(self.branch_matches(self.spec,value)),0)
    def test_unsupported_storage_resolution_and_statistic_refused(self):
        for key,field,value in [('CloudWatchPutMetricData','MetricData.member.1.StorageResolution',1),('CloudWatchGetMetricStatistics','Statistics.member.1','p99')]:
            request=copy.deepcopy(self.c['examples'][key]['value']);request[field]=value
            self.assertEqual(sum(self.branch_matches(self.spec,request)),0)
    def test_required_native_example_fields_are_not_optional(self):
        for key,field in [('CloudWatchPutMetricData','Namespace'),('CloudWatchPutMetricData','MetricData.member.1.Value'),('CloudWatchGetMetricStatistics','StartTime'),('CloudWatchGetMetricStatistics','MetricName')]:
            request=copy.deepcopy(self.c['examples'][key]['value']);del request[field]
            with self.subTest(action=key,field=field):
                self.assertEqual(sum(self.branch_matches(self.spec,request)),0)
    def test_each_missing_new_example_or_schema_refused(self):
        for name in self.c['examples']:
            self.reject(lambda s:s['paths']['/']['post']['requestBody']['content'][MEDIA]['examples'].pop(name))
        for name in self.c['schemaNames']:
            self.reject(lambda s:s['components']['schemas'].pop(name))
    def test_new_provider_assignment_refused(self):
        def move(s):
            next(g for g in s['x-tagGroups'] if g['name']=='AWS')['tags'].remove(TAG)
            next(g for g in s['x-tagGroups'] if g['name']=='Azure')['tags'].append(TAG)
        self.reject(move)
    def test_missing_shared_root_post_is_validated_refusal(self):
        for guard in (preserved_metrics_reference, self.guard):
            bad=copy.deepcopy(self.spec)
            bad['paths']['/'].pop('post')
            unchanged=copy.deepcopy(bad)
            with self.subTest(guard=guard.__name__):
                with self.assertRaisesRegex(AssertionError, 'Shared AWS root POST operation is required'):
                    guard(bad)
                self.assertEqual(bad,unchanged,'Rejected projection must not rewrite its input')
    def test_missing_unknown_or_duplicate_aws_group_is_validated_refusal(self):
        def missing(s):s['x-tagGroups'][:]=[g for g in s['x-tagGroups'] if g['name']!='AWS']
        def unknown(s):next(g for g in s['x-tagGroups'] if g['name']=='AWS')['name']='Another cloud'
        def duplicate(s):s['x-tagGroups'].append(copy.deepcopy(next(g for g in s['x-tagGroups'] if g['name']=='AWS')))
        for edit in (missing,unknown,duplicate):
            for guard in (preserved_metrics_reference,self.guard):
                bad=copy.deepcopy(self.spec);edit(bad);unchanged=copy.deepcopy(bad)
                with self.subTest(edit=edit.__name__,guard=guard.__name__):
                    with self.assertRaisesRegex(AssertionError,'Exactly one AWS cloud group is required'):
                        guard(bad)
                    self.assertEqual(bad,unchanged,'Rejected projection must not rewrite its input')
    def test_malformed_cloud_group_shape_is_validated_refusal(self):
        for value in (None,{},[None],[{'name':'AWS'}],[{'name':'AWS','tags':'CloudWatch Metrics'}]):
            bad=copy.deepcopy(self.spec);bad['x-tagGroups']=value
            with self.subTest(value=value):
                with self.assertRaisesRegex(AssertionError,'Cloud groups'):
                    preserved_metrics_reference(bad)
    def test_auth_and_routing_instructions_refused_if_removed(self):
        def token(s):
            p=s['paths']['/']['post']['parameters'];p[:]=[x for x in p if x.get('$ref')!='#/components/parameters/SimulatorToken']
        def hint(s):
            next(x for x in s['paths']['/']['post']['parameters'] if x.get('name')=='X-Simulator-Service')['schema']['enum'].remove('monitoring')
        self.reject(token);self.reject(hint)
        self.reject(lambda s:s['paths']['/']['post'].update(description=s['paths']['/']['post']['description'].replace('CLEAR X-Amz-Target','IGNORE X-Amz-Target')))
    def test_xml_examples_and_exact_error_status_codes(self):
        for status,media in self.c['responseMedia'].items():
            for example in media['examples'].values():
                root=ET.fromstring(example['value']);self.assertTrue(root.tag.startswith(XML))
                if status=='200':self.assertIn(root.tag.rsplit('}',1)[1],['PutMetricDataResponse','ListMetricsResponse','GetMetricStatisticsResponse'])
                else:
                    self.assertEqual(root.tag,XML+'ErrorResponse')
                    self.assertEqual(root.findtext(XML+'Error/'+XML+'Type'),'Receiver' if int(status)>=500 else 'Sender')
        self.reject(lambda s:s['paths']['/']['post']['responses']['500']['content']['text/xml']['examples']['CloudWatchStorageFailure'].update(value='<ErrorResponse/>'))
    def test_old_method_or_other_service_data_not_restored_over(self):
        def swap(s):
            a,b=s['paths']['/']['get'],s['paths']['/']['post'];a['operationId'],b['operationId']=b['operationId'],a['operationId']
        self.reject(swap)
        self.reject(lambda s:s['components']['schemas'].update(Unreviewed={'type':'string'}))
    def test_each_public_operation_still_required(self):
        original=preserved_metrics_reference(self.spec)
        methods={'get','head','put','post','delete','patch','options'}
        for path,item in original['paths'].items():
            for method in item:
                if method in methods:
                    with self.subTest(path=path,method=method):
                        self.reject(lambda s:s['paths'][path].pop(method))
    def test_unknown_metrics_path_or_operation_is_not_excluded(self):
        def add(s):
            s['paths']['/metrics-fake']={'post':{'operationId':'InventedMetricsAPI','responses':{'200':{'description':'fake'}}}}
        self.reject(add)

if __name__=='__main__':unittest.main()
