# SimpleDB per-item limit

A SimpleDB item can hold at most 256 distinct attribute name/value pairs. The
limit applies to the resulting set, not the size of one input request.
Duplicate name/value pairs do not use another slot. If a write would grow
the item beyond the limit, native Query returns NumberItemAttributesExceeded
with HTTP 409 and keeps the entire item unchanged. A conditional check and
the count check run in the same SQLite write transaction as the update.

Earlier local data may already exceed this limit. The SDK can still read it
and replace existing values without growing the item. Replacing a multivalue
attribute with a single value can bring it under the limit. No migration
silently removes existing pairs. DeleteAttributes is not supported; deleting
a domain still explicitly removes its items. An older item with more than
256 distinct attribute names cannot be shrunk in place with these operations.

See [Amazon SimpleDB PutAttributes](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_PutAttributes.html)
and [API error codes](https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/APIError.html).
