"""In-sandbox function invocation glue; no host resources or cloud credentials."""
import importlib
import json
import sys
import types

sys.path.insert(0, "/var/task")
request = json.load(sys.stdin)
try:
    module, name = request["handler"].rsplit(".", 1)
    handler = getattr(importlib.import_module(module), name)
    context = types.SimpleNamespace(**(request.get("context") or {}))
    value = handler(request["event"], context)
    response = json.dumps({"payload": value, "error": False}, allow_nan=False)
except BaseException as error:
    response = json.dumps({"payload": {"errorType": type(error).__name__, "errorMessage": str(error), "stackTrace": []}, "error": True})
print("\x1eTCSIM_RESULT " + response, flush=True)
