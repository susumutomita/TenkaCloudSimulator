"""Native EC2 subnets and instance launch records through the official Python SDK.

CreateSubnet/DeleteSubnet and RunInstances/TerminateInstances; see
docs/ec2-read-discovery.md. Starts only a disposable authenticated loopback
simulator via the existing E2E harness. No VM, real AWS or external endpoint.
Run after make build with the dependencies in scripts/requirements-test.txt.
"""
import json
import unittest
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

from check import running, boto3, Config, ClientError

IMAGE = 'ami-0123456789abcdef0'


class NativeInstance(unittest.TestCase):
    def setUp(self):
        self.endpoint, self.token = self.enterContext(running(authenticated=True))
        self.ec2 = self.client('ec2')
        self.cfn = self.client('cloudformation')

    def client(self, service, region='us-east-1', token=True):
        client = boto3.client(
            service, endpoint_url=self.endpoint, region_name=region,
            aws_access_key_id='TCSIM_NATIVE_INSTANCE', aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts': 0}, connect_timeout=3, read_timeout=10, proxies={}))
        if token:
            client.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', self.token))
        self.addCleanup(client.close)
        return client

    def rejected(self, operation, code, **kwargs):
        with self.assertRaises(ClientError) as caught:
            operation(**kwargs)
        self.assertEqual(caught.exception.response['Error']['Code'], code)

    def network(self, cidr='10.50.0.0/16', subnet='10.50.1.0/24', ec2=None):
        ec2 = ec2 or self.ec2
        vpc = ec2.create_vpc(CidrBlock=cidr)['Vpc']['VpcId']
        return vpc, ec2.create_subnet(VpcId=vpc, CidrBlock=subnet)['Subnet']['SubnetId']

    def instance_ids(self, region='us-east-1'):
        reservations = self.client('ec2', region).describe_instances()['Reservations']
        return sorted(i['InstanceId'] for r in reservations for i in r['Instances'])

    def subnet_ids(self, region='us-east-1'):
        return sorted(s['SubnetId'] for s in self.client('ec2', region).describe_subnets()['Subnets'])

    def launch(self, subnet, **kwargs):
        return self.ec2.run_instances(ImageId=IMAGE, InstanceType='t3.micro', MinCount=1, MaxCount=1, SubnetId=subnet, **kwargs)

    def test_subnet_and_launch_record_lifecycle(self):
        vpc = self.ec2.create_vpc(CidrBlock='10.50.0.0/16')['Vpc']['VpcId']
        created = self.ec2.create_subnet(VpcId=vpc, CidrBlock='10.50.1.9/24', AvailabilityZone='us-east-1b', TagSpecifications=[
            {'ResourceType': 'subnet', 'Tags': [{'Key': 'Name', 'Value': '公開サブネット'}]}])['Subnet']
        subnet = created['SubnetId']
        self.assertRegex(subnet, r'^subnet-[0-9a-f]{17}$')
        self.assertEqual((created['VpcId'], created['CidrBlock'], created['AvailabilityZone'], created['State']), (vpc, '10.50.1.0/24', 'us-east-1b', 'available'))
        self.assertEqual(created['Tags'], [{'Key': 'Name', 'Value': '公開サブネット'}])
        listed, = self.ec2.describe_subnets()['Subnets']
        self.assertEqual((listed['SubnetId'], listed['VpcId'], listed['CidrBlock']), (subnet, vpc, '10.50.1.0/24'))
        self.assertEqual(self.subnet_ids('ap-northeast-1'), [])

        response = self.launch(subnet, TagSpecifications=[
            {'ResourceType': 'instance', 'Tags': [{'Key': 'Name', 'Value': '練習用サーバー'}, {'Key': 'team', 'Value': 'blue'}]}])
        launched, = response['Instances']
        instance = launched['InstanceId']
        self.assertRegex(instance, r'^i-[0-9a-f]{17}$')
        self.assertEqual((response['OwnerId'], launched['ImageId'], launched['InstanceType'], launched['SubnetId'], launched['VpcId']),
                         ('000000000000', IMAGE, 't3.micro', subnet, vpc))
        self.assertEqual(sorted((t['Key'], t['Value']) for t in launched['Tags']), [('Name', '練習用サーバー'), ('team', 'blue')])
        # A launch record has no guest: no runtime state, address or security group is claimed.
        for fabricated in ('State', 'PrivateIpAddress', 'PublicIpAddress', 'PublicDnsName', 'LaunchTime'):
            self.assertNotIn(fabricated, launched)
        self.assertEqual(launched['SecurityGroups'], [])
        described, = self.ec2.describe_instances()['Reservations'][0]['Instances']
        self.assertEqual((described['InstanceId'], described['SubnetId'], described['VpcId']), (instance, subnet, vpc))
        self.assertNotIn('State', described)
        self.assertEqual(self.instance_ids('ap-northeast-1'), [])

        # Dependencies are refused in order: VPC -> subnet -> launch record.
        self.rejected(self.ec2.delete_vpc, 'DependencyViolation', VpcId=vpc)
        self.rejected(self.ec2.delete_subnet, 'DependencyViolation', SubnetId=subnet)
        self.rejected(self.client('ec2', 'ap-northeast-1').terminate_instances, 'InvalidInstanceID.NotFound', InstanceIds=[instance])
        change, = self.ec2.terminate_instances(InstanceIds=[instance])['TerminatingInstances']
        self.assertEqual((change['InstanceId'], change['CurrentState']), (instance, {'Code': 48, 'Name': 'terminated'}))
        self.assertNotIn('PreviousState', change)
        self.assertEqual(self.instance_ids(), [])
        self.rejected(self.ec2.terminate_instances, 'InvalidInstanceID.NotFound', InstanceIds=[instance])
        self.ec2.delete_subnet(SubnetId=subnet)
        self.rejected(self.ec2.delete_subnet, 'InvalidSubnetID.NotFound', SubnetId=subnet)
        self.ec2.delete_vpc(VpcId=vpc)
        self.assertEqual(self.subnet_ids(), [])

    def test_client_token_replays_one_record_and_refuses_changed_parameters(self):
        _, subnet = self.network()
        # boto3 adds a ClientToken to every RunInstances request; each call differs.
        first = self.launch(subnet, ClientToken='sdk-launch-1')['Instances'][0]['InstanceId']
        self.assertEqual(self.launch(subnet, ClientToken='sdk-launch-1')['Instances'][0]['InstanceId'], first)
        self.rejected(self.ec2.run_instances, 'IdempotentParameterMismatch', ImageId='ami-12345678', InstanceType='t3.micro',
                      MinCount=1, MaxCount=1, SubnetId=subnet, ClientToken='sdk-launch-1')
        second = self.launch(subnet)['Instances'][0]['InstanceId']
        self.assertNotEqual(first, second)
        self.assertEqual(self.instance_ids(), sorted([first, second]))

    def test_invalid_and_unsupported_requests_never_create_or_fake_success(self):
        vpc, subnet = self.network()
        for code, kwargs in (
            ('InvalidSubnet.Range', {'VpcId': vpc, 'CidrBlock': '10.51.0.0/24'}),
            ('InvalidSubnet.Range', {'VpcId': vpc, 'CidrBlock': '10.50.2.0/29'}),
            ('InvalidSubnet.Conflict', {'VpcId': vpc, 'CidrBlock': '10.50.1.128/25'}),
            ('InvalidParameterValue', {'VpcId': vpc, 'CidrBlock': '10.50.2.0/24', 'AvailabilityZone': 'us-west-2a'}),
            ('InvalidVpcID.NotFound', {'VpcId': 'vpc-0123456789abcdef0', 'CidrBlock': '10.50.2.0/24'}),
            ('UnsupportedOperation', {'VpcId': vpc, 'CidrBlock': '10.50.2.0/24', 'Ipv6CidrBlock': '2001:db8::/64'}),
            ('UnsupportedOperation', {'VpcId': vpc, 'CidrBlock': '10.50.2.0/24', 'DryRun': True}),
        ):
            with self.subTest(operation='create_subnet', code=code, kwargs=kwargs):
                self.rejected(self.ec2.create_subnet, code, **kwargs)
        base = {'ImageId': IMAGE, 'InstanceType': 't3.micro', 'MinCount': 1, 'MaxCount': 1, 'SubnetId': subnet}
        for code, kwargs in (
            ('InvalidAMIID.Malformed', {'ImageId': 'ami-nope'}),
            ('InvalidParameterValue', {'InstanceType': 'm5.large'}),
            ('UnsupportedOperation', {'MaxCount': 2}),
            ('InvalidSubnetID.NotFound', {'SubnetId': 'subnet-0123456789abcdef0'}),
            ('UnsupportedOperation', {'KeyName': 'my-key'}),
            ('UnsupportedOperation', {'SecurityGroupIds': ['sg-0123456789abcdef0']}),
            ('UnsupportedOperation', {'UserData': '#!/bin/sh\necho hello'}),
            ('UnsupportedOperation', {'IamInstanceProfile': {'Name': 'profile'}}),
            ('UnsupportedOperation', {'DryRun': True}),
            ('InvalidParameterValue', {'TagSpecifications': [{'ResourceType': 'volume', 'Tags': [{'Key': 'Name', 'Value': 'x'}]}]}),
        ):
            with self.subTest(operation='run_instances', code=code, kwargs=kwargs):
                self.rejected(self.ec2.run_instances, code, **{**base, **kwargs})
        self.rejected(self.ec2.terminate_instances, 'InvalidInstanceID.Malformed', InstanceIds=['i-nope'])
        self.rejected(self.ec2.terminate_instances, 'UnsupportedOperation', InstanceIds=['i-0123456789abcdef0', 'i-0123456789abcdef1'])
        self.rejected(self.ec2.describe_instances, 'UnsupportedOperation', InstanceIds=['i-0123456789abcdef0'])
        for operation in ('start_instances', 'stop_instances', 'reboot_instances'):
            self.rejected(getattr(self.ec2, operation), 'UnsupportedOperation', InstanceIds=['i-0123456789abcdef0'])
        self.assertEqual((self.subnet_ids(), self.instance_ids()), ([subnet], []))
        # Without the host token the request never reaches EC2: the host answers
        # 401 (not an EC2 document, so the SDK cannot parse it) and nothing is saved.
        body = urlencode({'Action': 'RunInstances', 'Version': '2016-11-15', **{k: str(v) for k, v in base.items()}}).encode()
        request = Request(self.endpoint + '/', data=body, headers={'Content-Type': 'application/x-www-form-urlencoded', 'X-Simulator-Service': 'ec2'})
        with self.assertRaises(HTTPError) as refused:
            urlopen(request, timeout=10)
        self.assertEqual(refused.exception.code, 401)
        with self.assertRaises(Exception):
            self.client('ec2', token=False).run_instances(**base)
        self.assertEqual(self.instance_ids(), [])

    def test_cloudformation_and_native_resources_keep_separate_owners(self):
        stack = self.cfn.create_stack(StackName='owned-subnet', TemplateBody=json.dumps({'Resources': {
            'Vpc': {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.60.0.0/16'}},
            'Subnet': {'Type': 'AWS::EC2::Subnet', 'Properties': {'VpcId': {'Ref': 'Vpc'}, 'CidrBlock': '10.60.1.0/24'}}},
            'Outputs': {'Vpc': {'Value': {'Ref': 'Vpc'}}, 'Subnet': {'Value': {'Ref': 'Subnet'}}}}))['StackId']
        outputs = {o['OutputKey']: o['OutputValue'] for o in self.cfn.describe_stacks(StackName=stack)['Stacks'][0]['Outputs']}
        vpc, subnet = self.network('10.61.0.0/16', '10.61.1.0/24')
        self.assertEqual(self.subnet_ids(), sorted([outputs['Subnet'], subnet]))
        # The stack's VPC and subnet are refused for native subnets, launches and deletes.
        self.rejected(self.ec2.create_subnet, 'UnsupportedOperation', VpcId=outputs['Vpc'], CidrBlock='10.60.2.0/24')
        self.rejected(self.ec2.delete_subnet, 'UnsupportedOperation', SubnetId=outputs['Subnet'])
        self.rejected(self.ec2.run_instances, 'UnsupportedOperation', ImageId=IMAGE, InstanceType='t3.micro', MinCount=1, MaxCount=1, SubnetId=outputs['Subnet'])
        # A stack cannot attach a route table to the EC2-owned VPC either.
        with self.assertRaises(ClientError):
            self.cfn.create_stack(StackName='adopt-native-vpc', TemplateBody=json.dumps({'Resources': {'Table': {
                'Type': 'AWS::EC2::RouteTable', 'Properties': {'VpcId': vpc}}}}))
        instance = self.launch(subnet)['Instances'][0]['InstanceId']
        # Deleting the stack leaves every EC2-owned resource in place.
        self.cfn.delete_stack(StackName=stack)
        self.assertEqual((self.subnet_ids(), self.instance_ids()), ([subnet], [instance]))
        self.ec2.terminate_instances(InstanceIds=[instance])
        self.ec2.delete_subnet(SubnetId=subnet)
        self.ec2.delete_vpc(VpcId=vpc)
        self.assertEqual((self.subnet_ids(), self.instance_ids()), ([], []))


if __name__ == '__main__':
    unittest.main()
