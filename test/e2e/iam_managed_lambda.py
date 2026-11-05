"""Actual customer managed policy authority through saved-role Python Lambda.

Native Query SDK -> SQLite role aggregate -> real CPython/WASI guest boto3 ->
native S3. Dummy local data only; no direct-principal substitute, AWS or skips.
"""
import hashlib
import io
import json
import zipfile
from pathlib import Path
from urllib.parse import urlsplit

from check import BINARY, REPORTS, Config, boto3, running

PHASE = "startup"
REGION = "us-east-1"
BUCKET = "managed-lambda-training"
FUNCTION = "managed-policy-guest"
ROLE = "ManagedLambdaExecutor"
VALUE = b"DUMMY-managed-lambda-training-bytes"
TRUST = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "sts:AssumeRole", "Principal": {"Service": "lambda.amazonaws.com"}}]}
ALLOW = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::" + BUCKET + "/value"}]}
DENY = {"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::" + BUCKET + "/value"}]}


def client(service, endpoint, token):
    origin = urlsplit(endpoint)
    assert origin.scheme == "http" and origin.hostname == "127.0.0.1" and origin.port
    sdk = boto3.client(service, endpoint_url=endpoint, region_name=REGION,
                       aws_access_key_id="TCSIMMANAGEDLAMBDA", aws_secret_access_key="dummy-local-only",
                       config=Config(s3={"addressing_style": "path"}, retries={"max_attempts": 0},
                                     connect_timeout=2, read_timeout=25,
                                     request_checksum_calculation="when_required", response_checksum_validation="when_required"))
    sdk.meta.events.register("before-sign", lambda request, **kwargs: request.headers.add_header("X-Simulator-Token", token))
    def local_only(request, **kwargs):
        target = urlsplit(request.url.decode("ascii") if isinstance(request.url, bytes) else request.url)
        assert (target.scheme, target.hostname, target.port) == (origin.scheme, origin.hostname, origin.port), "SDK left allocated loopback origin"
        assert not target.username and not target.password and not target.fragment
    def completed(http_response, **kwargs):
        assert http_response.status_code not in range(300, 400), "SDK unexpectedly redirected"
    sdk.meta.events.register("before-send", local_only)
    sdk.meta.events.register("after-call", completed)
    return sdk


def run():
    global PHASE
    evidence = {"realPythonWASIZIP": True, "nativeCustomerPolicy": True, "savedExecutionRole": True,
                "functionTimeoutSeconds": 15, "actualInvokeResults": [], "realAWS": False,
                "directPrincipalS3UsedAsGuestProof": False, "tokensOrObjectBytesReported": False}
    with running(authenticated=True) as (endpoint, token):
        iam, functions, s3 = (client(service, endpoint, token) for service in ("iam", "lambda", "s3"))
        PHASE = "create-native-dependencies"
        s3.create_bucket(Bucket=BUCKET)
        s3.put_object(Bucket=BUCKET, Key="value", Body=VALUE)
        role = iam.create_role(RoleName=ROLE, AssumeRolePolicyDocument=json.dumps(TRUST))["Role"]
        policy = iam.create_policy(PolicyName="ManagedLambdaRead", PolicyDocument=json.dumps(ALLOW))["Policy"]
        assert policy["DefaultVersionId"] == "v1"
        iam.attach_role_policy(RoleName=ROLE, PolicyArn=policy["Arn"])
        source = '''import boto3

def handler(event, context):
    if event.get("wrong_region"):
        sdk = boto3.client("s3", region_name="us-west-2")
    else:
        sdk = boto3.client("s3")
    body = sdk.get_object(Bucket="managed-lambda-training", Key="value")["Body"]
    return {"stored": body.read().decode("utf-8")}
'''
        code = io.BytesIO()
        with zipfile.ZipFile(code, "w") as archive:
            archive.writestr("app.py", source)
        functions.create_function(FunctionName=FUNCTION, Runtime="python3.12", Role=role["Arn"],
                                  Handler="app.handler", Timeout=15, Code={"ZipFile": code.getvalue()})
        configuration = functions.get_function_configuration(FunctionName=FUNCTION)
        assert configuration["Role"] == role["Arn"] and configuration["Timeout"] == 15
        assert configuration["FunctionArn"].startswith("arn:aws:lambda:" + REGION + ":")

        def invoke(label, expected, event=None, error_code=b"AccessDenied"):
            global PHASE
            PHASE = label
            # The operator invokes the function; it must not become guest authority.
            # Untrusted event identity/region fields are not a capability source.
            payload = event if event is not None else {"principal": "simulator-admin", "region": "eu-west-1"}
            result = functions.invoke(FunctionName=FUNCTION, Payload=json.dumps(payload).encode())
            assert result["StatusCode"] == 200
            try:
                body = result["Payload"].read()
            finally:
                result["Payload"].close()
            if expected == "allowed":
                assert "FunctionError" not in result, "Managed role did not grant the real guest read"
                assert json.loads(body) == {"stored": VALUE.decode()}
            else:
                assert result.get("FunctionError") == "Unhandled", "Guest refusal was not a real FunctionError"
                assert error_code in body and VALUE not in body, "Denied guest disclosed object bytes or lost its refusal"
            evidence["actualInvokeResults"].append({"step": label, "result": expected})

        invoke("attached-default-v1", "allowed")
        version = iam.create_policy_version(PolicyArn=policy["Arn"], PolicyDocument=json.dumps(DENY))["PolicyVersion"]
        assert not version["IsDefaultVersion"]
        invoke("nondefault-deny-inert", "allowed")
        iam.set_default_policy_version(PolicyArn=policy["Arn"], VersionId=version["VersionId"])
        invoke("default-deny-reaches-guest", "denied")
        iam.set_default_policy_version(PolicyArn=policy["Arn"], VersionId="v1")
        invoke("restored-default-v1", "allowed")
        iam.put_role_policy(RoleName=ROLE, PolicyName="InlineDeny", PolicyDocument=json.dumps(DENY))
        invoke("inline-deny-overrides-managed-allow", "denied")
        iam.delete_role_policy(RoleName=ROLE, PolicyName="InlineDeny")
        invoke("inline-deny-removed", "allowed")
        invoke("guest-region-override-refused", "denied", {"wrong_region": True}, b"InvalidRequest")
        iam.detach_role_policy(RoleName=ROLE, PolicyArn=policy["Arn"])
        invoke("detached-managed-policy", "denied")
        # Direct operator S3 still succeeds: denied invocations are execution-role
        # authorization failures, not a missing/corrupt object or stopped service.
        native = s3.get_object(Bucket=BUCKET, Key="value")
        try:
            assert native["Body"].read() == VALUE
        finally:
            native["Body"].close()
        PHASE = "cleanup"
        functions.delete_function(FunctionName=FUNCTION)
        iam.delete_policy_version(PolicyArn=policy["Arn"], VersionId=version["VersionId"])
        iam.delete_policy(PolicyArn=policy["Arn"])
        iam.delete_role(RoleName=ROLE)
        s3.delete_object(Bucket=BUCKET, Key="value")
        s3.delete_bucket(Bucket=BUCKET)
        assert functions.list_functions()["Functions"] == []
        assert iam.list_roles()["Roles"] == []
        assert iam.list_policies(Scope="Local")["Policies"] == []
        assert s3.list_buckets()["Buckets"] == []
        evidence["cleanup"] = "DELETED"
    evidence["binarySHA256"] = hashlib.sha256(Path(BINARY).read_bytes()).hexdigest()
    (REPORTS / "iam-managed-lambda.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print("Native managed IAM -> saved-role Python/WASI Lambda -> S3 authority/revocation/cleanup PASS")


if __name__ == "__main__":
    try:
        run()
    except Exception as failure:
        # SDK/guest/network exception text can contain response headers or values.
        raise RuntimeError("Managed Lambda SDK contract failed at " + PHASE + ": " + type(failure).__name__) from None
