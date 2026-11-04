"""In-sandbox function invocation glue; no host resources or cloud credentials."""
import importlib
import json
import sys
import types

sys.path.insert(0, "/var/task")
request = json.loads(sys.stdin.readline())
if request.get("capabilities"):
    capability = types.ModuleType("_simulator_bridge")
    def call(message):
        print("\n\x1eTCSIM_CALL " + json.dumps(message, allow_nan=False, separators=(",", ":")), flush=True)
        line = sys.stdin.readline()
        if not line:
            raise RuntimeError("Local capability channel closed")
        return json.loads(line)
    capability.call = call
    sys.modules["_simulator_bridge"] = capability
    sys.path.insert(1, "/simulator/modules")
try:
    module, name = request["handler"].rsplit(".", 1)
    handler = getattr(importlib.import_module(module), name)
    context = types.SimpleNamespace(**(request.get("context") or {}))
    value = handler(request["event"], context)
    response = json.dumps({"payload": value, "error": False}, allow_nan=False)
except BaseException as error:
    response = json.dumps({"payload": {"errorType": type(error).__name__, "errorMessage": str(error), "stackTrace": []}, "error": True})
print("\n\x1eTCSIM_RESULT " + response, flush=True)
