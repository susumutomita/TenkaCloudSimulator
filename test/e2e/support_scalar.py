"""Run real Scalar Send and stock boto3 against two disposable local Support authorities.

Use the repo's pinned test dependencies and set SIMULATOR_BINARY to a clean build.
Run in a neutral SDK environment with sandboxed Chromium and private loopback.
"""

from contextlib import closing, ExitStack
from http.client import HTTPConnection
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import tempfile
from urllib.parse import urlsplit
from uuid import uuid4

from playwright.sync_api import expect, sync_playwright
from sts_session_lifecycle import LocalSimulator


PREFIX = "AWSSupport_20130415."
MEDIA = "application/x-amz-json-1.1"


def support_sdk(team, stack):
    import boto3
    from botocore.config import Config

    client = boto3.client(
        "support", region_name="us-east-1", endpoint_url=team.endpoint,
        aws_access_key_id="TCSIMLOCAL", aws_secret_access_key="local-fixture-only",
        config=Config(proxies={}, retries={"max_attempts": 0}, connect_timeout=3, read_timeout=5),
    )
    stack.callback(client.close)
    client.meta.events.register("before-sign.support",
        lambda request, **_: request.headers.add_header("X-Simulator-Token", team.token))

    def check_signed(request, **_):
        assert request.method == "POST" and request.url == team.endpoint + "/"
        assert b"/us-east-1/support/aws4_request" in request.headers["Authorization"]

    client.meta.events.register("before-send.support", check_signed)
    return client


def provision_reader(team):
    def control(method, path, data):
        with closing(HTTPConnection("127.0.0.1", team.port, timeout=5)) as conn:
            conn.request(method, path, json.dumps(data), {
                "Content-Type": "application/json", "X-Simulator-Token": team.token,
            })
            response = conn.getresponse()
            assert 200 <= response.status < 300, (path, response.status)
            return json.loads(response.read())

    name = "support-scalar-reader"
    control("PUT", "/_simulator/v1/iam/principals/" + name + "/policy", {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Action": "support:DescribeCases", "Resource": "*"}],
    })
    return control("POST", "/_simulator/v1/access", {"Principal": name})["AccessToken"]


class ScalarSession:
    def __init__(self, browser, team, token, stack):
        self.origin = team.endpoint
        self.context = browser.new_context(service_workers="block")
        stack.callback(self.context.close)
        self.outside = []
        self.errors = []
        self.sends = []

        def allow_local(route):
            parsed = urlsplit(route.request.url)
            if (parsed.scheme, parsed.netloc) == ("http", f"127.0.0.1:{team.port}"):
                route.continue_()
            else:
                self.outside.append(parsed.hostname)
                route.abort()

        self.context.route("**/*", allow_local)
        login = self.context.request.post(self.origin + "/_simulator/login",
            form={"token": token}, max_redirects=0)
        assert login.status == 303
        login.dispose()

    def send(self, operation, payload, status):
        page = self.context.new_page()
        try:
            page.on("pageerror", lambda err: self.errors.append(str(err)))
            observed = []
            page.on("request", lambda req: observed.append(req) if req.method == "POST" else None)
            with page.expect_response(lambda res: res.url == self.origin + "/_simulator/docs/support.json") as loaded:
                page.goto(self.origin + "/_simulator/docs/support.html")
            spec = loaded.value.json()["paths"]["/"]["post"]
            target = PREFIX + operation
            assert target in spec["parameters"][0]["schema"]["enum"]
            assert operation in spec["requestBody"]["content"][MEDIA]["examples"]
            page.get_by_role("button", name="Test Request (post /)", exact=True).click()
            dialog = page.get_by_role("dialog").filter(has=page.get_by_role("button", name="Close Client", exact=True))
            request = dialog.get_by_role("region", name="Request: " + spec["summary"], exact=True)
            request.get_by_role("tab", name="Headers", exact=True).press("Enter")
            row = request.get_by_role("row", name=re.compile("^Include X-Amz-Target in request"))
            row.get_by_role("button", name=re.compile("^AWSSupport_20130415\\.")).click()
            page.get_by_role("menuitem", name=target, exact=True).click()
            expect(row.get_by_role("checkbox")).to_be_checked()
            request.get_by_role("tab", name="Body", exact=True).press("Enter")
            editor = request.locator(".cm-content[contenteditable=true]:visible")
            editor.click()
            editor.press("ControlOrMeta+a")
            page.keyboard.insert_text(json.dumps(payload, ensure_ascii=False))
            assert json.loads(editor.inner_text()) == payload and not observed
            with page.expect_response(lambda res: res.request.method == "POST") as sent:
                dialog.get_by_role("button", name=re.compile("^Send post request to ")).click()
            response = sent.value
            assert observed == [response.request]
            assert response.url == self.origin + "/" and response.status == status, (operation, response.status)
            assert response.request.post_data_json == payload
            headers = response.request.all_headers()
            assert headers["x-amz-target"] == target and headers["content-type"].split(";")[0] == MEDIA
            assert headers["x-simulator-service"] == "support" and headers["x-simulator-region"] == "us-east-1"
            assert headers.get("cookie") and not {"x-simulator-token", "x-simulator-principal", "authorization"} & headers.keys()
            body = response.json()
            panel = dialog.locator(".response-section-content-body").filter(
                has=page.get_by_role("link", name="Download Response Body", exact=True))
            expect(panel).to_contain_text(next(iter(body)))
            if "caseId" in body:
                expect(panel).to_contain_text(body["caseId"])
            if "cases" in body and body["cases"]:
                expect(panel).to_contain_text(body["cases"][0]["subject"])
            if "__type" in body:
                expect(panel).to_contain_text(body["__type"])
            self.sends.append([operation, status])
            assert not self.outside and not self.errors
            return body
        finally:
            page.close()


def exercise(browser, team, peer, sdk, peer_sdk, stack):
    subject = "Scalar training case " + uuid4().hex
    initial, followup = "First practice message.", "Follow-up from the local reference."
    owner = ScalarSession(browser, team, team.token, stack)
    limited = ScalarSession(browser, team, provision_reader(team), stack)
    other = ScalarSession(browser, peer, peer.token, stack)
    case_id = owner.send("CreateCase", {"subject": subject, "communicationBody": initial}, 200)["caseId"]
    lookup = {"caseIdList": [case_id], "includeResolvedCases": True, "includeCommunications": False}
    case, = owner.send("DescribeCases", lookup, 200)["cases"]
    assert case["caseId"] == case_id and case["displayId"] != case_id and case["subject"] == subject
    assert case["status"] == "opened" and "recentCommunications" not in case
    assert sdk.describe_cases(**lookup)["cases"] == [case]

    def messages():
        return [msg["body"] for msg in sdk.describe_communications(caseId=case_id)["communications"]]

    assert messages() == [initial]
    assert owner.send("AddCommunicationToCase", {"caseId": case_id, "communicationBody": followup}, 200) == {"result": True}
    assert messages() == [followup, initial]
    assert limited.send("DescribeCases", lookup, 200)["cases"] == [case]
    assert limited.send("ResolveCase", {"caseId": case_id}, 403)["__type"] == "AccessDeniedException"
    assert sdk.describe_cases(**lookup)["cases"] == [case] and messages() == [followup, initial]
    assert other.send("DescribeCases", lookup, 400)["__type"] == "CaseIdNotFound"
    assert peer_sdk.describe_cases(includeResolvedCases=True)["cases"] == []
    assert owner.send("ResolveCase", {"caseId": case_id}, 200) == {
        "initialCaseStatus": "opened", "finalCaseStatus": "resolved",
    }
    assert sdk.describe_cases()["cases"] == []
    resolved, = sdk.describe_cases(**lookup)["cases"]
    assert resolved == {**case, "status": "resolved"} and messages() == [followup, initial]
    return {"ownerSends": owner.sends, "participantSends": limited.sends, "peerSends": other.sends,
            "stockSDKReadback": True}


def main():
    if any(k.startswith(("AWS_", "BOTO_")) for k in os.environ):
        raise RuntimeError("Run on a neutral host without ambient AWS SDK configuration.")
    assert version("boto3") == "1.42.0" and version("botocore") == "1.41.6"
    binary = Path(os.environ["SIMULATOR_BINARY"]).resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="support-scalar-") as tmp, ExitStack() as stack:
        team, peer = [LocalSimulator(Path(tmp) / name, binary) for name in ("team", "peer")]
        for instance in (team, peer):
            stack.callback(instance.close)
            instance.start()
        assert team.token != peer.token
        sdk, peer_sdk = [support_sdk(instance, stack) for instance in (team, peer)]
        tools = stack.enter_context(sync_playwright())
        browser = tools.chromium.launch(executable_path=os.getenv("CHROMIUM_EXECUTABLE"), chromium_sandbox=True)
        stack.callback(browser.close)
        receipt = exercise(browser, team, peer, sdk, peer_sdk, stack)
    assert not Path(tmp).exists()
    print(json.dumps({**receipt, "cleanup": True}))


if __name__ == "__main__":
    main()
