"""Bounded CFN/EC2 native SDK contracts; see docs/ec2-network-contracts.md.

Starts only a disposable authenticated loopback simulator via the existing E2E
harness. No VM, real AWS, emulator comparison, or external endpoint argument.
Run after make build with the dependencies in scripts/requirements-test.txt.
"""
import copy
import json
from pathlib import Path
import unittest

from check import running, boto3, Config, ClientError
from botocore.parsers import ResponseParserError


FIXTURE = Path(__file__).with_name('fixtures') / 'ec2-network-contract.json'


class NetworkContract(unittest.TestCase):
    def setUp(self):
        self.endpoint, self.token = self.enterContext(running(authenticated=True))
        self.cfn = self.client('cloudformation')
        self.ec2 = self.client('ec2')
        self.template = json.loads(FIXTURE.read_text())

    def client(self, service, region='us-east-1', token=True):
        client = boto3.client(
            service, endpoint_url=self.endpoint, region_name=region,
            aws_access_key_id='TCSIM_NETWORK_CONTRACT',
            aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts': 0}, connect_timeout=3,
                          read_timeout=10, proxies={}))
        if token:
            client.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', self.token))
        self.addCleanup(client.close)
        return client

    def outputs(self, name):
        stack = self.cfn.describe_stacks(StackName=name)['Stacks'][0]
        self.assertEqual(stack['StackStatus'], 'CREATE_COMPLETE')
        return {item['OutputKey']: item['OutputValue'] for item in stack['Outputs']}

    def create(self, name, template=None):
        result = self.cfn.create_stack(StackName=name, TemplateBody=json.dumps(template or self.template))
        # Register before assertions: even a malformed success must be cleaned up.
        self.addCleanup(self.delete_if_present, result['StackId'])
        return result['StackId']

    def delete_if_present(self, stack_id):
        # Explicit deletes cancel cleanup by proving absence through CFN first.
        names = {s['StackId'] for s in self.cfn.describe_stacks()['Stacks']}
        if stack_id in names:
            self.cfn.delete_stack(StackName=stack_id)

    def rejected(self, operation, code, **kwargs):
        with self.assertRaises(ClientError) as caught:
            operation(**kwargs)
        self.assertTrue(caught.exception.response['Error']['Code'])
        if code is not None:
            self.assertEqual(caught.exception.response['Error']['Code'], code)
        self.assertGreaterEqual(caught.exception.response['ResponseMetadata']['HTTPStatusCode'], 400)

    def state(self):
        # Ignore response metadata (request IDs), retain every exposed resource field.
        stacks = self.cfn.describe_stacks()['Stacks']
        return (sorted(stacks, key=lambda s: s['StackId']),
                sorted(self.ec2.describe_vpcs()['Vpcs'], key=lambda v: v['VpcId']))

    def test_refs_canonical_cidr_and_scoped_cleanup(self):
        survivor = self.create('network-survivor')
        survivor_outputs = self.outputs(survivor)
        selected = self.create('network-selected')
        outputs = self.outputs(selected)
        self.assertNotEqual(outputs['Vpc'], survivor_outputs['Vpc'])
        self.assertEqual(outputs['Cidr'], '10.67.0.0/16')
        self.assertEqual(outputs['SubnetVpc'], outputs['Vpc'])
        self.assertEqual(outputs['TableAttribute'], outputs['Table'])
        self.assertEqual(outputs['AssociationAttribute'], outputs['Association'])
        for key, prefix in [('Vpc', 'vpc-'), ('Subnet', 'subnet-'),
                            ('Table', 'rtb-'), ('Association', 'rtbassoc-')]:
            self.assertTrue(outputs[key].startswith(prefix), (key, outputs[key]))
        vpcs = {v['VpcId']: v for v in self.ec2.describe_vpcs()['Vpcs']}
        self.assertEqual(set(vpcs), {outputs['Vpc'], survivor_outputs['Vpc']})
        self.assertEqual(vpcs[outputs['Vpc']]['CidrBlock'], outputs['Cidr'])
        self.assertEqual(self.client('ec2', 'ap-northeast-1').describe_vpcs()['Vpcs'], [])
        self.cfn.delete_stack(StackName=selected)
        self.rejected(self.cfn.describe_stacks, 'ValidationError', StackName=selected)
        self.assertEqual(self.outputs(survivor), survivor_outputs)
        self.assertEqual([v['VpcId'] for v in self.ec2.describe_vpcs()['Vpcs']], [survivor_outputs['Vpc']])
        self.cfn.delete_stack(StackName=survivor)
        self.assertEqual(self.state(), ([], []))

    def test_invalid_network_create_preserves_existing_state(self):
        self.create('network-survivor')
        before = self.state()
        cases = {}
        for label, key, value in [
            ('two-targets', 'NatGatewayId', 'nat-00000000000000001'),
            ('two-destinations', 'DestinationIpv6CidrBlock', '2001:db8::/64'),
            ('wrong-destination-type', 'DestinationCidrBlock', 12),
            ('absent-table', 'RouteTableId', 'rtb-00000000000000001'),
            ('absent-gateway', 'GatewayId', 'igw-00000000000000001'),
        ]:
            body = copy.deepcopy(self.template)
            body['Resources']['Route']['Properties'][key] = value
            cases[label] = body
        body = copy.deepcopy(self.template)
        del body['Resources']['Route']['Properties']['GatewayId']
        cases['missing-target'] = body
        body = copy.deepcopy(self.template)
        body['Resources']['OtherVpc'] = {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.68.0.0/16'}}
        body['Resources']['Table']['Properties']['VpcId'] = {'Ref': 'OtherVpc'}
        # Isolate the association constraint: a route through the first VPC's
        # gateway would independently make this template invalid.
        del body['Resources']['Route']
        cases['cross-vpc-association'] = body
        for label, body in cases.items():
            with self.subTest(label=label):
                # Relationship failures must reject atomically. Do not bless the
                # observed InternalFailure classification as an AWS contract.
                code = None if label in ('absent-table', 'absent-gateway', 'cross-vpc-association') else 'ValidationError'
                self.rejected(self.cfn.create_stack, code, StackName='invalid-' + label, TemplateBody=json.dumps(body))
                self.assertEqual(self.state(), before)
        # Reuse a rejected name: no hidden stack/name claim may remain.
        self.create('invalid-absent-table')

    def test_unsupported_native_routes_never_report_success_or_mutate(self):
        selected = self.create('network-selected')
        outputs = self.outputs(selected)
        before = self.state()
        for operation, args in [
            (self.ec2.create_route, {'RouteTableId': outputs['Table'], 'DestinationCidrBlock': '0.0.0.0/0', 'GatewayId': 'igw-00000000000000001'}),
            (self.ec2.create_route, {'RouteTableId': outputs['Table'], 'DestinationCidrBlock': '0.0.0.0/0', 'GatewayId': 'igw-00000000000000001', 'DryRun': True}),
            (self.ec2.describe_route_tables, {}),
        ]:
            with self.subTest(operation=operation.__name__, args=args):
                self.rejected(operation, 'UnsupportedOperation', **args)
                self.assertEqual(self.state(), before)

    def test_missing_local_token_cannot_create_network(self):
        before = self.state()
        client = self.client('cloudformation', token=False)
        statuses = []
        client.meta.events.register('before-parse.cloudformation.CreateStack',
                                    lambda response_dict, **_: statuses.append(response_dict['status_code']))
        # Local token middleware returns HTTP 401 text, not AWS Query XML.
        # Preserve the SDK parse failure and verify the HTTP denial separately.
        with self.assertRaises(ResponseParserError):
            client.create_stack(StackName='unauthorized-network', TemplateBody=json.dumps(self.template))
        self.assertEqual(statuses, [401])
        self.assertEqual(self.state(), before)


if __name__ == '__main__':
    unittest.main(verbosity=2)
