"""Real binary Logs SDK/restart/two-team contract; no AWS endpoint or VM guest."""
import json
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

from check import Config, ClientError, boto3, running


def client(endpoint, token, region="us-east-1", audit=None):
    result = boto3.client("logs", endpoint_url=endpoint, region_name=region,
                          aws_access_key_id="TCSIMLOGS", aws_secret_access_key="dummy",
                          config=Config(retries={"max_attempts": 0}))
    result.meta.events.register("before-sign", lambda request, **kwargs:
                                request.headers.add_header("X-Simulator-Token", token))
    # Count actual botocore calls/sends/responses, including expected errors.
    counts = {"calls": 0, "sends": 0, "responses": 0, "operations": {}}
    def before_call(model, **kwargs):
        counts["calls"] += 1
        counts["operations"][model.name] = counts["operations"].get(model.name, 0) + 1
    def local_only(request, **kwargs):
        assert request.url == endpoint + "/", "SDK selected a non-owned root endpoint"
        counts["sends"] += 1
    def after_call(**kwargs):
        counts["responses"] += 1
    result.meta.events.register("before-call", before_call)
    result.meta.events.register("before-send", local_only)
    result.meta.events.register("after-call", after_call)
    if audit is not None:
        audit.append((result, counts))
    return result


def error(code, action):
    try:
        action()
    except ClientError as exc:
        assert exc.response["Error"]["Code"] == code, exc.response["Error"]
    else:
        raise AssertionError("Expected " + code)


def control(endpoint, token, path, body):
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json",
                                              "X-Simulator-Token": token},
                                     method="POST" if path.endswith("/access") else "PUT")
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


# Independent exact main d166f56 inventory plus the bounded S3 multipart POST.
# Never derive these expectations from the served document being checked.
from test_sakura_openapi import NEW_IDS, COLLECTION, ITEM

MAIN_OPERATION_IDS = {'AzureListBlobs', 'DeleteIdentityPolicy', 'ListObjectsV2OrGetBucketLocation', 'UpdateFunctionCode', 'AzureDeleteContainer', 'InvokeLocalRESTProxyPost', 'DeleteResource', 'GCPCreateBucket', 'GetRestApis', 'ListPrincipals', 'GetRestApi', 'AzureDeleteBlob', 'GCPUploadObject', 'ListBuckets', 'DeleteFunction', 'AzureGetContainerProperties', 'GetFunctionConfiguration', 'Invoke', 'GCPGetObject', 'GetPolicy', 'HeadObject', 'PutMethod', 'GetResource', 'InvokeLocalRESTProxyGet', 'GetObject', 'GetStage', 'RemovePermission', 'AzureCreateContainer', 'GCPDownloadObject', 'HeadBucket', 'DeleteRestApi', 'PutObject', 'AzureGetBlob', 'CreateFunction', 'GCPListBuckets', 'GCPGetBucket', 'GCPDeleteBucket', 'CreateRestApi', 'GCPDeleteObject', 'CreateBucket', 'DeleteStage', 'CreateStage', 'GetResources', 'AzurePutBlob', 'GetDeployment', 'GetMethod', 'AzureGetBlobProperties', 'CreateDeployment', 'IAMRoleQueryOperation', 'PutIdentityPolicy', 'CreateResource', 'AzureListContainers', 'Capabilities', 'DeleteObject', 'PutIntegration', 'GCPListObjects', 'GetIdentityPolicy', 'AddPermission', 'DeleteBucket', 'awsRegionalOperation', 'ListFunctions', 'S3MultipartUpload'}
MAIN_PATHS = {'/2015-03-31/functions/{FunctionName}/code', '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o', '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}/integration', '/restapis/{RestApiId}/deployments/{DeploymentId}', '/_simulator/providers/aws/_execute-api/{Region}/{RestApiId}/{StageName}/{ProxyPath}', '/_simulator/providers/azure/{StorageAccount}', '/_simulator/v1/iam/principals', '/_simulator/providers/gcp/storage/v1/b', '/restapis/{RestApiId}/stages', '/restapis/{RestApiId}/resources', '/2015-03-31/functions/{FunctionName}/invocations', '/_simulator/providers/gcp/upload/storage/v1/b/{BucketName}/o', '/2015-03-31/functions', '/_simulator/v1/iam/principals/{principal}/policy', '/{bucket}/{key}', '/_simulator/providers/azure/{StorageAccount}/{ContainerName}/{BlobName}', '/_simulator/v1/capabilities', '/2015-03-31/functions/{FunctionName}/configuration', '/2015-03-31/functions/{FunctionName}/policy', '/restapis/{RestApiId}', '/_simulator/providers/gcp/storage/v1/b/{BucketName}', '/{bucket}', '/', '/2015-03-31/functions/{FunctionName}/policy/{StatementId}', '/2015-03-31/functions/{FunctionName}', '/restapis/{RestApiId}/stages/{StageName}', '/restapis/{RestApiId}/resources/{ResourceId}/methods/{HttpMethod}', '/restapis/{RestApiId}/deployments', '/restapis/{RestApiId}/resources/{ResourceId}', '/_simulator/providers/azure/{StorageAccount}/{ContainerName}', '/restapis', '/_simulator/providers/gcp/download/storage/v1/b/{BucketName}/o/{ObjectName}', '/_simulator/providers/aws/', '/_simulator/providers/gcp/storage/v1/b/{BucketName}/o/{ObjectName}'}


def verify_served_reference(endpoint, token, sdk):
    def read(path):
        request = urllib.request.Request(endpoint + path, headers={"X-Simulator-Token": token})
        with urllib.request.urlopen(request, timeout=5) as response:
            assert response.status == 200
            return json.load(response)
    spec = read("/_simulator/docs/openapi.json")
    identities = [item["operationId"] for path in spec["paths"].values() for item in path.values()
                  if isinstance(item, dict) and "operationId" in item]
    assert len(identities) == len(set(identities)) == 67, "Duplicated/missing operation ID"
    assert set(identities) == MAIN_OPERATION_IDS | NEW_IDS, "Existing main operation inventory changed"
    assert set(spec["paths"]) == MAIN_PATHS | {COLLECTION, ITEM} and len(spec["paths"]) == 36, "Existing main path inventory changed"
    assert not any("logs" in path.lower() for path in spec["paths"]), "Do not invent a Logs URL"
    examples = spec["paths"]["/"]["post"]["requestBody"]["content"]["application/x-amz-json-1.1"]["examples"]
    required = {"CreateLogGroup", "DescribeLogGroups", "DeleteLogGroup", "CreateLogStream", "DescribeLogStreams", "PutLogEvents", "GetLogEvents"}
    capabilities = read("/_simulator/v1/capabilities")["services"]["logs"]
    assert set(capabilities["operations"]) == required
    assert all(capabilities[feature] is False for feature in ("metrics", "alarms", "cloudFormation", "automaticLambdaDelivery"))
    # Use the actual served static examples, replacing the documented timestamp
    # template with the current clock. A static example is not preexisting state.
    for op in ("CreateLogGroup", "DescribeLogGroups", "CreateLogStream", "DescribeLogStreams", "PutLogEvents", "GetLogEvents", "DeleteLogGroup"):
        body = json.loads(json.dumps(examples["Logs" + op]["value"]))
        if op == "PutLogEvents":
            assert body["logEvents"][0]["timestamp"] == 0
            body["logEvents"][0] = {"timestamp": int(time.time() * 1000), "message": "reference-native-bytes"}
        request = urllib.request.Request(endpoint + "/", data=json.dumps(body).encode(), method="POST",
            headers={"X-Simulator-Token": token, "Content-Type": "application/x-amz-json-1.1",
                     "X-Amz-Target": "Logs_20140328." + op, "X-Simulator-Service": "logs", "X-Simulator-Region": "us-east-1"})
        with urllib.request.urlopen(request, timeout=5) as response:
            assert response.status == 200
            raw = response.read()
            result = json.loads(raw) if raw else None
        if op in ("CreateLogGroup", "CreateLogStream", "DeleteLogGroup"):
            assert result is None, "Native Logs control success has an empty body"
        if op == "GetLogEvents":
            assert [item["message"] for item in result["events"]] == ["reference-native-bytes"]
            assert sdk.get_log_events(logGroupName=body["logGroupName"], logStreamName=body["logStreamName"], startFromHead=True)["events"] == result["events"]
    assert sdk.describe_log_groups()["logGroups"] == []
    return {"servedOpenAPI": True, "operationIDsPreserved": 67, "pathsPreserved": 36, "sharedRootExamplesExecuted": 7,
            "staticTimestampExplicitlyReplaced": True, "SDKSameSQLiteBytes": True, "cleanup": "DELETED"}


def run():
    audit = []
    try:
        with tempfile.TemporaryDirectory(prefix="logs-two-teams-") as directory:
            state = Path(directory) / "team-a.db"
            other_state = Path(directory) / "team-b.db"
            now = int(time.time() * 1000)
            group, stream = "/aws/simulator-example", "application"
            with running(db=state, authenticated=True) as (endpoint, token), \
                    running(db=other_state, authenticated=True) as (other_endpoint, other_token):
                first, second = client(endpoint, token, audit=audit), client(other_endpoint, other_token, audit=audit)
                reference_proof = verify_served_reference(endpoint, token, first)
                for sdk, value in ((first, "team-a"), (second, "team-b")):
                    sdk.create_log_group(logGroupName=group)
                    sdk.create_log_stream(logGroupName=group, logStreamName=stream)
                    sdk.put_log_events(logGroupName=group, logStreamName=stream,
                                       sequenceToken="intentionally-invalid-ignored",
                                       logEvents=[{"timestamp": now, "message": value},
                                                  {"timestamp": now + 1, "message": value + "-next"}])
                assert first.describe_log_groups()["logGroups"][0]["logGroupName"] == group
                assert first.describe_log_streams(logGroupName=group)["logStreams"][0]["logStreamName"] == stream
                page = first.get_log_events(logGroupName=group, logStreamName=stream,
                                            startFromHead=True, limit=1)
                assert page["events"][0]["message"] == "team-a"
                saved_ingestion = page["events"][0]["ingestionTime"]
                token_after_first = page["nextForwardToken"]
                error("InvalidParameterException", lambda: second.get_log_events(
                    logGroupName=group, logStreamName=stream, startFromHead=True,
                    nextToken=token_after_first))
                assert second.get_log_events(logGroupName=group, logStreamName=stream,
                                             startFromHead=True)["events"][0]["message"] == "team-b"
                error("ResourceNotFoundException", lambda: client(endpoint, token, "us-west-2", audit=audit).get_log_events(
                    logGroupName=group, logStreamName=stream))
                try:
                    urllib.request.urlopen(urllib.request.Request(
                        other_endpoint + "/_simulator/health", headers={"X-Simulator-Token": token}), timeout=5)
                except urllib.error.HTTPError as exc:
                    assert exc.code == 401
                else:
                    raise AssertionError("A team's local token crossed authorities")
                resource = "arn:aws:logs:us-east-1:000000000000:log-group:" + group + ":log-stream:" + stream
                policy = {"Version": "2012-10-17", "Statement": [
                    {"Effect": "Allow", "Action": "logs:GetLogEvents", "Resource": resource}]}
                control(endpoint, token, "/_simulator/v1/iam/principals/log-reader/policy", policy)
                participant = control(endpoint, token, "/_simulator/v1/access", {"Principal": "log-reader"})["AccessToken"]
                limited = client(endpoint, participant, audit=audit)
                assert limited.get_log_events(logGroupName=group, logStreamName=stream,
                                              startFromHead=True)["events"][0]["message"] == "team-a"
                policy["Statement"].append({"Effect": "Deny", "Action": "logs:GetLogEvents", "Resource": "*"})
                control(endpoint, token, "/_simulator/v1/iam/principals/log-reader/policy", policy)
                error("AccessDeniedException", lambda: limited.get_log_events(
                    logGroupName=group, logStreamName=stream, startFromHead=True, nextToken=token_after_first))
                rejected = first.put_log_events(logGroupName=group, logStreamName=stream, logEvents=[
                    {"timestamp": now - 15 * 86400000, "message": "old"},
                    {"timestamp": now + 2, "message": "accepted"},
                    {"timestamp": now + 3 * 3600000, "message": "future"}])["rejectedLogEventsInfo"]
                assert rejected == {"tooOldLogEventEndIndex": 0, "tooNewLogEventStartIndex": 2}, rejected
                second.delete_log_group(logGroupName=group)
                assert second.describe_log_groups()["logGroups"] == []
                error("ResourceNotFoundException", lambda: second.get_log_events(
                    logGroupName=group, logStreamName=stream))
            # The real process is gone. Restart from exactly the saved database;
            # cursor signatures and ingestion timestamps must remain valid.
            with running(db=state, authenticated=True) as (endpoint, token):
                sdk = client(endpoint, token, audit=audit)
                page = sdk.get_log_events(logGroupName=group, logStreamName=stream,
                                          startFromHead=True, nextToken=token_after_first)
                assert [event["message"] for event in page["events"]] == ["team-a-next", "accepted"]
                first = sdk.get_log_events(logGroupName=group, logStreamName=stream,
                                           startFromHead=True, endTime=now + 1)
                assert len(first["events"]) == 1 and first["events"][0]["ingestionTime"] == saved_ingestion
                end = sdk.get_log_events(logGroupName=group, logStreamName=stream,
                                        startFromHead=True, nextToken=page["nextForwardToken"])
                assert end["events"] == [] and end["nextForwardToken"] == page["nextForwardToken"]
                sdk.delete_log_group(logGroupName=group)
                assert sdk.describe_log_groups()["logGroups"] == []
                error("ResourceNotFoundException", lambda: sdk.get_log_events(logGroupName=group, logStreamName=stream))
                sdk.create_log_group(logGroupName=group)
                sdk.create_log_stream(logGroupName=group, logStreamName=stream)
                error("InvalidParameterException", lambda: sdk.get_log_events(
                    logGroupName=group, logStreamName=stream, startFromHead=True, nextToken=token_after_first))
                sdk.delete_log_group(logGroupName=group)
                assert sdk.describe_log_groups()["logGroups"] == []
            with running(db=other_state, authenticated=True) as (endpoint, token):
                peer = client(endpoint, token, audit=audit)
                assert peer.describe_log_groups()["logGroups"] == []

        counts = {key: sum(item[key] for _, item in audit) for key in ("calls", "sends", "responses")}
        operations = {name for _, item in audit for name in item["operations"]}
        required = {"CreateLogGroup", "DescribeLogGroups", "DeleteLogGroup", "CreateLogStream", "DescribeLogStreams", "PutLogEvents", "GetLogEvents"}
        assert operations == required, operations
        assert counts["calls"] > 0 and counts["calls"] == counts["sends"] == counts["responses"], counts
        print(json.dumps({"nativeSDK": "boto3 " + boto3.__version__, "operations": sorted(operations), "counts": counts,
                          "twoAuthenticatedProcesses": True, "twoPersistentSQLiteDatabases": True,
                          "sameNamesIndependentState": True, "crossToken401": True,
                          "cursorAndIngestionSurviveRestart": True, "recreatedStreamRejectsOldCursor": True,
                          "continuationReauthorizesIAM": True, "cleanup": "DELETED",
                          "peerCleanupPersisted": True, "reference": reference_proof, "realAWS": False, "uiTested": False}))
    finally:
        for sdk, _ in audit:
            sdk.close()


if __name__ == "__main__":
    run()
