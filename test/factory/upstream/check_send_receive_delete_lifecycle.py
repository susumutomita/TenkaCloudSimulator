"""send-receive-delete-lifecycle."""
from __future__ import annotations

import hashlib
import time

from behavioral_harness import tags

CHECK = "send-receive-delete-lifecycle"
GROUP = "e2e"
SERVICES = ["sqs"]


@tags("axis:happy-path", "cap:c2", "concern:data-plane", "concern:representation", "dp:api", "plane:data")
def run(ctx) -> None:
    """SendMessage -> ReceiveMessage -> DeleteMessage round trip preserves body/MD5 and
    the message is gone afterwards (no longer receivable)."""
    sqs = ctx.client("sqs")
    url = sqs.create_queue(QueueName=ctx.unique_name("sim2real-sqs-lifecycle-"))["QueueUrl"]
    ctx.on_cleanup(lambda: sqs.delete_queue(QueueUrl=url))

    body = "hello-sqs-lifecycle-12345"
    send_resp = sqs.send_message(QueueUrl=url, MessageBody=body)
    expected_md5 = hashlib.md5(body.encode("utf-8")).hexdigest()
    assert send_resp["MD5OfMessageBody"] == expected_md5, (
        f"MD5OfMessageBody mismatch: expected {expected_md5}, got {send_resp['MD5OfMessageBody']}"
    )

    msg = _poll_receive_one(sqs, url, timeout_s=20.0)
    assert msg["Body"] == body, f"Body mismatch: expected {body!r}, got {msg['Body']!r}"
    assert msg["MD5OfBody"] == expected_md5, (
        f"MD5OfBody mismatch: expected {expected_md5}, got {msg['MD5OfBody']}"
    )
    assert msg["MessageId"] == send_resp["MessageId"], (
        f"MessageId mismatch: expected {send_resp['MessageId']}, got {msg['MessageId']}"
    )

    sqs.delete_message(QueueUrl=url, ReceiptHandle=msg["ReceiptHandle"])

    # after delete, the message must never be receivable again
    deadline = time.monotonic() + 10.0
    while time.monotonic() < deadline:
        resp = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, WaitTimeSeconds=2)
        assert not resp.get("Messages"), "deleted message was redelivered"


def _poll_receive_one(sqs, url, timeout_s=20.0):
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        resp = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=1, WaitTimeSeconds=5)
        messages = resp.get("Messages", [])
        if messages:
            return messages[0]
    raise AssertionError(f"no message received within {timeout_s}s")


if __name__ == "__main__":
    from fidelity_tests_runner import main
    main(run)
