"""Opt-in served Console, Chromium and SDK against the same private simulator."""

import json
import os
from pathlib import Path
import shutil
import tempfile
import unittest

from check import boto3, Config, expect, running, sync_playwright
from reflow import assert_reflow


class NativeSecurityGroupConsole(unittest.TestCase):
    def test_console_uses_live_local_ec2_state(self):
        desktop = {"width": 1100, "height": 800}
        chromium = os.environ.get("CHROMIUM_EXECUTABLE") or shutil.which("chromium")
        self.assertIsNotNone(chromium, "A real Chromium executable is required for this opt-in check")
        state = Path(self.enterContext(tempfile.TemporaryDirectory(prefix="native-sg-gui-"))) / "team.sqlite"
        playwright = self.enterContext(sync_playwright())
        browser = playwright.chromium.launch(executable_path=chromium, args=["--no-sandbox", "--disable-background-networking"])
        self.addCleanup(browser.close)

        def client(endpoint, operator, service="ec2"):
            self.assertTrue(endpoint.startswith("http://127.0.0.1:"))
            sdk = boto3.client(
                service, endpoint_url=endpoint, region_name="us-east-1",
                aws_access_key_id="TCSIM_SG_CONSOLE", aws_secret_access_key="dummy-local-only",
                config=Config(retries={"max_attempts": 0}, proxies={}, connect_timeout=3, read_timeout=10),
            )
            sdk.meta.events.register("before-sign", lambda request, **_: request.headers.add_header("X-Simulator-Token", operator))
            self.addCleanup(sdk.close)
            return sdk

        def page_for(endpoint, operator):
            context = browser.new_context(viewport=desktop)
            context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(endpoint + "/") else route.abort())
            page = context.new_page()
            page.set_default_timeout(10000)
            page.goto(endpoint + "/_simulator/console/#/vpc")
            page.get_by_label("Access token", exact=True).fill(operator)
            page.get_by_role("button", name="Open sandbox", exact=True).click()
            page.get_by_role("button", name="Services", exact=True).click()
            page.get_by_role("menuitem", name="VPC", exact=True).click()
            expect(page.get_by_role("heading", name="VPC", level=1, exact=True)).to_be_visible()
            return context, page

        def select_vpc(page, vpc):
            page.get_by_role("radio", name="Select " + vpc, exact=True).check()
            expect(page.get_by_role("heading", name="Resource details", exact=True)).to_be_visible()

        def by_vpc(ec2, vpc):
            return ec2.describe_security_groups(Filters=[{"Name": "vpc-id", "Values": [vpc]}])["SecurityGroups"]

        with running(state, authenticated=True) as (endpoint, operator):
            ec2, cfn = client(endpoint, operator), client(endpoint, operator, "cloudformation")
            one = ec2.create_vpc(CidrBlock="10.72.0.0/16")["Vpc"]["VpcId"]
            two = ec2.create_vpc(CidrBlock="10.73.0.0/16")["Vpc"]["VpcId"]
            existing = ec2.create_security_group(VpcId=two, GroupName="Second VPC", Description="Second VPC SDK group")["GroupId"]
            stack = "sg-console-legacy"
            cfn.create_stack(StackName=stack, TemplateBody=json.dumps({
                "Resources": {
                    "Vpc": {"Type": "AWS::EC2::VPC", "Properties": {"CidrBlock": "10.74.0.0/16"}},
                    "Group": {"Type": "AWS::EC2::SecurityGroup", "Properties": {"VpcId": {"Ref": "Vpc"}, "GroupDescription": "Legacy read-only"}},
                },
                "Outputs": {"Vpc": {"Value": {"Ref": "Vpc"}}},
            }))
            legacy = cfn.describe_stacks(StackName=stack)["Stacks"][0]["Outputs"][0]["OutputValue"]
            context, page = page_for(endpoint, operator)
            try:
                select_vpc(page, legacy)
                expect(page.get_by_text("Legacy read-only", exact=True)).to_be_visible()
                expect(page.get_by_role("button", name="Create security group", exact=True)).to_have_count(0)
                expect(page.get_by_role("button", name="Delete security group", exact=True)).to_have_count(0)
                select_vpc(page, two)
                expect(page.get_by_text("Second VPC SDK group", exact=True)).to_be_visible()
                select_vpc(page, one)
                expect(page.get_by_text("Second VPC SDK group", exact=True)).to_have_count(0)
                expect(page.get_by_text("No detached security groups are recorded for this VPC.", exact=True)).to_be_visible()
                expect(page.get_by_text(
                    "Save TCP/IPv4 rules only within this VPC CIDR. Groups are not attached "
                    "and saved rules do not affect traffic.", exact=True,
                )).to_be_visible()
                create = page.get_by_role("button", name="Create security group", exact=True)
                create.click()
                dialog = page.get_by_role("dialog", name="Create security group")
                submit = dialog.get_by_role("button", name="Create security group", exact=True)
                expect(submit).to_be_disabled()
                dialog.get_by_label("Group name", exact=True).fill("default")
                dialog.get_by_label("Group description", exact=True).fill("Browser saved group")
                expect(submit).to_be_disabled()
                dialog.get_by_label("Group name", exact=True).fill("Browser group  ")
                submit.click()
                expect(page.get_by_text("Saved security group:", exact=False)).to_be_visible()
                saved, = by_vpc(ec2, one)
                self.assertEqual((saved["GroupName"], saved["Description"], saved["VpcId"]), ("Browser group", "Browser saved group", one))
                group = saved["GroupId"]
                expect(page.get_by_text(group, exact=True).first).to_be_visible()
                # A stale-looking selection never mutates the other VPC.
                self.assertEqual([x["GroupId"] for x in by_vpc(ec2, two)], [existing])
            finally:
                context.close()

        with running(state, authenticated=True) as (endpoint, operator):
            ec2, cfn = client(endpoint, operator), client(endpoint, operator, "cloudformation")
            context, page = page_for(endpoint, operator)
            try:
                select_vpc(page, one)
                expect(page.get_by_text("Browser saved group", exact=True)).to_be_visible()
                page.set_viewport_size({"width": 390, "height": 844})
                assert_reflow(page)
                page.get_by_role("button", name="Delete security group", exact=True).click()
                dialog = page.get_by_role("dialog", name="Delete security group")
                expect(dialog.get_by_text(group, exact=True)).to_be_visible()
                submit = dialog.get_by_role("button", name="Delete security group", exact=True)
                expect(submit).to_be_disabled()
                dialog.get_by_label("Type delete to confirm", exact=True).fill("delete")
                submit.click()
                expect(page.get_by_text("Removed security group:", exact=False)).to_be_visible()
                expect(page.get_by_text("No detached security groups are recorded for this VPC.", exact=True)).to_be_visible()
                self.assertEqual(by_vpc(ec2, one), [])
                self.assertEqual([x["GroupId"] for x in by_vpc(ec2, two)], [existing])
                self.assertEqual(len(by_vpc(ec2, legacy)), 1)
            finally:
                context.close()
            ec2.delete_security_group(GroupId=existing)
            for vpc in (one, two):
                ec2.delete_vpc(VpcId=vpc)
            cfn.delete_stack(StackName=stack)
            self.assertEqual(ec2.describe_vpcs()["Vpcs"], [])
            self.assertEqual(ec2.describe_security_groups()["SecurityGroups"], [])


if __name__ == "__main__":
    unittest.main()
