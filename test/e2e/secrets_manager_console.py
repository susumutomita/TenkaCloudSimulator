"""Real native Secrets console contract against the canonical embedded UI.

Dummy values only, no value screenshots/traces, no fabricated API responses.
SECRETS_TEST_BUNDLE explicitly selects private focused asset-overlay evidence;
leave it unset for the canonical embedded-asset merge gate.
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


def authenticated_console_ready(page, endpoint):
    # Login submits asynchronously, then location.assign replaces the document.
    # A raw evaluate must run only after authenticated console controls exist.
    page.wait_for_url(endpoint + "/_simulator/console/")
    expect(page.get_by_role("button", name="Services", exact=True)).to_be_visible()


def login_readiness_regression(endpoint, token):
    """Hold one real native303 in an independent async diagnostic context."""
    import asyncio
    from playwright.async_api import async_playwright
    outcomes = []

    async def exercise():
        async with async_playwright() as playwright:
            diagnostic = await playwright.chromium.launch(executable_path=os.environ.get("CHROMIUM_EXECUTABLE") or shutil.which("chromium"), args=["--no-sandbox"])
            context = await diagnostic.new_context()
            page = await context.new_page()
            page.set_default_timeout(5000)
            held, fixture_errors, ready = [], [], asyncio.Event()
            pending = None
            try:
                await page.goto(endpoint + "/_simulator/console/")

                async def hold_login(route):
                    if route.request.method != "POST":
                        await route.continue_()
                        return
                    try:
                        response = await route.fetch(max_redirects=0)
                        assert response.status == 303
                        held.append((route, response))
                    except Exception as error:
                        # Network exception strings may contain cookies: types only.
                        fixture_errors.append(type(error).__name__)
                        try:
                            await route.abort()
                        except Exception:
                            pass  # Failure remains recorded and must fail the fixture.
                    finally:
                        ready.set()

                await page.route(endpoint + "/_simulator/login", hold_login)
                await page.get_by_label("Access token", exact=True).fill(token)
                await page.get_by_role("button", name="Open sandbox", exact=True).click()
                await asyncio.wait_for(ready.wait(), 5)
                assert not fixture_errors and len(held) == 1, "Native login303 fixture failed"
                old = await page.evaluate("({onLogin:location.pathname==='/_simulator/login',motion:document.body.classList.contains('awsui-motion-disabled')})")
                assert old["onLogin"] and not old["motion"]
                assert await page.get_by_role("button", name="Services", exact=True).count() == 0
                # Explicit pending-evaluation diagnostic, not a claim that the
                # original synchronous check itself waited this way in CI.
                pending = asyncio.create_task(page.evaluate("()=>{window.__oldEvaluationStarted=true;return new Promise(()=>{})}"))
                await page.wait_for_function("window.__oldEvaluationStarted===true")
                route, response = held.pop()
                await route.fulfill(response=response)
                destroyed = False
                try:
                    await asyncio.wait_for(pending, 5)
                except Exception as error:
                    destroyed = "Execution context was destroyed" in str(error)
                    assert destroyed, "Real navigation diagnostic failed unexpectedly"
                await page.wait_for_url(endpoint + "/_simulator/console/")
                await page.get_by_role("button", name="Services", exact=True).wait_for(state="visible")
                assert await page.evaluate("location.pathname==='/_simulator/console/'&&!document.body.classList.contains('awsui-motion-disabled')")
                outcomes.append({"oneRealNativeLogin303":True,"oldImmediateCheckRanOnLoginDocument":True,"deliberatelyPendingEvaluationDestroyedByRealNavigation":destroyed,"fixedCheckRunsAfterAuthenticatedControls":True,"fabricatedResponse":False,"tokensOrValuesReported":False})
            finally:
                for route, _ in held:
                    try:
                        await route.abort()
                    except Exception:
                        pass  # Context teardown still revokes the fixture session.
                if pending is not None:
                    if not pending.done():
                        pending.cancel()
                    await asyncio.gather(pending, return_exceptions=True)
                try:
                    await context.close()
                finally:
                    await diagnostic.close()

    async def bounded():
        await asyncio.wait_for(exercise(), 20)

    try:
        asyncio.run(bounded())
    except Exception as error:
        outcomes.append({"fixtureErrorType":type(error).__name__})
    assert len(outcomes) == 1 and "fixtureErrorType" not in outcomes[0], "Controlled login readiness fixture failed"
    (REPORTS/"secrets-login-readiness.json").write_text(json.dumps(outcomes[0],indent=2)+"\n")


def mutation_ack_refresh_regression(page, sdk, endpoint):
    """Real writes, followed by one failed read per case; no fake API success."""
    state = {"expected": None, "acked": False, "aborted": 0}
    mutations, acknowledgements, failures = [], [], []
    name, values = "training/ack-read-recovery", ["DUMMY-secret-refresh-A", "DUMMY-secret-refresh-B", "DUMMY-secret-refresh-C"]
    arn = None

    def requested(request):
        operation = request.headers.get("x-amz-target", "").removeprefix("secretsmanager.")
        if operation in ("CreateSecret", "PutSecretValue"):
            body = json.loads(request.post_data)
            # Keep tokens only in memory for equality checks; never emit them.
            mutations.append((operation, body.get("ClientRequestToken")))

    def responded(response):
        operation = response.request.headers.get("x-amz-target", "").removeprefix("secretsmanager.")
        if operation in ("CreateSecret", "PutSecretValue") and response.status == 200:
            acknowledgements.append(operation)
            if operation == state["expected"]:
                state["acked"] = True

    def fail_one_read(route):
        if (route.request.headers.get("x-amz-target") == "secretsmanager.DescribeSecret"
                and state["acked"] and state["aborted"] == 0):
            state["aborted"] = 1
            route.abort("failed")  # Explicit network-failure fixture, never a fabricated ACK.
        else:
            route.fallback()

    def arm(operation):
        state.update(expected=operation, acked=False, aborted=0)

    def wait_failed_read():
        deadline = time.monotonic() + 5
        while not state["aborted"]:
            assert time.monotonic() < deadline, "Post-ACK Describe failure was not injected"
            page.wait_for_timeout(10)

    def closed(label, absent):
        try:
            expect(absent).to_have_count(0)
            result = True
        except AssertionError:
            failures.append(label + " retained the completed mutation form")
            result = False
        assert page.get_by_label("Retrieved secret value", exact=True).count() == 0
        assert page.evaluate("value=>!document.body.textContent.includes(value)&&![...document.querySelectorAll('input,textarea')].some(node=>node.value.includes(value))", values[0])
        assert page.evaluate("value=>!document.body.textContent.includes(value)&&![...document.querySelectorAll('input,textarea')].some(node=>node.value.includes(value))", values[1])
        # Before-fix diagnostic cleanup lets the second independent case run;
        # this is NOT the product recovery path or a successful form-close proof.
        if not result:
            page.get_by_role("button", name="Cancel", exact=True).click()
        return result

    page.on("request", requested)
    page.on("response", responded)
    page.route(endpoint + "/", fail_one_read)
    try:
        page.get_by_role("button", name="Store a new secret", exact=True).click()
        page.get_by_role("tab", name="Plaintext", exact=True).click()
        page.get_by_label("Plaintext secret value", exact=True).fill(values[0])
        page.get_by_role("button", name="Next", exact=True).click()
        page.get_by_label("Secret name", exact=True).fill(name)
        page.get_by_role("button", name="Next", exact=True).click()
        page.get_by_role("button", name="Next", exact=True).click()
        arm("CreateSecret")
        with page.expect_response(lambda response: response.request.headers.get("x-amz-target") == "secretsmanager.CreateSecret") as acknowledged:
            page.get_by_role("button", name="Store", exact=True).click()
        assert acknowledged.value.status == 200
        wait_failed_read()
        create_closed = closed("CreateSecret", page.get_by_role("button", name="Store", exact=True))
        first = sdk.get_secret_value(SecretId=name)
        arn = first["ARN"]
        assert first["SecretString"] == values[0]
        assert len(mutations) == 1 and acknowledgements == ["CreateSecret"]
        assert mutations[0][1] and first["VersionId"] == mutations[0][1]
        assert len(sdk.describe_secret(SecretId=arn)["VersionIdsToStages"]) == 1
        page.get_by_role("button", name="Refresh secrets", exact=True).click()
        page.get_by_role("link", name=name, exact=True).click()
        expect(page.get_by_role("heading", name="Secret details", exact=True)).to_be_visible()
        assert sdk.get_secret_value(SecretId=arn)["VersionId"] == first["VersionId"]
        assert len(mutations) == 1, "Read-only recovery repeated CreateSecret"

        page.get_by_role("button", name="Retrieve secret value", exact=True).click()
        expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_value(values[0])
        page.get_by_role("button", name="Edit", exact=True).click()
        page.get_by_role("tab", name="Plaintext", exact=True).click()
        page.get_by_label("Plaintext secret value", exact=True).fill(values[1])
        arm("PutSecretValue")
        with page.expect_response(lambda response: response.request.headers.get("x-amz-target") == "secretsmanager.PutSecretValue") as acknowledged:
            page.get_by_role("button", name="Save", exact=True).click()
        assert acknowledged.value.status == 200
        wait_failed_read()
        put_closed = closed("PutSecretValue", page.get_by_role("heading", name="Edit secret value", exact=True))
        second = sdk.get_secret_value(SecretId=arn)
        assert second["ARN"] == arn and second["SecretString"] == values[1]
        assert len(mutations) == 2 and acknowledgements == ["CreateSecret", "PutSecretValue"]
        assert mutations[1][1] and mutations[1][1] != mutations[0][1] and second["VersionId"] == mutations[1][1]
        assert len(sdk.describe_secret(SecretId=arn)["VersionIdsToStages"]) == 2
        page.get_by_role("button", name="Retry metadata refresh", exact=True).click()
        expect(page.get_by_role("heading", name="Secret details", exact=True)).to_be_visible()
        deadline = time.monotonic() + 5
        while second["VersionId"] not in page.locator("body").inner_text():
            assert time.monotonic() < deadline, "Read-only refresh did not recover current metadata"
            page.wait_for_timeout(10)
        assert len(mutations) == 2, "Read-only recovery repeated PutSecretValue"

        # A deliberately opened NEW mutation must get a fresh nonempty token;
        # this third write is explicit test intent, never an automatic retry.
        page.get_by_role("button", name="Retrieve secret value", exact=True).click()
        expect(page.get_by_label("Retrieved secret value", exact=True)).to_have_value(values[1])
        page.get_by_role("button", name="Edit", exact=True).click()
        page.get_by_role("tab", name="Plaintext", exact=True).click()
        page.get_by_label("Plaintext secret value", exact=True).fill(values[2])
        state["expected"] = None
        page.get_by_role("button", name="Save", exact=True).click()
        expect(page.get_by_role("heading", name="Secret details", exact=True)).to_be_visible()
        third = sdk.get_secret_value(SecretId=arn)
        assert len(mutations) == 3 and acknowledgements == ["CreateSecret", "PutSecretValue", "PutSecretValue"]
        tokens = [item[1] for item in mutations]
        assert all(tokens) and len(set(tokens)) == 3 and third["VersionId"] == tokens[2]
        assert third["ARN"] == arn and third["SecretString"] == values[2]
        result = {"privateConsoleAssetOverlay": bool(os.environ.get("SECRETS_TEST_BUNDLE")), "actualNativeACKs": [1, 1], "singlePostACKReadAbortPerCase": True,
                  "createFormClosed": create_closed, "editFormClosed": put_closed,
                  "rawDraftAbsent": True, "readOnlyRefreshRecoveredSameARN": True,
                  "noAutomaticMutationReplay": True, "freshIntentTokensNonemptyDistinct": True,
                  "SDKExactValuesAndCurrentVersions": True, "tokensOrValuesReported": False,
                  "fixture": "One explicitly aborted Describe after each actual native ACK"}
        (REPORTS / "secrets-mutation-read-recovery.json").write_text(json.dumps(result, indent=2) + "\n")
        assert not failures, "Mutation ACK recovery failed: " + "; ".join(failures)
    finally:
        page.unroute(endpoint + "/", fail_one_read)
        page.remove_listener("request", requested)
        page.remove_listener("response", responded)
        if arn is not None:
            sdk.delete_secret(SecretId=arn, ForceDeleteWithoutRecovery=True)
        page.get_by_role("button", name="Secrets", exact=True).click()
        expect(page.get_by_role("heading", name=re.compile(r"^Secrets"), level=1)).to_be_visible()
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
    # Run async response control before entering Sync Playwright's event loop.
    with running(authenticated=True) as (endpoint, token):
        login_readiness_regression(endpoint, token)
    with running(authenticated=True) as (endpoint, token), sync_playwright() as playwright:
        sdk = client(endpoint, token)
        browser = playwright.chromium.launch(
            executable_path=os.environ.get("CHROMIUM_EXECUTABLE") or shutil.which("chromium"),
            args=["--no-sandbox"])
        try:
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            # Observe newly inserted alerts before their first animation frame;
            # never read alert text, draft values, names or credentials.
            page.add_init_script("""window.secretsMotionSamples=[];const seen=new WeakSet();new MutationObserver(()=>{for(const node of document.querySelectorAll('[data-secrets-motion] [class*=awsui_alert_mx3cw]')){if(seen.has(node))continue;seen.add(node);const start=performance.now();function sample(phase){if(!node.isConnected)return;const style=getComputedStyle(node);window.secretsMotionSamples.push({phase,elapsed:performance.now()-start,opacity:style.opacity,animation:style.animationName,scoped:!!node.closest('[data-secrets-motion].awsui-motion-disabled'),bodyDisabled:document.body.classList.contains('awsui-motion-disabled')});if(performance.now()-start<200)requestAnimationFrame(()=>sample('frame'))}sample('before-first-frame')}}).observe(document,{childList:true,subtree:true})""")
            errors, outside, reads = [], [], []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.on("request", lambda request: reads.append(request.url)
                    if request.headers.get("x-amz-target") == "secretsmanager.GetSecretValue" else None)

            def local_only(route):
                overlay = os.environ.get("SECRETS_TEST_BUNDLE")
                if overlay and route.request.url in (endpoint+"/_simulator/console/app.js", endpoint+"/_simulator/console/app.css"):
                    asset = Path(overlay) / route.request.url.rsplit("/", 1)[1]
                    route.fulfill(path=str(asset), content_type="text/javascript" if asset.suffix == ".js" else "text/css")
                elif route.request.url.startswith(endpoint+"/"):
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
            authenticated_console_ready(page, endpoint)
            assert not page.evaluate("document.body.classList.contains('awsui-motion-disabled')"), "Global motion unexpectedly disabled"
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
            page.get_by_text("This table shows labeled versions only.", exact=False).screenshot(path=str(REPORTS/"secrets-alert-info-desktop.png"), animations="allow")
            assert_reflow(page)
            page.screenshot(path=str(REPORTS/"secrets-metadata-desktop.png"), full_page=False, animations="disabled")
            page.set_viewport_size({"width": 390, "height": 844})
            assert_reflow(page)
            audit("metadata-mobile")
            page.get_by_text("This table shows labeled versions only.", exact=False).screenshot(path=str(REPORTS/"secrets-alert-info-mobile.png"), animations="allow")
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
            mutation_ack_refresh_regression(page, sdk, endpoint)
            assert sdk.list_secrets()["SecretList"] == [], "Regression cleanup left a secret"
            samples = page.evaluate('window.secretsMotionSamples')
            assert samples and any(sample['phase'] == 'before-first-frame' for sample in samples)
            assert any(sample['elapsed'] >= 180 for sample in samples), "No full entrance interval observed"
            assert all(sample['opacity'] == '1' and sample['animation'] == 'none' and sample['scoped'] and not sample['bodyDisabled'] for sample in samples), "New Secrets alert had transient low opacity or global motion changed"
            (REPORTS/"secrets-alert-motion.json").write_text(json.dumps({"samples":samples,"sampledTextOrCredentials":False,"first180msOpaque":True},indent=2)+"\n")
            page.get_by_role("button", name="Services", exact=True).click()
            page.get_by_role("menuitem", name="S3", exact=True).click()
            expect(page.locator('[data-secrets-motion]')).to_have_count(0)
            assert not page.evaluate("document.body.classList.contains('awsui-motion-disabled')"), "Secrets cleanup changed global motion"
            assert page.locator('.awsui-motion-disabled').count() == 0, "Secrets motion scope leaked to another service"
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
