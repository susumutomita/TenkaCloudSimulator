"""Native EC2 CreateVpc/DeleteVpc through the official Python SDK; see docs/ec2-read-discovery.md.

Starts only a disposable authenticated loopback simulator via the existing E2E
harness. No VM, real AWS or external endpoint. Run after make build with the
dependencies in scripts/requirements-test.txt.
"""
import json
import unittest

from check import running, boto3, Config, ClientError


class NativeVpc(unittest.TestCase):
    def setUp(self):
        self.endpoint, self.token = self.enterContext(running(authenticated=True))
        self.ec2 = self.client('ec2')
        self.cfn = self.client('cloudformation')

    def client(self, service, region='us-east-1', token=True):
        client = boto3.client(
            service, endpoint_url=self.endpoint, region_name=region,
            aws_access_key_id='TCSIM_NATIVE_VPC', aws_secret_access_key='dummy-local-only',
            config=Config(retries={'max_attempts': 0}, connect_timeout=3, read_timeout=10, proxies={}))
        if token:
            client.meta.events.register('before-sign', lambda request, **_: request.headers.add_header('X-Simulator-Token', self.token))
        self.addCleanup(client.close)
        return client

    def rejected(self, operation, code, **kwargs):
        with self.assertRaises(ClientError) as caught:
            operation(**kwargs)
        self.assertEqual(caught.exception.response['Error']['Code'], code)

    def vpc_ids(self, region='us-east-1'):
        return sorted(v['VpcId'] for v in self.client('ec2', region).describe_vpcs()['Vpcs'])

    def test_create_describe_delete_with_name_tag(self):
        created = self.ec2.create_vpc(CidrBlock='10.40.1.2/16', TagSpecifications=[
            {'ResourceType': 'vpc', 'Tags': [{'Key': 'Name', 'Value': '学習用 VPC'}, {'Key': 'team', 'Value': 'blue'}]}])['Vpc']
        vpc_id = created['VpcId']
        self.assertRegex(vpc_id, r'^vpc-[0-9a-f]{17}$')
        self.assertEqual(created['CidrBlock'], '10.40.0.0/16')
        self.assertEqual(sorted((t['Key'], t['Value']) for t in created['Tags']), [('Name', '学習用 VPC'), ('team', 'blue')])
        listed, = self.ec2.describe_vpcs()['Vpcs']
        self.assertEqual((listed['VpcId'], listed['CidrBlock']), (vpc_id, '10.40.0.0/16'))
        self.assertEqual(self.vpc_ids('ap-northeast-1'), [])
        self.rejected(self.client('ec2', 'ap-northeast-1').delete_vpc, 'InvalidVpcID.NotFound', VpcId=vpc_id)
        self.ec2.delete_vpc(VpcId=vpc_id)
        self.assertEqual(self.vpc_ids(), [])
        self.rejected(self.ec2.delete_vpc, 'InvalidVpcID.NotFound', VpcId=vpc_id)

    def test_invalid_and_unsupported_requests_never_create_or_fake_success(self):
        for code, kwargs in (
            ('InvalidVpc.Range', {'CidrBlock': '10.0.0.0/8'}),
            ('InvalidVpc.Range', {'CidrBlock': '10.0.0.0/29'}),
            ('InvalidParameterValue', {'CidrBlock': 'not-a-cidr'}),
            ('UnsupportedOperation', {'CidrBlock': '10.0.0.0/16', 'AmazonProvidedIpv6CidrBlock': True}),
            ('UnsupportedOperation', {'CidrBlock': '10.0.0.0/16', 'DryRun': True}),
            ('UnsupportedOperation', {'CidrBlock': '10.0.0.0/16', 'InstanceTenancy': 'dedicated'}),
            ('InvalidParameterValue', {'CidrBlock': '10.0.0.0/16', 'TagSpecifications': [{'ResourceType': 'subnet', 'Tags': [{'Key': 'Name', 'Value': 'x'}]}]}),
        ):
            with self.subTest(code=code, kwargs=kwargs):
                self.rejected(self.ec2.create_vpc, code, **kwargs)
        self.rejected(self.ec2.delete_vpc, 'InvalidVpcID.Malformed', VpcId='subnet-0123456789abcdef0')
        self.rejected(self.ec2.delete_vpc, 'UnsupportedOperation', VpcId='vpc-0123456789abcdef0', DryRun=True)
        self.assertEqual(self.vpc_ids(), [])
        anonymous = self.client('ec2', token=False)
        with self.assertRaises(Exception):
            anonymous.create_vpc(CidrBlock='10.0.0.0/16')
        self.assertEqual(self.vpc_ids(), [])

    def test_cloudformation_and_native_vpcs_keep_separate_owners(self):
        stack = self.cfn.create_stack(StackName='owned-network', TemplateBody=json.dumps(
            {'Resources': {'Vpc': {'Type': 'AWS::EC2::VPC', 'Properties': {'CidrBlock': '10.41.0.0/16'}}},
             'Outputs': {'Vpc': {'Value': {'Ref': 'Vpc'}}}}))['StackId']
        self.addCleanup(lambda: stack in {s['StackId'] for s in self.cfn.describe_stacks()['Stacks']} and self.cfn.delete_stack(StackName=stack))
        stack_vpc = self.cfn.describe_stacks(StackName=stack)['Stacks'][0]['Outputs'][0]['OutputValue']
        native = self.ec2.create_vpc(CidrBlock='10.42.0.0/16')['Vpc']['VpcId']
        self.assertEqual(self.vpc_ids(), sorted([stack_vpc, native]))
        self.rejected(self.ec2.delete_vpc, 'UnsupportedOperation', VpcId=stack_vpc)
        # A stack cannot attach its subnet to the EC2-owned VPC; nothing is adopted.
        with self.assertRaises(ClientError):
            self.cfn.create_stack(StackName='adopt-native', TemplateBody=json.dumps({'Resources': {'Subnet': {
                'Type': 'AWS::EC2::Subnet', 'Properties': {'VpcId': native, 'CidrBlock': '10.42.1.0/24'}}}}))
        self.assertNotIn('adopt-native', [s['StackName'] for s in self.cfn.describe_stacks()['Stacks']])
        self.assertEqual(self.vpc_ids(), sorted([stack_vpc, native]))
        self.ec2.delete_vpc(VpcId=native)
        self.cfn.delete_stack(StackName=stack)
        self.assertEqual(self.vpc_ids(), [])


if __name__ == '__main__':
    unittest.main()
