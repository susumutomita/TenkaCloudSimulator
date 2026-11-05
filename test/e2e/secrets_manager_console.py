"""Real native Secrets console contract against the canonical embedded UI.

Dummy values only, no value screenshots/traces, no fabricated API responses.
Source preparation is not evidence of a successful browser run.
"""
import hashlib
import json
import os
import re
import shutil
import time
from pathlib import Path

from check import REPORTS, assert_reflow, expect, running, sync_playwright
from secrets_manager import client, control


def safe_audit_summary(name, viewport, result):
    """Bound failure diagnostics to selectors and numeric/color check data."""
    violations = []
    for violation in result["violations"][:16]:
        nodes = []
        for node in violation["nodes"][:8]:
            checks = []
            for kind in ("any", "all", "none"):
                for check in node.get(kind, [])[:4]:
                    data, colors = check.get("data") or {}, {}
                    if isinstance(data, dict):
                        for key in ("fgColor", "bgColor", "contrastRatio",
                                    "expectedContrastRatio", "fontSize", "fontWeight"):
                            value = data.get(key)
                            if type(value) in (int, float):
                                colors[key] = value
                            elif isinstance(value, str) and len(value) <= 64:
                                if key in ("fgColor", "bgColor") and re.fullmatch(r"#[0-9a-fA-F]{3,8}", value):
                                    colors[key] = value
                                elif key not in ("fgColor", "bgColor") and re.fullmatch(r"[0-9 .:()pxpt]+", value):
                                    colors[key] = value
                    checks.append({"id": check["id"][:80], "data": colors})
            # Axe selectors can contain attribute values. Apply the same fixture
            # redaction as the exception sanitizer before persisting selectors.
            nodes.append({"target": [re.sub(r"DUMMY-secret-[A-Za-z0-9_-]+", "[redacted dummy value]", target)[:512]
                                     for target in node.get("target", [])[:4]
                                     if isinstance(target, str)], "checks": checks})
        violations.append({"id": violation["id"][:80], "nodeCount": len(violation["nodes"]),
                           "nodes": nodes})
    return {"screen": name, "viewport": viewport, "violationCount": len(result["violations"]),
            "violations": violations, "passes": len(result["passes"])}


def run():
    output_names = ["secrets-metadata-desktop.png", "secrets-metadata-mobile.png",
                    "secrets-a11y.json", "secrets-a11y-summary.json"]
    for name in output_names:
        (REPORTS/name).unlink(missing_ok=True)
    axe_path = Path(os.environ["AXE_SCRIPT"])
    provenance = json.loads((axe_path.parent/"provenance.json").read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance["bundleSha256"]
    axe = axe_path.read_text()
    audits, summaries = [], []
    with running(authenticated=True) as (endpoint, token), sync_playwright() as playwright:
        sdk = client(endpoint, token)
        browser = playwright.chromium.launch(
            executable_path=os.environ.get("CHROMIUM_EXECUTABLE") or shutil.which("chromium"),
            args=["--no-sandbox"])
        try:
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            errors, outside, reads = [], [], []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.on("request", lambda request: reads.append(request.url)
                    if request.headers.get("x-amz-target") == "secretsmanager.GetSecretValue" else None)

            def local_only(route):
                if route.request.url.startswith(endpoint+"/"):
                    route.continue_()
                else:
                    outside.append(route.request.url)
                    route.abort()

            def audit(name):
                page.evaluate(axe)
                result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
                audits.append({"screen": name, "violations": result["violations"],
                               "passes": len(result["passes"])})
                (REPORTS/"secrets-a11y.json").write_text(json.dumps({"tool": provenance, "screens": audits}, indent=2)+"\n")
                summary = safe_audit_summary(name, page.viewport_size, result)
                summaries.append(summary)
                (REPORTS/"secrets-a11y-summary.json").write_text(json.dumps({"tool": provenance, "screens": summaries}, indent=2)+"\n")
                assert result["violations"] == [], json.dumps(summary, ensure_ascii=True)

            page.route("**/*", local_only)
            page.goto(endpoint+"/_simulator/console/")
            page.get_by_label("Access token", exact=True).fill(token)
            page.get_by_role("button", name="Open sandbox", exact=True).click()
            page.get_by_role("button", name="Services", exact=True).click()
            page.get_by_role("menuitem", name="Secrets Manager", exact=True).click()
            expect(page.get_by_role("heading", name=re.compile(r"^Secrets"), level=1)).to_be_visible()
            audit("secrets-list")
            page.get_by_role("button", name="Store a new secret", exact=True).click()
            page.get_by_role("tab", name="Plaintext", exact=True).click()
            page.get_by_label("Plaintext secret value", exact=True).fill("DUMMY-secret-console-A")
            page.get_by_role("button", name="Next", exact=True).press("Enter")
            page.get_by_label("Secret name", exact=True).fill("training/console-example")
            page.get_by_label("Description", exact=True).fill("Dummy console metadata")
            page.get_by_role("button", name="Next", exact=True).click()
            expect(page.get_by_role("checkbox", name="Automatic rotation", exact=True)).to_be_disabled()
            page.get_by_role("button", name="Next", exact=True).click()
            # Review must not retain the entry in an input, text node or field.
            assert "DUMMY-secret-console-A" not in page.locator("body").inner_text()
            assert page.get_by_label("Plaintext secret value", exact=True).count() == 0
            audit("create-review-hidden")
            page.get_by_role("button", name="Store", exact=True).click()
            expect(page.get_by_role("heading", name="training/console-example", exact=True)).to_be_visible()
            assert reads == [], "Metadata navigation implicitly requested a secret value"
            saved = sdk.get_secret_value(SecretId="training/console-example")
            assert saved["SecretString"] == "DUMMY-secret-console-A", "Wizard did not persist the entered value"
            arn, first_version = saved["ARN"], saved["VersionId"]

            # Delay an actual Get request, Hide while in flight, release it.
            # No synthetic value/status/response body is supplied to the view.
            held = []

            def hold_read(route):
                if route.request.headers.get("x-amz-target") == "secretsmanager.GetSecretValue" and not held:
                    held.append(route)
                else:
                    route.fallback()

            page.route(endpoint+"/", hold_read)
            page.get_by_role("button", name="Retrieve secret value", exact=True).click()
            deadline = time.monotonic()+5
            while not held:
                assert time.monotonic() < deadline, "Explicit Get was not dispatched"
                page.wait_for_timeout(10)
            page.get_by_role("button", name="Hide secret value", exact=True).click()
            with page.expect_response(lambda response: response.request.headers.get("x-amz-target") == "secretsmanager.GetSecretValue") as stale_response:
                held[0].continue_()
            stale_response.value.body()
            page.evaluate("async()=>await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))")
            page.unroute(endpoint+"/", hold_read)
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            expect(page.get_by_role("button", name="Retrieve secret value", exact=True)).to_be_enabled()

            held.clear()
            page.route(endpoint+"/", hold_read)
            page.get_by_role("button", name="Retrieve secret value", exact=True).click()
            deadline = time.monotonic()+5
            while not held:
                assert time.monotonic() < deadline, "Navigation Get was not dispatched"
                page.wait_for_timeout(10)
            page.get_by_role("button", name="Secrets", exact=True).click()
            expect(page.get_by_role("heading", name=re.compile(r"^Secrets"), level=1)).to_be_visible()
            with page.expect_response(lambda response: response.request.headers.get("x-amz-target") == "secretsmanager.GetSecretValue") as stale_navigation:
                held[0].continue_()
            stale_navigation.value.body()
            page.evaluate("async()=>await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))")
            page.unroute(endpoint+"/", hold_read)
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            expect(page.get_by_role("heading", name=re.compile(r"^Secrets"), level=1)).to_be_visible()
            page.get_by_role("link", name="training/console-example", exact=True).click()
            expect(page.get_by_role("heading", name="Secret details", exact=True)).to_be_visible()

            page.get_by_role("button", name="Retrieve secret value", exact=True).click()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_value("DUMMY-secret-console-A")
            page.get_by_role("button", name="Edit", exact=True).click()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            page.get_by_role("tab", name="Plaintext", exact=True).click()
            # Editing a new version must never auto-populate an old secret.
            expect(page.get_by_label("Plaintext secret value", exact=True)).to_have_value("")
            page.get_by_label("Plaintext secret value", exact=True).fill("DUMMY-secret-console-B")
            page.get_by_role("button", name="Save", exact=True).click()
            expect(page.get_by_role("heading", name="Secret details", exact=True)).to_be_visible()
            current = sdk.get_secret_value(SecretId=arn)
            assert current["SecretString"] == "DUMMY-secret-console-B" and current["VersionId"] != first_version
            with page.expect_response(lambda response:
                    response.request.headers.get("x-amz-target") == "secretsmanager.DescribeSecret"
                    and response.request.post_data_json == {"SecretId": arn}) as rollback_metadata:
                page.get_by_role("button", name="Roll back to previous (simulator)", exact=True).click()
            assert rollback_metadata.value.status == 200, "Rollback metadata refresh failed"
            assert rollback_metadata.value.json()["VersionIdsToStages"] == {
                first_version: ["AWSCURRENT"], current["VersionId"]: ["AWSPREVIOUS"]}
            expect(page.get_by_text("Previous version is now current.", exact=False)).to_be_visible()
            assert sdk.get_secret_value(SecretId=arn)["VersionId"] == first_version
            # The success notice precedes the follow-up read. Audit the returned
            # detail state, not the intervening cached/loading list.
            expect(page.get_by_role("heading", name="Secret details", exact=True)).to_be_visible()
            expect(page.get_by_role("button", name="Refresh secret metadata", exact=True)).to_be_enabled()
            expect(page.get_by_role("button", name="Roll back to previous (simulator)", exact=True)).to_be_enabled()
            expect(page.get_by_role("row").filter(has_text=first_version).get_by_role(
                "cell", name="AWSCURRENT", exact=True)).to_be_visible()
            expect(page.get_by_role("row").filter(has_text=current["VersionId"]).get_by_role(
                "cell", name="AWSPREVIOUS", exact=True)).to_be_visible()
            assert "DUMMY-secret-" not in page.locator("body").inner_text()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            audit("metadata-desktop")
            assert_reflow(page)
            page.screenshot(path=str(REPORTS/"secrets-metadata-desktop.png"), full_page=False, animations="disabled")
            page.set_viewport_size({"width": 390, "height": 844})
            assert_reflow(page)
            audit("metadata-mobile")
            page.screenshot(path=str(REPORTS/"secrets-metadata-mobile.png"), full_page=False, animations="disabled")
            page.get_by_role("button", name="Secrets", exact=True).click()
            page.get_by_role("button", name="Store a new secret", exact=True).click()
            expect(page.get_by_label("Key 1", exact=True)).to_be_visible()
            assert_reflow(page)
            audit("create-pairs-mobile")
            page.get_by_role("button", name="Cancel", exact=True).click()
            page.get_by_role("link", name="training/console-example", exact=True).click()
            page.set_viewport_size({"width": 1440, "height": 1000})

            # A separately authenticated participant can list/describe metadata
            # but cannot reveal a value. UI selection is not authorization.
            control(endpoint, token, "/_simulator/v1/iam/principals/secret-console-reader/policy", {
                "Version": "2012-10-17", "Statement": [
                    {"Effect": "Allow", "Action": "secretsmanager:ListSecrets", "Resource": "*"},
                    {"Effect": "Allow", "Action": "secretsmanager:DescribeSecret", "Resource": arn}]})
            participant = control(endpoint, token, "/_simulator/v1/access", {"Principal": "secret-console-reader"})["AccessToken"]
            limited_context = browser.new_context(viewport={"width": 1440, "height": 1000})
            try:
                limited = limited_context.new_page()
                limited.route("**/*", local_only)
                limited.on("pageerror", lambda error: errors.append(str(error)))
                limited.goto(endpoint+"/_simulator/console/#/secretsmanager")
                limited.get_by_label("Access token", exact=True).fill(participant)
                limited.get_by_role("button", name="Open sandbox", exact=True).click()
                # Login returns to the default service; choose the actual menu.
                limited.get_by_role("button", name="Services", exact=True).click()
                limited.get_by_role("menuitem", name="Secrets Manager", exact=True).click()
                limited.get_by_role("link", name="training/console-example", exact=True).click()
                with limited.expect_response(lambda response: response.request.headers.get("x-amz-target") == "secretsmanager.GetSecretValue") as denied:
                    limited.get_by_role("button", name="Retrieve secret value", exact=True).click()
                assert denied.value.status == 403, "Participant reveal bypassed native IAM"
                expect(limited.get_by_text(re.compile(r"^403: IAM evaluation:"))).to_be_visible()
                expect(limited.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
                assert "DUMMY-secret-" not in limited.locator("body").inner_text()
            finally:
                limited_context.close()

            # A region change remounts the view: a displayed value must not
            # survive in the new region, which has no secret with this name.
            page.get_by_role("button", name="Retrieve secret value", exact=True).click()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_value("DUMMY-secret-console-A")
            page.get_by_role("button", name="us-east-1", exact=True).click()
            page.get_by_role("menuitem", name="us-west-2", exact=True).click()
            expect(page.get_by_role("heading", name=re.compile(r"^Secrets"), level=1)).to_be_visible()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            expect(page.get_by_role("link", name="training/console-example", exact=True)).to_have_count(0)
            assert "DUMMY-secret-" not in page.locator("body").inner_text()
            assert client(endpoint, token, "us-west-2").list_secrets()["SecretList"] == []
            page.get_by_role("button", name="us-west-2", exact=True).click()
            page.get_by_role("menuitem", name="us-east-1", exact=True).click()
            page.get_by_role("link", name="training/console-example", exact=True).click()
            page.get_by_role("button", name="Retrieve secret value", exact=True).click()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_value("DUMMY-secret-console-A")

            def choose_identity(principal):
                page.get_by_role("button", name="Account menu", exact=True).click()
                page.get_by_role("menuitem", name="Choose identity", exact=True).click()
                dialog = page.get_by_role("dialog")
                dialog.get_by_label("Principal name", exact=True).fill(principal)
                dialog.get_by_role("button", name="Choose identity", exact=True).click()
            choose_identity("secret-console-reader")
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            assert "DUMMY-secret-" not in page.locator("body").inner_text()
            page.get_by_role("link", name="training/console-example", exact=True).click()
            with page.expect_response(lambda response: response.request.headers.get("x-amz-target") == "secretsmanager.GetSecretValue") as selected_denied:
                page.get_by_role("button", name="Retrieve secret value", exact=True).click()
            assert selected_denied.value.status == 403
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            choose_identity("simulator-admin")
            page.get_by_role("link", name="training/console-example", exact=True).click()
            page.get_by_role("button", name="Retrieve secret value", exact=True).click()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_value("DUMMY-secret-console-A")

            # Keyboard Sign out revokes only the actual browser cookie, not the
            # host token used by the independent native SDK. Never report tokens.
            cookies = [entry for entry in page.context.cookies(endpoint) if entry["name"].startswith("tcsim_")]
            assert len(cookies) == 1
            retired_cookie = cookies[0]["name"]+"="+cookies[0]["value"]
            page.get_by_role("button", name="Account menu", exact=True).focus()
            page.keyboard.press("Enter")
            page.keyboard.press("ArrowDown")
            expect(page.get_by_role("menuitem", name="Sign out", exact=True)).to_be_focused()
            page.keyboard.press("Enter")
            expect(page.get_by_label("Access token", exact=True)).to_be_visible()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            assert "DUMMY-secret-" not in page.locator("body").inner_text()
            replay = page.context.request.get(endpoint+"/_simulator/v1/capabilities", headers={"Cookie": retired_cookie}, max_redirects=0)
            assert replay.status == 401, "Retired browser cookie retained authority"
            assert sdk.get_secret_value(SecretId=arn)["SecretString"] == "DUMMY-secret-console-A", "Sign out revoked the independent SDK credential"
            page.get_by_label("Access token", exact=True).fill(token)
            page.get_by_role("button", name="Open sandbox", exact=True).click()
            page.get_by_role("button", name="Services", exact=True).click()
            page.get_by_role("menuitem", name="Secrets Manager", exact=True).click()
            page.get_by_role("link", name="training/console-example", exact=True).click()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)

            page.get_by_role("button", name="Delete secret", exact=True).click()
            page.get_by_role("checkbox", name="Force delete without recovery", exact=True).check()
            page.get_by_label("Enter secret name to confirm", exact=True).fill("training/console-example")
            page.get_by_label("Enter secret name to confirm", exact=True).press("Enter")
            expect(page.get_by_role("heading", name=re.compile(r"^Secrets"), level=1)).to_be_visible()
            assert sdk.list_secrets()["SecretList"] == [], "Enter did not perform native deletion"
            assert errors == [], "Browser raised a page error"
            assert outside == [], "Browser attempted an external request"
            (REPORTS/"secrets-a11y.json").write_text(json.dumps({"tool": provenance, "screens": audits, "contextChecks": ["region-clear", "principal-clear-IAM403", "keyboard-signout", "revoked-cookie401-SDK-still-valid", "hide-and-navigation-stale-response"]}, indent=2)+"\n")
        finally:
            browser.close()
    print("Secrets Manager actual native console/reveal/cleanup/reflow/local-only contract PASS")


if __name__ == "__main__":
    try:
        run()
    except Exception as error:
        # Playwright may include current form fields in its automatic assertion
        # snapshot. Preserve the failure without persisting practice value bytes.
        message = re.sub(r"DUMMY-secret-[A-Za-z0-9_-]+", "[redacted dummy value]", str(error))
        raise RuntimeError(message) from None
