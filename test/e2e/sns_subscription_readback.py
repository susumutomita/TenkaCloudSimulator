"""Bounded native SNS subscription read-back through official boto3 (local only)."""
import os
from urllib.parse import urlsplit

os.environ["AWS_EC2_METADATA_DISABLED"] = "true"
os.environ["AWS_CONFIG_FILE"] = "/dev/null"
os.environ["AWS_SHARED_CREDENTIALS_FILE"] = "/dev/null"

import boto3
from botocore.config import Config


def check_sdk(endpoint, token):
    origin = urlsplit(endpoint)
    assert origin.scheme == "http" and origin.hostname == "127.0.0.1" and origin.port
    assert not any((origin.path, origin.query, origin.fragment, origin.username, origin.password))
    sns = boto3.client(
        "sns", region_name="us-east-1", endpoint_url=endpoint,
        aws_access_key_id="TCSIMSNSREADBACK", aws_secret_access_key="dummy-local-only",
        config=Config(connect_timeout=3, read_timeout=5, retries={"total_max_attempts": 1}, proxies={}),
    )
    try:
        calls = []

        def authenticate(request, **_):
            request.headers.add_header("X-Simulator-Token", token)

        def local_only(request, **_):
            target = urlsplit(request.url)
            assert (target.scheme, target.netloc, target.path) == ("http", origin.netloc, "/")
            assert not target.query and not target.fragment and request.method == "POST"

        def observe(model, http_response, **_):
            calls.append((model.name, http_response.status_code))

        sns.meta.events.register("before-sign.sns", authenticate)
        sns.meta.events.register("before-send.sns", local_only)
        sns.meta.events.register("after-call.sns", observe)
        topic = sns.create_topic(Name="sns-subscription-sdk-readback")["TopicArn"]
        local_url = "https://127.0.0.1:18443/events?topic=sns&fixture=only"

        def assert_absent(arn):
            try:
                sns.get_subscription_attributes(SubscriptionArn=arn)
            except sns.exceptions.NotFoundException as e:
                assert e.response["ResponseMetadata"]["HTTPStatusCode"] == 404
            else:
                raise AssertionError("Deleted subscription read as retained")

        try:
            arn = sns.subscribe(TopicArn=topic, Protocol="https", Endpoint=local_url, ReturnSubscriptionArn=True)["SubscriptionArn"]
            result = sns.get_subscription_attributes(SubscriptionArn=arn)
            assert result["Attributes"] == {
                "SubscriptionArn": arn, "TopicArn": topic, "Endpoint": local_url,
                "Protocol": "https", "Owner": "000000000000",
                "PendingConfirmation": "false", "ConfirmationWasAuthenticated": "false", "RawMessageDelivery": "false",
            }
            assert result["ResponseMetadata"]["HTTPStatusCode"] == 200
            sns.unsubscribe(SubscriptionArn=arn)
            assert_absent(arn)
            keep = sns.subscribe(TopicArn=topic, Protocol="https", Endpoint=local_url)["SubscriptionArn"]
        finally:
            sns.delete_topic(TopicArn=topic)
        assert_absent(keep)
        assert calls == [
            ("CreateTopic", 200), ("Subscribe", 200), ("GetSubscriptionAttributes", 200),
            ("Unsubscribe", 200), ("GetSubscriptionAttributes", 404), ("Subscribe", 200),
            ("DeleteTopic", 200), ("GetSubscriptionAttributes", 404),
        ]
        print("SNS Boto3: exact retained read-back, unsubscribe/topic-delete NotFound, loopback only")
    finally:
        sns.close()


def run():
    from check import running

    with running(authenticated=True) as (endpoint, token):
        check_sdk(endpoint, token)


if __name__ == "__main__":
    run()
