"""Drive five real Scalar Sends and independently read their saved notification rules."""

import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright

from codestar_notifications import CREATED, clients, control
from sts_session_lifecycle import LocalSimulator


SERVICE = "codestar-notifications"
REFERENCE = "/_simulator/docs/codestar-notifications"


class NotificationReference:
    def __init__(self, browser, team, token, stack):
        self.origin = team.endpoint
        self.attempts = []
        self.errors = []
        self.context = browser.new_context(service_workers="block")
        stack.callback(self.context.close)

        def local(route):
            parsed = urlsplit(route.request.url)
            if parsed.scheme != "http" or parsed.netloc != f"127.0.0.1:{team.port}":
                self.errors.append("unexpected origin")
                route.abort()
            else:
                route.continue_()

        self.context.route("**/*", local)
        login = self.context.request.post(
            team.endpoint + "/_simulator/login", form={"token": token}, max_redirects=0
        )
        assert login.status == 303
        login.dispose()

    def send(self, operation, payload, status, visible=""):
        path = "/" + operation[0].lower() + operation[1:]
        page = self.context.new_page()
        try:
            page.on("pageerror", lambda error: self.errors.append(str(error)))
            observed = []
            page.on("request", lambda req: observed.append(req) if req.method == "POST" else None)
            with page.expect_response(
                lambda r: r.url == self.origin + REFERENCE + ".json"
            ) as loaded:
                page.goto(self.origin + REFERENCE + ".html")
            spec = loaded.value.json()["paths"][path]["post"]
            assert spec["operationId"] == operation
            page.get_by_role("link").filter(
                has_text=re.compile(
                    "^" + re.escape(spec["summary"]) + r"\s*HTTP Method:\s*POST$", re.I
                )
            ).click()
            page.get_by_role("button", name=f"Test Request (post {path})", exact=True).click()
            dialog = page.get_by_role("dialog").filter(
                has=page.get_by_role("button", name="Close Client", exact=True)
            )
            block = dialog.get_by_role("region", name="Request: " + spec["summary"], exact=True)
            block.get_by_role("tab", name="Body", exact=True).press("Enter")
            editor = block.locator(".cm-content[contenteditable=true]:visible")
            editor.click()
            editor.press("ControlOrMeta+a")
            page.keyboard.insert_text(json.dumps(payload))
            assert json.loads(editor.inner_text()) == payload and not observed
            with page.expect_response(lambda r: r.request.method == "POST") as sent:
                dialog.get_by_role("button", name=re.compile("^Send post request to ")).click()
            response = sent.value
            assert observed == [response.request] and response.url == self.origin + path
            assert response.status == status, (operation, response.status)
            headers = response.request.all_headers()
            assert (
                headers["x-simulator-service"] == SERVICE
                and headers["x-simulator-region"] == "us-east-1"
            )
            assert headers["content-type"] == "application/json" and headers.get("cookie")
            assert not {"authorization", "x-amz-target", "x-simulator-token"} & headers.keys()
            assert response.request.post_data_json == payload
            body = response.json()
            if visible or body:
                panel = dialog.locator(".response-section-content-body").filter(
                    has=page.get_by_role("link", name="Download Response Body", exact=True)
                )
                expect(panel).to_contain_text(visible or next(iter(body)))
            assert not self.errors
            self.attempts.append([operation, status])
            return body
        finally:
            page.close()


def exercise(native):
    binary = Path(os.environ["SIMULATOR_BINARY"]).resolve(strict=True)
    if not native:
        assert not any(key.startswith(("AWS_", "BOTO_")) for key in os.environ)
    receipt = {"stockSDK": not native, "sends": 0, "replies": 0}
    with (
        tempfile.TemporaryDirectory(prefix="notification-reference-") as directory,
        ExitStack() as stack,
    ):
        team, peer = [LocalSimulator(Path(directory) / name, binary) for name in ("owner", "peer")]
        for authority in (team, peer):
            stack.callback(authority.close)
            authority.start()
        sdk = clients(team, team.token, stack, native, receipt)
        source = sdk("codecommit", "CreateRepository", repositoryName="practice")[
            "repositoryMetadata"
        ]["Arn"]
        stack.callback(lambda: sdk("codecommit", "DeleteRepository", repositoryName="practice"))
        control(
            team,
            "PUT",
            "/_simulator/v1/iam/principals/reference-reader/policy",
            {
                "Version": "2012-10-17",
                "Statement": [
                    {
                        "Effect": "Allow",
                        "Action": "codestar-notifications:DescribeNotificationRule",
                        "Resource": "*",
                    }
                ],
            },
        )
        reader = control(team, "POST", "/_simulator/v1/access", {"Principal": "reference-reader"})[
            "AccessToken"
        ]
        playwright = stack.enter_context(sync_playwright())
        browser = playwright.chromium.launch(
            executable_path=os.environ["CHROMIUM_EXECUTABLE"], chromium_sandbox=True
        )
        stack.callback(browser.close)
        owner = NotificationReference(browser, team, team.token, stack)
        limited = NotificationReference(browser, team, reader, stack)
        other = NotificationReference(browser, peer, peer.token, stack)
        rule = owner.send(
            "CreateNotificationRule",
            {
                "Name": "Reference rule",
                "Resource": source,
                "EventTypeIds": [CREATED],
                "Targets": [],
                "DetailType": "BASIC",
            },
            200,
        )["Arn"]
        assert sdk(SERVICE, "DescribeNotificationRule", Arn=rule)["Resource"] == source
        assert (
            owner.send("ListNotificationRules", {}, 200, rule)["NotificationRules"][0]["Arn"]
            == rule
        )
        assert (
            limited.send("DescribeNotificationRule", {"Arn": rule}, 200, "Reference rule")["Arn"]
            == rule
        )
        limited.send(
            "UpdateNotificationRule",
            {"Arn": rule, "Status": "DISABLED"},
            403,
            "AccessDeniedException",
        )
        other.send("DescribeNotificationRule", {"Arn": rule}, 404, "ResourceNotFoundException")
        assert owner.send("UpdateNotificationRule", {"Arn": rule, "Status": "DISABLED"}, 200) == {}
        assert sdk(SERVICE, "DescribeNotificationRule", Arn=rule)["Status"] == "DISABLED"
        assert owner.send("DeleteNotificationRule", {"Arn": rule}, 200, rule)["Arn"] == rule
        assert sdk(SERVICE, "ListNotificationRules")["NotificationRules"] == []
        receipt.update(owner=owner.attempts, reader=limited.attempts, peer=other.attempts)
    assert not Path(directory).exists() and receipt["sends"] == receipt["replies"]
    print(json.dumps({**receipt, "cleanup": True}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--native", action="store_true")
    exercise(parser.parse_args().native)
