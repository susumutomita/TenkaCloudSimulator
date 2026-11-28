# SimpleDB console

After signing in to the simulator, choose **SimpleDB** from **Services**.
It uses the existing local session and native Query API in `us-east-1`.
No AWS account or outbound connection is used.

1. Open a domain from the paged list, enter its name directly, or create one.
   Opening by name does not require `sdb:ListDomains`.
2. Enter a known item name and choose **Read attributes**. An unused name has
   no saved attributes.
3. Enter an attribute name and its new value. Choose **Attribute is absent** to
   set it only if it is missing. Choose **Attribute equals expected value** to
   replace a single value only if it matches. A blank value means an empty
   string, not absence.
4. Choose **Save attribute**. This is one native conditional `PutAttributes`,
   not a browser-side read followed by an unconditional write. A stale condition
   or a read-only identity leaves the item unchanged. The console reads the
   current attributes again after the write is acknowledged. A write that
   would exceed 256 name/value pairs is rejected without changes. Use the native
   SDK to read or repair an existing oversized item.
5. To delete a single-valued attribute, use **Delete attribute** on its row and
   **Confirm deletion**. Cancel sends nothing. The server checks the value you
   saw, then removes only that value. Deleting the last attribute removes the
   item. Refresh after a stale comparison. Use the SDK for multivalue records.
6. The result is announced and focused when the initiating control still owns
   focus. A refusal keeps the editor visible but locked until you read again.

## Find matching items

In an open domain, choose **Run query** to list item names, or enter an
attribute and its exact value before running. A blank value matches an empty
string. Each button press loads **one page of up to 25 names**. Use **Load next
result page** when available, even if this page found no matches. Changing the
filter or domain drops the old results and cursor. After a local editor write,
run the query again to refresh. Choose a result to fill **Item name**;
**Read attributes** still needs `sdb:GetAttributes` separately. The query
does not enable editing or deletion on its own.

The existing authorization checks for `sdb:ListDomains` (on `*`) and
`sdb:CreateDomain`, `sdb:Select` (on every results page), `sdb:GetAttributes`, `sdb:PutAttributes`, and, for deletion,
`sdb:DeleteAttributes` (on the selected
domain) apply unchanged. The console does not grant permissions. If a write is
not acknowledged, reload the item before retrying.

This console supports single-attribute conditional replacement or removal.
To use a separate attribute for the condition, use the native SDK. Arbitrary Select expressions,
batch writes, domain metadata, and whole-domain deletion are not offered here.
Domain cleanup uses native `DeleteDomain`, which also removes its items.


## Create a practice domain in the API reference

In the shared Query `POST /` form, choose **SimpleDB CreateDomain (writes local
state)**. Set `X-Simulator-Service: sdb`, clear `X-Amz-Target`, and use
`us-east-1` or leave the Region header unset. Replace `DomainName` with a
disposable name in your local environment. **Send** requires
`sdb:CreateDomain` on that domain ARN and writes to the same SQLite state as the
local Console and SDK. Sending the same name again preserves the existing data.
For cleanup, use native SDK `DeleteDomain` on your practice domain only.
See [AWS CreateDomain](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_CreateDomain.html).
[AWS does not offer a SimpleDB Management Console workflow](https://aws.amazon.com/blogs/database/export-amazon-simpledb-domain-data-to-amazon-s3/);
this is the simulator's local API reference.

## Read one item in the API reference

In **API reference**, open the Query `POST /` form and select the
**SimpleDB GetAttributes (read-only item values)** example. Set
`X-Simulator-Service: sdb`, clear `X-Amz-Target`, and replace the example domain
and item with the names you used in the console. **Send** reads the same saved
values using your local sign-in with `sdb:GetAttributes` on the domain. The form
does not create or change data. This is the local subset of
[AWS GetAttributes](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_GetAttributes.html).

## Verify shared local state

The opt-in cross-surface check mounts the served Cloudscape HTML and JavaScript
in jsdom and uses the exact served API reference examples and pinned official
SDK against disposable loopback simulators. It verifies both directions:
SDK values appear in the Console, and Console conditional writes and deletes
appear in SDK and API reference reads. Two teams use different processes,
database files and login tokens for the same domain/item names. The check
also verifies a denied write, signed-session refusal, persistence after
restart and cleanup observed by all three entry points.

Use a clean checkout, an independently verified simulator binary from that
revision, the pinned SimpleDB Python SDK packages in `scripts/requirements-test.txt`,
and approved local Node v24.22.3 and jsdom 30.0.1 installations:

```sh
python3 test/e2e/simpledb_crosssurface.py \
  --revision "$(git rev-parse HEAD)" \
  --binary dist/tenkacloud-simulator --binary-sha256 "$VERIFIED_SHA256" \
  --node /absolute/path/to/node --jsdom /absolute/path/to/node_modules/jsdom
```

This runs offline with no cloud account, external endpoint, or installed
browser required. It verifies Console DOM controls and real HTTP replies, not
browser rendering, layout, or clicks in the Scalar API reference.

## Browser checks for the served API examples

Build the simulator with `make build`. In a local test environment with the
pinned Playwright and Chromium from `scripts/requirements-test.txt`, set
`SIMULATOR_BINARY` to the absolute path of the build, `SIMULATOR_BINARY_SHA256`
to its verified SHA-256 and `CHROMIUM_EXECUTABLE` to the Chromium executable.
Run each fixture against its own temporary simulator.

```sh
python3 test/e2e/simpledb_scalar_reference_live.py
python3 test/e2e/simpledb_shipped_examples_live.py
```

The first fixture sends ListDomains, DomainMetadata and GetAttributes. The
second sends CreateDomain, Select and DeleteAttributes. Both use the examples
served on the shared AWS endpoint page and check the requests sent by the
browser. The second fixture verifies that deleting the selected attribute
leaves the other attribute and a second item intact. Both clean up their
domains.

## Write and clean up a practice domain in the API reference

After CreateDomain, choose **SimpleDB PutAttributes (writes one practice
attribute)** in Query `POST /`. Use your disposable domain and item, set
`X-Simulator-Service: sdb`, clear `X-Amz-Target`, and use `us-east-1` or omit
the Region header. Send requires `sdb:PutAttributes` on the domain ARN.
`Attribute.1.Replace=true` replaces only that attribute's values; other
attributes and items remain. This example is unconditional. Read the saved
values with GetAttributes; use the console or native SDK for conditional writes.
See [AWS PutAttributes](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_PutAttributes.html).

When finished, choose **SimpleDB DeleteDomain (deletes whole practice domain)**.
Check the name carefully: Send removes the domain and all its items and attributes.
It requires `sdb:DeleteDomain` on that domain ARN and the same routing headers.
Use only your disposable domain; verify its absence with ListDomains.
See [AWS DeleteDomain](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_DeleteDomain.html).
These are local native examples, not a claim of full AWS or Terraform coverage.
