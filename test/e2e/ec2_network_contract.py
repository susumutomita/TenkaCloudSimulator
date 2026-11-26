"""Bounded CFN/EC2 native SDK contracts; see docs/ec2-network-contracts.md.

Starts only a disposable authenticated loopback simulator via the existing E2E
harness. No VM, real AWS, emulator comparison, or external endpoint argument.
Run after make build with the dependencies in scripts/requirements-test.txt.
"""
from contextlib import ExitStack
import copy
import json
from pathlib import Path
import tempfile
import unittest

from check import running, boto3, Config, ClientError
from botocore.parsers import ResponseParserError


FIXTURE = Path(__file__).with_name('fixtures') / 'ec2-network-contract.json'
NONCANONICAL_DEFAULT = '203.0.113.19/0'


class NetworkContract(unittest.TestCase):
    def setUp(self):
        self.database = Path(self.enterContext(tempfile.TemporaryDirectory())) / 'state.sqlite'
        self.runtime = self.enterContext(ExitStack())
        self.start_runtime()
        self.template = json.loads(FIXTURE.read_text())
        self.template['Outputs']['Gateway'] = {'Value': {'Ref': 'Gateway'}}

    def start_runtime(self):
        self.endpoint, self.token = self.runtime.enter_context(
            running(db=self.database, authenticated=True))
        self.cfn = self.client('cloudformation')
        self.ec2 = self.client('ec2')

    def client(self, service, region='us-east-1', token=True):
        client = boto3.client(
            service, endpoint_url=self.endpoint, region_name=region,
            aws_access_key_id='TCSIM_NETWORK_CONTRACT',
            aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts': 0}, connect_timeout=3,
                          read_timeout=10, proxies={}))
        if token:
            client.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', self.token))
        self.runtime.callback(client.close)
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
        if self.cfn is None:  # A failed restart still disposes the local database.
            return
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
                sorted(self.ec2.describe_vpcs()['Vpcs'], key=lambda v: v['VpcId']),
                sorted(self.ec2.describe_subnets()['Subnets'], key=lambda s: s['SubnetId']),
                sorted(self.ec2.describe_route_tables()['RouteTables'], key=lambda t: t['RouteTableId']))

    def assert_subnet(self, subnet, outputs):
        self.assertEqual(subnet['SubnetId'], outputs['Subnet'])
        self.assertEqual(subnet['VpcId'], outputs['Vpc'])
        self.assertEqual(subnet['CidrBlock'], self.template['Resources']['Subnet']['Properties']['CidrBlock'])

    def assert_route_table(self, table, outputs):
        self.assertEqual(table['RouteTableId'], outputs['Table'])
        self.assertEqual(table['VpcId'], outputs['Vpc'])
        self.assertEqual([(r['DestinationCidrBlock'], r['GatewayId'], r['Origin'], r['State'])
                          for r in table['Routes']],
                         [('0.0.0.0/0', outputs['Gateway'], 'CreateRoute', 'active')])
        self.assertEqual([(a['RouteTableAssociationId'], a['RouteTableId'], a['SubnetId'], a['Main'])
                          for a in table['Associations']],
                         [(outputs['Association'], outputs['Table'], outputs['Subnet'], False)])

    def test_refs_canonical_cidr_and_scoped_cleanup(self):
        survivor = self.create('network-survivor')
        survivor_outputs = self.outputs(survivor)
        survivor_subnets = self.ec2.describe_subnets()['Subnets']
        self.assertEqual(len(survivor_subnets), 1)
        self.assert_subnet(survivor_subnets[0], survivor_outputs)
        survivor_tables = self.ec2.describe_route_tables()['RouteTables']
        self.assertEqual(len(survivor_tables), 1)
        self.assert_route_table(survivor_tables[0], survivor_outputs)
        body = copy.deepcopy(self.template)
        body['Resources']['Route']['Properties']['DestinationCidrBlock'] = NONCANONICAL_DEFAULT
        selected = self.create('network-selected', body)
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
        subnets = self.ec2.describe_subnets()['Subnets']
        self.assertCountEqual([s['SubnetId'] for s in subnets], [outputs['Subnet'], survivor_outputs['Subnet']])
        by_subnet_id = {s['SubnetId']: s for s in subnets}
        self.assert_subnet(by_subnet_id[outputs['Subnet']], outputs)
        self.assertEqual(by_subnet_id[survivor_outputs['Subnet']], survivor_subnets[0])
        tables = self.ec2.describe_route_tables()['RouteTables']
        self.assertCountEqual([t['RouteTableId'] for t in tables], [outputs['Table'], survivor_outputs['Table']])
        by_id = {t['RouteTableId']: t for t in tables}
        self.assert_route_table(by_id[outputs['Table']], outputs)
        self.assertEqual(by_id[survivor_outputs['Table']], survivor_tables[0])
        other = self.client('ec2', 'ap-northeast-1')
        self.assertEqual(other.describe_vpcs()['Vpcs'], [])
        self.assertEqual(other.describe_subnets()['Subnets'], [])
        self.assertEqual(other.describe_route_tables()['RouteTables'], [])
        self.cfn.delete_stack(StackName=selected)
        self.rejected(self.cfn.describe_stacks, 'ValidationError', StackName=selected)
        self.assertEqual(self.outputs(survivor), survivor_outputs)
        self.assertEqual([v['VpcId'] for v in self.ec2.describe_vpcs()['Vpcs']], [survivor_outputs['Vpc']])
        self.assertEqual(self.ec2.describe_subnets()['Subnets'], survivor_subnets)
        self.assertEqual(self.ec2.describe_route_tables()['RouteTables'], survivor_tables)
        self.cfn.delete_stack(StackName=survivor)
        self.assertEqual(self.state(), ([], [], [], []))

    def test_stack_network_persists_across_process_restart(self):
        stack_id = self.create('network-restart')
        before = self.state()
        outputs = self.outputs(stack_id)
        events = self.cfn.describe_stack_events(StackName=stack_id)['StackEvents']

        # Close the process (and clients) before reopening its SQLite file.
        # running() allocates a fresh isolated localhost endpoint and auth file.
        self.cfn = self.ec2 = None
        self.runtime.close()
        self.start_runtime()

        inventory = self.state()
        self.assertEqual(inventory, before)
        self.assertEqual(self.outputs(stack_id), outputs)
        self.assertEqual(self.cfn.describe_stack_events(StackName=stack_id)['StackEvents'], events)
        _, vpcs, subnets, tables = inventory
        self.assertEqual([v['VpcId'] for v in vpcs], [outputs['Vpc']])
        self.assertEqual(len(subnets), 1)
        self.assert_subnet(subnets[0], outputs)
        self.assertEqual(len(tables), 1)
        self.assert_route_table(tables[0], outputs)

        logical_ids = ('Vpc', 'Subnet', 'Table', 'Association')
        expected = [(stack_id, key, self.template['Resources'][key]['Type'], outputs[key])
                    for key in logical_ids]

        def completed_resources(history, status):
            return [(e['StackId'], e['LogicalResourceId'], e['ResourceType'], e['PhysicalResourceId'])
                    for e in history if e['LogicalResourceId'] in logical_ids and e['ResourceStatus'] == status]

        self.assertCountEqual(completed_resources(events, 'CREATE_COMPLETE'), expected)
        self.cfn.delete_stack(StackName=stack_id)
        self.rejected(self.cfn.describe_stacks, 'ValidationError', StackName=stack_id)
        self.assertEqual(self.state(), ([], [], [], []))
        history = self.cfn.describe_stack_events(StackName=stack_id)['StackEvents']
        self.assertCountEqual(completed_resources(history, 'DELETE_COMPLETE'), expected)
        self.assertEqual([e for e in history if e['ResourceStatus'].startswith('CREATE_')], events)

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
        body = copy.deepcopy(self.template)
        duplicate = copy.deepcopy(body['Resources']['Route'])
        duplicate['Properties']['DestinationCidrBlock'] = NONCANONICAL_DEFAULT
        body['Resources']['DuplicateRoute'] = duplicate
        cases['duplicate-canonical-destination'] = body
        relationship_errors = ('absent-table', 'absent-gateway', 'cross-vpc-association',
                               'duplicate-canonical-destination')
        for label, body in cases.items():
            with self.subTest(label=label):
                # Relationship failures must reject atomically. Do not bless the
                # observed InternalFailure classification as an AWS contract.
                code = None if label in relationship_errors else 'ValidationError'
                self.rejected(self.cfn.create_stack, code, StackName='invalid-' + label, TemplateBody=json.dumps(body))
                self.assertEqual(self.state(), before)
        # Reuse a rejected name: no hidden stack/name claim may remain.
        self.create('invalid-absent-table')
        self.create('invalid-duplicate-canonical-destination')

    def test_native_route_table_read_and_rejected_mutations(self):
        selected = self.create('network-selected')
        outputs = self.outputs(selected)
        tables = self.ec2.describe_route_tables()['RouteTables']
        self.assertEqual([t['RouteTableId'] for t in tables], [outputs['Table']])
        self.assert_route_table(tables[0], outputs)
        before = self.state()
        route = {'RouteTableId': outputs['Table'], 'DestinationCidrBlock': '0.0.0.0/0'}
        create_route = {**route, 'GatewayId': outputs['Gateway']}
        for operation, args in [
            (self.ec2.create_route, create_route),
            (self.ec2.create_route, {**create_route, 'DryRun': True}),
            (self.ec2.delete_route, route),
        ]:
            with self.subTest(operation=operation.__name__, args=args):
                self.rejected(operation, 'UnsupportedOperation', **args)
                self.assertEqual(self.state(), before)

    def test_native_vpc_has_no_implicit_table_and_bare_table_has_no_children(self):
        self.assertEqual(self.ec2.describe_route_tables()['RouteTables'], [])
        vpc_id = self.ec2.create_vpc(CidrBlock='10.68.0.0/16')['Vpc']['VpcId']
        self.addCleanup(self.ec2.delete_vpc, VpcId=vpc_id)
        self.assertEqual(self.ec2.describe_route_tables()['RouteTables'], [])

        bare = {section: {key: self.template[section][key] for key in ('Vpc', 'Table')}
                for section in ('Resources', 'Outputs')}
        outputs = self.outputs(self.create('network-bare-table', bare))
        tables = self.ec2.describe_route_tables()['RouteTables']
        self.assertEqual([t['RouteTableId'] for t in tables], [outputs['Table']])
        self.assertEqual(tables[0]['VpcId'], outputs['Vpc'])
        self.assertEqual(tables[0]['Routes'], [])
        self.assertEqual(tables[0]['Associations'], [])

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
