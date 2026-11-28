from copy import deepcopy
from hashlib import sha256
from io import BytesIO
from pathlib import Path
import json
from tempfile import TemporaryDirectory
from unittest.mock import Mock, patch
import unittest
from urllib.parse import urlencode

from simpledb_terraform_provider import (
    BASE, TF, PROVIDER, VERSION, FIXTURE_KEY, FIXTURE_SECRET, adapter, approved_simulator_digest,
    decode_request, fixture, native, run, verify_copy, verify_terraform_state, verify_unauthenticated_metadata,
)


# Independently generated with the pinned Go SDK's SigV2 canonical form at a fixed, expired timestamp.
REFERENCE_TIME = 1767323045
REFERENCE_PORT = 51723
REFERENCE_SIGNATURES = {
    "CreateDomain": "hC9N212haHVk/c4XEMDloiLNtqnvFzIjDKlfMDC3fhA=",
    "DeleteDomain": "zooCvn9DM99lOGqrXD5p7rytyOT5SFCkdMYC7iLpICQ=",
    "DomainMetadata": "MKzfikocQqDr4ZN4OQKnWxwUNeXVg53wTnWoSgTmti4=",
}


def signed_fixture_form(action):
    return {"AWSAccessKeyId": "TCSIMTESTDUMMYONLY", "Action": action, "DomainName": "tf-local-fixed",
            "SignatureVersion": "2", "SignatureMethod": "HmacSHA256", "Timestamp": "2026-01-02T03:04:05Z",
            "Version": VERSION, "Signature": REFERENCE_SIGNATURES[action]}


class AcceptanceBoundaries(unittest.TestCase):
    domain = "tf-local-fixed"

    def decode(self, values, **extra):
        data = urlencode(values).encode("ascii")
        request = dict(method="POST", path="/", content_type="application/x-www-form-urlencoded",
                       length=str(len(data)), raw=data, domain=self.domain)
        request.update(extra)
        return decode_request(**request)

    def test_only_fixed_calls(self):
        base = [("Action", "DomainMetadata"), ("Version", VERSION), ("DomainName", self.domain)]
        for action in ("DomainMetadata", "CreateDomain", "DeleteDomain"):
            self.assertEqual(self.decode([("Action", action), *base[1:]]), action)
        for request in [dict(path="//example.invalid"), dict(path="/?Action=ListDomains"),
                        dict(method="GET"), dict(length="1"), dict(length="1" * 1000),
                        dict(content_type="application/json"), dict(domain="different")]:
            with self.subTest(request=request), self.assertRaises(RuntimeError):
                self.decode(base, **request)
        for fields in [[("Action", "ListDomains"), *base[1:]], [*base, base[1]],
                       [("Action", "DomainMetadata"), ("Version", VERSION), ("Version", VERSION)],
                       [base[0], ("Version", "2010-01-01"), base[-1]]]:
            with self.subTest(fields=fields), self.assertRaises((RuntimeError, ValueError)):
                self.decode(fields)

    @patch("simpledb_terraform_provider.time.time", return_value=REFERENCE_TIME)
    def test_pinned_provider_signature_and_refusals(self, clock):
        def decode(parts, **overrides):
            return self.decode(sorted(parts), listener_port=REFERENCE_PORT, **overrides)

        for action in REFERENCE_SIGNATURES:
            self.assertEqual(decode(signed_fixture_form(action).items()), action)
        signed = signed_fixture_form("CreateDomain")
        for additions in ({"Extra": ""}, {"SecurityToken": "other"}, {"AWSAccessKeyId": "other"},
                          {"SignatureVersion": "4"}, {"SignatureMethod": "HmacSHA1"}, {"Signature": ""},
                          {"Signature": REFERENCE_SIGNATURES["DeleteDomain"]}, {"Action": "ListDomains"},
                          {"Version": "other"}, {"DomainName": "other"}, {"Timestamp": "2026-1-2T03:04:05Z"}):
            with self.subTest(fields=list(additions)), self.assertRaises((RuntimeError, ValueError)):
                decode((signed | additions).items())
        for key in signed:
            with self.subTest(missing=key), self.assertRaises((RuntimeError, ValueError)):
                decode((field, value) for field, value in signed.items() if field != key)
            with self.subTest(duplicate=key), self.assertRaises((RuntimeError, ValueError)):
                decode([*signed.items(), (key, signed[key])])
        with self.assertRaises(RuntimeError):
            self.decode(sorted(signed.items()), listener_port=REFERENCE_PORT + 1)
        for changes in ({"method": "GET"}, {"path": "/other"}, {"length": "999999"},
                        {"content_type": "application/json"}):
            with self.subTest(framing=list(changes)), self.assertRaises((RuntimeError, ValueError)):
                decode(signed.items(), **changes)
        for offset in (-301, 301):
            clock.return_value = REFERENCE_TIME + offset
            with self.assertRaisesRegex(RuntimeError, "signing time"):
                decode(signed.items())

    @patch("simpledb_terraform_provider.time.time", return_value=REFERENCE_TIME)
    def test_adapter_requires_signed_loopback_and_rebuilds_native_form(self, _clock):
        signed = urlencode(sorted(signed_fixture_form("CreateDomain").items())).encode("ascii")
        plain = urlencode({"Action": "CreateDomain", "Version": VERSION, "DomainName": self.domain}).encode("ascii")
        host = f"Host: 127.0.0.1:{REFERENCE_PORT}"
        with patch("simpledb_terraform_provider.HTTPServer") as constructor, \
                patch("simpledb_terraform_provider.native", return_value=(200, b"<result/>")) as upstream:
            server = constructor.return_value.__enter__.return_value
            server.server_port = REFERENCE_PORT
            with adapter(51724, "private-fixture-token", self.domain) as (_, calls, failures):
                handler = constructor.call_args.args[1]

                def post(headers, form):
                    req = Mock()
                    framing = ["POST / HTTP/1.0", *headers, "Content-Type: application/x-www-form-urlencoded",
                               f"Content-Length: {len(form)}", "", ""]
                    req.makefile.return_value = BytesIO("\r\n".join(framing).encode("ascii") + form)
                    handler(req, ("127.0.0.1", 50000), server)

                for headers, form in (([], signed), ([host, host], signed), (["Host: localhost:51723"], signed),
                                      ([host, "Authorization: other"], signed), ([host], plain)):
                    with self.subTest(headers=len(headers), signed=form == signed):
                        post(headers, form)
                        upstream.assert_not_called()
                self.assertEqual(len(failures), 5)
                post([host, "X-Extra-Test-Header: discard"], signed)
                upstream.assert_called_once_with(51724, "private-fixture-token", data=plain)
                self.assertEqual(calls, [("CreateDomain", 200)])

    def test_forwarding_and_redirect_refusal(self):
        with patch("simpledb_terraform_provider.HTTPConnection") as conn:
            mock = conn.return_value
            mock.getresponse.return_value.status = 200
            mock.getresponse.return_value.read.return_value = b"<xml/>"
            data = b"Action=DomainMetadata"
            self.assertEqual(native(19555, "ephemeral", data=data), (200, b"<xml/>"))
            conn.assert_called_once_with("127.0.0.1", 19555, timeout=5)
            mock.request.assert_called_once_with("POST", "/", body=data, headers={
                "X-Simulator-Token": "ephemeral", "X-Simulator-Service": "sdb",
                "Content-Type": "application/x-www-form-urlencoded",
            })
            mock.getresponse.return_value.status = 307
            with self.assertRaisesRegex(RuntimeError, "redirect"):
                native(19555, "ephemeral", data=data)

    def test_no_token_auth_diagnostic(self):
        with patch("simpledb_terraform_provider.native", return_value=(401, b"refused")) as up:
            verify_unauthenticated_metadata(51723, self.domain)
            up.assert_called_once_with(51723, None, data=urlencode({
                "Action": "DomainMetadata", "Version": VERSION, "DomainName": self.domain,
            }).encode("ascii"))
            up.return_value = (200, b"unexpected")
            with self.assertRaisesRegex(RuntimeError, "401"):
                verify_unauthenticated_metadata(51723, self.domain)
        with patch("simpledb_terraform_provider.HTTPConnection") as conn:
            mock = conn.return_value
            mock.getresponse.return_value.status = 401
            mock.getresponse.return_value.read.return_value = b"refused"
            self.assertEqual(native(51723, None, data=b"fixed"), (401, b"refused"))
            self.assertNotIn("X-Simulator-Token", mock.request.call_args.kwargs["headers"])

    def test_review_receipt_is_required_and_bound(self):
        with TemporaryDirectory() as tmp:
            receipt = Path(tmp) / "reviewed.json"
            expected = "a" * 64
            data = {"schema": "tcsim-reviewed-build-v1", "source_commit": BASE,
                    "platform": "linux_amd64", "binary_sha256": expected,
                    "review_ref": "approved/build-123"}
            def write(obj):
                raw = json.dumps(obj).encode()
                receipt.write_bytes(raw)
                return sha256(raw).hexdigest()

            env = {"TCSIM_APPROVED_SIMULATOR_RECEIPT": str(receipt),
                   "TCSIM_APPROVED_SIMULATOR_RECEIPT_SHA256": write(data),
                   "SIMULATOR_BINARY_SHA256": expected}
            with patch.dict("os.environ", env, clear=True):
                self.assertEqual(approved_simulator_digest(), expected)
                write(dict(data, source_commit="b" * 40))
                with self.assertRaisesRegex(RuntimeError, "receipt digest mismatch"):
                    approved_simulator_digest()
            for key, invalid, message in (
                ("source_commit", "b" * 40, "exact-source"),
                ("binary_sha256", "b" * 64, "differs from reviewed"),
                ("platform", "darwin_arm64", "exact-source"),
                ("review_ref", "", "review reference"),
            ):
                new = dict(env, TCSIM_APPROVED_SIMULATOR_RECEIPT_SHA256=write(dict(data, **{key: invalid})))
                with self.subTest(key=key), patch.dict("os.environ", new, clear=True):
                    with self.assertRaisesRegex(RuntimeError, message):
                        approved_simulator_digest()

    def test_exact_opt_in_and_no_network_fallback(self):
        with patch.dict("os.environ", {}, clear=True), self.assertRaisesRegex(RuntimeError, "opt-in"):
            run()
        with patch.dict("os.environ", {"TCSIM_RUN_SIMPLEDB_TERRAFORM": BASE}, clear=True):
            with patch("simpledb_terraform_provider.os.uname") as uname:
                uname.return_value.sysname = "Linux"
                uname.return_value.machine = "x86_64"
                with patch("simpledb_terraform_provider.socket.if_nameindex",
                           return_value=[(1, "lo"), (2, "eth0")]):
                    with self.assertRaisesRegex(RuntimeError, "loopback-only"):
                        run()

    def test_state_proves_exact_managed_resource_and_empty_destroy(self):
        resource = {"address": "aws_simpledb_domain.fixture", "mode": "managed",
                    "type": "aws_simpledb_domain", "name": "fixture",
                    "provider_name": "registry.terraform.io/hashicorp/aws",
                    "values": {"id": self.domain, "name": self.domain}}
        created = {"format_version": "1.0", "terraform_version": TF,
                   "values": {"root_module": {"resources": [resource]}}}
        empty = {"format_version": "1.0"}
        verify_terraform_state(json.dumps(created), domain=self.domain)
        verify_terraform_state(json.dumps(empty), domain=None)
        for key, wrong in (
            ("address", "aws_simpledb_domain.other"),
            ("provider_name", "terraform.io/builtin/terraform"),
            ("mode", "data"), ("values", {"name": self.domain, "id": "some-other-domain"}),
            ("tainted", True), ("deposed_key", "stale"), ("index", 0),
        ):
            impostor = deepcopy(created)
            impostor["values"]["root_module"]["resources"][0][key] = wrong
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                verify_terraform_state(json.dumps(impostor), domain=self.domain)
        for state, claim in (
            (b'{"format_version":"1.0","format_version":"1.0"}', None),
            (b'{"values":{}}', None), (b"Apply complete!", self.domain),
            (json.dumps(empty), self.domain), (json.dumps(created), None),
            (json.dumps(dict(created, terraform_version="1.12.0")), self.domain),
        ):
            with self.subTest(state=state, claim=claim), self.assertRaises(RuntimeError):
                verify_terraform_state(state, domain=claim)

    def test_only_terraform_receives_the_fixture_signing_pair(self):
        version = json.dumps({"terraform_version": TF, "platform": "linux_amd64"}).encode()
        with (
            patch.dict("os.environ", {"TCSIM_RUN_SIMPLEDB_TERRAFORM": BASE, "AWS_PROFILE": "unused"}, clear=True),
            patch("simpledb_terraform_provider.os.uname", return_value=Mock(sysname="Linux", machine="x86_64")),
            patch("simpledb_terraform_provider.socket.if_nameindex", return_value=[(1, "lo")]),
            patch("simpledb_terraform_provider.verify_copy"),
            patch("simpledb_terraform_provider.approved_simulator_digest"),
            patch("simpledb_terraform_provider.subprocess.run", return_value=Mock(returncode=0, stdout=version)) as cli,
            patch("simpledb_terraform_provider.simulator", side_effect=RuntimeError("simulator handoff")) as server,
        ):
            with self.assertRaisesRegex(RuntimeError, "simulator handoff"):
                run()
        simulator_env = server.call_args.args[2]
        provider_env = cli.call_args.kwargs["env"]
        self.assertNotIn("AWS_PROFILE", provider_env)
        self.assertNotIn("AWS_ACCESS_KEY_ID", simulator_env)
        self.assertNotIn("AWS_SECRET_ACCESS_KEY", simulator_env)
        expected = dict(simulator_env, AWS_ACCESS_KEY_ID=FIXTURE_KEY, AWS_SECRET_ACCESS_KEY=FIXTURE_SECRET)
        self.assertTrue(provider_env == expected, "Unexpected provider process environment")

    def test_artifact_and_fixture_pins(self):
        d = fixture(51723, self.domain)
        serialized = json.dumps(d)
        for signing_field in ('"access_key"', '"secret_key"', FIXTURE_KEY, FIXTURE_SECRET):
            self.assertTrue(signing_field not in serialized, "Signing material appeared in the Terraform configuration")
        self.assertEqual(d["terraform"]["required_version"], "= " + TF)
        self.assertEqual(d["terraform"]["required_providers"]["aws"]["version"], "= " + PROVIDER)
        self.assertEqual(d["provider"]["aws"]["endpoints"], {"simpledb": "http://127.0.0.1:51723"})
        with TemporaryDirectory() as tmp:
            root = Path(tmp)
            artifact = root / "source"
            artifact.write_bytes(b"test")
            with patch.dict("os.environ", {"FAKE": str(artifact), "FAKE_SHA256": "0" * 64}, clear=True):
                with self.assertRaisesRegex(RuntimeError, "hash mismatch"):
                    verify_copy("FAKE", root / "dest")


if __name__ == "__main__":
    unittest.main()
