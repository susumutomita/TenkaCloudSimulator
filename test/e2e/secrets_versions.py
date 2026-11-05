"""Real official boto3 ListSecretVersionIds against two authenticated local CLIs.

Run the exact candidate binary. Uses dummy data only, fixed allocated loopback
origins, no proxies/retries/cloud access; metadata results never expose values.
Shared mandatory registration/capability/docs are the integration owner's work.
"""
import json
import sqlite3
import tempfile
from datetime import datetime
from pathlib import Path
from urllib.parse import urlsplit
import urllib.request
import urllib.error

from check import Config, ClientError, REPORTS, boto3, running


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        raise AssertionError("Local control request redirected")


HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())


def origin(endpoint):
    parsed = urlsplit(endpoint)
    assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port
    assert not parsed.username and not parsed.password and not parsed.query and not parsed.fragment
    assert parsed.path in ("", "/")
    return parsed.scheme, parsed.hostname, parsed.port


def control(endpoint, token, path, body):
    origin(endpoint)
    assert path == "/_simulator/v1/access"
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(), method="POST",
        headers={"Content-Type": "application/json", "X-Simulator-Token": token})
    with HTTP.open(request, timeout=5) as response:
        assert response.status == 200
        return json.load(response)


def metadata(result):
    assert not any(k in result for k in ("SecretString", "SecretBinary", "KmsKeyIds", "LastAccessedDate"))
    assert "DUMMY-version-value" not in str(result), "Metadata disclosed dummy plaintext"
    headers = result["ResponseMetadata"]["HTTPHeaders"]
    assert headers["cache-control"] == "no-store" and headers["x-simulator-encryption"] == "none"
    for item in result.get("Versions", []):
        assert set(item) <= {"VersionId", "VersionStages", "CreatedDate"}
        assert set(item) >= {"VersionId", "CreatedDate"}
        assert isinstance(item["CreatedDate"], datetime)
    return result


def expect_error(code, fn, status=400):
    try:
        fn()
    except ClientError as exc:
        assert exc.response["ResponseMetadata"]["HTTPStatusCode"] == status
        if code is not None:
            assert exc.response["Error"]["Code"] == code, "Unexpected native error class"
        assert "DUMMY-version-value" not in str(exc.response), "Error disclosed dummy plaintext"
    else:
        raise AssertionError("Expected native refusal")


def frame(path):
    # Raw dummy values/cursor keys stay in test memory, never evidence or logs.
    with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
        return tuple(tuple(db.execute("SELECT * FROM " + table + " ORDER BY 1,2"))
                     for table in ("aws_secret_settings", "aws_secret_arns", "aws_secrets",
                                   "aws_secret_versions", "aws_secret_stages"))


def run():
    tracked = []

    def client(service, endpoint, token, region="us-east-1"):
        allocated = origin(endpoint)
        sdk = boto3.client(service, endpoint_url=endpoint, region_name=region,
            aws_access_key_id="TCSIMVERSIONS", aws_secret_access_key="dummy-local-only",
            config=Config(proxies={}, retries={"max_attempts": 0}, connect_timeout=2, read_timeout=5))
        evidence = {"service": service, "calls": 0, "sends": 0, "responses": 0, "statuses": []}
        sdk.meta.events.register("before-sign", lambda request, **kwargs:
            request.headers.add_header("X-Simulator-Token", token))

        def called(**kwargs):
            evidence["calls"] += 1

        def sending(request, **kwargs):
            url = request.url.decode("ascii") if isinstance(request.url, bytes) else request.url
            destination = urlsplit(url)
            assert (destination.scheme, destination.hostname, destination.port) == allocated
            assert destination.path == "/" and not destination.query and not destination.fragment
            assert not destination.username and not destination.password
            evidence["sends"] += 1

        def completed(http_response, **kwargs):
            assert not 300 <= http_response.status_code < 400
            evidence["responses"] += 1
            evidence["statuses"].append(http_response.status_code)

        sdk.meta.events.register("before-call", called)
        sdk.meta.events.register("before-send", sending)
        sdk.meta.events.register("after-call", completed)
        tracked.append((sdk, evidence))
        return sdk

    a, b, c = [f"00000000-0000-0000-0000-{i:012d}" for i in (1, 2, 3)]
    name, role = "training/version-list", "version-list-reader"
    with tempfile.TemporaryDirectory(prefix="secret-versions-") as temp:
        first_path, peer_path = Path(temp) / "a.db", Path(temp) / "b.db"
        try:
            with running(db=first_path, authenticated=True) as (endpoint, token), \
                    running(db=peer_path, authenticated=True) as (peer_endpoint, peer_token):
                first, peer = client("secretsmanager", endpoint, token), client("secretsmanager", peer_endpoint, peer_token)
                west = client("secretsmanager", endpoint, token, "us-west-2")
                iam = client("iam", endpoint, token)
                created = metadata(first.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-version-value-A"))
                arn = created["ARN"]
                peer_arn = metadata(peer.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-version-value-peer"))["ARN"]
                west_arn = metadata(west.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-version-value-west"))["ARN"]
                for version, value in ((b, "B"), (c, "C")):
                    metadata(first.put_secret_value(SecretId=arn, ClientRequestToken=version, SecretString="DUMMY-version-value-" + value))
                saved = frame(first_path)
                default = metadata(first.list_secret_version_ids(SecretId=name))
                assert default["ARN"] == arn and default["Name"] == name
                assert {v["VersionId"]: v["VersionStages"] for v in default["Versions"]} == {b: ["AWSPREVIOUS"], c: ["AWSCURRENT"]}
                first_page = metadata(first.list_secret_version_ids(SecretId=arn, IncludeDeprecated=True, MaxResults=1))
                assert first_page["Versions"][0]["VersionId"] == a and "VersionStages" not in first_page["Versions"][0]
                cursor = first_page["NextToken"]
                ids = [a]
                next_token = cursor
                while next_token:
                    response = metadata(first.list_secret_version_ids(SecretId=name, IncludeDeprecated=True, MaxResults=1, NextToken=next_token))
                    ids.extend(v["VersionId"] for v in response["Versions"])
                    next_token = response.get("NextToken", "")
                    assert len(ids) <= 3
                assert ids == [a, b, c] and len(set(ids)) == 3
                # Use the actual served static request unchanged. It deliberately
                # names this fixture, not a replaced/forged live template.
                docs_request = urllib.request.Request(endpoint + "/_simulator/docs/openapi.json",
                    headers={"X-Simulator-Token": token})
                with HTTP.open(docs_request, timeout=5) as response:
                    assert response.status == 200
                    spec = json.load(response)
                root = spec["paths"]["/"]["post"]
                template = root["requestBody"]["content"]["application/x-amz-json-1.1"]["examples"]["ListSecretVersionIds"]["value"]
                assert template == {"SecretId": name, "IncludeDeprecated": True, "MaxResults": 2}
                schema = spec["components"]["schemas"]["SecretsListSecretVersionIdsInput"]
                assert schema["required"] == ["SecretId"] and schema["additionalProperties"] is False
                assert set(schema["properties"]) == {"SecretId", "IncludeDeprecated", "MaxResults", "NextToken"}
                reference_request = urllib.request.Request(endpoint + "/", method="POST",
                    data=json.dumps(template, separators=(",", ":")).encode(),
                    headers={"X-Simulator-Token": token, "Content-Type": "application/x-amz-json-1.1",
                             "X-Amz-Target": "secretsmanager.ListSecretVersionIds"})
                with HTTP.open(reference_request, timeout=5) as response:
                    assert response.status == 200
                    assert response.headers.get_content_type() == "application/x-amz-json-1.1"
                    reference = json.load(response)
                assert reference["ARN"] == arn and reference["Name"] == name
                assert [v["VersionId"] for v in reference["Versions"]] == [a, b]
                assert "VersionStages" not in reference["Versions"][0]
                assert reference["Versions"][1]["VersionStages"] == ["AWSPREVIOUS"]
                assert reference["NextToken"] and "DUMMY-version-value" not in str(reference)
                sdk_by_id = {v["VersionId"]: v for v in default["Versions"] + first_page["Versions"]}
                for item in reference["Versions"]:
                    assert set(item) <= {"VersionId", "VersionStages", "CreatedDate"}
                    assert item["CreatedDate"] == sdk_by_id[item["VersionId"]]["CreatedDate"].timestamp()
                bad_reference = dict(template, SecretId="NOT_CREATED_STATIC_PLACEHOLDER")
                bad_request = urllib.request.Request(endpoint + "/", method="POST",
                    data=json.dumps(bad_reference).encode(),
                    headers={"X-Simulator-Token": token, "Content-Type": "application/x-amz-json-1.1",
                             "X-Amz-Target": "secretsmanager.ListSecretVersionIds"})
                try:
                    HTTP.open(bad_request, timeout=5)
                except urllib.error.HTTPError as exc:
                    assert exc.code == 400 and json.load(exc)["__type"] == "ResourceNotFoundException"
                else:
                    raise AssertionError("Unsubstituted nonexistent selector unexpectedly succeeded")
                # An actual served response example remains explanatory only;
                # no response example is accepted as a native request or live data.
                expect_error("InvalidNextTokenException", lambda: first.list_secret_version_ids(SecretId=arn, NextToken=cursor))
                expect_error("InvalidNextTokenException", lambda: peer.list_secret_version_ids(SecretId=name, IncludeDeprecated=True, NextToken=cursor))
                expect_error("InvalidNextTokenException", lambda: west.list_secret_version_ids(SecretId=name, IncludeDeprecated=True, NextToken=cursor))
                expect_error("InvalidParameterException", lambda: west.list_secret_version_ids(SecretId=arn))
                expect_error("InvalidNextTokenException", lambda: first.list_secret_version_ids(SecretId=arn, IncludeDeprecated=True, NextToken=cursor + "x"))
                assert metadata(west.list_secret_version_ids(SecretId=name))["ARN"] == west_arn
                assert metadata(peer.list_secret_version_ids(SecretId=name))["ARN"] == peer_arn
                for destination, wrong in ((endpoint, peer_token), (peer_endpoint, token)):
                    for credential in (wrong, ""):
                        foreign = client("secretsmanager", destination, credential)
                        expect_error(None, lambda: foreign.list_secret_version_ids(SecretId=name), status=401)
                assert frame(first_path) == saved, "Read/error/cross-token calls changed secret state"
                trust = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}]}
                iam.create_role(RoleName=role, AssumeRolePolicyDocument=json.dumps(trust))
                iam.put_role_policy(RoleName=role, PolicyName="list-only", PolicyDocument=json.dumps({"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "secretsmanager:ListSecretVersionIds", "Resource": arn}]}))
                participant = control(endpoint, token, "/_simulator/v1/access", {"Principal": role})["AccessToken"]
                reader = client("secretsmanager", endpoint, participant)
                assert metadata(reader.list_secret_version_ids(SecretId=name))["ARN"] == arn
                expect_error("AccessDeniedException", lambda: reader.get_secret_value(SecretId=arn), status=403)
                west_reader = client("secretsmanager", endpoint, participant, "us-west-2")
                expect_error("AccessDeniedException", lambda: west_reader.list_secret_version_ids(SecretId=west_arn), status=403)
                iam.put_role_policy(RoleName=role, PolicyName="explicit-deny", PolicyDocument=json.dumps({"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Action": "secretsmanager:ListSecretVersionIds", "Resource": arn}]}))
                expect_error("AccessDeniedException", lambda: reader.list_secret_version_ids(SecretId=name), status=403)
                iam.delete_role_policy(RoleName=role, PolicyName="explicit-deny")
                assert metadata(reader.list_secret_version_ids(SecretId=arn))["ARN"] == arn
                assert frame(first_path) == saved, "Role changes/list reads changed secret resources"
                metadata(peer.delete_secret(SecretId=peer_arn, ForceDeleteWithoutRecovery=True))
            # Both processes have exited; reopen the same DB with a fresh local
            # token authority. The persistent HMAC cursor remains usable.
            with running(db=first_path, authenticated=True) as (endpoint, token):
                first, iam = client("secretsmanager", endpoint, token), client("iam", endpoint, token)
                west = client("secretsmanager", endpoint, token, "us-west-2")
                response = metadata(first.list_secret_version_ids(SecretId=name, IncludeDeprecated=True, NextToken=cursor))
                assert [v["VersionId"] for v in response["Versions"]] == [b, c]
                metadata(first.delete_secret(SecretId=arn, ForceDeleteWithoutRecovery=True))
                expect_error("ResourceNotFoundException", lambda: first.list_secret_version_ids(SecretId=arn))
                replacement = metadata(first.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-version-value-recreated"))["ARN"]
                assert replacement != arn
                expect_error("InvalidNextTokenException", lambda: first.list_secret_version_ids(SecretId=name, IncludeDeprecated=True, NextToken=cursor))
                expect_error("ResourceNotFoundException", lambda: first.list_secret_version_ids(SecretId=arn))
                metadata(first.delete_secret(SecretId=replacement, ForceDeleteWithoutRecovery=True))
                metadata(west.delete_secret(SecretId=west_arn, ForceDeleteWithoutRecovery=True))
                iam.delete_role_policy(RoleName=role, PolicyName="list-only")
                iam.delete_role(RoleName=role)
                assert metadata(first.list_secrets())["SecretList"] == []
                assert metadata(west.list_secrets())["SecretList"] == []
            for state in (first_path, peer_path):
                with sqlite3.connect(f"file:{state}?mode=ro", uri=True) as db:
                    for table in ("aws_secrets", "aws_secret_versions", "aws_secret_stages"):
                        assert db.execute("SELECT count(*) FROM " + table).fetchone()[0] == 0
                    assert db.execute("SELECT count(*) FROM aws_iam_roles WHERE deleted=0").fetchone()[0] == 0
                    assert db.execute("SELECT count(*) FROM aws_iam_role_policies").fetchone()[0] == 0
        finally:
            for sdk, _ in tracked:
                sdk.close()
    for _, counts in tracked:
        assert counts["calls"] > 0 and counts["calls"] == counts["sends"] == counts["responses"], "Unbalanced actual SDK transport"
    report = {"clients": [counts for _, counts in tracked], "calls": sum(x["calls"] for _, x in tracked), "sends": sum(x["sends"] for _, x in tracked), "responses": sum(x["responses"] for _, x in tracked), "officialSDK": "boto3", "servedStaticExampleHTTP": 1, "servedPlaceholderRefusalHTTP": 1, "scope": "2localCLI/DB/token authorities, metadata-only version listing, IAM role Allow/Deny, region/cursor/restart/recreation/cleanup"}
    (REPORTS / "secrets-versions-sdk.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Secrets versions real official SDK/2CLI/metadata/IAM/cursor/restart/cleanup PASS")


if __name__ == "__main__":
    run()
