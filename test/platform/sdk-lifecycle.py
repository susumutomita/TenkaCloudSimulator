"""Verify local CloudFormation application and network SDK contracts in the PR gate."""
import json
from pathlib import Path
import sys
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from botocore.exceptions import ClientError

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "test/e2e"))
from check import running
from cloudformation_ssm_parameters import client
# unittest.main discovers these four tests alongside StackLifecycle. The existing
# required platform-audit PR job invokes this runner; failures share its exit code.
from ec2_network_contract import NetworkContract
# Native CreateVpc/DeleteVpc SDK lifecycle and CFN/native ownership separation.
from ec2_native_vpc import NativeVpc
# Native subnets and instance launch records: SDK lifecycle, idempotency and refusals.
from ec2_native_instance import NativeInstance


def template(description):
    sample = json.loads((ROOT / "examples/cloudformation-rest-lambda.json").read_text())
    sample["Description"] = description
    return json.dumps(sample, ensure_ascii=False)


class StackLifecycle(unittest.TestCase):
    def setUp(self):
        self.endpoint, self.token = self.enterContext(running(authenticated=True))
        self.cf = self.sdk("cloudformation")
        self.ssm = self.sdk("ssm")

    def sdk(self, service):
        return client(service, self.endpoint, self.token, region="us-east-1")

    def get(self, path):
        return urlopen(Request(self.endpoint + path, headers={"X-Simulator-Token": self.token}),
                       timeout=30)

    def test_oversize_description_is_rejected_before_resources_are_created(self):
        for byte_length in (1025, 1105):
            with self.subTest(byte_length=byte_length):
                description = "あ" * (byte_length // 3) + "x" * (byte_length % 3)
                body = template(description)
                for operation, args in (
                    (self.cf.validate_template, {}),
                    (self.cf.create_stack, {"StackName": "sdk-sample", "Capabilities": ["CAPABILITY_IAM"]}),
                ):
                    with self.subTest(operation=operation.__name__):
                        with self.assertRaises(ClientError) as rejected:
                            operation(TemplateBody=body, **args)
                        error = rejected.exception.response["Error"]
                        self.assertEqual(error["Code"], "ValidationError")
                        self.assertIn("1024 bytes", error["Message"])
                self.assertEqual(self.cf.describe_stacks()["Stacks"], [])
                self.assertEqual(self.ssm.describe_parameters()["Parameters"], [])
                self.assertEqual(self.sdk("apigateway").get_rest_apis()["items"], [])
                self.assertEqual(self.sdk("lambda").list_functions()["Functions"], [])

    def test_sdk_stack_serves_requests_enforces_waf_and_cleans_up(self):
        body = template("あ" * 341 + "x")  # Exactly 1,024 UTF-8 bytes.
        stack = self.cf.create_stack(StackName="sdk-sample", TemplateBody=body,
                                     Capabilities=["CAPABILITY_IAM"])
        record, = self.cf.describe_stacks(StackName="sdk-sample")["Stacks"]
        self.assertEqual(record["StackId"], stack["StackId"])
        self.assertEqual(record["StackStatus"], "CREATE_COMPLETE")
        outputs = {item["OutputKey"]: item["OutputValue"] for item in record["Outputs"]}
        self.assertEqual(self.ssm.get_parameter(Name="/cfn-rest/public")["Parameter"]["Value"],
                         "safe-dummy")

        waf = self.sdk("wafv2")
        stage = f"arn:aws:apigateway:us-east-1::/restapis/{outputs['ApiId']}/stages/prod"
        visibility = {"MetricName": "sample", "SampledRequestsEnabled": False,
                      "CloudWatchMetricsEnabled": False}
        rule = {"Name": "config-route", "Priority": 0, "VisibilityConfig": visibility,
                "Statement": {"ByteMatchStatement": {
                    "FieldToMatch": {"UriPath": {}}, "PositionalConstraint": "EXACTLY",
                    "SearchString": b"/config",
                    "TextTransformations": [{"Priority": 0, "Type": "NONE"}]}},
                "Action": {"Count": {}}}
        settings = {"Name": "sdk-sample", "Scope": "REGIONAL", "DefaultAction": {"Allow": {}},
                    "VisibilityConfig": visibility, "Rules": [rule]}
        acl = waf.create_web_acl(**settings)["Summary"]
        waf.associate_web_acl(WebACLArn=acl["ARN"], ResourceArn=stage)
        path = outputs["LocalPath"]
        with self.get(path) as response:
            self.assertEqual(json.load(response), {"value": "safe-dummy", "path": "/config"})
        rule["Action"] = {"Block": {}}
        updated = waf.update_web_acl(**settings, Id=acl["Id"], LockToken=acl["LockToken"])
        with self.assertRaises(HTTPError) as blocked:
            self.get(path)
        with blocked.exception as response:
            self.assertEqual(response.code, 403)
        waf.disassociate_web_acl(ResourceArn=stage)
        with self.get(path) as response:
            self.assertEqual(response.status, 200)
        waf.delete_web_acl(Name=acl["Name"], Scope="REGIONAL", Id=acl["Id"],
                           LockToken=updated["NextLockToken"])

        self.cf.delete_stack(StackName="sdk-sample")
        self.assertEqual(self.cf.describe_stacks()["Stacks"], [])
        self.assertEqual(self.ssm.describe_parameters()["Parameters"], [])
        self.assertEqual(self.sdk("apigateway").get_rest_apis()["items"], [])
        self.assertEqual(self.sdk("lambda").list_functions()["Functions"], [])


if __name__ == "__main__":
    unittest.main()
