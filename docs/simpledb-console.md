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


## Read one item in the API reference

In **API reference**, open the Query `POST /` form and select the
**SimpleDB GetAttributes (read-only item values)** example. Set
`X-Simulator-Service: sdb`, clear `X-Amz-Target`, and replace the example domain
and item with the names you used in the console. **Send** reads the same saved
values using your local sign-in with `sdb:GetAttributes` on the domain. The form
does not create or change data. This is the local subset of
[AWS GetAttributes](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_GetAttributes.html).
