"""Opt-in EC2 SDK acceptance on disposable authenticated numeric loopback."""

from pathlib import Path
import tempfile
import unittest

from check import ClientError, Config, boto3, running


class NativeVPCTags(unittest.TestCase):
    def client(self, endpoint, token, region="us-east-1"):
        self.assertTrue(endpoint.startswith("http://127.0.0.1:"))
        client = boto3.client(
            "ec2", endpoint_url=endpoint, region_name=region,
            aws_access_key_id="TCSIM_VPC_TAGS", aws_secret_access_key="dummy-local-only",
            config=Config(retries={"max_attempts": 0}, connect_timeout=3, read_timeout=10, proxies={}),
        )
        client.meta.events.register(
            "before-sign", lambda request, **_: request.headers.add_header("X-Simulator-Token", token)
        )
        self.addCleanup(client.close)
        return client

    def reject(self, call, code, **params):
        with self.assertRaises(ClientError) as caught:
            call(**params)
        self.assertEqual(caught.exception.response["Error"]["Code"], code)

    def test_tag_only_write_and_restart(self):
        folder = Path(self.enterContext(tempfile.TemporaryDirectory(prefix="vpc-tags-")))
        state = folder / "tags.sqlite"

        def tags(client, id_):
            [match] = [r for r in client.describe_vpcs()["Vpcs"] if r["VpcId"] == id_]
            return match.get("Tags", [])

        def tag(key, value):
            return {"Key": key, "Value": value}

        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            other = self.client(endpoint, token, "ap-northeast-1")
            [vpc, peer] = [ec2.create_vpc(CidrBlock=cidr)["Vpc"]["VpcId"]
                           for cidr in ("10.90.0.0/16", "10.91.0.0/16")]
            old_routes = ec2.describe_route_tables()["RouteTables"]
            self.reject(other.create_tags, "InvalidVpcID.NotFound", Resources=[vpc], Tags=[tag("Name", "fixture")])
            self.reject(ec2.create_tags, "UnsupportedOperation", Resources=[vpc, peer], Tags=[tag("Name", "fixture")])
            self.reject(ec2.delete_tags, "MissingParameter", Resources=[vpc])
            for pair in [tag("Name", "研修 & <fixture>"), tag("name", ""), tag("Keep", "unrelated")]:
                ec2.create_tags(Resources=[vpc], Tags=[pair])
            ec2.delete_tags(Resources=[vpc], Tags=[tag("Name", "")])
            want = [tag("Keep", "unrelated"), tag("Name", "研修 & <fixture>"), tag("name", "")]
            self.assertEqual(tags(ec2, vpc), want)
            self.assertEqual(tags(ec2, peer), [])
            self.assertEqual(ec2.describe_route_tables()["RouteTables"], old_routes)

        with running(state, authenticated=True) as (endpoint, token):
            ec2 = self.client(endpoint, token)
            self.assertEqual(tags(ec2, vpc), want)
            ec2.delete_tags(Resources=[vpc], Tags=[tag("Name", "研修 & <fixture>")])
            ec2.delete_tags(Resources=[vpc], Tags=[{"Key": "name"}])
            self.assertEqual(tags(ec2, vpc), want[:1])
            for id_ in (vpc, peer):
                ec2.delete_vpc(VpcId=id_)
            self.assertEqual(ec2.describe_vpcs()["Vpcs"], [])


if __name__ == "__main__":
    unittest.main()
