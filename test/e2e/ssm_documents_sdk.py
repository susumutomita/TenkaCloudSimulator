#!/usr/bin/env python3
"""Exercise the integrated native document endpoint on a disposable local server.

Requires an explicitly supplied local host token. Does not start a server or
execute any stored document. boto3 is the existing project test dependency.
"""
import argparse
import json
import uuid
from urllib.parse import urlparse

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--token", required=True)
    args = parser.parse_args()
    url = urlparse(args.endpoint)
    if url.scheme != "http" or url.hostname not in ("127.0.0.1", "::1") or url.username or url.password or url.query or url.fragment:
        parser.error("endpoint must be numeric loopback HTTP without credentials/query")
    client = boto3.client("ssm", endpoint_url=args.endpoint, region_name="us-east-1",
                          aws_access_key_id="TCSIM_DOCUMENT_TEST", aws_secret_access_key="dummy",
                          config=Config(retries={"max_attempts": 0}, connect_timeout=3, read_timeout=5))

    def authorize(request, **_):
        request.headers["X-Simulator-Token"] = args.token

    client.meta.events.register("before-send.ssm", authorize)
    name = "local-document-" + uuid.uuid4().hex
    content = {"schemaVersion": "2.2", "mainSteps": [{"action": "aws:runShellScript", "name": "example", "inputs": {"runCommand": ["echo definition-only"]}}]}
    created = False
    try:
        first = client.create_document(Name=name, Content=json.dumps(content), DocumentType="Command", DocumentFormat="JSON")
        created = True
        assert first["DocumentDescription"]["DocumentVersion"] == "1"
        assert json.loads(client.get_document(Name=name)["Content"]) == content
        content["mainSteps"][0]["inputs"]["runCommand"] = ["echo still-not-executed"]
        second = client.update_document(Name=name, Content=json.dumps(content), DocumentVersion="1")
        assert second["DocumentDescription"]["DocumentVersion"] == "2"
        assert client.describe_document(Name=name)["Document"]["DefaultVersion"] == "1"
        assert client.get_document(Name=name)["DocumentVersion"] == "1"
        assert json.loads(client.get_document(Name=name, DocumentVersion="$LATEST")["Content"]) == content
        found = False
        token = None
        while True:
            page = client.list_documents(MaxResults=1, **({"NextToken": token} if token else {}))
            found |= any(item["Name"] == name for item in page["DocumentIdentifiers"])
            token = page.get("NextToken")
            if not token:
                break
        assert found
        client.delete_document(Name=name)
        created = False
        try:
            client.get_document(Name=name)
        except ClientError as exc:
            assert exc.response["Error"]["Code"] == "InvalidDocument"
        else:
            raise AssertionError("deleted document remained accessible")
        print("SSM document native management passed; no command was executed")
    finally:
        if created:
            client.delete_document(Name=name)


if __name__ == "__main__":
    main()
