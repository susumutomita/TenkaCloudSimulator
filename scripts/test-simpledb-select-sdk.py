"""Opt-in pinned SDK acceptance against a compiled, local simulator; no cloud or profiles."""
import argparse
from contextlib import closing
import importlib.util
from pathlib import Path
from tempfile import TemporaryDirectory

DOMAIN = "select-native-sdk"
EXPR = "select * from `select-native-sdk` where `shade-name` = 'red' limit 1"


def wire():
    path = Path(__file__).resolve().parents[1] / "test/factory/testdata/simpledb/sdk_wire.py"
    spec = importlib.util.spec_from_file_location("simpledb_wire", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def expect_error(client, error, code, **params):
    try:
        client.select(**params)
    except error as exc:
        assert exc.response["Error"]["Code"] == code, exc.response
    else:
        raise AssertionError(f"expected {code}")


def exercise(client, error):
    client.create_domain(DomainName=DOMAIN)
    for name, shades in [("A", ["red", "ochre"]), ("B", ["red"]), ("C", ["blue"])]:
        client.put_attributes(DomainName=DOMAIN, ItemName=name,
                              Attributes=[{"Name": "shade-name", "Value": s} for s in shades])
    first = client.select(SelectExpression=EXPR, ConsistentRead=True)
    assert [item["Name"] for item in first["Items"]] == ["A"]
    assert sorted(attr["Value"] for attr in first["Items"][0]["Attributes"]) == ["ochre", "red"]
    token = first["NextToken"]
    second = client.select(SelectExpression=EXPR, ConsistentRead=True, NextToken=token)
    assert [item["Name"] for item in second["Items"]] == ["B"] and "NextToken" not in second
    expect_error(client, error, "InvalidNextToken", SelectExpression=EXPR, NextToken=token)
    expect_error(client, error, "InvalidQueryExpression", SelectExpression=f"select count(*) from `{DOMAIN}`")
    return token


def after_restart(client, error, token):
    expect_error(client, error, "InvalidNextToken", SelectExpression=EXPR, NextToken=token, ConsistentRead=True)
    expr = f"select itemName() from `{DOMAIN}` where itemName() >= 'B' order by itemName() desc"
    items = client.select(SelectExpression=expr)["Items"]
    assert [item["Name"] for item in items] == ["C", "B"]
    assert not any(item.get("Attributes") for item in items)
    client.delete_domain(DomainName=DOMAIN)
    expect_error(client, error, "NoSuchDomain", SelectExpression=EXPR)


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("Run without Python optimization")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    binary = parser.parse_args().binary.resolve()
    m = wire()
    boto, config, error, _, _ = m.preflight()
    report = {}
    with TemporaryDirectory(prefix="simpledb-select-sdk-") as directory:
        state_db = Path(directory) / "local.db"
        with m.running(binary, state_db, report) as (endpoint, auth):
            with closing(m.client_for(boto, config, endpoint, auth)) as client:
                token = exercise(client, error)
        with m.running(binary, state_db, report) as (endpoint, auth):
            with closing(m.client_for(boto, config, endpoint, auth)) as client:
                after_restart(client, error, token)
    if report.get("cleanup_errors"):
        raise RuntimeError(report["cleanup_errors"])
    print("PASS: bounded Select over pinned SDK, scoped paging, restart persistence and cleanup")
