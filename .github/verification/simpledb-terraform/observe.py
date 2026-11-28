"""Classify a separate failed-run reproduction without changing stock fixture functions."""
from pathlib import Path
import hashlib
import json
import os
import runpy
import subprocess
import sys
import threading
import time

HERE = Path(__file__).resolve().parent
FIXTURE = HERE.parents[2] / "test/e2e/simpledb_terraform_provider.py"
FIXTURE_SHA256 = "e3084f3d4005fc97fe4045c052b6aedc2f7bf9ec476ed0504ec14c7da6969f28"
COMMANDS = {"version", "init", "apply", "show", "plan", "destroy"}
ACTIONS = {"CreateDomain", "DomainMetadata", "DeleteDomain"}
FUNCTIONS = {"terraform", "native", "do_POST", "send_error"}
# Emit only a fixed vocabulary. No Terraform output, bodies, headers, paths, values or command lines are copied.
MARKERS = {
    b"initializing the backend": "backend-init",
    b"initializing provider plugins": "plugin-init",
    b"finding hashicorp/aws versions": "provider-search",
    b"installing hashicorp/aws": "provider-install",
    b"installed hashicorp/aws": "provider-installed",
    b"terraform has been successfully initialized": "init-complete",
    b"terraform used the selected providers": "planned-actions",
    b"aws_simpledb_domain.fixture: creating": "create-started",
    b"aws_simpledb_domain.fixture: still creating": "create-pending",
    b"aws_simpledb_domain.fixture: refreshing state": "refresh-started",
    b"aws_simpledb_domain.fixture: destroying": "delete-started",
    b"aws_simpledb_domain.fixture: still destroying": "delete-pending",
    b"failed to query available provider packages": "provider-query-failed",
    b"failed to install provider": "provider-install-failed",
    b"failed to load plugin schemas": "provider-schema-failed",
    b"failed to load provider schemas": "provider-schema-failed",
    b"unrecognized remote plugin message": "plugin-handshake-failed",
    b"timeout while waiting for plugin": "plugin-start-timeout",
    b"plugin did not respond": "plugin-unresponsive",
    b"incompatible api version": "plugin-version",
    b"error configuring terraform aws provider": "provider-config-failed",
    b"retrieving aws account details": "account-lookup",
    b"validating provider credentials": "credential-validation",
    b"creating simpledb domain": "create-reported-error",
    b"reading simpledb domain": "read-reported-error",
    b"deleting simpledb domain": "delete-reported-error",
    b"context deadline exceeded": "deadline",
    b"network is unreachable": "network-unreachable",
    b"no such host": "dns",
    b"connection refused": "connection-refused",
    b"permission denied": "permission",
    b"operation not permitted": "permission",
    b"cannot assign requested address": "bind-address",
    b"no space left on device": "disk-full",
    b"failed to decode": "decode",
    b"invalid character": "decode",
    b"unsupported argument": "unsupported-argument",
}
CAUSES = {"RuntimeError", "TimeoutExpired", "TimeoutError", "ConnectionRefusedError", "ConnectionResetError",
          "BrokenPipeError", "FileNotFoundError", "PermissionError", "ValueError", "ParseError", "OSError"}
FRAMING = {"invalid framing", "invalid Content-Length", "unexpected method or path", "unexpected content type",
           "unexpected body size", "unexpected or duplicate query fields", "outside fixed fixture scope",
           "missing zero counters for newly created domain", "invalid metadata timestamp",
           "no upstream redirects", "unexpectedly large fixture response", "unexpected fixture authority",
           "unexpected inbound authorization", "unexpected fixture signing identity or algorithm",
           "invalid fixture signing time", "noncanonical signed form", "invalid fixture signature"}


def output_classes(stdout, stderr):
    blob = ((stdout or b"") + b"\n" + (stderr or b"")).lower()
    return sorted({label for marker, label in MARKERS.items() if marker in blob})


def main():
    if hashlib.sha256(FIXTURE.read_bytes()).hexdigest() != FIXTURE_SHA256:
        raise RuntimeError("stock diagnostic fixture differs")
    events = []
    started = time.monotonic()

    def record(kind, **safe):
        if len(events) < 160:
            events.append({"atSeconds": round(time.monotonic() - started, 3), "kind": kind} | safe)

    def trace(frame, event, _arg):
        func = frame.f_code.co_name
        if event != "call" or frame.f_code.co_filename != str(FIXTURE) or func not in FUNCTIONS:
            return None
        frame.f_trace_lines = False
        entered = time.monotonic()
        if func == "terraform":
            phase = frame.f_locals["args"][0]
            if phase not in COMMANDS:
                return None
            record("terraform-start", command=phase)
        if func == "send_error":
            code = frame.f_locals["code"]
            if type(code) is int and 100 <= code <= 599:
                record("adapter-error", status=code)
            return None
        if func == "do_POST":
            previous = len(frame.f_locals["receipts"])
        if func == "native":
            callers = {"simulator": "health", "verify_unauthenticated_metadata": "unauthenticated-metadata",
                       "do_POST": "provider-forward", "run": "post-destroy-metadata"}
            caller = callers.get(frame.f_back.f_code.co_name, "unclassified")

        def observe(frame, event, arg):
            if event == "exception":
                name = arg[0].__name__
                safe = {"function": func, "line": frame.f_lineno, "exceptionClass": name if name in CAUSES else "other"}
                if (type(arg[1]) is RuntimeError and len(arg[1].args) == 1 and
                        type(arg[1].args[0]) is str and arg[1].args[0] in FRAMING):
                    safe["category"] = arg[1].args[0]
                if func == "terraform" and isinstance(arg[1], subprocess.TimeoutExpired):
                    safe.update(command=phase, timeoutSeconds=arg[1].timeout)
                    if phase != "show":
                        safe["outputClasses"] = output_classes(arg[1].stdout, arg[1].stderr)
                record("fixture-exception", **safe)
            if event != "return":
                return observe
            duration = round(time.monotonic() - entered, 3)
            if func == "terraform":
                child = frame.f_locals.get("child")
                safe = {"command": phase, "durationSeconds": duration}
                if isinstance(child, subprocess.CompletedProcess):
                    safe["exit"] = child.returncode
                    # 'show -json' is Terraform state and is never inspected for output classes.
                    if phase != "show":
                        safe["outputClasses"] = output_classes(child.stdout, child.stderr)
                record("terraform-end", **safe)
            if func == "native" and type(arg) is tuple and type(arg[0]) is int and 100 <= arg[0] <= 599:
                record("stock-loopback-response", source=caller, status=arg[0], durationSeconds=duration)
            if func == "do_POST":
                receipts = frame.f_locals["receipts"]
                if len(receipts) > previous:
                    action, status = receipts[-1]
                    if action in ACTIONS and type(status) is int and 100 <= status <= 599:
                        record("stock-adapter-receipt", action=action, status=status)
            return observe

        return observe

    # Sample only this process's direct known tool descendants. The output contains no PID, pathname or arguments.
    stop = threading.Event()

    def sample_tools():
        while not stop.wait(12):
            pending = [Path("/proc") / str(os.getpid())]
            seen = set()
            tools = []
            while pending:
                proc = pending.pop()
                if proc in seen:
                    continue
                seen.add(proc)
                try:
                    for children in proc.glob("task/*/children"):
                        pending.extend(Path("/proc") / pid for pid in children.read_text().split())
                    executable = Path(os.readlink(proc / "exe")).name
                    names = {"terraform": "terraform", "terraform-provider-aws_v5.100.0_x5": "provider",
                             "simulator": "simulator"}
                    if executable in names:
                        # comm can contain a space. The fields after its closing ')' start at Linux stat field 3.
                        stat = (proc / "stat").read_text().rsplit(")", 1)[1].split()
                        cpu = (int(stat[11]) + int(stat[12])) / os.sysconf("SC_CLK_TCK")
                        tools.append({"tool": names[executable], "cpuSeconds": round(cpu, 2)})
                except (OSError, ValueError, IndexError):
                    continue
            record("active-tools", processes=tools)

    sampler = threading.Thread(target=sample_tools, daemon=True)
    sampler.start()
    failed = False
    try:
        threading.settrace(trace)
        sys.settrace(trace)
        runpy.run_path(str(FIXTURE), run_name="__main__")
    except Exception as exc:
        failed = True
        name = type(exc).__name__
        record("fixture-end", exceptionClass=name if name in CAUSES else "other")
    finally:
        sys.settrace(None)
        threading.settrace(None)
        stop.set()
        sampler.join(timeout=3)
    result = {"purpose": "separate diagnostic reproduction", "fixtureSHA256": FIXTURE_SHA256,
              "observedFixtureFailed": failed, "events": events, "limitReached": len(events) == 160}
    dest = Path(os.environ["RUNNER_TEMP"]) / "simpledb-verdict/diagnostic.json"
    dest.write_text(json.dumps(result, indent=2) + "\n")
    return int(failed)


if __name__ == "__main__":
    sys.exit(main())
