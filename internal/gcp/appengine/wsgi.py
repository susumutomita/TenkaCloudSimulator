"""App Engine WSGI request glue inside the bounded sandbox.

The trusted provider adds this module to every version bundle. It imports the
deployed main.py, calls its WSGI callable app once with the request and returns
the status, headers and body. No host resources or credentials are available.
"""
import base64
import importlib
import io
import sys


def handle(event, context):
    body = base64.b64decode(event["body"])
    environ = {
        "REQUEST_METHOD": event["method"],
        "SCRIPT_NAME": event["scriptName"],
        "PATH_INFO": event["path"],
        "QUERY_STRING": event["query"],
        "SERVER_NAME": event["serverName"],
        "SERVER_PORT": event["serverPort"],
        "SERVER_PROTOCOL": "HTTP/1.1",
        "REMOTE_ADDR": event["remoteAddr"],
        "CONTENT_LENGTH": str(len(body)),
        "wsgi.version": (1, 0),
        "wsgi.url_scheme": event["scheme"],
        "wsgi.input": io.BytesIO(body),
        "wsgi.errors": sys.stderr,
        "wsgi.multithread": False,
        "wsgi.multiprocess": True,
        "wsgi.run_once": True,
    }
    for name, value in event["headers"]:
        key = name.upper().replace("-", "_")
        if key == "CONTENT_TYPE":
            environ[key] = value
        elif key != "CONTENT_LENGTH":
            key = "HTTP_" + key
            environ[key] = environ[key] + "," + value if key in environ else value
    application = getattr(importlib.import_module("main"), "app")
    state, chunks = {}, []

    def start_response(status, headers, exc_info=None):
        if exc_info is not None:
            try:
                if state.get("sent"):
                    raise exc_info[1].with_traceback(exc_info[2])
            finally:
                exc_info = None
        elif "status" in state:
            raise AssertionError("start_response was already called")
        state["status"], state["headers"] = status, list(headers)
        return write

    def write(data):
        if not isinstance(data, (bytes, bytearray)):
            raise TypeError("WSGI response bodies must be bytes")
        state["sent"] = True
        chunks.append(bytes(data))

    result = application(environ, start_response)
    try:
        for chunk in result:
            if chunk:
                write(chunk)
    finally:
        if hasattr(result, "close"):
            result.close()
    if "status" not in state:
        raise RuntimeError("The WSGI application did not call start_response")
    return {
        "status": str(state["status"]),
        "headers": [[str(name), str(value)] for name, value in state["headers"]],
        "body": base64.b64encode(b"".join(chunks)).decode("ascii"),
    }
