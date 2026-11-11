"""Route53 native SDK acceptance against authenticated local CLI processes.

Exercises persisted API behavior and isolation without external DNS or AWS.
"""
import json
import tempfile
import urllib.request
import urllib.error
from pathlib import Path
from check import running, boto3, Config, ClientError
from route53_openapi import validate_route53


def client(endpoint, token):
    c = boto3.client("route53", endpoint_url=endpoint, region_name="us-east-1",
        aws_access_key_id="TCSIMLOCAL", aws_secret_access_key="dummy",
        config=Config(retries={"max_attempts": 0}))
    c.meta.events.register("before-sign.route53", lambda request, **kwargs:
        request.headers.add_header("X-Simulator-Token", token))
    def local_only(request, **kwargs):
        assert request.url.startswith(endpoint + "/2013-04-01/"), "SDK escaped owned Route53 endpoint"
    c.meta.events.register("before-send.route53", local_only)
    return c


def error(code, call):
    try:
        call()
    except ClientError as exc:
        assert exc.response["Error"]["Code"] == code, exc.response
    else:
        raise AssertionError(f"Expected {code}")


def control(endpoint, token, path, body):
    request = urllib.request.Request(endpoint + path, data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "X-Simulator-Token": token},
        method="POST" if path.endswith("/access") else "PUT")
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)

with tempfile.TemporaryDirectory(prefix="route53-sdk-") as directory:
 with running(db=Path(directory)/"first.db",authenticated=True) as (first, token), running(db=Path(directory)/"second.db",authenticated=True) as (second, token2):
    with urllib.request.urlopen(urllib.request.Request(first+"/_simulator/docs/openapi.json",headers={"X-Simulator-Token":token}),timeout=5) as response:
        validate_route53(json.load(response))
    c, other = client(first, token), client(second, token2)
    created = c.create_hosted_zone(Name="sdk.example.test", CallerReference="route53-sdk")
    zone = created["HostedZone"]["Id"]
    assert created["HostedZone"]["ResourceRecordSetCount"] == 2
    assert c.get_hosted_zone(Id=zone)["HostedZone"]["Name"] == "sdk.example.test."
    assert c.list_hosted_zones()["HostedZones"][0]["Id"] == zone
    error("HostedZoneAlreadyExists", lambda: c.create_hosted_zone(Name="different.test", CallerReference="route53-sdk"))
    error("NoSuchHostedZone", lambda: other.get_hosted_zone(Id=zone))
    error("InvalidInput", lambda: c.create_hosted_zone(Name="K.example.test", CallerReference="unicode-rejected"))
    records = [
        {"Name": "a.sdk.example.test", "Type": "A", "TTL": 60, "ResourceRecords": [{"Value": "192.0.2.5"}]},
        {"Name": "a.sdk.example.test", "Type": "AAAA", "TTL": 60, "ResourceRecords": [{"Value": "2001:db8::5"}]},
        {"Name": "alias.sdk.example.test", "Type": "CNAME", "TTL": 60, "ResourceRecords": [{"Value": "a.sdk.example.test"}]},
        {"Name": "k.sdk.example.test", "Type": "A", "TTL": 60, "ResourceRecords": [{"Value": "192.0.2.11"}]},
    ]
    changed = c.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={"Changes": [
        {"Action": "CREATE", "ResourceRecordSet": record} for record in records]})
    assert c.get_change(Id=changed["ChangeInfo"]["Id"])["ChangeInfo"]["Status"] == "INSYNC"
    for action in ("UPSERT", "DELETE"):
        unicode_record = {**records[3], "Name": "K.sdk.example.test"}
        error("InvalidInput", lambda: c.change_resource_record_sets(HostedZoneId=zone,
            ChangeBatch={"Changes":[{"Action":action,"ResourceRecordSet":unicode_record}]}))
        assert c.test_dns_answer(HostedZoneId=zone,RecordName="k.sdk.example.test",RecordType="A")["RecordData"] == ["192.0.2.11"]
    assert c.test_dns_answer(HostedZoneId=zone, RecordName="alias.sdk.example.test", RecordType="A")["RecordData"] == ["192.0.2.5"]
    assert c.test_dns_answer(HostedZoneId=zone, RecordName="a.sdk.example.test", RecordType="AAAA")["RecordData"] == ["2001:db8::5"]
    assert c.test_dns_answer(HostedZoneId=zone, RecordName="missing.sdk.example.test", RecordType="A")["ResponseCode"] == "NXDOMAIN"
    invalid = {"Name": "bad.sdk.example.test", "Type": "A", "TTL": 60, "ResourceRecords": [{"Value": "not-an-address"}]}
    update = {**records[0], "ResourceRecords": [{"Value": "192.0.2.6"}]}
    error("InvalidInput", lambda: c.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={"Changes": [
        {"Action": "UPSERT", "ResourceRecordSet": update}, {"Action": "CREATE", "ResourceRecordSet": invalid}]}))
    assert c.test_dns_answer(HostedZoneId=zone, RecordName="a.sdk.example.test", RecordType="A")["RecordData"] == ["192.0.2.5"]
    error("HostedZoneNotEmpty", lambda: c.delete_hosted_zone(Id=zone))
    page = c.list_resource_record_sets(HostedZoneId=zone, MaxItems="1")
    assert page["IsTruncated"] and page["NextRecordType"] == "SOA"
    c.change_resource_record_sets(HostedZoneId=zone, ChangeBatch={"Changes": [
        {"Action": "DELETE", "ResourceRecordSet": record} for record in records]})
    c.delete_hosted_zone(Id=zone)
    error("NoSuchHostedZone", lambda: c.get_hosted_zone(Id=zone))
    assert other.list_hosted_zones()["HostedZones"] == []
    # A separate saved zone remains for actual CLI close/reopen acceptance.
    saved = c.create_hosted_zone(Name="restart.test", CallerReference="restart")["HostedZone"]["Id"]
    rr = {"Name":"www.restart.test", "Type":"A", "TTL":60, "ResourceRecords":[{"Value":"192.0.2.9"}]}
    c.change_resource_record_sets(HostedZoneId=saved, ChangeBatch={"Changes":[{"Action":"CREATE","ResourceRecordSet":rr}]})
    policy = {"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"route53:GetHostedZone","Resource":"arn:aws:route53:::"+saved.lstrip("/")}]}
    control(first, token, "/_simulator/v1/iam/principals/dns-reader/policy", policy)
    participant=control(first, token, "/_simulator/v1/access", {"Principal":"dns-reader"})["AccessToken"]
    limited=client(first, participant)
    assert limited.get_hosted_zone(Id=saved)["HostedZone"]["Id"] == saved
    error("AccessDenied", lambda: limited.list_hosted_zones())
    error("AccessDenied", lambda: limited.test_dns_answer(HostedZoneId=saved,RecordName="www.restart.test",RecordType="A"))
    policy["Statement"].append({"Effect":"Deny","Action":"route53:GetHostedZone","Resource":"*"})
    control(first, token, "/_simulator/v1/iam/principals/dns-reader/policy", policy)
    error("AccessDenied", lambda: limited.get_hosted_zone(Id=saved))
    try:
        urllib.request.urlopen(urllib.request.Request(second+"/_simulator/health",headers={"X-Simulator-Token":token}),timeout=5)
    except urllib.error.HTTPError as exc:
        assert exc.code == 401
    else:
        raise AssertionError("Token crossed team authorities")
    limited.close()
    c.close()
    other.close()
 with running(db=Path(directory)/"first.db",authenticated=True) as (restarted,new_token):
    c=client(restarted,new_token)
    assert c.test_dns_answer(HostedZoneId=saved,RecordName="www.restart.test",RecordType="A")["RecordData"] == ["192.0.2.9"]
    c.change_resource_record_sets(HostedZoneId=saved,ChangeBatch={"Changes":[{"Action":"DELETE","ResourceRecordSet":rr}]})
    c.delete_hosted_zone(Id=saved)
    assert c.list_hosted_zones()["HostedZones"] == []
    c.close()
 print(json.dumps({"route53NativeSDK": True, "atomicSavedStateAnswers": True,
    "twoAuthenticatedProcesses": True, "actualCLIReopen":True, "participantIAM":True,
    "teardown": "DELETED", "realAWS": False}))
