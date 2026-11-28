"""Verify reviewed inputs and run the fixed SimpleDB fixture on a disposable runner."""
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import socket
import subprocess
import sys
import tarfile
import time
import zipfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
TESTS = ROOT / "test/e2e"
FIXTURE = TESTS / "simpledb_terraform_provider.py"
FIXTURE_HASH = "e3084f3d4005fc97fe4045c052b6aedc2f7bf9ec476ed0504ec14c7da6969f28"
RECEIPT = HERE / "reviewed-build-receipt.json"
RECEIPT_HASH = "621968a82fd44f087c60a2d66ca29a53463613b1b7e076c966bf9b68e98e0f53"
REVIEW_HASH = "8fb0c90392fd68f819003ebbdcb19f9453d6fd133d3f01687cffc33925e5f1ab"
REPO = "susumutomita/TenkaCloudSimulator"
BUILD_RUN = 46108834461
ARTIFACT = 15249080577
SUCCESS = "SimpleDB official provider apply/metadata refresh/destroy and persisted state observed on loopback"


def require(condition, reason):
    if not condition:
        raise RuntimeError(reason)


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def review():
    path = HERE / "independent-review.json"
    require(digest(RECEIPT) == RECEIPT_HASH and digest(path) == REVIEW_HASH, "independent evidence mismatch")
    approved = json.loads(RECEIPT.read_bytes())
    reviewed = json.loads(path.read_bytes())
    require(approved["review_ref"] == "sha256:" + REVIEW_HASH and
            approved["binary_sha256"] == reviewed["binarySHA256"] and
            approved["source_commit"] == reviewed["sourceRevision"], "independent records disagree")
    return reviewed


def clean_identity():
    require(not any(key.startswith(("AWS_", "BOTO_")) for key in os.environ),
            "runner has existing AWS/Boto environment configuration")
    candidates = (Path.home() / ".aws", Path.home() / ".boto", Path("/etc/boto.cfg"), Path("/etc/aws"))
    require(not any(os.path.lexists(path) for path in candidates), "runner has existing AWS/Boto configuration")
    require("GH_TOKEN" not in os.environ and "GITHUB_TOKEN" not in os.environ,
            "source download credentials exposed to fixture stage")


def preflight():
    require(os.getuid() == os.geteuid() > 0 and sys.platform == "linux" and platform.machine() == "x86_64",
            "nonroot Linux/amd64 required")
    require(sys.version_info[:3] == (3, 12, 16) and __debug__, "unoptimized Python 3.12.16 required")
    require(digest(FIXTURE) == FIXTURE_HASH, "bound signed-form fixture differs")
    clean_identity()


def extract_verified(stream, dest, size, sha):
    # Keep input files non-executable. Only the stock fixture makes its private copies executable.
    with dest.open("xb") as output:
        shutil.copyfileobj(stream, output)
    require(dest.stat().st_size == size and digest(dest) == sha, "executable member differs from review")


def check_build_identity(build, artifact, reviewed):
    require(build["id"] == BUILD_RUN and build["run_attempt"] == 1 and build["conclusion"] == "success" and
            build["status"] == "completed" and build["event"] == "workflow_dispatch" and
            build["path"] == ".github/workflows/ci.yml" and build["head_sha"] == reviewed["workflowRevision"] and
            build["repository"]["full_name"] == build["head_repository"]["full_name"] == REPO,
            "unexpected historical Actions run")
    producing_run = artifact["workflow_run"]
    require(artifact["id"] == ARTIFACT and artifact["name"] == "historical-simulator-d95f8db-46108834461-1" and
            artifact["expired"] is False and artifact["digest"] == reviewed["zipSHA256"] and
            producing_run["id"] == BUILD_RUN and producing_run["head_sha"] == reviewed["workflowRevision"] and
            producing_run["repository_id"] == producing_run["head_repository_id"] == 1297812125,
            "unexpected historical Actions artifact")


def prepare_simulator(inputs, reviewed):
    artifact = json.loads((inputs / "artifact.json").read_bytes())
    check_build_identity(json.loads((inputs / "build-run.json").read_bytes()), artifact, reviewed)
    archive = inputs / "historical-build.zip"
    require(archive.stat().st_size == artifact["size_in_bytes"] == 17683993 and
            "sha256:" + digest(archive) == reviewed["zipSHA256"], "Actions ZIP differs from review")
    with zipfile.ZipFile(archive) as outer:
        evidence = json.loads(outer.read("build-evidence.json"))
        for key in ("sourceRepository", "sourceRevision", "sourceTree", "toolchain", "binarySHA256",
                    "workflowRevision", "workflowRun"):
            require(evidence[key] == reviewed[key], "build provenance mismatch")
        compressed = outer.read("simulator-linux-amd64.tar.gz")
    require(hashlib.sha256(compressed).hexdigest() == evidence["archiveSHA256"] == reviewed["tarGzipSHA256"],
            "historical inner tarball differs from review")
    with tarfile.open(fileobj=io.BytesIO(compressed), mode="r:gz") as tar:
        members = tar.getmembers()
        require(len(members) == 1 and members[0].name == "tenkacloud-simulator" and members[0].isfile() and
                members[0].size == reviewed["binaryBytes"], "unexpected historical binary archive")
        with tar.extractfile(members[0]) as stream:
            extract_verified(stream, inputs / "simulator", reviewed["binaryBytes"], reviewed["binarySHA256"])


def prepare_release(inputs, release):
    stem = release["name"] + "_" + release["version"]
    manifest = inputs / (stem + "_SHA256SUMS")
    archive = inputs / (stem + "_linux_amd64.zip")
    require(digest(manifest) == release["manifestSHA256"], "manifest differs from signed HashiCorp manifest")
    entries = [line.split() for line in manifest.read_text("ascii").splitlines()]
    require([parts[0] for parts in entries if parts[1:] == [archive.name]] == [release["zipSHA256"]] and
            digest(archive) == release["zipSHA256"], "release ZIP differs from signed manifest")
    with zipfile.ZipFile(archive) as release_zip:
        member = release_zip.getinfo(release["member"])
        require(member.file_size == release["binaryBytes"], "unexpected release member size")
        with release_zip.open(member) as stream:
            extract_verified(stream, inputs / release["member"], release["binaryBytes"], release["binarySHA256"])


def inside(inputs, reviewed, mode, uid, parent_net, parent_user):
    preflight()
    # Reuse the committed stock host isolation checks; this fixture also requires an empty bounding set.
    sys.path.insert(0, str(TESTS))
    import run_s3_multipart_paging as guards

    status = Path("/proc/self/status").read_text()
    guards.validate_inside_context(uid, parent_net, parent_user, uid=os.getuid(), euid=os.geteuid(),
                                   net=guards.namespace_inode("net"), user=guards.namespace_inode("user"),
                                   status=status, interfaces=[name for _, name in socket.if_nameindex()],
                                   lo_up=guards.loopback_up())
    require(re.findall(r"^CapBnd:\s*(\w+)$", status, re.M) == ["0000000000000000"] and
            re.findall(r"^NoNewPrivs:\s*(\d+)$", status, re.M) == ["1"], "unexpected privilege state")
    cli, provider = reviewed["hashicorpReleaseVerification"]
    fixture_temp = inputs / ("fixture-temp" if mode == "inside" else "diagnostic-temp")
    fixture_temp.mkdir(mode=0o700)
    options = {
        "TMPDIR": str(fixture_temp),
        "TCSIM_RUN_SIMPLEDB_TERRAFORM": reviewed["sourceRevision"],
        "TCSIM_APPROVED_SIMULATOR_RECEIPT": str(RECEIPT),
        "TCSIM_APPROVED_SIMULATOR_RECEIPT_SHA256": RECEIPT_HASH,
        "SIMULATOR_BINARY": str(inputs / "simulator"),
        "SIMULATOR_BINARY_SHA256": reviewed["binarySHA256"],
        "TCSIM_TERRAFORM": str(inputs / cli["member"]),
        "TCSIM_TERRAFORM_SHA256": cli["binarySHA256"],
        "TCSIM_TERRAFORM_AWS_PROVIDER": str(inputs / provider["member"]),
        "TCSIM_TERRAFORM_AWS_PROVIDER_SHA256": provider["binarySHA256"],
    }
    argv = [sys.executable, "-IB", str(FIXTURE)]
    if mode == "diagnose":
        argv = [sys.executable, "-IB", str(HERE / "observe.py")]
    started = time.monotonic()
    result = subprocess.run(argv, capture_output=True, env=os.environ | options, timeout=690)
    elapsed = round(time.monotonic() - started, 3)
    cleaned = not any(fixture_temp.iterdir())
    if cleaned:
        fixture_temp.rmdir()
    if mode == "diagnose":
        # An observer result cannot turn a failed direct run into an acceptance pass.
        path = inputs.parent / "simpledb-verdict" / "diagnostic.json"
        require(path.is_file(), "separate observer did not record a diagnostic")
        report = json.loads(path.read_bytes())
        report.update(processExit=result.returncode, workDirectoryRemoved=cleaned, durationSeconds=elapsed)
        path.write_text(json.dumps(report, indent=2) + "\n")
        require(cleaned, "diagnostic work directory cleanup failed")
        return
    verdict = {
        "workflowRevision": os.environ["GITHUB_SHA"], "fixtureRevision": os.environ["FIXTURE_REVISION"],
        "fixtureSHA256": FIXTURE_HASH, "simulatorSource": reviewed["sourceRevision"],
        "sourceTree": reviewed["sourceTree"], "buildArtifact": reviewed["artifactURL"],
        "artifactSHA256": reviewed["zipSHA256"], "tarGzipSHA256": reviewed["tarGzipSHA256"],
        "simulatorSHA256": reviewed["binarySHA256"], "independentReviewSHA256": REVIEW_HASH,
        "reviewedReceiptSHA256": RECEIPT_HASH, "terraformSHA256": cli["binarySHA256"],
        "providerSHA256": provider["binarySHA256"], "python": platform.python_version(),
        "isolation": "separate user and network namespaces, same nonroot UID, loopback only, no capabilities, no_new_privs",
        "fixtureExit": result.returncode, "workDirectoryRemoved": cleaned, "durationSeconds": elapsed,
    }
    dest = inputs.parent / "simpledb-verdict" / "fixture.json"
    if not cleaned or result.returncode or result.stdout.splitlines() != [SUCCESS.encode("ascii")]:
        reason = "stock fixture or cleanup did not complete"
        # Use only known stock errors or a fixed Terraform command and its numeric exit code.
        command = re.search(rb"Terraform (init|apply|plan|destroy|show|version) failed \((-?\d+)\)", result.stderr)
        if command:
            reason = command.group().decode("ascii")
        for text in ("unauthenticated local metadata probe did not return HTTP 401", "fixture create not observed",
                     "provider metadata read not observed", "fixture delete not observed",
                     "metadata did not confirm deletion", "Terraform state does not match the live fixture domain"):
            if b"RuntimeError: " + text.encode("ascii") in result.stderr.splitlines():
                reason = text
        frames = re.findall(rb'(?m)^\s*File "[^"\n]*?/simpledb_terraform_provider\.py", line (\d{1,3}), in (\w+|<module>)$', result.stderr)
        allowed = {"<module>", "run", "terraform", "need", "native", "simulator", "adapter", "verify_terraform_state",
                   "verify_unauthenticated_metadata", "verify_copy", "approved_simulator_digest"}
        verdict["stockFrames"] = [{"line": int(line), "function": name.decode("ascii")} for line, name in frames
                                  if name.decode("ascii") in allowed][:12]
        last_line = result.stderr.splitlines()[-1] if result.stderr else b""
        error = re.match(rb"(subprocess\.TimeoutExpired|RuntimeError|TimeoutError|OSError|ValueError|AssertionError|ConnectionRefusedError):", last_line)
        verdict["exceptionClass"] = error[1].decode("ascii") if error else "unclassified"
        if verdict["exceptionClass"] == "subprocess.TimeoutExpired":
            phase = re.search(rb"/terraform', '(version|init|apply|show|plan|destroy)'", last_line)
            reason = "stock Terraform command timed out" if phase else "stock subprocess timed out"
            if phase:
                verdict["terraformPhase"] = phase[1].decode("ascii")
        verdict["failure"] = reason
        dest.write_text(json.dumps(verdict, indent=2) + "\n")
        raise RuntimeError(reason)
    verdict.update({
        "stockResult": SUCCESS,
        "stockAssertions": {"CreateDomain": 200, "DomainMetadataRefresh": 200, "DeleteDomain": 200,
                            "unauthenticatedMetadata": 401, "metadataAfterDestroy": "400 NoSuchDomain",
                            "terraformStateAfterApply": "one matching domain", "terraformStateAfterDestroy": "empty"},
    })
    dest.write_text(json.dumps(verdict, indent=2) + "\n")
    print(SUCCESS)


if __name__ == "__main__":
    inputs = Path(os.environ["RUNNER_TEMP"]) / "simpledb-inputs"
    reviewed = review()
    if sys.argv[1:] == ["preflight"]:
        preflight()
    elif sys.argv[1:] == ["prepare"]:
        prepare_simulator(inputs, reviewed)
        for release in reviewed["hashicorpReleaseVerification"]:
            prepare_release(inputs, release)
    elif len(sys.argv) == 5 and sys.argv[1] in {"inside", "diagnose"}:
        inside(inputs, reviewed, sys.argv[1], *(int(arg) for arg in sys.argv[2:]))
    else:
        sys.exit("unexpected arguments")
