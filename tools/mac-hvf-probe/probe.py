"""Development probe only: paused empty HVF machine, not a Battle runtime."""
import json
import platform
import subprocess
import sys

QEMU = "/opt/homebrew/bin/qemu-system-aarch64"
ARGS = [QEMU, "-no-user-config", "-nodefaults", "-machine", "virt,accel=hvf",
        "-cpu", "host", "-m", "128", "-smp", "1", "-S", "-nic", "none",
        "-display", "none", "-serial", "none", "-monitor", "none", "-qmp", "stdio"]
COMMANDS = [{"execute": "qmp_capabilities", "id": "capabilities"},
            {"execute": "query-status", "id": "status"},
            {"execute": "quit", "id": "quit"}]


def verify(stdout, stderr, returncode):
    if returncode != 0 or stderr:
        raise ValueError("QEMU failed or reported a diagnostic")
    records = [json.loads(line) for line in stdout.splitlines()]
    if not records or "QMP" not in records[0]:
        raise ValueError("missing QMP greeting")
    replies = {}
    shutdown = False
    for record in records[1:]:
        if "error" in record:
            raise ValueError("QMP error")
        if record.get("event") == "SHUTDOWN":
            shutdown = record.get("data") == {"guest": False, "reason": "host-qmp-quit"}
        if "id" in record:
            ident = record["id"]
            if ident in replies or ident not in ("capabilities", "status", "quit"):
                raise ValueError("unexpected or duplicate response")
            replies[ident] = record.get("return")
    if replies.get("capabilities") != {} or replies.get("quit") != {}:
        raise ValueError("missing control acknowledgement")
    if replies.get("status") != {"status": "prelaunch", "running": False}:
        raise ValueError("machine was not paused and empty")
    if not shutdown:
        raise ValueError("missing orderly owned shutdown")


def main():
    if platform.system() != "Darwin" or platform.machine() != "arm64":
        raise SystemExit("This probe requires an Apple Silicon Mac; no fallback.")
    child = subprocess.Popen(ARGS, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                             stderr=subprocess.PIPE, text=True)
    try:
        stdout, stderr = child.communicate(
            "".join(json.dumps(command) + "\n" for command in COMMANDS), timeout=8)
    except BaseException:
        # Only the exact child created above is targeted. No process-name kills.
        child.kill()
        child.communicate()
        raise
    verify(stdout, stderr, child.returncode)
    print(json.dumps({"result": "empty-paused-hvf-probe-pass", "battleReady": False,
                      "guestBooted": False, "ownedChildReaped": True,
                      "qmp": [json.loads(line) for line in stdout.splitlines()]}))


if __name__ == "__main__":
    main()
