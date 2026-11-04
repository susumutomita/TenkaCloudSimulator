"""Real local binary Secrets Manager SDK/authority/restart/cleanup contract.

Uses dummy training values only. No browser, guest VM, AWS or external endpoint.
Shared make/CI registration is a separate integration-owner responsibility.
"""
import json
import sqlite3
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

from check import Config, ClientError, boto3, running


def client(endpoint, token, region="us-east-1"):
    sdk = boto3.client("secretsmanager", endpoint_url=endpoint, region_name=region,
                       aws_access_key_id="TCSIMSECRETS", aws_secret_access_key="dummy",
                       config=Config(retries={"max_attempts": 0}))
    sdk.meta.events.register("before-sign", lambda request, **kwargs:
                             request.headers.add_header("X-Simulator-Token", token))
    def local_only(request, **kwargs):
        assert request.url.startswith(endpoint + "/"), "SDK selected an external endpoint"
    sdk.meta.events.register("before-send", local_only)
    return sdk


def error(code, action):
    try:
        action()
    except ClientError as exc:
        assert exc.response["Error"]["Code"] == code, "Unexpected local service error"
        assert "DUMMY-secret-" not in str(exc.response), "Error reflected a secret value"
    else:
        raise AssertionError("Expected " + code)


def metadata(response):
    assert "SecretString" not in response and "SecretBinary" not in response
    assert "KmsKeyId" not in response, "Response claimed an implemented encryption key"
    assert "DUMMY-secret-" not in str(response), "Metadata exposed a stored value"
    headers = response["ResponseMetadata"]["HTTPHeaders"]
    assert headers["cache-control"] == "no-store" and headers["x-simulator-encryption"] == "none"
    return response


def control(endpoint, token, path, body):
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(),
                                     method="POST" if path.endswith("/access") else "PUT",
                                     headers={"Content-Type": "application/json", "X-Simulator-Token": token})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


def run():
    a, b, c = [f"00000000-0000-0000-0000-{i:012d}" for i in (1, 2, 3)]
    name = "training/example"
    with tempfile.TemporaryDirectory(prefix="secret-native-") as directory:
        state_a, state_b = Path(directory) / "team-a.db", Path(directory) / "team-b.db"
        with running(db=state_a, authenticated=True) as (endpoint, operator), \
                running(db=state_b, authenticated=True) as (other_endpoint, other_operator):
            first, other = client(endpoint, operator), client(other_endpoint, other_operator)
            created = metadata(first.create_secret(Name=name, Description="dummy metadata",
                                                   ClientRequestToken=a, SecretString="DUMMY-secret-team-a-A"))
            other_created = metadata(other.create_secret(Name=name, ClientRequestToken=a,
                                                          SecretString="DUMMY-secret-team-b"))
            arn = created["ARN"]
            assert created["VersionId"] == a
            assert first.get_secret_value(SecretId=name)["SecretString"] == "DUMMY-secret-team-a-A"
            assert other.get_secret_value(SecretId=name)["SecretString"] == "DUMMY-secret-team-b"
            wrong = urllib.request.Request(other_endpoint + "/", data=b"{}", method="POST",
                headers={"X-Simulator-Token": operator, "Content-Type": "application/x-amz-json-1.1",
                         "X-Amz-Target": "secretsmanager.ListSecrets"})
            try:
                urllib.request.urlopen(wrong, timeout=5)
            except urllib.error.HTTPError as exc:
                assert exc.code == 401
            else:
                raise AssertionError("Local access token crossed team authorities")
            policy = {"Version": "2012-10-17", "Statement": [
                {"Effect": "Allow", "Action": "secretsmanager:GetSecretValue", "Resource": arn}]}
            control(endpoint, operator, "/_simulator/v1/iam/principals/secret-reader/policy", policy)
            participant = control(endpoint, operator, "/_simulator/v1/access", {"Principal": "secret-reader"})["AccessToken"]
            limited = client(endpoint, participant)
            assert limited.get_secret_value(SecretId=name)["SecretString"] == "DUMMY-secret-team-a-A"
            error("AccessDeniedException", lambda: limited.list_secrets())
            error("AccessDeniedException", lambda: limited.put_secret_value(
                SecretId=name, ClientRequestToken=b, SecretString="DUMMY-secret-denied"))
            metadata(first.put_secret_value(SecretId=arn, ClientRequestToken=b, SecretString="DUMMY-secret-team-a-B"))
            metadata(first.put_secret_value(SecretId=arn, ClientRequestToken=c, SecretString="DUMMY-secret-team-a-C"))
            before = metadata(first.describe_secret(SecretId=arn))
            assert before["VersionIdsToStages"] == {b: ["AWSPREVIOUS"], c: ["AWSCURRENT"]}
            metadata(first.put_secret_value(SecretId=arn, ClientRequestToken=a, SecretString="DUMMY-secret-team-a-A"))
            metadata(first.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-secret-team-a-A"))
            after = metadata(first.describe_secret(SecretId=arn))
            assert {k:v for k,v in before.items() if k!="ResponseMetadata"} == {k:v for k,v in after.items() if k!="ResponseMetadata"}, "An idempotent retry changed version ownership or metadata"
            error("ResourceExistsException", lambda: first.put_secret_value(
                SecretId=arn, ClientRequestToken=a, SecretString="DUMMY-secret-conflict"))
            error("InvalidRequestException", lambda: first.get_secret_value(
                SecretId=arn, VersionId=a, VersionStage="AWSCURRENT"))
            assert first.get_secret_value(SecretId=arn, VersionStage="AWSPREVIOUS")["SecretString"] == "DUMMY-secret-team-a-B"
            metadata(first.update_secret_version_stage(SecretId=arn, VersionStage="AWSCURRENT",
                                                       MoveToVersionId=a, RemoveFromVersionId=c))
            assert first.get_secret_value(SecretId=arn)["SecretString"] == "DUMMY-secret-team-a-A"
            error("InvalidParameterException", lambda: first.create_secret(
                Name="training/reject-kms", SecretString="DUMMY-secret-unsupported", KmsKeyId="alias/aws/secretsmanager"))
            error("InvalidParameterException", lambda: first.create_secret(Name="training/reject-binary", SecretBinary=b"DUMMY-secret-binary"))
            error("InvalidParameterException", lambda: first.delete_secret(SecretId=arn, RecoveryWindowInDays=7))
            second = metadata(first.create_secret(Name="training/page-second", SecretString="DUMMY-secret-pagination"))
            listed = metadata(first.list_secrets(MaxResults=1, SortOrder="asc"))
            assert listed["SecretList"][0]["ARN"] == arn
            cursor = listed["NextToken"]
            error("InvalidParameterException", lambda: other.list_secrets(MaxResults=1, SortOrder="asc", NextToken=cursor))
            assert other.get_secret_value(SecretId=name)["SecretString"] == "DUMMY-secret-team-b"
            metadata(other.delete_secret(SecretId=other_created["ARN"], ForceDeleteWithoutRecovery=True))
        with running(db=state_a, authenticated=True) as (endpoint, operator):
            first = client(endpoint, operator)
            assert first.get_secret_value(SecretId=arn)["SecretString"] == "DUMMY-secret-team-a-A"
            assert first.get_secret_value(SecretId=arn, VersionId=b)["SecretString"] == "DUMMY-secret-team-a-B"
            page = metadata(first.list_secrets(MaxResults=1, SortOrder="asc", NextToken=cursor))
            assert [item["ARN"] for item in page["SecretList"]] == [second["ARN"]]
            assert metadata(first.describe_secret(SecretId=arn))["VersionIdsToStages"] == {a: ["AWSCURRENT"], c: ["AWSPREVIOUS"]}
            metadata(first.delete_secret(SecretId=arn, ForceDeleteWithoutRecovery=True))
            metadata(first.delete_secret(SecretId=arn, ForceDeleteWithoutRecovery=True))
            replacement = metadata(first.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-secret-recreated"))
            assert replacement["ARN"] != arn
            metadata(first.delete_secret(SecretId=arn, ForceDeleteWithoutRecovery=True))
            assert first.get_secret_value(SecretId=replacement["ARN"])["SecretString"] == "DUMMY-secret-recreated"
            participant = control(endpoint, operator, "/_simulator/v1/access", {"Principal": "secret-reader"})["AccessToken"]
            error("AccessDeniedException", lambda: client(endpoint, participant).get_secret_value(SecretId=name))
            error("ResourceNotFoundException", lambda: first.get_secret_value(SecretId=arn))
            metadata(first.delete_secret(SecretId=replacement["ARN"], ForceDeleteWithoutRecovery=True))
            metadata(first.delete_secret(SecretId=second["ARN"], ForceDeleteWithoutRecovery=True))
            assert metadata(first.list_secrets())["SecretList"] == []
        for state, reservations in ((state_a, 3), (state_b, 1)):
            with sqlite3.connect(f"file:{state}?mode=ro", uri=True) as database:
                for table in ("aws_secrets", "aws_secret_versions", "aws_secret_stages"):
                    assert database.execute("SELECT count(*) FROM " + table).fetchone()[0] == 0
                assert database.execute("SELECT count(*) FROM aws_secret_arns").fetchone()[0] == reservations
    print("Secrets Manager real SDK/7ops/2teams/IAM/restart/physical-ARN cleanup PASS")


if __name__ == "__main__":
    run()
