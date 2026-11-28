"""Exercise actual Cloudscape, Scalar and SDK clients against the same notification rules."""

import argparse
from contextlib import ExitStack
import json
import os
import re
from pathlib import Path
import tempfile
import time
from urllib.parse import urlsplit

from playwright.sync_api import Error as PlaywrightError, expect, sync_playwright

from codestar_notifications import CREATED, UPDATED, clients, control
from codestar_notification_scalar import NotificationReference
from sts_session_lifecycle import LocalSimulator


SERVICE = "codestar-notifications"
ROUTE = "/_simulator/console/#/codestar-notifications"


def exercise(native):
    binary = Path(os.environ["SIMULATOR_BINARY"]).resolve(strict=True)
    axe = Path(os.environ["AXE_SCRIPT"]).read_text()
    if not native:
        assert not any(key.startswith(("AWS_", "BOTO_")) for key in os.environ)
    receipt = {
        "stockSDK": not native,
        "sends": 0,
        "replies": 0,
        "browser": [],
        "audits": [],
        "events": [],
    }
    with tempfile.TemporaryDirectory(prefix="branch-console-") as folder, ExitStack() as stack:
        team, peer = [LocalSimulator(Path(folder) / name, binary) for name in ("owner", "peer")]
        for instance in (team, peer):
            stack.callback(instance.close)
            instance.start()
        owner = clients(team, team.token, stack, native, receipt)
        other = clients(peer, peer.token, stack, native, receipt)
        source = owner("codecommit", "CreateRepository", repositoryName="practice")[
            "repositoryMetadata"
        ]
        peer_source = other("codecommit", "CreateRepository", repositoryName="practice")[
            "repositoryMetadata"
        ]
        assert (
            source["Arn"] == peer_source["Arn"]
            and source["repositoryId"] != peer_source["repositoryId"]
        )
        topic = owner("sns", "CreateTopic", Name="events")["TopicArn"]
        queue = owner("sqs", "CreateQueue", QueueName="events")["QueueUrl"]
        queue_arn = owner("sqs", "GetQueueAttributes", QueueUrl=queue, AttributeNames=["QueueArn"])[
            "Attributes"
        ]["QueueArn"]
        subscription = owner(
            "sns", "Subscribe", TopicArn=topic, Protocol="sqs", Endpoint=queue_arn
        )["SubscriptionArn"]
        owner(
            "sns",
            "SetTopicAttributes",
            TopicArn=topic,
            AttributeName="Policy",
            AttributeValue=json.dumps(
                {
                    "Version": "2012-10-17",
                    "Statement": [
                        {
                            "Effect": "Allow",
                            "Principal": {"Service": "codestar-notifications.amazonaws.com"},
                            "Action": "SNS:Publish",
                            "Resource": topic,
                        }
                    ],
                }
            ),
        )

        def rule(name, repo=source["Arn"], targets=None):
            return owner(
                SERVICE,
                "CreateNotificationRule",
                Name=name,
                Resource=repo,
                EventTypeIds=[CREATED, UPDATED],
                Targets=targets or [],
                DetailType="BASIC",
            )["Arn"]

        seed = rule("SDK existing rule", targets=[{"TargetType": "SNS", "TargetAddress": topic}])

        def put(contents, parent=""):
            return owner(
                "codecommit",
                "PutFile",
                repositoryName="practice",
                branchName="main",
                filePath="note.txt",
                fileContent=contents,
                **({"parentCommitId": parent} if parent else {}),
            )["commitId"]

        def receive(expected_rule, tip, previous=None):
            limit = time.monotonic() + 15
            items = []
            while time.monotonic() < limit and not items:
                items = owner(
                    "sqs",
                    "ReceiveMessage",
                    QueueUrl=queue,
                    WaitTimeSeconds=1,
                    MaxNumberOfMessages=10,
                ).get("Messages", [])
            assert len(items) == 1, items
            item = items[0]
            event = json.loads(json.loads(item["Body"])["Message"])
            detail = event["detail"]
            assert event["notificationRuleArn"] == expected_rule and event["resources"] == [
                source["Arn"]
            ]
            assert (
                detail["repositoryId"] == source["repositoryId"]
                and detail["referenceFullName"] == "refs/heads/main"
            )
            assert detail["commitId"] == tip and detail.get("oldCommitId") == previous
            receipt["events"].append([detail["event"], tip, previous])
            owner("sqs", "DeleteMessage", QueueUrl=queue, ReceiptHandle=item["ReceiptHandle"])

        pw = stack.enter_context(sync_playwright())
        browser = pw.chromium.launch(
            executable_path=os.environ["CHROMIUM_EXECUTABLE"], chromium_sandbox=True
        )
        stack.callback(browser.close)

        def open_page(authority, token, lang="en", width=1365):
            ctx = browser.new_context(
                service_workers="block",
                viewport={"width": width, "height": 900},
                reduced_motion="reduce",
            )
            stack.callback(ctx.close)
            errors = []

            def local(route):
                u = urlsplit(route.request.url)
                if (u.scheme, u.netloc) != ("http", f"127.0.0.1:{authority.port}"):
                    errors.append(u.hostname)
                    route.abort()
                else:
                    route.continue_()

            ctx.route("**/*", local)
            response = ctx.request.post(
                authority.endpoint + "/_simulator/login", form={"token": token}, max_redirects=0
            )
            assert response.status == 303
            response.dispose()
            ctx.add_init_script(
                "localStorage.setItem('tenkacloud-simulator-locale'," + json.dumps(lang) + ")"
            )
            page = ctx.new_page()
            page.on("pageerror", lambda err: errors.append(str(err)))

            def record(res):
                if res.request.headers.get("x-simulator-service") != SERVICE:
                    return
                assert (
                    res.request.method == "POST"
                    and res.request.headers["x-simulator-region"] == "us-east-1"
                )
                assert "x-simulator-token" not in res.request.headers
                receipt["browser"].append([urlsplit(res.url).path, res.status])

            page.on("response", record)
            page.goto(authority.endpoint + ROUTE)
            expect(
                page.get_by_role(
                    "heading",
                    name="通知ルール" if lang == "ja" else "Notification rules",
                    exact=True,
                )
            ).to_be_visible()
            return page, errors

        def audit(page, name):
            page.evaluate(axe)
            violations = page.evaluate("""async () => (await axe.run(document, {
                runOnly: {type: 'tag', values: ['wcag2a','wcag2aa','wcag21a','wcag21aa']}
            })).violations.map(v => [v.id,v.nodes.map(n=>n.target)])""")
            assert not violations, (name, violations)
            assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth")
            receipt["audits"].append(name)

        def button(page, text):
            return page.get_by_role("button", name=text, exact=True)

        def dropped_write(page, operation, act):
            path = "/" + operation[0].lower() + operation[1:]
            captured = []

            def discard(route):
                res = route.fetch()
                assert res.status == 200
                captured.append((route.request.post_data_json, res.json()))
                res.dispose()
                route.abort("failed")

            page.route("**" + path, discard, times=1)
            act()
            expect(page.get_by_text("Write outcome needs verification", exact=True)).to_be_visible()
            assert len(captured) == 1
            return captured[0]

        page, issues = open_page(team, team.token)
        expect(button(page, seed.rsplit("/", 1)[1])).to_be_visible()
        entry = button(page, seed.rsplit("/", 1)[1])
        entry.focus()
        entry.press("Enter")
        expect(page.get_by_role("heading", name="SDK existing rule", exact=True)).to_be_visible()
        expect(page.get_by_text(topic, exact=True)).to_be_visible()
        audit(page, "English rule detail")
        first = put(b"one")
        receive(seed, first)
        button(page, "Edit rule").click()
        page.get_by_role("checkbox", name="Enabled", exact=True).uncheck()
        dropped_write(page, "UpdateNotificationRule", lambda: button(page, "Save rule").click())
        assert owner(SERVICE, "DescribeNotificationRule", Arn=seed)["Status"] == "DISABLED"
        expect(button(page, "Back to rules")).to_be_disabled()
        button(page, "Refresh affected rule").click()
        expect(page.get_by_text("DISABLED", exact=True)).to_be_visible()
        button(page, "Back to rules").click()
        button(page, "Create rule").click()
        page.get_by_label("Rule name", exact=True).fill("Console owned rule")
        page.get_by_label("Repository ARN", exact=True).fill(source["Arn"])
        page.get_by_label("SNS topic ARNs", exact=True).fill(topic)
        audit(page, "English create")
        initial, ack = dropped_write(
            page, "CreateNotificationRule", lambda: button(page, "Save rule").click()
        )
        new = ack["Arn"]
        assert (
            initial.get("ClientRequestToken")
            and owner(SERVICE, "DescribeNotificationRule", Arn=new)["Name"] == "Console owned rule"
        )
        expect(button(page, "Create rule")).to_be_disabled()
        with page.expect_request(
            lambda r: r.url == team.endpoint + "/createNotificationRule"
        ) as retry:
            button(page, "Retry original request").click()
        assert retry.value.post_data_json == initial
        expect(page.get_by_role("heading", name="Console owned rule", exact=True)).to_be_visible()
        assert len(owner(SERVICE, "ListNotificationRules")["NotificationRules"]) == 2
        second = put(b"two", first)
        receive(new, second, first)
        assert not owner("sqs", "ReceiveMessage", QueueUrl=queue, WaitTimeSeconds=2).get("Messages")
        scalar = NotificationReference(browser, team, team.token, stack)
        assert (
            scalar.send("DescribeNotificationRule", {"Arn": new}, 200, "Console owned rule")[
                "Targets"
            ][0]["TargetAddress"]
            == topic
        )

        ja, jp_errors = open_page(team, team.token, "ja", 390)
        button(ja, new.rsplit("/", 1)[1]).click()
        expect(ja.get_by_role("heading", name="Console owned rule", exact=True)).to_be_visible()
        audit(ja, "Japanese mobile detail")
        button(ja, "ルールを編集").click()
        audit(ja, "Japanese mobile edit")
        button(ja, "キャンセル").click()

        control(
            team,
            "PUT",
            "/_simulator/v1/iam/principals/rule-reader/policy",
            {
                "Version": "2012-10-17",
                "Statement": [
                    {
                        "Effect": "Allow",
                        "Action": [
                            "codestar-notifications:ListNotificationRules",
                            "codestar-notifications:DescribeNotificationRule",
                        ],
                        "Resource": "*",
                    }
                ],
            },
        )
        reader_token = control(team, "POST", "/_simulator/v1/access", {"Principal": "rule-reader"})[
            "AccessToken"
        ]
        reader, reader_errors = open_page(team, reader_token)
        button(reader, new.rsplit("/", 1)[1]).click()
        button(reader, "Edit rule").click()
        reader.get_by_label("Rule name", exact=True).fill("Refused")
        button(reader, "Save rule").click()
        expect(reader.get_by_text(re.compile("403:"))).to_be_visible()
        assert owner(SERVICE, "DescribeNotificationRule", Arn=new)["Name"] == "Console owned rule"
        isolated, peer_errors = open_page(peer, peer.token)
        expect(isolated.get_by_text("No notification rules", exact=True)).to_be_visible()
        assert not other(SERVICE, "ListNotificationRules")["NotificationRules"]

        button(page, "Back to rules").click()
        requests = []

        def hold(route):
            response = route.fetch()
            assert response.json()["Arn"] == new
            requests.append((route, response))

        page.route("**/describeNotificationRule", hold, times=1)
        button(page, new.rsplit("/", 1)[1]).click()
        expect(button(page, "Cancel loading")).to_be_visible()
        button(page, "Cancel loading").click()
        assert len(requests) == 1
        route, response = requests[0]
        try:
            route.fulfill(response=response)
        except PlaywrightError:
            pass
        response.dispose()
        expect(page.get_by_role("heading", name="Console owned rule", exact=True)).to_have_count(0)
        button(page, "Refresh rule").click()
        expect(page.get_by_role("heading", name="Console owned rule", exact=True)).to_be_visible()

        button(page, "Delete rule").click()
        page.get_by_label("Confirm rule name", exact=True).fill("Console owned rule")
        button(page, "Confirm deletion").click()
        expect(page.get_by_text("Saved state verified.", exact=True)).to_be_visible()
        assert [r["Arn"] for r in owner(SERVICE, "ListNotificationRules")["NotificationRules"]] == [
            seed
        ]
        third = put(b"three", second)
        assert (
            owner("codecommit", "GetBranch", repositoryName="practice", branchName="main")[
                "branch"
            ]["commitId"]
            == third
        )
        assert not owner("sqs", "ReceiveMessage", QueueUrl=queue, WaitTimeSeconds=2).get("Messages")
        for i in range(2):
            name = "pages-" + str(i)
            repo = owner("codecommit", "CreateRepository", repositoryName=name)[
                "repositoryMetadata"
            ]["Arn"]
            for index in range(10):
                rule(f"Paging {i} {index}", repo)
        button(page, "Refresh rules").click()
        button(page, "Load more rules").click()
        expect(button(page, seed.rsplit("/", 1)[1])).to_be_visible()
        expect(button(page, "Load more rules")).to_have_count(0)

        button(reader, "Cancel").click()
        button(reader, "Back to rules").click()
        expect(button(reader, "Load more rules")).to_be_visible()
        grid = reader.get_by_role("table", name="Notification rules")
        old_row = grid.get_by_role("button").first.inner_text()
        control(
            team,
            "PUT",
            "/_simulator/v1/iam/principals/rule-reader/policy",
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
        button(reader, "Load more rules").click()
        expect(reader.get_by_text(re.compile("403:"))).to_be_visible()
        expect(reader.get_by_text("Rules could not be loaded", exact=True)).to_be_visible()
        expect(button(reader, old_row)).to_have_count(0)
        expect(button(reader, "Load more rules")).to_have_count(0)
        control(
            team,
            "PUT",
            "/_simulator/v1/iam/principals/rule-reader/policy",
            {
                "Version": "2012-10-17",
                "Statement": [
                    {
                        "Effect": "Allow",
                        "Action": [
                            "codestar-notifications:ListNotificationRules",
                            "codestar-notifications:DescribeNotificationRule",
                        ],
                        "Resource": "*",
                    }
                ],
            },
        )
        button(reader, "Refresh rules").click()
        expect(button(reader, old_row)).to_be_visible()
        button(reader, "Load more rules").click()
        expect(button(reader, seed.rsplit("/", 1)[1])).to_be_visible()
        for i in range(2):
            owner("codecommit", "DeleteRepository", repositoryName="pages-" + str(i))
        owner(SERVICE, "DeleteNotificationRule", Arn=seed)
        owner("sns", "Unsubscribe", SubscriptionArn=subscription)
        owner("sqs", "DeleteQueue", QueueUrl=queue)
        owner("sns", "DeleteTopic", TopicArn=topic)
        owner("codecommit", "DeleteRepository", repositoryName="practice")
        other("codecommit", "DeleteRepository", repositoryName="practice")
        assert not (issues or jp_errors or reader_errors or peer_errors)
    assert not Path(folder).exists() and receipt["sends"] == receipt["replies"]
    print(
        json.dumps(
            {
                **receipt,
                "cleanup": True,
                "lostWriteReadback": True,
                "stableCreateToken": True,
                "canceledRead": True,
                "scalarReadback": True,
                "paginatedRules": 21,
                "deniedContinuationCleared": True,
                "postDeleteNoDelivery": True,
            }
        )
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--native", action="store_true")
    exercise(parser.parse_args().native)
