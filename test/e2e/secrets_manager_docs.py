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


# Independent literal inventory from immutable main5c97dc5916fa39fd43d80ae79532d5892fcb30ea.
# OpenAPI source SHA256 8feac717622e4e2208d82f6a497cbfe2b1d801c76d81a4c381849fe304551943.
# Do not derive expectations from the served document under test.
EXPECTED_OPERATION_IDS = frozenset({
    'AddPermission',
    'AzureCreateContainer',
    'AzureDeleteBlob',
    'AzureDeleteContainer',
    'AzureGetBlob',
    'AzureGetBlobProperties',
    'AzureGetContainerProperties',
    'AzureListBlobs',
    'AzureListContainers',
    'AzurePutBlob',
    'Capabilities',
    'CreateBucket',
    'CreateDeployment',
    'CreateFunction',
    'CreateResource',
    'CreateRestApi',
    'CreateStage',
    'DeleteBucket',
    'DeleteFunction',
    'DeleteIdentityPolicy',
    'DeleteObject',
    'DeleteResource',
    'DeleteRestApi',
    'DeleteStage',
    'GCPCreateBucket',
    'GCPDeleteBucket',
    'GCPDeleteObject',
    'GCPDownloadObject',
    'GCPGetBucket',
    'GCPGetObject',
    'GCPListBuckets',
    'GCPListObjects',
    'GCPUploadObject',
    'GetDeployment',
    'GetFunctionConfiguration',
    'GetIdentityPolicy',
    'GetMethod',
    'GetObject',
    'GetPolicy',
    'GetResource',
    'GetResources',
    'GetRestApi',
    'GetRestApis',
    'GetStage',
    'HeadBucket',
    'HeadObject',
    'IAMRoleQueryOperation',
    'Invoke',
    'InvokeLocalRESTProxyGet',
    'InvokeLocalRESTProxyPost',
    'ListBuckets',
    'ListFunctions',
    'ListObjectsV2OrGetBucketLocation',
    'ListPrincipals',
    'PutIdentityPolicy',
    'PutIntegration',
    'PutMethod',
    'PutObject',
    'RemovePermission',
    'UpdateFunctionCode',
    'awsRegionalOperation',
})
EXPECTED_PATHS = frozenset({
    '/',
    '/2015-03-31/functions',
    '/2015-03-31/functions/{FunctionName}',
    '/2015-03-31/functions/{FunctionName}/code',
    '/2015-03-31/functions/{FunctionName}/configuration',
    '/2015-03-31/functions/{FunctionName}/invocations',
    '/2015-03-31/functions/{FunctionName}/policy',
    '/2015-03-31/functions/{FunctionName}/policy/{StatementId}',
    '/_simulator/providers/aws/',
    '/_simulator/providers/aws/_execute-api/{Region}/{RestApiId}/{StageName}/{ProxyPath}',
    '/_simulator/providers/azure/{StorageAccount}',
    '/_simulator/providers/azure/{StorageAccount}/{ContainerName}',
    '/_simulator/providers/azure/{StorageAccount}/{ContainerName}/{BlobName}',
    '/_simulator/providers/gcp/download/storage/v1/b/{BucketName}/o/{ObjectName}',
    '/_simulator/providers/gcp/storage/v1/b',
    '/_simulator/providers/gcp/storage/v1/b/{BucketName}',
    '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o',
    '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o/{ObjectName}',
    '/_simulator/providers/gcp/upload/storage/v1/b/{BucketName}/o',
    '/_simulator/v1/capabilities',
    '/_simulator/v1/iam/principals',
    '/_simulator/v1/iam/principals/{principal}/policy',
    '/restapis',
    '/restapis/{RestApiId}',
    '/restapis/{RestApiId}/deployments',
    '/restapis/{RestApiId}/deployments/{DeploymentId}',
    '/restapis/{RestApiId}/resources',
    '/restapis/{RestApiId}/resources/{ResourceId}',
    '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}',
    '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}/integration',
    '/restapis/{RestApiId}/stages',
    '/restapis/{RestApiId}/stages/{StageName}',
    '/{bucket}',
    '/{bucket}/{key}',
})


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
        assert len(ids) == len(set(ids)) == 61, "Document duplicated or omitted a native operation"
        assert set(ids) == EXPECTED_OPERATION_IDS, "Served operation inventory differs from immutable main contract"
        assert len(spec["paths"]) == 34 and set(spec["paths"]) == EXPECTED_PATHS, "Served path inventory differs from immutable main contract"
        assert not any("secretsmanager" in path.lower() for path in spec["paths"]), "Secrets native API uses the actual shared root"
        names = ["CreateSecret", "DescribeSecret", "ListSecrets", "PutSecretValue",
                 "GetSecretValue", "UpdateSecretVersionStage", "DeleteSecret"]
        examples = root["requestBody"]["content"]["application/x-amz-json-1.1"]["examples"]
        target = next(p for p in root["parameters"] if p.get("name") == "X-Amz-Target")
        assert target["schema"]["type"] == "string", "Shared target header must not exclude other native services"
        assert "secretsmanager.<operation>" in target["description"]
        assert all(name in examples for name in names)
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
