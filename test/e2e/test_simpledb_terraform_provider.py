from hashlib import sha256
from pathlib import Path
import json
from tempfile import TemporaryDirectory
from unittest.mock import patch
import unittest
from urllib.parse import urlencode

from simpledb_terraform_provider import (
    BASE, TF, PROVIDER, VERSION, approved_simulator_digest, decode_request, fixture, native, run,
    verify_copy, verify_unauthenticated_metadata,
)


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

    def test_artifact_and_fixture_pins(self):
        d = fixture(51723, self.domain)
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
