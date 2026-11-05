"""Real native IAM customer policy/role authority, two CLIs and persisted SQLite.

No AWS endpoints, STS, user credentials, browser, guest VM or optional skips.
Registration in the shared Conformance/CI belongs to the integration owner.
"""
import json
import tempfile
import urllib.error
import urllib.request
from contextlib import ExitStack
from pathlib import Path
from urllib.parse import urlsplit

from check import Config, ClientError, boto3, running

TRUST = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "sts:AssumeRole", "Principal": {"Service": "lambda.amazonaws.com"}}]}
BUCKET = "managed-sdk-training"
RESOURCE = "arn:aws:s3:::" + BUCKET + "/value"
ALLOW = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": RESOURCE}]}
DENY = {"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Action": "s3:GetObject", "Resource": RESOURCE}]}
INLINE = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::" + BUCKET + "/inline"}]}
ROLES = ("ManagedFirst", "ManagedSecond")


def client(service, endpoint, token):
    origin = urlsplit(endpoint)
    assert origin.scheme == "http" and origin.hostname == "127.0.0.1" and origin.port
    sdk = boto3.client(service, endpoint_url=endpoint, region_name="us-east-1",
                       aws_access_key_id="TCSIMMANAGED", aws_secret_access_key="dummy-local-only",
                       config=Config(s3={"addressing_style": "path"}, retries={"max_attempts": 0},
                                     connect_timeout=2, read_timeout=5,
                                     request_checksum_calculation="when_required", response_checksum_validation="when_required"))
    sdk.meta.events.register("before-sign", lambda request, **kwargs: request.headers.add_header("X-Simulator-Token", token))
    def local_only(request, **kwargs):
        target = urlsplit(request.url.decode("ascii") if isinstance(request.url, bytes) else request.url)
        assert (target.scheme, target.hostname, target.port) == (origin.scheme, origin.hostname, origin.port), "SDK selected an unallocated origin"
        assert not target.username and not target.password and not target.fragment
        if service == "iam":
            assert target.path == "/" and not target.query
    def completed(http_response, **kwargs):
        assert http_response.status_code not in range(300, 400), "SDK unexpectedly redirected"
    sdk.meta.events.register("before-send", local_only)
    sdk.meta.events.register("after-call", completed)
    return sdk


def error(code, callback):
    try:
        callback()
    except ClientError as failure:
        assert failure.response["Error"]["Code"] == code, "Unexpected native IAM/S3 error"
    else:
        raise AssertionError("Expected native " + code)


def participant(endpoint, operator, name):
    request = urllib.request.Request(endpoint + "/_simulator/v1/access", method="POST",
                                     data=json.dumps({"Principal": name}).encode(),
                                     headers={"X-Simulator-Token": operator, "Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=5) as response:
        token = json.load(response)["AccessToken"]
        assert token
        return token


def forbidden_host_token(endpoint, token):
    request = urllib.request.Request(endpoint + "/_simulator/v1/capabilities", headers={"X-Simulator-Token": token})
    try:
        urllib.request.urlopen(request, timeout=5)
    except urllib.error.HTTPError as failure:
        assert failure.code == 401, "Foreign authority was not rejected"
        failure.close()
    else:
        raise AssertionError("Foreign CLI token was accepted")


def read(s3, key="value"):
    result = s3.get_object(Bucket=BUCKET, Key=key)
    try:
        return result["Body"].read()
    finally:
        result["Body"].close()


def run():
    with tempfile.TemporaryDirectory(prefix="iam-managed-sdk-owned-") as directory:
        databases = [str(Path(directory) / ("team-" + str(i) + ".db")) for i in range(2)]
        saved = []
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=database, authenticated=True)) for database in databases]
            for index, (endpoint, operator) in enumerate(runtimes):
                iam, s3 = client("iam", endpoint, operator), client("s3", endpoint, operator)
                value = ("dummy-managed-team-" + str(index)).encode()
                s3.create_bucket(Bucket=BUCKET)
                s3.put_object(Bucket=BUCKET, Key="value", Body=value)
                s3.put_object(Bucket=BUCKET, Key="inline", Body=b"dummy-inline")
                for role in ROLES:
                    iam.create_role(RoleName=role, AssumeRolePolicyDocument=json.dumps(TRUST))
                tokens = [participant(endpoint, operator, role) for role in ROLES]
                participants = [client("s3", endpoint, token) for token in tokens]
                denied_iam = client("iam", endpoint, tokens[0])
                error("AccessDenied", lambda: denied_iam.create_policy(PolicyName="DeniedMutation", PolicyDocument=json.dumps(ALLOW)))
                spoof = urllib.request.Request(endpoint + "/", method="POST",
                    data=b"Action=ListPolicies&Version=2010-05-08&Scope=Local",
                    headers={"Content-Type":"application/x-www-form-urlencoded", "X-Simulator-Service":"iam", "X-Simulator-Token":tokens[0], "X-Simulator-Principal":"simulator-admin"})
                try:
                    urllib.request.urlopen(spoof, timeout=5)
                except urllib.error.HTTPError as failure:
                    assert failure.code == 403, "Participant selected another identity"
                    failure.close()
                else:
                    raise AssertionError("Participant spoofed operator identity")
                for sdk in participants:
                    error("AccessDenied", lambda sdk=sdk: read(sdk))
                policy = iam.create_policy(PolicyName="SharedTraining", PolicyDocument=json.dumps(ALLOW), Description="dummy policy metadata")["Policy"]
                arn = policy["Arn"]
                assert policy["DefaultVersionId"] == "v1" and policy["IsAttachable"]
                assert iam.get_policy_version(PolicyArn=arn, VersionId="v1")["PolicyVersion"]["Document"] == ALLOW
                assert "Document" not in iam.get_policy(PolicyArn=arn)["Policy"]
                error("NotImplemented", lambda: iam.list_policies())
                error("NotImplemented", lambda: iam.list_policies(Scope="AWS"))
                error("NotImplemented", lambda: iam.list_policies(Scope="Local", OnlyAttached=False))
                extra = iam.create_policy(PolicyName="OtherTraining", PolicyDocument=json.dumps(ALLOW))["Policy"]
                first = iam.list_policies(Scope="Local", MaxItems=1)
                assert first["IsTruncated"] and "Description" not in first["Policies"][0]
                second = iam.list_policies(Scope="Local", Marker=first["Marker"], MaxItems=1)
                assert not second["IsTruncated"] and {first["Policies"][0]["Arn"], second["Policies"][0]["Arn"]} == {arn, extra["Arn"]}
                iam.delete_policy(PolicyArn=extra["Arn"])
                for role, sdk in zip(ROLES, participants):
                    iam.attach_role_policy(RoleName=role, PolicyArn=arn)
                    assert read(sdk) == value
                    assert iam.list_attached_role_policies(RoleName=role)["AttachedPolicies"] == [{"PolicyName": "SharedTraining", "PolicyArn": arn}]
                iam.attach_role_policy(RoleName=ROLES[0], PolicyArn=arn)  # Local verified-edge no-op.
                assert iam.get_policy(PolicyArn=arn)["Policy"]["AttachmentCount"] == 2
                iam.put_role_policy(RoleName=ROLES[0], PolicyName="ExplicitDeny", PolicyDocument=json.dumps(DENY))
                error("AccessDenied", lambda: read(participants[0]))
                assert read(participants[1]) == value
                iam.delete_role_policy(RoleName=ROLES[0], PolicyName="ExplicitDeny")
                iam.put_role_policy(RoleName=ROLES[0], PolicyName="PreservedInline", PolicyDocument=json.dumps(INLINE))
                version = iam.create_policy_version(PolicyArn=arn, PolicyDocument=json.dumps(DENY))["PolicyVersion"]
                assert version["VersionId"] == "v2" and not version["IsDefaultVersion"] and "Document" not in version
                for sdk in participants:
                    assert read(sdk) == value  # Nondefault policy does not grant/deny.
                iam.set_default_policy_version(PolicyArn=arn, VersionId="v2")
                for sdk in participants:
                    error("AccessDenied", lambda sdk=sdk: read(sdk))
                iam.set_default_policy_version(PolicyArn=arn, VersionId="v1")
                iam.create_policy_version(PolicyArn=arn, PolicyDocument=json.dumps(DENY), SetAsDefault=True)
                for sdk in participants:
                    error("AccessDenied", lambda sdk=sdk: read(sdk))
                iam.set_default_policy_version(PolicyArn=arn, VersionId="v1")
                for _ in range(2):
                    iam.create_policy_version(PolicyArn=arn, PolicyDocument=json.dumps(ALLOW))
                before = iam.get_policy(PolicyArn=arn)["Policy"]
                error("LimitExceeded", lambda: iam.create_policy_version(PolicyArn=arn, PolicyDocument=json.dumps(DENY), SetAsDefault=True))
                assert iam.get_policy(PolicyArn=arn)["Policy"] == before
                for sdk in participants:
                    assert read(sdk) == value
                error("DeleteConflict", lambda: iam.delete_policy_version(PolicyArn=arn, VersionId="v1"))
                iam.delete_policy_version(PolicyArn=arn, VersionId="v2")
                fresh = iam.create_policy_version(PolicyArn=arn, PolicyDocument=json.dumps(ALLOW))["PolicyVersion"]
                assert fresh["VersionId"] == "v6", "Deleted version identifier was reused"
                pages = iam.list_policy_versions(PolicyArn=arn, MaxItems=1)
                assert pages["IsTruncated"] and "Document" not in pages["Versions"][0]
                assert iam.list_policy_versions(PolicyArn=arn, Marker=pages["Marker"])["Versions"]
                error("DeleteConflict", lambda: iam.delete_policy(PolicyArn=arn))
                error("DeleteConflict", lambda: iam.delete_role(RoleName=ROLES[0]))
                saved.append({"arn": arn, "id": policy["PolicyId"], "value": value, "operator": operator, "participants": tokens})
            assert saved[0]["arn"] == saved[1]["arn"] and saved[0]["id"] != saved[1]["id"]
            forbidden_host_token(runtimes[1][0], saved[0]["operator"])
            forbidden_host_token(runtimes[0][0], saved[1]["participants"][0])
        # New processes deliberately have new access authorities. SQLite persists;
        # participant sessions do not: issue fresh tokens, never claim STS/recovery.
        with ExitStack() as stack:
            runtimes = [stack.enter_context(running(db=database, authenticated=True)) for database in databases]
            for index, (endpoint, operator) in enumerate(runtimes):
                iam, s3 = client("iam", endpoint, operator), client("s3", endpoint, operator)
                previous = saved[index]
                forbidden_host_token(endpoint, previous["participants"][0])
                policy = iam.get_policy(PolicyArn=previous["arn"])["Policy"]
                assert policy["PolicyId"] == previous["id"] and policy["DefaultVersionId"] == "v1" and policy["AttachmentCount"] == 2
                tokens = [participant(endpoint, operator, role) for role in ROLES]
                participants = [client("s3", endpoint, token) for token in tokens]
                for sdk in participants:
                    assert read(sdk) == previous["value"]
                iam.detach_role_policy(RoleName=ROLES[0], PolicyArn=previous["arn"])
                iam.detach_role_policy(RoleName=ROLES[0], PolicyArn=previous["arn"])  # Local no-op, parents still valid.
                error("AccessDenied", lambda: read(participants[0]))
                assert read(participants[0], "inline") == b"dummy-inline"
                assert read(participants[1]) == previous["value"]
                iam.detach_role_policy(RoleName=ROLES[1], PolicyArn=previous["arn"])
                error("AccessDenied", lambda: read(participants[1]))
                iam.delete_role_policy(RoleName=ROLES[0], PolicyName="PreservedInline")
                for version in iam.list_policy_versions(PolicyArn=previous["arn"])["Versions"]:
                    if not version["IsDefaultVersion"]:
                        iam.delete_policy_version(PolicyArn=previous["arn"], VersionId=version["VersionId"])
                iam.delete_policy(PolicyArn=previous["arn"])
                for role in ROLES:
                    iam.delete_role(RoleName=role)
                assert iam.list_policies(Scope="Local")["Policies"] == []
                assert iam.list_roles()["Roles"] == []
                s3.delete_object(Bucket=BUCKET, Key="value")
                s3.delete_object(Bucket=BUCKET, Key="inline")
                s3.delete_bucket(Bucket=BUCKET)
                assert s3.list_buckets()["Buckets"] == []
    print("Native IAM managed policies: actual two-team/token/S3/default/inline-Deny/restart/cleanup PASS")


if __name__ == "__main__":
    try:
        run()
    except Exception as failure:
        # SDK/network exceptions can contain headers: do not emit raw credentials.
        raise RuntimeError("IAM managed SDK contract failed: " + type(failure).__name__) from None
