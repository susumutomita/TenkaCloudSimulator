"""Exercise seven native AWS Support operations with two private local teams."""

import json
import os
import tempfile
import urllib.request
from collections import Counter
from contextlib import ExitStack, closing
from pathlib import Path
from urllib.parse import urlsplit


def neutral_environment():
    if any(key.startswith(("AWS_", "BOTO_")) for key in os.environ):
        raise RuntimeError("Run this fixture on a neutral host without ambient AWS configuration.")


def main():
    neutral_environment()
    from check import Config, ClientError, boto3, running

    calls, sends, replies = Counter(), Counter(), Counter()

    def client(endpoint, token):
        origin = urlsplit(endpoint)
        assert origin.scheme == "http" and origin.hostname == "127.0.0.1" and origin.port
        sdk = boto3.client(
            "support", region_name="us-east-1", endpoint_url=endpoint,
            aws_access_key_id="TCSIMLOCAL", aws_secret_access_key="dummy",
            config=Config(retries={"max_attempts": 0}, proxies={}),
        )
        sdk.meta.events.register("before-sign.support", lambda request, **_: request.headers.add_header("X-Simulator-Token", token))

        def before_send(request, **_):
            target = urlsplit(request.url)
            assert target.scheme == "http" and target.netloc == origin.netloc
            assert target.path == "/" and not target.query and not target.fragment
            name = request.headers["X-Amz-Target"].decode().removeprefix("AWSSupport_20130415.")
            assert request.method == "POST" and name in sdk.meta.service_model.operation_names
            sends[name] += 1

        sdk.meta.events.register("before-send.support", before_send)
        sdk.meta.events.register("before-call.support", lambda model, **_: calls.update([model.name]))
        sdk.meta.events.register("after-call.support", lambda model, **_: replies.update([model.name]))
        return sdk

    def expect_error(code, function, **kwargs):
        try:
            function(**kwargs)
        except ClientError as exc:
            assert exc.response["Error"]["Code"] == code, (function.__name__, exc.response["Error"]["Code"])
        else:
            raise AssertionError(f"{function.__name__} should have returned {code}")

    def control(endpoint, token, method, path, value):
        request = urllib.request.Request(
            endpoint + path, data=json.dumps(value).encode(), method=method,
            headers={"Content-Type": "application/json", "X-Simulator-Token": token},
        )
        assert urlsplit(request.full_url).netloc == urlsplit(endpoint).netloc
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(request, timeout=5) as response:
            return json.loads(response.read())

    def details(sdk, case_id, resolved=False):
        cases = sdk.describe_cases(caseIdList=[case_id], includeResolvedCases=resolved)["cases"]
        assert len(cases) == 1 and cases[0]["caseId"] == case_id
        return cases[0]

    files = [{"fileName": "sample.bin", "data": bytes([0, 129, 255])}]
    expected_ops = {
        "CreateCase", "DescribeCases", "DescribeCommunications", "AddCommunicationToCase",
        "ResolveCase", "AddAttachmentsToSet", "DescribeAttachment",
    }
    with tempfile.TemporaryDirectory(prefix="support-cases-") as directory:
        root = Path(directory)
        db_a, db_b = root / "team-a.db", root / "team-b.db"
        with ExitStack() as scopes:
            a, operator = scopes.enter_context(running(db_a, authenticated=True))
            b, peer_operator = scopes.enter_context(running(db_b, authenticated=True))
            owner = scopes.enter_context(closing(client(a, operator)))
            peer = scopes.enter_context(closing(client(b, peer_operator)))
            peer_id = peer.create_case(subject="Same subject", communicationBody="Peer's initial note")["caseId"]
            staged = owner.add_attachments_to_set(attachments=files)
            set_id = staged["attachmentSetId"]
            second_set = owner.add_attachments_to_set(attachmentSetId=set_id, attachments=[{"fileName": "empty.txt", "data": b""}])
            assert second_set["attachmentSetId"] == set_id and second_set["expiryTime"] == staged["expiryTime"]
            expect_error("AttachmentSetSizeLimitExceeded", owner.add_attachments_to_set, attachmentSetId=set_id, attachments=files * 2)
            case_id = owner.create_case(subject="Same subject", communicationBody="Owner's initial note", attachmentSetId=set_id)["caseId"]
            initial = details(owner, case_id)["recentCommunications"]["communications"][0]
            assert initial["body"] == "Owner's initial note"
            attached = initial["attachmentSet"]
            assert [f["fileName"] for f in attached] == ["sample.bin", "empty.txt"]
            assert all(set(f) == {"attachmentId", "fileName"} for f in attached)
            file_id = attached[0]["attachmentId"]
            assert owner.describe_attachment(attachmentId=file_id)["attachment"] == files[0]
            expect_error("AttachmentIdNotFound", peer.describe_attachment, attachmentId=file_id)
            expect_error("CaseIdNotFound", peer.describe_cases, caseIdList=[case_id])
            expect_error("AttachmentSetIdNotFound", owner.add_communication_to_case, caseId=case_id, communicationBody="Cannot commit", attachmentSetId="missing")
            assert len(owner.describe_communications(caseId=case_id)["communications"]) == 1

            for i in range(11):
                assert owner.add_communication_to_case(caseId=case_id, communicationBody=f"Note {i}")["result"]
            recent = details(owner, case_id)["recentCommunications"]
            assert [m["body"] for m in recent["communications"]] == [f"Note {n}" for n in range(10, 5, -1)]
            earlier = owner.describe_communications(caseId=case_id, nextToken=recent["nextToken"])["communications"]
            assert [m["body"] for m in earlier] == [f"Note {n}" for n in range(5, -1, -1)] + ["Owner's initial note"]
            all_pages = list(owner.get_paginator("describe_communications").paginate(caseId=case_id, PaginationConfig={"PageSize": 10}))
            assert [len(page["communications"]) for page in all_pages] == [10, 2]
            other_ids = [owner.create_case(subject=f"Practice {n}", communicationBody="Local note")["caseId"] for n in range(10)]
            pages = list(owner.get_paginator("describe_cases").paginate(includeCommunications=False, PaginationConfig={"PageSize": 10}))
            assert [len(page["cases"]) for page in pages] == [10, 1]
            assert set(other_ids + [case_id]) == {c["caseId"] for page in pages for c in page["cases"]}
            assert all("recentCommunications" not in c for page in pages for c in page["cases"])
            expect_error("ValidationException", owner.describe_cases, includeResolvedCases=True, nextToken=pages[0]["nextToken"])
            expect_error("ValidationException", owner.describe_cases, nextToken=all_pages[0]["nextToken"])

            policy = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": "support:DescribeCases", "Resource": "*"}]}
            control(a, operator, "PUT", "/_simulator/v1/iam/principals/support-reader/policy", policy)
            reader_token = control(a, operator, "POST", "/_simulator/v1/access", {"Principal": "support-reader"})["AccessToken"]
            reader = scopes.enter_context(closing(client(a, reader_token)))
            assert details(reader, case_id)["status"] == "opened"
            expect_error("AccessDeniedException", reader.resolve_case, caseId=case_id)
            expect_error("AccessDeniedException", reader.describe_attachment, attachmentId=file_id)
            policy["Statement"].append({"Effect": "Deny", "Action": "support:DescribeCases", "Resource": "*"})
            control(a, operator, "PUT", "/_simulator/v1/iam/principals/support-reader/policy", policy)
            expect_error("AccessDeniedException", reader.describe_cases, caseIdList=[case_id])
            expect_error("CaseIdNotFound", owner.describe_communications, caseId=peer_id)
            assert details(peer, peer_id)["recentCommunications"]["communications"][0]["body"] == "Peer's initial note"
            for other_id in other_ids:
                assert owner.resolve_case(caseId=other_id)["finalCaseStatus"] == "resolved"

        with ExitStack() as scopes:
            a, op_a = scopes.enter_context(running(db_a, authenticated=True))
            b, op_b = scopes.enter_context(running(db_b, authenticated=True))
            owner = scopes.enter_context(closing(client(a, op_a)))
            peer = scopes.enter_context(closing(client(b, op_b)))
            assert details(owner, case_id)["recentCommunications"]["communications"][0]["body"] == "Note 10"
            assert owner.describe_attachment(attachmentId=file_id)["attachment"] == files[0]
            expect_error("ValidationException", owner.describe_communications, caseId=case_id, nextToken=recent["nextToken"])
            resolved = owner.resolve_case(caseId=case_id)
            assert (resolved["initialCaseStatus"], resolved["finalCaseStatus"]) == ("opened", "resolved")
            assert owner.describe_cases()["cases"] == []
            assert details(owner, case_id, resolved=True)["status"] == "resolved"
            expect_error("ValidationException", owner.add_communication_to_case, caseId=case_id, communicationBody="Closed case")
            assert [c["caseId"] for c in peer.describe_cases()["cases"]] == [peer_id]
            assert peer.resolve_case(caseId=peer_id)["finalCaseStatus"] == "resolved"
            assert peer.describe_cases()["cases"] == []
        assert db_a.is_file() and db_b.is_file()
    assert not root.exists() and calls == sends == replies and set(calls) == expected_ops
    print(json.dumps({"operations": sorted(calls), "requests": sum(sends.values()), "responses": sum(replies.values()),
                      "twoTeams": True, "reopenedBothDatabases": True, "ownedFilesRemoved": True}))


if __name__ == "__main__":
    main()
