"""Real binary Logs SDK/restart/two-team contract; no AWS endpoint or VM guest."""
import json
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

from check import Config, ClientError, boto3, running


def client(endpoint, token, region="us-east-1"):
    result = boto3.client("logs", endpoint_url=endpoint, region_name=region,
                          aws_access_key_id="TCSIMLOGS", aws_secret_access_key="dummy",
                          config=Config(retries={"max_attempts": 0}))
    result.meta.events.register("before-sign", lambda request, **kwargs:
                                request.headers.add_header("X-Simulator-Token", token))
    # Detect unintended endpoint selection before any SDK network request.
    def local_only(request, **kwargs):
        assert request.url.startswith(endpoint + "/"), "SDK selected a nonlocal endpoint"
    result.meta.events.register("before-send", local_only)
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


def run():
    with tempfile.TemporaryDirectory(prefix="logs-two-teams-") as directory:
        state = Path(directory) / "team-a.db"
        now = int(time.time() * 1000)
        group, stream = "/aws/simulator-example", "application"
        with running(db=state, authenticated=True) as (endpoint, token), \
                running(authenticated=True) as (other_endpoint, other_token):
            first, second = client(endpoint, token), client(other_endpoint, other_token)
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
            error("ResourceNotFoundException", lambda: client(endpoint, token, "us-west-2").get_log_events(
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
            limited = client(endpoint, participant)
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
        # The real process is gone. Restart from exactly the saved database;
        # cursor signatures and ingestion timestamps must remain valid.
        with running(db=state, authenticated=True) as (endpoint, token):
            sdk = client(endpoint, token)
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
    print("CloudWatch Logs real SDK/two-team/restart/IAM/cleanup contract PASS")


if __name__ == "__main__":
    run()
