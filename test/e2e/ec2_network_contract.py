"""Bounded CFN/EC2 native SDK contracts; see docs/ec2-network-contracts.md.

Starts only a disposable authenticated loopback simulator via the existing E2E
harness. No VM, real AWS, emulator comparison, or external endpoint argument.
Run after make build with the dependencies in scripts/requirements-test.txt.
"""
from contextlib import ExitStack, closing
import copy
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

from check import running, boto3, Config, ClientError
from cloudformation_ssm_parameters import control
from botocore.exceptions import ParamValidationError
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

    def client(self, service, region='us-east-1', token=None):
        client = boto3.client(
            service, endpoint_url=self.endpoint, region_name=region,
            aws_access_key_id='TCSIM_NETWORK_CONTRACT',
            aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts': 0}, connect_timeout=3,
                          read_timeout=10, proxies={}))
        access_token = self.token if token is None else token
        if access_token:
            client.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', access_token))
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
        return caught.exception.response

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

    def test_route_table_filter_observes_only_live_explicit_subnet_associations(self):
        selected = self.create('network-filter-stack')
        outputs = self.outputs(selected)
        cfn_subnet = outputs['Subnet']

        def filtered(*values, client=None):
            return (client or self.ec2).describe_route_tables(
                Filters=[{'Name': 'association.subnet-id', 'Values': list(values)}])['RouteTables']

        def table_ids(rows):
            return [table['RouteTableId'] for table in rows]

        vpc = self.ec2.create_vpc(CidrBlock='10.89.0.0/16')['Vpc']['VpcId']
        self.addCleanup(self.ec2.delete_vpc, VpcId=vpc)
        main, = [r for r in self.ec2.describe_route_tables()['RouteTables'] if r['VpcId'] == vpc]
        self.assertEqual([(a['Main'], a.get('SubnetId')) for a in main['Associations']], [(True, None)])
        subnet = self.ec2.create_subnet(VpcId=vpc, CidrBlock='10.89.1.0/24')['Subnet']['SubnetId']
        self.addCleanup(self.ec2.delete_subnet, SubnetId=subnet)
        table = self.ec2.create_route_table(VpcId=vpc)['RouteTable']['RouteTableId']
        self.addCleanup(self.ec2.delete_route_table, RouteTableId=table)
        self.assertEqual(table_ids(filtered(cfn_subnet)), [outputs['Table']])
        self.assert_route_table(filtered(cfn_subnet)[0], outputs)
        self.assertEqual(filtered(subnet), [])
        self.assertEqual(filtered(cfn_subnet, client=self.client('ec2', 'ap-northeast-1')), [])

        association = self.ec2.associate_route_table(RouteTableId=table, SubnetId=subnet)['AssociationId']
        try:
            rows = filtered(subnet, cfn_subnet, subnet)
            self.assertEqual(table_ids(rows), sorted([table, outputs['Table']]))
            native, = [row for row in rows if row['RouteTableId'] == table]
            self.assertEqual([(a['RouteTableAssociationId'], a['SubnetId'], a['Main'])
                              for a in native['Associations']], [(association, subnet, False)])
            self.assertEqual(table_ids(filtered(subnet)), [table])
        finally:
            self.ec2.disassociate_route_table(AssociationId=association)
        self.assertEqual(filtered(subnet), [])
        current = self.ec2.describe_route_tables()['RouteTables']
        self.assertCountEqual(table_ids(current), [table, main['RouteTableId'], outputs['Table']])
        self.assertIn(main, current)

        # A reused logical stack name must not give the old subnet a new association.
        self.cfn.delete_stack(StackName=selected)
        replacement = self.outputs(self.create('network-filter-stack'))
        self.assertNotEqual(replacement['Subnet'], cfn_subnet)
        self.assertEqual(filtered(cfn_subnet), [])
        self.assertEqual(table_ids(filtered(replacement['Subnet'])), [replacement['Table']])

    def test_participant_missing_subnet_grant_preserves_stack_network(self):
        keys = ('Vpc', 'Subnet', 'Table', 'Association')
        template = {section: {key: self.template[section][key] for key in keys}
                    for section in ('Resources', 'Outputs')}
        survivor = self.create('network-policy-survivor', template)
        baseline = self.state()
        survivor_events = self.cfn.describe_stack_events(StackName=survivor)['StackEvents']

        name = 'network-policy-target'
        principal = 'network-policy-participant'
        region = 'us-east-1'
        ec2_arn = f'arn:aws:ec2:{region}:000000000000:'
        policy = {'Version': '2012-10-17', 'Statement': [
            {'Effect': 'Allow', 'Action': ['cloudformation:CreateStack', 'cloudformation:DeleteStack'],
             'Resource': f'arn:aws:cloudformation:{region}:000000000000:stack/{name}/*'},
            {'Effect': 'Allow', 'Action': ['ec2:CreateVpc', 'ec2:ModifyVpcAttribute', 'ec2:DeleteVpc',
                                           'ec2:CreateSubnet', 'ec2:ModifySubnetAttribute', 'ec2:DeleteSubnet',
                                           'ec2:CreateRouteTable', 'ec2:DeleteRouteTable'],
             'Resource': ec2_arn + '*'},
            {'Effect': 'Allow', 'Action': 'ec2:DescribeRouteTables', 'Resource': '*'},
            # The table grant alone does not authorize attaching or detaching its subnet.
            {'Effect': 'Allow', 'Action': ['ec2:AssociateRouteTable', 'ec2:DisassociateRouteTable'],
             'Resource': ec2_arn + 'route-table/*'},
        ]}
        control(self.endpoint, self.token, f'/_simulator/v1/iam/principals/{principal}/policy', policy)
        token = control(self.endpoint, self.token, '/_simulator/v1/access', {'Principal': principal})['AccessToken']
        limited_cfn = self.client('cloudformation', token=token)
        limited_ec2 = self.client('ec2', token=token)
        self.assertCountEqual(limited_ec2.describe_route_tables()['RouteTables'], baseline[3])

        def denied_on_subnet(operation, action, subnet='', **kwargs):
            response = self.rejected(operation, 'AccessDenied', **kwargs)
            self.assertEqual(response['ResponseMetadata']['HTTPStatusCode'], 403)
            self.assertIn(f'{action} on {ec2_arn}subnet/{subnet}', response['Error']['Message'])

        denied_on_subnet(limited_cfn.create_stack, 'ec2:AssociateRouteTable',
                         StackName=name, TemplateBody=json.dumps(template))
        self.assertEqual(self.state(), baseline)
        self.rejected(self.cfn.describe_stack_events, 'ValidationError', StackName=name)
        self.assertEqual(self.cfn.describe_stack_events(StackName=survivor)['StackEvents'], survivor_events)

        # The identical name and template can be used by the authorized operator.
        target = self.create(name, template)
        outputs = self.outputs(target)
        live = self.state()
        table = next(r for r in live[3] if r['RouteTableId'] == outputs['Table'])
        self.assertEqual([(a['RouteTableAssociationId'], a['SubnetId'], a['Main'])
                          for a in table['Associations']],
                         [(outputs['Association'], outputs['Subnet'], False)])
        target_events = self.cfn.describe_stack_events(StackName=target)['StackEvents']
        denied_on_subnet(limited_cfn.delete_stack, 'ec2:DisassociateRouteTable',
                         subnet=outputs['Subnet'], StackName=target)
        self.assertEqual(self.state(), live)
        self.assertEqual(self.cfn.describe_stack_events(StackName=target)['StackEvents'], target_events)

        self.cfn.delete_stack(StackName=target)
        self.assertEqual(self.state(), baseline)
        self.assertEqual(self.cfn.describe_stack_events(StackName=survivor)['StackEvents'], survivor_events)
        self.cfn.delete_stack(StackName=survivor)
        self.assertEqual(self.state(), ([], [], [], []))

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
        self.assertEqual(self.ec2.describe_route_tables(RouteTableIds=[outputs['Table']])['RouteTables'], tables)
        for operation, args, code in [
            (self.ec2.create_route, create_route, 'UnsupportedOperation'),
            # Native DryRun only checks permissions; it does not authorize
            # adopting or changing CloudFormation ownership or routes.
            (self.ec2.create_route, {**create_route, 'DryRun': True}, 'DryRunOperation'),
            (self.ec2.delete_route, route, 'UnsupportedOperation'),
        ]:
            with self.subTest(operation=operation.__name__, args=args):
                self.rejected(operation, code, **args)
                self.assertEqual(self.state(), before)

    def test_native_route_query_refusals_preserve_saved_network(self):
        def snapshot():
            gateways = sorted(self.ec2.describe_internet_gateways()['InternetGateways'],
                              key=lambda g: g['InternetGatewayId'])
            # Check the actual network rows as well as SDK projections. This
            # is a separate read-only connection to this test's own database.
            with closing(sqlite3.connect(self.database.as_uri() + '?mode=ro', uri=True)) as db:
                rows = tuple(db.execute('SELECT region,id,stack_id,kind,document,revision '
                                        'FROM aws_ec2_network_resources ORDER BY region,id'))
            return self.state(), gateways, rows

        def make_vpc(cleanup, cidr):
            vpc = self.ec2.create_vpc(CidrBlock=cidr)['Vpc']['VpcId']
            cleanup.callback(self.ec2.delete_vpc, VpcId=vpc)
            return vpc

        def make_gateway(cleanup, vpc=None):
            gateway = self.ec2.create_internet_gateway()['InternetGateway']['InternetGatewayId']
            cleanup.callback(self.ec2.delete_internet_gateway, InternetGatewayId=gateway)
            if vpc is not None:
                link = {'InternetGatewayId': gateway, 'VpcId': vpc}
                self.ec2.attach_internet_gateway(**link)
                cleanup.callback(self.ec2.detach_internet_gateway, **link)
            return gateway

        empty = snapshot()
        survivor = self.create('native-route-refusal-survivor')
        initial = snapshot()
        with ExitStack() as cleanup:
            vpc = make_vpc(cleanup, '10.76.0.0/16')
            other = make_vpc(cleanup, '10.77.0.0/16')
            table = self.ec2.create_route_table(VpcId=vpc)['RouteTable']['RouteTableId']
            cleanup.callback(self.ec2.delete_route_table, RouteTableId=table)
            subnet = self.ec2.create_subnet(VpcId=vpc, CidrBlock='10.76.1.0/24')['Subnet']['SubnetId']
            cleanup.callback(self.ec2.delete_subnet, SubnetId=subnet)
            assoc = self.ec2.associate_route_table(RouteTableId=table, SubnetId=subnet)['AssociationId']
            cleanup.callback(self.ec2.disassociate_route_table, AssociationId=assoc)
            gateway = make_gateway(cleanup, vpc)
            foreign = make_gateway(cleanup, other)
            detached = make_gateway(cleanup)

            self.assertTrue(self.ec2.create_route(RouteTableId=table, GatewayId=gateway,
                                                  DestinationCidrBlock='192.0.2.51/24')['Return'])
            cleanup.callback(self.ec2.delete_route, RouteTableId=table, DestinationCidrBlock='192.0.2.0/24')
            before = snapshot()
            exact, = self.ec2.describe_route_tables(RouteTableIds=[table])['RouteTables']
            self.assertEqual(exact['Routes'], [{'DestinationCidrBlock': '192.0.2.0/24',
                                               'GatewayId': gateway, 'Origin': 'CreateRoute', 'State': 'active'}])
            self.assertEqual(exact['Associations'][0]['RouteTableAssociationId'], assoc)

            query = {'RouteTableId': table}
            ipv4 = {'DestinationCidrBlock': '198.51.100.128/25'}
            ipv6 = {'DestinationIpv6CidrBlock': '2001:db8::/64'}
            prefix = {'DestinationPrefixListId': 'pl-0123456789abcdef0'}
            igw = {'GatewayId': gateway}
            nat = {'NatGatewayId': 'nat-0123456789abcdef0'}
            cases = [
                ('IPv6 only', {**ipv6, **igw}, 'UnsupportedOperation'),
                ('prefix list only', {**prefix, **igw}, 'UnsupportedOperation'),
                ('IPv4 and IPv6', {**ipv4, **ipv6, **igw}, 'UnsupportedOperation'),
                ('IPv4 and prefix list', {**ipv4, **prefix, **igw}, 'UnsupportedOperation'),
                ('NAT only', {**ipv4, **nat}, 'UnsupportedOperation'),
                ('internet and NAT gateways', {**ipv4, **igw, **nat}, 'UnsupportedOperation'),
                ('missing destination', igw, 'MissingParameter'),
                ('missing target', ipv4, 'MissingParameter'),
                ('invalid IPv4 prefix', {**igw, 'DestinationCidrBlock': '198.51.100.254/33'},
                 'InvalidParameterValue'),
                ('missing table', {**ipv4, **igw, 'RouteTableId': 'rtb-00000000000000001'},
                 'InvalidRouteTableID.NotFound'),
                ('missing gateway', {**ipv4, 'GatewayId': 'igw-00000000000000001'},
                 'InvalidInternetGatewayID.NotFound'),
                ('gateway attached to different VPC', {**ipv4, 'GatewayId': foreign}, 'Gateway.NotAttached'),
                ('detached gateway', {**ipv4, 'GatewayId': detached}, 'Gateway.NotAttached'),
            ]
            replies = []

            def observe(http_response, parsed, **_):
                media = http_response.headers.get('content-type', '').partition(';')[0]
                replies.append((http_response.status_code, media, parsed.get('Error', {}).get('Code')))

            self.ec2.meta.events.register('after-call.ec2.CreateRoute', observe)
            for label, fields, code in cases:
                with self.subTest(label=label):
                    count = len(replies)
                    self.rejected(self.ec2.create_route, code, **{**query, **fields})
                    self.assertEqual(replies[count:], [(400, 'text/xml', code)])
                    self.assertEqual(self.ec2.describe_route_tables(RouteTableIds=[table])['RouteTables'], [exact])
                    self.assertEqual(snapshot(), before)

            # The official SDK rejects these two before sending Query/XML.
            # They are client validation, not evidence about the server.
            for kwargs in ({**ipv4, **igw}, {**query, **igw, 'DestinationCidrBlock': 99}):
                count = len(replies)
                with self.assertRaises(ParamValidationError):
                    self.ec2.create_route(**kwargs)
                self.assertEqual(len(replies), count)
            self.assertEqual(snapshot(), before)
        self.assertEqual(snapshot(), initial)
        self.cfn.delete_stack(StackName=survivor)
        self.assertEqual(snapshot(), empty)

    def test_fresh_native_vpc_has_one_main_and_cfn_bare_table_has_no_children(self):
        self.assertEqual(self.ec2.describe_route_tables()['RouteTables'], [])
        vpc_id = self.ec2.create_vpc(CidrBlock='10.68.0.0/16')['Vpc']['VpcId']
        self.addCleanup(self.ec2.delete_vpc, VpcId=vpc_id)
        main, = self.ec2.describe_route_tables()['RouteTables']
        self.assertEqual(main['VpcId'], vpc_id)
        self.assertRegex(main['RouteTableId'], r'^rtb-[0-9a-f]{17}$')
        self.assertEqual(main['Routes'], [{'DestinationCidrBlock': '10.68.0.0/16', 'GatewayId': 'local',
                                           'Origin': 'CreateRouteTable', 'State': 'active'}])
        owner, = main['Associations']
        self.assertTrue(owner['Main'])
        self.assertEqual(owner['RouteTableId'], main['RouteTableId'])
        self.assertNotIn('SubnetId', owner)

        bare = {section: {key: self.template[section][key] for key in ('Vpc', 'Table')}
                for section in ('Resources', 'Outputs')}
        outputs = self.outputs(self.create('network-bare-table', bare))
        tables = self.ec2.describe_route_tables()['RouteTables']
        self.assertCountEqual([t['RouteTableId'] for t in tables], [outputs['Table'], main['RouteTableId']])
        self.assertIn(main, tables)
        cfn, = [row for row in tables if row['RouteTableId'] == outputs['Table']]
        self.assertEqual(cfn['VpcId'], outputs['Vpc'])
        self.assertEqual(cfn['Routes'], [])
        self.assertEqual(cfn['Associations'], [])

    def test_missing_local_token_cannot_create_network(self):
        before = self.state()
        client = self.client('cloudformation', token='')
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
