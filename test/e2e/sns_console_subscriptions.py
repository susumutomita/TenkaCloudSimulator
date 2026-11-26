"""Opt-in authenticated SNS console lifecycle on an exact, approved local binary.

SIMULATOR_BINARY=/path/to/build SIMULATOR_BINARY_SHA256=<verified digest>
CHROMIUM_EXECUTABLE=/path/to/chromium python test/e2e/sns_console_subscriptions.py

Use a dedicated loopback-only runner with working Chromium sandbox support.
Neither endpoint metadata value is contacted. No live AWS or browser mocks.
"""
import hashlib
import os
from pathlib import Path
import re
from urllib.parse import parse_qsl, urlsplit
from xml.etree import ElementTree as XML

from playwright.sync_api import sync_playwright, expect
from check import BINARY, open_service, running

NS = "{http://sns.amazonaws.com/doc/2010-03-31/}"
FORM = "application/x-www-form-urlencoded"
TOPIC = "sns-http-console-acceptance"
ARN = "arn:aws:sns:us-east-1:000000000000:" + TOPIC
INVALID = "http://127.0.0.1:18080/events"
VALID = "https://127.0.0.1:18443/my%20events?team=blue&show=all"
ACTIONS = {"ListTopics", "ListSubscriptionsByTopic", "Subscribe", "Unsubscribe"}


def namespace_isolated():
    # Match the repository's opt-in browser lanes. A route observer is not a firewall.
    interfaces = Path("/proc/net/dev").read_text().splitlines()[2:]
    return [row.split(":", 1)[0].strip() for row in interfaces] == ["lo"]


def fields(response):
    return dict(parse_qsl(response.request.post_data or "", strict_parsing=True))


def run():
    expected = os.environ.get("SIMULATOR_BINARY_SHA256", "")
    assert __debug__ and namespace_isolated(), "A dedicated Linux loopback-only runner is required"
    assert os.environ.get("SIMULATOR_BINARY") and re.fullmatch("[a-f0-9]{64}", expected)
    with BINARY.open("rb") as binary:
        assert hashlib.file_digest(binary, "sha256").hexdigest() == expected

    with running(authenticated=True) as (origin, operator), sync_playwright() as playwright:
        browser = playwright.chromium.launch(executable_path=os.environ["CHROMIUM_EXECUTABLE"],
                                             chromium_sandbox=True, args=["--no-proxy-server"])
        try:
            context = browser.new_context(locale="en-US", service_workers="block")
            origin_parts = urlsplit(origin)
            assert origin_parts.scheme == "http" and origin_parts.hostname == "127.0.0.1"
            outside, errors, observed = [], [], []

            def route_only_simulator(route):
                url = urlsplit(route.request.url)
                if (url.scheme, url.netloc) == (origin_parts.scheme, origin_parts.netloc):
                    route.continue_()
                else:
                    outside.append(True)
                    route.abort()

            context.route("**/*", route_only_simulator)
            page = context.new_page()
            page.on("pageerror", lambda _: errors.append(True))

            def observe(response):
                if response.url == origin + "/" and response.request.method == "POST":
                    if response.request.headers.get("x-simulator-service") == "sns":
                        observed.append((fields(response)["Action"], response.status))

            page.on("response", observe)

            def server_query(action, status=200, **values):
                reply = context.request.post(origin + "/", max_redirects=0, headers={
                    "X-Simulator-Token": operator, "X-Simulator-Service": "sns",
                    "X-Simulator-Region": "us-east-1", "Content-Type": FORM,
                }, form={"Action": action, "Version": "2010-03-31", **values})
                assert reply.url == origin + "/" and reply.status == status, f"{action}: unexpected result"
                return XML.fromstring(reply.body())

            def saved_members():
                return server_query("ListSubscriptionsByTopic", TopicArn=ARN).findall(".//" + NS + "member")

            def is_action(action):
                return lambda response: response.url == origin + "/" and response.request.method == "POST" and (
                    response.request.headers.get("x-simulator-service") == "sns" and fields(response).get("Action") == action)

            server_query("CreateTopic", Name=TOPIC)
            try:
                page.goto(origin + "/_simulator/console/")
                page.get_by_label("Access token", exact=True).fill(operator)
                page.get_by_role("button", name="Open sandbox", exact=True).click()
                open_service(page, "SNS")
                expect(page.get_by_role("heading", name="Subscriptions", exact=True)).to_be_visible()
                expect(page.get_by_text("No delivery, reachability check, or AWS confirmation occurs.",
                                        exact=False)).to_be_visible()
                page.get_by_label("Topic ARN", exact=True).fill(ARN)
                with page.expect_response(is_action("ListSubscriptionsByTopic")) as listed:
                    page.get_by_role("button", name="Open topic", exact=True).click()
                assert listed.value.status == 200
                expect(page.get_by_text("No subscriptions for this topic.", exact=True)).to_be_visible()

                def submit(url, status):
                    page.get_by_label("Endpoint", exact=True).fill(url)
                    with page.expect_response(is_action("Subscribe")) as made:
                        page.get_by_role("button", name="Create subscription", exact=True).click()
                    reply = made.value
                    assert reply.status == status and fields(reply) == {
                        "Action": "Subscribe", "Version": "2010-03-31", "TopicArn": ARN,
                        "Protocol": "https", "Endpoint": url,
                    }
                    headers = reply.request.all_headers()
                    assert headers.get("cookie") and headers.get("x-simulator-region") == "us-east-1"
                    assert "x-simulator-token" not in headers and "authorization" not in headers
                    return XML.fromstring(reply.body())

                page.get_by_role("button", name="Create subscription", exact=True).click()
                expect(page.get_by_role("heading", name="Create subscription", exact=True)).to_be_visible()
                error = submit(INVALID, 400)
                assert error.findtext(NS + "Error/" + NS + "Code") == "InvalidParameter"
                expect(page.get_by_text("InvalidParameter:", exact=False)).to_be_visible()
                assert not saved_members()

                good = submit(VALID, 200)
                sub = good.findtext(".//" + NS + "SubscriptionArn")
                assert sub and sub.startswith(ARN + ":")
                expect(page.get_by_text("Subscription saved locally.", exact=True)).to_be_visible()
                expect(page.get_by_role("cell", name=sub, exact=True)).to_be_visible()
                expect(page.get_by_role("cell", name=VALID, exact=True)).to_be_visible()
                rows = saved_members()
                assert len(rows) == 1 and rows[0].findtext(NS + "Endpoint") == VALID
                page.get_by_role("button", name="Refresh subscriptions", exact=True).click()
                expect(page.get_by_role("cell", name=VALID, exact=True)).to_be_visible()

                # Cloudscape puts the group label on the column header, not on each row.
                page.get_by_role("radio", name=sub, exact=True).check()
                page.get_by_role("button", name="Delete subscription", exact=True).click()
                dialog = page.get_by_role("dialog")
                expect(dialog.get_by_text("Only this saved subscription is removed.", exact=False)).to_be_visible()
                with page.expect_response(is_action("Unsubscribe")) as removed:
                    dialog.get_by_role("button", name="Unsubscribe", exact=True).click()
                assert removed.value.status == 200
                assert fields(removed.value) == {"Action": "Unsubscribe", "Version": "2010-03-31",
                                                   "SubscriptionArn": sub}
                expect(page.get_by_text("Subscription removed.", exact=True)).to_be_visible()
                expect(page.get_by_text("No subscriptions for this topic.", exact=True)).to_be_visible()
                assert not saved_members()
                assert "ListTopics" in {action for action, _ in observed}
                assert {action for action, _ in observed} == ACTIONS
                assert ("Subscribe", 400) in observed and ("Subscribe", 200) in observed
                assert not outside and not errors
            finally:
                server_query("DeleteTopic", TopicArn=ARN)
                gone = server_query("GetTopicAttributes", status=404, TopicArn=ARN)
                assert gone.findtext(NS + "Error/" + NS + "Code") == "NotFound"
        finally:
            browser.close()
    print("SNS GUI: native topic lookup, rejected/accepted subscriptions, read-back, no external requests, cleanup")


if __name__ == "__main__":
    run()
