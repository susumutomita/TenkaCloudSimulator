"""Benign transient launchd acceptance experiment. No QEMU or existing services.
This measures same-process-group cleanup, NOT hostile-process confinement.
"""
import json
import os
import pathlib
import plistlib
import subprocess
import sys
import tempfile
import time
import uuid


def run():
    if sys.platform != "darwin":
        raise RuntimeError("Darwin required")
    directory = pathlib.Path(tempfile.mkdtemp(prefix="battle-launchd-benign-", dir=str(pathlib.Path.home())))
    os.chmod(directory, 0o700)
    label = "local.tenkacloud.benign." + str(uuid.uuid4())
    target = f"gui/{os.getuid()}/{label}"
    script = directory / "owned.py"
    script.write_text('''import json, os, pathlib, signal, subprocess, sys, time
p=pathlib.Path(sys.argv[1])
if len(sys.argv)>2:
 signal.signal(signal.SIGTERM, signal.SIG_IGN)
 (p/"child.json").write_text(json.dumps({"pid":os.getpid(),"pgid":os.getpgrp(),"ppid":os.getppid()}))
 # Bound the fixture even if the experiment/controller dies unexpectedly.
 time.sleep(15)
else:
 child=subprocess.Popen([sys.executable,__file__,str(p),"child"],env={},stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 (p/"supervisor.json").write_text(json.dumps({"pid":os.getpid(),"pgid":os.getpgrp(),"child":child.pid}))
 child.wait()
''')
    plist = directory / "job.plist"
    plist.write_bytes(plistlib.dumps({"Label":label,"ProgramArguments":[str(pathlib.Path(sys.executable).resolve()),str(script),str(directory)],"RunAtLoad":False,"KeepAlive":False,"AbandonProcessGroup":False,"ExitTimeOut":2}))
    attempted = False
    failure = None
    result = {"label": label, "directory": str(directory), "battleReady": False}
    def ctl(*args, check=True):
        return subprocess.run(["/bin/launchctl",*args],capture_output=True,text=True,timeout=10,check=check)
    def detail(exc):
        return {"type": type(exc).__name__, "message": str(exc)[:2048], "returncode": getattr(exc, "returncode", None), "stderr": str(getattr(exc, "stderr", "") or "")[:4096]}
    def absent(check):
        return check.returncode != 0 and f'Could not find service "{label}"' in check.stderr
    try:
        attempted = True
        ctl("bootstrap",f"gui/{os.getuid()}",str(plist))
        ctl("kickstart",target)
        deadline=time.monotonic()+5
        while not (directory/"child.json").exists():
            if time.monotonic()>deadline: raise RuntimeError("child did not initialize")
            time.sleep(.05)
        child=json.loads((directory/"child.json").read_text())
        supervisor=json.loads((directory/"supervisor.json").read_text())
        if child["pgid"]!=supervisor["pgid"] or child["ppid"]!=supervisor["pid"] or child["pid"]!=supervisor["child"]:
            raise RuntimeError("fixture ownership mismatch")
        result["sameGroupObserved"]=True
        ctl("kill","SIGKILL",target)
        # kill(pid,0) is observation only; never signal or reap a numeric PID.
        deadline=time.monotonic()+5
        while True:
            try: os.kill(child["pid"],0)
            except ProcessLookupError: break
            if time.monotonic()>deadline: raise RuntimeError("owned child still visible after supervisor death")
            time.sleep(.05)
        result["childAbsentAfterSupervisorKill"]=True
        time.sleep(.3)
        status=ctl("print",target).stdout
        if "state = running" in status: raise RuntimeError("job unexpectedly restarted")
        result["noRestartObserved"]=True
    except BaseException as exc:
        failure = exc
        result["operationFailure"] = detail(exc)
    finally:
        # A failed/timeout bootstrap may have installed the dedicated job.
        # Inspect only its exact fresh label, never retry registration.
        if attempted:
            try:
                state = ctl("print", target, check=False)
                if state.returncode == 0:
                    result["registrationObserved"] = True
                    ctl("bootout", target)
                    state = ctl("print", target, check=False)
                if not absent(state):
                    raise RuntimeError("exact-label absence unconfirmed: " + state.stderr)
                result["serviceAbsentConfirmed"] = True
            except BaseException as exc:
                result["cleanupFailure"] = detail(exc)
                if failure is None:
                    failure = exc
        result["experimentPassed"] = failure is None and result.get("serviceAbsentConfirmed", False)
        # This outer evidence write is reached even when bootout/print fail.
        # Disk failure itself remains an explicit error, never a success receipt.
        (directory/"result.json").write_text(json.dumps(result,indent=2)+"\n")
        print(json.dumps(result))
    if failure is not None:
        raise RuntimeError("experiment incomplete; evidence: " + str(directory/"result.json")) from failure
    return result

if __name__=="__main__":
    raise SystemExit("Blocked experiment: bootstrap failed; do not retry without resolving the original failure through an authorized path.")
