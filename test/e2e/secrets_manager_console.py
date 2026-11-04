"""Real native Secrets console contract; needs integration owner's nav/bundle.

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


def run():
    output_names = ["secrets-metadata-desktop.png", "secrets-metadata-mobile.png",
                    "secrets-a11y.json"]
    for name in output_names:
        (REPORTS/name).unlink(missing_ok=True)
    axe_path = Path(os.environ["AXE_SCRIPT"])
    provenance = json.loads((axe_path.parent/"provenance.json").read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance["bundleSha256"]
    axe = axe_path.read_text()
    audits = []
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
                assert result["violations"] == [], [(v["id"], len(v["nodes"])) for v in result["violations"]]

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
            page.get_by_role("button", name="Roll back to previous (simulator)", exact=True).click()
            expect(page.get_by_text("Previous version is now current.", exact=False)).to_be_visible()
            assert sdk.get_secret_value(SecretId=arn)["VersionId"] == first_version
            assert "DUMMY-secret-" not in page.locator("body").inner_text()
            expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_count(0)
            audit("metadata-desktop")
            assert_reflow(page)
            page.screenshot(path=str(REPORTS/"secrets-metadata-desktop.png"), full_page=True)
            page.set_viewport_size({"width": 390, "height": 844})
            assert_reflow(page)
            audit("metadata-mobile")
            page.screenshot(path=str(REPORTS/"secrets-metadata-mobile.png"), full_page=True)
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

            page.get_by_role("button", name="Delete secret", exact=True).click()
            page.get_by_role("checkbox", name="Force delete without recovery", exact=True).check()
            page.get_by_label("Enter secret name to confirm", exact=True).fill("training/console-example")
            page.get_by_label("Enter secret name to confirm", exact=True).press("Enter")
            expect(page.get_by_role("heading", name=re.compile(r"^Secrets"), level=1)).to_be_visible()
            assert sdk.list_secrets()["SecretList"] == [], "Enter did not perform native deletion"
            assert errors == [], "Browser raised a page error"
            assert outside == [], "Browser attempted an external request"
            (REPORTS/"secrets-a11y.json").write_text(json.dumps({"tool": provenance, "screens": audits}, indent=2)+"\n")
        finally:
            browser.close()
    print("Secrets Manager actual native console/reveal/cleanup/reflow/local-only contract PASS")


if __name__ == "__main__":
    run()
