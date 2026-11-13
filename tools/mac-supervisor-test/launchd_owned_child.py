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
    bootstrapped = False
    result = {"label": label, "directory": str(directory), "battleReady": False}
    def ctl(*args, check=True):
        return subprocess.run(["/bin/launchctl",*args],capture_output=True,text=True,timeout=10,check=check)
    try:
        ctl("bootstrap",f"gui/{os.getuid()}",str(plist))
        bootstrapped = True
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
    except subprocess.CalledProcessError as exc:
        result["commandFailure"] = {"returncode":exc.returncode,"stderr":exc.stderr,"stdout":exc.stdout}
        raise
    finally:
        if bootstrapped:
            ctl("bootout",target)
            check=ctl("print",target,check=False)
            if check.returncode==0: raise RuntimeError("service still registered after bootout")
            # Preserve diagnostic evidence, never claim a permissions failure means absent.
            if "Could not find service" not in check.stderr:
                raise RuntimeError("service absence not confirmed: "+check.stderr)
            result["serviceRemoved"]=True
        (directory/"result.json").write_text(json.dumps(result,indent=2)+"\n")
        print(json.dumps(result))

if __name__=="__main__":
    raise SystemExit("Blocked experiment: bootstrap failed; do not retry without resolving the original failure through an authorized path.")
