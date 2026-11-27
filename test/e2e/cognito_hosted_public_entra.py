"""Opt-in #253 witness: public local Entra IdP, native Cognito SP, synthetic app.

Run against a candidate tagged simulator binary and a separately built/obtained
public Entra Emulator v0.9.0. Neither binary is downloaded by this test.
Only a new numeric-loopback trust store, SQLite state and synthetic user are used.
"""

import argparse
import base64
from contextlib import ExitStack, closing, contextmanager
from datetime import UTC, datetime, timedelta
import hashlib
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import socket
import ssl
import subprocess
import tempfile
from threading import Thread
import time
from urllib.error import HTTPError
from urllib.parse import parse_qs, urlencode, urlsplit
from urllib.request import HTTPSHandler, HTTPRedirectHandler, ProxyHandler, Request, build_opener
from uuid import uuid4
import xml.etree.ElementTree as ET

import boto3
from botocore.config import Config
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa
from playwright.sync_api import sync_playwright


HOST = "127.0.0.1"
REGION = "us-east-1"
ENTRA_REVISION = "cdbef2c0f3d28c00f19e5f98072406f7382c6d0f"
SAML_MD = "urn:oasis:names:tc:SAML:2.0:metadata"
SAML_PROTO = "urn:oasis:names:tc:SAML:2.0:protocol"
SAML_REDIRECT = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
DSIG = "http://www.w3.org/2000/09/xmldsig#"


def origin(port):
    return f"https://{HOST}:{port}"


def two_ports():
    with socket.socket() as first, socket.socket() as second:
        for sock in (first, second):
            sock.bind((HOST, 0))
        return first.getsockname()[1], second.getsockname()[1]


def create_pki(directory):
    now = datetime.now(UTC)
    ca_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    leaf_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    ca_name = x509.Name([x509.NameAttribute(x509.oid.NameOID.COMMON_NAME, "Disposable #253 witness CA")])
    leaf_name = x509.Name([x509.NameAttribute(x509.oid.NameOID.COMMON_NAME, HOST)])

    def cert(subject, public):
        return (x509.CertificateBuilder().subject_name(subject).issuer_name(ca_name)
                .public_key(public).serial_number(x509.random_serial_number())
                .not_valid_before(now - timedelta(minutes=5)).not_valid_after(now + timedelta(hours=2)))

    ca = cert(ca_name, ca_key.public_key()).add_extension(x509.BasicConstraints(ca=True, path_length=0), True)
    leaf = (cert(leaf_name, leaf_key.public_key())
            .add_extension(x509.BasicConstraints(ca=False, path_length=None), True)
            .add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address(HOST))]), False)
            .add_extension(x509.ExtendedKeyUsage([x509.oid.ExtendedKeyUsageOID.SERVER_AUTH]), False))
    ca_path, leaf_path, key_path = [directory / name for name in ("ca.pem", "leaf.pem", "leaf-key.pem")]
    ca_path.write_bytes(ca.sign(ca_key, hashes.SHA256()).public_bytes(serialization.Encoding.PEM))
    leaf_path.write_bytes(leaf.sign(ca_key, hashes.SHA256()).public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(leaf_key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                               serialization.NoEncryption()))
    key_path.chmod(0o600)
    return ca_path, leaf_path, key_path


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None


def local_http(ca, authorized_origins):
    transport = build_opener(ProxyHandler({}), HTTPSHandler(context=ssl.create_default_context(cafile=ca)), NoRedirect())

    def send(url, *, method="GET", obj=None, form=None, status=200, headers=None):
        u = urlsplit(url)
        assert f"{u.scheme}://{u.netloc}" in authorized_origins and u.hostname == HOST and not u.username
        hdr = dict(headers or {})
        body = None
        if obj is not None:
            hdr["Content-Type"], body = "application/json", json.dumps(obj).encode()
        if form is not None:
            hdr["Content-Type"], body = "application/x-www-form-urlencoded", urlencode(form).encode()
        request = Request(url, data=body, headers=hdr, method=method)
        try:
            with transport.open(request, timeout=5) as res:
                seen, data = res.status, res.read(1 << 19)
        except HTTPError as res:
            seen, data = res.code, res.read(1 << 19)
        assert seen == status, f"{method} {u.path}: HTTP {seen}, expected {status}"
        return data

    return send


@contextmanager
def isolated_process(binary, args, directory, environment):
    with (directory / "process.log").open("wb") as logs:
        p = subprocess.Popen([str(binary), *args], cwd=directory, env=environment, stdout=logs, stderr=logs)
        try:
            yield p
        finally:
            if p.poll() is None:
                p.terminate()
                try:
                    p.wait(10)
                except subprocess.TimeoutExpired:
                    p.kill()
                    p.wait()
                    raise AssertionError("fixture did not stop")


def ready(send, url, process, token_file=None):
    for _ in range(150):
        assert process.poll() is None, "fixture process exited before startup"
        try:
            headers = {"X-Simulator-Token": token_file.read_text().strip()} if token_file else {}
            send(url, headers=headers)
            return
        except OSError:
            time.sleep(0.1)
    raise AssertionError("numeric loopback HTTPS fixture did not become ready")


def exact_idp_metadata(send, idp, tenant):
    # Explicitly project the intended local Redirect and current signing pin,
    # not the other roles or bindings in the public emulator's metadata.
    raw = send(f"{idp}/{tenant}/federationmetadata/2007-06/federationmetadata.xml")
    original = ET.fromstring(raw)
    assert original.tag == f"{{{SAML_MD}}}EntityDescriptor"
    role, = original.findall(f"{{{SAML_MD}}}IDPSSODescriptor")
    signing, = [k for k in role.findall(f"{{{SAML_MD}}}KeyDescriptor") if k.attrib.get("use") == "signing"]
    signing_pin, = signing.findall(f".//{{{DSIG}}}X509Certificate")
    redirect, = [e for e in role.findall(f"{{{SAML_MD}}}SingleSignOnService")
                 if e.attrib.get("Binding") == SAML_REDIRECT]
    assert redirect.attrib["Location"] == f"{idp}/{tenant}/saml2"
    entity = ET.Element(f"{{{SAML_MD}}}EntityDescriptor", entityID=original.attrib["entityID"])
    projected = ET.SubElement(entity, f"{{{SAML_MD}}}IDPSSODescriptor", protocolSupportEnumeration=SAML_PROTO)
    signer = ET.SubElement(projected, f"{{{SAML_MD}}}KeyDescriptor", use="signing")
    for el in ("KeyInfo", "X509Data", "X509Certificate"):
        signer = ET.SubElement(signer, f"{{{DSIG}}}{el}")
    signer.text = signing_pin.text
    ET.SubElement(projected, f"{{{SAML_MD}}}SingleSignOnService", redirect.attrib)
    return ET.tostring(entity, encoding="unicode")


def decode(encoded):
    return base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4))


def verify_id_token(token, jwks, issuer, client, nonce):
    header, payload, signature = token.split(".")
    h, claims = json.loads(decode(header)), json.loads(decode(payload))
    assert h["alg"] == "RS256"
    key, = [k for k in jwks["keys"] if k["kid"] == h["kid"]]
    assert (key["kty"], key["use"], key["alg"]) == ("RSA", "sig", "RS256")
    exponent, modulus = (int.from_bytes(decode(key[k]), "big") for k in ("e", "n"))
    rsa.RSAPublicNumbers(exponent, modulus).public_key().verify(
        decode(signature), f"{header}.{payload}".encode(), padding.PKCS1v15(), hashes.SHA256())
    now = time.time()
    assert (claims["iss"], claims["aud"], claims["token_use"]) == (issuer, client, "id")
    assert claims["iat"] <= now + 2 and claims["exp"] > now and claims["exp"] - claims["iat"] <= 300
    assert isinstance(claims["nonce"], str) and claims["nonce"] and (nonce is None or nonce == claims["nonce"])
    assert isinstance(claims["sub"], str) and claims["sub"]
    return claims["sub"], claims["nonce"]


class SampleApplication:
    """Tiny independent test relying party; no Cognito or Simulator internals."""
    def __init__(self, cert, key):
        self.pending, self.sessions, self.accepted = {}, {}, []
        self.failure = None
        sample = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, _fmt, *_args):
                pass  # Do not log callback query strings or cookies.

            def do_GET(self):
                try:
                    sample.handle(self)
                except Exception as error:  # passed to test driver, not browser
                    sample.failure = error
                    sample.reply(self, 500, b"test application rejected the callback")

        self.server = ThreadingHTTPServer((HOST, 0), Handler)
        scheme = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        scheme.load_cert_chain(cert, key)
        self.server.socket = scheme.wrap_socket(self.server.socket, server_side=True)
        self.address = origin(self.server.server_port)
        self.callback = self.address + "/callback"
        self.thread = Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=8)
        assert not self.thread.is_alive()

    def bind(self, send, issuer, client):
        self.call, self.issuer, self.client = send, issuer, client

    @staticmethod
    def reply(handler, code, body=b"", *, location=None, cookie=None):
        handler.send_response(code)
        handler.send_header("Cache-Control", "no-store")
        handler.send_header("Referrer-Policy", "no-referrer")
        handler.send_header("Content-Type", "text/plain; charset=UTF-8")
        if location:
            handler.send_header("Location", location)
        if cookie:
            handler.send_header("Set-Cookie", cookie)
        handler.send_header("Content-Length", str(len(body)))
        handler.end_headers()
        handler.wfile.write(body)

    def handle(self, handler):
        route = urlsplit(handler.path)
        cookies = SimpleCookie(handler.headers.get("Cookie", ""))
        q = parse_qs(route.query)
        if route.path == "/protected":
            session = cookies.get("__Host-tcsim-proof-session")
            if not session or session.value not in self.sessions:
                return self.reply(handler, 401, b"synthetic authentication required")
            return self.reply(handler, 200, b"accepted synthetic application session")
        if route.path == "/start":
            mode, = q["nonce"]
            assert mode in {"explicit", "provider-generated"}
            state, verifier = secrets.token_urlsafe(32), secrets.token_urlsafe(48)
            nonce = secrets.token_urlsafe(24) if mode == "explicit" else None
            self.pending[state] = (verifier, nonce)
            challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
            params = {"identity_provider": "Entra", "response_type": "code", "client_id": self.client,
                      "redirect_uri": self.callback, "state": state, "scope": "openid",
                      "code_challenge": challenge, "code_challenge_method": "S256"}
            if nonce is not None:
                params["nonce"] = nonce
            return self.reply(handler, 303, location=self.issuer + "/oauth2/authorize?" + urlencode(params),
                              cookie=f"__Host-tcsim-proof-state={state}; Path=/; Secure; HttpOnly; SameSite=Lax")
        if route.path != "/callback":
            return self.reply(handler, 404)
        if set(q) != {"code", "state"} or any(len(v) != 1 for v in q.values()):
            return self.reply(handler, 400)
        state = q["state"][0]
        cookie_state = cookies.get("__Host-tcsim-proof-state")
        if not cookie_state or not secrets.compare_digest(state, cookie_state.value) or state not in self.pending:
            return self.reply(handler, 400)
        verifier, nonce = self.pending.pop(state)
        token = self.issuer + "/oauth2/token"
        exchange = {"grant_type": "authorization_code", "client_id": self.client, "redirect_uri": self.callback,
                    "code": q["code"][0], "code_verifier": verifier}
        wrong_verifier = {**exchange, "code_verifier": secrets.token_urlsafe(48)}
        self.call(token, method="POST", form=wrong_verifier, status=400)
        result = json.loads(self.call(token, method="POST", form=exchange))
        self.call(token, method="POST", form=exchange, status=400)
        jwks = json.loads(self.call(self.issuer + "/.well-known/jwks.json"))
        sub, returned_nonce = verify_id_token(result["id_token"], jwks, self.issuer, self.client, nonce)
        self.accepted.append((sub, returned_nonce))
        session = secrets.token_urlsafe(32)
        self.sessions[session] = sub
        return self.reply(handler, 303, location=self.address + "/protected",
                          cookie=f"__Host-tcsim-proof-session={session}; Path=/; Secure; HttpOnly; SameSite=Lax")


def require_clean_build(binary, module, revision):
    build = subprocess.check_output(["go", "version", "-m", str(binary)], text=True)
    fields = {tuple(line.strip().split("\t", 1)) for line in build.splitlines()}
    required = {("path", module), ("build", "vcs.revision=" + revision), ("build", "vcs.modified=false")}
    if not required <= fields:
        raise RuntimeError("fixture binary is not the expected clean " + module + " revision")


def sha256_file(binary):
    with binary.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def verify(simulator, entra, chromium, expected_revision, expected_sha256):
    if not __debug__:
        raise RuntimeError("the acceptance witness cannot run with Python -O")
    if re.fullmatch(r"[0-9a-f]{40}", expected_revision) is None:
        raise ValueError("expected simulator revision must be a full lowercase Git SHA")
    if re.fullmatch(r"[0-9a-f]{64}", expected_sha256) is None:
        raise ValueError("expected simulator SHA-256 must be a full lowercase digest")
    actual_sha256 = sha256_file(simulator)
    if not secrets.compare_digest(actual_sha256, expected_sha256):
        raise RuntimeError("simulator SHA-256 does not match the requested build")
    require_clean_build(simulator, "github.com/susumutomita/TenkaCloudSimulator/cmd/simulator", expected_revision)
    require_clean_build(entra, "github.com/calvinchengx/entra-emulator/cmd/entra-emulator", ENTRA_REVISION)
    with tempfile.TemporaryDirectory(prefix="cognito-public-entra-") as work, ExitStack() as stack:
        root = Path(work)
        ca, cert, key = create_pki(root)
        app = SampleApplication(cert, key)
        stack.callback(app.close)
        simulator_port, idp_port = two_ports()
        simulator_origin, idp = origin(simulator_port), origin(idp_port)
        allowed = {simulator_origin, idp, app.address}
        send = local_http(ca, allowed)
        home, idp_dir, sp_dir = [root / name for name in ("home", "provider", "simulator")]
        for directory in (home, idp_dir, sp_dir):
            directory.mkdir()
        nss = home / ".pki" / "nssdb"
        nss.mkdir(parents=True)
        env = {"PATH": os.environ["PATH"], "HOME": str(home), "TMPDIR": work,
               "XDG_CONFIG_HOME": str(home / "config"), "XDG_CACHE_HOME": str(home / "cache")}
        config = "sql:" + str(nss)
        subprocess.run(["certutil", "-N", "--empty-password", "-d", config], env=env, check=True)
        subprocess.run(["certutil", "-A", "-d", config, "-n", "Local acceptance CA", "-t", "C,,", "-i", str(ca)],
                       env=env, check=True)
        tenant = str(uuid4())
        idp_process = stack.enter_context(isolated_process(entra, [], idp_dir, {
            **env, "HOST": HOST, "PORT": str(idp_port), "ORIGIN_MODE": "compat", "PUBLIC_ORIGIN": idp,
            "TLS_ENABLED": "true", "TLS_CERT": str(cert), "TLS_KEY": str(key), "ENTRA_DATA_DIR": str(idp_dir),
            "TENANT_ID": tenant, "SEED_ON_START": "false", "REQUIRE_PASSWORD": "true",
        }))
        ready(send, idp + "/health", idp_process)
        upn = "witness-" + secrets.token_hex(8) + "@example.invalid"
        password = "Synthetic-" + secrets.token_urlsafe(22)
        user = json.loads(send(idp + "/admin/api/users", method="POST", status=201, obj={
            "userPrincipalName": upn, "displayName": "Generated SAML Witness", "password": password,
        }))
        token_path = sp_dir / "operator-token"
        sp_process = stack.enter_context(isolated_process(simulator, [
            "--listen", f"{HOST}:{simulator_port}", "--state", str(sp_dir / "state.sqlite"),
            "--token-file", str(token_path), "--cognito-local-federation-config", "--cognito-local-hosted-login",
            "--cognito-local-tls-cert", str(cert), "--cognito-local-tls-key", str(key),
        ], sp_dir, env))
        ready(send, simulator_origin + "/_simulator/health", sp_process, token_path)
        token = token_path.read_text().strip()
        with closing(boto3.client("cognito-idp", endpoint_url=simulator_origin, region_name=REGION, verify=str(ca),
                          aws_access_key_id="TCSIMOPERATOR", aws_secret_access_key=token,
                          config=Config(proxies={}, retries={"max_attempts": 0}))) as sdk:
            pool = sdk.create_user_pool(PoolName="Native hosted login witness")["UserPool"]["Id"]
            issuer = simulator_origin + "/_simulator/cognito/" + pool
            sdk.create_identity_provider(UserPoolId=pool, ProviderName="Entra", ProviderType="SAML", ProviderDetails={
                "MetadataFile": exact_idp_metadata(send, idp, tenant),
            })
            client = sdk.create_user_pool_client(UserPoolId=pool, ClientName="Synthetic application",
                                                 CallbackURLs=[app.callback], SupportedIdentityProviders=["Entra"],
                                                 AllowedOAuthFlows=["code"], AllowedOAuthScopes=["openid"],
                                                 AllowedOAuthFlowsUserPoolClient=True)["UserPoolClient"]["ClientId"]
            app.bind(send, issuer, client)
            metadata = ET.fromstring(send(issuer + "/saml2/metadata"))
            acs, = metadata.findall(f".//{{{SAML_MD}}}AssertionConsumerService")
            assert acs.attrib["Location"] == issuer + "/saml2/idpresponse"
            send(idp + "/admin/api/apps", method="POST", status=201, obj={
                "displayName": "Generated Cognito fixture", "appIdUri": metadata.attrib["entityID"],
                "redirectUris": [{"uri": acs.attrib["Location"], "type": "saml-acs"}],
            })
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(executable_path=chromium, headless=True, env=env,
                                                      args=["--disable-background-networking", "--disable-component-update"])
                try:
                    for mode in ("explicit", "provider-generated"):
                        if mode != "explicit":
                            upn = "renamed-" + secrets.token_hex(8) + "@example.invalid"
                            renamed = json.loads(send(idp + "/admin/api/users/" + user["id"], method="PATCH",
                                                      obj={"userPrincipalName": upn}))
                            assert renamed["id"] == user["id"] and renamed["userPrincipalName"] == upn
                        context = browser.new_context(service_workers="block")
                        try:
                            def numeric_fixture_only(route):
                                dest = urlsplit(route.request.url)
                                if f"{dest.scheme}://{dest.netloc}" in allowed:
                                    route.continue_()
                                else:
                                    route.abort()

                            context.route("**/*", numeric_fixture_only)
                            page = context.new_page()
                            page.set_default_timeout(12000)
                            assert page.goto(app.address + "/protected").status == 401
                            page.goto(app.address + "/start?nonce=" + mode)
                            assert page.url.startswith(idp + "/")
                            page.get_by_label("Email", exact=True).fill(upn)
                            page.get_by_label("Password", exact=True).fill(password)
                            callback_received = lambda r: r.url.split("?", 1)[0] == app.callback
                            with page.expect_response(callback_received) as redeemed:
                                with page.expect_request(lambda r: r.url == acs.attrib["Location"] and r.method == "POST") as signed_post:
                                    page.get_by_role("button", name="Sign in", exact=True).click()
                            assert signed_post.value.header_value("origin") == idp
                            if app.failure:
                                raise app.failure
                            assert redeemed.value.status == 303
                            page.wait_for_url(app.address + "/protected")
                            assert page.locator("body").inner_text() == "accepted synthetic application session"
                        finally:
                            context.close()
                finally:
                    browser.close()
            first, second = app.accepted
            assert first[0] == second[0] and first[1] != second[1]
            sdk.delete_user_pool(UserPoolId=pool)
            assert sdk.list_user_pools(MaxResults=10)["UserPools"] == []
    if not secrets.compare_digest(sha256_file(simulator), actual_sha256):
        raise RuntimeError("simulator changed during the witness")
    print(f"Simulator revision {expected_revision}; SHA-256 {actual_sha256}.")
    print("PASS real public Entra SAML → Cognito PKCE → accepted synthetic app; strict local TLS, isolated "
          "nonce modes, stable identity after UPN rename, wrong/replayed code denied, and clean SQLite fixture removal.")


if __name__ == "__main__":
    cli = argparse.ArgumentParser(description=__doc__)
    for name in ("simulator", "entra", "chromium"):
        cli.add_argument("--" + name, required=True, type=lambda p: Path(p).resolve(strict=True))
    cli.add_argument("--expected-simulator-revision", required=True)
    cli.add_argument("--expected-simulator-sha256", required=True)
    args = cli.parse_args()
    verify(args.simulator, args.entra, args.chromium, args.expected_simulator_revision, args.expected_simulator_sha256)
