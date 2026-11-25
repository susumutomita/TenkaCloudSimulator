"""Opt-in native SimpleDB DeleteAttributes contract for a compiled local simulator."""
import argparse
from contextlib import closing
import importlib.util
from pathlib import Path
from tempfile import TemporaryDirectory

FIXTURE = Path(__file__).resolve().parents[1] / "test/factory/testdata/simpledb/sdk_wire.py"


def wire():
    spec = importlib.util.spec_from_file_location("simpledb_wire", FIXTURE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def exercise(client, error):
    domain = "sdk-deletion"
    item = dict(DomainName=domain, ItemName="row")
    client.create_domain(DomainName=domain)
    client.put_attributes(**item, Attributes=[
        {"Name": "color", "Value": v} for v in ("", "blue", "red")
    ] + [{"Name": "version", "Value": "1"}])

    def state():
        values = client.get_attributes(**item, ConsistentRead=True).get("Attributes", [])
        return sorted((a["Name"], a["Value"]) for a in values)

    original = [("color", ""), ("color", "blue"), ("color", "red"), ("version", "1")]
    assert state() == original
    try:
        client.delete_attributes(**item, Expected={"Name": "version", "Value": "stale"})
    except error as exc:
        assert (exc.response["ResponseMetadata"]["HTTPStatusCode"], exc.response["Error"]["Code"]) == (409, "ConditionalCheckFailed")
    else:
        raise AssertionError("stale condition accepted")
    assert state() == original
    client.delete_attributes(**item, Attributes=[{"Name": "color", "Value": ""}],
                             Expected={"Name": "version", "Value": "1"})
    assert state() == [("color", "blue"), ("color", "red"), ("version", "1")]
    # The pinned Botocore Attribute shape requires Value even though the AWS
    # wire reference permits name-only deletion (covered by the Go wire test).
    client.delete_attributes(**item, Attributes=[
        {"Name": "color", "Value": value} for value in ("blue", "red")
    ])
    assert state() == [("version", "1")]
    client.delete_attributes(**item, Attributes=[{"Name": "version", "Value": "1"}])
    assert state() == []
    client.delete_attributes(**item)
    assert client.domain_metadata(DomainName=domain)["ItemCount"] == 0
    # Keep a second item and the domain so a fresh CLI can verify both a
    # committed deletion and a surviving value before cleanup.
    client.put_attributes(DomainName=domain, ItemName="survivor",
                          Attributes=[{"Name": "version", "Value": "2"}])


def after_restart(client):
    domain = "sdk-deletion"
    assert client.domain_metadata(DomainName=domain)["ItemCount"] == 1
    assert not client.get_attributes(DomainName=domain, ItemName="row", ConsistentRead=True).get("Attributes", [])
    assert client.get_attributes(DomainName=domain, ItemName="survivor", ConsistentRead=True)["Attributes"] == [{"Name":"version","Value":"2"}]
    client.delete_attributes(DomainName=domain, ItemName="survivor")
    assert client.domain_metadata(DomainName=domain)["ItemCount"] == 0
    client.delete_domain(DomainName=domain)
    assert domain not in client.list_domains().get("DomainNames", [])


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("Run this acceptance test without Python optimization")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    args = parser.parse_args()
    m = wire()
    boto, config, error, _, _ = m.preflight()
    report = {}
    with TemporaryDirectory(prefix="simpledb-delete-sdk-") as directory:
        state_db = Path(directory) / "local.db"
        for step in (lambda c: exercise(c, error), after_restart):
            with m.running(args.binary.resolve(), state_db, report) as (endpoint, token):
                with closing(m.client_for(boto, config, endpoint, token)) as client:
                    step(client)
    if report.get("cleanup_errors"):
        raise RuntimeError(report["cleanup_errors"])
    print("PASS: DeleteAttributes via pinned SDK, process restart, persistent state and cleanup")
