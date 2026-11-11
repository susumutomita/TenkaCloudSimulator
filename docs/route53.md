# Route53 public hosted zones

## Contract

Eight REST/XML operations use `/2013-04-01/`: CreateHostedZone, GetHostedZone,
ListHostedZones, DeleteHostedZone, ChangeResourceRecordSets (POST),
ListResourceRecordSets, GetChange and TestDNSAnswer. Both native XML and
persisted records use one provider-owned SQLite database. No external DNS or
cloud connection is made. There is no standalone DNS server or public delegation.

Only public zones with at least two ASCII DNS labels are admitted. Labels are
lowercased, a trailing dot is canonicalized, and labels are 1..63 bytes with a
253-byte unqualified name limit. Wildcards, underscores, escapes and Unicode
names are rejected. This is deliberately narrower than AWS. Different zones
may have the same name, but CallerReference reuse returns HostedZoneAlreadyExists
and never creates a second zone, including when name/comment differ.

Each zone receives immutable apex NS/SOA RRsets naming `ns.simulator.invalid.`.
They are local exercise records, not usable resolvers. AWS supplies four name
servers and asynchronous propagation; this simulator supplies one and commits
immediately with INSYNC. Persisted change receipts survive zone deletion.

Changes support simple A/AAAA/CNAME/TXT/MX only. TTL is required (0..2147483647).
A/AAAA addresses are type-checked/canonicalized; scoped/mapped IPv6 is rejected.
TXT is one quoted printable-ASCII string of at most 255 bytes, without escaping.
MX is a 0..65535 preference and DNS name. CNAME has one target, cannot be apex,
and cannot coexist with other types. A batch is validated as a whole, then
records and receipt commit in one transaction. DELETE must match the entire
canonical RRset, including TTL and sorted values. Unknown actions/fields,
weighted/geographic/failover/multivalue policies, aliases, health checks and
mutable NS/SOA are rejected, never ignored.

Limits: 100 zones/database, 1000 RRsets/zone (including default NS/SOA),
100 changes/batch, 100 values/RRset, 1024 bytes/value, 1 MiB XML transport,
12 XML nesting levels and the newest 10000 persisted change receipts/database. At capacity, the oldest
receipt is evicted within the same mutation transaction. GetChange returns
NoSuchChange for evicted IDs. This deliberate retention deviation preserves
cleanup availability; a failed mutation also rolls back receipt eviction.
This is a bounded exercise database, not production DNS storage.

ListHostedZones returns at most 100 zones in stable ID order; NextMarker names
the first ID in the following page. An unknown/deleted/other-database marker is
rejected. ListResourceRecordSets uses reversed DNS-label ordering and numeric
DNS type ordering, with name/type start bounds and at most 100 results. These
are live pages, not snapshot cursors: mutation between pages may change results.
Name/type starts are standard search bounds, not authority-bearing tokens, and
are reusable in another zone after that zone is independently authorized.

TestDNSAnswer reads an atomic zone snapshot. Exact simple records are returned;
missing names return NXDOMAIN, existing names/empty nonterminals with no requested
type return NOERROR with no data. In-zone CNAME chains are followed up to 16
nodes; a cycle/depth overflow returns SERVFAIL. External CNAME targets return an
explicit unsupported error instead of inventing DNS answers. Private zones, VPC
associations, delegation, recursion, DNSSEC, resolver EDNS options and real
propagation are unsupported.

## Authority and composition

Integration interface: `NewRoute53Store(db *sql.DB)` and
`store.ServeHTTP(w, r, authorize func(action, resource string) error)`.
Gateway must capture **one immutable caller policy** before dispatch and use it
for every callback. Existing server token middleware must remain in front.
The callback is mandatory; a nil callback fails closed. Identity selection is
not authentication or team isolation. Hosts allocate separate SQLite databases,
processes and token authorities per team.

CreateHostedZone and ListHostedZones require their `route53:` action on `*`.
CreateHostedZone additionally requires `ec2:DescribeVpcs` on `*`, as the official
CreateHostedZone reference states for the request, not just its private-zone
paragraph. TestDNSAnswer also uses `*`. Zone operations use
`arn:aws:route53:::hostedzone/ID`; GetChange uses `arn:aws:route53:::change/ID`.
All mutation authorization completes before persistence. The store does not
implement IAM conditions; the existing strict identity-policy parser remains
responsible for rejecting unsupported policy features.

Optional SigV4-shaped requests must use dummy TCSIM access keys and
`us-east-1/route53/aws4_request`; signatures are not verified as AWS credentials.
Session tokens and conflicting service selectors are rejected. No runtime
AWS SDK dependency is added. SDK clients use the simulator endpoint and its
X-Simulator-Token header; these requests never contact AWS or external DNS.

## References

- [CreateHostedZone](https://docs.aws.amazon.com/Route53/latest/APIReference/API_CreateHostedZone.html): public defaults, CallerReference and ec2 prerequisite.
- [ListHostedZones](https://docs.aws.amazon.com/Route53/latest/APIReference/API_ListHostedZones.html): first-ID NextMarker, page size.
- [ChangeResourceRecordSets](https://docs.aws.amazon.com/Route53/latest/APIReference/API_ChangeResourceRecordSets.html): transaction and exact DELETE contract.
- [TestDNSAnswer](https://docs.aws.amazon.com/Route53/latest/APIReference/API_TestDNSAnswer.html): public-zone test operation.
- [Route53 IAM actions/resources](https://docs.aws.amazon.com/service-authorization/latest/reference/list_route53.html): wildcard TestDNSAnswer versus zone/change ARNs.
- [ResourceRecordSet](https://docs.aws.amazon.com/Route53/latest/APIReference/API_ResourceRecordSet.html): type/TTL/routing fields.
