"""Opt-in SDK subnet tag acceptance on disposable authenticated loopback."""

from pathlib import Path
import tempfile
import unittest

from check import ClientError, Config, boto3, running


class NativeSubnetTags(unittest.TestCase):
    def client(self, endpoint, token, region="us-east-1"):
        self.assertTrue(endpoint.startswith("http://127.0.0.1:"))
        client = boto3.client(
            "ec2", endpoint_url=endpoint, region_name=region,
            aws_access_key_id="TCSIM_SUBNET_TAGS", aws_secret_access_key="dummy-local-only",
            config=Config(retries={"max_attempts": 0}, connect_timeout=3, read_timeout=10, proxies={}),
        )
        client.meta.events.register(
            "before-sign", lambda request, **_: request.headers.add_header("X-Simulator-Token", token)
        )
        self.addCleanup(client.close)
        return client

    def reject(self, operation, code, **params):
        with self.assertRaises(ClientError) as caught:
            operation(**params)
        self.assertEqual(caught.exception.response["Error"]["Code"], code)

    def test_same_subnet_restart_and_route_dependency(self):
        folder = Path(self.enterContext(tempfile.TemporaryDirectory(prefix="subnet-tags-")))
        state = folder / "subnets.sqlite"

        def row(client, id_):
            [selected] = [r for r in client.describe_subnets()["Subnets"] if r["SubnetId"] == id_]
            return selected

        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            other = self.client(endpoint, token, "ap-northeast-1")
            vpc = ec2.create_vpc(CidrBlock="10.90.0.0/16")["Vpc"]["VpcId"]
            [subnet, peer] = [ec2.create_subnet(VpcId=vpc, CidrBlock=cidr)["Subnet"]["SubnetId"]
                              for cidr in ("10.90.1.0/24", "10.90.2.0/24")]
            table = ec2.create_route_table(VpcId=vpc)["RouteTable"]["RouteTableId"]
            association = ec2.associate_route_table(SubnetId=subnet, RouteTableId=table)["AssociationId"]
            old_routes = ec2.describe_route_tables()["RouteTables"]
            original, original_peer = row(ec2, subnet), row(ec2, peer)
            primary = {"Key": "Name", "Value": "研修 & <fixture>"}
            empty = {"Key": "name", "Value": ""}
            self.reject(other.create_tags, "InvalidSubnetID.NotFound", Resources=[subnet], Tags=[primary])
            self.reject(ec2.create_tags, "UnsupportedOperation", Resources=[subnet, vpc], Tags=[primary])
            self.reject(ec2.delete_tags, "MissingParameter", Resources=[subnet])
            for tag in (primary, empty):
                ec2.create_tags(Resources=[subnet], Tags=[tag])
            ec2.delete_tags(Resources=[subnet], Tags=[{"Key": "Name", "Value": ""}])
            saved = {**original, "Tags": [primary, empty]}
            self.assertEqual(row(ec2, subnet), saved)
            self.assertEqual(row(ec2, peer), original_peer)
            self.assertEqual(ec2.describe_route_tables()["RouteTables"], old_routes)

        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            self.assertEqual(row(ec2, subnet), saved)
            self.assertEqual(ec2.describe_route_tables()["RouteTables"], old_routes)
            ec2.delete_tags(Resources=[subnet], Tags=[primary])
            self.assertEqual(row(ec2, subnet), {**saved, "Tags": [empty]})
            self.reject(ec2.delete_subnet, "DependencyViolation", SubnetId=subnet)
            ec2.disassociate_route_table(AssociationId=association)
            ec2.delete_route_table(RouteTableId=table)
            ec2.delete_subnet(SubnetId=subnet)
            ec2.delete_subnet(SubnetId=peer)
            ec2.delete_vpc(VpcId=vpc)
            self.assertEqual(ec2.describe_subnets()["Subnets"], [])
            self.assertEqual(ec2.describe_vpcs()["Vpcs"], [])


if __name__ == "__main__":
    unittest.main()
