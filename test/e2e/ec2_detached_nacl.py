"""Opt-in SDK acceptance for detached ACL configuration, never real networking.

After building the candidate, run with the pinned SDKs in requirements-test.txt.
Only disposable, authenticated simulator processes on numeric loopback are used.
"""

from pathlib import Path
import tempfile
import unittest

from check import ClientError, Config, boto3, running


class DetachedNetworkACL(unittest.TestCase):
    def client(self, endpoint, token, region="us-east-1"):
        self.assertTrue(endpoint.startswith("http://127.0.0.1:"))
        client = boto3.client(
            "ec2",
            endpoint_url=endpoint,
            region_name=region,
            aws_access_key_id="TCSIM_NETWORK_ACL",
            aws_secret_access_key="dummy-local-only",
            config=Config(retries={"max_attempts": 0}, connect_timeout=3, read_timeout=10, proxies={}),
        )
        client.meta.events.register(
            "before-sign", lambda request, **_: request.headers.add_header("X-Simulator-Token", token)
        )
        self.addCleanup(client.close)
        return client

    def rejected(self, operation, code, **kwargs):
        with self.assertRaises(ClientError) as caught:
            operation(**kwargs)
        self.assertEqual(caught.exception.response["Error"]["Code"], code)

    def test_sdk_create_filter_retry_two_databases_and_cleanup(self):
        directory = Path(self.enterContext(tempfile.TemporaryDirectory(prefix="detached-nacl-")))
        state = directory / "team-a.sqlite"
        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            other_region = self.client(endpoint, token, "ap-northeast-1")
            first = ec2.create_vpc(CidrBlock="10.45.0.0/16")["Vpc"]["VpcId"]
            second = ec2.create_vpc(CidrBlock="10.46.0.0/16")["Vpc"]["VpcId"]
            request = {
                "VpcId": first,
                "TagSpecifications": [{"ResourceType": "network-acl", "Tags": [{"Key": "Name", "Value": "学習 & fixture"}]}],
            }
            # Botocore inserts a fresh ClientToken when the SDK caller omits it.
            response = ec2.create_network_acl(**request)
            request["ClientToken"] = response["ClientToken"]
            acl = response["NetworkAcl"]
            acl_id = acl["NetworkAclId"]
            self.assertRegex(acl_id, r"^acl-[0-9a-f]{17}$")
            self.assertEqual(acl["VpcId"], first)
            self.assertFalse(acl["IsDefault"])
            self.assertEqual(acl["Associations"], [])
            self.assertEqual(acl["Tags"], [{"Key": "Name", "Value": "学習 & fixture"}])
            self.assertEqual(
                {(row["RuleNumber"], row["Protocol"], row["RuleAction"], row["Egress"], row["CidrBlock"]) for row in acl["Entries"]},
                {(32767, "-1", "deny", True, "0.0.0.0/0"), (32767, "-1", "deny", False, "0.0.0.0/0")},
            )
            self.assertEqual(ec2.create_network_acl(**request)["NetworkAcl"], acl)
            self.rejected(ec2.create_network_acl, "IdempotentParameterMismatch", **{**request, "VpcId": second})
            self.assertEqual(ec2.describe_network_acls(Filters=[{"Name": "vpc-id", "Values": [first]}])["NetworkAcls"], [acl])
            self.assertEqual(ec2.describe_network_acls(Filters=[{"Name": "vpc-id", "Values": [second]}])["NetworkAcls"], [])
            self.assertEqual({r["VpcId"] for r in ec2.describe_vpcs()["Vpcs"]}, {first, second})
            self.assertEqual(other_region.describe_network_acls()["NetworkAcls"], [])
            self.rejected(other_region.describe_network_acls, "InvalidNetworkAclID.NotFound", NetworkAclIds=[acl_id])
            self.rejected(ec2.delete_vpc, "DependencyViolation", VpcId=first)

            with running(directory / "team-b.sqlite", authenticated=True) as (other_endpoint, other_token):
                isolated = self.client(other_endpoint, other_token)
                self.assertEqual(isolated.describe_network_acls()["NetworkAcls"], [])

        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            self.assertEqual(ec2.describe_network_acls(NetworkAclIds=[acl_id])["NetworkAcls"], [acl])
            self.assertEqual(ec2.create_network_acl(**request)["NetworkAcl"]["NetworkAclId"], acl_id)
            ec2.delete_network_acl(NetworkAclId=acl_id)
            self.assertEqual(ec2.describe_network_acls()["NetworkAcls"], [])
            self.rejected(ec2.create_network_acl, "InvalidNetworkAclID.NotFound", **request)
            self.rejected(ec2.delete_network_acl, "InvalidNetworkAclID.NotFound", NetworkAclId=acl_id)
            ec2.delete_vpc(VpcId=first)
            ec2.delete_vpc(VpcId=second)
            self.assertEqual(ec2.describe_vpcs()["Vpcs"], [])


if __name__ == "__main__":
    unittest.main()
