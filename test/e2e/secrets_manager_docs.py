"""Execute the seven served OpenAPI request examples against the real binary.

Uses native root HTTP and boto3 shared-state assertions, not mocked responses.
This verifies the document contract, not Scalar's browser Send editor.
"""
import copy
import json
import urllib.error
import urllib.request

from check import running
from secrets_manager import client


def run():
    with running(authenticated=True) as (endpoint, token):
        req = urllib.request.Request(endpoint+"/_simulator/docs/openapi.json",
                                     headers={"X-Simulator-Token": token})
        with urllib.request.urlopen(req, timeout=5) as response:
            spec = json.load(response)
        root = spec["paths"]["/"]["post"]
        assert root["operationId"] == "awsRegionalOperation"
        ids = [entry["operationId"] for path in spec["paths"].values()
               for entry in path.values() if isinstance(entry, dict) and "operationId" in entry]
        assert len(ids) == len(set(ids)) == 43, "Document duplicated an operation or invented a Secrets URL"
        assert not any("secretsmanager" in path.lower() for path in spec["paths"]), "Secrets native API uses the actual shared root"
        names = ["CreateSecret", "DescribeSecret", "ListSecrets", "PutSecretValue",
                 "GetSecretValue", "UpdateSecretVersionStage", "DeleteSecret"]
        examples = root["requestBody"]["content"]["application/x-amz-json-1.1"]["examples"]
        targets = next(p for p in root["parameters"] if p["name"] == "X-Amz-Target")["schema"]["enum"]
        assert all("secretsmanager."+name in targets and name in examples for name in names)
        sdk = client(endpoint, token)

        def send(name, value):
            req = urllib.request.Request(endpoint+"/", data=json.dumps(value).encode(), method="POST",
                headers={"X-Simulator-Token": token, "Content-Type": "application/x-amz-json-1.1",
                         "X-Simulator-Service": "secretsmanager", "X-Simulator-Region": "us-east-1",
                         "X-Amz-Target": "secretsmanager."+name})
            with urllib.request.urlopen(req, timeout=5) as response:
                assert response.status == 200
                assert response.headers["Cache-Control"] == "no-store"
                assert response.headers["X-Simulator-Encryption"] == "none"
                result = json.load(response)
            assert "KmsKeyId" not in result
            if name != "GetSecretValue":
                assert "SecretString" not in result and "DUMMY-secret-" not in str(result)
            return result

        created = send("CreateSecret", copy.deepcopy(examples["CreateSecret"]["value"]))
        arn = created["ARN"]
        assert sdk.describe_secret(SecretId=arn)["VersionIdsToStages"] == {created["VersionId"]: ["AWSCURRENT"]}
        for name in ("DescribeSecret", "ListSecrets", "PutSecretValue", "GetSecretValue",
                     "UpdateSecretVersionStage", "DeleteSecret"):
            body = copy.deepcopy(examples[name]["value"])
            if "SecretId" in body:
                body["SecretId"] = arn
            result = send(name, body)
            if name == "PutSecretValue":
                assert sdk.get_secret_value(SecretId=arn)["SecretString"] == body["SecretString"]
            if name == "GetSecretValue":
                assert result["SecretString"] == examples["PutSecretValue"]["value"]["SecretString"]
            if name == "UpdateSecretVersionStage":
                assert sdk.get_secret_value(SecretId=arn)["VersionId"] == created["VersionId"]
        assert sdk.list_secrets()["SecretList"] == [], "Documented cleanup left a secret"
    print("Secrets Manager served OpenAPI seven real HTTP examples/native SDK shared-state PASS")


if __name__ == "__main__":
    run()
