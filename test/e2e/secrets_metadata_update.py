"""Description-only UpdateSecret through official boto3 and normal local CLIs.

Two authenticated processes/databases, restart and cleanup. No browser or cloud.
Target-specific document controls run before the live fixture.
"""
import copy
import json
import sqlite3
import tempfile
from pathlib import Path

from check import Config, ClientError, boto3, running
from secrets_versions import HTTP, frame, origin, expect_error as inherited_expect_error


def expect_error(code, action, status=400):
    # Retain inherited status/error assertions and cover this fixture's own
    # dummy sentinel in both the parsed SDK response and exception text.
    def checked():
        try:
            return action()
        except ClientError as exc:
            assert "DUMMY-metadata-" not in str(exc.response), "SDK error response exposed dummy secret"
            assert "DUMMY-metadata-" not in str(exc), "SDK error text exposed dummy secret"
            raise
    inherited_expect_error(code, checked, status)


def contract(spec):
    root = spec["paths"]["/"]["post"]
    mapping = root["x-simulator-secrets-operation-schemas"]["secretsmanager.UpdateSecret"]
    assert mapping["request"] == "#/components/schemas/SecretsUpdateSecretInput"
    assert mapping["response"] == "#/components/schemas/SecretsUpdateSecretOutput"
    assert mapping["permission"] == "secretsmanager:UpdateSecret on the resolved saved ARN"
    schemas = spec["components"]["schemas"]
    request, response = schemas["SecretsUpdateSecretInput"], schemas["SecretsUpdateSecretOutput"]
    assert request["type"] == response["type"] == "object"
    assert request["additionalProperties"] is False and response["additionalProperties"] is False
    assert set(request["required"]) == {"SecretId", "Description"}
    assert request["properties"] == {
        "SecretId": {"type": "string", "minLength": 1, "maxLength": 2048},
        "Description": {"type": "string", "maxLength": 2048},
        "ClientRequestToken": {"type": "string", "minLength": 32, "maxLength": 64}}
    assert set(response["required"]) == {"ARN", "Name"}
    assert response["properties"] == {"ARN": {"type": "string"}, "Name": {"type": "string"}}
    incoming = root["requestBody"]["content"]["application/x-amz-json-1.1"]
    outgoing = root["responses"]["200"]["content"]["application/x-amz-json-1.1"]
    assert {"$ref": mapping["request"]} in incoming["schema"]["anyOf"]
    assert {"$ref": mapping["response"]} in outgoing["schema"]["anyOf"]
    assert incoming["examples"]["UpdateSecret"]["value"] == {
        "SecretId": "training/metadata-update", "Description": "Updated practice description"}
    assert set(outgoing["examples"]["UpdateSecret"]["value"]) == {"ARN", "Name"}


def contract_controls(spec):
    contract(spec)
    cases = [
        ("required-description", lambda s: s["required"].remove("Description")),
        ("unknown-fields", lambda s: s.update(additionalProperties=True)),
        ("token-min", lambda s: s["properties"]["ClientRequestToken"].update(minLength=31)),
        ("token-max", lambda s: s["properties"]["ClientRequestToken"].update(maxLength=65)),
        ("description-limit", lambda s: s["properties"]["Description"].update(maxLength=2049)),
        ("description-clear", lambda s: s["properties"]["Description"].update(minLength=1)),
        ("value", lambda s: s["properties"].update(SecretString={"type": "string"})),
        ("binary", lambda s: s["properties"].update(SecretBinary={"type": "string"})),
        ("kms", lambda s: s["properties"].update(KmsKeyId={"type": "string"})),
    ]
    for name, mutate in cases:
        changed = copy.deepcopy(spec)
        mutate(changed["components"]["schemas"]["SecretsUpdateSecretInput"])
        try:
            contract(changed)
        except (AssertionError, KeyError):
            continue
        raise AssertionError("Contract detector missed " + name)
    for name in ("mapping", "request-example", "response-example", "response-version"):
        changed = copy.deepcopy(spec)
        root = changed["paths"]["/"]["post"]
        if name == "mapping":
            del root["x-simulator-secrets-operation-schemas"]["secretsmanager.UpdateSecret"]
        elif name == "request-example":
            del root["requestBody"]["content"]["application/x-amz-json-1.1"]["examples"]["UpdateSecret"]
        elif name == "response-example":
            del root["responses"]["200"]["content"]["application/x-amz-json-1.1"]["examples"]["UpdateSecret"]
        else:
            changed["components"]["schemas"]["SecretsUpdateSecretOutput"]["properties"]["VersionId"] = {"type": "string"}
        try:
            contract(changed)
        except (AssertionError, KeyError):
            continue
        raise AssertionError("Contract detector missed " + name)


def request(endpoint, token, path, body=None, method="GET", target=None, status=200):
    import urllib.request
    import urllib.error
    origin(endpoint)
    assert path in ("/", "/_simulator/docs/openapi.json", "/_simulator/v1/capabilities",
                    "/_simulator/v1/access", "/_simulator/v1/iam/principals/metadata-author/policy")
    headers = {"X-Simulator-Token": token}
    if body is not None:
        headers["Content-Type"] = "application/x-amz-json-1.1" if target else "application/json"
    if target:
        headers["X-Amz-Target"] = "secretsmanager." + target
    req = urllib.request.Request(endpoint + path, data=None if body is None else json.dumps(body).encode(),
                                 method=method, headers=headers)
    try:
        response = HTTP.open(req, timeout=5)
    except urllib.error.HTTPError as exc:
        response = exc
    with response:
        assert response.status == status, "Unexpected native status"
        result = json.load(response)
    assert "DUMMY-metadata-" not in str(result), "Non-value response exposed dummy secret"
    return result


def stable_metadata_frame(snapshot):
    # Preserve complete private values/versions/stages/settings/retired identities;
    # only description and changed are omitted from the metadata comparison.
    rows = tuple(row[:3] + (row[4],) for row in snapshot[2])
    return snapshot[:2] + (rows,) + snapshot[3:]


def run():
    contract_controls(json.loads((Path(__file__).resolve().parents[2] / "internal/server/apidocs/openapi.json").read_text()))
    clients = []

    def client(endpoint, token, region="us-east-1"):
        allocated = origin(endpoint)
        sdk = boto3.client("secretsmanager", endpoint_url=endpoint, region_name=region,
            aws_access_key_id="TCSIMMETADATA", aws_secret_access_key="dummy-local-only",
            config=Config(proxies={}, retries={"max_attempts": 0}, connect_timeout=2, read_timeout=5))
        evidence = {"calls": 0, "sends": 0, "responses": 0}
        sdk.meta.events.register("before-sign", lambda request, **kwargs: request.headers.add_header("X-Simulator-Token", token))
        def counted(**kwargs):
            evidence["calls"] += 1
        def sending(request, **kwargs):
            from urllib.parse import urlsplit
            u = urlsplit(request.url.decode() if isinstance(request.url, bytes) else request.url)
            assert (u.scheme, u.hostname, u.port) == allocated and u.path == "/"
            assert not u.query and not u.fragment and not u.username and not u.password
            evidence["sends"] += 1
        def completed(http_response, **kwargs):
            assert not 300 <= http_response.status_code < 400
            evidence["responses"] += 1
        sdk.meta.events.register("before-call", counted)
        sdk.meta.events.register("before-send", sending)
        sdk.meta.events.register("after-call", completed)
        clients.append((sdk, evidence))
        return sdk

    def update(sdk, arn, **kwargs):
        result = sdk.update_secret(**kwargs)
        assert {k: v for k, v in result.items() if k != "ResponseMetadata"} == {"ARN": arn, "Name": "training/metadata-update"}
        headers = result["ResponseMetadata"]["HTTPHeaders"]
        assert headers["cache-control"] == "no-store" and headers["x-simulator-encryption"] == "none"

    a, b = "a" * 32, "b" * 32
    name = "training/metadata-update"
    with tempfile.TemporaryDirectory(prefix="secret-metadata-sdk-") as directory:
        first_path, peer_path = Path(directory) / "a.db", Path(directory) / "b.db"
        with running(db=first_path, authenticated=True) as (endpoint, token), running(db=peer_path, authenticated=True) as (other_endpoint, other_token):
            sdk, peer, west = client(endpoint, token), client(other_endpoint, other_token), client(endpoint, token, "us-west-2")
            arn = sdk.create_secret(Name=name, Description="original", ClientRequestToken=a, SecretString="DUMMY-metadata-A")["ARN"]
            peer_arn = peer.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-metadata-peer")["ARN"]
            west_arn = west.create_secret(Name=name, ClientRequestToken=a, SecretString="DUMMY-metadata-west")["ARN"]
            sdk.put_secret_value(SecretId=arn, ClientRequestToken=b, SecretString="DUMMY-metadata-B")
            saved, peer_saved = frame(first_path), frame(peer_path)
            versions = sdk.describe_secret(SecretId=arn)["VersionIdsToStages"]
            west_before = west.describe_secret(SecretId=name)
            caps = request(endpoint, token, "/_simulator/v1/capabilities")["services"]["secretsmanager"]
            assert "UpdateSecret" in caps["operations"] and caps["updateSecret"] == "Description only; empty clears; no value, KMS key, stage or rotation changes"
            spec = request(endpoint, token, "/_simulator/docs/openapi.json")
            contract_controls(spec)
            template = spec["paths"]["/"]["post"]["requestBody"]["content"]["application/x-amz-json-1.1"]["examples"]["UpdateSecret"]["value"]
            assert request(endpoint, token, "/", template, "POST", "UpdateSecret") == {"ARN": arn, "Name": name}
            assert sdk.describe_secret(SecretId=arn)["Description"] == template["Description"]
            update(sdk, arn, SecretId=name, Description="", ClientRequestToken=a)
            assert sdk.describe_secret(SecretId=arn).get("Description", "") == ""
            update(sdk, arn, SecretId=arn, Description="same")  # SDK generates the optional token.
            changed = sdk.describe_secret(SecretId=arn)["LastChangedDate"]
            update(sdk, arn, SecretId=arn, Description="same", ClientRequestToken="界" * 32)
            # Wall-clock SDK evidence is non-regression only; the controlled-clock
            # Go regression proves timestamp refresh for an identical description.
            assert sdk.describe_secret(SecretId=arn)["LastChangedDate"] >= changed
            policy = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "secretsmanager:UpdateSecret", "Resource": arn}]}
            path = "/_simulator/v1/iam/principals/metadata-author/policy"
            request(endpoint, token, path, policy, "PUT")
            participant = request(endpoint, token, "/_simulator/v1/access", {"Principal": "metadata-author"}, "POST")["AccessToken"]
            limited = client(endpoint, participant)
            update(limited, arn, SecretId=name, Description="authorized metadata")
            expect_error("AccessDeniedException", lambda: limited.get_secret_value(SecretId=arn), 403)
            assert stable_metadata_frame(frame(first_path)) == stable_metadata_frame(saved)
            assert frame(peer_path) == peer_saved
            assert {k: v for k, v in west.describe_secret(SecretId=name).items() if k != "ResponseMetadata"} == {k: v for k, v in west_before.items() if k != "ResponseMetadata"}
            policy["Statement"].append({"Effect": "Deny", "Action": "secretsmanager:UpdateSecret", "Resource": arn})
            request(endpoint, token, path, policy, "PUT")
            before = frame(first_path)
            expect_error("AccessDeniedException", lambda: limited.update_secret(SecretId=arn, Description="denied"), 403)
            assert frame(first_path) == before
            for body in ({"SecretId": name}, {"SecretId": name, "Description": None},
                         {"SecretId": name, "Description": "new", "ClientRequestToken": "a" * 31},
                         {"SecretId": name, "Description": "new", "ClientRequestToken": "a" * 65},
                         *({"SecretId": name, "Description": "new", field: ""} for field in ("SecretString", "SecretBinary", "KmsKeyId", "Type", "RotationEnabled"))):
                failure = request(endpoint, token, "/", body, "POST", "UpdateSecret", 400)
                assert failure["__type"] == "InvalidParameterException" and frame(first_path) == before
            expect_error("ResourceNotFoundException", lambda: sdk.update_secret(SecretId="missing", Description="new"))
            request(other_endpoint, token, "/", {"SecretId": name, "Description": "foreign"}, "POST", "UpdateSecret", 401)
            assert frame(first_path) == before and frame(peer_path) == peer_saved
            assert sdk.describe_secret(SecretId=arn)["VersionIdsToStages"] == versions
            assert sdk.get_secret_value(SecretId=arn, VersionId=a)["SecretString"] == "DUMMY-metadata-A"
            assert sdk.get_secret_value(SecretId=arn, VersionId=b)["SecretString"] == "DUMMY-metadata-B"
            peer.delete_secret(SecretId=peer_arn, ForceDeleteWithoutRecovery=True)
            west.delete_secret(SecretId=west_arn, ForceDeleteWithoutRecovery=True)
        with running(db=first_path, authenticated=True) as (endpoint, token):
            sdk = client(endpoint, token)
            assert sdk.describe_secret(SecretId=arn)["Description"] == "authorized metadata"
            assert sdk.describe_secret(SecretId=arn)["VersionIdsToStages"] == versions
            assert sdk.get_secret_value(SecretId=arn, VersionId=a)["SecretString"] == "DUMMY-metadata-A"
            update(sdk, arn, SecretId=arn, Description="after restart")
            sdk.delete_secret(SecretId=arn, ForceDeleteWithoutRecovery=True)
            expect_error("ResourceNotFoundException", lambda: sdk.update_secret(SecretId=arn, Description="deleted"))
            assert sdk.list_secrets()["SecretList"] == []
        for path in (first_path, peer_path):
            with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as db:
                assert all(db.execute("SELECT count(*) FROM " + table).fetchone()[0] == 0
                           for table in ("aws_secrets", "aws_secret_versions", "aws_secret_stages"))
    for sdk, evidence in clients:
        assert evidence["calls"] == evidence["sends"] == evidence["responses"] > 0
        sdk.close()
    print("UpdateSecret metadata: official SDK, served contract controls, two teams/regions, IAM, restart and cleanup PASS")


if __name__ == "__main__":
    run()
