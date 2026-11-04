"""Actual Cloudscape Logs view; run after integration wires/builds the component."""
import json
import hashlib
import os
import re
import shutil
import time
from pathlib import Path

from check import REPORTS, assert_reflow, expect, running, sync_playwright
from cloudwatch_logs import client


def run():
    for name in ("cloudwatch-logs-events-desktop.png", "cloudwatch-logs-events-mobile.png", "cloudwatch-logs-events-mobile-scrolled.png", "cloudwatch-logs-a11y.json"):
        (REPORTS/name).unlink(missing_ok=True)
    # The canonical pinned audit asset is supplied by the host, never downloaded
    # by the running simulator or substituted with an empty checker.
    axe_path=Path(os.environ["AXE_SCRIPT"])
    provenance=json.loads((axe_path.parent/"provenance.json").read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest()==provenance["bundleSha256"]
    axe=axe_path.read_text()
    audits=[]
    with running(authenticated=True) as (endpoint, token), sync_playwright() as playwright:
        logs = client(endpoint, token)
        browser = playwright.chromium.launch(
            executable_path=os.environ.get("CHROMIUM_EXECUTABLE") or shutil.which("chromium"),
            args=["--no-sandbox"])
        try:
            context = browser.new_context(viewport={"width": 1440, "height": 1000})
            page = context.new_page()
            def audit(name):
                page.evaluate(axe)
                result=page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
                audits.append({"screen":name,"violations":result["violations"],"passes":len(result["passes"])})
                assert result["violations"]==[],[(v["id"],len(v["nodes"])) for v in result["violations"]]
            errors, outside = [], []
            page.on("pageerror", lambda error: errors.append(str(error)))
            def local_only(route):
                if route.request.url.startswith(endpoint + "/"):
                    route.continue_()
                else:
                    outside.append(route.request.url)
                    route.abort()
            page.route("**/*", local_only)
            page.goto(endpoint + "/_simulator/console/")
            page.get_by_label("Access token", exact=True).fill(token)
            page.get_by_role("button", name="Open sandbox", exact=True).click()
            page.get_by_role("button", name="Services", exact=True).click()
            page.get_by_role("menuitem", name="CloudWatch Logs", exact=True).click()
            expect(page.get_by_role("heading", name=re.compile(r"^Log groups"))).to_be_visible()
            audit("log-groups")
            page.get_by_role("button", name="Create log group", exact=True).click()
            page.get_by_label("Log group name", exact=True).fill("/aws/console-example")
            audit("create-log-group")
            page.get_by_label("Log group name", exact=True).press("Enter")
            expect(page.get_by_role("link", name="/aws/console-example", exact=True)).to_be_visible()
            assert logs.describe_log_groups()["logGroups"][0]["logGroupName"] == "/aws/console-example"
            page.get_by_role("link", name="/aws/console-example", exact=True).click()
            page.get_by_role("button", name="Create log stream", exact=True).click()
            page.get_by_label("Log stream name", exact=True).fill("application")
            page.get_by_label("Log stream name", exact=True).press("Enter")
            expect(page.get_by_role("link", name="application", exact=True)).to_be_visible()
            audit("log-streams")
            assert logs.describe_log_streams(logGroupName="/aws/console-example")["logStreams"][0]["logStreamName"] == "application"
            now = int(time.time() * 1000)
            logs.put_log_events(logGroupName="/aws/console-example", logStreamName="application",
                                logEvents=[{"timestamp": now, "message": "<script>dummy markup remains text</script>"}])
            # Hold one genuine native response; navigate within the same mounted
            # component, then release it. The old result must not replace the
            # newer group view or leave it loading. No fetch/data is fabricated.
            pending=[]
            def hold_event_read(route):
                if route.request.headers.get("x-amz-target")=="Logs_20140328.GetLogEvents" and not pending:
                    pending.append(route)
                else:
                    route.fallback()
            page.route(endpoint+"/",hold_event_read)
            page.get_by_role("link", name="application", exact=True).click()
            deadline=time.monotonic()+5
            while not pending:
                assert time.monotonic()<deadline, "GetLogEvents was not routed"
                page.wait_for_timeout(10)
            page.get_by_role("button", name="Log groups", exact=True).click()
            expect(page.get_by_role("heading", name=re.compile(r"^Log groups"))).to_be_visible()
            with page.expect_response(lambda response:response.request.headers.get("x-amz-target")=="Logs_20140328.GetLogEvents"):
                pending[0].continue_()
            page.unroute(endpoint+"/",hold_event_read)
            expect(page.get_by_role("heading", name=re.compile(r"^Log groups"))).to_be_visible()
            expect(page.get_by_role("button", name="Refresh CloudWatch Logs", exact=True)).to_be_enabled()
            expect(page.get_by_text("<script>dummy markup remains text</script>", exact=True)).to_have_count(0)
            page.get_by_role("link", name="/aws/console-example", exact=True).click()
            page.get_by_role("link", name="application", exact=True).click()
            expect(page.get_by_text("<script>dummy markup remains text</script>", exact=True)).to_be_visible()
            page.get_by_role("button", name="Add log events (simulator)", exact=True).click()
            page.get_by_label("Log events JSON", exact=True).fill(json.dumps([
                {"timestamp": now + 1, "message": "Created from the simulator ingestion helper"}]))
            page.get_by_role("button", name="Send events", exact=True).click()
            expect(page.get_by_text("Created from the simulator ingestion helper", exact=True)).to_be_visible()
            audit("log-events-desktop")
            assert len(logs.get_log_events(logGroupName="/aws/console-example", logStreamName="application")["events"]) == 2
            assert_reflow(page)
            page.screenshot(path=str(REPORTS / "cloudwatch-logs-events-desktop.png"), full_page=True)
            page.set_viewport_size({"width": 390, "height": 844})
            assert_reflow(page)
            audit("log-events-mobile")
            page.screenshot(path=str(REPORTS / "cloudwatch-logs-events-mobile.png"), full_page=True)
            # A data table may scroll within its own container. Verify that the
            # message remains readable rather than shrinking to a few letters,
            # and that actual horizontal navigation exposes the full column.
            message_cell=page.get_by_role("cell",name="<script>dummy markup remains text</script>",exact=True)
            assert message_cell.bounding_box()["width"]>=300
            message_cell.scroll_into_view_if_needed()
            assert_reflow(page)
            box=message_cell.bounding_box()
            assert box["x"]>=0 and box["x"]+box["width"]<=390, box
            page.screenshot(path=str(REPORTS / "cloudwatch-logs-events-mobile-scrolled.png"),full_page=True)
            page.set_viewport_size({"width": 1440, "height": 1000})
            page.get_by_role("button", name="Log streams", exact=True).click()
            page.get_by_role("button", name="Delete log group", exact=True).click()
            page.get_by_label("Enter the log group name to confirm", exact=True).fill("/aws/console-example")
            page.get_by_role("button", name="Delete", exact=True).click()
            expect(page.get_by_role("heading", name=re.compile(r"^Log groups"))).to_be_visible()
            assert logs.describe_log_groups()["logGroups"] == []
            assert errors == [], errors
            assert outside == [], outside
            (REPORTS/"cloudwatch-logs-a11y.json").write_text(json.dumps({"tool":provenance,"screens":audits},indent=2)+"\n")
        finally:
            browser.close()
    print("CloudWatch Logs actual console/SDK shared-state/reflow/local-only contract PASS")


if __name__ == "__main__":
    run()
