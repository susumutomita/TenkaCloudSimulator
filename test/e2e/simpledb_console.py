"""Focused signed-in SimpleDB Console flow against one standalone local CLI."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
from urllib.parse import parse_qs, urlencode, urlsplit
import urllib.request
import xml.etree.ElementTree as ET

from playwright.sync_api import expect, sync_playwright

SDK_ORACLE = Path(__file__).resolve().parents[1] / "factory/testdata/simpledb/sdk_wire.py"
spec = importlib.util.spec_from_file_location("simpledb_wire", SDK_ORACLE)
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)
NS = {"sdb": "http://sdb.amazonaws.com/doc/2009-04-15/"}
DOMAIN, ITEM = "console-versions", "order + & 1"


def run():
    binary = Path(os.environ.get("SIMULATOR_BINARY", "dist/tenkacloud-simulator")).resolve(strict=True)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    report = {}
    with tempfile.TemporaryDirectory(prefix="simpledb-console-state-") as state, sync_playwright() as p:
        with harness.running(binary, Path(state) / "example.db", report) as (endpoint, token):
            origin = urlsplit(endpoint)
            headers = {"X-Simulator-Token": token}

            def native(operation, **fields):
                body = urlencode({"Version": "2009-04-15", "Action": operation, **fields}).encode("utf-8")
                request = urllib.request.Request(endpoint + "/", body, {
                    **headers, "X-Simulator-Service": "sdb", "X-Simulator-Region": "us-east-1",
                    "Content-Type": "application/x-www-form-urlencoded"})
                with opener.open(request, timeout=3) as response:
                    return ET.fromstring(response.read())

            def control(method, path, fields):
                request = urllib.request.Request(endpoint + path, json.dumps(fields).encode(), {
                    **headers, "Content-Type": "application/json"}, method=method)
                with opener.open(request, timeout=3) as response:
                    return response.read()

            def values():
                response = native("GetAttributes", DomainName=DOMAIN, ItemName=ITEM, ConsistentRead="true")
                return [(attr.findtext("sdb:Name", namespaces=NS), attr.findtext("sdb:Value", namespaces=NS))
                        for attr in response.findall("./sdb:GetAttributesResult/sdb:Attribute", NS)]

            executable = os.environ.get("CHROMIUM_EXECUTABLE") or shutil.which("chromium")
            with p.chromium.launch(executable_path=executable) as browser:
                outside, errors, actions = [], [], []

                def page_for(credential, width=1200):
                    context = browser.new_context(locale="en-US", viewport={"width": width, "height": 900},
                                                  service_workers="block")

                    def guard(route):
                        url = urlsplit(route.request.url)
                        if (url.scheme, url.netloc) == (origin.scheme, origin.netloc):
                            route.continue_()
                        else:
                            outside.append(url.netloc)
                            route.abort()

                    context.route("**/*", guard)
                    if credential:
                        assert context.request.post(endpoint + "/_simulator/login", form={"token": credential},
                                                    max_redirects=0).status == 303
                    page = context.new_page()

                    def capture(request):
                        if request.headers.get("x-simulator-service") == "sdb":
                            assert request.url == endpoint + "/" and not request.headers.get("x-simulator-token")
                            assert request.headers.get("x-simulator-region") == "us-east-1"
                            actions.append(parse_qs(request.post_data, keep_blank_values=True))

                    page.on("request", capture)
                    page.on("pageerror", lambda error: errors.append(str(error)))
                    return context, page

                guest, unsigned = page_for(None)
                unsigned.goto(endpoint + "/_simulator/console/#/simpledb")
                unsigned.wait_for_url("**/_simulator/login")
                assert not actions
                guest.close()

                context, page = page_for(token)
                page.goto(endpoint + "/_simulator/console/#/home")
                page.get_by_role("button", name="Services", exact=True).click()
                page.get_by_role("menuitem", name="SimpleDB", exact=True).click()
                expect(page.get_by_role("heading", name="Amazon SimpleDB", exact=True)).to_be_visible()
                expect(page.get_by_text("No domains on this page.", exact=True)).to_be_visible()
                page.get_by_label("Domain to open or create").fill(DOMAIN)
                page.get_by_role("button", name="Create domain", exact=True).click()
                expect(page.get_by_role("heading", name="Look up an item", exact=True)).to_be_visible()
                expect(page.get_by_label("Item name", exact=True)).to_be_focused()

                def read(screen):
                    screen.get_by_label("Item name", exact=True).fill(ITEM)
                    screen.get_by_role("button", name="Read attributes", exact=True).click()
                    expect(screen.get_by_role("table", name="Saved attributes", exact=True)).to_be_visible()
                    expect(screen.get_by_label("Attribute to change", exact=True)).to_be_focused()

                read(page)
                expect(page.get_by_text("This item has no attributes.", exact=True)).to_be_visible()
                page.get_by_label("Attribute to change", exact=True).fill("version")
                page.get_by_role("button", name="Save attribute", exact=True).click()
                expect(page.get_by_text("Attribute saved.", exact=True)).to_be_visible()
                expect(page.get_by_role("region", name="SimpleDB action status", exact=True)).to_be_focused()
                expect(page.get_by_text("Empty string", exact=True)).to_be_visible()
                assert values() == [("version", "")]

                page.get_by_role("radio", name="Attribute equals expected value", exact=True).check()
                page.get_by_label("New value", exact=True).fill("new + & 値")
                page.get_by_role("button", name="Save attribute", exact=True).click()
                expect(page.get_by_text("new + & 値", exact=True)).to_be_visible()
                assert values() == [("version", "new + & 値")]
                page.get_by_label("New value", exact=True).fill("must not persist")
                page.get_by_role("button", name="Save attribute", exact=True).click()
                expect(page.get_by_text("ConditionalCheckFailed", exact=True)).to_be_visible()
                expect(page.get_by_role("region", name="SimpleDB action status", exact=True)).to_be_focused()
                expect(page.get_by_label("Attribute to change", exact=True)).to_be_disabled()
                assert values() == [("version", "new + & 値")]
                page.get_by_role("button", name="Read attributes", exact=True).click()
                expect(page.get_by_text("new + & 値", exact=True)).to_be_visible()
                # No automatic scan or attribute read. The names query uses the
                # same store and principal, but does not confer GetAttributes.
                page.get_by_label("Filter by attribute (optional)", exact=True).fill("version")
                page.get_by_label("Exact attribute value", exact=True).fill("new + & 値")
                page.get_by_role("button", name="Run query", exact=True).click()
                matches = page.get_by_role("table", name="Matching item names", exact=True)
                hit = matches.get_by_role("button", name=ITEM, exact=True)
                expect(hit).to_be_visible()
                count = len(actions)
                hit.click()
                expect(page.get_by_label("Item name", exact=True)).to_have_value(ITEM)
                expect(page.get_by_label("Item name", exact=True)).to_be_focused()
                expect(page.get_by_role("table", name="Saved attributes", exact=True)).to_have_count(0)
                assert len(actions) == count
                page.get_by_role("button", name="Read attributes", exact=True).click()
                expect(page.get_by_text("new + & 値", exact=True)).to_be_visible()
                page.get_by_label("Exact attribute value", exact=True).fill("not-a-match")
                expect(matches).to_have_count(0)
                expect(page.get_by_role("button", name="Load next result page")).to_be_disabled()
                # Deliberately seed one extra page through the native endpoint.
                for index in range(26):
                    native("PutAttributes", DomainName=DOMAIN, ItemName=f"paging-{index:02}",
                           **{"Attribute.1.Name": "batch", "Attribute.1.Value": "two-pages"})
                page.get_by_label("Filter by attribute (optional)", exact=True).fill("batch")
                page.get_by_label("Exact attribute value", exact=True).fill("two-pages")
                page.get_by_role("button", name="Run query", exact=True).click()
                expect(matches.get_by_role("button", name="paging-24", exact=True)).to_be_visible()
                page.get_by_role("button", name="Load next result page", exact=True).click()
                expect(matches.get_by_role("button", name="paging-25", exact=True)).to_be_visible()
                expect(matches.get_by_role("button", name="paging-24", exact=True)).to_have_count(0)
                expect(page.get_by_role("button", name="Load next result page")).to_be_disabled()
                select_calls = [a for a in actions if a["Action"] == ["Select"]]
                assert select_calls[-2]["SelectExpression"] == select_calls[-1]["SelectExpression"]
                assert "NextToken" not in select_calls[-2] and select_calls[-1].get("NextToken")
                page.get_by_role("button", name="Back to domains", exact=True).click()
                expect(page.get_by_role("button", name=DOMAIN, exact=True)).to_be_visible()
                expect(page.get_by_label("Domain to open or create", exact=True)).to_be_focused()
                context.close()

                principal = "simpledb-console-reader"
                control("PUT", "/_simulator/v1/iam/principals/" + principal + "/policy", {
                    "Version": "2012-10-17", "Statement": [
                        {"Effect": "Allow", "Action": "sdb:GetAttributes",
                         "Resource": "arn:aws:sdb:us-east-1:000000000000:domain/" + DOMAIN},
                    ]})
                reader_token = json.loads(control("POST", "/_simulator/v1/access", {"Principal": principal}))["AccessToken"]
                reader, limited = page_for(reader_token, width=390)
                limited.goto(endpoint + "/_simulator/console/#/simpledb")
                expect(limited.get_by_text("AccessDenied", exact=False)).to_be_visible()
                limited.get_by_label("Domain to open or create", exact=True).fill(DOMAIN)
                limited.get_by_role("button", name="Open domain", exact=True).click()
                limited.get_by_role("button", name="Run query", exact=True).click()
                expect(limited.get_by_text("Your identity cannot query this domain.", exact=True)).to_be_visible()
                expect(limited.get_by_role("table", name="Matching item names", exact=True)).to_have_count(0)
                read(limited)
                expect(limited.get_by_text("new + & 値", exact=True)).to_be_visible()
                limited.get_by_label("Attribute to change", exact=True).fill("denied")
                limited.get_by_label("New value", exact=True).fill("must not persist")
                limited.get_by_role("button", name="Save attribute", exact=True).click()
                expect(limited.get_by_text("AccessDenied", exact=True)).to_be_visible()
                expect(limited.get_by_role("region", name="SimpleDB action status", exact=True)).to_be_focused()
                assert limited.evaluate("document.documentElement.scrollWidth <= innerWidth")
                reader.close()
                assert values() == [("version", "new + & 値")]
                assert not outside and not errors, (outside, errors)

                writes = [a for a in actions if a["Action"] == ["PutAttributes"]]
                assert len(writes) == 4, writes
                assert writes[0]["Expected.Exists"] == ["false"] and "Expected.Value" not in writes[0]
                assert all(a["Expected.Exists"] == ["true"] and a["Expected.Value"] == [""] for a in writes[1:3])
                assert all(a["Attribute.1.Replace"] == ["true"] and a["ItemName"] == [ITEM] for a in writes)
            native("DeleteDomain", DomainName=DOMAIN)
            assert native("ListDomains").findall("./sdb:ListDomainsResult/sdb:DomainName", NS) == []
    assert not report.get("cleanup_errors")
    print(json.dumps({"simpledbConsole": True, "conditionalWrites": 4, "roleDenied": True,
                      "liveReadback": True, "mobileReflow": True, "closedAndCleaned": True, "realAWS": False}))


if __name__ == "__main__":
    run()
