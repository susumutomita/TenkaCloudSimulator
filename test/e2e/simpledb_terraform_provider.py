"""Opt-in Linux loopback acceptance: official AWS Terraform provider 5.100.0.

Run only in a dedicated network namespace containing an enabled 'lo' and no
other interfaces, with separately authorized and provenance-verified executable
artifacts. No installers, external endpoints, real credentials, or shared state.
This does not test direct AWS SigV4 authentication or implement an AWS proxy.
"""
from contextlib import contextmanager
from hashlib import file_digest, sha256
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import json
import os
import re
import shutil
import socket
import subprocess
import tempfile
import threading
import time
from urllib.parse import parse_qsl, urlencode
from uuid import uuid4
from xml.etree import ElementTree

BASE = "d95f8dbc3e6ddec4c65bc9d5e0124616a0e591da"
TF = "1.11.4"
PROVIDER = "5.100.0"
ADDR = "127.0.0.1"
VERSION = "2009-04-15"
LIMIT = 32768
XMLNS = "{http://sdb.amazonaws.com/doc/2009-04-15/}"
STATS = {"ItemCount", "ItemNamesSizeBytes", "AttributeNameCount",
         "AttributeNamesSizeBytes", "AttributeValueCount", "AttributeValuesSizeBytes"}
ACTIONS = {"CreateDomain", "DomainMetadata", "DeleteDomain"}


def need(condition, message):
    if not condition:
        raise RuntimeError(message)


def decode_request(method, path, content_type, length, raw, domain):
    need(method == "POST" and path == "/", "unexpected method or path")
    need(content_type.split(";", 1)[0].strip().lower() == "application/x-www-form-urlencoded",
         "unexpected content type")
    need(length.isascii() and length.isdecimal() and 0 < len(length) <= 5,
         "invalid Content-Length")
    need(0 < int(length) == len(raw) <= LIMIT, "unexpected body size")
    pairs = parse_qsl(raw.decode("ascii"), strict_parsing=True, max_num_fields=3,
                      encoding="utf-8", errors="strict")
    data = dict(pairs)
    need(len(pairs) == len(data) == 3 and set(data) == {"Action", "Version", "DomainName"},
         "unexpected or duplicate query fields")
    need(data["Action"] in ACTIONS and data["Version"] == VERSION and data["DomainName"] == domain,
         "outside fixed fixture scope")
    return data["Action"]


def native(port, token, path="/", data=None):
    conn = HTTPConnection(ADDR, port, timeout=5)
    try:
        headers = {"X-Simulator-Service": "sdb"}
        if token is not None:
            headers["X-Simulator-Token"] = token
        if data is not None:
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        conn.request("POST" if data is not None else "GET", path, body=data, headers=headers)
        response = conn.getresponse()
        need(not 300 <= response.status < 400, "no upstream redirects")
        body = response.read(LIMIT + 1)
        need(len(body) <= LIMIT, "unexpectedly large fixture response")
        return response.status, body
    finally:
        conn.close()


@contextmanager
def adapter(upstream_port, operator_token, domain):
    receipts = []
    failures = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            try:
                lengths = self.headers.get_all("Content-Length") or []
                types = self.headers.get_all("Content-Type") or []
                need(len(lengths) == len(types) == 1 and not self.headers.get_all("Transfer-Encoding"),
                     "invalid framing")
                length = lengths[0]
                need(length.isascii() and length.isdecimal() and len(length) <= 5
                     and 0 < int(length) <= LIMIT, "invalid Content-Length")
                self.connection.settimeout(5)
                body = self.rfile.read(int(length))
                action = decode_request(self.command, self.path, types[0], length, body, domain)
                # The local simulator authenticates a test capability, not AWS signing.
                # Forward exactly the validated form; no inbound headers or endpoint options.
                status, xml = native(upstream_port, operator_token, data=body)
                if status == 200 and action == "DomainMetadata":
                    result = ElementTree.fromstring(xml).find(XMLNS + "DomainMetadataResult")
                    need(result is not None and all(result.findtext(XMLNS + stat) == "0" for stat in STATS),
                         "missing zero counters for newly created domain")
                    timestamp = result.findtext(XMLNS + "Timestamp") or ""
                    need(timestamp.isascii() and timestamp.isdecimal(), "invalid metadata timestamp")
                receipts.append((action, status))
                self.send_response(status)
                self.send_header("Content-Type", "text/xml; charset=utf-8")
                self.send_header("Content-Length", str(len(xml)))
                self.end_headers()
                self.wfile.write(xml)
            except (ValueError, RuntimeError, OSError, ElementTree.ParseError):
                failures.append("rejected request")
                self.send_error(400, "Out of scope or invalid fixture response")

        def send_error(self, code, message=None, explain=None):
            # Base methods such as GET, CONNECT and OPTIONS never reach upstream.
            if code == 501:
                failures.append("unsupported method")
            super().send_error(code, message=message, explain=explain)

    with HTTPServer((ADDR, 0), Handler) as server:
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            yield server.server_port, receipts, failures
        finally:
            server.shutdown()
            worker.join(timeout=5)


def verify_unauthenticated_metadata(port, domain):
    # Ordinary auth refusal on the exact local origin, without a token.
    # The harmless read probe does not create or remove any state.
    refused, _ = native(port, None, data=urlencode(
        {"Action": "DomainMetadata", "Version": VERSION, "DomainName": domain}).encode("ascii"))
    need(refused == 401, "unauthenticated local metadata probe did not return HTTP 401")


def verify_terraform_state(payload, *, domain):
    """Verify the state of this single fixed resource, never print the state."""
    def unique(pairs):
        value = dict(pairs)
        need(len(value) == len(pairs), "duplicate Terraform state fields")
        return value
    try:
        state = json.loads(payload, object_pairs_hook=unique)
    except (ValueError, UnicodeError) as err:
        raise RuntimeError("invalid Terraform state JSON") from err
    need(type(state) is dict and state.get("format_version") == "1.0",
         "unexpected Terraform state JSON format")
    if domain is None:
        # Terraform 1.11.4 jsonstate returns only the format for empty state.
        need(set(state) == {"format_version"}, "Terraform state is not empty after destroy")
        return
    need(state.get("terraform_version") == TF, "unexpected Terraform state writer version")
    values = state.get("values")
    module = values.get("root_module") if type(values) is dict else None
    need(type(module) is dict and not module.get("child_modules"), "unexpected Terraform state module")
    entries = module.get("resources")
    need(type(entries) is list and len(entries) == 1 and type(entries[0]) is dict,
         "Terraform state must contain exactly one fixture resource")
    resource = entries[0]
    need(all(resource.get(key) == value for key, value in {
         "address": "aws_simpledb_domain.fixture", "mode": "managed",
         "type": "aws_simpledb_domain", "name": "fixture",
         "provider_name": "registry.terraform.io/hashicorp/aws",
         "values": {"id": domain, "name": domain},
         }.items()) and not any(key in resource for key in ("index", "tainted", "deposed_key")),
         "Terraform state does not match the live fixture domain")


def approved_simulator_digest():
    """Bind bytes to a separately reviewed receipt, never infer provenance from a hash."""
    expected_receipt_sha = os.environ.get("TCSIM_APPROVED_SIMULATOR_RECEIPT_SHA256", "")
    need(re.fullmatch(r"[a-f0-9]{64}", expected_receipt_sha), "approved receipt digest required")
    receipt_path = Path(os.environ["TCSIM_APPROVED_SIMULATOR_RECEIPT"])
    with receipt_path.open("rb") as stream:
        raw = stream.read(4097)
    need(len(raw) <= 4096, "oversize reviewed provenance receipt")
    need(sha256(raw).hexdigest() == expected_receipt_sha, "reviewed receipt digest mismatch")
    def unique(pairs):
        data = dict(pairs)
        need(len(data) == len(pairs), "duplicate provenance receipt fields")
        return data
    receipt = json.loads(raw, object_pairs_hook=unique)
    need(type(receipt) is dict and set(receipt) == {
        "schema", "source_commit", "platform", "binary_sha256", "review_ref",
    }, "reviewed receipt shape mismatch")
    need(receipt["schema"] == "tcsim-reviewed-build-v1" and
         receipt["source_commit"] == BASE and receipt["platform"] == "linux_amd64",
         "receipt does not identify designated exact-source simulator binary")
    need(type(receipt["review_ref"]) is str and
         re.fullmatch(r"[A-Za-z0-9:/._-]{1,180}", receipt["review_ref"]), "independent review reference required")
    digest = receipt["binary_sha256"]
    need(type(digest) is str and re.fullmatch(r"[a-f0-9]{64}", digest) and
         digest == os.environ.get("SIMULATOR_BINARY_SHA256"), "simulator digest differs from reviewed receipt")
    return digest


def verify_copy(name, dest, *, receipt_digest=None):
    expected = os.environ.get(name + "_SHA256", "")
    need(re.fullmatch(r"[a-f0-9]{64}", expected), f"{name} needs trusted SHA256")
    if receipt_digest is not None:
        need(expected == receipt_digest, "executable digest differs from reviewed receipt")
    source = Path(os.environ[name]).resolve(strict=True)
    need(source.is_file(), f"{name} must be a regular executable artifact")
    shutil.copyfile(source, dest)
    with dest.open("rb") as stream:
        need(file_digest(stream, "sha256").hexdigest() == expected, f"{name} hash mismatch")
    dest.chmod(0o700)


@contextmanager
def simulator(binary, root, env):
    with socket.socket() as reserve:
        reserve.bind((ADDR, 0))
        port = reserve.getsockname()[1]
    file = root / "operator-token"
    with subprocess.Popen([str(binary), "--listen", f"{ADDR}:{port}", "--token-file", str(file)],
                          cwd=root, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as child:
        try:
            for _ in range(100):
                need(child.poll() is None, "fixture simulator stopped during launch")
                if file.is_file():
                    token = file.read_text().strip()
                    need(bool(token) and "\n" not in token, "invalid test token")
                    try:
                        if native(port, token, path="/_simulator/health")[0] == 200:
                            break
                    except OSError:
                        pass
                time.sleep(0.1)
            else:
                raise RuntimeError("authenticated simulator failed to become ready")
            yield port, token
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
                    raise


def fixture(port, domain):
    return {
        "terraform": {"required_version": "= " + TF, "required_providers":
                      {"aws": {"source": "hashicorp/aws", "version": "= " + PROVIDER}}},
        "provider": {"aws": {
            "region": "us-east-1", "access_key": "TCSIMTESTDUMMYONLY", "secret_key": "simulator-only",
            "skip_credentials_validation": True, "skip_requesting_account_id": True,
            "skip_metadata_api_check": True, "skip_region_validation": True, "max_retries": 0,
            "endpoints": {"simpledb": f"http://{ADDR}:{port}"},
        }},
        "resource": {"aws_simpledb_domain": {"fixture": {"name": domain}}},
    }


def run():
    need(os.environ.get("TCSIM_RUN_SIMPLEDB_TERRAFORM") == BASE, "exact candidate opt-in required")
    need(os.uname().sysname == "Linux" and os.uname().machine == "x86_64", "pinned linux_amd64 only")
    need({name for _, name in socket.if_nameindex()} == {"lo"}, "dedicated loopback-only namespace required")
    domain = "tf-local-" + uuid4().hex
    with tempfile.TemporaryDirectory(prefix="tcsim-simpledb-tf-") as tmp:
        root = Path(tmp)
        home = root / "home"
        home.mkdir()
        cli = root / "terraform"
        binary = root / "simulator"
        mirror = root / "mirror"
        plugin_dir = mirror / "registry.terraform.io/hashicorp/aws" / PROVIDER / "linux_amd64"
        plugin_dir.mkdir(parents=True)
        verify_copy("TCSIM_TERRAFORM", cli)
        verify_copy("TCSIM_TERRAFORM_AWS_PROVIDER", plugin_dir / ("terraform-provider-aws_v" + PROVIDER + "_x5"))
        verify_copy("SIMULATOR_BINARY", binary, receipt_digest=approved_simulator_digest())
        config = root / "terraform.rc"
        config.write_text('disable_checkpoint = true\nprovider_installation {\n  filesystem_mirror {\n    path = '
                          + json.dumps(str(mirror)) + '\n    include = ["registry.terraform.io/hashicorp/aws"]\n  }\n}\n')
        env = {"HOME": str(home), "PATH": "/nonexistent", "TF_CLI_CONFIG_FILE": str(config),
               "TF_IN_AUTOMATION": "1", "CHECKPOINT_DISABLE": "1", "AWS_EC2_METADATA_DISABLED": "true",
               "AWS_CONFIG_FILE": "/dev/null", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null"}

        def terraform(*args, expected=0):
            child = subprocess.run([str(cli), *args], cwd=root, env=env, capture_output=True, timeout=90)
            need(child.returncode == expected, f"Terraform {args[0]} failed ({child.returncode})")
            return child.stdout

        info = json.loads(terraform("version", "-json"))
        need(info.get("terraform_version") == TF and info.get("platform") == "linux_amd64",
             "unexpected CLI version or platform")
        with simulator(binary, root, env) as (port, token), adapter(port, token, domain) as (bridge, calls, errors):
            verify_unauthenticated_metadata(port, domain)
            (root / "main.tf.json").write_text(json.dumps(fixture(bridge, domain)))
            terraform("init", "-input=false", "-no-color")
            terraform("apply", "-input=false", "-auto-approve", "-no-color")
            need((root / "terraform.tfstate").is_file(), "apply did not persist local Terraform state")
            verify_terraform_state(terraform("show", "-json", "terraform.tfstate"), domain=domain)
            need(("CreateDomain", 200) in calls and not errors, "fixture create not observed")
            before = len(calls)
            terraform("plan", "-input=false", "-detailed-exitcode", "-no-color")
            need(("DomainMetadata", 200) in calls[before:] and not errors, "provider metadata read not observed")
            before = len(calls)
            terraform("destroy", "-input=false", "-auto-approve", "-no-color")
            need((root / "terraform.tfstate").is_file(), "destroy did not persist local Terraform state")
            verify_terraform_state(terraform("show", "-json", "terraform.tfstate"), domain=None)
            need(("DeleteDomain", 200) in calls[before:] and not errors, "fixture delete not observed")
            code, xml = native(port, token, data=urlencode(
                {"Action": "DomainMetadata", "Version": VERSION, "DomainName": domain}).encode("ascii"))
            body = ElementTree.fromstring(xml)
            need(code == 400 and any(x.text == "NoSuchDomain" for x in body.iter()
                                     if x.tag.rsplit("}", 1)[-1] == "Code"),
                 "metadata did not confirm deletion")
    print("SimpleDB official provider apply/metadata refresh/destroy and persisted state observed on loopback")


if __name__ == "__main__":
    run()
