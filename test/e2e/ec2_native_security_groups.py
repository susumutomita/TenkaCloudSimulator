"""Opt-in API check for detached security group metadata, not rule enforcement."""

import json
from pathlib import Path
import tempfile
import unittest

from check import ClientError, Config, boto3, running


class NativeSecurityGroups(unittest.TestCase):
    def client(self, endpoint, token, service="ec2", region="us-east-1"):
        self.assertTrue(endpoint.startswith("http://127.0.0.1:"))
        c = boto3.client(
            service, endpoint_url=endpoint, region_name=region,
            aws_access_key_id="TCSIM_NATIVE_SECURITY_GROUP", aws_secret_access_key="dummy-local-only",
            config=Config(retries={"max_attempts": 0}, proxies={}, connect_timeout=3, read_timeout=10),
        )
        c.meta.events.register("before-sign", lambda request, **_: request.headers.add_header("X-Simulator-Token", token))
        self.addCleanup(c.close)
        return c

    def refused(self, call, code, **kwargs):
        with self.assertRaises(ClientError) as caught:
            call(**kwargs)
        self.assertEqual(caught.exception.response["Error"]["Code"], code)

    def test_sdk_owned_group_metadata_restart_and_cleanup(self):
        temp = Path(self.enterContext(tempfile.TemporaryDirectory(prefix="native-sg-")))
        state = temp / "team-a.sqlite"
        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            cfn = self.client(endpoint, token, service="cloudformation")
            other = self.client(endpoint, token, region="ap-northeast-1")
            stack_name = "sg-legacy"
            cfn.create_stack(StackName=stack_name, TemplateBody=json.dumps({
                "Resources": {
                    "Vpc": {"Type": "AWS::EC2::VPC", "Properties": {"CidrBlock": "10.58.0.0/16"}},
                    "Group": {"Type": "AWS::EC2::SecurityGroup",
                              "Properties": {"GroupDescription": "Legacy group", "VpcId": {"Ref": "Vpc"}}},
                },
                "Outputs": {"StackVpc": {"Value": {"Ref": "Vpc"}}, "StackGroup": {"Value": {"Ref": "Group"}}},
            }))
            outputs = {x["OutputKey"]: x["OutputValue"] for x in cfn.describe_stacks(StackName=stack_name)["Stacks"][0]["Outputs"]}
            legacy, stack_vpc = outputs["StackGroup"], outputs["StackVpc"]
            vpc = ec2.create_vpc(CidrBlock="10.59.0.0/16")["Vpc"]["VpcId"]
            second = ec2.create_vpc(CidrBlock="10.60.0.0/16")["Vpc"]["VpcId"]
            kwargs = {
                "GroupName": "App + Web  ", "Description": "Fixture & persisted", "VpcId": vpc,
                "TagSpecifications": [{"ResourceType": "security-group", "Tags": [{"Key": "Name", "Value": "学習"}]}],
            }
            made = ec2.create_security_group(**kwargs)
            group = made["GroupId"]
            self.assertRegex(group, r"^sg-[0-9a-f]{17}$")
            self.assertEqual(made["SecurityGroupArn"], f"arn:aws:ec2:us-east-1:000000000000:security-group/{group}")
            self.assertEqual(made["Tags"], [{"Key": "Name", "Value": "学習"}])
            self.refused(ec2.create_security_group, "InvalidGroup.Duplicate", **{**kwargs, "GroupName": "app + web"})
            self.refused(ec2.create_security_group, "UnsupportedOperation", **{**kwargs, "VpcId": stack_vpc})
            second_group = ec2.create_security_group(**{**kwargs, "VpcId": second})["GroupId"]
            self.assertNotEqual(second_group, group)
            ids = ec2.describe_security_groups(GroupIds=[group, legacy])["SecurityGroups"]
            self.assertEqual([item["GroupId"] for item in ids], sorted([group, legacy]))
            by_id = {r["GroupId"]: r for r in ids}
            self.assertNotIn("GroupName", by_id[legacy])
            saved = by_id[group]
            self.assertEqual((saved["GroupName"], saved["Description"], saved["VpcId"]), ("App + Web", "Fixture & persisted", vpc))
            self.assertEqual(saved["Tags"], made["Tags"])
            self.assertEqual(saved["IpPermissions"], [])
            self.assertNotIn("IpPermissionsEgress", saved)
            self.assertEqual(ec2.describe_security_groups(Filters=[{"Name": "vpc-id", "Values": [vpc]}])["SecurityGroups"], [saved])
            self.refused(ec2.delete_security_group, "UnsupportedOperation", GroupId=legacy)
            self.refused(ec2.delete_vpc, "DependencyViolation", VpcId=vpc)
            self.assertEqual(other.describe_security_groups()["SecurityGroups"], [])
            self.refused(other.describe_security_groups, "InvalidGroup.NotFound", GroupIds=[group])
            with running(temp / "team-b.sqlite", authenticated=True) as (endpoint_b, token_b):
                self.assertEqual(self.client(endpoint_b, token_b).describe_security_groups()["SecurityGroups"], [])

        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            cfn = self.client(endpoint, token, service="cloudformation")
            self.assertEqual(ec2.describe_security_groups(GroupIds=[group])["SecurityGroups"], [saved])
            deleted = ec2.delete_security_group(GroupId=group)
            self.assertTrue(deleted["Return"])
            self.assertEqual(deleted["GroupId"], group)
            self.refused(ec2.delete_security_group, "InvalidGroup.NotFound", GroupId=group)
            self.refused(ec2.describe_security_groups, "InvalidGroup.NotFound", GroupIds=[group])
            ec2.delete_security_group(GroupId=second_group)
            ec2.delete_vpc(VpcId=vpc)
            ec2.delete_vpc(VpcId=second)
            cfn.delete_stack(StackName=stack_name)
            self.assertEqual(ec2.describe_security_groups()["SecurityGroups"], [])
            self.assertEqual(ec2.describe_vpcs()["Vpcs"], [])


if __name__ == "__main__":
    unittest.main()
