# Native SimpleDB Select (bounded)

Use the simulator's authenticated local SimpleDB Query endpoint with an AWS SDK. For example, after saving items with a `color` attribute in `my-domain`:

```python
page = client.select(SelectExpression="select * from `my-domain` where color = 'red' limit 20")
```

A match on any value of `color` returns the item **once**, with *all* its attributes and values. To return only item names, use `select itemName() from ...`. Domain, attribute and value comparisons are case-sensitive. `sdb:Select` is evaluated against the parsed domain ARN in the authenticated server's configured account and region **on every page**. `DomainName` is not a separate parameter.

## Accepted expression

```text
select ( * | itemName() ) from domain
  [ where attribute = 'value' | where itemName() ( = | < | <= | > | >= ) 'value' ]
  [ order by itemName() [ asc | desc ] ]
  [ limit 1..2500 ]
```

Grammar words are accepted in either case. Item ordering is lexicographic, not numeric. Explicit ordering is supported only with an `itemName()` predicate. This simulator traverses unordered requests by ascending item name; do not depend on that for AWS. The default result limit is 100. Backtick-quote identifiers with punctuation or reserved names; double an interior backtick. Single- and double-quoted values use doubled delimiters. Local expressions must fit in 8 KiB. Attribute projections, `count(*)`, compound predicates, attribute ordering, and the rest of the AWS Select language are not implemented. Unsupported expressions or sorts return errors, never fabricated results.

## Paging and local boundaries

Always follow `NextToken` until absent, **including after an empty page**. Resend *the same exact expression* and `ConsistentRead` setting. The token is signed and bound to the query, account, region and running store. Restarting invalidates tokens, not the data. Each page uses a committed SQLite snapshot with either value of `ConsistentRead`; there is no simulated replica lag or cross-page snapshot. After concurrent writes, restart the query if a stable full listing matters.

The native reader stops after at most 128 consumed candidates, after a 1 MiB raw-scan budget is reached, or before adding an item that would exceed the XML response budget (below 1 MB including the envelope). A single item's encoded XML that cannot fit yields the local `ItemTooLargeForSelect` error; retry with `itemName()` and retrieve needed attributes by name with `GetAttributes`. The query deadline is five seconds. No external SQL or AWS call is made.

The [console](simpledb-console.md) runs the same operation for item names, optionally filtered by an exact attribute value. The local API reference offers the read-only **SimpleDB Select (read-only item names)** form example on `POST /`. Set its service header to `sdb` and substitute your domain. Compare the AWS [Select API](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_Select.html), [syntax and limits](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/UsingSelect.html), [quoting](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/QuotingRulesSelect.html), and [sort rules](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SortingDataSelect.html).

## Optional local integration acceptance

Build the standalone simulator from the exact checkout and install the pinned
SimpleDB SDK dependencies in `scripts/requirements-test.txt` (boto3/botocore and
their dependencies). Set `VERIFIED_SHA256` to the independently verified digest
of that exact binary, then run the focused Linux acceptance:

```sh
python test/e2e/simpledb_select_sessions.py \
  --revision "$(git rev-parse HEAD)" \
  --binary dist/tenkacloud-simulator \
  --binary-sha256 "$VERIFIED_SHA256" \
  --report /tmp/simpledb-select-evidence.json
```

The test refuses a dirty tracked checkout or a digest mismatch. It starts two
temporary loopback
simulators and checks that SDK writes, individually authorized selections,
and the form preset served by the API reference agree on saved data. It also
checks a nonterminal empty page, permission reevaluation on continuation,
isolation across process/SQLite/token authorities, and persistence across a
restart with the old token, cookie and cursor disallowed. No real AWS
credentials or remote runtime are used. This is not a test of the Scalar Send
button or Cloudscape rendering, and it is not an additional CI-wide E2E gate.
