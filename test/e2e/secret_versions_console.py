"""Actual native version metadata UI contract; never retrieve secret values.

Requires the integrated Version metadata tab. An explicitly selected private
SECRET_VERSIONS_TEST_BUNDLE is preview-only, never canonical embedded proof.
Held responses come from real native ListSecretVersionIds. Browser Abort may
prevent JS delivery; those cases do not independently prove post-Abort callbacks.
"""
import hashlib
import json
import os
import shutil
import sqlite3
import tempfile
import time
from pathlib import Path
from urllib.parse import urlsplit

from check import Config, REPORTS, boto3, expect, running, sync_playwright, assert_reflow
from secrets_manager import control
from secrets_versions import frame as secret_frame


def run():
    axe_path = Path(os.environ["AXE_SCRIPT"])
    provenance = json.loads((axe_path.parent / "provenance.json").read_text())
    assert hashlib.sha256(axe_path.read_bytes()).hexdigest() == provenance["bundleSha256"]
    axe = axe_path.read_text()
    audits, boundaries, clients = [], [], []
    operations, outside, errors = [], [], []
    overlay = os.environ.get("SECRET_VERSIONS_TEST_BUNDLE")

    def sdk(endpoint, token, region="us-east-1"):
        origin = urlsplit(endpoint)
        assert origin.scheme == "http" and origin.hostname == "127.0.0.1" and origin.port
        client = boto3.client("secretsmanager", endpoint_url=endpoint, region_name=region,
            aws_access_key_id="TCSIMVERSIONUI", aws_secret_access_key="dummy-only",
            config=Config(proxies={}, retries={"max_attempts": 0}, connect_timeout=2, read_timeout=5))
        client.meta.events.register("before-sign", lambda request, **kw: request.headers.add_header("X-Simulator-Token", token))
        def guard(request, **kw):
            target = urlsplit(request.url)
            assert (target.scheme, target.hostname, target.port) == (origin.scheme, origin.hostname, origin.port)
            assert target.path == "/" and not target.query and not target.fragment
        client.meta.events.register("before-send", guard)
        clients.append(client)
        return client

    def token(i):
        return f"26600000-0000-0000-0000-{i:012d}"

    def no_values(page):
        assert "DUMMY-version-ui-" not in page.locator("body").inner_text()
        assert not any(op == "secretsmanager.GetSecretValue" for op in operations), "Metadata UI retrieved a secret value"
        assert all(not op.startswith("secretsmanager.") or op in {"secretsmanager.ListSecretVersionIds", "secretsmanager.ListSecrets", "secretsmanager.DescribeSecret"} for op in operations), "Metadata UI issued a mutation"

    def audit(page, label):
        page.evaluate(axe)
        result = page.evaluate("async()=>await axe.run(document,{runOnly:{type:'tag',values:['wcag2a','wcag2aa','wcag21aa']}})")
        summaries = [{"id": v["id"], "impact": v["impact"], "nodes": len(v["nodes"])} for v in result["violations"]]
        audits.append({"screen": label, "width": page.viewport_size["width"], "violations": summaries})
        assert summaries == [], "Version metadata accessibility violations"
        assert_reflow(page)
        no_values(page)
        page.screenshot(path=str(REPORTS / (label + ".png")), full_page=True)

    def login(page, endpoint, credential):
        page.goto(endpoint + "/_simulator/console/")
        page.get_by_label("Access token", exact=True).fill(credential)
        page.get_by_role("button", name="Open sandbox", exact=True).click()
        expect(page.get_by_role("button", name="Services", exact=True)).to_be_visible()
        page.goto(endpoint + "/_simulator/console/#/secretsmanager")
        page.get_by_role("tab", name="Version metadata", exact=True).click()
        expect(page.get_by_label("Secret ARN for version metadata", exact=True)).to_be_visible()

    def enter(page, arn):
        page.get_by_label("Secret ARN for version metadata", exact=True).fill(arn)

    def list_versions(page, status=200):
        with page.expect_response(lambda r: r.request.headers.get("x-amz-target") == "secretsmanager.ListSecretVersionIds") as response:
            page.get_by_role("button", name="List versions", exact=True).click()
        assert response.value.status == status
        if status == 200:
            expect(page.get_by_role("table", name="Secret version metadata", exact=True)).to_be_visible()
        else:
            expect(page.get_by_role("alert").filter(has=page.get_by_text("Version metadata unavailable", exact=True))).to_be_visible()
        no_values(page)

    def choose_identity(page, identity):
        page.get_by_role("button", name="Account menu", exact=True).click()
        page.get_by_role("menuitem", name="Choose identity", exact=True).click()
        dialog = page.get_by_role("dialog")
        dialog.get_by_label("Principal name", exact=True).fill(identity)
        dialog.get_by_role("button", name="Choose identity", exact=True).click()
        page.get_by_role("tab", name="Version metadata", exact=True).click()

    with tempfile.TemporaryDirectory(prefix="secret-version-ui-") as temp:
        state, peer_state = Path(temp) / "a.db", Path(temp) / "b.db"
        with running(db=state, authenticated=True) as (endpoint, operator), \
                running(db=peer_state, authenticated=True) as (peer_endpoint, peer_operator), sync_playwright() as playwright:
            first, peer, west = sdk(endpoint, operator), sdk(peer_endpoint, peer_operator), sdk(endpoint, operator, "us-west-2")
            arn = first.create_secret(Name="training/version-ui", ClientRequestToken=token(0), SecretString="DUMMY-version-ui-A")["ARN"]
            for index in range(1, 23):
                first.put_secret_value(SecretId=arn, ClientRequestToken=token(index), SecretString="DUMMY-version-ui-" + str(index))
            peer_arn = peer.create_secret(Name="training/version-ui", ClientRequestToken=token(99), SecretString="DUMMY-version-ui-peer")["ARN"]
            west_arn = west.create_secret(Name="training/version-ui", ClientRequestToken=token(98), SecretString="DUMMY-version-ui-west")["ARN"]
            expected = first.list_secret_version_ids(SecretId=arn, IncludeDeprecated=True)["Versions"]
            assert len(expected) == 23
            policy = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "secretsmanager:ListSecretVersionIds", "Resource": arn}]}
            control(endpoint, operator, "/_simulator/v1/iam/principals/version-ui-reader/policy", policy)
            participant = control(endpoint, operator, "/_simulator/v1/access", {"Principal": "version-ui-reader"})["AccessToken"]
            control(endpoint, operator, "/_simulator/v1/iam/principals/version-ui-denied/policy", {"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Action": "secretsmanager:*", "Resource": "*"}]})
            before_frame, peer_before_frame = secret_frame(state), secret_frame(peer_state)
            browser = playwright.chromium.launch(executable_path=os.environ.get("CHROMIUM_EXECUTABLE") or shutil.which("chromium"), args=["--no-sandbox"])
            contexts = []
            try:
                def context_for(destination, width=1440):
                    context = browser.new_context(viewport={"width": width, "height": 1000})
                    contexts.append(context)
                    page = context.new_page()
                    page.set_default_timeout(5000)
                    page.on("pageerror", lambda error: errors.append(type(error).__name__))
                    page.on("request", lambda request: operations.append(request.headers.get("x-amz-target", "")))
                    def local(route):
                        address = route.request.url
                        if not address.startswith(destination + "/"):
                            outside.append(urlsplit(address).hostname)
                            route.abort()
                        elif overlay and address in (destination + "/_simulator/console/app.js", destination + "/_simulator/console/app.css"):
                            asset = Path(overlay) / address.rsplit("/", 1)[1]
                            assert asset.is_file()
                            route.fulfill(path=str(asset), content_type="text/javascript" if asset.suffix == ".js" else "text/css")
                        else:
                            route.continue_()
                    page.route("**/*", local)
                    return page

                page = context_for(endpoint)
                login(page, endpoint, operator)
                enter(page, arn)
                list_versions(page)
                table = page.get_by_role("table", name="Secret version metadata", exact=True)
                for version in (token(21), token(22)):
                    expect(table.get_by_text(version, exact=True)).to_be_visible()
                expect(table.get_by_text(token(0), exact=True)).to_have_count(0)
                audit(page, "versions-labeled-desktop")
                page.get_by_role("checkbox", name="Include deprecated versions", exact=True).check()
                expect(table.get_by_text(token(21), exact=True)).to_have_count(0)
                list_versions(page)
                for item in expected[:20]:
                    expect(table.get_by_text(item["VersionId"], exact=True)).to_be_visible()
                    row = table.get_by_role("row").filter(has=page.get_by_text(item["VersionId"], exact=True))
                    expect(row.get_by_text(item["CreatedDate"].isoformat(timespec="milliseconds").replace("+00:00", "Z"), exact=True)).to_be_visible()
                page.get_by_role("button", name="Next page", exact=True).focus()
                page.keyboard.press("Enter")
                for item in expected[20:]:
                    expect(table.get_by_text(item["VersionId"], exact=True)).to_be_visible()
                expect(table.get_by_text(token(0), exact=True)).to_have_count(0)
                expect(page.get_by_role("button", name="Next page", exact=True)).to_be_disabled()
                page.set_viewport_size({"width": 390, "height": 844})
                audit(page, "versions-page-two-mobile")
                page.set_viewport_size({"width": 1440, "height": 1000})

                held, hold = [], {"next": False, "arn": arn}
                def native_hold(route):
                    if hold["next"] and route.request.headers.get("x-amz-target") == "secretsmanager.ListSecretVersionIds":
                        hold["next"] = False
                        response = route.fetch(max_redirects=0)
                        assert response.status == 200
                        body = response.json()
                        assert body["ARN"] == hold["arn"] and body["Versions"], "Held native response was vacuous"
                        assert "DUMMY-version-ui-" not in str(body)
                        held.append((route, response))
                    else:
                        route.continue_()
                page.route(endpoint + "/", native_hold)

                def wait_hold():
                    deadline = time.monotonic() + 5
                    while not held and time.monotonic() < deadline:
                        page.wait_for_timeout(20)
                    assert held, "Real native hold did not complete"
                    expect(page.get_by_role("button", name="Cancel version request", exact=True)).to_be_visible()
                    return held.pop(0)

                def release(pair):
                    route, response = pair
                    aborted = "ERR_ABORTED" in (route.request.failure or "")
                    try:
                        route.fulfill(response=response)
                    except Exception:
                        aborted = "ERR_ABORTED" in (route.request.failure or "")
                        assert aborted, "Unexpected route delivery failure"
                    return {"actualNativeResponse": True, "transportWasAborted": aborted, "postAbortJSDeliveryProved": False}

                hold["next"] = True
                page.get_by_role("button", name="Refresh versions", exact=True).click()
                old = wait_hold()
                page.evaluate("window.__versionLeaf=document.querySelector('[data-secret-versions]')")
                assert page.evaluate("!!window.__versionLeaf")
                page.get_by_role("button", name="Cancel version request", exact=True).click()
                expect(table.get_by_text(token(0), exact=True)).to_have_count(0)
                page.get_by_role("checkbox", name="Include deprecated versions", exact=True).uncheck()
                hold["next"] = True
                page.get_by_role("button", name="List versions", exact=True).click()
                new = wait_hold()
                assert page.evaluate("window.__versionLeaf===document.querySelector('[data-secret-versions]')"), "Fixture remounted the leaf"
                boundaries.append(release(old))
                page.evaluate("()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)))")
                expect(page.get_by_role("button", name="Cancel version request", exact=True)).to_be_visible()
                expect(table.get_by_text(token(0), exact=True)).to_have_count(0)
                boundaries.append(release(new))
                expect(table.get_by_text(token(22), exact=True)).to_be_visible()
                expect(table.get_by_text(token(0), exact=True)).to_have_count(0)

                # Actual storage fault, sanitized500, no stale rows, then repair.
                with sqlite3.connect(state) as db:
                    db.execute("ALTER TABLE aws_secret_versions RENAME TO version_ui_private_fault")
                try:
                    with page.expect_response(lambda r: r.request.headers.get("x-amz-target") == "secretsmanager.ListSecretVersionIds") as response:
                        page.get_by_role("button", name="Refresh versions", exact=True).click()
                    assert response.value.status == 500
                    expect(page.get_by_text("Version metadata unavailable", exact=True)).to_be_visible()
                    expect(table.get_by_text(token(22), exact=True)).to_have_count(0)
                    assert "version_ui_private_fault" not in page.locator("body").inner_text()
                    audit(page, "versions-read-failure")
                finally:
                    with sqlite3.connect(state) as db:
                        db.execute("ALTER TABLE version_ui_private_fault RENAME TO aws_secret_versions")
                page.get_by_role("button", name="Retry first page", exact=True).click()
                expect(table.get_by_text(token(22), exact=True)).to_be_visible()

                # Direct principal transition while actual old metadata is held.
                hold["next"] = True
                page.get_by_role("button", name="Refresh versions", exact=True).click()
                pending = wait_hold()
                choose_identity(page, "version-ui-denied")
                boundaries.append(release(pending))
                expect(page.get_by_label("Secret ARN for version metadata", exact=True)).to_have_value("")
                enter(page, arn)
                list_versions(page, 403)
                expect(table.get_by_text(token(22), exact=True)).to_have_count(0)
                choose_identity(page, "simulator-admin")
                enter(page, arn)
                list_versions(page)

                # Direct region transition before releasing the old east reply.
                hold["next"] = True
                page.get_by_role("button", name="Refresh versions", exact=True).click()
                pending = wait_hold()
                page.get_by_role("button", name="us-east-1", exact=True).click()
                page.get_by_role("menuitem", name="us-west-2", exact=True).click()
                page.get_by_role("tab", name="Version metadata", exact=True).click()
                boundaries.append(release(pending))
                enter(page, west_arn)
                list_versions(page)
                expect(table.get_by_text(token(98), exact=True)).to_be_visible()
                expect(table.get_by_text(token(22), exact=True)).to_have_count(0)
                hold["arn"] = west_arn
                hold["next"] = True
                page.get_by_role("button", name="Refresh versions", exact=True).click()
                pending = wait_hold()
                unknown_arn = west_arn[:-6] + ("AAAAAA" if not west_arn.endswith("AAAAAA") else "BBBBBB")
                enter(page, unknown_arn)
                boundaries.append(release(pending))
                expect(table.get_by_text(token(98), exact=True)).to_have_count(0)
                list_versions(page, 400)
                enter(page, west_arn)
                list_versions(page)
                expect(table.get_by_text(token(98), exact=True)).to_be_visible()
                page.unroute(endpoint + "/", native_hold)
                page.get_by_role("button", name="Account menu", exact=True).click()
                page.keyboard.press("End")
                page.keyboard.press("Enter")
                expect(page.get_by_label("Access token", exact=True)).to_be_visible()
                login(page, endpoint, operator)
                expect(page.get_by_label("Secret ARN for version metadata", exact=True)).to_have_value("")
                enter(page, arn)
                list_versions(page)
                expect(table.get_by_text(token(22), exact=True)).to_be_visible()

                # A principal possessing ONLY the new list permission can use
                # the known-ARN tab even when the parent's ListSecrets is403.
                limited = context_for(endpoint, 390)
                login(limited, endpoint, participant)
                bound = limited.evaluate("async()=>{const r=await fetch('/_simulator/v1/capabilities');if(r.status!==200)throw Error('Capability authentication failed');const c=await r.json();return {principal:c.principal,identitySelection:c.identitySelection}}")
                assert bound == {"principal": "version-ui-reader", "identitySelection": False}, "Browser session did not bind the list-only participant"
                enter(limited, arn)
                list_versions(limited)
                expect(limited.get_by_role("table", name="Secret version metadata", exact=True).get_by_text(token(22), exact=True)).to_be_visible()
                audit(limited, "versions-list-only-mobile")
                limited.evaluate("localStorage.setItem('tenkacloud-simulator-locale','ja')")
                limited.reload()
                expect(limited.get_by_role("tab", name="バージョンメタデータ", exact=True)).to_be_visible()
                limited.get_by_role("tab", name="バージョンメタデータ", exact=True).click()
                limited.get_by_label("バージョンメタデータのシークレット ARN", exact=True).fill(arn)
                limited.get_by_role("button", name="バージョンを一覧表示", exact=True).click()
                expect(limited.get_by_role("table", name="シークレットのバージョンメタデータ", exact=True).get_by_text(token(22), exact=True)).to_be_visible()
                audit(limited, "versions-japanese-mobile")
                peer_page = context_for(peer_endpoint)
                login(peer_page, peer_endpoint, peer_operator)
                enter(peer_page, peer_arn)
                list_versions(peer_page)
                peer_table = peer_page.get_by_role("table", name="Secret version metadata", exact=True)
                expect(peer_table.get_by_text(token(99), exact=True)).to_be_visible()
                expect(peer_table.get_by_text(token(22), exact=True)).to_have_count(0)
                for active_page in (page, limited, peer_page):
                    no_values(active_page)
                assert secret_frame(state) == before_frame and secret_frame(peer_state) == peer_before_frame, "Metadata console changed a secret graph"
                assert not outside and not errors, "Console made an outside request or page error"
                assert first.list_secret_version_ids(SecretId=arn, IncludeDeprecated=True)["Versions"] == expected
                assert peer.list_secret_version_ids(SecretId=peer_arn)["Versions"][0]["VersionId"] == token(99)
            finally:
                for context in contexts:
                    context.close()
                browser.close()
                # Cleanup uses only original owned native resources.
                first.delete_secret(SecretId=arn, ForceDeleteWithoutRecovery=True)
                west.delete_secret(SecretId=west_arn, ForceDeleteWithoutRecovery=True)
                peer.delete_secret(SecretId=peer_arn, ForceDeleteWithoutRecovery=True)
                for client in clients:
                    client.close()
    (REPORTS / "secret-versions-console.json").write_text(json.dumps({"audits": audits, "boundaries": boundaries,
        "privatePreviewAssetOverlay": bool(overlay), "outsideRequests": len(outside), "pageErrors": len(errors),
        "secretValueRequests": sum(op == "secretsmanager.GetSecretValue" for op in operations),
        "limits": "Actual abort/late-network boundaries, not proof of delivery of post-Abort JS callbacks; locale/principal fixtures use real existing host controls."}, indent=2) + "\n")
    print("Secret version metadata actual console/native/IAM/epoch/locale/a11y PASS")


if __name__ == "__main__":
    run()
