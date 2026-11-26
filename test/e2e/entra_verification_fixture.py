"""Check the exact built simulator's opt-in local Entra verification fixture."""
import base64
from contextlib import contextmanager
from hashlib import sha256
from http.client import HTTPConnection
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import tempfile
import time
from urllib.parse import urlsplit


COMMAND = "entra-verification-fixture"
NONCE = "one-local-transaction-nonce"
AUDIENCE = "urn:tenkacloud:simulator:test:entra"
PREFIX = "/_simulator/azure/entra"
ISSUER_PATH = PREFIX + "/v2.0"
META_PATH = ISSUER_PATH + "/verification-metadata.json"
KEYS_PATH = PREFIX + "/discovery/v2.0/keys"


@contextmanager
def launch(binary, cwd, listen, *options):
    with subprocess.Popen([binary, COMMAND, "--listen", listen, *options], cwd=cwd,
                          stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE) as process:
        try:
            with selectors.DefaultSelector() as sel:
                sel.register(process.stdout, selectors.EVENT_READ)
                if not sel.select(12):
                    raise RuntimeError("fixture did not emit readiness")
            yield json.loads(process.stdout.readline())
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    process.kill()
                    raise
            assert process.returncode == 0, "fixture did not shut down gracefully"
            assert not process.stdout.read() and not process.stderr.read(), "unexpected output after readiness"


def get(authority, path, method="GET", headers=None, body=None):
    # Pin direct numeric loopback and ignore proxy environment configuration.
    assert re.fullmatch(r"127\.0\.0\.1:[0-9]+", authority)
    connection = HTTPConnection(authority, timeout=4)
    try:
        connection.request(method, path, body=body, headers=headers or {})
        response = connection.getresponse()
        payload = response.read(8193)
        assert len(payload) <= 8192
        return response.status, response.headers, payload
    finally:
        connection.close()


def b64(raw):
    return base64.urlsafe_b64decode(raw + "=" * (-len(raw) % 4))


def verifies(token, jwk):
    header, claims, signature = token.split(".")
    key = int.from_bytes(b64(jwk["n"]), "big")
    exponent = int.from_bytes(b64(jwk["e"]), "big")
    assert key.bit_length() == 2048 and exponent == 65537
    encoded = pow(int.from_bytes(b64(signature), "big"), exponent, key).to_bytes(256, "big")
    digest = bytes.fromhex("3031300d060960864801650304020105000420") + sha256(
        (header + "." + claims).encode("ascii")).digest()
    return encoded == b"\x00\x01" + b"\xff" * (253 - len(digest)) + b"\x00" + digest


def check_metadata(ready):
    assert ready["fixture_profile"] == "tenkacloud-simulator-entra-verification-fixture-v1"
    assert ready["expected_audience"] == AUDIENCE
    assert ready["id_token_signing_alg_values_supported"] == ["RS256"]
    issuer = urlsplit(ready["issuer"])
    assert issuer.scheme == "http" and issuer.path == ISSUER_PATH
    assert not issuer.query and not issuer.fragment and issuer.username is None
    authority = issuer.netloc
    assert ready["jwks_uri"] == "http://" + authority + KEYS_PATH
    assert ready["verification_metadata_url"] == "http://" + authority + META_PATH
    status, hdr, body = get(authority, META_PATH)
    assert status == 200 and hdr["Cache-Control"] == "no-store" and hdr["Content-Type"] == "application/json"
    assert json.loads(body) == {k: v for k, v in ready.items()
                                if k not in {"verification_metadata_url", "test_id_token"}}
    status, hdr, body = get(authority, KEYS_PATH)
    assert status == 200 and hdr["Cache-Control"] == "no-store"
    document = json.loads(body)
    assert set(document) == {"keys"} and len(document["keys"]) == 1
    key, = document["keys"]
    assert set(key) == {"alg", "e", "kid", "kty", "n", "use"}
    assert (key["alg"], key["kty"], key["use"]) == ("RS256", "RSA", "sig")
    return authority, key


def check(binary):
    with tempfile.TemporaryDirectory(prefix="simulator-entra-test.") as directory:
        for args in [
            ["--listen", "0.0.0.0:0"], ["--listen", "localhost:0"],
            ["--listen", "[::1]:0"], ["--allow-remote"], ["--unsafe-no-auth"],
            ["--issuer", "example.invalid"], ["--test-id-token-nonce", ""],
        ]:
            proc = subprocess.run([binary, COMMAND, *args], cwd=directory,
                                  capture_output=True, timeout=8)
            assert proc.returncode == 1 and b'"fixture_profile"' not in proc.stdout
        with launch(binary, directory, "127.0.0.1:0", "--test-id-token-nonce", NONCE) as ready:
            authority, key = check_metadata(ready)
            token = ready["test_id_token"]
            header, claims, _ = token.split(".")
            assert json.loads(b64(header)) == {"alg": "RS256", "typ": "JWT", "kid": key["kid"]}
            claims = json.loads(b64(claims))
            assert set(claims) == {"iss", "aud", "sub", "nonce", "iat", "exp"}
            assert claims["iss"] == ready["issuer"] and claims["aud"] == AUDIENCE
            assert claims["sub"] == "synthetic-fixture-user" and claims["nonce"] == NONCE
            assert claims["exp"] - claims["iat"] == 60 and claims["iat"] <= time.time() < claims["exp"]
            assert verifies(token, key)
            for path in ["/", ISSUER_PATH + "/.well-known/openid-configuration", ISSUER_PATH + "/authorize",
                         ISSUER_PATH + "/saml2", KEYS_PATH + "?foo=bar"]:
                assert get(authority, path)[0] == 404
            assert get(authority, ISSUER_PATH + "/token", "POST")[0] == 404
            assert get(authority, KEYS_PATH, headers={"Host": "example.invalid"})[0] == 404
            assert get(authority, KEYS_PATH, headers={"Forwarded": "host=example.invalid"})[0] == 404
            assert get(authority, KEYS_PATH, headers={"Authorization": "synthetic-only"})[0] == 404
            assert get(authority, KEYS_PATH, "POST")[0] == 405
            assert get(authority, KEYS_PATH, body="synthetic")[0] == 400
        with launch(binary, directory, authority) as fresh:
            _, new_key = check_metadata(fresh)
            assert fresh["issuer"] == ready["issuer"]
            assert "test_id_token" not in fresh and "test_id_token" not in get(authority, META_PATH)[2].decode()
            assert new_key["kid"] != key["kid"] and not verifies(token, new_key)
        assert not list(Path(directory).iterdir()), "fixture persisted state or an operator token"
    print("PASS isolated native Entra OIDC verification fixture: actual local metadata/JWKS, known synthetic signature, refused boundaries, key rotation and clean exit; no login/SAML.")


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("Run without Python optimization")
    check(str(Path(os.environ["SIMULATOR_BINARY"]).resolve(strict=True)))
